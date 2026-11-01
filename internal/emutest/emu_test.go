package emutest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Offsets in the fb.bin header (src/emu/emu.h).
const (
	hdrPower = 37
	powerOff = 2
)

func TestInfoAndState(t *testing.T) {
	in := start(t, "", "LCD349_CLOCK=2026-10-12T09:00:00Z")
	in.ready()
	var info rpc.Info
	in.call("info", nil, &info)
	if info.Board.Width != 640 || info.Board.Height != 172 || info.DownloadMode || info.BootReason != "power_on" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.NotModelled) == 0 || len(info.Methods) < 14 || info.ExitCodes["reset"] != exitReset {
		t.Fatalf("info lacks the fidelity list, methods or exit codes: %+v", info)
	}
	var st rpc.State
	in.call("state.get", nil, &st)
	if st.Power != "on" || !st.PowerHold || st.Source != "usb" || st.Display.Backlight != 80 || st.Display.Dark {
		t.Fatalf("state = %+v", st)
	}
	if !strings.HasPrefix(st.Clock.ISO, "2026-10-12T09:0") {
		t.Fatalf("clock = %s; want the fixed LCD349_CLOCK", st.Clock.ISO)
	}
	if len(st.Memory.Warnings) != 0 {
		t.Fatalf("memory warnings: %v", st.Memory.Warnings)
	}

	in.call("state.set", map[string]any{"battery": map[string]any{"mv": 3700}, "source": "battery", "board_rev": "V2",
		"clock": "2027-01-01T00:00:00Z"}, &st)
	if st.Battery.MV != 3700 || st.Battery.Pct != 13 || st.Source != "battery" || st.BoardRev != "V2" ||
		!strings.HasPrefix(st.Clock.ISO, "2027-01-01T00:00") {
		t.Fatalf("after state.set: %+v", st)
	}
	// The header the viewer reads follows the device model.
	h := fbHeader(t, in.dir)
	if mv := binary.LittleEndian.Uint16(h[40:42]); mv != 3700 || h[39] != 1 {
		t.Fatalf("header battery %d mV, on_battery %d", mv, h[39])
	}
	// Invalid values are refused as a whole.
	err := in.try("state.set", map[string]any{"battery": map[string]any{"mv": 9000}, "source": "usb"}, nil)
	if !rpc.IsCode(err, rpc.CodeInvalidParams) {
		t.Fatalf("battery 9000 mV: %v", err)
	}
	in.call("state.get", nil, &st)
	if st.Source != "battery" {
		t.Fatal("a refused state.set changed the source")
	}
}

