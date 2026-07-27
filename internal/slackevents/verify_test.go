package slackevents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func sign(secret, ts, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerify(t *testing.T) {
	secret := "shhh"
	now := time.Unix(1600000000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := `{"type":"event_callback"}`

	good := map[string]string{
		"X-Slack-Request-Timestamp": ts,
		"X-Slack-Signature":         sign(secret, ts, body),
	}
	if !Verify(good, body, secret, now) {
		t.Fatal("valid signature should pass")
	}
	if Verify(good, body+"tampered", secret, now) {
		t.Fatal("tampered body should fail")
	}
	if Verify(good, body, secret, now.Add(10*time.Minute)) {
		t.Fatal("stale timestamp should fail")
	}
	if Verify(good, body, "", now) {
		t.Fatal("empty signing secret should fail")
	}

	// API Gateway HTTP API lowercases header keys.
	low := map[string]string{
		"x-slack-request-timestamp": ts,
		"x-slack-signature":         sign(secret, ts, body),
	}
	if !Verify(low, body, secret, now) {
		t.Fatal("lowercased headers should pass")
	}
}

func TestParseAppMention(t *testing.T) {
	body := `{"type":"event_callback","event":{"type":"app_mention","channel":"C1","user":"U1","text":"<@B> 서버 확인","ts":"1.2","thread_ts":"1.0"}}`
	ev, ok := ParseAppMention(body)
	if !ok || ev.Channel != "C1" || ev.User != "U1" || ev.ThreadTS != "1.0" {
		t.Fatalf("unexpected parse: %+v ok=%v", ev, ok)
	}
	if _, ok := ParseAppMention(`{"type":"url_verification","challenge":"abc"}`); ok {
		t.Fatal("url_verification should not parse as app_mention")
	}
	if c, ok := Challenge(`{"type":"url_verification","challenge":"abc"}`); !ok || c != "abc" {
		t.Fatalf("challenge parse failed: %q %v", c, ok)
	}
}
