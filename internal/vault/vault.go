// Package vault checks a draft against the one property this wiki has to keep:
// 옵시디언에서 열어도 멀쩡하고, GitHub에서 읽어도 멀쩡한가.
//
// CONVENTIONS.md 첫 줄이 그 규칙이다 — "GitHub에서 읽히는 위키이자, 나중에 옵시디언
// Vault로 그대로 쓰기 위한" 저장소. 노트 47개가 이미 그렇게 쓰여 있다. 그래서 봇의
// 일은 "볼트 호환으로 만들기"가 아니라 **이미 호환인 걸 깨뜨리지 않기**다.
//
// 이 패키지는 세기만 한다. 고쳐 쓰지 않는다.
//
// 본문은 우드로가 쓴 글이고, 봇이 문장을 손대는 순간 "원문 그대로"라는 약속이 무너진다.
// 무엇이 몇 개 있는지 말하고, 고칠지 말지는 사람이 PR에서 정한다. 이 패키지에 문자열을
// 바꿔 돌려주는 함수가 없는 건 실수가 아니라 설계다 — 없으면 잘못 쓸 수도 없다.
package vault

import (
	"fmt"
	"regexp"
	"strings"
)

// Finding is one kind of Obsidian-only syntax found in a draft.
type Finding struct {
	What   string // 사람이 읽는 이름 — "위키링크"
	Count  int
	Sample string // 처음 걸린 것 하나, 길면 자른다
	Why    string // GitHub에서 무슨 일이 나는지
}

// Report is everything Check found. 빈 Report가 정상이다.
type Report struct {
	Findings []Finding
}

// OK reports whether the draft is clean — the common case.
func (r Report) OK() bool { return len(r.Findings) == 0 }

// Total counts every occurrence, not every kind.
func (r Report) Total() int {
	n := 0
	for _, f := range r.Findings {
		n += f.Count
	}
	return n
}

// Lines renders the findings one per line, for Slack and for the PR body alike.
func (r Report) Lines() []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.Sample != "" {
			out = append(out, fmt.Sprintf("%s %d개 (예: `%s`) — %s", f.What, f.Count, f.Sample, f.Why))
			continue
		}
		out = append(out, fmt.Sprintf("%s %d개 — %s", f.What, f.Count, f.Why))
	}
	return out
}

// linkRe finds both [[위키링크]] and ![[임베드]]. 앞 글자가 !인지로 둘을 가른다 —
// RE2에는 lookbehind가 없어서 정규식 하나로는 못 가른다.
var (
	linkRe    = regexp.MustCompile(`\[\[[^\[\]\n]+\]\]`)
	calloutRe = regexp.MustCompile(`(?m)^[ \t]*>[ \t]*\[!([A-Za-z]+)\][-+]?`)
	commentRe = regexp.MustCompile(`(?s)%%.*?%%`)
)

// githubAlerts는 GitHub가 알아보는 콜아웃 종류다.
//
// 2023년부터 GitHub도 `> [!NOTE]` 꼴을 인용문이 아니라 알림 상자로 그린다. 다섯
// 가지뿐이라서, 옵시디언에만 있는 나머지(`[!info]`, `[!todo]`, `[!question]` …)만
// 문제가 된다. 전부 싸잡아 알리면 멀쩡한 것까지 고치게 만든다 — 경고는 정확할 때만
// 쓸모가 있다.
var githubAlerts = map[string]bool{
	"note": true, "tip": true, "important": true, "warning": true, "caution": true,
}

// Check counts the Obsidian-only syntax in a markdown draft.
//
// 코드블록 안은 보지 않는다. 마크다운 문법을 설명하는 노트가 ```블록에 [[링크]]를
// 예시로 적어두는 건 깨진 게 아니라 인용이다.
func Check(md string) Report {
	s := blankCode(md)
	var r Report

	var wiki, embed []string
	for _, loc := range linkRe.FindAllStringIndex(s, -1) {
		text := s[loc[0]:loc[1]]
		if loc[0] > 0 && s[loc[0]-1] == '!' {
			embed = append(embed, "!"+text)
			continue
		}
		wiki = append(wiki, text)
	}
	r.add("위키링크", wiki, "GitHub에선 링크가 아니라 글자 그대로 보인다. 규칙은 `[제목](path.md)`")
	r.add("임베드", embed, "GitHub에선 그림도 노트도 안 붙는다")

	var odd []string
	for _, m := range calloutRe.FindAllStringSubmatch(s, -1) {
		if githubAlerts[strings.ToLower(m[1])] {
			continue // GitHub도 아는 다섯 가지 — 양쪽 다 멀쩡하다
		}
		odd = append(odd, strings.TrimSpace(m[0]))
	}
	r.add("옵시디언 전용 콜아웃", odd,
		"GitHub는 note·tip·important·warning·caution만 안다. 나머지는 그냥 인용문이 된다")

	r.add("옵시디언 주석", commentRe.FindAllString(s, -1), "GitHub에선 숨겨지지 않고 그대로 보인다")

	return r
}

func (r *Report) add(what string, hits []string, why string) {
	if len(hits) == 0 {
		return
	}
	r.Findings = append(r.Findings, Finding{
		What: what, Count: len(hits), Sample: sample(hits[0]), Why: why,
	})
}

// sample trims one hit down to something that fits on a Slack line.
func sample(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 40
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// blankCode empties out code so the counters never fire on quoted syntax.
//
// 줄 수는 그대로 둔다 — 콜아웃 검사가 (?m)^로 줄머리를 보기 때문에, 줄을 지워
// 없애면 앞뒤가 붙어서 없던 게 생기거나 있던 게 사라진다.
func blankCode(md string) string {
	lines := strings.Split(md, "\n")
	inFence := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			lines[i] = ""
			continue
		}
		if inFence {
			lines[i] = ""
			continue
		}
		lines[i] = blankInline(ln)
	}
	return strings.Join(lines, "\n")
}

// blankInline drops `code` spans on one line, backticks and all.
func blankInline(ln string) string {
	var b strings.Builder
	in := false
	for _, c := range ln {
		if c == '`' {
			in = !in
			continue
		}
		if in {
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