func TestScreenshot(t *testing.T) {
	in := start(t, "")
	in.ready()
	var idle rpc.Wait
	in.call("wait", map[string]any{"idle_ms": 200, "timeout_ms": 5000}, &idle)

	var s rpc.Screenshot
	in.call("screenshot", nil, &s)
	img, err := png.Decode(bytes.NewReader(s.Data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 640 || b.Dy() != 172 || s.Dark {
		t.Fatalf("screenshot %v dark=%v", b, s.Dark)
	}
	// Background #0B1622 and the button #1565C0, as RGB565 shows them.
	r, g, b, _ := img.At(5, 100).RGBA()
	if r>>8 != 8 || g>>8 != 20 || b>>8 != 33 {
		t.Fatalf("background pixel = %d,%d,%d; want 8,20,33", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = img.At(320, 60).RGBA()
	if r>>8 != 16 || g>>8 != 101 || b>>8 != 197 {
		t.Fatalf("button pixel = %d,%d,%d; want 16,101,197", r>>8, g>>8, b>>8)
	}

	in.call("screenshot", map[string]any{"raw": true}, &s)
	img, err = png.Decode(bytes.NewReader(s.Data))
	if err != nil || img.Bounds().Dx() != 172 || img.Bounds().Dy() != 640 {
		t.Fatalf("raw screenshot: %v %v", err, img.Bounds())
	}
	in.call("screenshot", map[string]any{"scale": 2}, &s)
	if s.Width != 1280 || s.Height != 344 {
		t.Fatalf("scale 2: %dx%d", s.Width, s.Height)
	}
	in.call("screenshot", map[string]any{"format": "rgb565be"}, &s)
	if len(s.Data) != 172*640*2 {
		t.Fatalf("rgb565be: %d bytes", len(s.Data))
	}
	// Big-endian RGB565 of the background at landscape (5, 100): panel row 5, column 171-100.
	off := 2 * (5*172 + 71)
	if v := uint16(s.Data[off])<<8 | uint16(s.Data[off+1]); v != 0x08A4 {
		t.Fatalf("panel pixel = %#04x; want 0x08A4", v)
	}
	if err := in.try("screenshot", map[string]any{"scale": 9}, nil); !rpc.IsCode(err, rpc.CodeInvalidParams) {
		t.Fatalf("scale 9: %v", err)
	}
}

func TestTreeAndLint(t *testing.T) {
	in := start(t, "")
	in.ready()
	var tr rpc.Tree
	in.call("ui.tree", nil, &tr)
	var tap *rpc.Node
	tr.Tree.Walk(func(n *rpc.Node) {
		if n.Class == "button" {
			tap = n
		}
	})
	if tap == nil || !tap.Interactive || tap.W != 160 || tap.H != 72 || len(tap.Children) != 1 {
		t.Fatalf("button node = %+v", tap)
	}
	lbl := tap.Children[0]
	if lbl.Class != "label" || lbl.Text != "TAP 0" || lbl.Font == nil || lbl.Font.Name != "montserrat_20" || lbl.TextColor != "#FFFFFF" {
		t.Fatalf("label node = %+v font %+v", lbl, lbl.Font)
	}
	if !strings.HasPrefix(lbl.Path, "screen/") || !strings.HasSuffix(lbl.Path, ":button/0:label") {
		t.Fatalf("path = %s", lbl.Path)
	}
	// A subtree by path, and depth limits.
	var sub rpc.Tree
	in.call("ui.tree", map[string]any{"path": tap.Path}, &sub)
	if sub.Tree.Path != tap.Path || sub.Count != 2 {
		t.Fatalf("subtree = %+v", sub)
	}
	for _, p := range []string{"screen/99", "screen/abc", "screen/0x"} {
		if err := in.try("ui.tree", map[string]any{"path": p}, nil); !rpc.IsCode(err, rpc.CodeInvalidParams) {
			t.Fatalf("bad path %s: %v", p, err)
		}
	}
	// "/" is the screen (it crashed the instance).
	var root rpc.Tree
	in.call("ui.tree", map[string]any{"path": "/", "depth": 1}, &root)
	if root.Tree.Path != "screen" {
		t.Fatalf("path / = %+v", root.Tree)
	}
	var l rpc.Lint
	in.call("ui.lint", nil, &l)
	if l.Errors != 0 || l.Warnings != 0 {
		t.Fatalf("the example should lint clean: %+v", l.Findings)
	}
}

func TestTouchPressWaitAudio(t *testing.T) {
	in := start(t, "")
	in.ready()
	var tc rpc.Touch
	in.call("input.touch", map[string]any{"tap": []int{320, 88}}, &tc)
	if !tc.Delivered || tc.Samples != 4 {
		t.Fatalf("tap: %+v", tc)
	}
	// wait looks back to the previous request, so a line logged during the tap counts.
	var w rpc.Wait
	in.call("wait", map[string]any{"log": "tap count 1", "timeout_ms": 3000}, &w)
	if !w.Matched || w.By != "log" {
		t.Fatalf("wait log: %+v", w)
	}
	in.call("wait", map[string]any{"text": "TAP 1", "timeout_ms": 3000}, &w)
	if !w.Matched || w.By != "text" || w.Text != "TAP 1" {
		t.Fatalf("wait text: %+v", w)
	}
	in.waitLog(`touch down 320 88`, 1)

	var ev struct {
		Events []struct {
			Hz     int  `json:"hz"`
			MS     int  `json:"ms"`
			Volume int  `json:"volume"`
			Stop   bool `json:"stop"`
		} `json:"events"`
		Next int `json:"next"`
	}
	in.call("audio.events", nil, &ev)
	found := false
	for _, e := range ev.Events {
		found = found || (e.Hz == 880 && e.MS == 40 && e.Volume == 60)
	}
	if !found {
		t.Fatalf("the tap's tone is missing: %+v", ev.Events)
	}

	// Buttons: a short press, a 1 s hold and a chord, through the debounce and classifier.
	in.call("input.press", map[string]any{"button": "pwr", "ms": 150}, nil)
	in.call("wait", map[string]any{"log": `gesture SHORT PWR 1[45][0-9] ms`, "timeout_ms": 3000}, &w)
	if !w.Matched {
		t.Fatalf("short press: %+v", w)
	}
	in.call("input.press", map[string]any{"chord": true, "ms": 200}, nil)
	in.call("wait", map[string]any{"log": `gesture CHORD_END`, "timeout_ms": 3000}, &w)
	if !w.Matched {
		t.Fatalf("chord: %+v", w)
	}
	if strings.Contains(in.serial(), "gesture SHORT BOOT") {
		t.Fatal("a chord also produced a short press")
	}

	// Errors keep the instance alive.
	if err := in.try("input.touch", map[string]any{"tap": []int{700, 10}}, nil); !rpc.IsCode(err, rpc.CodeInvalidParams) {
		t.Fatalf("tap outside the screen: %v", err)
	}
	if err := in.try("input.press", map[string]any{"button": "reset"}, nil); !rpc.IsCode(err, rpc.CodeInvalidParams) {
		t.Fatalf("press reset: %v", err)
	}

	// idle, and a timeout with its tail and screenshot.
	in.call("wait", map[string]any{"idle_ms": 300, "timeout_ms": 5000}, &w)
	if !w.Matched || w.By != "idle" {
		t.Fatalf("idle: %+v", w)
	}
	in.call("wait", map[string]any{"log": "never printed", "timeout_ms": 300}, &w)
	if w.Matched || len(w.Tail) == 0 || w.Screenshot == "" {
		t.Fatalf("timeout: %+v", w)
	}
	if _, err := os.Stat(w.Screenshot); err != nil {
		t.Fatal(err)
	}
	if err := in.try("wait", map[string]any{"log": "([", "timeout_ms": 100}, nil); !rpc.IsCode(err, rpc.CodeInvalidParams) {
		t.Fatalf("bad regex: %v", err)
	}
}

func TestSleepAndTouchNeverWakes(t *testing.T) {
	in := start(t, "")
	in.ready()
	in.call("input.press", map[string]any{"button": "boot", "ms": 1100}, nil)
	in.waitLog(`sleeping until a button`, 1)
	var st rpc.State
	for i := 0; i < 50; i++ { // the log line comes just before the sleep starts
		in.call("state.get", nil, &st)
		if st.Power == "light_sleep" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Power != "light_sleep" || !st.Display.Dark {
		t.Fatalf("asleep: power %s dark %v", st.Power, st.Display.Dark)
	}
	var tc rpc.Touch
	in.call("input.touch", map[string]any{"tap": []int{320, 88}}, &tc)
	if tc.Delivered || !strings.Contains(tc.Reason, "never a wake source") {
		t.Fatalf("touch while asleep: %+v", tc)
	}
	var s rpc.Screenshot
	in.call("screenshot", nil, &s)
	if !s.Dark {
		t.Fatal("screenshot while asleep isn't dark")
	}
	var pr struct {
		Woke bool `json:"woke"`
	}
	in.call("input.press", map[string]any{"button": "pwr", "ms": 100}, &pr)
	if !pr.Woke {
		t.Fatal("press didn't report waking the device")
	}
	in.waitLog(`woke by PWR`, 1)
	in.call("wait", map[string]any{"idle_ms": 200, "timeout_ms": 3000}, nil)
	if countMatches(in.serial(), `gesture SHORT PWR`) != 0 {
		t.Fatal("the waking press was also handled as a press")
	}
	if strings.Contains(in.serial(), "tap count") {
		t.Fatal("a touch while asleep reached the product")
	}
}

func TestWiFi(t *testing.T) {
	in := start(t, "")
	in.ready()
	// Only a 5 GHz network with the right name: invisible to the radio.
	in.call("state.set", map[string]any{"wifi": map[string]any{"networks": []rpc.Net{
		{SSID: "lcd349-test", Password: "password123", Band: 5, RSSI: -40},
	}}}, nil)
	in.waitLog(`wifi disconnected not_found`, 1)
	in.call("state.set", map[string]any{"wifi": map[string]any{"networks": []rpc.Net{
		{SSID: "lcd349-test", Password: "password123", Band: 2.4, RSSI: -52},
		{SSID: "other", Band: 2.4, RSSI: -70},
	}}}, nil)
	in.waitLog(`wifi connected +lcd349-test`, 1)
	var st rpc.State
	in.call("state.get", nil, &st)
	if st.WiFi.State != "connected" || st.WiFi.SSID != "lcd349-test" || st.WiFi.IP == "" || len(st.WiFi.Networks) != 2 {
		t.Fatalf("wifi state: %+v", st.WiFi)
	}
	in.call("state.set", map[string]any{"wifi": map[string]any{"networks": []rpc.Net{}}}, nil)
	in.waitLog(`wifi disconnected lost lcd349-test`, 1)
}

func TestPowerOffOnBatteryAndUSB(t *testing.T) {
	in := start(t, "")
	in.ready()
	// On USB the board keeps running: power_off reports it.
	in.call("input.press", map[string]any{"button": "pwr", "ms": 3200}, nil)
	in.waitLog(`power_off: LCD349_ERR_ON_USB`, 1)

	// On battery it powers off: the instance exits and presents OFF.
	in.call("state.set", map[string]any{"source": "battery"}, nil)
	in.try("input.press", map[string]any{"button": "pwr", "ms": 3200}, nil) // the instance may exit before answering
	if code := in.waitExit(10 * time.Second); code != exitOff {
		t.Fatalf("exit code %d; want %d\n%s", code, exitOff, tail(in.serial(), 10))
	}
	if h := fbHeader(t, in.dir); h[hdrPower] != powerOff {
		t.Fatalf("header power = %d; want OFF", h[hdrPower])
	}
}

func TestResetStorageAndDownloadMode(t *testing.T) {
	dir := newDir(t)
	in := start(t, dir)
	in.ready()
	in.waitLog(`boot count 1,`, 1)
	in.call("state.set", map[string]any{"battery": map[string]any{"mv": 3650}}, nil)
	in.call("power", map[string]any{"action": "reset"}, nil)
	if code := in.waitExit(5 * time.Second); code != exitReset {
		t.Fatalf("reset exit code %d", code)
	}

	// The supervisor would start it again: storage and the device model survive.
	in = start(t, dir, "LCD349_BOOT_REASON=software")
	in.ready()
	in.waitLog(`boot count 2, boot reason 1`, 1)
	var st rpc.State
	in.call("state.get", nil, &st)
	if st.Battery.MV != 3650 {
		t.Fatalf("battery after reset = %d", st.Battery.MV)
	}
	var info rpc.Info
	in.call("info", nil, &info)
	if info.BootReason != "software" {
		t.Fatalf("boot reason %s", info.BootReason)
	}

	// Reset with BOOT held: ROM download mode, the product never starts.
	in.call("power", map[string]any{"action": "reset", "boot_held": true}, nil)
	if code := in.waitExit(5 * time.Second); code != exitResetBootHeld {
		t.Fatalf("exit code %d", code)
	}
	dl := start(t, dir, "LCD349_BOOT_HELD=1")
	dl.call("info", nil, &info)
	if !info.DownloadMode {
		t.Fatal("not in download mode")
	}
	if err := dl.try("ui.tree", nil, nil); !rpc.IsCode(err, rpc.CodeUnavailable) {
		t.Fatalf("ui.tree in download mode: %v", err)
	}
	var s rpc.Screenshot
	dl.call("screenshot", nil, &s)
	if s.Power != "download" || s.Dark {
		t.Fatalf("download screenshot: power %s dark %v", s.Power, s.Dark)
	}
	if strings.Contains(dl.serial()[strings.LastIndex(dl.serial(), "waiting for download"):], "inputs:") {
		t.Fatal("the product ran in download mode")
	}
	dl.call("power", map[string]any{"action": "reset"}, nil)
	if code := dl.waitExit(5 * time.Second); code != exitReset {
		t.Fatalf("exit code %d", code)
	}

	// --wipe clears storage.
	in = start(t, dir, "LCD349_WIPE=1")
	in.ready()
	in.waitLog(`boot count 1,`, 1) // serial line numbers start again with each process
}

func TestProtocolErrors(t *testing.T) {
	in := start(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, e, err := in.client.Raw(ctx, []byte("{not json\n"))
	if err != nil || e == nil || e.Code != rpc.CodeParse {
		t.Fatalf("malformed: %v %+v", err, e)
	}
	_, e, err = in.client.Raw(ctx, []byte(`{"jsonrpc":"2.0","id":7,"method":"nope"}`+"\n"))
	if err != nil || e == nil || e.Code != rpc.CodeMethodNotFound {
		t.Fatalf("unknown method: %v %+v", err, e)
	}
	_, e, err = in.client.Raw(ctx, []byte(`{"jsonrpc":"2.0","id":8,"method":"ping","params":[1]}`+"\n"))
	if err != nil || e == nil || e.Code != rpc.CodeInvalidParams {
		t.Fatalf("array params: %v %+v", err, e)
	}
	// A line over 64 KB is refused; the connection and the instance carry on.
	big := append([]byte(`{"jsonrpc":"2.0","id":9,"method":"ping","params":{"pad":"`), bytes.Repeat([]byte("x"), 70*1024)...)
	big = append(big, []byte("\"}}\n")...)
	_, e, err = in.client.Raw(ctx, big)
	if err != nil || e == nil || e.Code != rpc.CodeInvalidRequest {
		t.Fatalf("oversize: %v %+v", err, e)
	}
	// A request nesting deeper than rpc.MaxDepth is refused before it's
	// parsed (recursively, on the control task's stack); one at the limit
	// is parsed and answered. Brackets in strings don't count.
	nested := func(id, n int) []byte {
		pad := strings.Repeat("[", n-2) + strings.Repeat("]", n-2)
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping","params":{"pad":%s,"s":"[[[[[[[[[[[[[[[[[[[["}}`+"\n", id, pad))
	}
	_, e, err = in.client.Raw(ctx, nested(11, rpc.MaxDepth+1))
	if err != nil || e == nil || e.Code != rpc.CodeInvalidRequest || !strings.Contains(e.Message, "nests") {
		t.Fatalf("too deep: %v %+v", err, e)
	}
	res, e, err = in.client.Raw(ctx, nested(12, rpc.MaxDepth))
	if err != nil || e != nil || !bytes.Contains(res, []byte("pong")) {
		t.Fatalf("at the limit: %v %+v %s", err, e, res)
	}
	// A notification (no id) gets no answer: the next answer is the ping's.
	res, e, err = in.client.Raw(ctx, []byte(`{"jsonrpc":"2.0","method":"ping"}`+"\n"+`{"jsonrpc":"2.0","id":10,"method":"ping"}`+"\n"))
	if err != nil || e != nil || !bytes.Contains(res, []byte("pong")) {
		t.Fatalf("after a notification: %v %+v %s", err, e, res)
	}
	in.call("ping", nil, nil)

	// A client that sends its requests and closes its side (as
	// `printf ... | nc -U control.sock` does) still gets every answer.
	conn, err := net.Dial("unix", rpc.SocketPath(in.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"state.get"}` + "\n"))
	conn.(*net.UnixConn).CloseWrite()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	all, err := io.ReadAll(conn)
	if err != nil || bytes.Count(all, []byte("\n")) != 2 || !bytes.Contains(all, []byte("pong")) || !bytes.Contains(all, []byte(`"id":2`)) {
		t.Fatalf("half-closed client: %v, got %q", err, all)
	}
	in.call("ping", nil, nil)
}

func TestConcurrentClientsAndLimits(t *testing.T) {
	in := start(t, "")
	var wg sync.WaitGroup
	errs := make(chan error, 7)
	for i := 0; i < 7; i++ { // 7 more clients: 8 in all
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c, err := rpc.Dial(ctx, in.dir)
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			for j := 0; j < 30; j++ {
				var st rpc.State
				if err := c.Call(ctx, "state.get", nil, &st); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestSocketPermissionsAndNoNetworkListener(t *testing.T) {
	in := start(t, "")
	fi, err := os.Stat(rpc.SocketPath(in.dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("control.sock mode %v; want a 0600 socket", fi.Mode())
	}
	di, _ := os.Stat(in.dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("instance directory mode %v; want 0700", di.Mode().Perm())
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Skip("lsof not available")
	}
	// No TCP or UDP sockets at all, listening or otherwise (requirement 13.1).
	out, _ := exec.Command("lsof", "-nP", "-a", "-p", fmt.Sprint(in.cmd.Process.Pid), "-i").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("the instance has network sockets:\n%s", out)
	}
}

func TestRecord(t *testing.T) {
	in := start(t, "")
	in.ready()
	var r struct {
		Dir    string `json:"dir"`
		Frames int    `json:"frames"`
	}
	in.call("record.start", map[string]any{"max_s": 5}, &r)
	in.call("input.touch", map[string]any{"tap": []int{320, 88}}, nil)
	in.call("wait", map[string]any{"text": "TAP 1", "timeout_ms": 3000}, nil)
	in.call("record.stop", nil, &r)
	files, _ := filepath.Glob(filepath.Join(r.Dir, "frame-*.png"))
	if r.Frames < 1 || len(files) != r.Frames {
		t.Fatalf("recorded %d frames, %d files in %s", r.Frames, len(files), r.Dir)
	}
}

// TestPressHold covers input.press {state}: a hold for as long as a person
// holds a key in the viewer. A client that disconnects lets go of its buttons.
func TestPressHold(t *testing.T) {
	in := start(t, "")
	in.ready()
	var r struct {
		Button string `json:"button"`
		State  string `json:"state"`
		MS     int    `json:"ms"`
	}
	in.call("input.press", map[string]any{"button": "pwr", "state": "down"}, &r)
	if r.Button != "pwr" || r.State != "down" {
		t.Fatalf("down: %+v", r)
	}
	in.waitLog(`gesture HOLD PWR level 1`, 1)
	in.call("input.press", map[string]any{"button": "pwr", "state": "up"}, &r)
	if r.State != "up" || r.MS < 1000 || r.MS > 3000 {
		t.Fatalf("up: %+v", r)
	}
	in.waitLog(`gesture HOLD_END PWR level 1 1[0-9]{3} ms`, 1)

	// A short hold is a short press; what isn't allowed is refused.
	in.call("input.press", map[string]any{"button": "boot", "state": "down"}, nil)
	for _, c := range []struct {
		params map[string]any
		code   int
	}{
		{map[string]any{"button": "boot", "state": "down"}, rpc.CodeUnavailable}, // already held
		{map[string]any{"button": "boot", "ms": 100}, rpc.CodeUnavailable},       // held by a hold
		{map[string]any{"button": "pwr", "state": "up"}, rpc.CodeUnavailable},    // not held
		{map[string]any{"button": "pwr", "state": "down", "ms": 100}, rpc.CodeInvalidParams},
		{map[string]any{"button": "pwr", "state": "sideways"}, rpc.CodeInvalidParams},
	} {
		if err := in.try("input.press", c.params, nil); !rpc.IsCode(err, c.code) {
			t.Fatalf("%v: got %v, want code %d", c.params, err, c.code)
		}
	}
	in.call("input.press", map[string]any{"button": "boot", "state": "up"}, nil)
	in.waitLog(`gesture SHORT BOOT`, 1)

	// Only the holder can release; when it disconnects, the button is released.
	// The wait looks from this press on: the hold above logged PWR down too,
	// and matching that one let the client leave before this press was past
	// its debounce, so it never logged PWR up (CI run 41338616154).
	var before rpc.State
	in.call("state.get", nil, &before)
	in.call("input.press", map[string]any{"button": "pwr", "state": "down"}, nil)
	in.waitLog(`button PWR down`, before.SerialNext) // past the debounce: a shorter press is a bounce
	var st rpc.State
	in.call("state.get", nil, &st)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other, err := rpc.Dial(ctx, in.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Call(ctx, "input.press", map[string]any{"button": "pwr", "state": "up"}, nil); !rpc.IsCode(err, rpc.CodeUnavailable) {
		t.Fatalf("another client released PWR: %v", err)
	}
	in.client.Close()
	var w rpc.Wait
	if err := other.Call(ctx, "wait", map[string]any{"log": "button PWR up", "since": st.SerialNext, "timeout_ms": 3000}, &w); err != nil || !w.Matched {
		t.Fatalf("PWR still held after its client left: %v %+v", err, w)
	}
}
