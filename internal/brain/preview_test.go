package brain

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// layeredDraft is the file 우드로 actually threw at the bot on 2026-07-30, down to
// the block-style lists 옵시디언's property editor writes. `cs/layering` is the
// tag that matters: 모델이라면 `cs/design`을 고를 자리에 그가 직접 적어둔 값이다.
const layeredDraft = `---
title: 레이어드 아키텍처
aliases:
  - layered architecture
  - 계층형 아키텍처
created: 2026-07-30
updated:
tags:
  - cs/architecture
  - cs/layering
status:
---

# 레이어드 아키텍처

> 백엔드(NestJS / Spring) 관점에서 정리.

## 핵심

의존 방향을 한쪽으로만 흐르게 한다.
`

// layeredPropose is what the model sends: its own tags and aliases, which lose
// to his. status는 그가 비워뒀으니 이것만 실제로 쓰인다.
const layeredPropose = `{
  "path": "topics/cs/layered-architecture.md",
  "mode": "create",
  "title": "레이어드 아키텍처",
  "status": "seedling",
  "tags": ["cs/design", "cs/ddd"],
  "aliases": ["layered"],
  "summary": "층을 다루는 노트가 아직 없어서 cs에 새 노트로 넣었어요."
}`

func preview(t *testing.T, b *Brain, args string, ask Ask) previewOut {
	t.Helper()
	raw, err := b.runTool(context.Background(), "preview_note", json.RawMessage(args), ask)
	if err != nil {
		t.Fatalf("preview_note: %v", err)
	}
	var out previewOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("preview_note 응답을 못 읽었다: %v — %s", err, raw)
	}
	return out
}

// 이 테스트가 고침의 이유 그 자체다. 확인 라운드에서 모델이 손으로 적어 보여준 블록에는
// `cs/layering`이 없었고, PR에는 있었다 — 값을 정하는 건 코드인데 보여주는 건 모델이라서
// 갈렸다. 그리고 그 블록은 "복사해 초안 맨 위에 붙이세요"라고 안내되는 것이라, 그가
// 그대로 붙였으면 자기가 적은 태그를 자기 손으로 지우게 된다.
func TestPreviewKeepsTheTagsHeWroteHimself(t *testing.T) {
	b, _ := writingBrain(nil)

	out := preview(t, b, layeredPropose, Ask{
		Text: "확인",
		File: &Attached{Name: "layered-architecture.md", Content: layeredDraft},
	})

	for _, want := range []string{"cs/architecture", "cs/layering"} {
		if !strings.Contains(out.Frontmatter, want) {
			t.Errorf("미리보기에서 %q가 빠졌다 — 그가 파일에 적은 태그다:\n%s", want, out.Frontmatter)
		}
	}
	for _, unwanted := range []string{"cs/design", "cs/ddd", "layered]"} {
		if strings.Contains(out.Frontmatter, unwanted) {
			t.Errorf("모델이 고른 %q가 미리보기에 들어갔다 — 그가 적은 값이 이겨야 한다:\n%s",
				unwanted, out.Frontmatter)
		}
	}

	// 빈 칸이었던 것만 "봇이 채운 칸"이다. aliases·tags가 여기 끼면 그가 적은 값을
	// 덮었다는 뜻이고, 그건 PR 본문과도 어긋난다.
	if len(out.Filled) != 1 || out.Filled[0] != "status" {
		t.Errorf("filled = %v, want [status]", out.Filled)
	}
}

