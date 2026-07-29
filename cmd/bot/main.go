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
	"strconv"
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
	// 고르기만 한다. 바이트를 받아오는 건 워커 일이다 — 여기서 파일을 내려받으면
	// 슬랙의 3초 ack를 놓치고, 놓치면 같은 이벤트가 세 번 온다.
	if len(ev.Files) > 0 {
		cand := make([]jobs.File, 0, len(ev.Files))
		for _, f := range ev.Files {
			cand = append(cand, jobs.File{Name: f.Name, URL: f.URLPrivate, Size: f.Size})
		}
		job.File, job.Ignored = jobs.Pick(cand)
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

	// 바이트는 여기서 받는다. 게이트웨이는 3초 안에 ack해야 해서 파일 하나 받아오는 데
	// 그 시간을 쓸 수 없고, 워커는 300초를 쥐고 있다.
	ask := brain.Ask{Text: job.Text}
	if job.File != nil {
		b, err := a.slack.Download(ctx, job.File.URL, jobs.MaxBytes)
		if err != nil {
			// 모델을 부르지 않고 여기서 끝낸다. 파일을 못 읽은 채로 넘기면 봇은 첨부가
			// 없었던 걸로 알고 답하고, 그는 자기 초안이 어디로 갔는지 모르게 된다.
			log.Printf("worker: download %s: %v", job.File.Name, err)
			if err := a.slack.PostThread(job.Channel, job.ThreadTS,
				"첨부한 `"+job.File.Name+"`을 못 읽었어요.\n"+err.Error()); err != nil {
				log.Printf("slack post: %v", err)
			}
			audit.Log(audit.Entry{
				UserID: job.User, ChannelID: job.Channel, Tool: "download",
				Allowed: true, Success: false, LatencyMs: time.Since(start).Milliseconds(),
			})
			return
		}
		ask.File = &brain.Attached{Name: job.File.Name, Content: string(b)}
	}

	msg, tool := a.answer(ctx, ask)
	msg += tail(job, ask.File)

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

// tail is what the worker says on its own, under the model's answer.
//
// 모델에게 안 맡기는 이유: 이 둘은 판단이 아니라 사실이고, 사실은 매번 똑같이 나와야
// 한다. 모델을 거치면 개수를 틀리거나 답이 길다고 빼먹는 날이 생기는데, 그러면 그가
// 던진 파일이 왜 무시됐는지는 영영 아무도 말해주지 않는다.
//
// PR이 안 열려도 들린다는 게 핵심이다. 초안만 던져 추천만 받는 자리에서도 `[[위키링크]]`가
// 몇 개인지는 알아야 하고, 그건 PR 본문만으로는 닿지 않는 자리다.
func tail(job jobs.Job, att *brain.Attached) string {
	var lines []string
	if len(job.Ignored) > 0 {
		lines = append(lines, "*안 읽은 첨부*")
		for _, s := range job.Ignored {
			lines = append(lines, "• "+s)
		}
	}
	if att != nil {
		if r := att.Check(); !r.OK() {
			lines = append(lines, "*옵시디언에서만 되는 문법이 "+strconv.Itoa(r.Total())+"개* — 고치지 않고 그대로 뒀어요")
			for _, s := range r.Lines() {
				lines = append(lines, "• "+s)
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(lines, "\n")
}

func (a *app) answer(ctx context.Context, ask brain.Ask) (msg, tool string) {
	// 파일만 던지고 아무 말도 안 붙였을 수 있다. 그때도 물어볼 게 있는 요청이다.
	if a.brainOn && (ask.Text != "" || ask.File != nil) {
		ans, err := a.brain.Run(ctx, ask)
		if err == nil {
			log.Printf("brain: turns=%d in=%d out=%d tools=%v",
				ans.Turns, ans.InputTokens, ans.OutputTokens, ans.ToolsUsed)
			return ans.Text, "brain(" + strings.Join(dedupe(ans.ToolsUsed), ",") + ")"
		}
		log.Printf("brain: %v — falling back to keyword search", err)
	}
	// 파일을 받아 놓고 여기까지 왔다면 자리 추천도 PR도 못 한다 — 둘 다 모델의 일이다.
	// 그래도 하나는 남는다: 같은 주제 노트가 이미 있나 찾아보는 것. 이 봇을 만든 첫
	// 번째 이유고, 검색은 모델 없이도 된다. 그가 슬랙에 친 말은 "이거 올려줘" 한 줄일
	// 수 있으니 검색어는 파일에서 뽑는다.
	//
	// 잠자코 위키 현황만 뱉으면 그는 초안이 접수된 줄 안다. 그래서 못 했다는 말이
	// 먼저 나온다.
	if ask.File != nil {
		msg, tool = a.keywordAnswer(ctx, ask.File.Topic())
		return "지금은 자리 추천을 못 해요 — 모델을 못 불렀어요. 대신 같은 주제 노트가 " +
			"있는지만 찾아봤어요. 잠시 뒤에 파일을 다시 올려주세요.\n\n" + msg, tool
	}
	return a.keywordAnswer(ctx, ask.Text)
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
