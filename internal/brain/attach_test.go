package brain

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// 옵시디언에서 쓰던 노트를 그대로 던진 모양. 프론트매터가 이미 붙어 있고, 그 값들은
// 그가 손으로 적은 것이다.
const attachedFile = `---
title: gRPC 스트리밍
aliases: [스트리밍 RPC]
tags: [cs/grpc]
status: growing
created: 2025-03-02
---

# gRPC 스트리밍

## 핵심
- 양방향 스트리밍은 HTTP/2 위에서 돈다
`

func file(name, content string) *Attached { return &Attached{Name: name, Content: content} }

// ---- 본문은 파일 그대로 ----

// 첨부 경로의 대들보. 이게 깨지면 그가 옵시디언에서 쓴 글과 저장소의 글이 달라진다.
func TestAnAttachedFileBecomesTheBodyVerbatim(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("grpc-streaming.md", attachedFile)})
	if err != nil {
		t.Fatal(err)
	}

	_, body, ok := strings.Cut(w.got[0].Files[0].Content, "---\n\n")
	if !ok {
		t.Fatalf("no frontmatter break:\n%s", w.got[0].Files[0].Content)
	}
	want := "# gRPC 스트리밍\n\n## 핵심\n- 양방향 스트리밍은 HTTP/2 위에서 돈다\n"
	if body != want {
		t.Errorf("body was rewritten.\n got: %q\nwant: %q", body, want)
	}
}

// 파일이 왔으면 메시지 본문은 본문이 아니다. 붙여넣기 경로의 draftBody가 여기까지
// 손을 뻗으면, 그가 슬랙에 친 한 줄이 노트에 실린다.
func TestTypedTextNeverLeaksIntoAnAttachedNote(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "이거 정리해서 올려줘\n어제 스터디에서 나온 얘기", File: file("g.md", attachedFile)})
	if err != nil {
		t.Fatal(err)
	}
	content := w.got[0].Files[0].Content
	for _, gone := range []string{"이거 정리해서 올려줘", "어제 스터디에서 나온 얘기"} {
		if strings.Contains(content, gone) {
			t.Errorf("typed text landed in the note (%q):\n%s", gone, content)
		}
	}
}

// body_from은 붙여넣기 경로의 도구다. 파일에는 잘라낼 지시문이 없으니, 모델이 습관처럼
// 채워 보내도 파일 첫 줄이 잘려나가면 안 된다.
func TestBodyFromIsIgnoredWhenAFileCame(t *testing.T) {
	b, w := writingBrain(nil)
	in := strings.Replace(goodPropose, `"summary":`, `"body_from": "## 핵심",
  "summary":`, 1)

	_, err := b.runPropose(context.Background(), json.RawMessage(in),
		Ask{Text: "올려줘", File: file("g.md", attachedFile)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.got[0].Files[0].Content, "# gRPC 스트리밍") {
		t.Errorf("body_from cut into the file:\n%s", w.got[0].Files[0].Content)
	}
}

func TestAnEmptyAttachmentIsRefusedByName(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("빈파일.md", "---\ntitle: 빈 노트\n---\n\n   \n")})
	if err == nil {
		t.Fatal("a frontmatter-only file must not open a PR")
	}
	if !strings.Contains(err.Error(), "빈파일.md") {
		t.Errorf("the error must name the file so he knows which one: %v", err)
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times on an empty draft", len(w.got))
	}
}

// ---- 프론트매터: 그의 것을 살리고 빈 칸만 채운다 ----

func TestHisFrontmatterOutranksTheModel(t *testing.T) {
	b, w := writingBrain(nil)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("g.md", attachedFile)})
	if err != nil {
		t.Fatal(err)
	}
	fm, _, _ := strings.Cut(w.got[0].Files[0].Content, "---\n\n")

	// goodPropose가 주는 값은 title "gRPC", status "seedling", tags cs/grpc·cs/network,
	// aliases 그RPC. 파일에 적힌 게 있으면 넷 다 파일 쪽이 이긴다.
	for _, want := range []string{
		"title: gRPC 스트리밍",
		"status: growing",
		"tags: [cs/grpc]",
		"aliases: [스트리밍 RPC]",
		"created: 2025-03-02",
	} {
		if !strings.Contains(fm, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, fm)
		}
	}
	// 모델이 얹으려던 값이 하나라도 새어 들어가면 "적은 그대로"가 아니다.
	for _, gone := range []string{"cs/network", "그RPC", "seedling", "title: gRPC\n"} {
		if strings.Contains(fm, gone) {
			t.Errorf("model value %q overrode his own:\n%s", gone, fm)
		}
	}
	// updated만은 오늘이다 — 그 칸은 "언제 만졌나"라서 매번 새로 찍는 게 맞다.
	if !strings.Contains(fm, "updated: 2026-07-22") {
		t.Errorf("updated was not stamped today:\n%s", fm)
	}
}

