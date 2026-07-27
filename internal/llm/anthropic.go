// Package llm is a minimal Anthropic Messages API client — just enough for a
// tool-calling loop. Written against net/http rather than an SDK so the Lambda
// binary stays one static file with no transitive dependencies.
//
// https://platform.claude.com/docs/en/api/messages
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "https://api.anthropic.com/v1/messages"
	apiVersion     = "2023-06-01"

	// DefaultModel — Haiku is the right default here: the work is "read these
	// notes and answer in 5 lines", not deep reasoning. Swap via LLM_MODEL.
	DefaultModel = "claude-haiku-4-5"
)

// Block is one content block. The Messages API discriminates on Type, so the
// unused fields simply stay empty in both directions.
type Block struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"` // type=text

	ID    string          `json:"id,omitempty"`    // type=tool_use
	Name  string          `json:"name,omitempty"`  // type=tool_use
	Input json.RawMessage `json:"input,omitempty"` // type=tool_use

	ToolUseID string `json:"tool_use_id,omitempty"` // type=tool_result
	Content   string `json:"content,omitempty"`     // type=tool_result
	IsError   bool   `json:"is_error,omitempty"`    // type=tool_result
}

func Text(s string) Block { return Block{Type: "text", Text: s} }

func ToolResult(toolUseID, content string, isErr bool) Block {
	return Block{Type: "tool_result", ToolUseID: toolUseID, Content: content, IsError: isErr}
}

type Message struct {
	Role    string  `json:"role"` // "user" | "assistant"
	Content []Block `json:"content"`
}

func UserText(s string) Message {
	return Message{Role: "user", Content: []Block{Text(s)}}
}

// Tool is a tool definition handed to the model.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type Request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Response struct {
	ID         string  `json:"id"`
	Model      string  `json:"model"`
	Role       string  `json:"role"`
	Content    []Block `json:"content"`
	StopReason string  `json:"stop_reason"`
	Usage      Usage   `json:"usage"`
}

// ToolUses returns the tool_use blocks in the response, in order.
func (r *Response) ToolUses() []Block {
	var out []Block
	for _, b := range r.Content {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// JoinText concatenates the text blocks — the part a human actually reads.
func (r *Response) JoinText() string {
	var buf bytes.Buffer
	for _, b := range r.Content {
		if b.Type == "text" && b.Text != "" {
			if buf.Len() > 0 {
				buf.WriteString("\n")
			}
			buf.WriteString(b.Text)
		}
	}
	return buf.String()
}

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
	retries int
}

func New(apiKey string, timeout time.Duration) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: timeout},
		retries: 2,
	}
}

// WithBaseURL points the client at a different endpoint (used by tests).
func (c *Client) WithBaseURL(u string) *Client { c.baseURL = u; return c }

// Enabled reports whether an API key was configured. When false the caller is
// expected to fall back to the keyword search path rather than go silent.
func (c *Client) Enabled() bool { return c != nil && c.apiKey != "" }

type apiError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete sends one Messages request, retrying transient failures (429/5xx)
// with a short backoff.
func (c *Client) Complete(ctx context.Context, r Request) (*Response, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("llm: ANTHROPIC_API_KEY not set")
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 700 * time.Millisecond):
			}
		}
		resp, retryable, err := c.once(ctx, payload)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retryable {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, payload []byte) (*Response, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Anthropic-Version", apiVersion)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, true, err // network blips are worth one more try
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, true, err
	}
	if res.StatusCode != http.StatusOK {
		var ae apiError
		msg := string(body)
		if json.Unmarshal(body, &ae) == nil && ae.Error.Message != "" {
			msg = ae.Error.Type + ": " + ae.Error.Message
		}
		retryable := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		return nil, retryable, fmt.Errorf("anthropic %d — %s", res.StatusCode, msg)
	}

	var out Response
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, false, fmt.Errorf("anthropic: bad response: %w", err)
	}
	return &out, false, nil
}
