package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wikiwrite"
)

// fakeWriter stands in for the GitHub write client. It records the proposal it
// was handed so the tests can assert on what *would* have been pushed.
type fakeWriter struct {
	on   bool
	got  []wikiwrite.Proposal
	res  wikiwrite.Result
	err  error
	fail error // returned from Propose
}

func (w *fakeWriter) Enabled() bool { return w.on }

func (w *fakeWriter) Propose(_ context.Context, p wikiwrite.Proposal) (wikiwrite.Result, error) {
	w.got = append(w.got, p)
	if w.fail != nil {
		return wikiwrite.Result{}, w.fail
	}
	return w.res, w.err
}

// pinnedNow is 2026-07-22 14:30 UTC = 23:30 the same day in Seoul.
func pinnedNow() time.Time { return time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC) }

func writingBrain(notes map[string]wiki.Note) (*Brain, *fakeWriter) {
	w := &fakeWriter{on: true, res: wikiwrite.Result{
		PRURL:  "https://github.com/WoodrowDy/memories/pull/7",
		Number: 7,
		Branch: "bot/note-topics-cs-grpc",
		Files:  []string{"topics/cs/grpc.md", "topics/cs/README.md"},
	}}
	b := New(&fakeLLM{}, &fakeWiki{notes: notes}, "m", "WoodrowDy", "memories").WithWriter(w)
	b.now = pinnedNow
	return b, w
}

const goodPropose = `{
  "path": "topics/cs/grpc.md",
  "mode": "create",
  "title": "gRPC",
  "status": "seedling",
  "tags": ["cs/grpc", "cs/network"],
  "aliases": ["그RPC"],
  "summary": "cs 카테고리에 새 노트로 넣었어요.",
  "also": [{
    "path": "topics/cs/README.md",
    "content": "# cs\n\n- [gRPC](grpc.md)\n",
    "why": "목차에 링크 추가"
  }]
}`

// draft stands in for 우드로's Slack message. Under the passthrough design this
// — not anything the model writes — is what ends up in the file.
const draft = "# gRPC\n\n## 한 줄\n프로토콜 버퍼를 쓰는 RPC 프레임워크."

// goDraft is the same message with the go-ahead on it. Every test that reaches
// propose_note *through the dispatcher* has to use this one — 초안만 던진 메시지는
// 추천까지고 PR은 올리라고 했을 때만 열린다. draftBody strips that last line
// again, so what lands in the file is byte-identical to draft.
const goDraft = draft + "\n\n올려줘"

// ---- the tool only exists when writing is on ----

func TestProposeToolIsOfferedOnlyWhenWritingIsOn(t *testing.T) {
	read := New(&fakeLLM{}, &fakeWiki{}, "m", "o", "r")
	if got := len(read.toolDefs()); got != 4 {
		t.Errorf("read-only brain offered %d tools, want 4", got)
	}

	off := New(&fakeLLM{}, &fakeWiki{}, "m", "o", "r").WithWriter(&fakeWriter{on: false})
	if got := len(off.toolDefs()); got != 4 {
		t.Errorf("brain with a tokenless writer offered %d tools, want 4", got)
	}

	on, _ := writingBrain(nil)
	defs := on.toolDefs()
	if len(defs) != 5 || defs[4].Name != "propose_note" {
		t.Fatalf("writing brain tools = %d, last = %q", len(defs), defs[len(defs)-1].Name)
	}
}

// A model can invent a tool call the schema never offered it. The refusal has
// to live in code, not in the absence of a tool definition.
func TestRunToolRefusesProposeWhenWritingIsOff(t *testing.T) {
	b := New(&fakeLLM{}, &fakeWiki{}, "m", "o", "r")

	out, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), goDraft)
	if err == nil {
		t.Fatalf("read-only brain proposed a note: %s", out)
	}
	if !strings.Contains(err.Error(), "쓰기가 꺼져") {
		t.Errorf("error should say writing is off, got %v", err)
	}
}

// ---- validation, all of it before any write ----

func TestValidateProposeRefusesBadInput(t *testing.T) {
	base := func(mut func(*proposeIn)) proposeIn {
		in := proposeIn{
			Path: "topics/cs/grpc.md", Mode: "create", Title: "gRPC",
			Status: "seedling", Tags: []string{"cs/grpc"},
			Summary: "요약",
		}
		mut(&in)
		return in
	}

	cases := []struct {
		name string
		in   proposeIn
	}{
		{"모드 없음", base(func(i *proposeIn) { i.Mode = "" })},
		{"모드 오타", base(func(i *proposeIn) { i.Mode = "createt" })},
		{"머지 모드는 없다", base(func(i *proposeIn) { i.Mode = "merge" })},
		{"규칙 문서는 못 고친다", base(func(i *proposeIn) { i.Path = "CONVENTIONS.md" })},
		{"스타일 문서도 못 고친다", base(func(i *proposeIn) { i.Path = "docs/note-style.md" })},
		{"워크플로는 못 건드린다", base(func(i *proposeIn) { i.Path = ".github/workflows/ci.md" })},
		{"경로 탈출", base(func(i *proposeIn) { i.Path = "topics/../../etc/passwd.md" })},
		{"마크다운이 아님", base(func(i *proposeIn) { i.Path = "topics/cs/grpc.txt" })},
		{"한글 파일명", base(func(i *proposeIn) { i.Path = "topics/cs/동시성.md" })},
		{"공백 파일명", base(func(i *proposeIn) { i.Path = "topics/cs/go routines.md" })},
		{"대문자 파일명", base(func(i *proposeIn) { i.Path = "topics/cs/gRPC.md" })},
		{"topics 새 노트에 태그 없음", base(func(i *proposeIn) { i.Tags = nil })},
		{"없는 status", base(func(i *proposeIn) { i.Status = "budding" })},
		{"새 노트인데 status 없음", base(func(i *proposeIn) { i.Status = "" })},
		{"제목 빈칸", base(func(i *proposeIn) { i.Title = "   " })},
		{"본문을 모델이 써 보냄", base(func(i *proposeIn) { i.Body = "모델이 지어낸 본문" })},
		{"요약 없음", base(func(i *proposeIn) { i.Summary = "\n" })},
		{"also 너무 많음", base(func(i *proposeIn) {
			for n := 0; n < maxAlsoFiles+1; n++ {
				i.Also = append(i.Also, alsoIn{Path: "topics/cs/README.md", Content: "x"})
			}
		})},
		{"also에 규칙 문서", base(func(i *proposeIn) {
			i.Also = []alsoIn{{Path: "CONVENTIONS.md", Content: "x"}}
		})},
		{"also에 자기 자신", base(func(i *proposeIn) {
			i.Also = []alsoIn{{Path: "topics/cs/grpc.md", Content: "x"}}
		})},
		{"also 내용이 빔", base(func(i *proposeIn) {
			i.Also = []alsoIn{{Path: "topics/cs/README.md", Content: "  "}}
		})},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validatePropose(c.in); err == nil {
				t.Fatalf("accepted %+v", c.in)
			}
		})
	}
}

