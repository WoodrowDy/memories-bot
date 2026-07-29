// Package wiki reads a public GitHub markdown wiki (the `memories` repo) and
// answers "does a note on X exist?" plus a status summary. Read-only, no auth
// (public repo). This is the bot's tool layer — the search_wiki / wiki_status
// tools referenced in the proposal.
package wiki

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Owner    string
	Repo     string
	Branch   string
	Token    string        // optional: GitHub token to raise the 60/hour unauth rate limit
	CacheTTL time.Duration // how long one loaded snapshot stays warm (default 60s)
}

type Client struct {
	cfg  Config
	http *http.Client

	// A single LLM answer can fire several tool calls, and Lambda keeps
	// containers warm between invocations. Without this cache every tool call
	// re-walked the whole repo — which is precisely what burns the 60/hour
	// unauthenticated GitHub budget.
	mu       sync.Mutex
	cached   []Note
	cachedAt time.Time
}

func New(cfg Config) *Client {
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 60 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 8 * time.Second}}
}

// Note is one parsed wiki note (frontmatter + body).
type Note struct {
	Path    string
	Title   string
	Status  string
	Created string // kept so an edit can preserve it; the wiki writes YYYY-MM-DD
	Tags    []string
	Aliases []string
	Body    string // frontmatter removed — this is what read_note shows the model

	// Frontmatter is the raw "---\n…\n---" block, verbatim and unparsed.
	//
	// Body deliberately excludes it, which means the model is never shown it. So
	// a model asked to rewrite a whole file — a category README's table of
	// contents — hands back a file with no frontmatter at all, and writing that
	// as-is deletes the block. Keeping the original here lets the write path put
	// it back. Empty when the file had none.
	Frontmatter string
}

// Match is a search hit, structured data only (rendering is the render pkg's job).
type Match struct {
	Path    string
	Title   string
	Status  string
	Score   int
	Snippet string
	URL     string
}

// StatusReport is a snapshot of the wiki (for "위키 현황").
type StatusReport struct {
	TopicsByCat map[string]int
	StatusCount map[string]int
	Total       int
	Daily       int
	Personal    int
}