// 미리보기와 PR이 같은 여섯 줄이어야 한다. 이 둘이 갈릴 수 있는 한 미리보기는
// 미리보기가 아니라 별개의 추측이고, 그가 확인 답을 보고 내리는 판단의 근거가 없어진다.
func TestPreviewMatchesTheFileThePRWouldWriteByteForByte(t *testing.T) {
	ask := Ask{
		Text: "올려줘",
		File: &Attached{Name: "layered-architecture.md", Content: layeredDraft},
	}

	pb, _ := writingBrain(nil)
	out := preview(t, pb, layeredPropose, Ask{Text: "확인", File: ask.File})

	wb, w := writingBrain(nil)
	if _, err := wb.runTool(context.Background(), "propose_note", json.RawMessage(layeredPropose), ask); err != nil {
		t.Fatalf("propose_note: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("PR 제안 %d건", len(w.got))
	}
	filed := w.got[0].Files[0].Content

	if !strings.HasPrefix(filed, out.Frontmatter) {
		t.Errorf("보여준 것과 심은 것이 다르다.\n미리보기:\n%s\n\n파일 맨 위:\n%s",
			out.Frontmatter, filed[:min(len(filed), len(out.Frontmatter)+40)])
	}
}

// 읽기 툴이라는 것을 지킨다. 확인 라운드에서 PR이 열리면 그가 값을 보기 전에 이미
// 올라간 것이고, 그 순서를 뒤집는 것이 이 봇에서 제일 하면 안 되는 일이다.
func TestPreviewOpensNoPR(t *testing.T) {
	b, w := writingBrain(nil)

	// 올리라는 말이 같이 왔더라도 이 툴은 PR을 열지 않는다.
	preview(t, b, layeredPropose, Ask{
		Text: "올려줘",
		File: &Attached{Name: "layered-architecture.md", Content: layeredDraft},
	})

	if len(w.got) != 0 {
		t.Fatalf("preview_note가 PR을 열었다: %+v", w.got)
	}
}

// 쓰기가 꺼진 배포에서도 확인 답은 나와야 한다. 그때 그가 받는 건 프론트매터 한 블록이
// 전부라서, 여기서 툴이 없으면 모델이 다시 손으로 적는 자리로 돌아간다.
func TestPreviewWorksWithWritingOff(t *testing.T) {
	b := New(&fakeLLM{}, &fakeWiki{}, "m", "WoodrowDy", "memories")
	b.now = pinnedNow

	out := preview(t, b, layeredPropose, Ask{
		Text: "확인",
		File: &Attached{Name: "layered-architecture.md", Content: layeredDraft},
	})
	if !strings.Contains(out.Frontmatter, "cs/layering") {
		t.Errorf("쓰기가 꺼졌을 때 미리보기가 비었다:\n%s", out.Frontmatter)
	}
}

// 붙여넣기로 온 초안에는 "그가 적은 칸"이 없다. 전부 봇이 정한 것이고, 그걸 다 "봇이
// 채운 칸"으로 적어봐야 알려주는 게 없다 — PR 본문과 같은 규칙을 미리보기도 따른다.
func TestPreviewReportsNothingFilledWhenNothingWasHis(t *testing.T) {
	b, _ := writingBrain(nil)

	out := preview(t, b, layeredPropose, Ask{Text: layeredDraft + "\n\n확인"})
	if len(out.Filled) != 0 {
		t.Errorf("filled = %v, 붙여넣기 초안에는 빈 칸이라는 게 없다", out.Filled)
	}
	if !strings.Contains(out.Frontmatter, "cs/design") {
		t.Errorf("파일이 없으면 모델이 고른 태그가 쓰인다:\n%s", out.Frontmatter)
	}
}

// 확인이 통과했는데 올려줘가 거부되는 일이 없어야 한다. mode 검사는 두 툴이 같은
// 함수를 본다.
func TestPreviewChecksModeAgainstTheRepoJustLikeThePR(t *testing.T) {
	b, _ := writingBrain(map[string]wiki.Note{
		"topics/cs/layered-architecture.md": {Title: "레이어드 아키텍처", Body: "이미 있다"},
	})

	_, err := b.runTool(context.Background(), "preview_note", json.RawMessage(layeredPropose), Ask{Text: "확인"})
	if err == nil {
		t.Fatal("이미 있는 노트에 create를 통과시켰다")
	}
	if !strings.Contains(err.Error(), "이미 있어요") {
		t.Errorf("err = %q", err)
	}
}

// 인자 검사도 같은 함수다. 확인 라운드에서 걸러지면 그는 한 번에 고칠 수 있고, 여기서
// 통과한 것이 PR에서 걸리면 두 번 던져야 한다.
func TestPreviewRefusesWhatThePRWouldRefuse(t *testing.T) {
	b, _ := writingBrain(nil)

	cases := map[string]string{
		"한글 파일명": `{"path":"topics/cs/레이어드.md","mode":"create","title":"t","status":"seedling","tags":["cs/a"],"summary":"s"}`,
		"태그 없음":  `{"path":"topics/cs/a.md","mode":"create","title":"t","status":"seedling","summary":"s"}`,
		"읽기 전용":  `{"path":"CONVENTIONS.md","mode":"update","title":"t","summary":"s"}`,
		"새 노트에 evergreen": `{"path":"topics/cs/a.md","mode":"create","title":"t","status":"evergreen",` +
			`"tags":["cs/a"],"summary":"s"}`,
	}
	for name, args := range cases {
		if _, err := b.runTool(context.Background(), "preview_note", json.RawMessage(args), Ask{Text: "확인"}); err == nil {
			t.Errorf("%s: 통과했다", name)
		}
	}
}

// 두 툴이 받는 칸이 같아야 한다. 하나에만 있는 칸이 생기면 모델은 확인에서 준 값을
// 올릴 때 못 주거나 그 반대가 되고, 그 어긋남은 실행 중에만 보인다.
func TestPreviewAndProposeAgreeOnTheirFields(t *testing.T) {
	fields := func(tool map[string]any) []string {
		var out []string
		for k := range tool["properties"].(map[string]any) {
			out = append(out, k)
		}
		return out
	}
	prev := fields(previewToolDef().InputSchema)
	prop := fields(proposeToolDef().InputSchema)

	for _, f := range prev {
		if !slices.Contains(prop, f) {
			t.Errorf("preview_note에만 있는 칸: %q", f)
		}
	}
	// propose_note에만 있는 칸은 본문과 다른 파일뿐이다 — 미리보기가 건드리지 않는 것들.
	extra := map[string]bool{"body_from": true, "also": true}
	for _, f := range prop {
		if !slices.Contains(prev, f) && !extra[f] {
			t.Errorf("propose_note에만 있는 칸: %q — 미리보기에도 있어야 한다", f)
		}
	}
}
