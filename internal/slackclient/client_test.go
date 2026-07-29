package slackclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 필드 이름이 하나만 틀려도 슬랙은 HTTP 200에 ok:false를 실어 보내고, 우리 쪽엔
// 로그 한 줄만 남는다. 표시가 안 뜨는 걸 눈으로 알아채기 전까지는 아무도 모른다.
//
// 특히 chat.postMessage는 channel인데 setStatus는 channel_id다. 이 비대칭은 슬랙
// API에 실제로 있는 거라, 헷갈려서 맞춰 놓고 싶어지는 자리이기도 하다.
func TestSetStatusSendsWhatSlackAsksFor(t *testing.T) {
	var (
		path, auth string
		body       map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL)
	if err := c.SetStatus("C123", "1700000000.000100", "답을 쓰는 중…"); err != nil {
		t.Fatal(err)
	}

	if want := "/assistant.threads.setStatus"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if want := "Bearer xoxb-test"; auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
	for k, want := range map[string]string{
		"channel_id": "C123",
		"thread_ts":  "1700000000.000100",
		"status":     "답을 쓰는 중…",
	} {
		if got, _ := body[k].(string); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := body["channel"]; ok {
		t.Error("channel을 보냈다 — setStatus가 받는 건 channel_id다")
	}
}

// 표시 하나 붙이자고 call을 하나로 합치면서 답 올리는 길도 같이 바뀌었다. 표시가
// 안 뜨는 건 불편이지만 답이 안 올라가면 봇이 죽은 거다. 여기가 그 길을 지킨다.
func TestPostThreadStillSendsWhatSlackAsksFor(t *testing.T) {
	var (
		path string
		body map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL)
	if err := c.PostThread("C123", "1700000000.000100", "답이다"); err != nil {
		t.Fatal(err)
	}

	if want := "/chat.postMessage"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	for k, want := range map[string]string{
		"channel":   "C123",
		"thread_ts": "1700000000.000100",
		"text":      "답이다",
	} {
		if got, _ := body[k].(string); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// 스레드 밖으로 나가는 답은 채널 아무 데나 떨어진다. thread_ts가 비면 아예 안 싣는다.
func TestAnEmptyThreadIsOmittedNotSentBlank(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL)
	if err := c.PostThread("C123", "", "답이다"); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["thread_ts"]; ok {
		t.Errorf("빈 thread_ts를 실어 보냈다: %v", body["thread_ts"])
	}
}

// 슬랙은 실패도 HTTP 200에 실어 보낸다. 상태 코드만 보면 전부 성공이다.
//
// 그리고 이 에러 낱말이 로그까지 살아 나가야 한다. 표시가 안 뜰 때 스코프가 모자란
// 건지 채널이 틀린 건지는 CloudWatch에 남은 이 한 낱말로 갈린다.
func TestASlackRefusalIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
	}))
	defer srv.Close()

	c := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL)

	err := c.SetStatus("C1", "1.1", "답을 쓰는 중…")
	if err == nil {
		t.Fatal("ok:false인데 성공이라고 한다")
	}
	if !strings.Contains(err.Error(), "missing_scope") {
		t.Errorf("슬랙이 준 이유가 에러에 없다: %v", err)
	}
	if err := c.PostThread("C1", "1.1", "답이다"); err == nil {
		t.Fatal("답 올리는 쪽도 ok:false를 성공으로 본다")
	}
}

// 이 표시는 답을 기다리는 동안 보라고 붙인 거다. 표시를 기다리느라 답이 늦어지면
// 붙인 이유가 통째로 뒤집힌다. 슬랙이 안 받아주면 포기하고 넘어가야 한다.
func TestASlowStatusGivesUpInsteadOfHoldingTheAnswer(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked) // 핸들러를 먼저 풀어주고 서버를 닫는다

	old := statusTimeout
	statusTimeout = 50 * time.Millisecond
	defer func() { statusTimeout = old }()

	// http.Client 쪽 상한은 일부러 넉넉히 둔다. 여기서 걸려야 하는 건 statusTimeout이다.
	c := New("xoxb-test", 30*time.Second).WithBaseURL(srv.URL)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.SetStatus("C1", "1.1", "답을 쓰는 중…") }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("서버가 답을 안 줬는데 성공이라고 한다")
		}
		if el := time.Since(start); el > time.Second {
			t.Errorf("포기하는 데 %v 걸렸다 — 그동안 답이 묶여 있다", el)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("표시가 답을 붙잡고 안 놓는다")
	}
}

// 토큰 없이 배포되면 답마다 슬랙으로 헛 호출이 두 번씩 나간다. 코드에서 먼저 막는다.
func TestNoTokenMeansNoCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New("", 5*time.Second).WithBaseURL(srv.URL)
	if err := c.SetStatus("C1", "1.1", "답을 쓰는 중…"); err == nil {
		t.Error("토큰이 없는데 표시가 됐다고 한다")
	}
	if err := c.PostThread("C1", "1.1", "답이다"); err == nil {
		t.Error("토큰이 없는데 답을 올렸다고 한다")
	}
	if calls != 0 {
		t.Errorf("슬랙을 %d번 불렀다", calls)
	}
}
