package clitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func needIDF(t *testing.T) {
	t.Helper()
	if os.Getenv("LCD349_REQUIRE_IDF") == "1" {
		return
	}
	c := newCLI(t)
	if _, _, code := c.run("doctor", "--no-viewer"); code != 0 {
		t.Skip("ESP-IDF isn't set up here (lcd349 doctor fails); LCD349_REQUIRE_IDF=1 makes this a failure")
	}
}

// TestBuildNamesTheKitReplacement: a product that requires esp_lcd fails to
// build for the emulator with the kit API to use instead (task 6.2).
func TestBuildNamesTheKitReplacement(t *testing.T) {
	needIDF(t)
	c := newCLI(t)
	dir := t.TempDir()
	files := map[string]string{
		"CMakeLists.txt":      "cmake_minimum_required(VERSION 3.16)\nset(COMPONENTS main)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(uses_lcd)\n",
		"main/CMakeLists.txt": "idf_component_register(SRCS \"main.c\" REQUIRES esp_lcd)\n",
		"main/main.c":         "void app_main(void) {}\n",
	}
	for name, body := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	out, errOut, code := c.run("build", "-C", dir, "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out, errOut)
	}
	var res struct {
		OK     bool `json:"ok"`
		Errors []struct {
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"errors"`
		Log string `json:"log"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.OK || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "esp_lcd") || !strings.Contains(res.Errors[0].Hint, "lcd349_display()") {
		t.Fatalf("result %+v", res)
	}
	if res.Log != filepath.Join(dir, "build-emu", "lcd349-build.log") {
		t.Fatalf("log %s", res.Log)
	}
	// The emulator build stays in build-emu: the board's build/ is untouched (R7.2).
	if _, err := os.Stat(filepath.Join(dir, "build")); !os.IsNotExist(err) {
		t.Fatalf("build/ was created: %v", err)
	}
}

// TestDoctor: every check runs; on a set-up machine none fails, and the
// viewer is built from the binary's own copy into the cache.
func TestDoctor(t *testing.T) {
	needIDF(t)
	c := newCLI(t)
	c.env = append(c.env, "XDG_CACHE_HOME="+filepath.Join(c.root, "cache"))
	args := []string{"doctor", "--json", "-C", filepath.Join("..", "..", "examples", "inputs")}
	viewer := os.Getenv("LCD349_REQUIRE_VIEWER") == "1"
	if !viewer {
		args = append(args, "--no-viewer")
	}
	out, errOut, code := c.run(args...)
	var checks []struct {
		Name, Status, Detail, Fix string
	}
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	names := map[string]string{}
	for _, ch := range checks {
		names[ch.Name] = ch.Status
		if ch.Status == "fail" {
			t.Errorf("%s fails: %s (fix: %s)", ch.Name, ch.Detail, ch.Fix)
		}
	}
	for _, n := range []string{"ESP-IDF", "Python TLS", "cmake", "kit pin", "per-target lock files", "instances"} {
		if names[n] == "" {
			t.Errorf("no %q check", n)
		}
	}
	if code != 0 {
		t.Errorf("exit %d", code)
	}
	if viewer {
		if names["viewer"] == "" || names["viewer"] == "fail" {
			t.Errorf("viewer check %q", names["viewer"])
		}
		bins, _ := filepath.Glob(filepath.Join(c.root, "cache", "lcd349", "viewer", "*", "build", "lcd349-view"))
		if len(bins) != 1 {
			t.Errorf("the viewer wasn't built into the cache: %v", bins)
		}
	}
}
