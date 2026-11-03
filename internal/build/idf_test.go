package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// pinned splits lcd349kit.IDFVersion into its numbers, so these tests
// survive the next ESP-IDF bump.
func pinned(t *testing.T) (major, minor, patch int) {
	t.Helper()
	m := regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(lcd349kit.IDFVersion)
	if m == nil {
		t.Fatalf("IDFVersion %q isn't vX.Y.Z", lcd349kit.IDFVersion)
	}
	major, _ = strconv.Atoi(m[1])
	minor, _ = strconv.Atoi(m[2])
	patch, _ = strconv.Atoi(m[3])
	return major, minor, patch
}

func writeVersionCMake(t *testing.T, idfPath string, major, minor, patch int) {
	t.Helper()
	dir := filepath.Join(idfPath, "tools", "cmake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("set(IDF_VERSION_MAJOR %d)\nset(IDF_VERSION_MINOR %d)\nset(IDF_VERSION_PATCH %d)\n", major, minor, patch)
	if err := os.WriteFile(filepath.Join(dir, "version.cmake"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCandidatesFallBackToTheSameMinorLine: after the pinned version's
// directories and ~/esp/esp-idf, lcd349 looks at other installs of the
// pinned minor line, ~/esp/vX.Y.*/esp-idf, newest first, and then the
// container's. So the v5.5.5 install a user of 0.2.1 has is still found
// when the pin moves to v5.5.6 (and lcd349 doctor warns about it).
func TestCandidatesFallBackToTheSameMinorLine(t *testing.T) {
	major, minor, patch := pinned(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("IDF_PATH", "")
	esp := filepath.Join(home, "esp")
	v := func(mi, p int) string { return fmt.Sprintf("v%d.%d.%d", major, mi, p) }
	newer, newest := v(minor, patch+3), v(minor, patch+12) // "…12" sorts before "…3" as text
	names := []string{newer, newest, lcd349kit.IDFVersion,
		v(minor+1, 0),                          // another minor line
		v(minor, patch+1) + "-rc1",             // not a release
		fmt.Sprintf("v%d.%d0.1", major, minor), // vX.Y0.1 is another minor line too
		"esp-idf"}
	if patch > 0 {
		names = append(names, v(minor, patch-1))
	}
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(esp, n, "esp-idf"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		filepath.Join(esp, lcd349kit.IDFVersion, "esp-idf"),
		filepath.Join(esp, "esp-idf-"+lcd349kit.IDFVersion),
		filepath.Join(esp, "esp-idf"),
		filepath.Join(esp, newest, "esp-idf"),
		filepath.Join(esp, newer, "esp-idf"),
	}
	if patch > 0 {
		want = append(want, filepath.Join(esp, v(minor, patch-1), "esp-idf"))
	}
	want = append(want, "/opt/esp/idf")
	got := Candidates()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Candidates =\n  %q\nwant\n  %q", got, want)
	}

	// IDF_PATH still comes first.
	t.Setenv("IDF_PATH", "/somewhere/esp-idf")
	if got := Candidates(); len(got) == 0 || got[0] != "/somewhere/esp-idf" {
		t.Fatalf("with IDF_PATH set, Candidates = %q", got)
	}
}

// TestFindTakesAnOlderPatchWhenThePinIsMissing: with only an older patch
// of the pinned minor line installed, Find returns it, with its own version.
func TestFindTakesAnOlderPatchWhenThePinIsMissing(t *testing.T) {
	major, minor, patch := pinned(t)
	if patch == 0 {
		t.Skip("the pin is the minor line's first patch")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("IDF_PATH", "")
	older := filepath.Join(home, "esp", fmt.Sprintf("v%d.%d.%d", major, minor, patch-1), "esp-idf")
	writeVersionCMake(t, older, major, minor, patch-1)
	idf, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("v%d.%d.%d", major, minor, patch-1); idf.Path != older || idf.Version != want {
		t.Fatalf("Find = %s %s, want %s %s", idf.Path, idf.Version, older, want)
	}
}

// TestEnvironmentCacheIsPerToolsPath: the environment cache is keyed by
// IDF_TOOLS_PATH as well as the ESP-IDF path (unset is ESP-IDF's default,
// ~/.espressif), so an export made against one tools directory is never
// reused with another.
func TestEnvironmentCacheIsPerToolsPath(t *testing.T) {
	major, minor, patch := pinned(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	idfPath := filepath.Join(t.TempDir(), "esp-idf")
	writeVersionCMake(t, idfPath, major, minor, patch)
	t.Setenv("IDF_PATH", idfPath)
	toolsA, toolsB := t.TempDir(), t.TempDir()

	t.Setenv("IDF_TOOLS_PATH", "")
	unset := cachePath(idfPath)
	t.Setenv("IDF_TOOLS_PATH", filepath.Join(home, ".espressif"))
	if cachePath(idfPath) != unset {
		t.Errorf("IDF_TOOLS_PATH unset and set to the default ~/.espressif key different caches")
	}
	t.Setenv("IDF_TOOLS_PATH", toolsA)
	a := cachePath(idfPath)
	t.Setenv("IDF_TOOLS_PATH", toolsB)
	if b := cachePath(idfPath); a == b || a == unset {
		t.Errorf("cache paths: tools A %s, tools B %s, default %s", a, b, unset)
	}

	// A valid cache, written under tools A, is used under A only. Under B
	// lcd349 exports again, which fails here: this ESP-IDF has no idf_tools.py.
	python := filepath.Join(t.TempDir(), "python")
	if err := os.WriteFile(python, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IDF_TOOLS_PATH", toolsA)
	writeCache(cachePath(idfPath), &IDF{Path: idfPath, Version: lcd349kit.IDFVersion, Python: python, Exports: []string{"PATH=$PATH"}})
	if got, err := Environment(context.Background(), false); err != nil || got.Python != python {
		t.Fatalf("under tools A: %+v, %v; want the cached environment", got, err)
	}
	t.Setenv("IDF_TOOLS_PATH", toolsB)
	if got, err := Environment(context.Background(), false); err == nil && got.Python == python {
		t.Fatalf("under tools B, the environment cached under tools A was reused")
	}
}
