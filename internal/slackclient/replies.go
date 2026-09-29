package slackclient

import (
	"context"
	"errors"
	"net/url"
	"strconv"
)

// ThreadMessage is one message in a thread, cut down to what the bot needs in
// order to find a draft that was attached earlier.
type ThreadMessage struct {
	TS    string
	User  string
	Text  string
	Files []ThreadFile
}

// ThreadFile is an attachment Slack still holds, described well enough to fetch.
type ThreadFile struct {
	Name string
	URL  string // url_private — 봇 토큰을 Bearer로 붙여야 열린다
	Size int
}

// threadLimit bounds one conversations.replies call.
//
// 슬랙은 오래된 것부터 준다. 스레드가 이 수를 넘으면 잘리는 건 *뒤쪽*인데, 초안은
// 거의 언제나 스레드를 여는 첫 메시지라 잘려도 찾는 것은 남는다. 페이지를 넘기지
// 않는 건 그래서다 — 200개를 넘긴 스레드에서 201번째로 붙인 초안을 올리는 건
// 이 봇이 감당해야 할 경우가 아니다.
const threadLimit = 200

// ErrNeedsHistory is what a missing history scope looks like from here.
//
// 슬랙은 이걸 missing_scope 한 낱말로만 알려준다. 그대로 스레드에 올리면 무엇을
// 어디에 추가해야 하는지는 아무도 말해주지 않아서, 사람 말로 바꿔 두는 자리가 여기다.
var ErrNeedsHistory = errors.New("slack: 스레드의 앞 메시지를 못 읽었어요 — 봇 토큰에 " +
	"`channels:history` 스코프가 없어 보여요 (비공개 채널이면 `groups:history`, " +
	"디엠이면 `im:history`). 슬랙 앱 설정에서 추가하고 Reinstall하면 됩니다")

// Replies returns one thread's messages, oldest first.
//
// 왜 이게 필요한가: 봇은 메시지 하나가 곧 대화 하나다. 앞 메시지를 기억하지 못하니
// 초안과 승인이 늘 같은 메시지를 타고 와야 했고, 그래서 확인을 한 번 거치면 같은
// 파일을 두 번 첨부해야 했다.
//
// 그런데 앞 메시지는 슬랙에 그대로 남아 있다. 스레드에 "올려줘" 한 마디만 달았을 때
// 초안을 다시 찾아오는 창구가 여기다. 봇이 상태를 쥐는 게 아니라 슬랙을 다시 읽는
// 것이라, 단발성 구조는 그대로 남는다 — 람다가 죽어도, 큐가 늦어도, 며칠 뒤에
// 답글을 달아도 같은 답이 나온다.
func (c *Client) Replies(ctx context.Context, channel, threadTS string) ([]ThreadMessage, error) {
	q := url.Values{}
	q.Set("channel", channel)
	q.Set("ts", threadTS)
	q.Set("limit", strconv.Itoa(threadLimit))

	var r struct {
		apiResp
		Messages []struct {
			TS    string `json:"ts"`
			User  string `json:"user"`
			Text  string `json:"text"`
			Files []struct {
				Name       string `json:"name"`
				URLPrivate string `json:"url_private"`
				Size       int    `json:"size"`
			} `json:"files"`
		} `json:"messages"`
	}
	if err := c.get(ctx, "conversations.replies", q, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		if r.Error == "missing_scope" {
			return nil, ErrNeedsHistory
		}
		// 낱말을 그대로 싣는다. not_in_channel과 thread_not_found는 고치는 방법이
		// 전혀 다른데, 뭉뚱그리면 로그만 보고는 갈리지 않는다.
		return nil, errors.New("slack: " + r.Error)
	}

	out := make([]ThreadMessage, 0, len(r.Messages))
	for _, m := range r.Messages {
		tm := ThreadMessage{TS: m.TS, User: m.User, Text: m.Text}
		for _, f := range m.Files {
			// url_private이 없으면 받아올 방법이 없다. files:read가 없을 때 슬랙이
			// 파일 항목의 속을 비워 보내는 자리이기도 해서, 없는 건 세지도 않는다.
			if f.URLPrivate == "" {
				continue
			}
			tm.Files = append(tm.Files, ThreadFile{Name: f.Name, URL: f.URLPrivate, Size: f.Size})
		}
		out = append(out, tm)
	}
	return out, nil
}
