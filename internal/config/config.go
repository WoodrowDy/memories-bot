package config

import "os"

// Config points the bot at one public GitHub markdown wiki.
type Config struct {
	Owner  string
	Repo   string
	Branch string
	Token  string // optional GITHUB_TOKEN (higher API rate limit)

	// WriteToken is GITHUB_WRITE_TOKEN — a *different*, fine-grained PAT with
	// Contents R/W + Pull requests R/W on this one repo. It is deliberately a
	// separate variable from Token: the read path is handed Token only, so no
	// amount of prompt trickery can reach a credential that can write.
	//
	// Empty is a supported state. The bot then answers questions and simply
	// never offers propose_note.
	WriteToken string
}

func FromEnv() Config {
	return Config{
		Owner:      envOr("WIKI_OWNER", "WoodrowDy"),
		Repo:       envOr("WIKI_REPO", "memories"),
		Branch:     envOr("WIKI_BRANCH", "main"),
		Token:      os.Getenv("GITHUB_TOKEN"),
		WriteToken: os.Getenv("GITHUB_WRITE_TOKEN"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
