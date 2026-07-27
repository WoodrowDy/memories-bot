package slackevents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// Verify checks the Slack request signature and rejects stale timestamps.
// Signature scheme: HMAC-SHA256 over "v0:{timestamp}:{rawBody}" with the app's
// signing secret. rawBody MUST be the exact bytes Slack sent (decode base64
// from API Gateway before calling this).
func Verify(headers map[string]string, rawBody, signingSecret string, now time.Time) bool {
	if signingSecret == "" {
		return false
	}
	ts := header(headers, "X-Slack-Request-Timestamp")
	sig := header(headers, "X-Slack-Signature")
	if ts == "" || sig == "" {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if diff := now.Unix() - n; diff > 300 || diff < -300 {
		return false // older or newer than 5 minutes -> reject (replay protection)
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte("v0:" + ts + ":" + rawBody))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// IsRetry reports whether Slack is redelivering an event (X-Slack-Retry-Num).
func IsRetry(headers map[string]string) bool {
	return header(headers, "X-Slack-Retry-Num") != ""
}

// header looks a key up case-insensitively (API Gateway HTTP API lowercases keys).
func header(headers map[string]string, key string) string {
	if v, ok := headers[key]; ok {
		return v
	}
	lk := lower(key)
	for k, v := range headers {
		if lower(k) == lk {
			return v
		}
	}
	return ""
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
