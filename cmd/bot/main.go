// Command bot is the memories wiki Slack bot (MVP, read-only).
//
// Flow: Slack app_mention → verify signature → strip the @mention → search the
// memories wiki (public GitHub, no token) → reply in-thread as the bot.
//
// Reuses the A0 gateway skeleton (slackevents / slackclient / audit). The tool
// layer is internal/wiki (search_wiki, wiki_status). No LLM yet — a mention is
// treated as a search query; "현황"-type asks return the status summary.
package main

import (
	"context"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"github.com/WoodrowDy/memories-wiki-bot/internal/audit"
	"github.com/WoodrowDy/memories-wiki-bot/internal/config"
	"github.com/WoodrowDy/memories-wiki-bot/internal/render"
	"github.com/WoodrowDy/memories-wiki-bot/internal/slackclient"
	"github.com/WoodrowDy/memories-wiki-bot/internal/slackevents"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

type app struct {
	signingSecret string
	slack         *slackclient.Client
	wiki          *wiki.Client
}

func main() {
	c := config.FromEnv()
	a := &app{
		signingSecret: os.Getenv("SLACK_SIGNING_SECRET"),
		slack:         slackclient.New(os.Getenv("SLACK_BOT_TOKEN"), 8*time.Second),
		wiki:          wiki.New(wiki.Config{Owner: c.Owner, Repo: c.Repo, Branch: c.Branch, Token: c.Token}),
	}
	lambda.Start(a.handle)
}

var mentionRe = regexp.MustCompile(`<@[A-Za-z0-9]+>`)

func (a *app) handle(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
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
		return ack(http.StatusOK, "") // avoid duplicate replies (MVP: no queue)
	}
	ev, ok := slackevents.ParseAppMention(body)
	if !ok {
		return ack(http.StatusOK, "")
	}

	query := strings.TrimSpace(mentionRe.ReplaceAllString(ev.Text, ""))
	threadTS := ev.ThreadTS
	if threadTS == "" {
		threadTS = ev.TS
	}

	msg, tool := a.answer(ctx, query)
	if err := a.slack.PostThread(ev.Channel, threadTS, msg); err != nil {
		log.Printf("slack post: %v", err)
	}
	audit.Log(audit.Entry{
		UserID: ev.User, ChannelID: ev.Channel, Tool: tool, Allowed: true, Success: true,
	})
	return ack(http.StatusOK, "")
}

func (a *app) answer(ctx context.Context, query string) (msg, tool string) {
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

func ack(code int, body string) (events.APIGatewayV2HTTPResponse, error) {
	return events.APIGatewayV2HTTPResponse{StatusCode: code, Body: body}, nil
}
