package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newProject(t *testing.T, manifest string, locks bool) string {
	t.Helper()
	dir := t.TempDir()
	cm := "cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\n"
	if locks {
		cm += LockLine + "\n"
	}
	write(t, filepath.Join(dir, "CMakeLists.txt"), cm+"project(demo_app)\n")
	if manifest != "" {
		write(t, filepath.Join(dir, "main", "idf_component.yml"), manifest)
	}
	return dir
}

func TestLoad(t *testing.T) {
	dir := newProject(t, "dependencies:\n  lcd349:\n    git: https://github.com/o/lcd349-kit.git\n    path: components/lcd349\n    version: v0.1.0\n", true)
	p, err := LoadKitProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo_app" || !p.PerTargetLocks || p.Kit.Version != "v0.1.0" || p.Kit.Path != "components/lcd349" {
		t.Fatalf("%+v %+v", p, p.Kit)
	}
	dir = newProject(t, "dependencies:\n  lcd349:\n    override_path: ../kit/components/lcd349\n", false)
	if p, err = Load(dir); err != nil || p.Kit.OverridePath == "" || p.PerTargetLocks {
		t.Fatalf("%+v %v", p, err)
	}
	dir = newProject(t, "dependencies:\n  lvgl/lvgl: \"9.6.0\"\n", true)
	if _, err := LoadKitProject(dir); err == nil || !strings.Contains(err.Error(), "depends on lcd349") {
		t.Fatalf("a project without the kit: %v", err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty directory loaded as a project")
	}
}

// TestInside: paths are confined to the project, symlinks included (KT3).
func TestInside(t *testing.T) {
	dir := newProject(t, "dependencies:\n  lcd349: \"*\"\n", true)
	p, err := LoadKitProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(dir, "escape"))
	os.MkdirAll(filepath.Join(dir, "tests", "goldens"), 0o755)
	for path, ok := range map[string]bool{
		"tests/goldens/new.png":              true, // doesn't exist yet
		filepath.Join(dir, "tests/a.yaml"):   true,
		"../x.png":                           false,
		"tests/../../x.png":                  false,
		"escape/golden.png":                  false, // a symlink out
		filepath.Join(outside, "golden.png"): false,
	} {
		got, err := p.Inside(path)
		if ok && (err != nil || !strings.HasPrefix(got, p.Dir)) {
			t.Errorf("Inside(%q) = %q, %v; want it allowed", path, got, err)
		}
		if !ok && err == nil {
			t.Errorf("Inside(%q) = %q; want it refused", path, got)
		}
	}
}
