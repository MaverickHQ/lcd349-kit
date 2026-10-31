package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

func readRepo(t *testing.T) (raw, schema []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "device", "device.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err = os.ReadFile(filepath.Join(repoRoot, "device", "device.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw, schema
}

// mutate decodes device.json, lets f change it, and re-encodes it.
func mutate(t *testing.T, raw []byte, f func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func obj(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		m = m[p].(map[string]any)
	}
	return m
}

func rule(m map[string]any, id string) map[string]any {
	for _, r := range m["design_rules"].([]any) {
		if r := r.(map[string]any); r["id"] == id {
			return r
		}
	}
	panic("no rule " + id)
}

func TestRepoDeviceJSONIsValid(t *testing.T) {
	raw, schema := readRepo(t)
	d, err := Parse(raw, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Render(d, raw); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaRejectsBadDevices(t *testing.T) {
	raw, schema := readRepo(t)
	cases := map[string]func(m map[string]any){
		"missing ppi":   func(m map[string]any) { delete(obj(m, "display"), "ppi") },
		"unknown field": func(m map[string]any) { obj(m, "display")["vsync_hz"] = 60 },
		"touch wakes":   func(m map[string]any) { obj(m, "touch")["wake_source"] = true },
		"verified fact without value": func(m map[string]any) {
			f := obj(m, "display", "flush_ms")
			f["value"], f["verified"] = nil, "2026-10-12"
		},
		"value without verification": func(m map[string]any) {
			f := obj(m, "display", "flush_ms")
			f["value"], f["verified"] = 30, nil
		},
		"fact without source": func(m map[string]any) { delete(obj(m, "display", "flush_ms"), "source") },
		"bad date":            func(m map[string]any) { obj(m, "board")["verified"] = "yesterday" },
		"bad severity":        func(m map[string]any) { rule(m, "contrast")["severity"] = "fatal" },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(mutate(t, raw, f), schema); err == nil {
				t.Fatal("accepted an invalid device.json")
			}
		})
	}
}

func TestRefusesNullThresholdForErrorRule(t *testing.T) {
	raw, schema := readRepo(t)
	bad := mutate(t, raw, func(m map[string]any) {
		obj(rule(m, "touch-target"), "threshold")["value"] = nil
	})
	_, err := Parse(bad, schema)
	if err == nil || !strings.Contains(err.Error(), "touch-target") || !strings.Contains(err.Error(), "threshold is null") {
		t.Fatalf("want a null-threshold error naming touch-target, got %v", err)
	}

	// Switched off, the same rule is allowed: it just isn't enforced.
	off := mutate(t, bad, func(m map[string]any) { rule(m, "touch-target")["severity"] = "off" })
	if _, err := Parse(off, schema); err != nil {
		t.Fatalf("an off rule with a null threshold should pass: %v", err)
	}
}

func TestRefusesUnverifiedEnforcedRule(t *testing.T) {
	raw, schema := readRepo(t)
	bad := mutate(t, raw, func(m map[string]any) { rule(m, "font-min")["verified"] = nil })
	if _, err := Parse(bad, schema); err == nil || !strings.Contains(err.Error(), "font-min") {
		t.Fatalf("want an unverified-rule error naming font-min, got %v", err)
	}
}

func TestRefusesBadBatteryCurve(t *testing.T) {
	raw, schema := readRepo(t)
	bad := mutate(t, raw, func(m map[string]any) {
		obj(m, "power", "battery_curve")["assumed"] = []any{[]any{4200, 100}, []any{4250, 50}, []any{3300, 0}}
	})
	if _, err := Parse(bad, schema); err == nil || !strings.Contains(err.Error(), "battery_curve") {
		t.Fatalf("want a battery_curve error, got %v", err)
	}
}

