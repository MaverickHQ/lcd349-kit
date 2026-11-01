// Package scaffold creates a product from the embedded templates/app
// (requirement 11.1): an ESP-IDF project that pins the kit, a main with one
// screen, one scenario with its golden, and a CLAUDE.md for agents.
package scaffold

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// Options for a new project.
type Options struct {
	Dir  string // created; must not exist, or be empty
	Name string // the ESP-IDF project name; default from Dir
	// KitPath is a local kit checkout's components/lcd349, written as
	// override_path; empty pins the kit release from KitRepo, or RepoURL.
	KitPath string
	// KitRepo, when KitPath is empty, is the git URL to pin the release's tag
	// from instead of RepoURL: a release candidate, before it is published
	// (phase 5 requirement 2.1). A development build may pin one.
	KitRepo string
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// NameFrom makes a project name from a directory name.
func NameFrom(dir string) string {
	n := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r - 'A' + 'a'
		}
		return '_'
	}, filepath.Base(dir))
	n = strings.Trim(n, "_")
	if n == "" || n[0] < 'a' {
		n = "app_" + n
	}
	if len(n) > 32 {
		n = n[:32]
	}
	return n
}

// Released reports whether this binary can pin a published kit release: a
// development or pre-release build can't.
func Released() bool {
	return !strings.Contains(lcd349kit.Version, "-")
}

// New creates the project and returns the files written, relative to Dir.
func New(o Options) ([]string, error) {
	if o.Name == "" {
		o.Name = NameFrom(o.Dir)
	}
	if !nameRe.MatchString(o.Name) {
		return nil, fmt.Errorf("name %q: use 1-32 lower-case letters, digits and underscores, starting with a letter", o.Name)
	}
	kitPath := ""
	if o.KitPath != "" {
		abs, err := filepath.Abs(o.KitPath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(abs, "include", "lcd349.h")); err != nil {
			return nil, fmt.Errorf("%s isn't the lcd349 component (no include/lcd349.h); give the kit's components/lcd349", abs)
		}
		// A JSON string is a YAML double-quoted scalar: quotes, backslashes
		// and newlines in the path stay the path's.
		q, err := json.Marshal(abs)
		if err != nil {
			return nil, err
		}
		kitPath = string(q)
	} else if o.KitRepo == "" && !Released() {
		return nil, fmt.Errorf("this lcd349 (%s) has no published release to pin; give --kit-path <kit checkout>/components/lcd349", lcd349kit.Version)
	}
	repo := lcd349kit.RepoURL
	if o.KitRepo != "" {
		repo = o.KitRepo
	}
	if ents, err := os.ReadDir(o.Dir); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("%s isn't empty", o.Dir)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data := map[string]string{
		"Name":       o.Name,
		"KitVersion": lcd349kit.Version,
		"KitTag":     "v" + lcd349kit.Version,
		"RepoURL":    repo,
		"KitPath":    kitPath,
	}
	root := "templates/app"
	var written []string
	err := fs.WalkDir(lcd349kit.AppTemplate, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := lcd349kit.AppTemplate.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, root+"/")
		if strings.HasSuffix(rel, ".tmpl") {
			rel = strings.TrimSuffix(rel, ".tmpl")
			t, err := template.New(rel).Option("missingkey=error").Parse(string(b))
			if err != nil {
				return err
			}
			var out bytes.Buffer
			if err := t.Execute(&out, data); err != nil {
				return err
			}
			b = out.Bytes()
		}
		if rel == "gitignore" {
			rel = ".gitignore"
		}
		dst := filepath.Join(o.Dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		written = append(written, path.Clean(rel))
		return nil
	})
	return written, err
}