func TestValidateProposeAcceptsAnUpdateWithoutStatusOrTags(t *testing.T) {
	// An update is allowed to leave status and tags out — renderNote keeps the
	// note's existing values rather than blanking them.
	in := proposeIn{
		Path: "topics/cs/grpc.md", Mode: "update", Title: "gRPC",
		Summary: "요약",
	}
	if err := validatePropose(in); err != nil {
		t.Fatalf("rejected a legitimate update: %v", err)
	}
}

// Bad input must never reach the writer — the refusal happens before the branch.
func TestProposeRefusesBeforeTouchingTheWriter(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runTool(context.Background(), "propose_note",
		json.RawMessage(`{"path":"CONVENTIONS.md","mode":"create","title":"x","status":"seedling","tags":["x"],"summary":"s"}`), goDraft)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times on invalid input", len(w.got))
	}
}

// ---- create vs update is decided against the repo, not the model's word ----

func TestProposeRefusesCreateOnANoteThatAlreadyExists(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/grpc.md": {Path: "topics/cs/grpc.md", Title: "gRPC", Status: "growing"},
	})

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), draft)
	if err == nil {
		t.Fatal("a create over an existing note would silently replace it")
	}
	if !strings.Contains(err.Error(), `mode="update"`) {
		t.Errorf("error should tell the model which mode to retry with, got %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times", len(w.got))
	}
}

func TestProposeRefusesUpdateOnANoteThatDoesNotExist(t *testing.T) {
	b, w := writingBrain(nil)
	in := strings.Replace(goodPropose, `"mode": "create"`, `"mode": "update"`, 1)

	_, err := b.runPropose(context.Background(), json.RawMessage(in), draft)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), `mode="create"`) {
		t.Errorf("error should tell the model which mode to retry with, got %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times", len(w.got))
	}
}

// A GitHub outage must not read as "the note is missing" — that is how a
// duplicate note gets created on top of a real one.
func TestProposeStopsWhenTheRepoCannotBeRead(t *testing.T) {
	b, w := writingBrain(nil)
	b.wiki = &flakyWiki{err: errors.New("github: 502 bad gateway")}

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), draft)
	if err == nil {
		t.Fatal("a transient read failure was treated as 'note absent'")
	}
	if !strings.Contains(err.Error(), "확인하지 못했어요") {
		t.Errorf("error = %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times during an outage", len(w.got))
	}
}

type flakyWiki struct {
	fakeWiki
	err error
}

func (f *flakyWiki) ReadNote(context.Context, string) (wiki.Note, error) {
	return wiki.Note{}, f.err
}

func TestIsMissingOnlyMatchesARealAbsence(t *testing.T) {
	missing := []error{
		errors.New("없는 노트: topics/cs/grpc.md"),
		errors.New("github: 404 Not Found"),
	}
	for _, err := range missing {
		if !isMissing(err) {
			t.Errorf("%v should count as absent", err)
		}
	}
	present := []error{
		errors.New("github: 502 bad gateway"),
		errors.New("context deadline exceeded"),
		errors.New("github: 403 rate limit"),
	}
	for _, err := range present {
		if isMissing(err) {
			t.Errorf("%v must not count as absent", err)
		}
	}
}

// ---- frontmatter ----

