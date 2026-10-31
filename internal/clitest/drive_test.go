package clitest

import (
	"bytes"
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDriveCommands: shot, tree, lint, touch, press, wait and state against
// examples/inputs, one CLI process each, as an agent or script uses them (task 6.3).
func TestDriveCommands(t *testing.T) {
	c := newCLI(t)
	in := c.start("--clock", "2026-10-12T09:00:00Z")
	dir := t.TempDir()

	c.must("wait", in.ID, "--log", "inputs: ready")
	for _, tc := range []struct {
		args []string
		w, h int
	}{{nil, 640, 172}, {[]string{"--raw"}, 172, 640}, {[]string{"--scale", "2"}, 1280, 344}} {
		path := filepath.Join(dir, "s.png")
		var res struct {
			Path      string `json:"path"`
			Width     int    `json:"width"`
			FrameSeq  int    `json:"frame_seq"`
			Backlight int    `json:"backlight"`
			Timings   struct {
				RequestMS float64 `json:"request_ms"`
			} `json:"timings"`
		}
		json.Unmarshal([]byte(c.must(append([]string{"shot", in.ID, "-o", path, "--json"}, tc.args...)...)), &res)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != tc.w || img.Bounds().Dy() != tc.h || !bytes.Contains(b, []byte("pHYs")) {
			t.Fatalf("shot %v: %v %v (pHYs %v)", tc.args, img.Bounds(), err, bytes.Contains(b, []byte("pHYs")))
		}
		if res.FrameSeq == 0 || res.Backlight == 0 || res.Timings.RequestMS <= 0 {
			t.Fatalf("shot result %+v", res)
		}
		t.Logf("shot %v: %.1f ms", tc.args, res.Timings.RequestMS)
	}

	var tree struct {
		Tree  json.RawMessage `json:"tree"`
		Count int             `json:"count"`
	}
	json.Unmarshal([]byte(c.must("tree", in.ID, "--json")), &tree)
	if tree.Count < 4 || !bytes.Contains(tree.Tree, []byte(`"TAP`)) {
		t.Fatalf("tree: %d objects, %s", tree.Count, tree.Tree)
	}
	if out := c.must("tree", in.ID, "--depth", "1"); !strings.Contains(out, "screen 640x172") {
		t.Fatalf("tree text:\n%s", out)
	}
	if out := c.must("lint", in.ID); !strings.Contains(out, "0 errors") {
		t.Fatalf("lint:\n%s", out)
	}

	// Input, then wait for its log line in a separate command.
	_, errOut, code := c.run("touch", in.ID, "tap", "320", "88", "--timings")
	if code != 0 || !strings.Contains(errOut, "timings: request") {
		t.Fatalf("touch: exit %d %s", code, errOut)
	}
	c.must("wait", in.ID, "--log", "tap count 1", "--timeout", "3000")
	c.must("press", in.ID, "boot", "--ms", "150")
	c.must("wait", in.ID, "--log", "gesture SHORT BOOT [0-9]+ ms", "--timeout", "3000")

	c.must("state", in.ID, "set", "battery.mv=3600", "source=battery")
	var st struct {
		Source  string `json:"source"`
		Battery struct {
			MV int `json:"mv"`
		} `json:"battery"`
	}
	json.Unmarshal([]byte(c.must("state", in.ID, "--json")), &st)
	if st.Battery.MV != 3600 || st.Source != "battery" {
		t.Fatalf("state after set: %+v", st)
	}

	// Failures: a wait that times out shows the screen; bad arguments are usage errors.
	_, errOut, code = c.run("wait", in.ID, "--text", "NOT ON SCREEN", "--timeout", "200")
	if code != 1 || !strings.Contains(errOut, "wait-timeout.png") {
		t.Fatalf("wait timeout: exit %d %s", code, errOut)
	}
	if _, _, code := c.run("touch", in.ID, "poke", "1", "2"); code != 2 {
		t.Fatalf("touch poke: exit %d, want 2", code)
	}
	if _, _, code := c.run("press", in.ID, "reset"); code != 2 {
		t.Fatalf("press reset: exit %d, want 2", code)
	}
	// With one instance running, the instance argument may be left out.
	c.must("touch", "tap", "320", "88")
	c.must("wait", "--log", "tap count 2", "--timeout", "3000")
}
