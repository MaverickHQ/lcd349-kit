package build

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/emulator-components.md")

// TestParseResolveFailure: ESP-IDF v5.5.5's own output when a product
// requires esp_lcd for the linux target (testdata, recorded in task 6.2).
func TestParseResolveFailure(t *testing.T) {
	b, err := os.ReadFile("testdata/resolve-esp_lcd.txt")
	if err != nil {
		t.Fatal(err)
	}
	ds := Parse(string(b), "/work/unsup")
	if len(ds) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %v", len(ds), ds)
	}
	d := ds[0]
	if d.File != "" || !strings.Contains(d.Message, "Failed to resolve component 'esp_lcd'") || !strings.Contains(d.Message, "not registered") {
		t.Fatalf("diagnostic %+v", d)
	}
	if !strings.Contains(d.Hint, "lcd349_display()") {
		t.Fatalf("hint %q doesn't name lcd349_display()", d.Hint)
	}
}

func TestParseCompilerAndLinker(t *testing.T) {
	out := strings.Join([]string{
		"[3/10] Building C object esp-idf/main/CMakeFiles/__idf_main.dir/main.c.o",
		"/work/app/main/main.c:1:10: fatal error: 'driver/gpio.h' file not found",
		"/work/app/main/ui.c:42:5: error: use of undeclared identifier 'lv_foo'",
		"/work/app/main/ui.c:43:1: warning: unused variable 'x'",
		"/opt/x/lvgl/src/core/lv_obj.c:7:3: error: something in a dependency",
		"Undefined symbols for architecture arm64:",
		`  "_i2c_new_master_bus", referenced from:`,
		"main.c:(.text+0x10): undefined reference to `esp_light_sleep_start'",
		"ninja: build stopped: subcommand failed.",
	}, "\n")
	ds := Parse(out, "/work/app")
	want := []Diagnostic{
		{File: "main/main.c", Line: 1, Column: 10, Message: "'driver/gpio.h' file not found"},
		{File: "main/ui.c", Line: 42, Column: 5, Message: "use of undeclared identifier 'lv_foo'"},
		{File: "/opt/x/lvgl/src/core/lv_obj.c", Line: 7, Column: 3, Message: "something in a dependency"},
		{Message: "undefined symbol i2c_new_master_bus"},
		{Message: "undefined symbol esp_light_sleep_start"},
	}
	if len(ds) != len(want) {
		t.Fatalf("got %d diagnostics, want %d:\n%v", len(ds), len(want), ds)
	}
	for i, w := range want {
		d := ds[i]
		if d.File != w.File || d.Line != w.Line || d.Column != w.Column || d.Message != w.Message {
			t.Errorf("diagnostic %d = %+v, want %+v", i, d, w)
		}
	}
	hints := map[int]string{0: "lcd349_buttons_subscribe()", 3: "lcd349_rtc_get()", 4: "lcd349_sleep_until_button()"}
	for i, h := range hints {
		if !strings.Contains(ds[i].Hint, h) {
			t.Errorf("diagnostic %d hint %q doesn't name %s", i, ds[i].Hint, h)
		}
	}
	if ds[1].Hint != "" {
		t.Errorf("a plain compiler error got hint %q", ds[1].Hint)
	}
}

// TestComponentsDocIsCurrent keeps docs/emulator-components.md generated
// from the table (go test ./internal/build -update rewrites it).
func TestComponentsDocIsCurrent(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "emulator-components.md")
	want := ComponentsDoc()
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s is stale or missing; run go test ./internal/build -update", path)
	}
}

func TestReadVersion(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "tools", "cmake"), 0o755)
	os.WriteFile(filepath.Join(dir, "tools", "cmake", "version.cmake"),
		[]byte("set(IDF_VERSION_MAJOR 5)\nset(IDF_VERSION_MINOR 5)\nset(IDF_VERSION_PATCH 5)\n"), 0o644)
	if v, err := ReadVersion(dir); err != nil || v != "v5.5.5" {
		t.Fatalf("ReadVersion = %q, %v", v, err)
	}
}

func TestMergeReplacesAndAdds(t *testing.T) {
	got := merge([]string{"A=1", "PATH=/bin", "B=2"}, []string{"PATH=/x:/bin", "C=3"})
	if strings.Join(got, " ") != "A=1 B=2 PATH=/x:/bin C=3" {
		t.Fatalf("merge = %v", got)
	}
}

// TestEnvironmentInExportedShell: with export.sh sourced, ESP-IDF's exporter
// leaves out IDF_PYTHON_ENV_PATH; lcd349 must still find the Python
// environment (found in task 6.2's verification run).
func TestEnvironmentInExportedShell(t *testing.T) {
	idf, err := Find()
	if err != nil {
		t.Skip("no ESP-IDF here")
	}
	fresh, err := Environment(context.Background(), true)
	if err != nil {
		t.Skipf("ESP-IDF isn't set up: %v", err)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("IDF_PATH", idf.Path)
	t.Setenv("IDF_PYTHON_ENV_PATH", lookup(fresh.Exports, "IDF_PYTHON_ENV_PATH"))
	t.Setenv("ESP_IDF_VERSION", lookup(fresh.Exports, "ESP_IDF_VERSION"))
	got, err := Environment(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Python != fresh.Python || lookup(got.Exports, "IDF_PYTHON_ENV_PATH") == "" {
		t.Fatalf("python %q (want %q), exports %v", got.Python, fresh.Python, got.Exports)
	}
}
