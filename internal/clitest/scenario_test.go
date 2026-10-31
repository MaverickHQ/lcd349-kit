package clitest

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kitProject makes a directory that is an lcd349 project as far as path
// checks go; the scenarios run the prebuilt examples/inputs with --elf.
func kitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte("project(scn)\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "main"), 0o755)
	os.WriteFile(filepath.Join(dir, "main", "idf_component.yml"), []byte("dependencies:\n  lcd349:\n    override_path: ../../components/lcd349\n"), 0o644)
	return dir
}

// The inputs screen settles once the simulated Wi-Fi has given up (about 1 s).
const settle = `
  - wait: { log: "wifi disconnected", timeout_ms: 5000 }
  - wait: { idle_ms: 200 }`

const passing = `name: inputs-tour
run: { clock: "2026-10-12T09:00:00Z", battery_mv: 3700, usb: false }
steps:
  - wait: { log: "inputs: ready" }` + settle + `
  - shot: { golden: goldens/home.png }
  - lint: { max_errors: 0 }
  - touch: { tap: [320, 88] }
  - wait: { log: "tap count 1" }
  - press: { button: boot, ms: 150 }
  - wait: { log: "gesture SHORT BOOT [0-9]+ ms" }
  - state: { set: { battery: { mv: 3600 } }, expect: { battery: { mv: 3600 }, source: battery } }
  - power: reset
  - wait: { log: "boot count 2, boot reason 1" }
  - state: { expect: { battery: { mv: 3600 } } }
`

func scenarioFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, "tests", name+".scenario.yaml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScenarioGoldens covers task 6.4's three cases: a missing golden fails
// without --update-goldens and passes with it; a passing scenario passes;
// a changed pixel fails with actual and diff images.
func TestScenarioGoldens(t *testing.T) {
	c := newCLI(t)
	elf := inputsELF(t)
	dir := kitProject(t)
	file := scenarioFile(t, dir, "tour", passing)
	gold := filepath.Join(dir, "tests", "goldens", "home.png")

	// 1. Missing golden: written, and the step fails.
	out, _, code := c.run("scenario", file, "--elf", elf)
	if code != 1 || !strings.Contains(out, "FAIL") || !strings.Contains(out, "missing") {
		t.Fatalf("missing golden: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(gold); err != nil {
		t.Fatalf("the golden wasn't written: %v", err)
	}
	// 2. Now it passes, every step, through a reset.
	out, _, code = c.run("scenario", file, "--elf", elf)
	if code != 0 || !strings.Contains(out, "passed: 13 steps") {
		t.Fatalf("passing scenario: exit %d\n%s", code, out)
	}
	// 3. One changed pixel fails, with actual and diff images beside the golden.
	b, _ := os.ReadFile(gold)
	img, _ := png.Decode(bytes.NewReader(b))
	rgba := image.NewRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	rgba.Set(5, 5, color.RGBA{255, 255, 0, 255})
	var nb bytes.Buffer
	png.Encode(&nb, rgba)
	os.WriteFile(gold, nb.Bytes(), 0o644)
	out, _, code = c.run("scenario", file, "--elf", elf)
	diff := filepath.Join(dir, "tests", "goldens", "home_diff.png")
	actual := filepath.Join(dir, "tests", "goldens", "home_actual.png")
	if code != 1 || !strings.Contains(out, "1 pixels differ") || !strings.Contains(out, diff) || !strings.Contains(out, "FAILED at step 4") {
		t.Fatalf("changed pixel: exit %d\n%s", code, out)
	}
	for _, p := range []string{diff, actual} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	// 4. --update-goldens accepts the screen and cleans up.
	if out, _, code = c.run("scenario", file, "--elf", elf, "--update-goldens"); code != 0 {
		t.Fatalf("--update-goldens: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(diff); !os.IsNotExist(err) {
		t.Fatal("the diff image is still there after the update")
	}
	if out, _, code = c.run("scenario", file, "--elf", elf); code != 0 {
		t.Fatalf("after --update-goldens: exit %d\n%s", code, out)
	}
	// A relative path from outside the project finds the same goldens.
	cmd := c.cmd("scenario", filepath.Join(filepath.Base(dir), "tests", "tour.scenario.yaml"), "--elf", elf)
	cmd.Dir = filepath.Dir(dir)
	if b, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(b), "passed: 13 steps") {
		t.Fatalf("relative path from the parent: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.Base(dir))); !os.IsNotExist(err) {
		t.Fatal("goldens were written under a doubled path")
	}
	// A missing golden with --update-goldens passes at once.
	file2 := scenarioFile(t, dir, "fresh", "steps:"+settle+"\n  - shot: goldens/fresh.png\n")
	if out, _, code = c.run("scenario", file2, "--elf", elf, "--update-goldens"); code != 0 || !strings.Contains(out, "golden written") {
		t.Fatalf("missing golden with --update-goldens: exit %d\n%s", code, out)
	}
	if n := len(c.list()); n != 0 {
		t.Fatalf("%d instances left running", n)
	}
}

// TestScenarioFailures: a failing wait keeps the screen, paths outside the
// project are refused, and --keep leaves the instance for inspection.
func TestScenarioFailures(t *testing.T) {
	c := newCLI(t)
	elf := inputsELF(t)
	dir := kitProject(t)
	file := scenarioFile(t, dir, "waits", "steps:\n  - wait: { text: \"NOT THERE\", timeout_ms: 300 }\n")
	out, _, code := c.run("scenario", file, "--elf", elf, "--keep")
	shot := filepath.Join(dir, "tests", "waits_step1_screen.png")
	if code != 1 || !strings.Contains(out, "timed out") || !strings.Contains(out, shot) || !strings.Contains(out, "still running") {
		t.Fatalf("failing wait: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(shot); err != nil {
		t.Fatal(err)
	}
	if l := c.list(); len(l) != 1 || l[0].State != "running" {
		t.Fatalf("--keep: instances %+v", l)
	}
	c.must("stop", "--all")

	outside := filepath.Join(t.TempDir(), "x.png")
	file = scenarioFile(t, dir, "escape", "steps:\n  - shot: "+outside+"\n")
	out, errOut, code := c.run("scenario", file, "--elf", elf)
	if code != 1 || !strings.Contains(out+errOut, "outside the project") {
		t.Fatalf("golden outside the project: exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("a golden was written outside the project")
	}
	bad := scenarioFile(t, dir, "typo", "steps:\n  - shot: { golden: a.png, tolerence_px: 2 }\n")
	if _, errOut, code := c.run("scenario", bad, "--elf", elf); code != 1 || !strings.Contains(errOut, "tolerence_px") {
		t.Fatalf("typo: exit %d %s", code, errOut)
	}
}
