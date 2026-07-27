package config

import "os"

// Config points the bot at one public GitHub markdown wiki.
type Config struct {
	Owner  string
	Repo   string
	Branch string
	Token  string // optional GITHUB_TOKEN (higher API rate limit)
}

func FromEnv() Config {
	return Config{
		Owner:  envOr("WIKI_OWNER", "WoodrowDy"),
		Repo:   envOr("WIKI_REPO", "memories"),
		Branch: envOr("WIKI_BRANCH", "main"),
		Token:  os.Getenv("GITHUB_TOKEN"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