// Search returns notes matching the query, best first (max 5).
func (c *Client) Search(ctx context.Context, query string) ([]Match, error) {
	notes, err := c.loadNotes(ctx)
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, n := range notes {
		if score, snip := scoreNote(n, query); score > 0 {
			matches = append(matches, Match{
				Path: n.Path, Title: n.Title, Status: n.Status,
				Score: score, Snippet: snip, URL: c.fileURL(n.Path),
			})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if len(matches) > 5 {
		matches = matches[:5]
	}
	return matches, nil
}

// Status summarizes the wiki.
func (c *Client) Status(ctx context.Context) (StatusReport, error) {
	notes, err := c.loadNotes(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	rep := StatusReport{TopicsByCat: map[string]int{}, StatusCount: map[string]int{}}
	for _, n := range notes {
		if strings.HasSuffix(n.Path, "/README.md") || strings.HasSuffix(n.Path, "README.md") {
			continue
		}
		switch {
		case strings.HasPrefix(n.Path, "topics/"):
			parts := strings.Split(n.Path, "/")
			if len(parts) >= 3 {
				rep.TopicsByCat[parts[1]]++
				rep.Total++
				if n.Status != "" {
					rep.StatusCount[n.Status]++
				}
			}
		case strings.HasPrefix(n.Path, "daily/"):
			rep.Daily++
		case strings.HasPrefix(n.Path, "personal/"):
			rep.Personal++
		}
	}
	return rep, nil
}

// List returns a lightweight index of the wiki (no bodies), optionally limited
// to one path prefix. This is what lets the bot answer "무슨 주제들 있어?"
// without shipping every note body to the model.
func (c *Client) List(ctx context.Context, prefix string) ([]Match, error) {
	notes, err := c.loadNotes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Match
	for _, n := range notes {
		if prefix != "" && !strings.HasPrefix(n.Path, prefix) {
			continue
		}
		out = append(out, Match{Path: n.Path, Title: n.Title, Status: n.Status, URL: c.fileURL(n.Path)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadNote fetches one note in full.
//
// The path arrives from an LLM, so it is validated here — in code, immediately
// before the fetch — rather than trusted because the prompt asked nicely.
func (c *Client) ReadNote(ctx context.Context, path string) (Note, error) {
	if !IsNotePath(path) {
		return Note{}, fmt.Errorf("허용되지 않은 경로: %q (위키 노트 경로여야 함)", path)
	}
	raw, err := c.getText(ctx, c.rawURL(path))
	if err != nil {
		return Note{}, err
	}
	return parseNote(path, raw), nil
}

// ruleDocs are the wiki's own writing rules. The bot has to read them before
// it can tidy a draft into a note, but they are not study notes — so they are
// readable without being indexed (see listNotePaths, which uses underContent).
//
// Exact paths, deliberately not a prefix: "docs/" must not become an open door,
// and a root-level allowance by prefix would be no allowance at all.
var ruleDocs = map[string]bool{
	"docs/note-style.md": true,
	"CONVENTIONS.md":     true,
}

// IsNotePath reports whether p is a path this bot is allowed to read: a .md
// file under one of the wiki's content directories (or one of the rule docs
// above), with no traversal or absolute/scheme trickery.
func IsNotePath(p string) bool {
	if p == "" || len(p) > 300 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") ||
		strings.Contains(p, "://") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	return strings.HasSuffix(p, ".md") && (underContent(p) || ruleDocs[p])
}

// IsWritablePath reports whether the bot may propose a change to p.
//
// Deliberately not the same set as IsNotePath. The rule docs (CONVENTIONS.md,
// docs/note-style.md) are readable so the bot can tidy a draft to spec, but not
// writable — it does not get to rewrite the rules it is being held to. README.md
// inside a content directory *is* writable, so the bot can keep the MOCs current
// in the same PR that adds a note.
func IsWritablePath(p string) bool {
	if p == "" || len(p) > 300 {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, "..") ||
		strings.Contains(p, "://") || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	return strings.HasSuffix(p, ".md") && underContent(p)
}

// ---- fetching ----

func (c *Client) loadNotes(ctx context.Context) ([]Note, error) {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.cachedAt) < c.cfg.CacheTTL {
		notes := c.cached
		c.mu.Unlock()
		return notes, nil
	}
	c.mu.Unlock()

	paths, err := c.listNotePaths(ctx)
	if err != nil {
		return nil, err
	}
	notes := c.fetchNotes(ctx, paths)

	c.mu.Lock()
	c.cached, c.cachedAt = notes, time.Now()
	c.mu.Unlock()
	return notes, nil
}

type treeResp struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

func (c *Client) listNotePaths(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1",
		c.cfg.Owner, c.cfg.Repo, c.cfg.Branch)
	var tr treeResp
	if err := c.getJSON(ctx, url, &tr); err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range tr.Tree {
		if e.Type == "blob" && strings.HasSuffix(e.Path, ".md") && underContent(e.Path) {
			paths = append(paths, e.Path)
		}
	}
	return paths, nil
}

func underContent(p string) bool {
	for _, pre := range []string{"topics/", "daily/", "personal/", "projects/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

func (c *Client) fetchNotes(ctx context.Context, paths []string) []Note {
	const workers = 6
	jobs := make(chan string)
	out := make(chan Note)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if raw, err := c.getText(ctx, c.rawURL(p)); err == nil {
					out <- parseNote(p, raw)
				}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(out) }()

	var notes []Note
	for n := range out {
		notes = append(notes, n)
	}
	return notes
}

func (c *Client) rawURL(path string) string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
		c.cfg.Owner, c.cfg.Repo, c.cfg.Branch, path)
}

func (c *Client) fileURL(path string) string {
	return fmt.Sprintf("https://github.com/%s/%s/blob/%s/%s",
		c.cfg.Owner, c.cfg.Repo, c.cfg.Branch, path)
}

func (c *Client) getJSON(ctx context.Context, url string, out any) error {
	body, err := c.getText(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), out)
}

func (c *Client) getText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "memories-wiki-bot")
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s -> %d", url, res.StatusCode)
	}
	return string(b), nil
}

// ---- parsing ----

