// Package audit records who did what. In A0 it writes structured JSON to stdout
// (CloudWatch Logs); move it to DynamoDB when the team grows (A4).
package audit

import (
	"encoding/json"
	"log"
	"time"
)

type Entry struct {
	Timestamp   string `json:"timestamp"`
	UserID      string `json:"userId"`
	ChannelID   string `json:"channelId"`
	Service     string `json:"service"`
	Environment string `json:"environment"`
	Tool        string `json:"tool"`
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason,omitempty"`
	Success     bool   `json:"success"`
	HTTPStatus  int    `json:"httpStatus,omitempty"`
	LatencyMs   int64  `json:"latencyMs,omitempty"`
}

func Log(e Entry) {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		log.Printf("audit marshal error: %v", err)
		return
	}
	log.Printf("AUDIT %s", b)
}
