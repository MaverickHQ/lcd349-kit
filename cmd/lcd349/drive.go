package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

func init() {
	register(&command{name: "shot", usage: "[instance] [-o file.png|-] [--raw] [--scale 1-4] [--timings] [--json]", summary: "save a screenshot (PNG at the panel's pixel density)", run: cmdShot})
	register(&command{name: "tree", usage: "[instance] [--path path] [--depth n] [--timings] [--json]", summary: "print the UI's object tree", run: cmdTree})
	register(&command{name: "lint", usage: "[instance] [--timings] [--json]", summary: "check the screen against the board's design rules (exit 1 on errors)", run: cmdLint})
	register(&command{name: "press", usage: "[instance] boot|pwr|boot+pwr [--ms hold] [--timings] [--json]", summary: "press a button, or both, for a time", run: cmdPress})
	register(&command{name: "touch", usage: "[instance] tap X Y | long X Y [--ms n] | swipe X1 Y1 X2 Y2 [--ms n] | points JSON [--timings] [--json]", summary: "touch the screen (landscape pixels, 640x172)", run: cmdTouch})
	register(&command{name: "wait", usage: "[instance] [--log regexp] [--text text] [--idle ms] [--timeout ms] [--since line] [--json]", summary: "wait for a log line, text on screen or an idle display (exit 1 on timeout)", run: cmdWait})
	register(&command{name: "state", usage: "[instance] [set key=value...] [--timings] [--json]", summary: "show the device state, or set battery, power source, Wi-Fi networks, board revision or clock", run: cmdState})
}

// session connects to the instance named by the leading argument (or the
// only one running) for one command.
type session struct {
	c     *cmdCtx
	d     *drive.Conn
	ctx   context.Context
	stop  context.CancelFunc
	start time.Time
	rest  []string
}

func (c *cmdCtx) session(want int, timeout time.Duration) (*session, int) {
	in, rest, err := c.pick(want)
	if err != nil {
		return nil, c.fail(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), timeout)
	d, err := drive.Connect(ctx, in)
	if err != nil {
		stop()
		return nil, c.fail(err)
	}
	return &session{c: c, d: d, ctx: ctx, stop: stop, start: time.Now(), rest: rest}, 0
}

func (s *session) close() {
	s.d.Close()
	s.stop()
}

