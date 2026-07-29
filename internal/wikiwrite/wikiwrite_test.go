package wikiwrite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder is a stand-in GitHub that records every request it is asked to make.
// Its job is less to prove the happy path works than to prove what the bot
// never asks for: a write to main, a merge, a path outside the wiki.
type recorder struct {
	t        *testing.T
	calls    []string // "METHOD path"
	bodies   map[string]map[string]any
	existing map[string]bool // paths that already exist on the branch
	refTaken map[string]bool // branch names that already exist
	prNum    int
}

func newRecorder(t *testing.T) *recorder {
	return &recorder{
		t: t, bodies: map[string]map[string]any{},
		existing: map[string]bool{}, refTaken: map[string]bool{}, prNum: 42,
	}
}

func (r *recorder) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := strings.TrimPrefix(req.URL.Path, "/repos/o/wiki/")
		r.calls = append(r.calls, req.Method+" "+p)

		if got := req.Header.Get("Authorization"); got != "Bearer WRITE-TOKEN" {
			r.t.Errorf("missing/incorrect write credential: %q", got)
		}

		var body map[string]any
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			if len(b) > 0 {
				_ = json.Unmarshal(b, &body)
				r.bodies[req.Method+" "+p] = body
			}
		}

		switch {
		case req.Method == http.MethodGet && strings.HasPrefix(p, "git/ref/heads/"):
			_, _ = w.Write([]byte(`{"object":{"sha":"BASESHA"}}`))

		case req.Method == http.MethodPost && p == "git/refs":
			ref, _ := body["ref"].(string)
			if !strings.HasPrefix(ref, "refs/heads/bot/") {
				r.t.Errorf("branch outside the bot/ namespace: %q", ref)
			}
			if r.refTaken[ref] {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"Reference already exists"}`))
				return
			}
			r.refTaken[ref] = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))

		case req.Method == http.MethodGet && strings.HasPrefix(p, "contents/"):
			path := strings.TrimPrefix(p, "contents/")
			if !r.existing[path] {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"sha":"OLDBLOB"}`))

		case req.Method == http.MethodPut && strings.HasPrefix(p, "contents/"):
			br, _ := body["branch"].(string)
			if !strings.HasPrefix(br, "bot/") {
				r.t.Errorf("write aimed at %q — only bot/* may be written", br)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))

		case req.Method == http.MethodPost && p == "pulls":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/o/wiki/pull/42"}`))

		default:
			r.t.Errorf("unexpected call: %s %s", req.Method, p)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
}

func newTestClient(t *testing.T, r *recorder) (*Client, func()) {
	srv := r.server()
	c := New(Config{Owner: "o", Repo: "wiki", Base: "main", Token: "WRITE-TOKEN"})
	c.api = srv.URL
	return c, srv.Close
}

