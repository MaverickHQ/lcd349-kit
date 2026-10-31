package repocheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const viewerDir = "../../viewer"

// TestViewerVersionMatchesKit: lcd349-view --version reports the kit version.
func TestViewerVersionMatchesKit(t *testing.T) {
	cm, err := os.ReadFile(filepath.Join(viewerDir, "CMakeLists.txt"))
	if err != nil {
		t.Fatal(err)
	}
	core, err := os.ReadFile(filepath.Join(includeDir, "lcd349_core.h"))
	if err != nil {
		t.Fatal(err)
	}
	view := regexp.MustCompile(`set\(VIEW_VERSION "([^"]+)"`).FindSubmatch(cm)
	kit := regexp.MustCompile(`#define LCD349_VERSION_STR "([^"]+)"`).FindSubmatch(core)
	if view == nil || kit == nil || string(view[1]) != string(kit[1]) {
		t.Fatalf("viewer VIEW_VERSION %q != LCD349_VERSION_STR %q", view, kit)
	}
}

// TestViewerBuildsAlone: the viewer is built from its own directory (task 6.2
// builds it from a copy), so its program sources include only system, SDL
// and viewer headers. Only the unit tests reach into the component.
func TestViewerBuildsAlone(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(viewerDir, "*.[ch]"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no viewer sources: %v", err)
	}
	for _, f := range files {
		if filepath.Base(f) == "test_core.c" {
			continue
		}
		for _, inc := range includes(t, f) {
			if inc.quoted {
				if _, err := os.Stat(filepath.Join(viewerDir, inc.name)); err != nil {
					t.Errorf("%s includes \"%s\", which isn't in viewer/", filepath.Base(f), inc.name)
				}
			} else if strings.HasPrefix(inc.name, "lcd349") || strings.HasPrefix(inc.name, "lvgl") {
				t.Errorf("%s includes <%s>", filepath.Base(f), inc.name)
			}
		}
	}
}

// TestProductsNeverLinkSDL enforces requirement 4.4 for the component and the examples.
func TestProductsNeverLinkSDL(t *testing.T) {
	for _, root := range []string{"../../components", "../../examples"} {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (strings.HasPrefix(d.Name(), "build") || d.Name() == "managed_components") {
				return filepath.SkipDir
			}
			if d.IsDir() || !(strings.HasSuffix(p, ".c") || strings.HasSuffix(p, ".h") || strings.HasSuffix(p, ".cpp") || filepath.Base(p) == "CMakeLists.txt") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if regexp.MustCompile(`(?i)#\s*include\s*[<"]SDL|find_package\(\s*SDL|SDL2::|-lSDL2`).Match(b) {
				t.Errorf("%s refers to SDL; only viewer/ may", p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

type include struct {
	name   string
	quoted bool
}

func includes(t *testing.T, path string) []include {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []include
	for _, line := range strings.Split(string(b), "\n") {
		if m := includeRe.FindStringSubmatch(line); m != nil {
			out = append(out, include{m[2], m[1] == `"`})
		}
	}
	return out
}
