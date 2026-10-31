package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	mdLink    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	htmlLink  = regexp.MustCompile(`(?:src|href)="([^"]+)"`)
	mdHeading = regexp.MustCompile(`(?m)^#{1,6} (.+)$`)
	slugDrop  = regexp.MustCompile(`[^a-z0-9 _-]`)
)

// slug is GitHub's anchor for a heading.
func slug(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.NewReplacer("`", "", "*", "").Replace(h)
	return strings.ReplaceAll(slugDrop.ReplaceAllString(h, ""), " ", "-")
}

func anchors(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, m := range mdHeading.FindAllStringSubmatch(string(b), -1) {
		out[slug(m[1])] = true
	}
	return out
}

// TestDocLinks: every relative link in the README, docs/ and the skill, in
// Markdown or an HTML src or href, reaches a file, and every #anchor a heading
// in it.
func TestDocLinks(t *testing.T) {
	files := []string{"../../README.md", "../../CHANGELOG.md"}
	for _, g := range []string{"../../docs/*.md", "../../plugin/skills/lcd349/*.md", "../../plugin/skills/lcd349/references/*.md"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// Links inside code blocks aren't links.
		text := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(string(b), "")
		links := append(mdLink.FindAllStringSubmatch(text, -1), htmlLink.FindAllStringSubmatch(text, -1)...)
		for _, m := range links {
			link := m[1]
			if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
				continue
			}
			target, anchor, _ := strings.Cut(link, "#")
			path := f
			if target != "" {
				path = filepath.Join(filepath.Dir(f), target)
				if _, err := os.Stat(path); err != nil {
					t.Errorf("%s links to %s, which doesn't exist", f, link)
					continue
				}
			}
			if anchor != "" && strings.HasSuffix(path, ".md") && !anchors(path)[anchor] {
				t.Errorf("%s links to %s: no heading for #%s", f, link, anchor)
			}
		}
	}
}