func TestRefusesScreenOutsideCase(t *testing.T) {
	raw, schema := readRepo(t)
	bad := mutate(t, raw, func(m map[string]any) {
		f := obj(m, "case", "screen_offset_mm")
		f["value"], f["verified"] = []any{20, 5.9}, "2026-10-18"
	})
	if _, err := Parse(bad, schema); err == nil || !strings.Contains(err.Error(), "doesn't fit") {
		t.Fatalf("want a screen_offset_mm fit error, got %v", err)
	}
}

func TestButtonPositionsReachTheViewer(t *testing.T) {
	raw, schema := readRepo(t)
	bad := mutate(t, raw, func(m map[string]any) {
		p := obj(m, "buttons")["items"].([]any)[0].(map[string]any)["position_mm"].(map[string]any)
		p["value"], p["verified"] = "left side", "2026-10-18"
	})
	if _, err := Parse(bad, schema); err == nil || !strings.Contains(err.Error(), "buttons[BOOT].position_mm") {
		t.Fatalf("want a position_mm error naming BOOT, got %v", err)
	}

	// A measured position is drawn on the case; an unmeasured one is not.
	good := mutate(t, raw, func(m map[string]any) {
		items := obj(m, "buttons")["items"].([]any)
		boot := items[0].(map[string]any)["position_mm"].(map[string]any)
		boot["value"], boot["verified"] = nil, nil
		pwr := items[1].(map[string]any)["position_mm"].(map[string]any)
		pwr["value"], pwr["verified"] = []any{90.5, -4}, "2026-10-20"
	})
	d, err := Parse(good, schema)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(d, good)
	if err != nil {
		t.Fatal(err)
	}
	h := string(files["viewer/device_gen.h"])
	for _, want := range []string{`{"BOOT", 'b', 0, 0.0f, 0.0f}`, `{"PWR", 'p', 1, 90.5f, -4.0f}`} {
		if !strings.Contains(h, want) {
			t.Errorf("viewer/device_gen.h lacks %s:\n%s", want, h)
		}
	}
}

// TestBusFactsAreGenerated: the hardware backend's lanes need the main I2C
// bus, the expander, the codec's wiring, the backlight's polarity and the
// battery's ADC channel as generated facts, never copied numbers. Expected
// values: spikes/hw/RESULTS.md (bus scan, schematic pin table) and the
// spike's pins (tones heard through them).
func TestBusFactsAreGenerated(t *testing.T) {
	d, raw, err := Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(d, raw)
	if err != nil {
		t.Fatal(err)
	}
	h := string(files["components/lcd349/include/lcd349_board.h"])
	for _, want := range []string{
		"#define LCD349_PIN_I2C_SDA 47\n",
		"#define LCD349_PIN_I2C_SCL 48\n",
		"#define LCD349_EXPANDER_I2C_ADDR 0x20\n",
		"#define LCD349_PIN_EXPANDER_INT 42\n",
		"#define LCD349_EXPANDER_PIN_TP_INT 0\n",
		"#define LCD349_EXPANDER_PIN_BL_EN 1\n",
		"#define LCD349_EXPANDER_PIN_LCD_TE 5\n",
		"#define LCD349_EXPANDER_PIN_SYS_EN 6\n",
		"#define LCD349_EXPANDER_PIN_NS_MODE 7\n",
		"#define LCD349_CODEC_I2C_ADDR 0x18\n",
		"#define LCD349_PIN_I2S_MCLK 7\n",
		"#define LCD349_PIN_I2S_BCLK 15\n",
		"#define LCD349_PIN_I2S_WS 46\n",
		"#define LCD349_PIN_I2S_DOUT 45\n",
		"#define LCD349_BACKLIGHT_ACTIVE_LOW 1\n",
		"#define LCD349_BATTERY_ADC_UNIT 1\n",
		"#define LCD349_BATTERY_ADC_CHANNEL 3\n",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("lcd349_board.h lacks %q", strings.TrimSpace(want))
		}
	}

	// The power hold must name the expander's SYS_EN pin.
	bad := mutate(t, raw, func(m map[string]any) { obj(m, "power", "hold")["pin"] = "P5" })
	if _, err := Parse(bad, readSchema(t)); err == nil || !strings.Contains(err.Error(), "power.hold.pin") {
		t.Fatalf("want a power.hold.pin error, got %v", err)
	}
}

