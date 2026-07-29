package slackclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Client is a tiny Slack Web API client — only the two calls this bot makes:
// chat.postMessage for the answer, assistant.threads.setStatus for the "쓰는 중"
// 표시 that fills the wait before it.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

const defaultBaseURL = "https://slack.com/api"

func New(token string, timeout time.Duration) *Client {
	return &Client{token: token, baseURL: defaultBaseURL, http: &http.Client{Timeout: timeout}}
}

// WithBaseURL points the client at a different endpoint (used by tests).
func (c *Client) WithBaseURL(u string) *Client {
	c.baseURL = strings.TrimRight(u, "/")
	return c
}

// statusTimeout bounds the setStatus call.
//
// 이건 답이 아니라 답을 기다리는 동안의 표시다. 슬랙이 굼뜨면 표시를 포기하는 게 맞다 —
// 표시를 기다리느라 답이 늦어지면 이걸 붙인 이유가 통째로 뒤집힌다. 답을 올리는
// PostThread에 이런 상한이 없는 건 그 반대라서다. 답은 늦더라도 나가야 한다.
//
// var인 건 테스트가 줄여 쓰기 때문이다.
var statusTimeout = 2 * time.Second

type apiResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// call posts body as JSON to one Slack Web API method.
//
// 슬랙은 HTTP 200에 ok:false를 실어 보낸다. 상태 코드만 보면 실패가 성공으로 보인다.
func (c *Client) call(ctx context.Context, method string, body any) error {
	if c.token == "" {
		return errors.New("SLACK_BOT_TOKEN not set")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	var r apiResp
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		// 에러 낱말을 그대로 실어 보낸다. 이 줄이 CloudWatch에 남아야 표시가 안 뜰 때
		// 스코프 문제인지 채널 문제인지 로그만 보고 갈린다.
		return errors.New("slack: " + r.Error)
	}
	return nil
}

type postMessageReq struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

// PostThread posts text into a thread (or the channel if threadTS is empty).
func (c *Client) PostThread(channel, threadTS, text string) error {
	return c.call(context.Background(), "chat.postMessage",
		postMessageReq{Channel: channel, Text: text, ThreadTS: threadTS})
}

type setStatusReq struct {
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts"`
	Status    string `json:"status"`
}

// SetStatus shows a loading indicator on the thread until the bot replies.
//
// 슬랙에는 봇이 보낼 수 있는 "입력 중…" 표시가 없다. user_typing은 봇이 *받는*
// 이벤트고 그나마 레거시 RTM 전용이다. 이 메서드가 그 자리를 대신하는 공식 창구다.
//
// chat:write로 호출된다(2026-03-05 슬랙 변경 전에는 assistant:write만 받았다).
// 봇이 이미 가진 스코프라 앱 재설치도, 토큰 교체도 없다.
//
// 표시는 저절로 사라진다 — 봇이 답을 올리거나, 2분이 지나거나. 그래서 worker가
// 중간에 죽어도 "쓰는 중"이 스레드에 영영 박혀 있지는 않다.
//
// 반드시 답보다 *먼저* 불러야 한다. 답을 올린 뒤에 부르면 지워줄 답이 이미 지나갔으니
// 2분을 꽉 채우고서야 사라진다.
func (c *Client) SetStatus(channel, threadTS, status string) error {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	return c.call(ctx, "assistant.threads.setStatus",
		setStatusReq{ChannelID: channel, ThreadTS: threadTS, Status: status})
}