// timing prints how long the request took, and for input, when the next
// frame came (KN1: input to next frame within 200 ms).
func (s *session) timing(on bool, framesBefore uint32, inputDone time.Time, input bool) map[string]any {
	t := map[string]any{"request_ms": ms(inputDone.Sub(s.start))}
	if input {
		switch now := s.d.Frames(); {
		case now != framesBefore:
			t["frame"] = "during the input"
		default:
			if d, ok := s.d.NextFrame(now, time.Second); ok {
				t["next_frame_ms"] = ms(d)
			} else {
				t["frame"] = "none within 1 s"
			}
		}
	}
	if on && !s.c.json {
		parts := []string{fmt.Sprintf("request %v ms", t["request_ms"])}
		if v, ok := t["next_frame_ms"]; ok {
			parts = append(parts, fmt.Sprintf("next frame %v ms after the input", v))
		} else if v, ok := t["frame"]; ok {
			parts = append(parts, fmt.Sprintf("frame %v", v))
		}
		fmt.Fprintf(s.c.stderr, "timings: %s\n", strings.Join(parts, ", "))
	}
	return t
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func cmdShot(c *cmdCtx) int {
	c.jsonFlag()
	out := c.flags.String("o", "", "write the PNG to `file` (- for stdout; default <instance>.png)")
	var o drive.ShotOptions
	c.flags.BoolVar(&o.Raw, "raw", false, "the panel-native portrait framebuffer (172x640)")
	c.flags.IntVar(&o.Scale, "scale", 1, "integer scale `1-4`")
	timings := c.flags.Bool("timings", false, "print how long it took")
	if code, ok := c.parse(); !ok {
		return code
	}
	s, code := c.session(0, 30*time.Second)
	if s == nil {
		return code
	}
	defer s.close()
	shot, err := s.d.Screenshot(s.ctx, o)
	if err != nil {
		return c.fail(err)
	}
	t := s.timing(*timings, 0, time.Now(), false)
	path := *out
	if path == "" {
		path = s.d.Info.ID + ".png"
	}
	if path == "-" {
		os.Stdout.Write(shot.PNG)
		return exitOK
	}
	if err := os.WriteFile(path, shot.PNG, 0o644); err != nil {
		return c.fail(err)
	}
	abs, _ := filepath.Abs(path)
	if c.json {
		c.printJSON(map[string]any{"path": abs, "width": shot.Width, "height": shot.Height, "frame_seq": shot.FrameSeq,
			"backlight": shot.Backlight, "power": shot.Power, "dark": shot.Dark, "timings": t})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "%s: %dx%d, frame %d, backlight %d%%\n", path, shot.Width, shot.Height, shot.FrameSeq, shot.Backlight)
	if shot.Dark {
		fmt.Fprintf(c.stdout, "the display is dark (power %s), so the screenshot is black\n", shot.Power)
	}
	return exitOK
}

func cmdTree(c *cmdCtx) int {
	c.jsonFlag()
	path := c.flags.String("path", "", "start at this object `path` (from an earlier tree)")
	depth := c.flags.Int("depth", 0, "only this many `levels` (0 = all)")
	timings := c.flags.Bool("timings", false, "print how long it took")
	if code, ok := c.parse(); !ok {
		return code
	}
	s, code := c.session(0, 30*time.Second)
	if s == nil {
		return code
	}
	defer s.close()
	params := map[string]any{}
	if *path != "" {
		params["path"] = *path
	}
	if *depth > 0 {
		params["depth"] = *depth
	}
	var raw json.RawMessage
	if err := s.d.Call(s.ctx, "ui.tree", params, &raw); err != nil {
		return c.fail(err)
	}
	s.timing(*timings, 0, time.Now(), false)
	if c.json {
		fmt.Fprintln(c.stdout, string(raw))
		return exitOK
	}
	var t rpc.Tree
	if err := json.Unmarshal(raw, &t); err != nil {
		return c.fail(err)
	}
	printNode(c, &t.Tree, 0)
	if t.Tree.TopLayer != nil && len(t.Tree.TopLayer.Children) > 0 {
		fmt.Fprintln(c.stdout, "top layer:")
		printNode(c, t.Tree.TopLayer, 1)
	}
	if t.Truncated {
		fmt.Fprintf(c.stdout, "(truncated at %d objects: narrow it with --path or --depth)\n", t.Count)
	}
	return exitOK
}

func printNode(c *cmdCtx, n *rpc.Node, level int) {
	name := n.Path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s %dx%d+%d+%d", strings.Repeat("  ", level), name, n.W, n.H, n.X, n.Y)
	if n.Text != "" {
		fmt.Fprintf(&b, " %q", n.Text)
	}
	if !n.Visible {
		b.WriteString(" hidden")
	}
	if n.Interactive {
		b.WriteString(" interactive")
	}
	if n.Font != nil {
		fmt.Fprintf(&b, " font=%s/%d", n.Font.Name, n.Font.LineHeight)
	}
	if len(n.Tags) > 0 {
		fmt.Fprintf(&b, " tags=%s", strings.Join(n.Tags, ","))
	}
	fmt.Fprintln(c.stdout, b.String())
	for i := range n.Children {
		printNode(c, &n.Children[i], level+1)
	}
}

func cmdLint(c *cmdCtx) int {
	c.jsonFlag()
	timings := c.flags.Bool("timings", false, "print how long it took")
	if code, ok := c.parse(); !ok {
		return code
	}
	s, code := c.session(0, 30*time.Second)
	if s == nil {
		return code
	}
	defer s.close()
	var l rpc.Lint
	if err := s.d.Call(s.ctx, "ui.lint", nil, &l); err != nil {
		return c.fail(err)
	}
	s.timing(*timings, 0, time.Now(), false)
	if c.json {
		c.printJSON(l)
	} else {
		for _, f := range l.Findings {
			text := ""
			if f.Text != "" {
				text = fmt.Sprintf(" %q", f.Text)
			}
			fmt.Fprintf(c.stdout, "%-7s %s %s%s: %s\n", f.Severity, f.Rule, f.Path, text, f.Message)
			if f.Fix != "" {
				fmt.Fprintf(c.stdout, "        fix: %s\n", f.Fix)
			}
		}
		fmt.Fprintf(c.stdout, "%d errors, %d warnings\n", l.Errors, l.Warnings)
	}
	if l.Errors > 0 {
		return exitFail
	}
	return exitOK
}

func cmdPress(c *cmdCtx) int {
	c.jsonFlag()
	hold := c.flags.Int("ms", 0, "hold `time` in ms, 20-60000 (default 100)")
	timings := c.flags.Bool("timings", false, "print how long it took and when the next frame came")
	if code, ok := c.parse(); !ok {
		return code
	}
	params, err := pressArgs(c, *hold)
	if err != nil {
		return c.usageErr("%v", err)
	}
	s, code := c.session(1, time.Duration(*hold+30000)*time.Millisecond)
	if s == nil {
		return code
	}
	defer s.close()
	s.d.MarkInput(s.ctx)
	before := s.d.Frames()
	var res map[string]any
	if err := s.d.Call(s.ctx, "input.press", params, &res); err != nil {
		return c.fail(err)
	}
	t := s.timing(*timings, before, time.Now(), true)
	if c.json {
		res["timings"] = t
		c.printJSON(res)
		return exitOK
	}
	switch {
	case res["powered_on"] == true:
		fmt.Fprintf(c.stdout, "pressed PWR: powering on\n")
	default:
		fmt.Fprintf(c.stdout, "pressed %v for %v ms", res["button"], res["ms"])
		if res["woke"] == true {
			fmt.Fprint(c.stdout, " (it woke the device from light sleep)")
		}
		fmt.Fprintln(c.stdout)
	}
	return exitOK
}

func pressArgs(c *cmdCtx, hold int) (map[string]any, error) {
	if len(c.args) == 0 {
		return nil, fmt.Errorf("which button: boot, pwr or boot+pwr")
	}
	return drive.PressParams(c.args[len(c.args)-1], hold)
}

var touchVerbs = map[string]int{"tap": 2, "long": 2, "long_press": 2, "swipe": 4, "points": 1}

func cmdTouch(c *cmdCtx) int {
	c.jsonFlag()
	dur := c.flags.Int("ms", 0, "long press or swipe duration in ms")
	timings := c.flags.Bool("timings", false, "print how long it took and when the next frame came")
	if code, ok := c.parse(); !ok {
		return code
	}
	verb := -1
	for i, a := range c.args {
		if _, ok := touchVerbs[a]; ok {
			verb = i
			break
		}
	}
	if verb < 0 || verb > 1 || len(c.args)-verb-1 != touchVerbs[c.args[verb]] {
		return c.usageErr("give tap X Y, long X Y, swipe X1 Y1 X2 Y2 or points JSON")
	}
	t, err := touchArgs(c.args[verb], c.args[verb+1:], *dur)
	if err != nil {
		return c.usageErr("%v", err)
	}
	c.args = c.args[:verb]
	s, code := c.session(0, 30*time.Second)
	if s == nil {
		return code
	}
	defer s.close()
	s.d.MarkInput(s.ctx)
	before := s.d.Frames()
	var res rpc.Touch
	if err := s.d.Call(s.ctx, "input.touch", t, &res); err != nil {
		return c.fail(err)
	}
	tm := s.timing(*timings, before, time.Now(), true)
	if c.json {
		c.printJSON(map[string]any{"delivered": res.Delivered, "samples": res.Samples, "reason": res.Reason, "timings": tm})
	} else if res.Delivered {
		fmt.Fprintf(c.stdout, "touched: %d samples\n", res.Samples)
	}
	if !res.Delivered {
		return c.fail(fmt.Errorf("the touch wasn't delivered: %s", res.Reason))
	}
	return exitOK
}

func touchArgs(verb string, args []string, dur int) (drive.Touch, error) {
	var t drive.Touch
	if verb == "points" {
		if err := json.Unmarshal([]byte(args[0]), &t.Points); err != nil {
			return t, fmt.Errorf(`points: a JSON array of {"x", "y", "down"}: %v`, err)
		}
		return t, nil
	}
	n := make([]int, len(args))
	for i, a := range args {
		v, err := strconv.Atoi(a)
		if err != nil {
			return t, fmt.Errorf("%q isn't a pixel coordinate", a)
		}
		n[i] = v
	}
	switch verb {
	case "tap":
		t.Tap = &drive.Point{n[0], n[1]}
	case "long", "long_press":
		t.LongPress = &drive.Point{n[0], n[1]}
		t.MS = dur
	case "swipe":
		t.Swipe = &drive.Swipe{From: drive.Point{n[0], n[1]}, To: drive.Point{n[2], n[3]}}
		t.MS = dur
	}
	return t, nil
}

func cmdWait(c *cmdCtx) int {
	c.jsonFlag()
	logRe := c.flags.String("log", "", "a serial line matching this extended `regexp`")
	text := c.flags.String("text", "", "this `text` visible on screen")
	idle := c.flags.Int("idle", 0, "no new frame for this many `ms`")
	timeout := c.flags.Int("timeout", 5000, "give up after this many `ms` (at most 120000)")
	since := c.flags.Int64("since", 0, "match log lines from this serial line `number` (default: from the last press, touch, power or state set)")
	if code, ok := c.parse(); !ok {
		return code
	}
	if *logRe == "" && *text == "" && *idle == 0 {
		return c.usageErr("give --log, --text or --idle")
	}
	params := map[string]any{"timeout_ms": *timeout}
	if *logRe != "" {
		params["log"] = *logRe
	}
	if *text != "" {
		params["text"] = *text
	}
	if *idle > 0 {
		params["idle_ms"] = *idle
	}
	s, code := c.session(0, time.Duration(*timeout+30000)*time.Millisecond)
	if s == nil {
		return code
	}
	defer s.close()
	if *since == 0 && *logRe != "" {
		*since = s.d.InputCursor() // from the last press, touch, power or state set
	}
	if *since > 0 {
		params["since"] = *since
	}
	var w rpc.Wait
	if err := s.d.Call(s.ctx, "wait", params, &w); err != nil {
		return c.fail(err)
	}
	if c.json {
		c.printJSON(w)
	} else if w.Matched {
		switch w.By {
		case "log":
			fmt.Fprintf(c.stdout, "matched line %d: %s\n", w.LineNumber, w.Line)
		case "text":
			fmt.Fprintf(c.stdout, "on screen: %q\n", w.Text)
		default:
			fmt.Fprintf(c.stdout, "matched (%s) after %d ms\n", w.By, w.ElapsedMS)
		}
	} else {
		fmt.Fprintf(c.stderr, "lcd349 wait: timed out after %d ms; last serial lines:\n  %s\n", w.ElapsedMS, strings.Join(w.Tail, "\n  "))
		if w.Screenshot != "" {
			fmt.Fprintf(c.stderr, "the screen at the timeout: %s\n", w.Screenshot)
		}
	}
	if !w.Matched {
		return exitFail
	}
	return exitOK
}

func cmdState(c *cmdCtx) int {
	c.jsonFlag()
	timings := c.flags.Bool("timings", false, "print how long it took")
	if code, ok := c.parse(); !ok {
		return code
	}
	// [instance] [set k=v ...]
	setAt := -1
	for i, a := range c.args {
		if a == "set" {
			setAt = i
			break
		}
	}
	var pairs []string
	if setAt >= 0 {
		pairs = c.args[setAt+1:]
		c.args = c.args[:setAt]
		if len(pairs) == 0 {
			return c.usageErr("set needs key=value pairs, such as battery.mv=3600 source=battery")
		}
	}
	params, err := statePairs(pairs)
	if err != nil {
		return c.usageErr("%v", err)
	}
	s, code := c.session(0, 30*time.Second)
	if s == nil {
		return code
	}
	defer s.close()
	method, p := "state.get", any(map[string]any{})
	if params != nil {
		method, p = "state.set", params
		s.d.MarkInput(s.ctx)
	}
	var raw json.RawMessage
	if err := s.d.Call(s.ctx, method, p, &raw); err != nil {
		return c.fail(err)
	}
	s.timing(*timings, 0, time.Now(), false)
	if c.json {
		fmt.Fprintln(c.stdout, string(raw))
		return exitOK
	}
	var st rpc.State
	json.Unmarshal(raw, &st)
	printState(c, s.d.Info, &st)
	return exitOK
}

// statePairs turns key=value pairs into a state.set object: dotted keys
// nest, and a value that parses as JSON is used as JSON (numbers, arrays).
func statePairs(pairs []string) (map[string]any, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%q isn't key=value", p)
		}
		var val any = v
		var j any
		if json.Unmarshal([]byte(v), &j) == nil {
			val = j
		}
		m := out
		keys := strings.Split(k, ".")
		for _, key := range keys[:len(keys)-1] {
			next, ok := m[key].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[key] = next
			}
			m = next
		}
		m[keys[len(keys)-1]] = val
	}
	return out, nil
}

