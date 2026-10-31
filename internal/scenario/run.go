package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/golden"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Options for running scenarios.
type Options struct {
	Project       *project.Project // scenario and golden paths must lie inside it (R7.5)
	ELF           string           // the product built for the emulator
	Exe           string           // the lcd349 binary, which runs supervisors
	UpdateGoldens bool
	Keep          bool      // on failure, leave the instance running for inspection
	Progress      io.Writer // one line per step, or nil
}

// StepResult is one step's outcome.
type StepResult struct {
	N       int            `json:"n"`
	Line    int            `json:"line"`
	Step    string         `json:"step"`
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitempty"`
	MS      float64        `json:"ms"`
	Golden  *golden.Result `json:"golden,omitempty"`
	Image   string         `json:"image,omitempty"` // the screen when a wait timed out
}

// Result is a scenario's outcome. It stops at the first failing step.
type Result struct {
	Name     string       `json:"name"`
	File     string       `json:"file"`
	OK       bool         `json:"ok"`
	Steps    []StepResult `json:"steps"`
	Instance string       `json:"instance,omitempty"` // kept running after a failure (Keep)
	Seconds  float64      `json:"seconds"`
}

// Failed returns the failing step, or nil.
func (r *Result) Failed() *StepResult {
	for i := range r.Steps {
		if !r.Steps[i].OK {
			return &r.Steps[i]
		}
	}
	return nil
}

type runner struct {
	s    *Scenario
	o    Options
	ctx  context.Context
	in   *instance.Info
	conn *drive.Conn
	pid  int
	// fresh: nothing has been asked on this connection yet. The instance's
	// wait {log} looks back to the start of the connection's previous
	// request; on a new connection that is the boot, so wait from line 1.
	fresh bool
}

