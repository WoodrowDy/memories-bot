package slackclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// conversations.replies는 GET에 쿼리스트링으로 부른다. POST 몸통에 실어 보내면 슬랙은
// 200에 ok:false를 돌려주고, 우리 쪽엔 "초안을 못 찾았어요" 한 줄만 남는다 — 스코프
// 문제로 착각하기 딱 좋은 자리라서 부르는 꼴을 여기서 못 박아 둔다.
func TestRepliesAsksSlackForTheThread(t *testing.T) {
	var (
		method, path, auth string
		q                  url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`))
	}))
	defer srv.Close()

	c := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL)
	if _, err := c.Replies(context.Background(), "C123", "1700000000.000100"); err != nil {
		t.Fatal(err)
	}

	if method != http.MethodGet {
		t.Errorf("method = %q, want GET", method)
	}
	if want := "/conversations.replies"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if want := "Bearer xoxb-test"; auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
	for k, want := range map[string]string{
		"channel": "C123",
		"ts":      "1700000000.000100",
		"limit":   "200",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// 슬랙이 준 순서를 그대로 넘긴다. 고르는 쪽(pickThreadDraft)이 "뒤에서부터"로 최신
// 초안을 집기 때문에, 여기서 한 번 뒤집으면 그가 고쳐 올린 초안 대신 처음 것이 올라간다.
//
// url_private이 빈 항목을 세지 않는 것도 같이 지킨다. files:read가 없을 때 슬랙이
// 파일의 속을 비워 보내는데, 그걸 첨부로 세면 받아올 수 없는 URL로 다운로드를 시도하고
// 그는 "초안을 못 찾았어요" 대신 알 수 없는 실패를 받는다.
func TestRepliesKeepsSlacksOrderAndDropsUnfetchableFiles(t *testing.T) {
	body := `{"ok":true,"messages":[
		{"ts":"1.0","user":"U1","text":"이거 확인","files":[
			{"name":"first.md","url_private":"https://files.slack.com/first.md","size":10}]},
		{"ts":"2.0","user":"B1","text":"자리는 topics/cs/..."},
		{"ts":"3.0","user":"U1","text":"고쳤어","files":[
			{"name":"second.md","url_private":"https://files.slack.com/second.md","size":20},
			{"name":"hidden.md","url_private":"","size":30}]}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	msgs, err := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL).
		Replies(context.Background(), "C123", "1.0")
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 3 {
		t.Fatalf("메시지 %d개, 3개여야 한다", len(msgs))
	}
	if msgs[0].TS != "1.0" || msgs[2].TS != "3.0" {
		t.Errorf("순서가 바뀌었다: %q … %q", msgs[0].TS, msgs[2].TS)
	}
	if len(msgs[1].Files) != 0 {
		t.Errorf("첨부 없는 메시지에 파일이 생겼다: %v", msgs[1].Files)
	}
	if len(msgs[2].Files) != 1 {
		t.Fatalf("마지막 메시지의 첨부 %d개, 1개여야 한다 (url_private 빈 건 안 센다)", len(msgs[2].Files))
	}
	got := msgs[2].Files[0]
	if got.Name != "second.md" || got.URL != "https://files.slack.com/second.md" || got.Size != 20 {
		t.Errorf("첨부 = %+v", got)
	}
	if msgs[2].User != "U1" {
		t.Errorf("user = %q, want U1", msgs[2].User)
	}
}

// 슬랙은 스코프가 없다는 걸 missing_scope 한 낱말로만 알려준다. 그대로 스레드에 올리면
// 무엇을 어디에 추가해야 하는지는 아무도 말해주지 않는다.
func TestRepliesNamesTheMissingScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`))
	}))
	defer srv.Close()

	_, err := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL).
		Replies(context.Background(), "C123", "1.0")
	if !errors.Is(err, ErrNeedsHistory) {
		t.Fatalf("err = %v, want ErrNeedsHistory", err)
	}
}

// 나머지 에러는 낱말을 그대로 싣는다. not_in_channel(봇을 채널에 초대해야 한다)과
// thread_not_found(그 스레드가 없다)는 고치는 방법이 전혀 다른데, 뭉뚱그리면
// CloudWatch 로그만 보고는 갈리지 않는다.
func TestRepliesCarriesSlacksOwnErrorWord(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
	}))
	defer srv.Close()

	_, err := New("xoxb-test", 5*time.Second).WithBaseURL(srv.URL).
		Replies(context.Background(), "C123", "1.0")
	if err == nil {
		t.Fatal("ok:false인데 에러가 없다 — HTTP 200이라 상태 코드만 보면 성공처럼 보인다")
	}
	if want := "not_in_channel"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, %q가 들어 있어야 한다", err, want)
	}
}
