package slackclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Client is a tiny Slack Web API client (only what A0 needs: chat.postMessage).
type Client struct {
	token string
	http  *http.Client
}

func New(token string, timeout time.Duration) *Client {
	return &Client{token: token, http: &http.Client{Timeout: timeout}}
}

type postMessageReq struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

type postMessageResp struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// PostThread posts text into a thread (or the channel if threadTS is empty).
func (c *Client) PostThread(channel, threadTS, text string) error {
	if c.token == "" {
		return errors.New("SLACK_BOT_TOKEN not set")
	}
	payload, err := json.Marshal(postMessageReq{Channel: channel, Text: text, ThreadTS: threadTS})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(payload))
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

	var pr postMessageResp
	if err := json.NewDecoder(res.Body).Decode(&pr); err != nil {
		return err
	}
	if !pr.OK {
		return errors.New("slack: " + pr.Error)
	}
	return nil
}