func TestBlankFieldsAreFilledAndSaidSo(t *testing.T) {
	b, w := writingBrain(nil)
	bare := "---\ntitle: gRPC\n---\n\n본문이 있다\n"

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("g.md", bare)})
	if err != nil {
		t.Fatal(err)
	}
	fm, _, _ := strings.Cut(w.got[0].Files[0].Content, "---\n\n")
	for _, want := range []string{
		"title: gRPC",      // 그가 적은 값
		"status: seedling", // 빈 칸 → 모델 값
		"tags: [cs/grpc, cs/network]",
		"aliases: [그RPC]",
		"created: 2026-07-22", // 아무도 안 적었으니 오늘
	} {
		if !strings.Contains(fm, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, fm)
		}
	}

	// PR 본문이 어느 줄을 봇이 채웠는지 말해줘야 한다. diff는 그걸 말해주지 않는다.
	body := w.got[0].Body
	if !strings.Contains(body, "봇이 채운 칸") {
		t.Fatalf("PR body never said what it filled:\n%s", body)
	}
	for _, want := range []string{"`aliases`", "`tags`", "`created`", "`status`"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body did not list %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "`title`") {
		t.Errorf("title was his, not the bot's, and must not be listed as filled:\n%s", body)
	}
}

// 반만 채운 파일. 옵시디언에서 프론트매터 틀만 잡아놓고 만 흔한 꼴이고,
// `test-drafts/http2.md`가 일부러 이 모양이다.
//
// 위의 두 테스트는 "다 적힌 파일"과 "title만 적힌 파일"을 보는데, 정작 깨져 있던 건 그
// 사이였다 — 적힌 칸과 빈 칸이 *섞인* 파일. `fmScalar`의 `\s*`가 줄바꿈까지 먹어서 빈
// `tags:`가 아랫줄의 `status:`를 값으로 물고 왔고, resolveMeta는 비지 않은 값을 "그가
// 손으로 적은 값"으로 읽으니 모델이 고른 태그를 존중하며 버리고 `tags: [status:]`를
// PR에 박았다. 봇도 로그도 아무 말을 안 한다 — diff를 열어야만 보인다.
//
// wiki 패키지의 단위 테스트는 파서가 뭘 읽었는지까지만 본다. 피해는 여기서 난다.
func TestAHalfFilledFileNeverTurnsTheNextLineIntoAValue(t *testing.T) {
	b, w := writingBrain(nil)
	half := "---\ntitle: HTTP/2\naliases: [http2, http 2.0]\ncreated: 2026-07-30\n" +
		"updated:\ntags:\nstatus:\n---\n\n# HTTP/2\n\n하나의 TCP 연결에 여러 요청을 실어 보낸다.\n"

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("http2.md", half)})
	if err != nil {
		t.Fatal(err)
	}
	fm, _, _ := strings.Cut(w.got[0].Files[0].Content, "---\n\n")

	if strings.Contains(fm, "status:]") || strings.Contains(fm, "updated:]") {
		t.Errorf("빈 칸이 아랫줄을 값으로 물고 와 그대로 실렸다:\n%s", fm)
	}
	for _, want := range []string{
		"title: HTTP/2",               // 그가 적은 값
		"aliases: [http2, http 2.0]",  // 그가 적은 값
		"created: 2026-07-30",         // 그가 적은 값
		"updated: 2026-07-22",         // 이 칸은 언제나 오늘
		"tags: [cs/grpc, cs/network]", // 빈 칸 → 모델 값이 들어와야 한다
		"status: seedling",            // 빈 칸 → 모델 값
	} {
		if !strings.Contains(fm, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, fm)
		}
	}

	// 빈 칸이 값으로 읽히면 봇은 그걸 그의 값으로 알고, 채웠다는 말조차 안 한다.
	body := w.got[0].Body
	for _, want := range []string{"`tags`", "`status`"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR 본문이 채운 칸에 %s를 안 적었다:\n%s", want, body)
		}
	}
	for _, gone := range []string{"`title`", "`aliases`", "`created`"} {
		if strings.Contains(body, gone) {
			t.Errorf("그가 적은 %s가 봇이 채운 칸에 끼었다:\n%s", gone, body)
		}
	}
}

