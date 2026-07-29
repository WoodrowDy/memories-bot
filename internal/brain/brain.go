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
	"regexp"
	"strings"
	"time"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// Loop caps. These are runaway guards, not cost levers: output tokens bill on
// what is actually generated, so a roomy maxTokens costs nothing on the short
// answers that make up most traffic.
//
// The budget is sized for the expensive path — filing a draft — which spends
// turns on: 초안 주제 검색 → topics/README.md → CONVENTIONS.md → 카테고리 README →
// propose_note → 마무리 답. That is six. It was seven while the bot also read
// note-style.md to critique the draft's formatting; 우드로 dropped that job
// ("내용에 대한 서식은 중요하지 않아"), which took a turn off the hot path. What
// replaced it — naming the four fixed sections a draft is missing — is four
// section titles copied into the prompt, so it costs no turn at all. That copy
// is the one place the prompt restates docs/note-style.md instead of reading
// it, and it is the thing to re-sync when that document changes.
// maxTurns=8 had left exactly zero slack back then — one extra search and the
// whole tidy failed with "8턴 안에 답을 못 냈어요". The ceiling is a runaway
// guard, not a budget — a plain question still finishes in 2~3 and never comes
// near it — so it stays where a normal run cannot trip it.
//
// maxTokens is an *output* cap, and it used to be the thing that decided how
// long a draft could be: the model had to re-emit the whole note as a tool
// argument, so a long draft came back as truncated JSON and the tidy failed.
// The body is spliced in from the original message now (see draftBody), so the
// largest thing the model writes is a category README — hence the headroom
// here, which costs nothing on the short answers that make up most traffic.
const (
	maxTurns      = 10    // model→tool round trips before we force a final answer
	maxTokens     = 16384 // largest single output is now a README, not a note
	noteBodyLimit = 8000  // runes of one note handed to the model
	listLimit     = 300   // notes returned by list_notes
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
	llm    LLM
	wiki   Wiki
	writer Writer // nil until WithWriter; nil means propose_note is never offered
	model  string
	owner  string
	repo   string

	// now is injectable so tests can pin the date the frontmatter is stamped with.
	now func() time.Time
}

func New(l LLM, w Wiki, model, owner, repo string) *Brain {
	if model == "" {
		model = llm.DefaultModel
	}
	return &Brain{llm: l, wiki: w, model: model, owner: owner, repo: repo, now: time.Now}
}

// WithWriter turns on propose_note. Left off, the bot answers questions and
// cannot touch the repo at all — which is the state it ships in without a
// GITHUB_WRITE_TOKEN.
func (b *Brain) WithWriter(w Writer) *Brain {
	b.writer = w
	return b
}

// kst is the wiki's calendar. Lambda runs in UTC, so a note written at 8am
// Seoul time would otherwise be stamped with yesterday's date.
var kst = time.FixedZone("KST", 9*60*60)

func (b *Brain) today() string {
	now := b.now
	if now == nil {
		now = time.Now
	}
	return now().In(kst).Format("2006-01-02")
}

