package wiki

import (
	"strings"
	"testing"
)

const concurrencyNote = `---
title: 동시성 vs 병렬성
aliases: [동시성, 병렬성, concurrency vs parallelism, concurrency, parallelism]
created: 2026-06-04
updated: 2026-06-04
tags: [cs/concurrency, cs/parallelism]
status: growing
---

# 동시성(Concurrency) vs 병렬성(Parallelism)

## 한 줄 요약

동시성(Concurrency) = 구조(Structure)
병렬성(Parallelism) = 실행(Execution)
`

func TestParseNote(t *testing.T) {
	n := parseNote("topics/cs/concurrency-vs-parallelism.md", concurrencyNote)
	if n.Title != "동시성 vs 병렬성" {
		t.Fatalf("title: %q", n.Title)
	}
	if n.Status != "growing" {
		t.Fatalf("status: %q", n.Status)
	}
	if len(n.Aliases) < 3 {
		t.Fatalf("aliases: %v", n.Aliases)
	}
	if len(n.Tags) != 2 {
		t.Fatalf("tags: %v", n.Tags)
	}
}

func TestScoreMatchAndMiss(t *testing.T) {
	n := parseNote("topics/cs/concurrency-vs-parallelism.md", concurrencyNote)
	if s, snip := scoreNote(n, "동시성이랑 병렬성 정리한 거 있어?"); s <= 0 || snip == "" {
		t.Fatalf("expected positive score+snippet, got score=%d snip=%q", s, snip)
	}
	if s, _ := scoreNote(n, "카프카 정리한 거 있어?"); s != 0 {
		t.Fatalf("expected no match for kafka, got %d", s)
	}
}

func TestCandidatesStripParticle(t *testing.T) {
	found := false
	for _, c := range candidates("동시성이랑") {
		if c == "동시성" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected '동시성' among candidates")
	}
}

// Body drops the frontmatter, so the model never sees it. Frontmatter keeps it
// verbatim, which is the only reason the write path can put it back on a file
// the model rewrote whole (a category README).
func TestParseNoteKeepsTheRawFrontmatterAndTheBodyWithoutIt(t *testing.T) {
	n := parseNote("topics/cs/concurrency-vs-parallelism.md", concurrencyNote)

	if !strings.HasPrefix(n.Frontmatter, "---\ntitle: 동시성 vs 병렬성\n") {
		t.Errorf("frontmatter did not start at the opening delimiter:\n%s", n.Frontmatter)
	}
	if !strings.HasSuffix(n.Frontmatter, "status: growing\n---") {
		t.Errorf("frontmatter did not end at the closing delimiter:\n%s", n.Frontmatter)
	}
	if strings.Contains(n.Body, "status: growing") {
		t.Errorf("body still carries frontmatter:\n%s", n.Body)
	}
	// The two halves must reassemble into the original file byte for byte.
	if n.Frontmatter+n.Body != concurrencyNote {
		t.Error("frontmatter + body != the original note")
	}
}

// 우드로가 옵시디언에서 틀만 잡아 놓고 아직 안 채운 파일. 첨부로 오는 흔한 꼴이다.
const halfFilledNote = `---
title: HTTP/2
aliases: [http2, http 2.0]
created: 2026-07-30
updated:
tags:
status:
---

# HTTP/2

## 한 줄 요약

하나의 TCP 연결에 여러 요청을 동시에 실어 보낸다.
`

