package wiki

import "testing"

// read_note's path comes from a language model, so the allowlist is the only
// thing standing between a confused (or steered) model and an arbitrary fetch.
func TestIsNotePath(t *testing.T) {
	allowed := []string{
		"topics/cs/concurrency.md",
		"daily/2026-07-22.md",
		"personal/reading.md",
		"projects/wiki-assistant.md",
	}
	for _, p := range allowed {
		if !IsNotePath(p) {
			t.Errorf("IsNotePath(%q) = false, want true", p)
		}
	}

	rejected := []string{
		"",
		"README.md",                      // outside the content dirs
		".env",                           // not a note at all
		"topics/../../etc/passwd",        // traversal
		"topics/cs/../../../secrets.md",  // traversal that ends in .md
		"/etc/passwd.md",                 // absolute
		"https://evil.example/x.md",      // scheme
		"topics/cs/note.md\x00.png",      // NUL splice
		"topics\\cs\\note.md",            // backslash
		"topics/cs/concurrency.markdown", // wrong suffix
		".github/workflows/deploy.md",    // outside the content dirs
	}
	for _, p := range rejected {
		if IsNotePath(p) {
			t.Errorf("IsNotePath(%q) = true, want false", p)
		}
	}
}

func TestReadNoteRefusesBadPathBeforeFetching(t *testing.T) {
	c := New(Config{Owner: "o", Repo: "r"})
	if _, err := c.ReadNote(nil, "../../etc/passwd"); err == nil {
		t.Fatal("expected ReadNote to refuse a traversal path")
	}
	// Nil context proves no request was attempted: reaching http would panic.
}
