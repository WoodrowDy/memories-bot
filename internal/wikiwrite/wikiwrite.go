// Package wikiwrite opens pull requests against the memories wiki.
//
// It is a separate package from `wiki` on purpose. The read client and the
// write client hold different credentials on different types, so no code path
// that only has a *wiki.Client can reach the write token — the separation is
// enforced by the type system rather than by care.
//
// Three rules are enforced here in code, not in the prompt:
//
//   - every commit lands on a branch whose name starts with "bot/"; the base
//     branch is never written to
//   - every path is checked against wiki.IsWritablePath before it is sent
//   - the merge API is not implemented. The bot opens a PR and stops. Merging
//     is a human's, and closing the PR is a complete undo.
package wikiwrite

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// BranchPrefix is the only namespace this bot may create or commit to.
const BranchPrefix = "bot/"

const maxFilesPerProposal = 5

type Config struct {
	Owner string
	Repo  string
	Base  string // base branch, default "main"

	// Token is GITHUB_WRITE_TOKEN — a fine-grained PAT scoped to this one repo
	// with Contents R/W + Pull requests R/W and nothing else. Kept on this type
	// so it is unreachable from the read client.
	Token string
}

type Client struct {
	cfg  Config
	api  string // GitHub API root; swapped for a test server in package tests
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.Base == "" {
		cfg.Base = "main"
	}
	return &Client{
		cfg:  cfg,
		api:  "https://api.github.com",
		http: &http.Client{Timeout: 12 * time.Second},
	}
}

// Enabled reports whether a write token was configured. When false the bot
// keeps answering questions and simply cannot propose notes.
func (c *Client) Enabled() bool { return c != nil && c.cfg.Token != "" }

// File is one file to write in a proposal.
type File struct {
	Path    string
	Content string
}

// Proposal is one pull request.
type Proposal struct {
	Slug  string // branch becomes "bot/note-<slug>"
	Title string
	Body  string
	Files []File
}

// Result is what the caller reports back to Slack.
type Result struct {
	PRURL  string   `json:"pr_url"`
	Number int      `json:"pr_number"`
	Branch string   `json:"branch"`
	Files  []string `json:"files"`
}

// Propose creates a branch, writes every file to it, and opens a pull request.
//
// Nothing is retried. A retried POST could open a second branch or a second PR,
// and a duplicate PR is worse than a failure the model can report and redo.
func (c *Client) Propose(ctx context.Context, p Proposal) (Result, error) {
	var res Result

	if !c.Enabled() {
		return res, fmt.Errorf("쓰기 토큰(GITHUB_WRITE_TOKEN)이 없어서 PR을 열 수 없어요")
	}
	if len(p.Files) == 0 {
		return res, fmt.Errorf("PR에 담을 파일이 없어요")
	}
	if len(p.Files) > maxFilesPerProposal {
		return res, fmt.Errorf("한 PR에 파일 %d개는 너무 많아요 (최대 %d개)", len(p.Files), maxFilesPerProposal)
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if !wiki.IsWritablePath(f.Path) {
			return res, fmt.Errorf("쓸 수 없는 경로: %q (topics/ daily/ personal/ projects/ 아래 .md만 가능)", f.Path)
		}
		if seen[f.Path] {
			return res, fmt.Errorf("같은 경로가 두 번 들어왔어요: %q", f.Path)
		}
		seen[f.Path] = true
		if strings.TrimSpace(f.Content) == "" {
			return res, fmt.Errorf("%s의 내용이 비었어요", f.Path)
		}
	}
	if strings.TrimSpace(p.Title) == "" {
		return res, fmt.Errorf("PR 제목이 비었어요")
	}

	baseSHA, err := c.refSHA(ctx, c.cfg.Base)
	if err != nil {
		return res, err
	}

	branch, err := c.createBranch(ctx, "note-"+slugify(p.Slug), baseSHA)
	if err != nil {
		return res, err
	}
	res.Branch = branch

	for _, f := range p.Files {
		if err := c.putFile(ctx, branch, f); err != nil {
			return res, err
		}
		res.Files = append(res.Files, f.Path)
	}

	num, prURL, err := c.openPR(ctx, branch, p.Title, p.Body)
	if err != nil {
		return res, err
	}
	res.Number, res.PRURL = num, prURL
	return res, nil
}

// ---- GitHub calls ----

func (c *Client) refSHA(ctx context.Context, branch string) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("git/ref/heads/%s", url.PathEscape(branch)), nil, &out)
	if err != nil {
		return "", fmt.Errorf("기준 브랜치 %s를 찾지 못했어요: %w", branch, err)
	}
	if out.Object.SHA == "" {
		return "", fmt.Errorf("기준 브랜치 %s의 커밋을 못 읽었어요", branch)
	}
	return out.Object.SHA, nil
}

