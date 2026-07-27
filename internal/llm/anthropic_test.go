package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteSendsAuthAndVersionHeaders(t *testing.T) {
	var gotKey, gotVersion, gotType string
	var body Request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotVersion = r.Header.Get("Anthropic-Version")
		gotType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"안녕"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":3}}`)
	}))
	defer srv.Close()

	c := New("sk-test", 5*time.Second).WithBaseURL(srv.URL)
	res, err := c.Complete(context.Background(), Request{
		Model: "claude-haiku-4-5", MaxTokens: 100, System: "규칙",
		Messages: []Message{UserText("안녕?")},
		Tools:    []Tool{{Name: "search_wiki", Description: "d", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "sk-test" || gotVersion != apiVersion || !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("headers: key=%q version=%q type=%q", gotKey, gotVersion, gotType)
	}
	if body.Model != "claude-haiku-4-5" || body.System != "규칙" || len(body.Tools) != 1 {
		t.Errorf("request body not marshaled as expected: %+v", body)
	}
	if res.JoinText() != "안녕" || res.Usage.InputTokens != 9 {
		t.Errorf("response = %+v", res)
	}
}

func TestToolUseRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[
			{"type":"text","text":"찾아볼게"},
			{"type":"tool_use","id":"toolu_1","name":"search_wiki","input":{"query":"동시성"}}
		],"stop_reason":"tool_use"}`)
	}))
	defer srv.Close()

	c := New("k", 5*time.Second).WithBaseURL(srv.URL)
	res, err := c.Complete(context.Background(), Request{Messages: []Message{UserText("동시성?")}})
	if err != nil {
		t.Fatal(err)
	}
	uses := res.ToolUses()
	if len(uses) != 1 || uses[0].Name != "search_wiki" || uses[0].ID != "toolu_1" {
		t.Fatalf("tool_use not parsed: %+v", res.Content)
	}
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(uses[0].Input, &in); err != nil || in.Query != "동시성" {
		t.Errorf("tool input = %s (%v)", uses[0].Input, err)
	}
	if res.JoinText() != "찾아볼게" {
		t.Errorf("text blocks = %q", res.JoinText())
	}

	// A tool_result must marshal with tool_use_id and content, not text.
	raw, _ := json.Marshal(ToolResult("toolu_1", `{"count":1}`, false))
	if !strings.Contains(string(raw), `"tool_use_id":"toolu_1"`) || strings.Contains(string(raw), `"text"`) {
		t.Errorf("tool_result marshaled wrong: %s", raw)
	}
}

func TestCompleteRetriesOnOverload(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	c := New("k", 5*time.Second).WithBaseURL(srv.URL)
	res, err := c.Complete(context.Background(), Request{Messages: []Message{UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || res.JoinText() != "ok" {
		t.Errorf("calls = %d, text = %q", calls, res.JoinText())
	}
}

func TestCompleteDoesNotRetryClientErrors(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"model: unknown"}}`)
	}))
	defer srv.Close()

	c := New("k", 5*time.Second).WithBaseURL(srv.URL)
	_, err := c.Complete(context.Background(), Request{Messages: []Message{UserText("hi")}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("a 400 was retried %d times; it should fail fast", calls)
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("error lost the API detail: %v", err)
	}
}

func TestDisabledWithoutAPIKey(t *testing.T) {
	c := New("", time.Second)
	if c.Enabled() {
		t.Fatal("client with no key should report disabled")
	}
	if _, err := c.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("expected an error so the caller falls back to keyword search")
	}
}
