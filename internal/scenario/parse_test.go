package scenario

import (
	"strings"
	"testing"
)

func TestParseDesignExample(t *testing.T) {
	// The example from design §Scenario format.
	s, err := Parse([]byte(`
name: list-to-card
run: { clock: "2026-10-12T09:00:00Z", wipe: true, battery_mv: 3900, usb: false }
steps:
  - wait: { idle_ms: 300 }
  - shot: { golden: goldens/list.png }
  - lint: { max_errors: 0 }
  - touch: { tap: [320, 82] }
  - wait: { text: "DETAIL" }
  - press: { button: boot, ms: 1100 }
  - wait: { log: "approved", timeout_ms: 2000 }
  - state: { set: { wifi: { networks: [] } } }
  - shot: { golden: goldens/no-wifi.png, tolerance_px: 0 }
`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "list-to-card" || s.Run.BatteryMV != 3900 || s.Run.USB == nil || *s.Run.USB || len(s.Steps) != 9 {
		t.Fatalf("%+v", s)
	}
	want := []string{"wait idle 300 ms", "shot goldens/list.png", "lint", "touch tap 320,82", `wait text "DETAIL"`,
		"press boot 1100 ms", `wait log "approved"`, "state", "shot goldens/no-wifi.png"}
	for i, w := range want {
		if got := s.Steps[i].Describe(); got != w {
			t.Errorf("step %d = %q, want %q", i+1, got, w)
		}
	}
	if s.Steps[5].Line != 10 { // the document starts with a blank line
		t.Errorf("press is on line %d, want 10", s.Steps[5].Line)
	}
}

func TestParseShortForms(t *testing.T) {
	s, err := Parse([]byte("steps:\n  - press: pwr\n  - shot: g.png\n  - lint:\n  - power: reset\n  - touch: { swipe: { from: [10, 80], to: [600, 80] }, ms: 400 }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Steps[0].Press.Button != "pwr" || s.Steps[1].Shot.Golden != "g.png" || s.Steps[2].Lint == nil || s.Steps[3].Power != "reset" ||
		s.Steps[4].Touch.Swipe.To[0] != 600 || s.Steps[4].Touch.MS != 400 {
		t.Fatalf("%+v", s.Steps)
	}
}

// TestParseIsStrict: a typo is an error naming its line, never a skipped check.
func TestParseIsStrict(t *testing.T) {
	for doc, want := range map[string]string{
		"steps:\n  - shot: { golden: a.png, tolerance: 3 }\n":      "line 2",
		"steps:\n  - snap: a.png\n":                                `unknown step "snap"`,
		"steps:\n  - press: { button: reset }\n":                   "use boot, pwr or boot+pwr",
		"steps:\n  - touch: { tap: [1, 2], long_press: [1, 2] }\n": "exactly one",
		"steps:\n  - wait: { timeout_ms: 100 }\n":                  "log, text or idle_ms",
		"steps:\n  - power: sleep\n":                               "on, off, reset or download",
		"steps:\n  - state: {}\n":                                  "set or expect",
		"run: { clok: x }\nsteps:\n  - lint:\n":                    "clok",
		"name: x\n":                                                "no steps",
		"steps:\n  - { wait: { idle_ms: 1 }, lint: {} }\n":         "a step is one of",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want it to mention %q", doc, err, want)
		}
	}
}

func TestExpectSubset(t *testing.T) {
	got := map[string]any{"battery": map[string]any{"mv": 3600.0, "pct": 30.0}, "source": "battery"}
	var diffs []string
	expectSubset("", normalise(map[string]any{"battery": map[string]any{"mv": 3600}, "source": "battery"}), got, &diffs)
	if len(diffs) != 0 {
		t.Fatalf("diffs %v", diffs)
	}
	expectSubset("", normalise(map[string]any{"battery": map[string]any{"pct": 50}, "wifi": map[string]any{}}), got, &diffs)
	if len(diffs) != 2 || !strings.Contains(diffs[0]+diffs[1], "battery.pct is 30, expected 50") {
		t.Fatalf("diffs %v", diffs)
	}
}
