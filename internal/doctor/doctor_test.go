package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byName(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func TestProjectChecks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "CMakeLists.txt"), "project(a)\n")
	got := byName(checkProject(dir))
	if c := got["kit pin"]; c.Status != Fail || c.Fix == "" {
		t.Errorf("no lcd349 dependency: %+v", c)
	}
	if c := got["per-target lock files"]; c.Status != Warn || !strings.Contains(c.Fix, project.LockLine) {
		t.Errorf("shared lock file: %+v", c)
	}

	kit := filepath.Join(t.TempDir(), "lcd349")
	write(t, filepath.Join(kit, "include", "lcd349_core.h"), `#define LCD349_VERSION_STR "9.9.9"`+"\n")
	write(t, filepath.Join(dir, "CMakeLists.txt"), project.LockLine+"\nproject(a)\n")
	write(t, filepath.Join(dir, "main", "idf_component.yml"), "dependencies:\n  lcd349:\n    override_path: "+kit+"\n")
	got = byName(checkProject(dir))
	if c := got["kit pin"]; c.Status != Warn || !strings.Contains(c.Detail, "9.9.9") {
		t.Errorf("a kit checkout of another version: %+v", c)
	}
	if c := got["per-target lock files"]; c.Status != OK {
		t.Errorf("per-target lock files: %+v", c)
	}

	write(t, filepath.Join(dir, "main", "idf_component.yml"), "dependencies:\n  lcd349:\n    git: https://example.com/k.git\n    version: v"+strings.TrimSuffix(lcd349kit.Version, "-dev")+"\n")
	if c := byName(checkProject(dir))["kit pin"]; c.Status != OK {
		t.Errorf("a matching pin: %+v", c)
	}
	if c := byName(checkProject(t.TempDir()))["project"]; c.Status != Fail {
		t.Errorf("not a project: %+v", c)
	}
}