func TestRenderNoteWritesFrontmatterInTheWikisOrder(t *testing.T) {
	in := proposeIn{
		Path: "topics/cs/grpc.md", Mode: "create", Title: "gRPC", Status: "seedling",
		Tags: []string{"cs/grpc", "cs/network"}, Aliases: []string{"그RPC"},
	}
	got := renderNote(in, nil, "\n## 한 줄\n프로토콜 버퍼를 쓰는 RPC.\n\n", "2026-07-22")

	want := "---\ntitle: gRPC\naliases: [그RPC]\ncreated: 2026-07-22\nupdated: 2026-07-22\n" +
		"tags: [cs/grpc, cs/network]\nstatus: seedling\n---\n\n## 한 줄\n프로토콜 버퍼를 쓰는 RPC.\n"
	if got != want {
		t.Errorf("rendered note:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderNoteOmitsEmptyListsRatherThanWritingBlankOnes(t *testing.T) {
	in := proposeIn{
		Path: "daily/2026-07-22.md", Mode: "create", Title: "7월 22일",
		Status: "seedling",
	}
	got := renderNote(in, nil, "메모", "2026-07-22")

	if strings.Contains(got, "aliases:") || strings.Contains(got, "tags:") {
		t.Errorf("empty lists should be left out entirely:\n%s", got)
	}
}

func TestRenderNoteUpdateKeepsCreatedAndUnionsTags(t *testing.T) {
	prev := &wiki.Note{
		Path: "topics/cs/grpc.md", Title: "gRPC", Status: "growing",
		Created: "2025-01-05", Tags: []string{"cs/grpc"}, Aliases: []string{"그RPC"},
	}
	in := proposeIn{
		Path: "topics/cs/grpc.md", Mode: "update", Title: "gRPC",
		Tags: []string{"cs/network"}, Aliases: []string{"grpc"},
	}
	got := renderNote(in, prev, "합친 본문", "2026-07-22")

	for _, want := range []string{
		"created: 2025-01-05",
		"updated: 2026-07-22",
		"tags: [cs/grpc, cs/network]",
		"aliases: [그RPC, grpc]",
		"status: growing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A tidy-up must never knock a note back down the ladder — and the model writes
// "seedling" out of habit, so leaving it out is not the only case that matters.
func TestRenderNoteUpdateNeverDemotesAMatureNote(t *testing.T) {
	cases := []struct{ prev, asked, want string }{
		{"evergreen", "", "evergreen"},         // omitted keeps what is there
		{"evergreen", "seedling", "evergreen"}, // the habitual demotion
		{"growing", "seedling", "growing"},     // one rung down is still down
		{"seedling", "growing", "growing"},     // upward is the model's to make
		{"growing", "evergreen", "evergreen"},  // ...all the way up
		{"evergreen", "archived", "evergreen"}, // archiving is 우드로's call
		{"archived", "growing", "growing"},     // a revived note starts over
		{"", "growing", "growing"},             // no previous status, take theirs
	}
	for _, c := range cases {
		prev := &wiki.Note{Path: "topics/cs/grpc.md", Title: "gRPC", Status: c.prev, Created: "2024-11-02"}
		in := proposeIn{Path: "topics/cs/grpc.md", Mode: "update", Title: "gRPC", Status: c.asked}

		got := renderNote(in, prev, "보강", "2026-07-22")
		if !strings.Contains(got, "status: "+c.want+"\n") {
			t.Errorf("prev=%q asked=%q → want status %q, got:\n%s", c.prev, c.asked, c.want, got)
		}
	}
}

func TestUnionDedupesCaseInsensitivelyAndKeepsTheOldForm(t *testing.T) {
	got := union([]string{"CS/gRPC", "os"}, []string{"cs/grpc", " ", "db"})
	want := []string{"CS/gRPC", "os", "db"}
	if len(got) != len(want) {
		t.Fatalf("union = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("union = %v, want %v", got, want)
		}
	}
}

// ---- the date the note is stamped with ----

func TestTodayIsSeoulTimeNotUTC(t *testing.T) {
	b, _ := writingBrain(nil)

	// 23:30 UTC is already the next morning in Seoul.
	b.now = func() time.Time { return time.Date(2026, 7, 22, 23, 30, 0, 0, time.UTC) }
	if got := b.today(); got != "2026-07-23" {
		t.Errorf("today = %s, want 2026-07-23 (KST)", got)
	}

	b.now = func() time.Time { return time.Date(2026, 7, 22, 14, 30, 0, 0, time.UTC) }
	if got := b.today(); got != "2026-07-22" {
		t.Errorf("today = %s, want 2026-07-22 (KST)", got)
	}
}

// ---- the happy path, end to end through the dispatcher ----

func TestProposeOpensAPRAndReportsItBack(t *testing.T) {
	b, w := writingBrain(nil)

	out, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), goDraft)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.got))
	}
	p := w.got[0]

	if p.Slug != "topics/cs/grpc.md" {
		t.Errorf("slug = %q", p.Slug)
	}
	if p.Title != "노트: gRPC" {
		t.Errorf("PR title = %q", p.Title)
	}
	if len(p.Files) != 2 {
		t.Fatalf("files = %d, want note + README", len(p.Files))
	}
	if p.Files[0].Path != "topics/cs/grpc.md" || p.Files[1].Path != "topics/cs/README.md" {
		t.Errorf("files = %q, %q", p.Files[0].Path, p.Files[1].Path)
	}
	if !strings.HasPrefix(p.Files[0].Content, "---\ntitle: gRPC\n") {
		t.Errorf("note content did not start with frontmatter:\n%s", p.Files[0].Content)
	}
	if !strings.Contains(p.Files[0].Content, "created: 2026-07-22") {
		t.Errorf("note was not stamped with the pinned date:\n%s", p.Files[0].Content)
	}
	// The README does not exist in this fake wiki, so there is no frontmatter to
	// put back and the model's content stands as written.
	if p.Files[1].Content != "# cs\n\n- [gRPC](grpc.md)\n" {
		t.Errorf("also file was rewritten: %q", p.Files[1].Content)
	}

	for _, want := range []string{
		"cs 카테고리에 새 노트로 넣었어요.",
		"`topics/cs/grpc.md` — 새 노트",
		"`topics/cs/README.md` — 목차에 링크 추가",
		"memories-wiki-bot",
	} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("PR body missing %q:\n%s", want, p.Body)
		}
	}

	var got proposeOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "https://github.com/WoodrowDy/memories/pull/7" || got.Number != 7 {
		t.Errorf("tool output lost the PR: %+v", got)
	}
	if got.Branch != "bot/note-topics-cs-grpc" {
		t.Errorf("branch = %q", got.Branch)
	}
	if got.Mode != "create" {
		t.Errorf("mode = %q", got.Mode)
	}
	if !strings.Contains(got.Reminder, "머지는 사람이") {
		t.Errorf("the model must be told it cannot merge: %q", got.Reminder)
	}
}

