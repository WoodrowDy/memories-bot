package slackclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadSendsTheBotTokenAndReturnsTheBytes(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# 동시성\n\n본문."))
	}))
	defer srv.Close()

	b, err := New("xoxb-test", time.Second).Download(context.Background(), srv.URL, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("Authorization = %q — url_private은 토큰 없이는 안 열린다", gotAuth)
	}
	if string(b) != "# 동시성\n\n본문." {
		t.Errorf("body = %q", b)
	}
}

// 이게 이 파일이 존재하는 이유다. files:read가 없으면 슬랙은 401이 아니라 200에
// 로그인 페이지를 실어 보낸다 — 그대로 믿으면 <!DOCTYPE html>로 시작하는 노트가
// PR로 올라간다.
func TestAnHTMLLoginPageIsNotAFile(t *testing.T) {
	for _, ct := range []string{"text/html; charset=utf-8", "application/octet-stream"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			_, _ = w.Write([]byte("<!DOCTYPE html>\n<html><body>Sign in to Slack</body></html>"))
		}))

		_, err := New("xoxb-test", time.Second).Download(context.Background(), srv.URL, 1<<20)
		srv.Close()

		if !errors.Is(err, ErrNeedsFilesRead) {
			t.Errorf("Content-Type %q: err = %v, want ErrNeedsFilesRead", ct, err)
		}
	}
}

// 넘치면 자르지 않고 거절한다. 잘라 올리면 뒷부분이 조용히 사라진 노트가 PR로 가고,
// 그건 diff를 끝까지 읽어야 보인다.
func TestATooBigFileIsRefusedNotTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 2048)))
	}))
	defer srv.Close()

	b, err := New("xoxb-test", time.Second).Download(context.Background(), srv.URL, 1024)
	if err == nil {
		t.Fatalf("상한 1024에 2048바이트가 통과했다 (%d바이트 반환)", len(b))
	}
	if b != nil {
		t.Errorf("거절하면서 바이트를 돌려줬다: %d", len(b))
	}
}

// 상한이 헤더가 아니라 리더에 걸려 있어야 하는 이유. 슬랙이 청크로 흘려보내면
// Content-Length가 아예 없다 — 헤더를 재는 방식이었다면 상한이 없는 것과 같다.
func TestAStreamedFileStillHitsTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)
		for i := 0; i < 8; i++ {
			_, _ = w.Write([]byte(strings.Repeat("가", 512)))
			if f != nil {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	if _, err := New("xoxb-test", 2*time.Second).Download(context.Background(), srv.URL, 1024); err == nil {
		t.Error("Content-Length가 없는 응답이 상한을 그냥 지나갔다")
	}
}

func TestABinaryFileIsNotANote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0xff, 0xfe})
	}))
	defer srv.Close()

	_, err := New("xoxb-test", time.Second).Download(context.Background(), srv.URL, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("err = %v, want UTF-8 거절", err)
	}
}

func TestASlackErrorStatusIsNotSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := New("xoxb-test", time.Second).Download(context.Background(), srv.URL, 1<<20); err == nil {
		t.Error("404를 성공으로 읽었다")
	}
}

func TestNoTokenMeansNoDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("토큰도 없이 슬랙을 불렀다")
	}))
	defer srv.Close()

	if _, err := New("", time.Second).Download(context.Background(), srv.URL, 1<<20); err == nil {
		t.Error("err = nil, want SLACK_BOT_TOKEN not set")
	}
}