func TestProposeOpensAPRWithoutTouchingMain(t *testing.T) {
	r := newRecorder(t)
	c, done := newTestClient(t, r)
	defer done()

	res, err := c.Propose(context.Background(), Proposal{
		Slug:  "topics/cs/grpc.md",
		Title: "노트: gRPC",
		Body:  "왜 여기인지",
		Files: []File{
			{Path: "topics/cs/grpc.md", Content: "---\ntitle: gRPC\n---\n\n# gRPC\n"},
			{Path: "topics/cs/README.md", Content: "# cs\n- [gRPC](grpc.md)\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.Branch != "bot/note-topics-cs-grpc" {
		t.Errorf("branch = %q", res.Branch)
	}
	if res.PRURL != "https://github.com/o/wiki/pull/42" || res.Number != 42 {
		t.Errorf("pr = %d %q", res.Number, res.PRURL)
	}
	if len(res.Files) != 2 {
		t.Errorf("files = %v", res.Files)
	}

	// The PR must point the new branch at main, not the other way around.
	pr := r.bodies["POST pulls"]
	if pr["head"] != "bot/note-topics-cs-grpc" || pr["base"] != "main" {
		t.Errorf("PR head/base = %v / %v", pr["head"], pr["base"])
	}

	// A brand new file must be written with no sha — sending one would mean
	// "replace the blob I already saw", which is a different operation.
	put := r.bodies["PUT contents/topics/cs/grpc.md"]
	if _, hasSHA := put["sha"]; hasSHA {
		t.Error("new file should be created without a sha")
	}
	decoded, err := base64.StdEncoding.DecodeString(put["content"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "# gRPC") {
		t.Errorf("content did not survive the round trip: %q", decoded)
	}
}

func TestProposeSendsTheCurrentSHAWhenTheFileExists(t *testing.T) {
	r := newRecorder(t)
	r.existing["topics/cs/grpc.md"] = true
	c, done := newTestClient(t, r)
	defer done()

	if _, err := c.Propose(context.Background(), Proposal{
		Slug: "topics/cs/grpc.md", Title: "노트 보강: gRPC", Body: "b",
		Files: []File{{Path: "topics/cs/grpc.md", Content: "새 본문"}},
	}); err != nil {
		t.Fatal(err)
	}

	put := r.bodies["PUT contents/topics/cs/grpc.md"]
	if put["sha"] != "OLDBLOB" {
		t.Errorf("update must carry the current blob sha, got %v", put["sha"])
	}
}

func TestProposeNeverCallsTheMergeAPI(t *testing.T) {
	r := newRecorder(t)
	c, done := newTestClient(t, r)
	defer done()

	if _, err := c.Propose(context.Background(), Proposal{
		Slug: "topics/cs/x.md", Title: "t", Body: "b",
		Files: []File{{Path: "topics/cs/x.md", Content: "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.calls {
		if strings.Contains(call, "merge") {
			t.Fatalf("the bot must never merge: %s", call)
		}
	}
}

func TestProposeWorksAroundATakenBranchName(t *testing.T) {
	r := newRecorder(t)
	r.refTaken["refs/heads/bot/note-topics-cs-grpc"] = true
	c, done := newTestClient(t, r)
	defer done()

	res, err := c.Propose(context.Background(), Proposal{
		Slug: "topics/cs/grpc.md", Title: "t", Body: "b",
		Files: []File{{Path: "topics/cs/grpc.md", Content: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// An open proposal on the same note must not be clobbered.
	if res.Branch != "bot/note-topics-cs-grpc-2" {
		t.Errorf("branch = %q, want the -2 variant", res.Branch)
	}
}

func TestProposeRefusesBadInputBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		p    Proposal
	}{
		{"no files", Proposal{Slug: "s", Title: "t"}},
		{"empty title", Proposal{Slug: "s", Files: []File{{Path: "topics/cs/a.md", Content: "x"}}}},
		{"empty content", Proposal{Slug: "s", Title: "t", Files: []File{{Path: "topics/cs/a.md", Content: "  "}}}},
		{"rule doc", Proposal{Slug: "s", Title: "t", Files: []File{{Path: "CONVENTIONS.md", Content: "x"}}}},
		{"style doc", Proposal{Slug: "s", Title: "t", Files: []File{{Path: "docs/note-style.md", Content: "x"}}}},
		{"workflow", Proposal{Slug: "s", Title: "t", Files: []File{{Path: ".github/workflows/x.md", Content: "x"}}}},
		{"traversal", Proposal{Slug: "s", Title: "t", Files: []File{{Path: "topics/../../etc/x.md", Content: "x"}}}},
		{"duplicate path", Proposal{Slug: "s", Title: "t", Files: []File{
			{Path: "topics/cs/a.md", Content: "x"}, {Path: "topics/cs/a.md", Content: "y"}}}},
		{"too many files", Proposal{Slug: "s", Title: "t", Files: []File{
			{Path: "topics/cs/a.md", Content: "x"}, {Path: "topics/cs/b.md", Content: "x"},
			{Path: "topics/cs/c.md", Content: "x"}, {Path: "topics/cs/d.md", Content: "x"},
			{Path: "topics/cs/e.md", Content: "x"}, {Path: "topics/cs/f.md", Content: "x"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecorder(t)
			c, done := newTestClient(t, r)
			defer done()

			if _, err := c.Propose(context.Background(), tc.p); err == nil {
				t.Fatal("expected a refusal")
			}
			if len(r.calls) != 0 {
				t.Errorf("refused input still hit GitHub: %v", r.calls)
			}
		})
	}
}

func TestProposeRefusesWithoutAToken(t *testing.T) {
	c := New(Config{Owner: "o", Repo: "wiki"})
	if c.Enabled() {
		t.Fatal("a client with no token must report itself disabled")
	}
	if _, err := c.Propose(context.Background(), Proposal{
		Slug: "s", Title: "t", Files: []File{{Path: "topics/cs/a.md", Content: "x"}},
	}); err == nil {
		t.Fatal("expected a refusal with no write token")
	}
}

// putFile re-checks the branch even though Propose only ever passes one it just
// created. This is the last line of code before bytes leave for GitHub.
func TestPutFileRefusesABranchOutsideTheBotNamespace(t *testing.T) {
	r := newRecorder(t)
	c, done := newTestClient(t, r)
	defer done()

	for _, branch := range []string{"main", "master", "notbot/x", ""} {
		err := c.putFile(context.Background(), branch, File{Path: "topics/cs/a.md", Content: "x"})
		if err == nil {
			t.Fatalf("putFile wrote to %q", branch)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("refused branch still hit GitHub: %v", r.calls)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"topics/cs/grpc.md":            "topics-cs-grpc",
		"topics/db/postgresql.md":      "topics-db-postgresql",
		"daily/2026/2026-07-28.md":     "daily-2026-2026-07-28",
		"topics/cs/Some_Mixed CASE.md": "topics-cs-some-mixed-case",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}

	// An all-Korean name leaves nothing behind, so it falls back to a stable
	// hash rather than an empty (invalid) branch name.
	got := slugify("topics/cs/동시성.md")
	if got == "" || strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
		t.Errorf("korean path produced an unusable slug: %q", got)
	}
	if got != slugify("topics/cs/동시성.md") {
		t.Error("slug must be stable for the same path")
	}
	if got == slugify("topics/cs/병렬성.md") {
		t.Error("different paths must not collide")
	}

	if n := len(slugify(strings.Repeat("verylongsegment/", 20) + "x.md")); n > 60 {
		t.Errorf("slug not capped: %d chars", n)
	}
}

func TestEscapePathKeepsSeparatorsAndEncodesSegments(t *testing.T) {
	if got := escapePath("topics/cs/grpc.md"); got != "topics/cs/grpc.md" {
		t.Errorf("plain ascii path was altered: %q", got)
	}
	got := escapePath("topics/cs/동시성.md")
	if strings.Count(got, "/") != 2 {
		t.Errorf("separators were escaped away: %q", got)
	}
	if strings.Contains(got, "동") {
		t.Errorf("korean segment was not percent-encoded: %q", got)
	}
}
