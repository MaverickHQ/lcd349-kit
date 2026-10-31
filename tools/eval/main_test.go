package main

import (
	"os"
	"path/filepath"
	"testing"
)

// kitProject writes the least that loads as an lcd349 project: a CMake
// project whose main component depends on lcd349.
func kitProject(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"CMakeLists.txt":         "project(notes)\n",
		"main/idf_component.yml": "dependencies:\n  lcd349:\n    path: ../lcd349\n",
	}
	for f, body := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFindProject: the checks run on the agent's own project, never on a copy
// under managed_components or a build directory, even one that comes first.
func TestFindProject(t *testing.T) {
	ws := t.TempDir()
	if got := findProject(ws); got != "" {
		t.Fatalf("an empty workspace: found %q", got)
	}
	kitProject(t, filepath.Join(ws, "a", "build-emu", "copy"))
	kitProject(t, filepath.Join(ws, "managed_components", "copy"))
	if got := findProject(ws); got != "" {
		t.Fatalf("only copies in skipped directories: found %q", got)
	}
	kitProject(t, filepath.Join(ws, "notes"))
	if got, want := findProject(ws), filepath.Join(ws, "notes"); got != want {
		t.Fatalf("found %q, want %q", got, want)
	}
}