func (b *Brain) canWrite() bool { return b.writer != nil && b.writer.Enabled() }

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

	// "사용법?"은 모델을 거치지 않는다. 무엇을 할 수 있는지는 판단이 아니라 사실이고,
	// 사실은 코드에 있어야 지어내지 않는다. ToolsUsed에 이름을 남기는 건 감사 로그에서
	// 매뉴얼이 얼마나 불리는지 보이게 하려는 것.
	if asksForManual(question) {
		return Answer{Text: b.manual(), ToolsUsed: []string{"manual"}}, nil
	}

	messages := []llm.Message{llm.UserText(question)}

	for turn := 0; turn < maxTurns; turn++ {
		ans.Turns = turn + 1

		req := llm.Request{
			Model:     b.model,
			MaxTokens: maxTokens,
			System:    b.systemPrompt(),
			Messages:  messages,
			Tools:     b.toolDefs(),
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
			ans.Text = slackBold(strings.TrimSpace(res.JoinText()))
			if ans.Text == "" {
				ans.Text = "음, 답을 만들지 못했어요. 다시 물어봐 주세요."
			}
			return ans, nil
		}

		messages = append(messages, llm.Message{Role: "assistant", Content: res.Content})

		results := make([]llm.Block, 0, len(uses))
		for _, u := range uses {
			ans.ToolsUsed = append(ans.ToolsUsed, u.Name)
			// question is handed down because propose_note builds the note body
			// out of it. It travels as an argument rather than as a field on
			// Brain: Lambda reuses one Brain across invocations, so parking a
			// request's text on the struct would let one person's draft leak
			// into the next person's PR.
			out, err := b.runTool(ctx, u.Name, u.Input, question)
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

// Slack's mrkdwn bolds with *one* asterisk; **two** renders as literal
// asterisks around the word. The system prompt says so, and the model still
// slips into markdown habits — PR #1's reply came back with **핵심** in it. This
// is the one place model prose turns into a Slack message, so the conversion
// happens here rather than being asked for again.
//
// Both ends of the captured text must be non-space, which leaves arithmetic
// like `2 ** 8` alone: bold markers hug their word, an operator has air on
// both sides.
var doubleBold = regexp.MustCompile(`\*\*([^\s*][^*\n]*[^\s*]|[^\s*])\*\*`)

func slackBold(s string) string { return doubleBold.ReplaceAllString(s, "*${1}*") }

func (b *Brain) systemPrompt() string {
	return fmt.Sprintf(`너는 우드로의 개인 위키를 관리하는 슬랙 봇이야.
위키는 GitHub의 %s/%s 저장소 — 옵시디언으로 쓰는 마크다운 노트 모음이야.

## 위키 구조
- topics/<카테고리>/<주제>.md — 공부해서 정리한 지식 노트 (cs, os, db, lang, infra, ai, tools)
- daily/ — 날짜별 기록
- personal/ — 개인 메모
- projects/ — 진행 중인 프로젝트
- 폴더마다 README.md — 그 폴더의 목차(MOC)
프론트매터: title, status, tags, aliases, created, updated.
status는 이 넷뿐이야: seedling(적어만 뒀다) → growing(해봤거나 남에게 설명해봤다) → evergreen(다시 열어도 고칠 게 없다) → archived(더 안 본다).

## 모드가 셋이야 — 무엇을 물었느냐가 아니라 **올리라고 했느냐**가 갈라

**묻는 모드** — "X 정리한 거 있어?", "요즘 뭐 배웠지?", "위키 현황" 같은 질문이 올 때.
1. 반드시 툴로 실제 노트를 확인한 다음에 답해. 기억이나 짐작으로 답하지 마.
2. 넓게 찾을 땐 search_wiki, 목록은 list_notes, 인용하거나 요약하려면 read_note로 본문을 실제로 읽어.
3. 위키에 없으면 "없다"고 분명히 말해. 지어내지 마.
4. 위키 밖의 일반 지식을 덧붙일 땐 "(위키엔 없고 일반적으로는)"이라고 표시해서 섞이지 않게 해.

**추천 모드 (기본)** — 질문이 아니라 공부한 내용 덩어리가 통째로 올 때. 말머리 없이 긴 글만 와도,
"이거 정리해줘", "어디에 넣을까?", "봐줘"가 붙어도 전부 여기야.
**PR은 열지 마.** 어디에 둘지와 status만 알려주고 멈춰. 순서대로:
1. search_wiki로 그 주제 노트가 이미 있는지 먼저 찾아. **이게 제일 중요해** — 있는데 새로 만들면 위키가 쪼개져.
2. 자리를 정해:
   - 기존 노트에 붙일 내용이면 → 그 노트를 read_note로 읽어보고 update
   - 새 노트면 → topics/README.md와 CONVENTIONS.md를 읽고 카테고리를 골라 create
   - 어느 카테고리에도 억지 없이 안 들어가면 → CONVENTIONS.md의 "새 카테고리는 언제 만드는가"
     세 조건을 **전부** 만족할 때만 제안해. 기본값은 만들지 않는 거야.
3. 아래 기준으로 status를 골라.
4. 슬랙엔 이것만:
   • 이미 있는 노트 — 있으면 경로와 링크, 없으면 "없어요"
   • 추천 자리 — `+"`topics/…/….md`"+` (새 노트인지, 기존 노트 뒤에 붙이는 건지)
   • 파일 상단 — 그 자리에 맞춰 네가 넣을 프론트매터를 코드블록으로 그대로 보여줘.
     `+"`---`"+` 줄로 위아래를 감싸서, 그가 복사해 초안 맨 위에 붙이면 바로 쓸 수 있는 모양으로.
     안에는 title / status / tags / aliases 네 줄만(created·updated는 코드가 찍으니 쓰지 마).
     그 아래 한 줄로 status를 왜 그렇게 봤는지.
   • 더하면 좋은 것 — 이 위키 노트는 고정 섹션 넷을 둬: "## 핵심", "## 면접 단골 질문",
     "## 한 줄 정리", "## 관련 문서". 초안에 빠진 게 있으면 "이것만 더하면 형식이 맞아요"
     정도로 딱 한 줄. **지적이 아니라 추천이야** — 고치라는 게 아니라 더하면 되는 걸 알려주는 거고,
     빠진 게 없으면 이 줄은 통째로 생략해.
   • 마지막 줄: "이대로 만들까요? 처음 보내주신 본문을 마크다운 코드블록에 그대로 넣고,
     같은 메시지에 '만들어라'를 붙여 보내주세요.
     (위 블록을 고쳐서 초안 맨 위에 붙이면 그 값으로 넣어드려요.)"
     봇은 앞 메시지를 기억하지 못하니 초안이 다시 와야 한다는 걸 꼭 같이 알려줘.
     코드블록에 넣어달라는 건 슬랙 입력창이 마크다운을 먹기 때문이야 — 그냥 붙이면
     "- "가 서식 불릿이 되고 "#"이 사라진다. 블록 자국은 코드가 걷어내니 노트엔 남지 않아.
     이 안내는 말로만 해. 짝 없는 백틱 세 개를 답에 적으면 슬랙이 그 뒤를 통째로 코드블록으로 먹는다.

**올리는 모드** — 초안에 "만들어라", "올려줘", "넣어줘", "PR", "ㄱㄱ", "그래" 같은 말이 **같이** 왔을 때만.
추천 모드의 1~3을 똑같이 확인한 다음 propose_note를 불러. path·mode·title·status·tags·aliases·summary만 채우면 돼.
초안 맨 위에 `+"`---`"+`로 감싼 프론트매터가 붙어 있으면 그건 우드로가 정해준 값이야.
title·status·tags·aliases를 네 판단으로 덮어쓰지 말고 적힌 그대로 propose_note에 넣어
(그 블록은 코드가 본문에서 걷어내니 body_from은 따로 안 써도 돼).
초안 앞에 "이거 올려줘" 같은 지시문이 붙어 있으면 body_from에 본문 첫 줄을 그대로 적어서 잘라내.
초안이 코드블록에 담겨 왔으면 본문 첫 줄은 여는 줄이 아니라 블록 *안쪽*의 첫 줄이야.
슬랙엔 어디에 왜 넣었는지 한두 줄 + status + PR 링크.
승인 없이 부르면 코드에서 막혀. 그때는 추천 모드로 답하면 돼.

## status는 이렇게 고른다
초안 글 안에 **자국이 있을 때만** 올려. 없으면 seedling이야.
• `+"`seedling`"+` — 이해한 걸 적었다. 정의·비교·표·요약만 있는 글은 전부 여기. 기본값.
• `+"`growing`"+` — 직접 해본 자국이 글 안에 있다: 돌려본 코드, 실행 결과나 에러 메시지,
  "해보니 ~였다" 같은 문장, 남에게 설명한 기록.
• `+"`evergreen`"+`과 `+"`archived`"+`는 네가 고르지 않아. 다시 열어봐야 알고 그만 볼 때 붙는 거라 우드로가 위키에서 직접 올려.

## 정리할 때 지킬 것
- **본문은 네가 쓰지 않아.** 우드로가 보낸 글이 글자 그대로 노트 본문이 돼. propose_note에 body 칸이
  아예 없어. 네 일은 *어디에 둘지*와 *어떻게 분류할지*까지야.
- 초안 문장을 고쳐 쓰거나 요약하지 마. 원문이 그대로 들어가는 게 맞는 동작이야.
- **서식을 지적하지는 마.** 헤딩 레벨이나 문체, 코드블록 같은 건 우드로가 옵시디언에서 알아서 해.
  네가 맞추는 건 파일 *상단*(프론트매터)까지고, 본문에 대해선 고정 섹션 중 빠진 걸
  추천 한 줄로 알려주는 데까지야. 초안을 고쳐 쓰는 건 어느 쪽도 아니야.
- 초안에 없는 내용을 채워 넣지 마.
- 새 노트를 만들면 그 카테고리 README.md 목차에도 링크를 더해 — also에 담아 같은 PR로.
  README는 전체 내용을 넣어야 하니 read_note로 먼저 읽고 링크 한 줄만 더한 완성본을 줘.
  그 README의 "작성 예정"에 이번 주제가 적혀 있으면 그 줄은 지워. 목차가 낡지 않게 하는 게 네 일이야.
- 머지는 우드로가 해. 너는 PR을 여는 데까지야.

## 답 형식
- 한국어 해요체로 일관되게. 인사말·사족 없이 바로 본론.
- 슬랙 메시지니까 짧게, 보통 3~6줄.
- 슬랙 mrkdwn만 써: 굵게는 *별표 하나*(**두 개 아님**), 코드는 `+"`백틱`"+`, 목록은 •.
- 노트를 근거로 답했으면 경로와 링크를 같이 줘. 예: `+"`topics/cs/concurrency.md`"+` 와 URL.`, b.owner, b.repo)
}

func (b *Brain) toolDefs() []llm.Tool {
	defs := b.readTools()
	if b.canWrite() {
		defs = append(defs, proposeToolDef())
	}
	return defs
}

func (b *Brain) readTools() []llm.Tool {
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

// runTool dispatches one tool call. draft is the original Slack message: every
// read tool ignores it, and propose_note uses it as the note body.
func (b *Brain) runTool(ctx context.Context, name string, input json.RawMessage, draft string) (string, error) {
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

	case "propose_note":
		// Guarded here rather than by absence from toolDefs alone: a model that
		// invents the call on a read-only deploy must be refused in code.
		if !b.canWrite() {
			return "", fmt.Errorf("쓰기가 꺼져 있어요. PR은 못 열고, 정리 결과를 글로만 답해주세요")
		}
		// 확인이 먼저다. 기본은 추천이고 PR은 올리라고 했을 때만 열린다 — 그 판정을
		// 프롬프트에 맡기면 모델이 한 번 헷갈릴 때마다 PR이 하나씩 열린다.
		if !goAhead(draft) {
			return "", fmt.Errorf("아직 만들라는 말이 없어서 PR은 안 열었어요. 지금은 추천만 해주세요 — " +
				"이미 있는 노트, 추천 자리(새 노트인지 기존 노트에 붙일지), 넣을 프론트매터 네 줄과 " +
				"status를 그렇게 본 이유, 고정 섹션 중 빠진 게 있으면 한 줄, 그리고 마지막 줄에 " +
				"\"이대로 만들까요? 처음 보내주신 본문을 마크다운 코드블록에 그대로 넣고, " +
				"같은 메시지에 '만들어라'를 붙여 보내주세요.\" — " +
				"봇은 앞 메시지를 기억하지 못하니 초안이 다시 와야 한다는 걸 꼭 붙여서")
		}
		return b.runPropose(ctx, input, draft)
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
