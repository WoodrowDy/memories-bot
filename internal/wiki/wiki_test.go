package wiki

import "testing"

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