func TestProposeUpdateTitlesThePRAsAnAddition(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/grpc.md": {
			Path: "topics/cs/grpc.md", Title: "gRPC", Status: "evergreen",
			Created: "2025-01-05", Tags: []string{"cs/grpc"}, Body: "예전 본문",
		},
	})
	in := strings.Replace(goodPropose, `"mode": "create"`, `"mode": "update"`, 1)

	if _, err := b.runPropose(context.Background(), json.RawMessage(in), draft); err != nil {
		t.Fatal(err)
	}
	p := w.got[0]
	if p.Title != "노트 보강: gRPC" {
		t.Errorf("PR title = %q", p.Title)
	}
	if !strings.Contains(p.Body, "기존 노트 뒤에 초안을 이어붙임") {
		t.Errorf("PR body should say it is an addition:\n%s", p.Body)
	}
	if !strings.Contains(p.Files[0].Content, "created: 2025-01-05") {
		t.Errorf("update lost the original created date:\n%s", p.Files[0].Content)
	}
	if !strings.Contains(p.Files[0].Content, "status: evergreen") {
		t.Errorf("update demoted the note:\n%s", p.Files[0].Content)
	}
}

func TestProposeReportsAWriteFailureToTheModel(t *testing.T) {
	b, w := writingBrain(nil)
	w.fail = errors.New("github: 403 forbidden")

	if _, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), draft); err == nil {
		t.Fatal("a failed PR must not be reported as success")
	}
}

// ---- the body is 우드로's, character for character ----

// The load-bearing test of the passthrough design. If this ever goes green
// while the file content differs from what was typed into Slack, the bot has
// started editing his writing again.
func TestTheNoteBodyIsTheDraftVerbatim(t *testing.T) {
	b, w := writingBrain(nil)

	if _, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), draft); err != nil {
		t.Fatal(err)
	}
	content := w.got[0].Files[0].Content

	_, body, ok := strings.Cut(content, "---\n\n")
	if !ok {
		t.Fatalf("no frontmatter break in:\n%s", content)
	}
	if body != draft+"\n" {
		t.Errorf("body was rewritten.\n got: %q\nwant: %q", body, draft+"\n")
	}
}

// A draft long enough to have blown the old output ceiling has to survive
// intact — that is the entire reason the design changed.
func TestALongDraftSurvivesWhole(t *testing.T) {
	b, w := writingBrain(nil)
	long := "# GoF\n\n" + strings.Repeat("객체지향 디자인 패턴은 반복되는 설계 문제의 이름 붙은 해법이다. ", 2000)

	if _, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), long); err != nil {
		t.Fatal(err)
	}
	if got, want := w.got[0].Files[0].Content, strings.TrimSpace(long); !strings.Contains(got, want) {
		t.Errorf("a %d-char draft did not land whole (file is %d chars)", len(want), len(got))
	}
}

// With no draft there is nothing to file, and inventing one is the failure this
// design exists to prevent.
func TestProposeRefusesWithNothingToFile(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), "   \n\n  ")
	if err == nil {
		t.Fatal("a note was proposed with no draft behind it")
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times", len(w.got))
	}
}

