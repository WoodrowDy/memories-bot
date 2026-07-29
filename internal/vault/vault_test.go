package vault

import (
	"strings"
	"testing"
)

// 깨끗한 초안에는 아무 말도 하지 않는다. 이게 대부분의 경우다 — 노트 47개가 이미
// 규칙대로 쓰여 있고, 경고가 매번 뜨면 아무도 안 읽게 된다.
func TestACleanDraftSaysNothing(t *testing.T) {
	md := `# 동시성

표준 링크는 [고루틴](topics/cs/goroutine.md)처럼 쓴다.

> [!NOTE]
> GitHub도 이건 알아본다.

` + "```go\nfor i := range 10 { go work(i) }\n```" + `
`
	if r := Check(md); !r.OK() {
		t.Fatalf("clean draft flagged: %v", r.Lines())
	}
}

func TestWikilinksAreCountedNotFixed(t *testing.T) {
	md := "여기 [[동시성]]과 [[고루틴]]을 보자."

	r := Check(md)
	if len(r.Findings) != 1 {
		t.Fatalf("findings = %d, want 1: %v", len(r.Findings), r.Lines())
	}
	if r.Findings[0].What != "위키링크" || r.Findings[0].Count != 2 {
		t.Errorf("got %+v, want 위키링크 x2", r.Findings[0])
	}
	if r.Total() != 2 {
		t.Errorf("Total() = %d, want 2", r.Total())
	}
	// 이 패키지는 문자열을 돌려주지 않는다. 세고 말할 뿐이다.
	if !strings.Contains(r.Lines()[0], "[[동시성]]") {
		t.Errorf("sample missing from line: %q", r.Lines()[0])
	}
}

// ![[...]]는 위키링크가 아니라 임베드다. 둘을 한 줄로 합쳐 세면 "링크 3개"라고
// 말하면서 정작 그림이 안 뜬다는 건 못 알려준다.
func TestAnEmbedIsNotCountedAsALink(t *testing.T) {
	r := Check("![[diagram.png]] 그리고 [[동시성]]")

	var kinds []string
	for _, f := range r.Findings {
		kinds = append(kinds, f.What)
		if f.Count != 1 {
			t.Errorf("%s count = %d, want 1", f.What, f.Count)
		}
	}
	if len(kinds) != 2 {
		t.Fatalf("kinds = %v, want 위키링크 and 임베드", kinds)
	}
}

// GitHub가 아는 다섯 가지는 옵시디언에서도 그려진다 — 양쪽 다 멀쩡하니 할 말이 없다.
func TestGitHubsOwnAlertsAreLeftAlone(t *testing.T) {
	for _, kind := range []string{"NOTE", "tip", "Important", "WARNING", "caution"} {
		if r := Check("> [!" + kind + "]\n> 본문"); !r.OK() {
			t.Errorf("[!%s] flagged: %v", kind, r.Lines())
		}
	}
}

func TestObsidianOnlyCalloutsAreFlagged(t *testing.T) {
	r := Check("> [!info] 참고\n> 본문\n\n> [!todo]-\n> 할 일")

	if len(r.Findings) != 1 || r.Findings[0].Count != 2 {
		t.Fatalf("got %v, want one finding of 2", r.Lines())
	}
	if !strings.Contains(r.Findings[0].Why, "caution") {
		t.Errorf("why should name what GitHub does know: %q", r.Findings[0].Why)
	}
}

func TestObsidianCommentsAreFlagged(t *testing.T) {
	r := Check("본문\n\n%%\n이건 나만 보는 메모\n%%\n\n끝")

	if len(r.Findings) != 1 || r.Findings[0].What != "옵시디언 주석" {
		t.Fatalf("got %v, want 옵시디언 주석", r.Lines())
	}
}

// 마크다운 문법을 설명하는 노트는 코드블록 안에 [[링크]]를 예시로 적는다. 그건
// 깨진 게 아니라 인용이라서, 세면 그 노트는 영영 경고를 달고 다닌다.
func TestSyntaxQuotedInCodeIsNotAFinding(t *testing.T) {
	md := "옵시디언은 이렇게 쓴다:\n\n```\n[[위키링크]]\n> [!info] 콜아웃\n%% 주석 %%\n```\n\n인라인도 `[[이렇게]]` 인용한다."

	if r := Check(md); !r.OK() {
		t.Fatalf("quoted syntax flagged: %v", r.Lines())
	}
}

// blankCode가 줄을 지워 없애면 앞뒤 줄이 붙는다. 붙으면 인용문이 아니던 게 줄머리로
// 올라와 콜아웃으로 잡히거나, 그 반대가 된다.
func TestBlankingCodeKeepsTheLineCount(t *testing.T) {
	md := "첫 줄\n```\n코드\n```\n> [!info] 콜아웃"

	if got, want := strings.Count(blankCode(md), "\n"), strings.Count(md, "\n"); got != want {
		t.Fatalf("newlines = %d, want %d", got, want)
	}
	if r := Check(md); len(r.Findings) != 1 {
		t.Errorf("findings = %v, want the callout below the fence", r.Lines())
	}
}

func TestAHitIsTrimmedToOneSlackLine(t *testing.T) {
	long := "[[" + strings.Repeat("가", 100) + "]]"

	got := Check(long).Findings[0].Sample
	if r := []rune(got); len(r) > 41 {
		t.Errorf("sample is %d runes, too long for a line: %q", len(r), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a trimmed sample should say it was trimmed: %q", got)
	}
}
