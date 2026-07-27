// Package brain is the natural-language layer (2단계): it hands a question to
// Claude with the wiki tools attached, runs the tool-calling loop, and returns
// the text to post in Slack.
//
// Design rules carried over from the proposal:
//   - the model never touches GitHub directly — it can only call the four tools
//     below, and each one validates its own arguments in code
//   - tools return structured JSON; the words are the model's job
//   - the loop is bounded (turns, tokens, note size) so one weird question can
//     never turn into an expensive runaway
package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

const (
	maxTurns      = 5    // model→tool round trips before we force a final answer
	maxTokens     = 1024 // a Slack message, not an essay
	noteBodyLimit = 6000 // runes of one note handed to the model
	listLimit     = 300  // notes returned by list_notes
)

// Wiki is the tool surface the brain is allowed to reach.
type Wiki interface {
	Search(ctx context.Context, query string) ([]wiki.Match, error)
	List(ctx context.Context, prefix string) ([]wiki.Match, error)
	ReadNote(ctx context.Context, path string) (wiki.Note, error)
	Status(ctx context.Context) (wiki.StatusReport, error)
}

// LLM is the model call. An interface so tests can run the loop without network.
type LLM interface {
	Complete(ctx context.Context, r llm.Request) (*llm.Response, error)
}

type Brain struct {
	llm   LLM
	wiki  Wiki
	model string
	owner string
	repo  string
}

func New(l LLM, w Wiki, model, owner, repo string) *Brain {
	if model == "" {
		model = llm.DefaultModel
	}
	return &Brain{llm: l, wiki: w, model: model, owner: owner, repo: repo}
}

// Answer is one completed reply plus what it cost.
type Answer struct {
	Text         string
	ToolsUsed    []string
	Turns        int
	InputTokens  int
	OutputTokens int
}

// Answer runs the tool-calling loop until the model stops asking for tools.
func (b *Brain) Answer(ctx context.Context, question string) (Answer, error) {
	var ans Answer
	messages := []llm.Message{llm.UserText(question)}

	for turn := 0; turn < maxTurns; turn++ {
		ans.Turns = turn + 1

		req := llm.Request{
			Model:     b.model,
			MaxTokens: maxTokens,
			System:    b.systemPrompt(),
			Messages:  messages,
			Tools:     toolDefs(),
		}
		// Last turn: take the tools away so the model has to write the answer
		// instead of asking for a sixth lookup.
		if turn == maxTurns-1 {
			req.Tools = nil
		}

		res, err := b.llm.Complete(ctx, req)
		if err != nil {
			return ans, err
		}
		ans.InputTokens += res.Usage.InputTokens
		ans.OutputTokens += res.Usage.OutputTokens

		uses := res.ToolUses()
		if len(uses) == 0 {
			ans.Text = strings.TrimSpace(res.JoinText())
			if ans.Text == "" {
				ans.Text = "음, 답을 만들지 못했어요. 다시 물어봐 주세요."
			}
			return ans, nil
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: res.Content})

		results := make([]llm.Block, 0, len(uses))
		for _, u := range uses {
			ans.ToolsUsed = append(ans.ToolsUsed, u.Name)
			out, err := b.runTool(ctx, u.Name, u.Input)
			if err != nil {
				results = append(results, llm.ToolResult(u.ID, err.Error(), true))
				continue
			}
			results = append(results, llm.ToolResult(u.ID, out, false))
		}
		messages = append(messages, llm.Message{Role: "user", Content: results})
	}

	return ans, fmt.Errorf("brain: %d턴 안에 답을 못 냈어요", maxTurns)
}

func (b *Brain) systemPrompt() string {
	return fmt.Sprintf(`너는 우드로의 개인 위키를 대신 뒤져주는 슬랙 봇이야.
위키는 GitHub의 %s/%s 저장소이고, 옵시디언으로 쓰는 마크다운 노트 모음이야.

폴더 구조:
- topics/<카테고리>/<주제>.md — 공부하고 정리한 지식 노트
- daily/ — 날짜별 기록
- personal/ — 개인 메모
- projects/ — 진행 중인 프로젝트
노트 앞머리(frontmatter)에 title, status(seedling/budding/evergreen 같은 성숙도), tags, aliases가 있을 수 있어.

일하는 방식:
1. 반드시 툴로 실제 노트를 확인한 다음에 답해. 기억이나 짐작으로 답하지 마.
2. 넓게 찾을 땐 search_wiki, 목록이 필요하면 list_notes, 내용을 인용하거나 요약하려면 read_note로 본문을 실제로 읽어.
3. 위키에 없으면 "없다"고 분명히 말해. 지어내지 마.
4. 위키 밖의 일반 지식을 덧붙일 땐 "(위키엔 없고 일반적으로는)"이라고 반드시 표시해서 섞이지 않게 해.

답 형식:
- 한국어. 슬랙 메시지니까 짧게, 보통 3~6줄.
- 슬랙 mrkdwn만 써: 굵게는 *별표 하나*(**두 개 아님**), 코드는 `+"`백틱`"+`, 목록은 •.
- 노트를 근거로 답했으면 경로와 링크를 같이 줘. 예: `+"`topics/cs/concurrency.md`"+` 와 URL.
- 인사말·사족 없이 바로 본론.`, b.owner, b.repo)
}

