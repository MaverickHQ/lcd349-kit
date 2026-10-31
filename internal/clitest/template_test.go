package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTemplateBuildsAndPasses: lcd349 new makes a project that builds for
// the emulator and passes its own scenario on the first run, golden
// included (task 6.5). It builds LVGL from scratch, so it runs only with
// LCD349_TEMPLATE_BUILD=1 (CI sets it; CI also builds the template for esp32s3).
func TestTemplateBuildsAndPasses(t *testing.T) {
	if os.Getenv("LCD349_TEMPLATE_BUILD") != "1" {
		t.Skip("set LCD349_TEMPLATE_BUILD=1 to build the template (a few minutes)")
	}
	c := newCLI(t)
	kit, _ := filepath.Abs(filepath.Join("..", "..", "components", "lcd349"))
	dir := filepath.Join(t.TempDir(), "hello_bar")
	c.must("new", dir, "--kit-path", kit)
	out, errOut, code := c.run("scenario", filepath.Join(dir, "tests", "app.scenario.yaml"))
	if code != 0 || !strings.Contains(out, "passed: 9 steps") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if out := c.must("doctor", "--no-viewer", "-C", dir); !strings.Contains(out, "ok    per-target lock files") {
		t.Fatalf("doctor:\n%s", out)
	}
}
