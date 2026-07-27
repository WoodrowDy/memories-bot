// Package render turns wiki tool data into Slack messages (Slack mrkdwn:
// *bold*, `code`). Data → words seam; the bot posts these as itself.
package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// SearchResults formats a search answer for Slack.
func SearchResults(query string, matches []wiki.Match) string {
	if len(matches) == 0 {
		return fmt.Sprintf("❌ *'%s'* 관련 노트는 아직 없어요.\n필요하면 새로 정리해 볼까요?", query)
	}
	top := matches[0]
	var b strings.Builder
	fmt.Fprintf(&b, "✅ *있어요* — %s\n", top.Title)
	fmt.Fprintf(&b, "📄 `%s` · status: %s\n", top.Path, orQ(top.Status))
	if top.Snippet != "" {
		fmt.Fprintf(&b, "%s\n", top.Snippet)
	}
	fmt.Fprintf(&b, "🔗 %s", top.URL)
	if len(matches) > 1 {
		b.WriteString("\n\n*관련 노트:*")
		for _, m := range matches[1:] {
			fmt.Fprintf(&b, "\n• %s — `%s`", m.Title, m.Path)
		}
	}
	return b.String()
}

// Status formats the wiki snapshot for Slack.
func Status(r wiki.StatusReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📚 *위키 현황* — topics %d편\n", r.Total)
	if len(r.TopicsByCat) > 0 {
		var parts []string
		for _, c := range sortedKeys(r.TopicsByCat) {
			parts = append(parts, fmt.Sprintf("%s %d", c, r.TopicsByCat[c]))
		}
		fmt.Fprintf(&b, "• 카테고리: %s\n", strings.Join(parts, " · "))
	}
	if len(r.StatusCount) > 0 {
		var parts []string
		for _, s := range sortedKeys(r.StatusCount) {
			parts = append(parts, fmt.Sprintf("%s %d", s, r.StatusCount[s]))
		}
		fmt.Fprintf(&b, "• 성숙도: %s\n", strings.Join(parts, " · "))
	}
	fmt.Fprintf(&b, "• daily %d편 · personal %d편", r.Daily, r.Personal)
	return b.String()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