// TestRefreshRateIsAFact: the panel's refresh rate (task 0.3: TE every
// 16.41 ms) is a fact with its source, shown in docs/hardware.md (D1-8).
func TestRefreshRateIsAFact(t *testing.T) {
	raw, schema := readRepo(t)
	withRate := mutate(t, raw, func(m map[string]any) {
		obj(m, "display")["refresh_hz"] = map[string]any{"value": 61, "source": "TE every 16.41 ms (test)", "verified": "2026-10-18"}
	})
	d, err := Parse(withRate, schema)
	if err != nil {
		t.Fatalf("display.refresh_hz is refused: %v", err)
	}
	files, err := Render(d, withRate)
	if err != nil {
		t.Fatal(err)
	}
	if md := string(files["docs/hardware.md"]); !strings.Contains(md, "| Panel refresh rate | 61 Hz | TE every 16.41 ms (test) | 2026-10-18 |") {
		t.Errorf("docs/hardware.md doesn't show the refresh rate:\n%s", md)
	}
}

// TestButtonXPositionsAreGenerated: each button's centre as a landscape x
// (R2.9), from its position_mm (millimetres from the active area's left, as
// the viewer draws it) and display.px_per_mm. Worked by hand from
// device.json: PWR 69.4 mm x 7.5 = 520.5 -> 521; BOOT 79.6 mm x 7.5 = 597.
// At K-07's 84 mm (7.62 px per mm, requirement 6.1) they are the spec's 529
// and 607: 69.4 x 7.62 = 528.8, 79.6 x 7.62 = 606.6.
func TestButtonXPositionsAreGenerated(t *testing.T) {
	raw, schema := readRepo(t)
	header := func(raw []byte) string {
		d, err := Parse(raw, schema)
		if err != nil {
			t.Fatal(err)
		}
		files, err := Render(d, raw)
		if err != nil {
			t.Fatal(err)
		}
		return string(files["components/lcd349/include/lcd349_board.h"])
	}
	h := header(raw)
	for _, want := range []string{"#define LCD349_BUTTON_PWR_X_PX 521\n", "#define LCD349_BUTTON_BOOT_X_PX 597\n"} {
		if !strings.Contains(h, want) {
			t.Errorf("lcd349_board.h lacks %q", strings.TrimSpace(want))
		}
	}
	h = header(mutate(t, raw, func(m map[string]any) { obj(m, "display")["px_per_mm"] = 7.62 }))
	for _, want := range []string{"#define LCD349_BUTTON_PWR_X_PX 529\n", "#define LCD349_BUTTON_BOOT_X_PX 607\n"} {
		if !strings.Contains(h, want) {
			t.Errorf("at 7.62 px per mm, lcd349_board.h lacks %q", strings.TrimSpace(want))
		}
	}
}

func readSchema(t *testing.T) []byte {
	t.Helper()
	_, schema := readRepo(t)
	return schema
}