// 옵시디언 속성(Properties) 편집기로 태그를 한 번 만지면 그 파일은 이 꼴로 다시 쓰인다.
// 지금 위키의 노트는 전부 인라인 `[...]`이지만, 저장소가 볼트와 같아야 하니 이렇게 온
// 파일도 그의 값으로 읽혀야 한다.
//
// 못 읽으면 조용히 진다. fmList가 빈 목록을 주고, resolveMeta는 "그가 안 적었다"로 읽고,
// 그가 고른 태그가 모델이 고른 태그로 갈린 채 PR에 실린다. pickList가 막으려던 게 정확히
// 그거다 — 옵시디언에서 일부러 뗀 태그가 PR마다 되살아나는 것.
func TestObsidiansBlockStyleFrontmatterIsStillHisOwnValue(t *testing.T) {
	b, w := writingBrain(nil)
	block := "---\ntitle: HTTP/2\naliases:\n  - http2\n  - \"http 2.0\"\n" +
		"tags:\n  - cs/http\n  - cs/network\nstatus: growing\ncreated: 2026-07-30\n---\n\n# HTTP/2\n\n본문\n"

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("http2.md", block)})
	if err != nil {
		t.Fatal(err)
	}
	fm, _, _ := strings.Cut(w.got[0].Files[0].Content, "---\n\n")

	// 읽기는 두 꼴 다, 쓰기는 컨벤션대로 인라인 한 꼴이다.
	for _, want := range []string{
		"aliases: [http2, http 2.0]",
		"tags: [cs/http, cs/network]",
		"status: growing",
	} {
		if !strings.Contains(fm, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, fm)
		}
	}
	if strings.Contains(fm, "\n  - ") {
		t.Errorf("블록 꼴이 그대로 다시 쓰였다 — CONVENTIONS.md가 정한 꼴은 인라인이다:\n%s", fm)
	}
	// 모델 값이 하나라도 새어 들어가면 그가 고른 분류가 갈린 것이다.
	for _, gone := range []string{"cs/grpc", "그RPC", "seedling"} {
		if strings.Contains(fm, gone) {
			t.Errorf("model value %q overrode his own:\n%s", gone, fm)
		}
	}
	// 그가 적은 칸을 봇이 채웠다고 말하면, 그것도 거짓말이다.
	if body := w.got[0].Body; strings.Contains(body, "`tags`") || strings.Contains(body, "`aliases`") {
		t.Errorf("그가 적은 tags/aliases가 봇이 채운 칸에 끼었다:\n%s", body)
	}
}

// 붙여넣기 경로에는 "그가 적은 칸"이라는 게 없다. 전부 봇이 정한 것이라 다 적어봐야
// PR 본문에 한 줄이 늘 뿐이고, 그래서 이 절은 통째로 나오지 않는다.
func TestThePasteePathSaysNothingAboutFilledFields(t *testing.T) {
	b, w := writingBrain(nil)

	if _, err := b.runPropose(context.Background(), json.RawMessage(goodPropose), Ask{Text: draft}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.got[0].Body, "봇이 채운 칸") {
		t.Errorf("the paste path grew a section it should not have:\n%s", w.got[0].Body)
	}
}

