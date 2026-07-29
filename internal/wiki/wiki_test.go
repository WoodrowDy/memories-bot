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
