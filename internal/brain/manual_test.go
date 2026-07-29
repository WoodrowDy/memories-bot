package brain

import (
	"context"
	"strings"
	"testing"
)

// 매뉴얼은 코드에 박힌 문구지만, 그 안의 승인 낱말은 goAhead의 복사본이다. 복사본은
// 언젠가 원본과 어긋나고, 어긋나면 봇이 알려준 대로 쳤는데 PR이 안 열린다 — 그러면
// 그가 못 믿게 되는 건 매뉴얼이 아니라 봇이다. 그래서 여기서 하나씩 넣어본다.
func TestTheManualsGoAheadWordsAllWork(t *testing.T) {
	const marker = "승인 낱말: "

	var line string
	for _, l := range strings.Split(manualText, "\n") {
		if rest, ok := strings.CutPrefix(l, marker); ok {
			line = rest
		}
	}
	if line == "" {
		t.Fatalf("매뉴얼에서 %q 줄을 못 찾았다 — 문구를 바꿨으면 이 테스트도 같이 봐야 한다", marker)
	}

	for _, w := range strings.Split(line, "/") {
		w = strings.TrimSpace(w)
		t.Run(w, func(t *testing.T) {
			if !goAhead("# 노트\n\n본문이다.\n\n" + w) {
				t.Errorf("매뉴얼은 %q로 PR이 열린다고 하는데 goAhead는 거부한다", w)
			}
		})
	}
}

// 매뉴얼이 "코드블록에 넣어 보내세요, 자국은 제가 걷어냅니다"라고 약속하니, 시킨
// 대로 보냈을 때 정말 그대로 들어가는지도 여기서 지킨다. 어긋나면 그가 시킨 대로
// 했는데 노트 위아래에 ```가 박히고, 그건 PR diff를 열어야만 보인다.
func TestTheManualsCodeBlockPromiseHolds(t *testing.T) {
	if !strings.Contains(manualText, "코드블록") {
		t.Fatal("매뉴얼에서 코드블록 안내를 못 찾았다 — 문구를 바꿨으면 이 테스트도 같이 봐야 한다")
	}

	const body = "# 노트\n\n- 첫째 줄\n- 둘째 줄"
	msg := "```\n" + body + "\n```\n\n만들어라"

	if !goAhead(msg) {
		t.Fatal("매뉴얼대로 보냈는데 PR이 안 열린다")
	}
	if got := draftBody(msg, ""); got != body {
		t.Errorf("코드블록 자국이 남거나 본문이 바뀌었다:\n got: %q\nwant: %q", got, body)
	}
}

// 이 문구는 모델을 거치지 않으니 slackBold도 거치지 않는다. **굵게**를 그대로 쓰면
// 슬랙에 별표가 그대로 찍힌다.
func TestTheManualIsSlackMrkdwn(t *testing.T) {
	if strings.Contains(manualText, "**") {
		t.Error("슬랙 굵게는 별표 하나다. ** 를 쓰면 별표가 그대로 보인다")
	}
	// 코드블록을 설명하느라 백틱 세 개를 예시로 찍고 싶어지는 자리다. 짝이 안 맞는
	// 하나면 슬랙이 그 뒤를 통째로 코드블록으로 먹어 매뉴얼 절반이 사라진다.
	if strings.Contains(manualText, "```") {
		t.Error("매뉴얼에 백틱 세 개가 있다. 코드블록은 말로 설명해야 한다")
	}
}

func TestAsksForManualReadsTheWholeMessage(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"그가 물어본 그대로", "사용방법?", true},
		{"띄어쓰기", "사용 방법", true},
		{"짧게", "사용법", true},
		{"도움말", "도움말", true},
		{"영어", "help", true},
		{"대문자도", "HELP", true},
		{"앞에 부름말", "봇 사용법 알려줘", true},
		{"물음형", "어떻게 써?", true},
		{"할 수 있는 것", "너 뭐 할 수 있어?", true},
		{"매뉴얼", "매뉴얼 좀", true},

		// 초안이 매뉴얼로 먹히면 그 글이 통째로 사라진다. 여기가 제일 중요하다.
		{"초안은 매뉴얼이 아니다", gofLike, false},
		{"제목에 사용법이 들어간 초안", "Redis 사용법 정리\n\n## 1. SET\n- 키를 넣는다", false},
		{"사용법을 다룬 짧은 초안도 아니다", "gRPC 사용법 정리한 것", false},
		{"주제를 물으면 매뉴얼이 아니다", "도커 사용법 정리한 거 있어?", false},
		{"위키 질문", "위키 현황", false},
		{"빈 메시지", "   ", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := asksForManual(c.text); got != c.want {
				t.Errorf("asksForManual = %v, want %v for %q", got, c.want, c.text)
			}
		})
	}
}

// 매뉴얼은 즉답이다. 모델을 부르면 느리고, 돈이 들고, 무엇보다 매번 문구가 달라진다.
func TestTheManualNeverReachesTheModel(t *testing.T) {
	f := &fakeLLM{}
	b := New(f, &fakeWiki{}, "m", "WoodrowDy", "memories")

	ans, err := b.Answer(context.Background(), "사용방법?")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.seen) != 0 {
		t.Errorf("모델을 %d번 불렀다", len(f.seen))
	}
	if ans.Turns != 0 || ans.InputTokens != 0 || ans.OutputTokens != 0 {
		t.Errorf("턴/토큰이 잡혔다: %+v", ans)
	}
	if !strings.Contains(ans.Text, "저는 이렇게 씁니다") {
		t.Errorf("매뉴얼이 아니다:\n%s", ans.Text)
	}
}

// 쓰기가 꺼진 채로 배포되면 PR을 못 연다. 그때도 열어준다고 하면 매뉴얼이 거짓말이 된다.
func TestTheManualAdmitsWhenWritingIsOff(t *testing.T) {
	on, _ := writingBrain(nil)
	if strings.Contains(on.manual(), "쓰기가 꺼져") {
		t.Error("쓸 수 있는데 못 쓴다고 한다")
	}

	off := New(&fakeLLM{}, &fakeWiki{}, "m", "o", "r")
	if !strings.Contains(off.manual(), "쓰기가 꺼져") {
		t.Errorf("PR을 못 여는데 연다고 한다:\n%s", off.manual())
	}
}
