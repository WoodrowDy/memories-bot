package jobs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAMarkdownFileIsPicked(t *testing.T) {
	got, ignored := Pick([]File{{Name: "동시성.md", URL: "https://files.slack.com/x", Size: 1200}})

	if got == nil {
		t.Fatal("a .md attachment was not picked")
	}
	if got.Name != "동시성.md" || got.URL != "https://files.slack.com/x" {
		t.Errorf("picked %+v", *got)
	}
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want none", ignored)
	}
}

func TestOnlyMarkdownIsFetched(t *testing.T) {
	got, ignored := Pick([]File{
		{Name: "설계.png", URL: "u1", Size: 10},
		{Name: "메모.txt", URL: "u2", Size: 10},
		{Name: "노트.MARKDOWN", URL: "u3", Size: 10},
	})

	if got == nil || got.Name != "노트.MARKDOWN" {
		t.Fatalf("picked %+v, want the .markdown one (확장자는 대소문자를 안 가린다)", got)
	}
	if len(ignored) != 2 {
		t.Fatalf("ignored = %v, want both non-markdown files named", ignored)
	}
	for _, s := range ignored {
		if !strings.Contains(s, ".md") {
			t.Errorf("이유가 안 적혔다: %q", s)
		}
	}
}

// 상한은 코드에 있다. 프롬프트에 있으면 모델이 봐주기 나름이 된다.
func TestATooBigFileIsRefusedBeforeItIsFetched(t *testing.T) {
	got, ignored := Pick([]File{{Name: "덤프.md", URL: "u", Size: MaxBytes + 1}})

	if got != nil {
		t.Fatal("상한을 넘은 파일을 받으러 갔다")
	}
	if len(ignored) != 1 || !strings.Contains(ignored[0], "상한") {
		t.Errorf("ignored = %v, want the size named", ignored)
	}
}

// 슬랙이 size를 안 줄 때가 있다. 그건 통과시키고 워커가 실제 바이트로 다시 잰다 —
// 여기서 0을 "너무 크다"로 읽으면 멀쩡한 파일이 통째로 막힌다.
func TestAMissingSizeIsNotTreatedAsTooBig(t *testing.T) {
	if got, _ := Pick([]File{{Name: "노트.md", URL: "u"}}); got == nil {
		t.Fatal("size가 0인 첨부를 거절했다")
	}
}

func TestTheSecondMarkdownFileIsNamedNotSwallowed(t *testing.T) {
	got, ignored := Pick([]File{
		{Name: "첫째.md", URL: "u1", Size: 10},
		{Name: "둘째.md", URL: "u2", Size: 10},
	})

	if got == nil || got.Name != "첫째.md" {
		t.Fatalf("picked %+v, want 첫째.md", got)
	}
	if len(ignored) != 1 || !strings.Contains(ignored[0], "둘째.md") {
		t.Errorf("ignored = %v, want 둘째.md 이름이 그대로 나오기", ignored)
	}
}

func TestNoAttachmentsIsNotAnEvent(t *testing.T) {
	got, ignored := Pick(nil)
	if got != nil || ignored != nil {
		t.Errorf("Pick(nil) = %v, %v, want nothing at all", got, ignored)
	}
}

// 파일 메타데이터가 SQS를 건너오지 못하면 워커는 첨부가 있었다는 사실조차 모른다.
func TestTheAttachmentSurvivesTheQueue(t *testing.T) {
	in := Job{
		Channel: "C1", ThreadTS: "1.0", Text: "올려줘",
		File:    &File{Name: "노트.md", URL: "https://files.slack.com/x", Size: 42},
		Ignored: []string{"설계.png — `.md`가 아니라 안 읽었어요"},
	}

	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.File == nil || *out.File != *in.File {
		t.Fatalf("file = %+v, want %+v", out.File, in.File)
	}
	if len(out.Ignored) != 1 || out.Ignored[0] != in.Ignored[0] {
		t.Errorf("ignored = %v, want %v", out.Ignored, in.Ignored)
	}
}

// 첨부 없는 평범한 질문은 예전과 똑같은 봉투로 간다. 큐에 이미 들어가 있는 메시지가
// 새 워커에서 깨지지 않는 것도 같은 이유로 확인해 둔다.
func TestAPlainQuestionCarriesNoFileKeys(t *testing.T) {
	b, err := Job{Channel: "C1", Text: "동시성 정리한 거 있어?"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"file", "ignored"} {
		if _, ok := m[k]; ok {
			t.Errorf("%q가 첨부도 없는 봉투에 실렸다: %s", k, b)
		}
	}

	old, err := Decode([]byte(`{"channel":"C1","thread_ts":"1.0","text":"안녕"}`))
	if err != nil || old.File != nil {
		t.Errorf("옛 봉투 = %+v, %v", old, err)
	}
}
