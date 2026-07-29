// Command bot is the memories wiki Slack bot.
//
// One binary, two roles, picked from the shape of the incoming event:
//
//	gateway (API Gateway)  verify signature → enqueue → 200 within ~50ms
//	worker  (SQS)          LLM tool-calling loop → post the answer in-thread
//
// The split exists because Slack wants an ack in 3 seconds and a model turn
// takes longer than that. Without the queue, Slack retries and the user gets
// the same answer three times.
//
// The LLM is optional: with no ANTHROPIC_API_KEY (or if the API fails) the bot
// falls back to the 1차 keyword search rather than going silent. Writing is
// optional in the same way: with no GITHUB_WRITE_TOKEN the bot answers
// questions and never offers to open a PR.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/WoodrowDy/memories-wiki-bot/internal/audit"
	"github.com/WoodrowDy/memories-wiki-bot/internal/brain"
	"github.com/WoodrowDy/memories-wiki-bot/internal/config"
	"github.com/WoodrowDy/memories-wiki-bot/internal/jobs"
	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/queue"
	"github.com/WoodrowDy/memories-wiki-bot/internal/render"
	"github.com/WoodrowDy/memories-wiki-bot/internal/slackclient"
	"github.com/WoodrowDy/memories-wiki-bot/internal/slackevents"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wikiwrite"
)

type app struct {
	signingSecret string
	slack         *slackclient.Client
	wiki          *wiki.Client
	queue         *queue.Client
	brain         *brain.Brain
	brainOn       bool
	queueOn       bool
	writeOn       bool
}

func main() {
	c := config.FromEnv()
	w := wiki.New(wiki.Config{Owner: c.Owner, Repo: c.Repo, Branch: c.Branch, Token: c.Token})
	model := llm.New(os.Getenv("ANTHROPIC_API_KEY"), 60*time.Second)
	queueURL := os.Getenv("JOBS_QUEUE_URL")

	// The write client is built here and nowhere else. It holds the only copy of
	// the write token, and the read client above never sees it.
	writer := wikiwrite.New(wikiwrite.Config{
		Owner: c.Owner, Repo: c.Repo, Base: c.Branch, Token: c.WriteToken,
	})

	b := brain.New(model, w, os.Getenv("LLM_MODEL"), c.Owner, c.Repo)
	if writer.Enabled() {
		b = b.WithWriter(writer)
	}

	a := &app{
		signingSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		slack:         slackclient.New(os.Getenv("SLACK_BOT_TOKEN"), 8*time.Second),
		wiki:          w,
		queue:         queue.New(queueURL),
		brain:         b,
		brainOn:       model.Enabled(),
		queueOn:       queueURL != "",
		writeOn:       writer.Enabled(),
	}
	log.Printf("boot: brain=%v queue=%v write=%v model=%s",
		a.brainOn, a.queueOn, a.writeOn, envOr("LLM_MODEL", llm.DefaultModel))
	lambda.Start(a.route)
}

// route dispatches on the event shape rather than a ROLE env var, so the two
// functions can never drift out of sync with their configuration.
func (a *app) route(ctx context.Context, raw json.RawMessage) (any, error) {
	if ev, ok := asSQS(raw); ok {
		return nil, a.work(ctx, ev)
	}
	var req events.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		log.Printf("route: unrecognized event: %v", err)
		return ack(http.StatusBadRequest, "")
	}
	return a.gateway(ctx, req)
}