func TestStaleDetectsChangedAndMissingFiles(t *testing.T) {
	raw, schema := readRepo(t)
	d, err := Parse(raw, schema)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(d, raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := Write(root, files); err != nil {
		t.Fatal(err)
	}
	if s := Stale(root, files); len(s) != 0 {
		t.Fatalf("fresh output reported stale: %v", s)
	}

	changed := filepath.Join(root, "components/lcd349/include/lcd349_board.h")
	if err := os.WriteFile(changed, []byte("/* edited by hand */\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "docs/hardware.md")); err != nil {
		t.Fatal(err)
	}
	s := Stale(root, files)
	if strings.Join(s, ",") != "components/lcd349/include/lcd349_board.h,docs/hardware.md" {
		t.Fatalf("stale = %v", s)
	}

	// A changed fact makes every dependent file stale.
	raw2 := mutate(t, raw, func(m map[string]any) { obj(m, "display", "flush_ms")["value"] = 14 })
	d2, err := Parse(raw2, schema)
	if err != nil {
		t.Fatal(err)
	}
	files2, err := Render(d2, raw2)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(root, files); err != nil {
		t.Fatal(err)
	}
	if s := Stale(root, files2); len(s) == 0 {
		t.Fatal("changing flush_ms left every generated file current")
	}
}

// TestGeneratedFilesAreCurrent is the --check run that CI also does.
func TestGeneratedFilesAreCurrent(t *testing.T) {
	d, raw, err := Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(d, raw)
	if err != nil {
		t.Fatal(err)
	}
	if s := Stale(repoRoot, files); len(s) > 0 {
		t.Fatalf("stale generated files %v; run: go run ./internal/devicegen", s)
	}
}

func TestCStringEscapes(t *testing.T) {
	if got := cstr(`a "b" \ c` + "\n×"); got != `"a \"b\" \\ c\n×"` {
		t.Fatalf("cstr = %s", got)
	}
}

// TestSkillRendersFromRepository: the skill's generated files come out of
// device.json, the public headers and examples/gallery.
func TestSkillRendersFromRepository(t *testing.T) {
	d, raw, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	files, err := RenderSkill("../..", d, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"plugin/skills/lcd349/SKILL.md":                         {"7.5 px per mm", "`touch-target` (error) | 56 px"},
		"plugin/skills/lcd349/references/sdk-api.md":            {"lcd349_console_read_line", "LCD349_RULE_TOUCH_TARGET_PX", "## `lcd349_wifi.h`"},
		"plugin/skills/lcd349/references/recipes.md":            {"lv_obj_t *list_create(", "hold_accepts", "lcd349_classifier_feed"},
		"plugin/skills/lcd349/references/design-guide.md":       {"| Smallest touch target | 56 × 56 | 7.5 |"},
		"plugin/skills/lcd349/references/hardware-checklist.md": {"idf.py set-target esp32s3 build", "Speaker loudness"},
	}
	for path, subs := range want {
		got := string(files[path])
		for _, s := range subs {
			if !strings.Contains(got, s) {
				t.Errorf("%s lacks %q", path, s)
			}
		}
	}
}

func TestRecipeRegionsAndBlock(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.h"), []byte("/* recipe:start x */\nint x(void);\n/* recipe:end */\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.c"), []byte("/* recipe:start x */\nint x(void) { return 1; }\n/* recipe:end */\n"), 0o644)
	r, err := recipeRegions(dir)
	if err != nil || r["x"] != "int x(void);\n\nint x(void) { return 1; }" {
		t.Fatalf("regions %q %v", r, err)
	}
	if _, err := replaceBlock([]byte("no markers"), []byte("x")); err == nil {
		t.Fatal("a SKILL.md without markers was accepted")
	}
	got, err := replaceBlock([]byte("a\n"+blockBegin+"\nold\n"+blockEnd+"\nb"), []byte("new"))
	if err != nil || string(got) != "a\n"+blockBegin+"\nnew\n"+blockEnd+"\nb" {
		t.Fatalf("%q %v", got, err)
	}
}

// TestAPIReferenceUsesThisRunsHeader: the API reference quotes
// lcd349_board.h as generated in the same run, so a device.json change needs
// one run of the generator, not two (task 0.3 found the second was needed).
func TestAPIReferenceUsesThisRunsHeader(t *testing.T) {
	d, raw, err := Load("../..")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := Render(d, raw)
	if err != nil {
		t.Fatal(err)
	}
	header := headerDir + "/lcd349_board.h"
	gen[header] = append(gen[header], []byte("\n#define LCD349_ONLY_IN_THIS_RUN 1\n")...)
	files, err := RenderSkill("../..", d, raw, gen)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"docs/sdk-api.md", skillDir + "/references/sdk-api.md"} {
		if !strings.Contains(string(files[p]), "LCD349_ONLY_IN_THIS_RUN") {
			t.Errorf("%s was rendered from the header on disk, not this run's", p)
		}
	}
}