var (
	fmRe    = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---`)
	titleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

// ParseFrontmatter reads a markdown file's own frontmatter — nothing else.
//
// parseNote와 갈라 둔 건 되메우기 때문이다. 저쪽은 title이 비면 H1에서, 그것도 없으면
// 경로에서 끌어와 채운다 — 위키에 있는 노트를 *보여줄* 때는 맞는 짓이다. 하지만
// 첨부 파일에서 "우드로가 직접 적은 값"을 골라낼 때는 그 되메우기가 거짓말이 된다:
// 파일 이름이 제목으로 둔갑해 프론트매터에 박히고, 봇은 그걸 그가 쓴 값으로 착각한다.
//
// 그래서 여기서는 없으면 없는 채로 둔다. 빈 칸이어야 채울 칸인 줄 안다.
func ParseFrontmatter(raw string) Note {
	n := Note{Body: raw}
	if m := fmRe.FindStringSubmatch(raw); m != nil {
		block := m[1]
		n.Title = fmScalar(block, "title")
		n.Status = fmScalar(block, "status")
		n.Created = fmScalar(block, "created")
		n.Tags = fmList(block, "tags")
		n.Aliases = fmList(block, "aliases")
		n.Frontmatter = raw[:len(m[0])] // "---\n…\n---", no trailing newline
		n.Body = raw[len(m[0]):]
	}
	return n
}

func parseNote(path, raw string) Note {
	n := ParseFrontmatter(raw)
	n.Path = path
	if n.Title == "" {
		if h := titleRe.FindStringSubmatch(raw); h != nil {
			n.Title = strings.TrimSpace(h[1])
		}
	}
	if n.Title == "" {
		n.Title = path
	}
	return n
}

func fmScalar(block, key string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*(.*)$`)
	if m := re.FindStringSubmatch(block); m != nil {
		return strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), `"'`))
	}
	return ""
}

func fmList(block, key string) []string {
	v := fmScalar(block, key)
	v = strings.TrimSpace(strings.Trim(v, "[]"))
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"'`))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- scoring (Korean-aware, heuristic) ----

var stopWords = map[string]bool{
	"정리": true, "정리한": true, "거": true, "있어": true, "있나": true, "있는지": true,
	"뭐": true, "알려줘": true, "관련": true, "대해": true, "내": true, "나": true,
	"좀": true, "해줘": true, "봐": true, "위키": true,
	"the": true, "a": true, "is": true, "about": true, "of": true, "on": true, "me": true,
}

var particles = []string{"이랑", "랑", "으로", "에서", "에게", "한테", "까지", "부터", "이나", "와", "과", "이", "가", "은", "는", "을", "를", "에", "의", "도", "로", "만", "라고"}

func tokenize(q string) []string {
	q = strings.ToLower(q)
	fields := strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '?', '!', ',', '.', '"', '\'', '(', ')', '/':
			return true
		}
		return false
	})
	var out []string
	for _, f := range fields {
		if !stopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

func candidates(term string) []string {
	cands := []string{term}
	for _, p := range particles {
		if strings.HasSuffix(term, p) {
			base := term[:len(term)-len(p)]
			if len([]rune(base)) >= 2 {
				cands = append(cands, base)
			}
		}
	}
	return cands
}

func scoreNote(n Note, query string) (int, string) {
	ql := strings.ToLower(query)
	searchText := strings.ToLower(strings.Join([]string{
		n.Title, strings.Join(n.Aliases, " "), strings.Join(n.Tags, " "), n.Path, n.Body,
	}, "\n"))

	score := 0
	// 1) a note's alias/title appearing inside the query is a strong signal.
	for _, a := range n.Aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if len([]rune(a)) >= 2 && strings.Contains(ql, a) {
			score += 6
		}
	}
	if t := strings.ToLower(n.Title); len([]rune(t)) >= 2 && strings.Contains(ql, t) {
		score += 6
	}

	// 2) query terms (particle-stripped) found in the note.
	matched := ""
	for _, term := range tokenize(query) {
		for _, cand := range candidates(term) {
			if len([]rune(cand)) < 2 {
				continue
			}
			if strings.Contains(searchText, cand) {
				if strings.Contains(strings.ToLower(n.Title), cand) ||
					containsAny(n.Aliases, cand) || containsAny(n.Tags, cand) {
					score += 4
				} else {
					score += 2
				}
				if matched == "" {
					matched = cand
				}
				break
			}
		}
	}

	if score == 0 {
		return 0, ""
	}
	return score, snippet(n, matched)
}

func containsAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(strings.ToLower(s), sub) {
			return true
		}
	}
	return false
}

func snippet(n Note, term string) string {
	lines := strings.Split(n.Body, "\n")
	skip := func(l string) bool {
		return l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "---") ||
			strings.HasPrefix(l, "```") || strings.HasPrefix(l, "![") || strings.HasPrefix(l, ">")
	}
	if term != "" {
		for _, ln := range lines {
			l := strings.TrimSpace(ln)
			if skip(l) {
				continue
			}
			if strings.Contains(strings.ToLower(l), term) {
				return trimRunes(l, 140)
			}
		}
	}
	for _, ln := range lines {
		l := strings.TrimSpace(ln)
		if skip(l) {
			continue
		}
		return trimRunes(l, 140)
	}
	return ""
}

func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