func TestDraftBodyCutsTheInstructionOffTheTop(t *testing.T) {
	cases := []struct {
		name, draft, from, want string
	}{
		{
			name:  "지시문을 잘라낸다",
			draft: "이거 정리해줘\n\n# gRPC\n\n본문이다.",
			from:  "# gRPC",
			want:  "# gRPC\n\n본문이다.",
		},
		{
			name:  "from이 없으면 통째로",
			draft: "# gRPC\n\n본문이다.",
			want:  "# gRPC\n\n본문이다.",
		},
		{
			// The model paraphrased instead of copying. Keeping everything leaves
			// a stray instruction line visible in the diff; cutting at a guess
			// would silently drop real content.
			name:  "from을 못 찾으면 자르지 않는다",
			draft: "이거 정리해줘\n\n# gRPC\n\n본문이다.",
			from:  "gRPC에 대한 설명",
			want:  "이거 정리해줘\n\n# gRPC\n\n본문이다.",
		},
		{
			name:  "from에 여러 줄이 와도 첫 줄로 찾는다",
			draft: "정리 부탁\n\n## 한 줄\n프로토콜 버퍼.",
			from:  "## 한 줄\n프로토콜 버퍼.",
			want:  "## 한 줄\n프로토콜 버퍼.",
		},
		{
			name:  "옵시디언에서 복사한 프론트매터는 걷어낸다",
			draft: "---\ntitle: gRPC\ntags: [cs/grpc]\n---\n\n# gRPC\n\n본문이다.",
			want:  "# gRPC\n\n본문이다.",
		},
		{
			// --- as a horizontal rule, not frontmatter. Cutting to the next ---
			// would eat the first section.
			name:  "구분선으로 시작하는 초안은 건드리지 않는다",
			draft: "---\n\n## 핵심\n첫 문단.\n\n---\n\n## 다음\n둘째 문단.",
			want:  "---\n\n## 핵심\n첫 문단.\n\n---\n\n## 다음\n둘째 문단.",
		},
		{
			name:  "닫히지 않은 프론트매터는 본문으로 둔다",
			draft: "---\ntitle: gRPC\n\n본문이다.",
			want:  "---\ntitle: gRPC\n\n본문이다.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := draftBody(c.draft, c.from); got != c.want {
				t.Errorf("draftBody:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// ---- 확인이 먼저다: 초안만 오면 추천, 만들라고 해야 PR ----

// gofLike is the shape of a real study draft — the outermost lines of 우드로's
// GoF note, kept verbatim. It has to come back as a recommendation and nothing
// more: it says Prototype and Proxy, and a substring match on "pr" would file a
// PR he never asked for.
const gofLike = "GoF 디자인 패턴 정리\n\n" +
	"## 1. GoF 디자인 패턴이란?\n" +
	"- Erich Gamma\n" +
	"- Richard Helm\n\n" +
	"| Prototype | 기존 객체를 복제한다. |\n" +
	"| Proxy | 대리 객체로 접근을 제어한다. |\n\n" +
	"## Composite vs Decorator\n" +
	"| Composite | 객체를 트리 구조로 조합 |"

func TestGoAheadReadsTheInstructionNotTheDraft(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"맨 아래 한 줄", draft + "\n\n올려줘", true},
		{"맨 위 한 줄", "이거 올려줘\n\n" + draft, true},
		{"그가 실제로 쓰는 말", draft + "\n\n그래 만들어라", true},
		{"짧은 대답 하나로도", draft + "\n\n동의", true},
		{"PR이라고만", draft + "\n\nPR", true},
		{"ㄱㄱ", draft + "\n\nㄱㄱ", true},
		{"앞에 말이 붙어도", "정리해서 올려줘\n\n" + draft, true},
		{"프론트매터를 고쳐 보낼 때 쓰는 말", draft + "\n\n맞춰서 넣어줘", true},
		{"앞머리가 둘이어도", draft + "\n\n이거 맞춰서 넣어줘", true},
		{"뒤에 빈 줄이 남아도", draft + "\n\n올려줘\n\n   \n", true},

		{"초안만 오면 추천까지", draft, false},
		{"봐달라는 건 승인이 아니다", "이거 어디에 넣을까?\n\n" + draft, false},
		{"GoF 초안은 PR을 열지 않는다", gofLike, false},

		// 본문 한 줄이 PR을 열면 안 된다. 그래서 가운데는 보지 않는다.
		{"가운데 줄은 지시문이 아니다", "# 배포\n\n이건 나중에 올려야 한다\n\n## 끝", false},
		// 초안 문장에도 같은 낱말이 나온다. 뒤를 묶어둔 이유가 이것.
		{"만들어는 초안 문장에도 나온다", "# 팩토리\n\n객체를 만들어 반환한다", false},
		{"넣어도 초안 문장에 나온다", "# 큐\n\n버퍼에 넣어 두면 순서가 보장된다", false},
		// 목록·표·헤딩으로 시작하는 줄은 글이지 지시가 아니다.
		{"목록 항목", "# 할 일\n\n- 파일 올려주기", false},
		{"표의 마지막 행", "# 패턴\n\n| Proxy | 올려도 되는가 |", false},
		{"헤딩", "# 올려줘\n\n본문이다.", false},
		// 지시문은 짧다. 문단은 지시문이 아니다.
		{"문단은 길다", draft + "\n\n이 부분은 다음에 다시 정리해서 위키에 올려야겠다고 생각했다", false},

		{"빈 메시지", "   \n\n  ", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := goAhead(c.text); got != c.want {
				t.Errorf("goAhead = %v, want %v for:\n%s", got, c.want, c.text)
			}
		})
	}
}

// The strip is deliberately narrower than the detection. Missing a go-ahead
// costs one more paste; deleting a line he wrote costs a sentence out of the
// note, and that only shows in the diff.
func TestStripGoAheadLineTakesTheWordAndNothingElse(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"맨 아래", "# gRPC\n\n본문이다.\n\n올려줘", "# gRPC\n\n본문이다.\n"},
		{"맨 위", "올려줘\n\n# gRPC\n\n본문이다.", "\n# gRPC\n\n본문이다."},
		{"위아래 둘 다", "올려줘\n\n# gRPC\n\n만들어라", "\n# gRPC\n"},
		{"긴 형태도 통째로", "# gRPC\n\n이대로 PR 올려줘!", "# gRPC\n"},
		{"짧은 대답", "# gRPC\n\n그래", "# gRPC\n"},
		{"앞머리가 붙어도 통째로", "# gRPC\n\n맞춰서 넣어줘", "# gRPC\n"},
		{"앞머리가 둘이어도", "# gRPC\n\n이거 맞춰서 넣어줘", "# gRPC\n"},

		{"목록 항목은 글이다", "# 할 일\n\n- 파일 올려주기", "# 할 일\n\n- 파일 올려주기"},
		{"본문 문장은 남는다", "# 배포\n\n이건 나중에 올려야 한다", "# 배포\n\n이건 나중에 올려야 한다"},
		{"문장 부호만 있는 줄은 건드리지 않는다", "# gRPC\n\n본문이다.\n\n?", "# gRPC\n\n본문이다.\n\n?"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripGoAheadLine(c.in); got != c.want {
				t.Errorf("stripGoAheadLine:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// The approval rides in the same message as the draft, so the word that
// unlocked the PR must not end up filed as part of the note.
func TestTheGoAheadWordDoesNotLandInTheNote(t *testing.T) {
	if got := draftBody(goDraft, ""); got != draft {
		t.Errorf("draftBody(goDraft):\n got: %q\nwant: %q", got, draft)
	}

	b, w := writingBrain(nil)
	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), goDraft); err != nil {
		t.Fatal(err)
	}
	if got := w.got[0].Files[0].Content; strings.Contains(got, "올려줘") {
		t.Errorf("the instruction was filed as part of the note:\n%s", got)
	}
}

// 봇이 추천한 프론트매터를 그가 고쳐서 초안 맨 위에 붙여 다시 보내는 길. 값은 모델이
// 읽어 툴 인자로 옮기고, 블록 자체는 본문에서 걷혀야 한다 — 안 걷히면 renderNote가
// 찍는 프론트매터 아래에 그가 붙인 블록이 한 번 더 남아 노트 상단이 두 겹이 된다.
func TestPastedFrontmatterDoesNotLandInTheBody(t *testing.T) {
	msg := "---\n" +
		"title: GoF 디자인 패턴\n" +
		"status: seedling\n" +
		"tags: [cs/pattern, cs/design]\n" +
		"aliases: [GoF, 디자인 패턴, design pattern]\n" +
		"---\n" +
		gofLike + "\n\n맞춰서 넣어줘"

	if !goAhead(msg) {
		t.Fatal(`goAhead = false — "맞춰서 넣어줘"로는 PR이 안 열린다`)
	}

	got := draftBody(msg, "")
	if got != gofLike {
		t.Errorf("본문이 초안 그대로가 아니다:\n got: %q\nwant: %q", got, gofLike)
	}
}

// ---- 슬랙 코드블록에 넣어 보낸 초안 ----

// 슬랙 입력창은 마크다운을 먹는다 — "- "는 서식 불릿이 되고 "#"은 사라진다. 그래서
// 초안은 ``` 블록에 담겨 오고, 그 자국이 메시지 원문에 남는다. 보내느라 생긴
// 자국이지 그가 쓴 글이 아니니, 어느 자리에 승인을 붙였든 파일에 들어가는 건 초안
// 그대로여야 한다.
func TestAFencedDraftLandsAsPlainMarkdown(t *testing.T) {
	const fence = "```"

	cases := []struct{ name, msg string }{
		{
			name: "승인은 블록 아래에",
			msg:  fence + "\n" + gofLike + "\n" + fence + "\n\n올려줘",
		},
		{
			// 슬랙에서 블록을 닫고 나서 다시 밖으로 나오기가 번거로워 안에서 끝내는 길.
			// 이때 맨 아래 줄은 승인이 아니라 닫는 ```라 가장자리만 봐선 못 찾는다.
			name: "승인은 블록 안에",
			msg:  fence + "\n" + gofLike + "\n\n만들어라\n" + fence,
		},
		{
			name: "언어 태그가 붙어도",
			msg:  fence + "md\n" + gofLike + "\n" + fence + "\n\n넣어줘",
		},
		{
			name: "승인은 블록 위에",
			msg:  "이거 올려줘\n\n" + fence + "\n" + gofLike + "\n" + fence,
		},
		{
			name: "블록 바깥에 빈 줄이 남아도",
			msg:  "\n\n" + fence + "\n" + gofLike + "\n" + fence + "\n\n그래\n\n   \n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !goAhead(c.msg) {
				t.Fatalf("goAhead = false — 코드블록에 넣었다고 PR이 안 열린다:\n%s", c.msg)
			}
			if got := draftBody(c.msg, ""); got != gofLike {
				t.Errorf("본문이 초안 그대로가 아니다:\n got: %q\nwant: %q", got, gofLike)
			}
		})
	}
}

