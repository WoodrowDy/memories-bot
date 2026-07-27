package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// fakeLLM replays a scripted sequence of responses and records what it was asked.
type fakeLLM struct {
	script []llm.Response
	seen   []llm.Request
	err    error
}

func (f *fakeLLM) Complete(_ context.Context, r llm.Request) (*llm.Response, error) {
	f.seen = append(f.seen, r)
	if f.err != nil {
		return nil, f.err
	}
	i := len(f.seen) - 1
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	res := f.script[i]
	return &res, nil
}

type fakeWiki struct {
	searched []string
	read     []string
	notes    map[string]wiki.Note
}

func (f *fakeWiki) Search(_ context.Context, q string) ([]wiki.Match, error) {
	f.searched = append(f.searched, q)
	return []wiki.Match{{
		Path: "topics/cs/concurrency.md", Title: "동시성", Status: "budding",
		Snippet: "고루틴과 채널", URL: "https://github.com/x/y/blob/main/topics/cs/concurrency.md",
	}}, nil
}

func (f *fakeWiki) List(_ context.Context, prefix string) ([]wiki.Match, error) {
	return []wiki.Match{{Path: prefix + "a.md", Title: "A"}}, nil
}

func (f *fakeWiki) ReadNote(_ context.Context, p string) (wiki.Note, error) {
	f.read = append(f.read, p)
	n, ok := f.notes[p]
	if !ok {
		return wiki.Note{}, errors.New("없는 노트: " + p)
	}
	return n, nil
}

func (f *fakeWiki) Status(context.Context) (wiki.StatusReport, error) {
	return wiki.StatusReport{Total: 12, TopicsByCat: map[string]int{"cs": 12}}, nil
}

func toolUse(id, name, args string) llm.Response {
	return llm.Response{
		StopReason: "tool_use",
		Content:    []llm.Block{{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(args)}},
	}
}

func finalText(s string) llm.Response {
	return llm.Response{
		StopReason: "end_turn",
		Content:    []llm.Block{{Type: "text", Text: s}},
		Usage:      llm.Usage{InputTokens: 100, OutputTokens: 20},
	}
}

func TestAnswerRunsToolThenReplies(t *testing.T) {
	f := &fakeLLM{script: []llm.Response{
		toolUse("t1", "search_wiki", `{"query":"동시성"}`),
		finalText("✅ *있어요* — 동시성"),
	}}
	w := &fakeWiki{}
	b := New(f, w, "test-model", "WoodrowDy", "memories")

	ans, err := b.Answer(context.Background(), "동시성 정리한 거 있어?")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "✅ *있어요* — 동시성" {
		t.Errorf("text = %q", ans.Text)
	}
	if ans.Turns != 2 {
		t.Errorf("turns = %d, want 2", ans.Turns)
	}
	if len(w.searched) != 1 || w.searched[0] != "동시성" {
		t.Errorf("search calls = %v", w.searched)
	}
	if len(ans.ToolsUsed) != 1 || ans.ToolsUsed[0] != "search_wiki" {
		t.Errorf("tools = %v", ans.ToolsUsed)
	}

	// The second call must carry the conversation forward: original question,
	// the assistant's tool_use, and our tool_result.
	second := f.seen[1]
	if len(second.Messages) != 3 {
		t.Fatalf("messages on turn 2 = %d, want 3", len(second.Messages))
	}
	res := second.Messages[2].Content[0]
	if res.Type != "tool_result" || res.ToolUseID != "t1" {
		t.Fatalf("bad tool_result block: %+v", res)
	}
	if !strings.Contains(res.Content, "topics/cs/concurrency.md") {
		t.Errorf("tool_result lost the search hit: %s", res.Content)
	}
}

func TestAnswerSurfacesToolErrorsToTheModel(t *testing.T) {
	f := &fakeLLM{script: []llm.Response{
		toolUse("t1", "read_note", `{"path":"topics/nope.md"}`),
		finalText("그 노트는 없어요."),
	}}
	b := New(f, &fakeWiki{notes: map[string]wiki.Note{}}, "m", "o", "r")

	ans, err := b.Answer(context.Background(), "nope 읽어줘")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "그 노트는 없어요." {
		t.Errorf("text = %q", ans.Text)
	}
	res := f.seen[1].Messages[2].Content[0]
	if !res.IsError {
		t.Error("tool failure should be marked is_error so the model can recover")
	}
}

func TestAnswerRejectsUnknownTool(t *testing.T) {
	f := &fakeLLM{script: []llm.Response{
		toolUse("t1", "delete_everything", `{}`),
		finalText("못 해요."),
	}}
	b := New(f, &fakeWiki{}, "m", "o", "r")

	if _, err := b.Answer(context.Background(), "다 지워줘"); err != nil {
		t.Fatal(err)
	}
	res := f.seen[1].Messages[2].Content[0]
	if !res.IsError || !strings.Contains(res.Content, "알 수 없는 툴") {
		t.Errorf("unknown tool should be refused in code: %+v", res)
	}
}

func TestLastTurnDropsToolsSoTheModelMustAnswer(t *testing.T) {
	// A model that never stops asking for tools.
	f := &fakeLLM{script: []llm.Response{toolUse("t", "wiki_status", `{}`)}}
	b := New(f, &fakeWiki{}, "m", "o", "r")

	_, err := b.Answer(context.Background(), "끝없이")
	if err == nil {
		t.Fatal("expected an error once the turn budget is spent")
	}
	if len(f.seen) != maxTurns {
		t.Fatalf("calls = %d, want %d", len(f.seen), maxTurns)
	}
	if f.seen[maxTurns-1].Tools != nil {
		t.Error("tools should be withheld on the final turn")
	}
	for i := 0; i < maxTurns-1; i++ {
		if len(f.seen[i].Tools) != 4 {
			t.Errorf("turn %d offered %d tools, want 4", i, len(f.seen[i].Tools))
		}
	}
}

func TestAnswerPropagatesLLMFailure(t *testing.T) {
	f := &fakeLLM{err: errors.New("overloaded")}
	b := New(f, &fakeWiki{}, "m", "o", "r")

	if _, err := b.Answer(context.Background(), "뭐든"); err == nil {
		t.Fatal("expected the API error to reach the caller so it can fall back")
	}
}

func TestSystemPromptNamesTheRepoAndBansDoubleAsterisks(t *testing.T) {
	b := New(&fakeLLM{}, &fakeWiki{}, "m", "WoodrowDy", "memories")
	p := b.systemPrompt()
	for _, want := range []string{"WoodrowDy/memories", "topics/", "지어내지 마", "별표 하나"} {
		if !strings.Contains(p, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestReadNoteTruncatesLongBodies(t *testing.T) {
	long := strings.Repeat("가", noteBodyLimit+500)
	w := &fakeWiki{notes: map[string]wiki.Note{
		"topics/cs/x.md": {Path: "topics/cs/x.md", Title: "X", Body: long},
	}}
	b := New(&fakeLLM{}, w, "m", "o", "r")

	out, err := b.runTool(context.Background(), "read_note", json.RawMessage(`{"path":"topics/cs/x.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got noteOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Error("oversized note should be flagged truncated")
	}
	if n := len([]rune(got.Body)); n != noteBodyLimit {
		t.Errorf("body = %d runes, want %d", n, noteBodyLimit)
	}
}
