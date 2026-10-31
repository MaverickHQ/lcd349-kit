// Command measure times the agent loop and start-up against examples/inputs
// (task 10.2): KN1 (screenshot, input to next frame, incremental build) and
// KN2 (instance start to first frame). It prints JSON for
// docs/measurements.md.
//
//	go run ./tools/measure [-runs 20] [-starts 5] [-builds 20]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

type stats struct {
	N      int     `json:"n"`
	Median float64 `json:"median_ms"`
	P95    float64 `json:"p95_ms"`
	Max    float64 `json:"max_ms"`
	Min    float64 `json:"min_ms"`
	Budget float64 `json:"budget_ms,omitempty"`
	Pass   *bool   `json:"pass,omitempty"`
}

func summarise(ds []time.Duration, budget time.Duration) stats {
	ms := make([]float64, len(ds))
	for i, d := range ds {
		ms[i] = float64(d.Microseconds()) / 1000
	}
	sort.Float64s(ms)
	pick := func(q float64) float64 { return ms[int(q*float64(len(ms)-1)+0.5)] }
	s := stats{N: len(ms), Median: pick(0.5), P95: pick(0.95), Max: ms[len(ms)-1], Min: ms[0]}
	if budget > 0 {
		s.Budget = float64(budget.Milliseconds())
		pass := s.Max <= s.Budget
		s.Pass = &pass
	}
	return s
}

func main() {
	runs := flag.Int("runs", 20, "screenshot and input runs (KN1)")
	starts := flag.Int("starts", 5, "start-up runs (KN2)")
	builds := flag.Int("builds", 20, "incremental builds (KN1); 0 skips")
	flag.Parse()
	root, _ := filepath.Abs(".")
	project := filepath.Join(root, "examples", "inputs")
	tmp, err := os.MkdirTemp("", "lcd349-measure-")
	check(err)
	defer os.RemoveAll(tmp)
	inst, err := os.MkdirTemp("/tmp", "l349k-")
	check(err)
	defer os.RemoveAll(inst)
	os.Setenv("LCD349_INSTANCES", inst)
	exe := filepath.Join(tmp, "lcd349")
	out, err := exec.Command("go", "build", "-o", exe, "./cmd/lcd349").CombinedOutput()
	if err != nil {
		fail("building lcd349: %v\n%s", err, out)
	}
	ctx := context.Background()
	idf, err := build.Environment(ctx, false)
	check(err)
	res, err := build.Build(ctx, idf, project, nil)
	check(err)
	if !res.OK {
		fail("examples/inputs doesn't build: %v", res.Diagnostics)
	}
	// Input latency is measured on hello, which draws only in answer to
	// input: inputs also refreshes its status line every second, and that
	// frame could pass for the input's.
	hello, err := build.Build(ctx, idf, filepath.Join(root, "examples", "hello"), nil)
	check(err)
	if !hello.OK {
		fail("examples/hello doesn't build: %v", hello.Diagnostics)
	}
	report := map[string]any{"kit_version": lcd349kit.Version, "date": time.Now().Format("2006-01-02")}

	// KN2: instance start to first frame.
	var startup, ready []time.Duration
	for i := 0; i < *starts; i++ {
		t0 := time.Now()
		in, sup := start(ctx, exe, res.ELF)
		ready = append(ready, time.Since(t0))
		for {
			fh, err := instance.ReadFrameHeader(in.Dir)
			if err == nil && int(fh.PID) == in.PID && fh.Frames > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		startup = append(startup, time.Since(t0))
		instance.Stop(in.Dir)
		sup.Wait()
	}
	report["kn2_start_to_first_frame"] = summarise(startup, 3*time.Second)
	report["kn2_start_to_ready"] = summarise(ready, 0)

	// KN1 screenshots, on inputs.
	in, sup := start(ctx, exe, res.ELF)
	defer func() { instance.Stop(in.Dir); sup.Wait() }()
	waitFor(ctx, in, "inputs: ready")
	waitFor(ctx, in, "wifi disconnected")

	var apiShot, cliShot []time.Duration
	for i := 0; i < *runs; i++ {
		t0 := time.Now()
		d, err := drive.Connect(ctx, in)
		check(err)
		_, err = d.Screenshot(ctx, drive.ShotOptions{})
		check(err)
		apiShot = append(apiShot, time.Since(t0))
		d.Close()
		t0 = time.Now()
		cmd := exec.Command(exe, "shot", in.ID, "-o", filepath.Join(tmp, "s.png"))
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			fail("lcd349 shot: %v %s", err, out)
		}
		cliShot = append(cliShot, time.Since(t0))
	}
	report["kn1_screenshot_tool_call"] = summarise(apiShot, 300*time.Millisecond)
	report["kn1_screenshot_cli"] = summarise(cliShot, 300*time.Millisecond)

	// Input to next frame, on hello: from sending the input to the first
	// frame, which shows the touch-down or button-down.
	hin, hsup := start(ctx, exe, hello.ELF)
	defer func() { instance.Stop(hin.Dir); hsup.Wait() }()
	waitFor(ctx, hin, "hello: ready")
	d, err := drive.Connect(ctx, hin)
	check(err)
	defer d.Close()
	var tapFirst, pressFirst []time.Duration
	for i := 0; i < *runs; i++ {
		for _, input := range []struct {
			method string
			params map[string]any
			into   *[]time.Duration
		}{
			{"input.touch", map[string]any{"tap": []int{320, 86}}, &tapFirst},
			{"input.press", map[string]any{"button": "boot", "ms": 100}, &pressFirst},
		} {
			idle(ctx, d)
			before := d.Frames()
			t0 := time.Now()
			done := make(chan error, 1)
			go func() { done <- d.Call(ctx, input.method, input.params, nil) }()
			f := waitFrame(d, before, time.Second)
			check(<-done)
			if f.IsZero() {
				fail("%s: no frame within 1 s", input.method)
			}
			*input.into = append(*input.into, f.Sub(t0))
		}
	}
	report["kn1_tap_to_first_frame"] = summarise(tapFirst, 200*time.Millisecond)
	report["kn1_press_to_first_frame"] = summarise(pressFirst, 200*time.Millisecond)

	// KN1: incremental emulator build after a one-file change.
	if *builds > 0 {
		var bt []time.Duration
		src := filepath.Join(project, "main", "main.c")
		for i := 0; i < *builds; i++ {
			now := time.Now()
			check(os.Chtimes(src, now, now))
			r, err := build.Build(ctx, idf, project, nil)
			check(err)
			if !r.OK {
				fail("incremental build failed: %v", r.Diagnostics)
			}
			bt = append(bt, r.Duration)
		}
		report["kn1_incremental_build"] = summarise(bt, 20*time.Second)
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(b))
}