// 블록 위에 한 줄 적어 보내는 길. 지시문이 펜스 밖에 있으니 그걸 먼저 걷어내지 않으면
// 여는 ```가 첫 줄이 아니게 되고, 노트 위아래에 ```가 그대로 박힌다.
func TestAnInstructionAboveTheFenceDoesNotLeaveTheFenceBehind(t *testing.T) {
	msg := "이번주에 공부한 GoF인데 위키에 올려줘\n\n```\n" + gofLike + "\n```"

	if !goAhead(msg) {
		t.Fatal("goAhead = false")
	}
	got := draftBody(msg, "GoF 디자인 패턴 정리")
	if strings.Contains(got, "```") {
		t.Errorf("코드블록 자국이 노트에 남았다:\n%s", got)
	}
	if got != gofLike {
		t.Errorf("본문이 초안 그대로가 아니다:\n got: %q\nwant: %q", got, gofLike)
	}
}

// 자르는 자리를 위로 올리는 것뿐이라 남는 글은 늘어나기만 한다. 코드블록으로 시작하는
// 초안이 걸려도 그 블록은 노트에 그대로 남아야 한다.
func TestFenceAboveNeverCostsContent(t *testing.T) {
	msg := "이거 정리해서 올려줘\n\n```go\nfunc main() {}\n```\n\n러너블한 최소 예제."
	want := "```go\nfunc main() {}\n```\n\n러너블한 최소 예제."

	if got := draftBody(msg, "func main() {}"); got != want {
		t.Errorf("draftBody:\n got: %q\nwant: %q", got, want)
	}
}

// 감싸기만 하고 승인을 안 붙였으면 여전히 추천까지다. 펜스를 알아보게 만든 것이
// 승인 없이 PR을 여는 길이 되면 안 된다.
func TestAFencedDraftOnItsOwnIsStillOnlyARecommendation(t *testing.T) {
	msg := "```\n" + gofLike + "\n```"

	if goAhead(msg) {
		t.Fatal("코드블록에 넣었다는 이유로 PR이 열렸다")
	}

	b, w := writingBrain(nil)
	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), msg); err == nil {
		t.Fatal("승인 없는 초안이 PR을 열었다")
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times with no go-ahead", len(w.got))
	}
}

