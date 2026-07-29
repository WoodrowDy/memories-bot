package brain

import (
	"regexp"
	"strings"
)

// The manual is a fixed string, not something the model writes.
//
// 무엇을 할 수 있는지는 판단이 아니라 사실이다. 모델에게 맡기면 언젠가 없는 기능을
// 그럴듯하게 지어내고, 그 말을 믿고 쓰다 안 되는 게 제일 나쁘다. 그래서 이 답만은
// LLM을 거치지 않고 코드에서 그대로 나간다 — 즉답이고, 토큰을 쓰지 않고, 항상 같다.
//
// 대신 여기 적힌 낱말이 실제 코드와 어긋날 수 있다. 승인 낱말 줄은
// TestTheManualsGoAheadWordsAllWork가 goAhead에 하나씩 넣어보며 지킨다.
const manualText = `*저는 이렇게 씁니다*

• *공부한 글을 그냥 붙여넣으면* → 같은 주제 노트가 있는지 찾아보고, 어디에 둘지·파일 상단 프론트매터·status를 추천해요. PR은 안 열어요.
• *초안이랑 같이 "만들어라"* → 추천한 자리로 PR을 엽니다. 머지는 직접 하시면 돼요. 이때 처음 보내주신 본문을 마크다운 코드블록에 그대로 넣어주세요.
• *"X 정리한 거 있어?"* → 위키에서 찾아 경로랑 링크로 답해요.
• *"위키 현황"* → 노트 수랑 status 분포.

승인 낱말: 만들어라 / 올려줘 / 넣어줘 / PR / ㄱㄱ / 그래 / 동의
저는 앞 메시지를 기억하지 못해요 — 승인은 초안과 *같은 메시지*에 넣어주세요.
슬랙 입력창이 마크다운을 먹어요. 그냥 붙이면 "- "는 서식 불릿이 되고 "#"은 사라지니, 본문은 마크다운 코드블록에 넣어 보내주세요. 블록 자국은 제가 걷어내고 넣습니다.`

// noWriteNote is appended when GITHUB_WRITE_TOKEN is missing. 매뉴얼이 열지도
// 못할 PR을 약속하면 그건 거짓말이 된다.
const noWriteNote = "\n\n(지금은 쓰기가 꺼져 있어서 PR은 못 열어요. 자리 추천까지만 해드립니다.)"

func (b *Brain) manual() string {
	if !b.canWrite() {
		return manualText + noWriteNote
	}
	return manualText
}

// manualAskRunes caps how long a message can be and still be a request for the
// manual. 사용법을 묻는 말은 짧다.
const manualAskRunes = 25

// asksForManual reports whether the message is *nothing but* a request for the
// manual.
//
// 판정은 메시지 전체를 건다. 낱말만 찾으면 "Redis 사용법 정리" 같은 초안이 위키가
// 아니라 매뉴얼을 받고 끝난다 — 초안 한 편이 통째로 무시되는 건 조용한 실패라서,
// 애매하면 못 알아듣고 모델에게 넘기는 쪽으로 기운다. 매뉴얼을 못 알아들으면
// 모델이 비슷하게 답하고 말지만, 초안을 매뉴얼로 받으면 그가 쓴 글이 사라진다.
func asksForManual(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || len([]rune(t)) > manualAskRunes {
		return false
	}
	// 공백과 문장부호를 먼저 걷어내고 통째로 맞춘다. 정규식 하나에 띄어쓰기 경우의
	// 수를 다 넣는 것보다 읽기 쉽고, 낱말이 문장 *안에* 있는 경우는 앵커가 걸러낸다.
	return manualAsk.MatchString(manualNoise.ReplaceAllString(strings.ToLower(t), ""))
}

// manualNoise is what gets thrown away before matching: 공백, 문장부호, 그리고
// 뜻을 바꾸지 않는 꼬리말.
var manualNoise = regexp.MustCompile(`[\s,?!.~ㅋㅎ]+|좀`)

var manualAsk = regexp.MustCompile(`^(?:이거|이건|봇|너|넌|니가|네가|야|저기)*(?:` +
	// 명사로 묻는 쪽 — "사용법?", "도움말", "매뉴얼 좀 알려줘"
	`(?:사용법|사용방법|쓰는법|사용설명서|설명서|매뉴얼|메뉴얼|도움말|헬프|help|명령어|기능)` +
	`(?:은|는|이|가|을|를)*(?:뭐야|뭐|뭔데|알려줘|알려주세요|보여줘|설명해줘|어떻게돼|어떻게되)*` +
	// 물음으로 묻는 쪽 — "어떻게 써?", "뭐 할 수 있어?"
	`|어떻게(?:쓰는거|쓰는|써|사용해|사용하는거|사용하는)(?:야|니|에요|예요|죠|나요|지)*` +
	`|(?:뭐|뭘|무엇)(?:을)*(?:할|해줄|하는)수*있(?:어|나|니|는지|어요|나요)*` +
	`)요*$`)