// 빈 칸은 빈 칸으로 읽혀야 한다. 안 그러면 다음 줄이 값으로 둔갑한다.
//
// `tags:`의 값을 찾는 정규식이 `\s*`로 공백을 넘기고 있었는데, `\s`는 줄바꿈까지 먹어서
// 바로 아랫줄의 `status:`를 tags의 값으로 물고 왔다. 실측한 값이 `tags=[status:]`였다.
//
// 이게 조용한 버그다. resolveMeta는 "비지 않은 값 = 그가 손으로 적은 값"으로 읽으니,
// 모델이 골라온 태그를 그의 것을 존중한다며 버리고 `status:`라는 태그를 PR에 박는다.
// 봇도 로그도 아무 말을 안 한다 — diff를 열어야만 보인다.
func TestParseFrontmatterReadsNoValuePastTheEndOfItsOwnLine(t *testing.T) {
	n := ParseFrontmatter(halfFilledNote)

	// 빈 칸 — 비어 있어야 "채울 칸"으로 넘어간다.
	if n.Tags != nil {
		t.Errorf("빈 `tags:`가 아랫줄을 값으로 물고 왔다: %q (`[status:]`가 나오면 그 버그가 돌아온 것)", n.Tags)
	}
	if n.Status != "" {
		t.Errorf("빈 `status:`가 값을 물고 왔다: %q", n.Status)
	}

	// 그가 적은 칸 — 그대로 있어야 한다. 고치면서 이쪽을 부수면 그의 값이 사라진다.
	if n.Title != "HTTP/2" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Created != "2026-07-30" {
		t.Errorf("created = %q", n.Created)
	}
	if want := []string{"http2", "http 2.0"}; !equal(n.Aliases, want) {
		t.Errorf("aliases = %q, want %q", n.Aliases, want)
	}
}

// 스칼라도 같은 자리를 밟는다. 빈 `title:` 아래에 `aliases:`가 있으면 제목이
// "aliases:"가 되고, 그건 파일 이름이나 H1로 되메우지도 못한다 — 값이 있으니까.
func TestParseFrontmatterDoesNotTurnTheNextKeyIntoAScalar(t *testing.T) {
	n := ParseFrontmatter("---\ntitle:\naliases: [http2]\nstatus: seedling\n---\n\n# HTTP/2\n")

	if n.Title != "" {
		t.Errorf("title = %q — 빈 칸이어야 parseNote가 H1에서 끌어올 수 있다", n.Title)
	}
	if n.Status != "seedling" {
		t.Errorf("status = %q", n.Status)
	}
}

// 옵시디언 속성 편집기로 태그를 한 번 만지면 그 파일은 이 꼴로 다시 쓰인다. 저장소가
// 볼트와 같아야 하니 이 꼴도 읽어야 한다 — 못 읽으면 빈 목록으로 보이고, 그가 고른
// 태그가 모델이 고른 태그로 조용히 갈린다.
func TestParseFrontmatterReadsObsidiansBlockLists(t *testing.T) {
	n := ParseFrontmatter(`---
title: HTTP/2
aliases:
  - http2
  - "http 2.0"
tags:
  - cs/http
  - cs/network
status: seedling
---

# HTTP/2
`)

	if want := []string{"http2", "http 2.0"}; !equal(n.Aliases, want) {
		// 여기에 cs/http가 섞여 나오면 목록이 다음 키를 넘어 계속 훑은 것이다.
		t.Errorf("aliases = %q, want %q", n.Aliases, want)
	}
	if want := []string{"cs/http", "cs/network"}; !equal(n.Tags, want) {
		t.Errorf("tags = %q, want %q", n.Tags, want)
	}
	if n.Status != "seedling" {
		t.Errorf("status = %q — 목록 뒤의 키를 여전히 읽어야 한다", n.Status)
	}
}

// 컨벤션이 정한 인라인 꼴이 계속 우선이라는 것도 같이 지킨다. 위키의 노트는 전부 이 꼴이다.
func TestParseFrontmatterStillPrefersTheInlineListOnTheKeysOwnLine(t *testing.T) {
	n := ParseFrontmatter("---\ntitle: gRPC\ntags: [cs/grpc, cs/network]\n- 아니다\n---\n\n# gRPC\n")

	if want := []string{"cs/grpc", "cs/network"}; !equal(n.Tags, want) {
		t.Errorf("tags = %q, want %q", n.Tags, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A file with no frontmatter leaves the field empty rather than guessing.
func TestParseNoteLeavesFrontmatterEmptyWhenThereIsNone(t *testing.T) {
	n := parseNote("topics/cs/README.md", "# CS\n\n- [gRPC](grpc.md)\n")
	if n.Frontmatter != "" {
		t.Errorf("invented frontmatter: %q", n.Frontmatter)
	}
	if n.Body != "# CS\n\n- [gRPC](grpc.md)\n" {
		t.Errorf("body = %q", n.Body)
	}
}