func toolDefs() []llm.Tool {
	return []llm.Tool{
		{
			Name: "search_wiki",
			Description: "위키 전체에서 키워드로 노트를 찾는다. 제목·별칭·태그·본문을 훑어 관련도 순으로 최대 5개를 돌려준다. " +
				"'X 정리한 거 있어?' 같은 질문의 출발점. 검색어는 조사를 뗀 핵심 명사가 잘 걸린다.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "찾을 키워드 (예: '동시성', 'kafka')",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name: "list_notes",
			Description: "노트 목록(경로·제목·status)을 본문 없이 훑는다. '무슨 주제들 정리해놨지?'처럼 " +
				"둘러보는 질문이나, 검색이 빈손일 때 실제로 뭐가 있는지 확인할 때 쓴다.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{
						"type":        "string",
						"description": "경로 앞부분으로 좁히기 (예: 'topics/', 'daily/'). 비우면 전체.",
					},
				},
			},
		},
		{
			Name: "read_note",
			Description: "노트 하나의 본문을 읽는다. 요약하거나 내용을 인용해야 할 때 필수 — " +
				"search_wiki의 짧은 스니펫만 보고 내용을 단정하지 말 것. 경로는 search_wiki나 list_notes가 준 값을 그대로 쓴다.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "노트 경로 (예: 'topics/cs/concurrency.md')",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        "wiki_status",
			Description: "위키 전체 통계: 카테고리별 편수, 성숙도(status) 분포, daily/personal 편수. '위키 현황' 류 질문에 쓴다.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

// ---- tool dispatch ----

type searchOut struct {
	Query   string     `json:"query"`
	Count   int        `json:"count"`
	Matches []matchOut `json:"matches"`
}

type matchOut struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Status  string `json:"status,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	URL     string `json:"url"`
}

type noteOut struct {
	Path      string   `json:"path"`
	Title     string   `json:"title"`
	Status    string   `json:"status,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Aliases   []string `json:"aliases,omitempty"`
	Body      string   `json:"body"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (b *Brain) runTool(ctx context.Context, name string, input json.RawMessage) (string, error) {
	switch name {
	case "search_wiki":
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("search_wiki 인자를 못 읽었어요: %v", err)
		}
		if strings.TrimSpace(in.Query) == "" {
			return "", fmt.Errorf("search_wiki: query가 비었어요")
		}
		matches, err := b.wiki.Search(ctx, in.Query)
		if err != nil {
			return "", fmt.Errorf("위키를 읽지 못했어요: %v", err)
		}
		out := searchOut{Query: in.Query, Count: len(matches)}
		for _, m := range matches {
			out.Matches = append(out.Matches, matchOut{
				Path: m.Path, Title: m.Title, Status: m.Status, Snippet: m.Snippet, URL: m.URL,
			})
		}
		return encode(out)

	case "list_notes":
		var in struct {
			Prefix string `json:"prefix"`
		}
		_ = json.Unmarshal(input, &in) // prefix is optional; empty means "everything"
		items, err := b.wiki.List(ctx, in.Prefix)
		if err != nil {
			return "", fmt.Errorf("위키를 읽지 못했어요: %v", err)
		}
		total := len(items)
		if len(items) > listLimit {
			items = items[:listLimit]
		}
		out := struct {
			Prefix    string     `json:"prefix,omitempty"`
			Total     int        `json:"total"`
			Truncated bool       `json:"truncated,omitempty"`
			Notes     []matchOut `json:"notes"`
		}{Prefix: in.Prefix, Total: total, Truncated: total > len(items)}
		for _, m := range items {
			out.Notes = append(out.Notes, matchOut{Path: m.Path, Title: m.Title, Status: m.Status, URL: m.URL})
		}
		return encode(out)

	case "read_note":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("read_note 인자를 못 읽었어요: %v", err)
		}
		n, err := b.wiki.ReadNote(ctx, in.Path)
		if err != nil {
			return "", err // wiki.ReadNote already explains bad paths
		}
		body, cut := trimRunes(n.Body, noteBodyLimit)
		return encode(noteOut{
			Path: n.Path, Title: n.Title, Status: n.Status,
			Tags: n.Tags, Aliases: n.Aliases, Body: body, Truncated: cut,
		})

	case "wiki_status":
		rep, err := b.wiki.Status(ctx)
		if err != nil {
			return "", fmt.Errorf("위키를 읽지 못했어요: %v", err)
		}
		return encode(rep)
	}
	return "", fmt.Errorf("알 수 없는 툴: %s", name)
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func trimRunes(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]), true
}
