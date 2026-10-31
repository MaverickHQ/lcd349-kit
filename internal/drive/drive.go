// Package drive is what the CLI, the scenario runner and the MCP server
// share for driving an instance: connecting through restarts, screenshots,
// the button and touch argument grammar, and frame timing (tasks 6.3, 6.4, 7.1).
package drive

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Conn is a connection to one instance.
type Conn struct {
	Info  *instance.Info
	c     *rpc.Client
	board *rpc.Info // from "info", once
}

// Connect dials an instance's control socket. While the product restarts the
// socket is briefly missing, so it retries for up to 10 s.
func Connect(ctx context.Context, in *instance.Info) (*Conn, error) {
	if !in.Alive() {
		return nil, fmt.Errorf("instance %s isn't running (its supervisor is gone); lcd349 stop %s cleans up", in.ID, in.ID)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := rpc.Dial(ctx, in.Dir)
		if err == nil {
			return &Conn{Info: in, c: c}, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("instance %s doesn't answer: %w", in.ID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Close closes the connection.
func (d *Conn) Close() error { return d.c.Close() }

// Call makes one request; an instance_exited error carries the last serial lines.
func (d *Conn) Call(ctx context.Context, method string, params, result any) error {
	return Readable(d.c.Call(ctx, method, params, result))
}

// Readable turns an instance_exited error into one that shows the last
// serial lines; other errors pass unchanged.
func Readable(err error) error {
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != rpc.CodeExited {
		return err
	}
	var d struct {
		LastLines []string `json:"last_lines"`
	}
	json.Unmarshal(e.Data, &d)
	lines := d.LastLines
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return fmt.Errorf("%s\nlast serial lines:\n  %s", e.Message, strings.Join(lines, "\n  "))
}

// Frames is the number of frames the current product has presented, read
// from the framebuffer file (no request needed).
func (d *Conn) Frames() uint32 {
	fh, err := instance.ReadFrameHeader(d.Info.Dir)
	if err != nil {
		return 0
	}
	return fh.Frames
}

// NextFrame waits up to limit for a frame after the count `after`, and
// returns how long it took.
func (d *Conn) NextFrame(after uint32, limit time.Duration) (time.Duration, bool) {
	start := time.Now()
	for time.Since(start) < limit {
		if d.Frames() != after {
			return time.Since(start), true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, false
}

// ---- The CLI's log cursor ----
//
// The instance's wait {log} looks back to the start of the same connection's
// previous request, so "press, then wait for its log line" can't miss it.
// CLI commands each open a new connection, so the CLI keeps that point in
// the instance directory instead: every input command records where the
// serial log stood when it began, and wait starts there by default.

const cursorFile = "cli-cursor"

// MarkInput records the serial position before an input command acts, with
// the product's pid: a reset starts a new process, numbering lines from 1.
func (d *Conn) MarkInput(ctx context.Context) {
	var st struct {
		SerialNext int64 `json:"serial_next"`
	}
	if d.Call(ctx, "state.get", map[string]any{}, &st) == nil && st.SerialNext > 0 {
		os.WriteFile(filepath.Join(d.Info.Dir, cursorFile), fmt.Appendf(nil, "%d %d\n", d.Info.PID, st.SerialNext), 0o600)
	}
}

// InputCursor is the serial line to match from: where the last input command
// began, or 1 when the product has restarted since (or nothing was recorded).
func (d *Conn) InputCursor() int64 {
	b, _ := os.ReadFile(filepath.Join(d.Info.Dir, cursorFile))
	var pid int
	var next int64
	if _, err := fmt.Sscan(string(b), &pid, &next); err != nil || next < 1 {
		return 1
	}
	if in, err := instance.Read(d.Info.Dir); err != nil || in.PID != pid {
		return 1
	}
	return next
}

// ---- Screenshots ----

// ShotOptions are the screenshot request's options.
type ShotOptions struct {
	Raw   bool // the panel-native portrait image
	Scale int  // 1-4
}

// Shot is a screenshot: PNG bytes and the decoded image.
type Shot struct {
	rpc.Screenshot
	PNG   []byte // with the panel's pixel density (pHYs), so DPI-aware viewers show it at physical size
	Image image.Image
}

// Board returns the instance's "info" (board facts from device.json), asked once.
func (d *Conn) Board(ctx context.Context) (*rpc.Info, error) {
	if d.board == nil {
		var in rpc.Info
		if err := d.Call(ctx, "info", nil, &in); err != nil {
			return nil, err
		}
		d.board = &in
	}
	return d.board, nil
}

// Screenshot takes a PNG screenshot.
func (d *Conn) Screenshot(ctx context.Context, o ShotOptions) (*Shot, error) {
	params := map[string]any{"raw": o.Raw}
	if o.Scale > 1 {
		params["scale"] = o.Scale
	}
	var s Shot
	if err := d.Call(ctx, "screenshot", params, &s.Screenshot); err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(s.Data))
	if err != nil {
		return nil, fmt.Errorf("the instance sent an unreadable PNG: %w", err)
	}
	s.Image = img
	scale := o.Scale
	if scale < 1 {
		scale = 1
	}
	s.PNG = s.Data
	if b, err := d.Board(ctx); err == nil && b.Board.PxPerMM > 0 {
		s.PNG = WithDensity(s.Data, b.Board.PxPerMM*float64(scale))
	}
	return &s, nil
}

// WithDensity adds a pHYs chunk (pixels per metre) after a PNG's IHDR.
func WithDensity(p []byte, pxPerMM float64) []byte {
	const ihdrEnd = 8 + 4 + 4 + 13 + 4 // signature, length, type, data, CRC
	if len(p) < ihdrEnd || string(p[12:16]) != "IHDR" || bytes.Contains(p, []byte("pHYs")) {
		return p
	}
	ppm := uint32(pxPerMM*1000 + 0.5)
	chunk := make([]byte, 4+4+9+4)
	binary.BigEndian.PutUint32(chunk[0:], 9)
	copy(chunk[4:], "pHYs")
	binary.BigEndian.PutUint32(chunk[8:], ppm)
	binary.BigEndian.PutUint32(chunk[12:], ppm)
	chunk[16] = 1 // unit: metre
	binary.BigEndian.PutUint32(chunk[17:], crc32.ChecksumIEEE(chunk[4:17]))
	out := make([]byte, 0, len(p)+len(chunk))
	out = append(out, p[:ihdrEnd]...)
	out = append(out, chunk...)
	return append(out, p[ihdrEnd:]...)
}

// ---- Buttons ----

// PressParams builds input.press parameters from the button grammar: "boot",
// "pwr", or "boot+pwr" (a chord), with a hold of ms (0 = the instance's
// default, 100 ms).
func PressParams(button string, ms int) (map[string]any, error) {
	p := map[string]any{}
	switch strings.ToLower(strings.ReplaceAll(button, " ", "")) {
	case "boot":
		p["button"] = "boot"
	case "pwr", "power":
		p["button"] = "pwr"
	case "boot+pwr", "pwr+boot", "chord":
		p["chord"] = true
	default:
		return nil, fmt.Errorf("button %q: use boot, pwr or boot+pwr", button)
	}
	if ms != 0 {
		if ms < 20 || ms > 60000 {
			return nil, fmt.Errorf("a press lasts 20-60000 ms, not %d", ms)
		}
		p["ms"] = ms
	}
	return p, nil
}

// PressResult is input.press's answer.
type PressResult struct {
	Button string `json:"button"`
	MS     int    `json:"ms"`
	Woke   bool   `json:"woke"`
}

// ---- Touch ----

// Point is a touch position in landscape pixels (640×172).
type Point [2]int

// Touch is one touch gesture; exactly one of Tap, LongPress, Swipe or Points.
type Touch struct {
	Tap       *Point     `json:"tap,omitempty" yaml:"tap"`
	LongPress *Point     `json:"long_press,omitempty" yaml:"long_press"`
	Swipe     *Swipe     `json:"swipe,omitempty" yaml:"swipe"`
	Points    []RawPoint `json:"points,omitempty" yaml:"points"`
	MS        int        `json:"ms,omitempty" yaml:"ms"` // long press or swipe duration
}

// Swipe goes from one point to another.
type Swipe struct {
	From Point `json:"from" yaml:"from"`
	To   Point `json:"to" yaml:"to"`
}

// RawPoint is one touch sample.
type RawPoint struct {
	X    int  `json:"x" yaml:"x"`
	Y    int  `json:"y" yaml:"y"`
	Down bool `json:"down" yaml:"down"`
}

// Validate checks that exactly one gesture is given.
func (t Touch) Validate() error {
	n := 0
	for _, set := range []bool{t.Tap != nil, t.LongPress != nil, t.Swipe != nil, len(t.Points) > 0} {
		if set {
			n++
		}
	}
	if n != 1 {
		return errors.New("give exactly one of tap [x, y], long_press [x, y], swipe {from, to} or points [...]")
	}
	return nil
}

// TouchResult is input.touch's answer.
type TouchResult = rpc.Touch
