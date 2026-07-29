package slackclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ErrNeedsFilesRead is what a missing files:read scope actually looks like.
//
// 슬랙은 여기서 401을 주지 않는다. 200에 로그인 HTML 페이지를 실어 보낸다. 그대로
// 믿으면 `<!DOCTYPE html>`로 시작하는 노트가 PR로 올라간다 — 실패가 성공처럼 지나가는
// 자리라서, 이 패키지에서 유일하게 본문을 들여다보고 거르는 곳이다.
var ErrNeedsFilesRead = errors.New("slack: 파일을 못 읽었어요 — 봇 토큰에 files:read 스코프가 없어 보여요")

// Download fetches one Slack-hosted file with the bot token.
//
// call()을 못 쓴다. 저쪽은 JSON을 POST하고 JSON을 받는 창구고, 이건 GET에 원시
// 바이트다. 같은 헬퍼에 억지로 태우면 둘 다 애매해진다.
//
// limit은 리더에 건다. Content-Length를 믿고 재면 그 값이 거짓일 때 상한이 없는
// 것과 같아진다.
func (c *Client) Download(ctx context.Context, url string, limit int64) ([]byte, error) {
	if c.token == "" {
		return nil, errors.New("SLACK_BOT_TOKEN not set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack: 파일 응답이 %s예요", res.Status)
	}
	if isLoginPage(res.Header.Get("Content-Type")) {
		return nil, ErrNeedsFilesRead
	}

	// limit+1을 읽어서 한 바이트라도 넘치면 자르지 않고 거절한다. 잘라서 올리면
	// 뒷부분이 조용히 사라진 노트가 PR로 가고, 그건 diff를 끝까지 봐야 보인다.
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("slack: 파일이 상한(%dKB)보다 커요", limit>>10)
	}
	if looksLikeHTML(b) {
		return nil, ErrNeedsFilesRead
	}
	if !utf8.Valid(b) {
		return nil, errors.New("slack: 이 파일은 UTF-8 텍스트가 아니에요")
	}
	return b, nil
}

func isLoginPage(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// looksLikeHTML is the backstop for when Slack serves the login page without
// saying text/html. 헤더는 틀릴 수 있어도 첫 글자는 안 틀린다.
func looksLikeHTML(b []byte) bool {
	head := strings.ToLower(strings.TrimLeft(string(b[:min(len(b), 64)]), " \t\r\n\uFEFF"))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}