func TestResolveMetaPrecedence(t *testing.T) {
	const today = "2026-07-22"
	model := proposeIn{
		Title: "모델 제목", Status: "seedling",
		Tags: []string{"cs/model"}, Aliases: []string{"모델별칭"},
	}
	his := &wiki.Note{
		Title: "그의 제목", Status: "growing",
		Tags: []string{"cs/his"}, Aliases: []string{"그의별칭"}, Created: "2025-03-02",
	}
	old := &wiki.Note{
		Title: "옛 제목", Status: "evergreen",
		Tags: []string{"cs/old"}, Aliases: []string{"옛별칭"}, Created: "2024-01-01",
	}

	t.Run("파일에 적힌 값이 모델과 기존 노트를 다 이긴다", func(t *testing.T) {
		m := resolveMeta(model, old, his, today)
		if m.Title != "그의 제목" || m.Status != "growing" || m.Created != "2025-03-02" {
			t.Errorf("his values lost: %+v", m)
		}
		if strings.Join(m.Tags, ",") != "cs/old,cs/his" {
			t.Errorf("tags = %v — want the note's own plus his, and not the model's", m.Tags)
		}
	})

	t.Run("빈 칸은 모델이 채운다", func(t *testing.T) {
		m := resolveMeta(model, nil, &wiki.Note{}, today)
		if m.Title != "모델 제목" || m.Status != "seedling" {
			t.Errorf("model values did not fill the blanks: %+v", m)
		}
		if strings.Join(m.Tags, ",") != "cs/model" {
			t.Errorf("tags = %v", m.Tags)
		}
		if m.Created != today {
			t.Errorf("created = %q, want today", m.Created)
		}
	})

	// created는 역사다. 오늘 날짜로 덮으면 그 노트가 언제부터 있었는지가 영영 사라진다.
	t.Run("created는 기존 노트 것이 오늘보다 세다", func(t *testing.T) {
		m := resolveMeta(model, old, &wiki.Note{}, today)
		if m.Created != "2024-01-01" {
			t.Errorf("created = %q, want the note's own date", m.Created)
		}
		if m.Updated != today {
			t.Errorf("updated = %q, want today", m.Updated)
		}
	})

	// keepMature는 모델의 습관을 막는 장치지, 사람이 적은 값을 되돌리는 장치가 아니다.
	t.Run("그가 손으로 적은 archived는 keepMature를 건너뛴다", func(t *testing.T) {
		m := resolveMeta(model, old, &wiki.Note{Status: "archived"}, today)
		if m.Status != "archived" {
			t.Errorf("status = %q — his own archived was overridden", m.Status)
		}
		if len(m.Replaced) != 0 {
			t.Errorf("a valid status must not be reported as replaced: %v", m.Replaced)
		}
	})

	// 모델이 evergreen 노트에 seedling을 얹는 걸 막는 쪽은 그대로 살아 있어야 한다.
	t.Run("모델은 여전히 성숙한 노트를 끌어내리지 못한다", func(t *testing.T) {
		m := resolveMeta(model, old, &wiki.Note{}, today)
		if m.Status != "evergreen" {
			t.Errorf("status = %q — the model demoted a mature note", m.Status)
		}
	})

	// 옵시디언 템플릿엔 status: draft 같은 게 흔하다. 그대로 실으면 그날부터 "위키 현황"의
	// 편수가 어긋난다.
	t.Run("사다리에 없는 status는 바꾸고 그 사실을 알린다", func(t *testing.T) {
		m := resolveMeta(model, nil, &wiki.Note{Status: "draft"}, today)
		if m.Status != "seedling" {
			t.Errorf("status = %q, want the ladder's bottom rung", m.Status)
		}
		if len(m.Replaced) != 1 || !strings.Contains(m.Replaced[0], "draft") {
			t.Fatalf("the swap was made silently: %v", m.Replaced)
		}
		for _, k := range m.Filled {
			if k == "status" {
				t.Error("a replaced value is not a filled blank — they must not be conflated")
			}
		}
	})

	// 붙여넣기 경로(own == nil)에서는 채운 칸을 세지 않는다.
	t.Run("붙여넣기 경로는 Filled를 남기지 않는다", func(t *testing.T) {
		if m := resolveMeta(model, nil, nil, today); len(m.Filled) != 0 {
			t.Errorf("Filled = %v on the paste path", m.Filled)
		}
	})
}

