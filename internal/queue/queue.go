// Package queue is a one-call SQS client: SendMessage over the AWS JSON
// protocol, signed with SigV4 from the Lambda environment credentials.
//
// This is the seam that makes the bot async. Slack demands a 200 within 3s, but
// an LLM turn takes 5–20s — so the gateway drops a job here and returns
// immediately, and the worker answers on its own schedule.
package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/WoodrowDy/memories-wiki-bot/internal/awssig"
)

type Client struct {
	queueURL string
	region   string
	http     *http.Client
}

func New(queueURL string) *Client {
	return &Client{
		queueURL: queueURL,
		region:   regionFromQueueURL(queueURL),
		http:     &http.Client{Timeout: 5 * time.Second},
	}
}

// regionFromQueueURL reads the region out of https://sqs.<region>.amazonaws.com/...
// and falls back to the region Lambda advertises.
func regionFromQueueURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		parts := strings.Split(u.Host, ".")
		if len(parts) >= 3 && parts[0] == "sqs" {
			return parts[1]
		}
	}
	return os.Getenv("AWS_REGION")
}

type sendMessageReq struct {
	QueueUrl    string `json:"QueueUrl"`
	MessageBody string `json:"MessageBody"`
}

// Send enqueues body. Returns an error the caller is expected to surface —
// a silently dropped job means a silently ignored user.
func (c *Client) Send(ctx context.Context, body []byte) error {
	if c.queueURL == "" {
		return fmt.Errorf("queue: JOBS_QUEUE_URL not set")
	}
	payload, err := json.Marshal(sendMessageReq{QueueUrl: c.queueURL, MessageBody: string(body)})
	if err != nil {
		return err
	}

	endpoint := "https://sqs." + c.region + ".amazonaws.com/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.SendMessage")

	if err := awssig.Sign(req, payload, "sqs", c.region, awssig.CredsFromEnv(), time.Now()); err != nil {
		return err
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("sqs SendMessage: %d %s", res.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}