// Run runs one scenario on a fresh headless instance.
func Run(ctx context.Context, s *Scenario, o Options) (*Result, error) {
	start := time.Now()
	res := &Result{Name: s.Name, File: s.File}
	if res.Name == "" {
		res.Name = strings.TrimSuffix(filepath.Base(s.File), ".scenario.yaml")
	}
	if _, err := o.Project.Inside(s.File); err != nil {
		return nil, err
	}
	r := &runner{s: s, o: o, ctx: ctx}
	defer r.stop(res)
	if err := r.start(s.Run); err != nil {
		return nil, err
	}
	for i, st := range s.Steps {
		t0 := time.Now()
		sr := StepResult{N: i + 1, Line: st.Line, Step: st.Describe()}
		err := r.follow()
		if err == nil {
			err = r.step(st, &sr)
		}
		sr.MS = float64(time.Since(t0).Microseconds()) / 1000
		sr.OK = err == nil
		if err != nil {
			sr.Message = err.Error()
		}
		res.Steps = append(res.Steps, sr)
		r.progress(sr)
		if err != nil {
			break
		}
	}
	res.OK = res.Failed() == nil
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

func (r *runner) progress(sr StepResult) {
	w := r.o.Progress
	if w == nil {
		return
	}
	if sr.OK {
		note := ""
		if sr.Golden != nil && sr.Golden.Written {
			note = " (golden written)"
		}
		fmt.Fprintf(w, "  ok   %2d %s (%.0f ms)%s\n", sr.N, sr.Step, sr.MS, note)
		return
	}
	fmt.Fprintf(w, "  FAIL %2d %s (line %d): %s\n", sr.N, sr.Step, sr.Line, sr.Message)
	if g := sr.Golden; g != nil {
		for _, p := range []struct{ k, v string }{{"golden", g.Golden}, {"actual", g.Actual}, {"diff", g.Diff}} {
			if p.v != "" {
				fmt.Fprintf(w, "       %-6s %s\n", p.k+":", p.v)
			}
		}
	}
	if sr.Image != "" {
		fmt.Fprintf(w, "       screen: %s\n", sr.Image)
	}
}

// start starts a fresh instance with the scenario's run options.
func (r *runner) start(ro RunOptions) error {
	r.stopInstance()
	wipe := ro.Wipe == nil || *ro.Wipe
	opts := instance.Options{Clock: ro.Clock, Wipe: wipe, BatteryMV: ro.BatteryMV}
	if ro.USB != nil {
		opts.Source = map[bool]string{true: "usb", false: "battery"}[*ro.USB]
	}
	in, err := instance.Prepare(instance.Config{Project: r.o.Project.Dir, ELF: r.o.ELF, Options: opts})
	if err != nil {
		return err
	}
	sup, err := instance.Spawn(in, r.o.Exe, !r.o.Keep, false, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	if in, err = instance.WaitReady(ctx, in.Dir, sup); err != nil {
		instance.Stop(in.Dir)
		return err
	}
	r.in = in
	return r.connect()
}

func (r *runner) connect() error {
	if r.conn != nil {
		r.conn.Close()
	}
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	defer cancel()
	in, err := instance.Read(r.in.Dir)
	if err != nil {
		return err
	}
	r.in, r.pid, r.fresh = in, in.PID, true
	r.conn, err = drive.Connect(ctx, in)
	return err
}

// settle follows a restart the last step caused (a power step, or PWR
// pressed while off): it waits for the new boot to answer, then reconnects.
func (r *runner) settle() error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		in, err := instance.Read(r.in.Dir)
		if err != nil {
			return err
		}
		if in.PID != r.pid {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the instance didn't restart")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	for {
		in, err := instance.Read(r.in.Dir)
		if err != nil {
			return err
		}
		switch in.State {
		case instance.StateRunning, instance.StateDownload, instance.StateOff, instance.StateCrashed:
			return r.connect()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the instance didn't come back (state %s)", in.State)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// follow notices that the product changed since the last step (it powered
// itself off, reset or crashed) and reconnects, to the new boot or to the
// supervisor's stand-in socket.
func (r *runner) follow() error {
	if r.in == nil {
		return nil
	}
	in, err := instance.Read(r.in.Dir)
	if err != nil || in.PID == r.pid {
		return nil
	}
	return r.settle()
}

func closed(err error) bool {
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "connection reset")
}

// readOnly methods are safe to ask again when the product went away mid-call.
// A wait is too: asked again of the stand-in for a device that powered off, it
// searches the last boot's lines.
var readOnly = map[string]bool{"state.get": true, "screenshot": true, "ui.tree": true, "ui.lint": true, "info": true, "wait": true}

func (r *runner) stopInstance() {
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	if r.in != nil {
		instance.Stop(r.in.Dir)
		r.in = nil
	}
}

func (r *runner) stop(res *Result) {
	if r.o.Keep && res.Failed() != nil && r.in != nil {
		res.Instance = r.in.ID
		if r.conn != nil {
			r.conn.Close()
		}
		return
	}
	r.stopInstance()
}

func (r *runner) call(timeout time.Duration, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	defer cancel()
	r.fresh = false
	err := r.conn.Call(ctx, method, params, result)
	if err != nil && closed(err) {
		// The product went away mid-request: it powered off, reset or crashed.
		// Follow it. A request it never read is sent again: a write into its
		// closed socket, or a reset because it closed with the request unread
		// (Linux, a PWR press just after the product reported itself off). So is
		// a request that changes nothing. An input it read evidently took effect
		// (a PWR hold that powers off ends the process before the press is
		// answered), unless the product crashed.
		unread := !errors.Is(err, io.EOF)
		if serr := r.settle(); serr != nil {
			return fmt.Errorf("the instance closed the connection and didn't come back: %v", serr)
		}
		if in, _ := instance.Read(r.in.Dir); in != nil && in.State == instance.StateCrashed {
			return fmt.Errorf("the product crashed during %s; its last lines are in lcd349 logs %s", method, r.in.ID)
		}
		if readOnly[method] || unread {
			if m, ok := params.(map[string]any); ok && method == "wait" && m["log"] != nil {
				m["since"] = 1 // a new boot's lines from its start, as after any reconnect
			}
			return r.conn.Call(ctx, method, params, result)
		}
		return nil
	}
	return err
}

func (r *runner) step(st Step, sr *StepResult) error {
	switch {
	case st.Run != nil:
		return r.start(*st.Run)
	case st.Press != nil:
		p, err := drive.PressParams(st.Press.Button, st.Press.MS)
		if err != nil {
			return err
		}
		var res map[string]any
		if err := r.call(time.Duration(st.Press.MS)*time.Millisecond+10*time.Second, "input.press", p, &res); err != nil {
			return err
		}
		// Only the stand-in for a device that's off answers powered_on, whether
		// the device was off before the press or went off just as it was sent.
		if res["powered_on"] == true {
			return r.settle()
		}
		return nil
	case st.Touch != nil:
		var res rpc.Touch
		if err := r.call(time.Duration(st.Touch.MS)*time.Millisecond+10*time.Second, "input.touch", st.Touch, &res); err != nil {
			return err
		}
		if !res.Delivered {
			return fmt.Errorf("the touch wasn't delivered: %s", res.Reason)
		}
		return nil
	case st.Wait != nil:
		return r.wait(st, sr)
	case st.Shot != nil:
		return r.shot(st, sr)
	case st.Lint != nil:
		return r.lint(st.Lint)
	case st.State != nil:
		return r.state(st.State)
	case st.Power != "":
		p := map[string]any{"action": st.Power}
		if st.Power == "download" {
			p = map[string]any{"action": "reset", "boot_held": true}
		}
		var res map[string]any
		if err := r.call(10*time.Second, "power", p, &res); err != nil {
			return err
		}
		if res["already_on"] == true {
			return nil
		}
		return r.settle()
	}
	return fmt.Errorf("empty step")
}

func (r *runner) wait(st Step, sr *StepResult) error {
	w := st.Wait
	timeout := w.TimeoutMS
	if timeout == 0 {
		timeout = 5000
	}
	p := map[string]any{"timeout_ms": timeout}
	if w.Log != "" {
		p["log"] = w.Log
	}
	if w.Text != "" {
		p["text"] = w.Text
	}
	if w.IdleMS > 0 {
		p["idle_ms"] = w.IdleMS
	}
	if r.fresh && w.Log != "" {
		p["since"] = 1
	}
	var res rpc.Wait
	if err := r.call(time.Duration(timeout)*time.Millisecond+10*time.Second, "wait", p, &res); err != nil {
		return err
	}
	if res.Matched {
		return nil
	}
	// Keep the instance's picture of the screen: its directory goes when the run ends.
	if res.Screenshot != "" {
		dst := r.artifact(sr.N, "screen.png")
		if b, err := os.ReadFile(res.Screenshot); err == nil && os.WriteFile(dst, b, 0o644) == nil {
			sr.Image = dst
		}
	}
	tail := res.Tail
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return fmt.Errorf("timed out after %d ms; last serial lines:\n         %s", res.ElapsedMS, strings.Join(tail, "\n         "))
}

// artifact is a file beside the scenario for a failing step's evidence.
func (r *runner) artifact(n int, suffix string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(r.s.File, ".yaml"), ".scenario")
	return fmt.Sprintf("%s_step%d_%s", base, n, suffix)
}

func (r *runner) shot(st Step, sr *StepResult) error {
	path := st.Shot.Golden
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(r.s.File), path)
	}
	path, err := r.o.Project.Inside(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	r.fresh = false
	shot, err := r.conn.Screenshot(ctx, drive.ShotOptions{Raw: st.Shot.Raw, Scale: st.Shot.Scale})
	if err != nil {
		return err
	}
	g, err := golden.Compare(shot.PNG, path, golden.Options{TolerancePx: st.Shot.TolerancePx, Update: r.o.UpdateGoldens})
	if err != nil {
		return err
	}
	sr.Golden = g
	if !g.OK {
		return errors.New(g.Message)
	}
	return nil
}

func (r *runner) lint(l *Lint) error {
	var res rpc.Lint
	if err := r.call(30*time.Second, "ui.lint", nil, &res); err != nil {
		return err
	}
	maxErr := 0
	if l.MaxErrors != nil {
		maxErr = *l.MaxErrors
	}
	over := res.Errors > maxErr || (l.MaxWarnings != nil && res.Warnings > *l.MaxWarnings)
	if !over {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d errors and %d warnings (allowed %d", res.Errors, res.Warnings, maxErr)
	if l.MaxWarnings != nil {
		fmt.Fprintf(&b, " and %d", *l.MaxWarnings)
	}
	b.WriteString(")")
	for i, f := range res.Findings {
		if i == 8 {
			fmt.Fprintf(&b, "\n         … and %d more (lcd349 lint)", len(res.Findings)-i)
			break
		}
		fmt.Fprintf(&b, "\n         %s %s %s: %s", f.Severity, f.Rule, f.Path, f.Message)
	}
	return errors.New(b.String())
}

func (r *runner) state(s *State) error {
	if s.Set != nil {
		if err := r.call(10*time.Second, "state.set", s.Set, nil); err != nil {
			return err
		}
	}
	if s.Expect == nil {
		return nil
	}
	// Checked until it holds, for up to expectWithin: the product reaches a
	// state a moment after the log line or input that leads to it.
	want := normalise(s.Expect)
	deadline := time.Now().Add(expectWithin)
	for {
		var got map[string]any
		if err := r.call(10*time.Second, "state.get", map[string]any{}, &got); err != nil {
			return err
		}
		var diffs []string
		expectSubset("", want, got, &diffs)
		if len(diffs) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state differs: %s", strings.Join(diffs, "; "))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// expectWithin is how long a state step's expect may take to hold.
const expectWithin = 2 * time.Second

// normalise round-trips YAML values through JSON, so numbers compare as float64.
func normalise(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func expectSubset(path string, want, got map[string]any, diffs *[]string) {
	for k, w := range want {
		p := strings.TrimPrefix(path+"."+k, ".")
		g, ok := got[k]
		if !ok {
			*diffs = append(*diffs, p+" is missing")
			continue
		}
		wm, wIsMap := w.(map[string]any)
		gm, gIsMap := g.(map[string]any)
		if wIsMap && gIsMap {
			expectSubset(p, wm, gm, diffs)
			continue
		}
		if !reflect.DeepEqual(w, g) {
			wb, _ := json.Marshal(w)
			gb, _ := json.Marshal(g)
			*diffs = append(*diffs, fmt.Sprintf("%s is %s, expected %s", p, gb, wb))
		}
	}
}