func TestAReplacedStatusIsWarnedAboutInThePR(t *testing.T) {
	b, w := writingBrain(nil)
	tpl := "---\ntitle: gRPC\nstatus: draft\n---\n\n본문\n"

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("g.md", tpl)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.got[0].Files[0].Content, "status: seedling") {
		t.Errorf("draft was written into the note:\n%s", w.got[0].Files[0].Content)
	}
	body := w.got[0].Body
	if !strings.Contains(body, "[!WARNING]") || !strings.Contains(body, "draft") {
		t.Errorf("the PR did not warn that a value he wrote was changed:\n%s", body)
	}
}

// ---- 옵시디언 전용 문법: 세기만 한다 ----

func TestAttachedCheckReadsTheBodyNotTheFrontmatter(t *testing.T) {
	// 옵시디언 속성에는 [[링크]]가 흔하다. PR이 세는 건 본문이니 여기도 본문만 세야
	// 한다 — 슬랙에서 2개라 듣고 PR에서 3개를 보면 어느 쪽이 거짓말인지 찾느라 저녁이 간다.
	a := Attached{Name: "g.md", Content: "---\ntitle: g\nrelated: \"[[다른 노트]]\"\n---\n\n[[진짜 링크]]\n"}

	r := a.Check()
	if r.Total() != 1 {
		t.Errorf("counted %d, want only the one in the body: %v", r.Total(), r.Lines())
	}
}

func TestACleanAttachmentReportsNothing(t *testing.T) {
	if r := (Attached{Content: attachedFile}).Check(); !r.OK() {
		t.Errorf("a clean file was flagged: %v", r.Lines())
	}
}

func TestObsidianSyntaxIsReportedInThePRButNeverFixed(t *testing.T) {
	b, w := writingBrain(nil)
	md := "---\ntitle: gRPC\n---\n\n자세한 건 [[gRPC 기초]]를 보라.\n\n> [!todo] 나중에\n> 스트리밍 정리\n"

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("g.md", md)})
	if err != nil {
		t.Fatal(err)
	}

	// 고치지 않는다. 본문에 손대는 순간 "원문 그대로"가 무너진다.
	content := w.got[0].Files[0].Content
	if !strings.Contains(content, "[[gRPC 기초]]") || !strings.Contains(content, "[!todo]") {
		t.Errorf("the bot rewrote his syntax instead of reporting it:\n%s", content)
	}
	body := w.got[0].Body
	if !strings.Contains(body, "옵시디언에서만 되는 문법") {
		t.Fatalf("the PR said nothing about it:\n%s", body)
	}
	for _, want := range []string{"위키링크", "콜아웃"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body missing %q:\n%s", want, body)
		}
	}
}

// ---- 첨부가 바꾸지 않는 것 ----

// 승인은 그가 슬랙에 친 말에서만 읽는다. 초안을 옮겨 적다 남은 "올려줘" 한 줄이
// 승인이 되면, 그가 보기만 하려던 글이 PR로 나간다.
func TestApprovalIsReadFromTheMessageNotTheFile(t *testing.T) {
	b, w := writingBrain(nil)
	withWord := attachedFile + "\n올려줘\n"

	_, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose),
		Ask{Text: "어디에 넣을까?", File: file("g.md", withWord)})
	if err == nil {
		t.Fatal("a word inside the file must not open a PR")
	}
	if len(w.got) != 0 {
		t.Errorf("writer was called %d times without approval", len(w.got))
	}
}

func TestAttachingAFileStillOpensThePRWhenHeSaysSo(t *testing.T) {
	b, w := writingBrain(nil)

	if _, err := b.runTool(context.Background(), "propose_note", json.RawMessage(goodPropose),
		Ask{Text: "이거 올려줘", File: file("g.md", attachedFile)}); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 {
		t.Fatalf("writer called %d times, want 1", len(w.got))
	}
}

// .md를 붙이고 "이거 어떻게 쓰지?"라고 적을 수 있다. 그때 초안이 통째로 매뉴얼 답변에
// 먹히면 그 글은 아무 데도 가지 못한다.
func TestAnAttachmentIsNeverAnswerredWithTheManual(t *testing.T) {
	b := New(&fakeLLM{script: []llm.Response{finalText("cs 카테고리가 맞아 보여요")}},
		&fakeWiki{}, "m", "o", "r")

	ans, err := b.Run(context.Background(), Ask{Text: "사용법?", File: file("g.md", attachedFile)})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ans.ToolsUsed {
		if u == "manual" {
			t.Fatalf("the draft was swallowed by the manual: %q", ans.Text)
		}
	}
}

