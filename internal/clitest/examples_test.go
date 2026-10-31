package clitest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleScenarios runs every example's scenarios headless against its
// goldens (requirement 11.3). An example that isn't built for the emulator
// is skipped, unless LCD349_REQUIRE_EXAMPLES=1 (CI builds them all first).
func TestExampleScenarios(t *testing.T) {
	examples, _ := filepath.Glob(filepath.Join("..", "..", "examples", "*"))
	require := os.Getenv("LCD349_REQUIRE_EXAMPLES") == "1"
	ran := 0
	for _, ex := range examples {
		files, _ := filepath.Glob(filepath.Join(ex, "tests", "*.scenario.yaml"))
		if len(files) == 0 {
			t.Errorf("examples/%s has no scenarios (R11.3)", filepath.Base(ex))
			continue
		}
		for _, f := range files {
			name := filepath.Base(ex) + "/" + strings.TrimSuffix(filepath.Base(f), ".scenario.yaml")
			t.Run(name, func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(ex, "build-emu", "project_description.json")); err != nil {
					if require {
						t.Fatalf("examples/%s isn't built for the emulator", filepath.Base(ex))
					}
					t.Skipf("examples/%s isn't built for the emulator (lcd349 build -C examples/%s)", filepath.Base(ex), filepath.Base(ex))
				}
				c := newCLI(t)
				out, errOut, code := c.run("scenario", f, "--no-build")
				if code != 0 {
					t.Fatalf("exit %d\n%s%s", code, out, errOut)
				}
				ran++
			})
		}
	}
	if require && ran == 0 {
		t.Fatal("no example scenario ran")
	}
}
