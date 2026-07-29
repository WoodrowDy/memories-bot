// Package jobs defines the envelope the gateway puts on the queue and the
// worker takes off it. Keeping it in its own package stops the two halves of
// cmd/bot from drifting apart.
package jobs

import "encoding/json"

// Job is one question waiting for an answer.
type Job struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	User     string `json:"user"`
	Text     string `json:"text"`
	EventID  string `json:"event_id,omitempty"`

	// File is the one .md attachment the worker should fetch, or nil.
	//
	// 메타데이터만 실린다. 게이트웨이가 바이트를 받아오면 슬랙의 3초 ack를 놓치고,
	// 놓치면 슬랙이 같은 이벤트를 세 번 보낸다. 받는 자리는 워커다.
	File *File `json:"file,omitempty"`

	// Ignored is what the gateway saw and passed over, already written out for
	// a human. 게이트웨이만 첨부 목록 전체를 본다 — 여기 안 적으면 워커는 무엇이
	// 빠졌는지 알 길이 없고, 우드로는 파일을 던졌는데 아무 말도 못 듣는다.
	Ignored []string `json:"ignored,omitempty"`
}

func (j Job) Encode() ([]byte, error) { return json.Marshal(j) }

func Decode(b []byte) (Job, error) {
	var j Job
	err := json.Unmarshal(b, &j)
	return j, err
}