func TestAPlainQuestionStillGetsTheManual(t *testing.T) {
	b := New(&fakeLLM{}, &fakeWiki{}, "m", "o", "r")

	ans, err := b.Run(context.Background(), Ask{Text: "사용법?"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.ToolsUsed) != 1 || ans.ToolsUsed[0] != "manual" {
		t.Errorf("tools = %v, want the manual shortcut", ans.ToolsUsed)
	}
}

// ---- 모델에게 보내는 첫 메시지 ----

func TestPromptCarriesTheFileAndSaysWhatItIs(t *testing.T) {
	p := Ask{Text: "어디에 넣을까?", File: file("grpc-streaming.md", attachedFile)}.prompt()

	for _, want := range []string{
		"어디에 넣을까?",
		"grpc-streaming.md",
		"양방향 스트리밍은 HTTP/2 위에서 돈다",
		"body_from은 쓰지 마",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestPromptIsJustTheTextWhenNoFileCame(t *testing.T) {
	if got := (Ask{Text: draft}).prompt(); got != draft {
		t.Errorf("the paste path's prompt changed:\n%s", got)
	}
}

// ---- 어느 라운드인지 코드가 알려준다 ----

// 2026-07-30 11:45. 그는 승인 낱말을 적어 파일과 함께 던졌는데 PR이 열리지 않았고,
// 돌아온 건 "초안을 다시 보내주세요"였다. 낱말을 잘못 읽은 게 아니다 — 모델이
// propose_note 앞까지 오지도 않았다. 낱말 읽는 일은 이미 코드가 하고 있으니, 그 결과를
// 첫 메시지에 같이 실어 보낸다.
func TestThePromptSaysWhichRoundThisIs(t *testing.T) {
	f := file("layered-architecture.md", attachedFile)

	t.Run("승인이 실려 있으면 열라고 못 박는다", func(t *testing.T) {
		p := Ask{Text: "올려줘", File: f}.prompt()
		for _, want := range []string{
			"올리는 모드",
			"반드시 propose_note를 불러 PR을 열어",
			"초안을 다시 보내달라고 하지 마",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("prompt missing %q:\n%s", want, p)
			}
		}
	})

	t.Run("확인이면 멈추라고 한다", func(t *testing.T) {
		p := Ask{Text: "확인", File: f}.prompt()
		if !strings.Contains(p, "확인 모드") || !strings.Contains(p, "멈춰") {
			t.Errorf("확인 라운드인데 그렇게 안 적혀 있다:\n%s", p)
		}
		if strings.Contains(p, "PR을 열어") {
			t.Errorf("승인이 없는데 PR을 열라고 적혀 있다:\n%s", p)
		}
	})
}

// 판정 문구는 goAhead의 복사본이 아니라 그 결과여야 한다. 둘이 갈라지면 프롬프트는
// 열라고 적힌 채로 코드가 막는다 — 모델은 시킨 대로 propose_note를 부르고, 그는 PR
// 대신 거절 메시지를 받는다. 겉모습이 지금 고치려는 버그와 똑같아서 제일 나쁘다.
func TestTheVerdictNeverDisagreesWithTheGate(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"올려줘", "올려줘"},
		{"만들어라", "만들어라"},
		{"앞에 말이 붙어도", "이거 정리해서 넣어줘"},
		{"짧은 대답", "그래"},
		{"그가 정한 확인 낱말", "확인"},
		{"확인해줘", "이거 확인해줘"},
		{"물음", "어디에 넣을까?"},
		{"말 없이 파일만", ""},
		{"펜스 아래 승인", "```\n# 노트\n\n본문\n```\n올려줘"},
		{"본문 문장은 승인이 아니다", "- 객체를 만들어 반환한다"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Ask{Text: tc.text, File: file("a.md", attachedFile)}.prompt()
			said, gate := strings.Contains(p, "올리는 모드"), goAhead(tc.text)
			if said != gate {
				t.Errorf("프롬프트는 올리는 모드=%v라고 하는데 코드의 문은 %v다", said, gate)
			}
		})
	}
}

