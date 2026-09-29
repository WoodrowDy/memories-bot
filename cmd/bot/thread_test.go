package main

import (
	"testing"

	"github.com/WoodrowDy/memories-wiki-bot/internal/jobs"
	"github.com/WoodrowDy/memories-wiki-bot/internal/slackclient"
)

func file(name string, size int) slackclient.ThreadFile {
	return slackclient.ThreadFile{Name: name, URL: "https://files.slack.com/" + name, Size: size}
}

// 스레드에 초안을 두 번 붙였으면 나중 것이 그가 말하는 초안이다. 여기가 뒤집히면
// 그는 고친 파일을 올린 줄 알고 봇은 처음 파일로 PR을 여는데, 그 어긋남은 PR diff를
// 끝까지 봐야 보인다.
func TestPickThreadDraftTakesTheNewestOfHisAttachments(t *testing.T) {
	msgs := []slackclient.ThreadMessage{
		{TS: "1.0", User: "U1", Files: []slackclient.ThreadFile{file("first.md", 100)}},
		{TS: "2.0", User: "B1", Text: "자리는 topics/cs/… 이대로 올릴까요?"},
		{TS: "3.0", User: "U1", Files: []slackclient.ThreadFile{file("second.md", 200)}},
		{TS: "4.0", User: "U1", Text: "올려줘"},
	}
	got := pickThreadDraft(msgs, "U1", "4.0")
	if got == nil {
		t.Fatal("초안을 못 찾았다")
	}
	if got.Name != "second.md" {
		t.Errorf("고른 파일 = %q, want second.md — 뒤에서부터 봐야 한다", got.Name)
	}
}

// 승인 메시지 자신은 건너뛴다. 이 함수는 첨부가 *없는* 승인에만 불리지만, 건너뛰기가
// 없으면 나중에 "파일 + 올려줘"를 스레드에 함께 던졌을 때 같은 파일을 두 번 세게 된다.
func TestPickThreadDraftSkipsTheApprovalMessageItself(t *testing.T) {
	msgs := []slackclient.ThreadMessage{
		{TS: "1.0", User: "U1", Files: []slackclient.ThreadFile{file("draft.md", 100)}},
		{TS: "2.0", User: "U1", Text: "올려줘", Files: []slackclient.ThreadFile{file("wrong.md", 100)}},
	}
	got := pickThreadDraft(msgs, "U1", "2.0")
	if got == nil || got.Name != "draft.md" {
		t.Fatalf("고른 파일 = %v, want draft.md", got)
	}
}

// 남의 첨부는 안 본다. 혼자 쓰는 봇이지만, 남이 스레드에 떨군 파일이 그의 이름으로
// 위키에 올라가는 길을 열어둘 이유가 없다. 봇 자기 답을 걸러내는 것도 같은 한 줄이다.
func TestPickThreadDraftIgnoresOtherPeoplesFiles(t *testing.T) {
	msgs := []slackclient.ThreadMessage{
		{TS: "1.0", User: "U1", Files: []slackclient.ThreadFile{file("mine.md", 100)}},
		{TS: "2.0", User: "U9", Files: []slackclient.ThreadFile{file("theirs.md", 100)}},
	}
	got := pickThreadDraft(msgs, "U1", "3.0")
	if got == nil || got.Name != "mine.md" {
		t.Fatalf("고른 파일 = %v, want mine.md", got)
	}
}

// 고르는 규칙은 jobs.Pick에 맡긴다. 첨부로 던졌을 때와 스레드에서 되찾을 때 서로 다른
// 파일이 뽑히면 그건 설명할 수 없는 동작이 된다. .md가 아닌 것과 상한을 넘은 것이
// 여기서도 똑같이 걸러지는지만 확인한다.
func TestPickThreadDraftDefersToJobsPick(t *testing.T) {
	msgs := []slackclient.ThreadMessage{
		{TS: "1.0", User: "U1", Files: []slackclient.ThreadFile{
			file("screenshot.png", 100),
			file("vault-dump.md", jobs.MaxBytes+1),
		}},
	}
	if got := pickThreadDraft(msgs, "U1", "9.0"); got != nil {
		t.Fatalf("고른 파일 = %v, 아무것도 고르면 안 된다", got)
	}

	msgs[0].Files = append(msgs[0].Files, file("ok.md", 100))
	got := pickThreadDraft(msgs, "U1", "9.0")
	if got == nil || got.Name != "ok.md" {
		t.Fatalf("고른 파일 = %v, want ok.md", got)
	}
}

func TestPickThreadDraftFindsNothingInAnEmptyThread(t *testing.T) {
	if got := pickThreadDraft(nil, "U1", "1.0"); got != nil {
		t.Fatalf("빈 스레드에서 %v를 골랐다", got)
	}
}

// 스레드를 읽는 건 슬랙 호출 하나다. 조건이 하나라도 헐거워지면 평범한 질문마다 호출이
// 붙거나(값), 그가 방금 던진 것과 다른 파일이 올라간다(사고).
func TestWantsThreadDraft(t *testing.T) {
	ok := jobs.Job{ThreadTS: "1.0", TS: "2.0", Text: "올려줘"}

	cases := []struct {
		name string
		job  jobs.Job
		want bool
	}{
		{"스레드 답글 + 승인 + 첨부 없음", ok, true},
		{"첨부가 같이 왔다", with(ok, func(j *jobs.Job) {
			j.File = &jobs.File{Name: "draft.md"}
		}), false},
		{"넘긴 첨부가 있다", with(ok, func(j *jobs.Job) {
			j.Ignored = []string{"a.png — `.md`가 아니라 안 읽었어요"}
		}), false},
		{"스레드를 여는 첫 메시지", with(ok, func(j *jobs.Job) { j.TS = j.ThreadTS }), false},
		{"옛 큐 메시지라 TS가 없다", with(ok, func(j *jobs.Job) { j.TS = "" }), false},
		{"올리라는 말이 없다", with(ok, func(j *jobs.Job) { j.Text = "확인" }), false},
		{"묻는 말이다", with(ok, func(j *jobs.Job) { j.Text = "위키 현황 알려줘" }), false},
	}
	for _, tc := range cases {
		if got := wantsThreadDraft(tc.job); got != tc.want {
			t.Errorf("%s: wantsThreadDraft = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func with(j jobs.Job, f func(*jobs.Job)) jobs.Job {
	f(&j)
	return j
}