func start(ctx context.Context, exe, elf string) (*instance.Info, *instance.Supervisor) {
	in, err := instance.Prepare(instance.Config{ID: "", ELF: elf, Options: instance.Options{Clock: "2026-10-12T09:00:00Z", Source: "usb"}})
	check(err)
	sup, err := instance.Spawn(in, exe, true, false, nil)
	check(err)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	in, err = instance.WaitReady(wctx, in.Dir, sup)
	check(err)
	return in, sup
}

func waitFor(ctx context.Context, in *instance.Info, re string) {
	d, err := drive.Connect(ctx, in)
	check(err)
	defer d.Close()
	var w rpc.Wait
	check(d.Call(ctx, "wait", map[string]any{"log": re, "since": 1, "timeout_ms": 10000}, &w))
	if !w.Matched {
		fail("no %q in the log", re)
	}
}

// idle waits until no frame has come for 300 ms, so the next frame is ours.
func idle(ctx context.Context, d *drive.Conn) {
	var w rpc.Wait
	check(d.Call(ctx, "wait", map[string]any{"idle_ms": 300, "timeout_ms": 5000}, &w))
}

// waitFrame returns when the frame counter passes after, or zero after limit.
func waitFrame(d *drive.Conn, after uint32, limit time.Duration) time.Time {
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		if d.Frames() != after {
			return time.Now()
		}
		time.Sleep(500 * time.Microsecond)
	}
	return time.Time{}
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "measure: "+format+"\n", a...)
	os.Exit(1)
}