// 판정은 첨부가 있을 때만 붙는다. 파일 없이 오는 말은 "위키 현황" 같은 질문일 수도
// 있어서, 거기에 "추천만 하고 멈춰"를 붙이면 묻는 모드가 확인 모드로 끌려간다.
func TestTheVerdictOnlyRidesWithAFile(t *testing.T) {
	for _, text := range []string{"위키 현황", "```\n# 노트\n\n본문\n```\n\n올려줘"} {
		if got := (Ask{Text: text}).prompt(); got != text {
			t.Errorf("붙여넣기 길의 첫 메시지가 바뀌었다:\n got: %q\nwant: %q", got, text)
		}
	}
}

// 잘린 자리를 글의 끝으로 읽으면 모델이 멀쩡한 노트를 "미완성"이라고 평한다.
func TestALongFileIsTruncatedForTheModelAndSaidSo(t *testing.T) {
	long := strings.Repeat("가", draftViewLimit+500)
	p := Ask{File: file("long.md", long)}.prompt()

	if strings.Contains(p, strings.Repeat("가", draftViewLimit+1)) {
		t.Error("the whole file was sent to the model")
	}
	if !strings.Contains(p, "노트에는 파일 전체가 그대로 들어가니") {
		t.Errorf("truncation was silent:\n%s", p[len(p)-400:])
	}
}

// 잘리는 건 모델이 읽는 몫뿐이다. 파일은 통째로 노트에 들어간다.
func TestTruncationNeverReachesTheNote(t *testing.T) {
	b, w := writingBrain(nil)
	long := "# 긴 글\n\n" + strings.Repeat("스트리밍은 HTTP/2 위에서 돈다. ", 3000)

	_, err := b.runPropose(context.Background(), json.RawMessage(goodPropose),
		Ask{Text: "올려줘", File: file("long.md", long)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.got[0].Files[0].Content, strings.TrimSpace(long)) {
		t.Error("the note was written from the truncated view instead of the file")
	}
}

// ---- 모델이 죽어 있을 때 쓸 검색어 ----

// Topic은 모델을 못 부를 때만 쓰인다. 그때 봇에게 남은 일은 "같은 주제 노트가 이미
// 있나" 하나뿐인데, 그가 슬랙에 친 말("이거 올려줘")로 찾으면 아무것도 안 나온다.
func TestTopicPrefersTitleThenHeadingThenFilename(t *testing.T) {
	for _, tc := range []struct {
		name, file, content, want string
	}{
		{"프론트매터 title이 첫째", "grpc-streaming.md", attachedFile, "gRPC 스트리밍"},
		{"title이 없으면 첫 H1", "grpc-streaming.md", "# 스트리밍 RPC\n\n본문\n", "스트리밍 RPC"},
		{"둘 다 없으면 파일 이름", "grpc-streaming.md", "본문만 있다\n", "grpc streaming"},
		{"H1은 첫 번째만 본다", "x.md", "머리말\n\n# 앞\n\n# 뒤\n", "앞"},
		// 옵시디언 속성에 title이 있으면 파일 이름보다 그게 그의 말이다.
		{"빈 title은 없는 것으로 친다", "raft-consensus.md", "---\ntitle:\n---\n\n본문\n", "raft consensus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Attached{Name: tc.file, Content: tc.content}).Topic(); got != tc.want {
				t.Errorf("Topic() = %q, want %q", got, tc.want)
			}
		})
	}
}

// 검색어로 쓰이는 값이라 빈 문자열이면 위키 현황이 나온다 — 초안을 던졌는데 통계를
// 받는 그 답이 정확히 Topic이 막으려던 것이다.
func TestTopicIsNeverEmpty(t *testing.T) {
	for _, a := range []Attached{
		{Name: "a.md", Content: ""},
		{Name: "a.md", Content: "---\ntitle:\n---\n"},
		{Name: "a", Content: "\n\n"},
	} {
		if got := a.Topic(); strings.TrimSpace(got) == "" {
			t.Errorf("Topic() was empty for %+v", a)
		}
	}
}
