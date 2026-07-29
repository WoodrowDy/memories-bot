package slackevents

import "encoding/json"

// AppMention is the subset of a Slack app_mention event we act on.
type AppMention struct {
	Channel  string
	User     string
	Text     string
	TS       string
	ThreadTS string

	// Files are the attachments Slack listed on the mention — metadata only.
	//
	// 바이트는 여기서 안 받는다. 게이트웨이는 슬랙에 3초 안에 ack해야 하고, 파일
	// 하나 받아오는 데 그 시간이 통째로 날아갈 수 있다. 받는 건 워커 일이다.
	Files []File
}

// File is one attachment, as Slack describes it before anything is fetched.
type File struct {
	ID         string
	Name       string
	Title      string
	URLPrivate string // 봇 토큰을 Bearer로 붙여야 열린다 — 공개 URL이 아니다
	Size       int
	Filetype   string
	Mimetype   string
}

type outer struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Event     json.RawMessage `json:"event"`
}

type innerEvent struct {
	Type     string      `json:"type"`
	Channel  string      `json:"channel"`
	User     string      `json:"user"`
	Text     string      `json:"text"`
	TS       string      `json:"ts"`
	ThreadTS string      `json:"thread_ts"`
	Files    []innerFile `json:"files"`
}

type innerFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Title      string `json:"title"`
	URLPrivate string `json:"url_private"`
	Size       int    `json:"size"`
	Filetype   string `json:"filetype"`
	Mimetype   string `json:"mimetype"`
}

// Challenge returns the url_verification challenge string if this is that request.
func Challenge(body string) (string, bool) {
	var o outer
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		return "", false
	}
	if o.Type == "url_verification" && o.Challenge != "" {
		return o.Challenge, true
	}
	return "", false
}

// ParseAppMention extracts an app_mention event, reporting false for anything else.
func ParseAppMention(body string) (AppMention, bool) {
	var o outer
	if err := json.Unmarshal([]byte(body), &o); err != nil || len(o.Event) == 0 {
		return AppMention{}, false
	}
	var ie innerEvent
	if err := json.Unmarshal(o.Event, &ie); err != nil {
		return AppMention{}, false
	}
	if ie.Type != "app_mention" {
		return AppMention{}, false
	}
	m := AppMention{
		Channel:  ie.Channel,
		User:     ie.User,
		Text:     ie.Text,
		TS:       ie.TS,
		ThreadTS: ie.ThreadTS,
	}
	for _, f := range ie.Files {
		// url_private 없이는 아무것도 못 하니 그런 항목은 애초에 싣지 않는다.
		// 슬랙이 파일을 지웠거나 아직 업로드 중이면 이 칸이 빈다.
		if f.URLPrivate == "" {
			continue
		}
		m.Files = append(m.Files, File{
			ID: f.ID, Name: f.Name, Title: f.Title, URLPrivate: f.URLPrivate,
			Size: f.Size, Filetype: f.Filetype, Mimetype: f.Mimetype,
		})
	}
	return m, true
}
