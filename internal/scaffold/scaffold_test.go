package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
)

func kit(t *testing.T) string {
	t.Helper()
	abs, _ := filepath.Abs(filepath.Join("..", "..", "components", "lcd349"))
	return abs
}

func TestNewFromTemplate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My-Bar App")
	files, err := New(Options{Dir: dir, KitPath: kit(t)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "CLAUDE.md", "CMakeLists.txt", "main/CMakeLists.txt", "main/idf_component.yml",
		"main/main.c", "sdkconfig.defaults", "sdkconfig.defaults.esp32s3", "tests/app.scenario.yaml", "tests/goldens/home.png"}
	got := strings.Join(files, " ")
	for _, w := range want {
		if !strings.Contains(" "+got+" ", " "+w+" ") {
			t.Errorf("%s wasn't written (got %s)", w, got)
		}
	}
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		if strings.Contains(string(b), "{{") {
			t.Errorf("%s has an unrendered template action", f)
		}
	}
	// It is an lcd349 project as the MCP server and doctor see it.
	p, err := project.LoadKitProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my_bar_app" || p.Kit.OverridePath != kit(t) || !p.PerTargetLocks {
		t.Fatalf("%+v %+v", p, p.Kit)
	}
	main, _ := os.ReadFile(filepath.Join(dir, "main", "main.c"))
	if !strings.Contains(string(main), `TAG = "my_bar_app"`) {
		t.Error("main.c doesn't use the project name for its log tag")
	}
	// Never into a directory with files in it.
	if _, err := New(Options{Dir: dir, KitPath: kit(t)}); err == nil || !strings.Contains(err.Error(), "isn't empty") {
		t.Fatalf("a second New into the same directory: %v", err)
	}
}

func TestNewRefuses(t *testing.T) {
	base := t.TempDir()
	if _, err := New(Options{Dir: filepath.Join(base, "a"), Name: "Bad-Name", KitPath: kit(t)}); err == nil {
		t.Error("an invalid name was accepted")
	}
	if _, err := New(Options{Dir: filepath.Join(base, "b"), KitPath: base}); err == nil || !strings.Contains(err.Error(), "isn't the lcd349 component") {
		t.Errorf("a kit path without the component: %v", err)
	}
	if !Released() {
		if _, err := New(Options{Dir: filepath.Join(base, "c")}); err == nil || !strings.Contains(err.Error(), "--kit-path") {
			t.Errorf("a development build pinned a release that doesn't exist: %v", err)
		}
	}
}

// A release candidate is pinned as the release will be, from its own
// repository, even by a development build (TestTemplateBuildsAgainstReleaseCandidate builds it).
func TestNewPinsCandidate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rc")
	if _, err := New(Options{Dir: dir, KitRepo: "file:///tmp/lcd349-kit.git"}); err != nil {
		t.Fatal(err)
	}
	p, err := project.LoadKitProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kit.Git != "file:///tmp/lcd349-kit.git" || p.Kit.Path != "components/lcd349" ||
		p.Kit.Version != "v"+lcd349kit.Version || p.Kit.OverridePath != "" {
		t.Fatalf("%+v", p.Kit)
	}
}

func TestNameFrom(t *testing.T) {
	for in, want := range map[string]string{"/x/hello": "hello", "Pager 2": "pager_2", "3d": "app_3d", "---": "app_"} {
		if got := NameFrom(in); got != want {
			t.Errorf("NameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
