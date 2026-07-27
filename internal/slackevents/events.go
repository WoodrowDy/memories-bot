package slackevents

import "encoding/json"

// AppMention is the subset of a Slack app_mention event we act on.
type AppMention struct {
	Channel  string
	User     string
	Text     string
	TS       string
	ThreadTS string
}

type outer struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Event     json.RawMessage `json:"event"`
}

type innerEvent struct {
	Type     string `json:"type"`
	Channel  string `json:"channel"`
	User     string `json:"user"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
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
	return AppMention{
		Channel:  ie.Channel,
		User:     ie.User,
		Text:     ie.Text,
		TS:       ie.TS,
		ThreadTS: ie.ThreadTS,
	}, true
}