// 벗기는 쪽은 좁게. 여기서 잘못 벗기면 코드블록으로 시작하고 코드블록으로 끝나는
// 노트의 첫 줄과 끝 줄이 조용히 날아가고, 그건 diff를 열어봐야 보인다.
func TestUnfenceOnlyStripsAWrapperItIsSureOf(t *testing.T) {
	cases := []struct {
		name, in, want string
		wantFenced     bool
	}{
		{"통째로 감싼 초안", "```\n# gRPC\n\n본문이다.\n```", "# gRPC\n\n본문이다.", true},
		{"md 태그", "```md\n# gRPC\n```", "# gRPC", true},
		{"markdown 태그", "```markdown\n# gRPC\n```", "# gRPC", true},
		{"바깥 빈 줄은 무시한다", "\n\n```\n# gRPC\n```\n\n", "# gRPC", true},

		// 통째로 감쌀 때 붙는 태그는 없거나 md 계열이고, 코드블록으로 여는 노트는
		// 언어를 적는다. 그 차이가 유일하게 믿을 만한 표시다.
		{"언어를 적은 블록은 노트의 일부다", "```go\nfunc main() {}\n```", "```go\nfunc main() {}\n```", false},
		{"sql도 마찬가지", "```sql\nSELECT 1;\n```", "```sql\nSELECT 1;\n```", false},

		{"닫히지 않았으면 펜스가 아니다", "```\n# gRPC\n\n본문이다.", "```\n# gRPC\n\n본문이다.", false},
		{"닫는 줄에 태그가 붙으면 짝이 아니다", "```\n# gRPC\n```go", "```\n# gRPC\n```go", false},
		{"가운데서 열린 블록은 감싼 게 아니다", "# gRPC\n\n```\n코드\n```", "# gRPC\n\n```\n코드\n```", false},
		{"``` 한 줄뿐이면 벗길 게 없다", "```", "```", false},
		{"빈 메시지", "   \n\n  ", "   \n\n  ", false},

		// 빈 블록은 벗겨도 남는 게 없다. runPropose가 "초안이 없다"로 거부한다.
		{"빈 블록", "```\n```", "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, fenced := unfence(c.in)
			if fenced != c.wantFenced {
				t.Fatalf("unfence fenced = %v, want %v for:\n%s", fenced, c.wantFenced, c.in)
			}
			if got != c.want {
				t.Errorf("unfence:\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// 승인은 한 줄만 지운다. 안팎을 다 훑는 구현은 마침 승인 낱말로 끝나는 본문 —
// 대화 예시나 짧은 결론 — 의 마지막 줄까지 같이 가져가는데, 지워진 건 diff를 열어야
// 보인다. 알아보는 쪽은 넓게, 지우는 쪽은 좁게.
func TestOnlyOneGoAheadLineIsEverRemoved(t *testing.T) {
	body := "# 코드리뷰 말투\n\n승인할 때 쓰는 말:\n\n좋아"
	msg := "```\n" + body + "\n```\n\n올려줘"

	if !goAhead(msg) {
		t.Fatal("goAhead = false")
	}
	if got := draftBody(msg, ""); got != body {
		t.Errorf("본문 마지막 줄까지 같이 지워졌다:\n got: %q\nwant: %q", got, body)
	}
}

// The end-to-end shape of what he actually does: paste the draft inside a code
// block, say the word, get a PR whose file matches the paste.
func TestAFencedDraftIsFiledVerbatim(t *testing.T) {
	b, w := writingBrain(nil)
	msg := "```\n" + gofLike + "\n```\n\n만들어라"

	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), msg); err != nil {
		t.Fatal(err)
	}

	content := w.got[0].Files[0].Content
	_, filed, ok := strings.Cut(content, "---\n\n")
	if !ok {
		t.Fatalf("no frontmatter break in:\n%s", content)
	}
	if filed != gofLike+"\n" {
		t.Errorf("파일에 들어간 게 초안과 다르다:\n got: %q\nwant: %q", filed, gofLike+"\n")
	}
}

// The default is a recommendation. A draft on its own must not open a PR, and
// the refusal has to happen in code — a prompt is not a rule.
func TestProposeIsRefusedUntilHeSaysSo(t *testing.T) {
	b, w := writingBrain(nil)

	out, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), draft)
	if err == nil {
		t.Fatalf("a bare draft opened a PR: %s", out)
	}
	if !strings.Contains(err.Error(), "만들라는 말이 없어서") {
		t.Errorf("the refusal should send the model back to recommending, got %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times with no go-ahead", len(w.got))
	}
}

// A brand-new note can be seedling or growing and nothing above. evergreen is
// "다시 열었을 때 고칠 게 없었다" and archived is "더 안 본다" — both are verdicts
// that need time, so neither can be read off a first draft.
func TestCreateStatusStopsAtGrowing(t *testing.T) {
	in := func(mode, status string) proposeIn {
		return proposeIn{
			Path: "topics/cs/grpc.md", Mode: mode, Title: "gRPC",
			Status: status, Tags: []string{"cs/grpc"}, Summary: "요약",
		}
	}

	for _, s := range []string{"seedling", "growing"} {
		if err := validatePropose(in("create", s)); err != nil {
			t.Errorf("create with status %q was rejected: %v", s, err)
		}
	}
	for _, s := range []string{"evergreen", "archived"} {
		if err := validatePropose(in("create", s)); err == nil {
			t.Errorf("a note being created for the first time was accepted as %q", s)
		}
	}

	// The ceiling is on new notes only. An update carries whatever the note has
	// already grown into, and keepMature is what stops a demotion.
	if err := validatePropose(in("update", "evergreen")); err != nil {
		t.Errorf("update to evergreen was rejected: %v", err)
	}
}

// ---- update appends, and appending never loses ----

func TestMergeBodyAppendsAndKeepsWhatWasThere(t *testing.T) {
	prev := "# gRPC\n\n## 핵심\n계약 중심의 호출."
	add := "# gRPC\n\n## 스트리밍\n양방향으로 열린다."

	got := mergeBody(prev, add)

	if !strings.HasPrefix(got, prev) {
		t.Fatalf("existing content was not kept intact:\n%s", got)
	}
	if !strings.Contains(got, "## 스트리밍") {
		t.Errorf("draft did not land:\n%s", got)
	}
	// The draft repeats the title as an H1; a second one mid-file is not a
	// heading, it is a seam.
	if strings.Count(got, "\n# gRPC") != 0 || strings.Count(got, "# gRPC") != 1 {
		t.Errorf("duplicate H1 survived the merge:\n%s", got)
	}
	// note-style.md: 섹션 사이는 ---로 끊는다.
	if !strings.Contains(got, "\n\n---\n\n") {
		t.Errorf("no section break at the seam:\n%s", got)
	}
}

func TestMergeBodyKeepsTheH1WhenTheNoteHasNone(t *testing.T) {
	got := mergeBody("본문만 있는 노트.", "# gRPC\n\n새 내용.")
	if !strings.Contains(got, "# gRPC") {
		t.Errorf("a note with no heading of its own lost the draft's:\n%s", got)
	}
}

func TestProposeUpdateAppendsRatherThanReplacing(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/grpc.md": {
			Path: "topics/cs/grpc.md", Title: "gRPC", Status: "evergreen",
			Created: "2025-01-05", Tags: []string{"cs/grpc"},
			Body: "# gRPC\n\n## 핵심\n예전에 써둔 내용. 이게 사라지면 안 된다.",
		},
	})
	in := strings.Replace(goodPropose, `"mode": "create"`, `"mode": "update"`, 1)

	if _, err := b.runPropose(context.Background(), json.RawMessage(in), draft); err != nil {
		t.Fatal(err)
	}
	content := w.got[0].Files[0].Content

	if !strings.Contains(content, "예전에 써둔 내용") {
		t.Errorf("an update destroyed the existing note:\n%s", content)
	}
	if !strings.Contains(content, "프로토콜 버퍼를 쓰는 RPC 프레임워크") {
		t.Errorf("the draft did not make it into the note:\n%s", content)
	}
	if strings.Index(content, "예전에 써둔 내용") > strings.Index(content, "프로토콜 버퍼") {
		t.Error("the draft was put above the existing content, not appended")
	}
}