func asSQS(raw json.RawMessage) (events.SQSEvent, bool) {
	var probe struct {
		Records []struct {
			EventSource string `json:"eventSource"`
		} `json:"Records"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Records) == 0 {
		return events.SQSEvent{}, false
	}
	if probe.Records[0].EventSource != "aws:sqs" {
		return events.SQSEvent{}, false
	}
	var ev events.SQSEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return events.SQSEvent{}, false
	}
	return ev, true
}

// ---- gateway: Slack → queue ----

var mentionRe = regexp.MustCompile(`<@[A-Za-z0-9]+>`)

func (a *app) gateway(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := req.Body
	if req.IsBase64Encoded {
		if dec, err := base64.StdEncoding.DecodeString(body); err == nil {
			body = string(dec)
		}
	}

	if !slackevents.Verify(req.Headers, body, a.signingSecret, time.Now()) {
		return ack(http.StatusUnauthorized, "invalid signature")
	}
	if challenge, ok := slackevents.Challenge(body); ok {
		return ack(http.StatusOK, challenge)
	}
	if slackevents.IsRetry(req.Headers) {
		return ack(http.StatusOK, "") // we already have the job; don't answer twice
	}
	ev, ok := slackevents.ParseAppMention(body)
	if !ok {
		return ack(http.StatusOK, "")
	}

	threadTS := ev.ThreadTS
	if threadTS == "" {
		threadTS = ev.TS
	}
	job := jobs.Job{
		Channel:  ev.Channel,
		ThreadTS: threadTS,
		User:     ev.User,
		Text:     strings.TrimSpace(mentionRe.ReplaceAllString(ev.Text, "")),
	}

	if a.queueOn {
		if payload, err := job.Encode(); err != nil {
			log.Printf("gateway: encode job: %v", err)
		} else if err := a.queue.Send(ctx, payload); err != nil {
			log.Printf("gateway: enqueue failed (%v) — answering inline", err)
		} else {
			return ack(http.StatusOK, "")
		}
	}

	// No queue, or the queue refused: answer here. Slower, but never silent.
	a.reply(ctx, job)
	return ack(http.StatusOK, "")
}

// ---- worker: queue → Slack ----

func (a *app) work(ctx context.Context, ev events.SQSEvent) error {
	for _, rec := range ev.Records {
		job, err := jobs.Decode([]byte(rec.Body))
		if err != nil {
			log.Printf("worker: bad job %s: %v", rec.MessageId, err)
			continue
		}
		a.reply(ctx, job)
	}
	// Always nil: a returned error makes SQS redeliver, and a redelivered job
	// posts the same answer again. Failures are reported into the thread instead.
	return nil
}

// thinkingStatus is what the thread shows while the model runs. 슬랙이 봇 이름 뒤에
// 붙여 회색으로 띄운다 — 디엠의 "입력 중…"이 있는 그 자리다.
const thinkingStatus = "답을 쓰는 중…"

func (a *app) reply(ctx context.Context, job jobs.Job) {
	start := time.Now()

	// 멘션과 답 사이가 5~20초다. 그동안 슬랙으로 나가는 게 하나도 없으면 그가 보기엔
	// 봇이 죽은 거랑 구별이 안 된다. 답을 올리는 순간 슬랙이 이 표시를 알아서 지운다.
	//
	// answer보다 먼저, 그리고 동기로 부른다. 뒤에 부르면 지워줄 답이 이미 지나가서
	// 표시가 2분을 꽉 채우고, 고루틴으로 띄우면 그 순서가 안 지켜진다.
	//
	// 실패는 로그만 남긴다. 진행 표시 하나 때문에 답을 못 주면 본말이 뒤집힌다.
	if err := a.slack.SetStatus(job.Channel, job.ThreadTS, thinkingStatus); err != nil {
		log.Printf("slack status: %v", err)
	}

	msg, tool := a.answer(ctx, job.Text)

	success := true
	if err := a.slack.PostThread(job.Channel, job.ThreadTS, msg); err != nil {
		log.Printf("slack post: %v", err)
		success = false
	}
	audit.Log(audit.Entry{
		UserID: job.User, ChannelID: job.Channel, Tool: tool,
		Allowed: true, Success: success, LatencyMs: time.Since(start).Milliseconds(),
	})
}

func (a *app) answer(ctx context.Context, query string) (msg, tool string) {
	if a.brainOn && query != "" {
		ans, err := a.brain.Answer(ctx, query)
		if err == nil {
			log.Printf("brain: turns=%d in=%d out=%d tools=%v",
				ans.Turns, ans.InputTokens, ans.OutputTokens, ans.ToolsUsed)
			return ans.Text, "brain(" + strings.Join(dedupe(ans.ToolsUsed), ",") + ")"
		}
		log.Printf("brain: %v — falling back to keyword search", err)
	}
	return a.keywordAnswer(ctx, query)
}

// keywordAnswer is the 1차 behaviour, kept as the safety net for when the LLM
// is unconfigured, rate-limited, or down.
func (a *app) keywordAnswer(ctx context.Context, query string) (msg, tool string) {
	if query == "" || isStatusQuery(query) {
		rep, err := a.wiki.Status(ctx)
		if err != nil {
			return "위키를 읽지 못했어요: " + err.Error(), "wiki_status"
		}
		return render.Status(rep), "wiki_status"
	}
	matches, err := a.wiki.Search(ctx, query)
	if err != nil {
		return "위키를 읽지 못했어요: " + err.Error(), "search_wiki"
	}
	return render.SearchResults(query, matches), "search_wiki"
}

func isStatusQuery(q string) bool {
	q = strings.ToLower(q)
	for _, kw := range []string{"현황", "status", "overview"} {
		if strings.Contains(q, kw) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func ack(code int, body string) (events.APIGatewayV2HTTPResponse, error) {
	return events.APIGatewayV2HTTPResponse{StatusCode: code, Body: body}, nil
}
