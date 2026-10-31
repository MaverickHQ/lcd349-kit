package scaffold

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/build"
)

// TestTemplateBuildsAgainstReleaseCandidate: a project made as lcd349 new
// makes it, pinned by git to the release candidate (this repository's HEAD,
// tagged v<Version> in a bare repository, as it will be published), builds
// for both targets and passes its scenario (phase 5 requirement 2.1, seam
// S5K-2). Like TestTemplateBuildsAndPasses, it builds LVGL from scratch (twice
// here), so it runs only with LCD349_TEMPLATE_BUILD=1.
func TestTemplateBuildsAgainstReleaseCandidate(t *testing.T) {
	if os.Getenv("LCD349_TEMPLATE_BUILD") != "1" {
		t.Skip("set LCD349_TEMPLATE_BUILD=1 to build the template against the release candidate (a few minutes)")
	}
	idf, err := build.Environment(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	tmp := t.TempDir()

	// The candidate: HEAD, under the tag the release will carry.
	tag := "v" + lcd349kit.Version
	repo := filepath.Join(tmp, "lcd349-kit.git")
	head := strings.TrimSpace(run(t, nil, root, "git", "rev-parse", "HEAD"))
	run(t, nil, tmp, "git", "init", "--quiet", "--bare", repo)
	run(t, nil, tmp, "git", "--git-dir", repo, "fetch", "--quiet", "--no-tags", root, head)
	run(t, nil, tmp, "git", "--git-dir", repo, "tag", tag, head)

	dir := filepath.Join(tmp, "hello_bar")
	if _, err := New(Options{Dir: dir, KitRepo: "file://" + repo}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, "main", "idf_component.yml"))
	for _, want := range []string{"git: file://" + repo, "path: components/lcd349", "version: " + tag} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("the manifest doesn't pin the candidate (no %q):\n%s", want, manifest)
		}
	}

	// The emulator: lcd349 scenario builds it and runs its scenario.
	bin := filepath.Join(tmp, "lcd349")
	run(t, nil, root, "go", "build", "-o", bin, "github.com/MaverickHQ/lcd349-kit/cmd/lcd349")
	inst, err := os.MkdirTemp("/tmp", "l349rc-") // short: socket paths
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LCD349_INSTANCES="+filepath.Join(inst, "i"))
	t.Cleanup(func() {
		// In env, so --all stops only this test's instances, not the user's.
		stop := exec.Command(bin, "stop", "--all")
		stop.Env = env
		stop.Run()
		os.RemoveAll(inst)
	})
	cmd := exec.Command(bin, "scenario", filepath.Join(dir, "tests", "app.scenario.yaml"))
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "passed: 9 steps") {
		t.Fatalf("lcd349 scenario: %v\n%s", err, out)
	}

	// The board.
	run(t, idf.Env, dir, idf.Python, filepath.Join(idf.Path, "tools", "idf.py"), "set-target", "esp32s3", "build")

	// Each target's lock file resolved the kit from the candidate.
	for _, target := range []string{"linux", "esp32s3"} {
		lock, err := os.ReadFile(filepath.Join(dir, "dependencies.lock."+target))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(lock), repo) || !strings.Contains(string(lock), head) {
			t.Errorf("dependencies.lock.%s doesn't resolve lcd349 to %s at %s:\n%s", target, repo, head, lock)
		}
	}
}

// run runs a command with a fixed argv in dir (env nil: this process's) and
// returns its output, failing the test with the output's tail if it fails.
func run(t *testing.T, env []string, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := string(out)
		if len(s) > 4000 {
			s = "…" + s[len(s)-4000:]
		}
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, s)
	}
	return string(out)
}
