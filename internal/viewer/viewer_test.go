package viewer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// besideLCD349 makes Path see an lcd349 in a fresh directory, which it
// returns, with no $LCD349_VIEW and no way to build the viewer.
func besideLCD349(t *testing.T) string {
	dir := t.TempDir()
	saved := executable
	executable = func() (string, error) { return filepath.Join(dir, "lcd349"), nil }
	t.Cleanup(func() { executable = saved })
	t.Setenv("LCD349_VIEW", "")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", "")
	return dir
}

func touch(t *testing.T, p string) {
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestViewerFindsTheReleaseName enforces requirement 2.10: a release's viewer,
// downloaded beside lcd349 under its asset's name, is found.
func TestViewerFindsTheReleaseName(t *testing.T) {
	dir := besideLCD349(t)
	release := filepath.Join(dir, fmt.Sprintf("lcd349-view_%s_%s_%s", lcd349kit.Version, runtime.GOOS, runtime.GOARCH))
	touch(t, release)
	got, err := Path(os.Stderr)
	if err != nil || got != release {
		t.Fatalf("Path() = %q, %v; want %s", got, err, release)
	}
}

func TestViewerPrefersItsPlainName(t *testing.T) {
	dir := besideLCD349(t)
	plain := filepath.Join(dir, "lcd349-view")
	touch(t, plain)
	touch(t, filepath.Join(dir, fmt.Sprintf("lcd349-view_%s_%s_%s", lcd349kit.Version, runtime.GOOS, runtime.GOARCH)))
	if got, err := Path(os.Stderr); err != nil || got != plain {
		t.Fatalf("Path() = %q, %v; want %s", got, err, plain)
	}
}

func TestViewerIgnoresOtherVersionsAndPlatforms(t *testing.T) {
	dir := besideLCD349(t)
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	for _, name := range []string{
		"lcd349-view_0.0.1_" + runtime.GOOS + "_" + runtime.GOARCH,
		fmt.Sprintf("lcd349-view_%s_%s_%s", lcd349kit.Version, runtime.GOOS, other),
	} {
		touch(t, filepath.Join(dir, name))
	}
	if got, err := Path(os.Stderr); err == nil {
		t.Fatalf("Path() = %q; want the build's error, with no viewer of this version and platform beside lcd349", got)
	}
}