func printState(c *cmdCtx, in *instance.Info, st *rpc.State) {
	w := c.stdout
	fmt.Fprintf(w, "power:    %s (hold %v), on %s, board %s\n", st.Power, st.PowerHold, st.Source, st.BoardRev)
	fmt.Fprintf(w, "battery:  %d mV, %d%%\n", st.Battery.MV, st.Battery.Pct)
	dark := ""
	if st.Display.Dark {
		dark = ", dark"
	}
	fmt.Fprintf(w, "display:  backlight %d%%%s, %d frames, idle %d ms\n", st.Display.Backlight, dark, st.Display.Frames, st.Display.IdleMS)
	fmt.Fprintf(w, "wifi:     %s %s %s %s\n", st.WiFi.State, st.WiFi.SSID, st.WiFi.IP, st.WiFi.Reason)
	for _, n := range st.WiFi.Networks {
		fmt.Fprintf(w, "          network %q, %.1f GHz, %d dBm\n", n.SSID, n.Band, n.RSSI)
	}
	fmt.Fprintf(w, "clock:    %s\n", st.Clock.ISO)
	fmt.Fprintf(w, "buttons:  boot %v, pwr %v\n", st.Buttons.Boot, st.Buttons.Pwr)
	for _, m := range st.Memory.Warnings {
		fmt.Fprintf(w, "memory:   %s\n", m)
	}
	fmt.Fprintf(w, "serial:   next line %d (for wait --since); port %s\n", st.SerialNext, in.Serial)
}