// ---- an `also` file keeps the frontmatter the model was never shown ----

// The bug PR #1 shipped: read_note hands the model a frontmatter-free body, so
// the rewritten README it hands back has none either, and writing that raw
// deleted title/created/tags off topics/cs/README.md.
func TestAlsoRestoresTheFrontmatterTheModelNeverSaw(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/README.md": {
			Path:        "topics/cs/README.md",
			Title:       "CS MOC",
			Frontmatter: "---\ntitle: CS MOC\ncreated: 2026-05-29\nupdated: 2026-07-28\ntags: [moc, cs]\n---",
			Body:        "\n\n# CS (백엔드 기초)\n\n- [동시성](concurrency.md)\n",
		},
	})

	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), goDraft); err != nil {
		t.Fatal(err)
	}
	got := w.got[0].Files[1].Content

	for _, want := range []string{
		"---\ntitle: CS MOC\n",
		"created: 2026-05-29\n",
		"tags: [moc, cs]\n",
		"---\n\n# cs\n\n- [gRPC](grpc.md)\n", // the model's new table of contents, below the block
	} {
		if !strings.Contains(got, want) {
			t.Errorf("README lost %q:\n%s", want, got)
		}
	}
	// The date moves — the file really was touched — but only that one line.
	if !strings.Contains(got, "updated: 2026-07-22") {
		t.Errorf("updated: was not bumped to today:\n%s", got)
	}
	if strings.Contains(got, "2026-07-28") {
		t.Errorf("the stale updated: date survived:\n%s", got)
	}
}

func TestAlsoDiscardsFrontmatterTheModelInvented(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/README.md": {
			Path:        "topics/cs/README.md",
			Frontmatter: "---\ntitle: CS MOC\ncreated: 2026-05-29\nupdated: 2026-07-28\ntags: [moc, cs]\n---",
			Body:        "\n\n# CS (백엔드 기초)\n",
		},
	})

	in := `{
	  "path": "topics/cs/grpc.md", "mode": "create", "title": "gRPC", "status": "seedling",
	  "tags": ["cs/grpc"], "summary": "새 노트로 넣었어요.",
	  "also": [{
	    "path": "topics/cs/README.md",
	    "content": "---\ntitle: CS\ncreated: 2026-07-22\n---\n\n# cs\n\n- [gRPC](grpc.md)\n"
	  }]
	}`
	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(in), goDraft); err != nil {
		t.Fatal(err)
	}
	got := w.got[0].Files[1].Content

	if n := strings.Count(got, "---"); n != 2 {
		t.Errorf("want exactly one frontmatter block, found %d delimiters:\n%s", n, got)
	}
	if !strings.Contains(got, "title: CS MOC") || !strings.Contains(got, "created: 2026-05-29") {
		t.Errorf("the file's real frontmatter lost to the model's:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n\n# cs\n\n- [gRPC](grpc.md)\n") {
		t.Errorf("the table of contents did not survive:\n%s", got)
	}
}

func TestAlsoLeavesAFileThatNeverHadFrontmatterAlone(t *testing.T) {
	b, w := writingBrain(map[string]wiki.Note{
		"topics/cs/README.md": {Path: "topics/cs/README.md", Body: "# CS\n"},
	})

	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose), goDraft); err != nil {
		t.Fatal(err)
	}
	if got := w.got[0].Files[1].Content; got != "# cs\n\n- [gRPC](grpc.md)\n" {
		t.Errorf("nothing to restore, so the content should be untouched: %q", got)
	}
}

func TestBumpUpdatedTouchesOnlyThatLine(t *testing.T) {
	fm := "---\ntitle: CS MOC\ncreated: 2026-05-29\nupdated: 2026-07-28\ntags: [moc, cs]\n---"
	want := "---\ntitle: CS MOC\ncreated: 2026-05-29\nupdated: 2026-07-22\ntags: [moc, cs]\n---"
	if got := bumpUpdated(fm, "2026-07-22"); got != want {
		t.Errorf("bumpUpdated =\n%s\nwant\n%s", got, want)
	}

	// A block with no updated: field gets no new one invented for it.
	bare := "---\ntitle: CS MOC\ntags: [moc]\n---"
	if got := bumpUpdated(bare, "2026-07-22"); got != bare {
		t.Errorf("bumpUpdated added a field that was not there: %q", got)
	}
}
