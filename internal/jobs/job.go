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
}

func (j Job) Encode() ([]byte, error) { return json.Marshal(j) }

func Decode(b []byte) (Job, error) {
	var j Job
	err := json.Unmarshal(b, &j)
	return j, err
}