// createBranch makes bot/<name>, adding -2, -3 … if that name is taken.
// A name collision means an earlier proposal is still open; both should exist.
func (c *Client) createBranch(ctx context.Context, name, baseSHA string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		branch := BranchPrefix + name
		if attempt > 1 {
			branch = fmt.Sprintf("%s%s-%d", BranchPrefix, name, attempt)
		}
		body := map[string]string{"ref": "refs/heads/" + branch, "sha": baseSHA}
		err := c.do(ctx, http.MethodPost, "git/refs", body, nil)
		if err == nil {
			return branch, nil
		}
		if !isAlreadyExists(err) {
			return "", fmt.Errorf("브랜치를 만들지 못했어요: %w", err)
		}
		lastErr = err
	}
	return "", fmt.Errorf("브랜치 이름이 계속 겹쳐요 (%s…): %w", BranchPrefix+name, lastErr)
}

func (c *Client) putFile(ctx context.Context, branch string, f File) error {
	// Belt and braces: the branch is validated again here, at the last point
	// before bytes leave for GitHub, because this is the call that can write.
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("봇은 %s* 브랜치에만 쓸 수 있어요 (받은 값: %q)", BranchPrefix, branch)
	}
	if !wiki.IsWritablePath(f.Path) {
		return fmt.Errorf("쓸 수 없는 경로: %q", f.Path)
	}

	body := map[string]string{
		"message": commitMessage(f.Path),
		"content": base64.StdEncoding.EncodeToString([]byte(f.Content)),
		"branch":  branch,
	}
	// An existing file needs its current blob sha; omitting it means "create".
	if sha, err := c.fileSHA(ctx, branch, f.Path); err != nil {
		return err
	} else if sha != "" {
		body["sha"] = sha
	}

	if err := c.do(ctx, http.MethodPut, "contents/"+escapePath(f.Path), body, nil); err != nil {
		return fmt.Errorf("%s를 쓰지 못했어요: %w", f.Path, err)
	}
	return nil
}

// fileSHA returns the blob sha of path on branch, or "" if the file is new.
func (c *Client) fileSHA(ctx context.Context, branch, path string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("contents/%s?ref=%s", escapePath(path), url.QueryEscape(branch)), nil, &out)
	if err != nil {
		if isNotFound(err) {
			return "", nil // new file
		}
		return "", fmt.Errorf("%s의 현재 상태를 못 읽었어요: %w", path, err)
	}
	return out.SHA, nil
}

func (c *Client) openPR(ctx context.Context, branch, title, body string) (int, string, error) {
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	req := map[string]any{
		"title": title,
		"body":  body,
		"head":  branch,
		"base":  c.cfg.Base,
	}
	if err := c.do(ctx, http.MethodPost, "pulls", req, &out); err != nil {
		return 0, "", fmt.Errorf("PR을 열지 못했어요 (브랜치 %s는 만들어졌어요): %w", branch, err)
	}
	return out.Number, out.HTMLURL, nil
}

// ---- transport ----

// apiError carries the status code so callers can tell 404 and 422 apart from
// real failures without string matching.
type apiError struct {
	Status int
	Body   string
	Path   string
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return fmt.Sprintf("GitHub %s -> %d: %s", e.Path, e.Status, msg)
}

func isNotFound(err error) bool {
	var e *apiError
	return asAPIError(err, &e) && e.Status == http.StatusNotFound
}

func isAlreadyExists(err error) bool {
	var e *apiError
	return asAPIError(err, &e) && e.Status == http.StatusUnprocessableEntity &&
		strings.Contains(e.Body, "already exists")
}

func asAPIError(err error, out **apiError) bool {
	for err != nil {
		if e, ok := err.(*apiError); ok {
			*out = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var rdr io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/%s", c.api, c.cfg.Owner, c.cfg.Repo, path)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "memories-wiki-bot")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &apiError{Status: res.StatusCode, Body: string(body), Path: path}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

// ---- naming ----

func commitMessage(path string) string {
	return fmt.Sprintf("bot: %s 정리", path)
}

// escapePath percent-escapes each path segment. Note names can be Korean, and
// an unescaped segment would break the contents API.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// slugify turns a note path into a branch-safe name.
//
// A Korean note name leaves nothing behind after ASCII filtering, which would
// collapse topics/cs/동시성.md and topics/cs/병렬성.md onto the same "topics-cs"
// branch. Whenever the note's own filename contributes nothing, a short stable
// hash of the full path is appended: different notes get different branches,
// and the same note retried gets the same branch.
func slugify(s string) string {
	s = strings.TrimSuffix(s, ".md")

	out := asciiSlug(s)
	if len(out) > 60 {
		out = strings.Trim(out[:60], "-")
	}

	name := s
	if i := strings.LastIndex(s, "/"); i >= 0 {
		name = s[i+1:]
	}
	if asciiSlug(name) == "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(s))
		suffix := fmt.Sprintf("%08x", h.Sum32())
		if out == "" {
			return suffix
		}
		return out + "-" + suffix
	}
	return out
}

// asciiSlug lowercases and keeps [a-z0-9], collapsing everything else to a
// single dash.
func asciiSlug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
