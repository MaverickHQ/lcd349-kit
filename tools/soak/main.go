// Command soak runs examples/inputs for a long time under a scripted loop
// of input, inspection and state changes, sampling the product's and the
// supervisor's resident memory every minute (task 10.2, KN9: 8 hours, no
// crash or hang, under 10 % growth).
//
//	go run ./tools/soak [-duration 8h] [-out dir] [-elf program] [-detach]
//
// -detach starts the soak in its own process session and returns with its
// PID (design D1-7): the soak outlives the session that started it, writes
// its output to <out>/soak.log and its PID to <out>/soak.pid, and keeps the
// Mac awake itself (caffeinate -i -w <its PID>) until it ends.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

type sample struct {
	At           time.Time
	Iteration    int
	ProductKB    int
	SupervisorKB int
	Frames       uint32
	State        string
}

func main() {
	duration := flag.Duration("duration", 8*time.Hour, "how long to run")
	every := flag.Duration("sample", time.Minute, "memory sample interval")
	warmup := flag.Duration("warmup", 10*time.Minute, "growth is measured from after this")
	window := flag.Duration("window", time.Hour, "growth compares the median of this long after the warm-up with the median of this long at the end")
	outDir := flag.String("out", "soak-out", "directory for samples.csv and summary.json")
	elf := flag.String("elf", "", "run this inputs program instead of building examples/inputs")
	detach := flag.Bool("detach", false, "start the soak in its own process session, print its PID and return")
	flag.Parse()
	check(os.MkdirAll(*outDir, 0o755))
	if *detach {
		pid, err := startDetached(os.Args[1:], *outDir)
		check(err)
		fmt.Printf("soak: started pid %d in its own session; output in %s\n", pid, filepath.Join(*outDir, "soak.log"))
		return
	}
	if os.Getenv(detachedEnv) == "1" {
		keepAwake()
	}
	ctx := context.Background()
	tmp, err := os.MkdirTemp("", "lcd349-soak-")
	check(err)
	defer os.RemoveAll(tmp)
	inst, err := os.MkdirTemp("/tmp", "l349s-")
	check(err)
	defer os.RemoveAll(inst)
	os.Setenv("LCD349_INSTANCES", inst)
	exe := filepath.Join(tmp, "lcd349")
	if out, err := exec.Command("go", "build", "-o", exe, "./cmd/lcd349").CombinedOutput(); err != nil {
		fail("building lcd349: %v\n%s", err, out)
	}
	program := *elf
	if program == "" {
		idf, err := build.Environment(ctx, false)
		check(err)
		project, _ := filepath.Abs(filepath.Join("examples", "inputs"))
		res, err := build.Build(ctx, idf, project, nil)
		check(err)
		if !res.OK {
			fail("examples/inputs doesn't build")
		}
		program = res.ELF
	}
	in, err := instance.Prepare(instance.Config{ELF: program, Options: instance.Options{Clock: "2026-10-12T09:00:00Z", Source: "usb"}})
	check(err)
	sup, err := instance.Spawn(in, exe, true, false, nil)
	check(err)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	in, err = instance.WaitReady(wctx, in.Dir, sup)
	cancel()
	check(err)
	defer func() { instance.Stop(in.Dir); sup.Wait() }()

	f, err := os.Create(filepath.Join(*outDir, "samples.csv"))
	check(err)
	defer f.Close()
	w := csv.NewWriter(f)
	w.Write([]string{"elapsed_s", "iteration", "product_rss_kb", "supervisor_rss_kb", "frames", "state"})

	startT := time.Now()
	var samples []sample
	var failure string
	take := func(it int) {
		cur, _ := instance.Read(in.Dir)
		s := sample{At: time.Now(), Iteration: it, ProductKB: rss(in.PID), SupervisorKB: rss(cur.SupervisorPID), State: cur.State}
		if fh, err := instance.ReadFrameHeader(in.Dir); err == nil {
			s.Frames = fh.Frames
		}
		samples = append(samples, s)
		w.Write([]string{strconv.Itoa(int(s.At.Sub(startT).Seconds())), strconv.Itoa(it), strconv.Itoa(s.ProductKB),
			strconv.Itoa(s.SupervisorKB), strconv.Itoa(int(s.Frames)), s.State})
		w.Flush()
		fmt.Fprintf(os.Stderr, "soak: %s iteration %d, product %d KB, supervisor %d KB, %s\n",
			s.At.Sub(startT).Round(time.Second), it, s.ProductKB, s.SupervisorKB, s.State)
	}
	take(0)
	next := startT.Add(*every)
	d, err := drive.Connect(ctx, in)
	check(err)
	it := 0
	for time.Since(startT) < *duration {
		it++
		if err := iteration(ctx, &d, in, it); err != nil {
			failure = fmt.Sprintf("iteration %d: %v", it, err)
			break
		}
		if cur, _ := instance.Read(in.Dir); cur == nil || cur.PID != in.PID || cur.State != instance.StateRunning {
			state := "gone"
			if cur != nil {
				state = cur.State
			}
			failure = fmt.Sprintf("iteration %d: the instance is %s (pid %d)", it, state, in.PID)
			break
		}
		if time.Now().After(next) {
			take(it)
			next = next.Add(*every)
		}
	}
	d.Close()
	take(it)
	if failure != "" {
		captureFailure(in, filepath.Join(*outDir, "failure"))
	}

	// Growth: resident memory jitters by a megabyte or more from one sample
	// to the next on macOS, so compare medians: the window after the warm-up
	// with the window at the end.
	elapsed := time.Since(startT)
	median := func(from, to time.Duration, get func(sample) int) int {
		var v []int
		for _, s := range samples {
			if at := s.At.Sub(startT); at >= from && at <= to {
				v = append(v, get(s))
			}
		}
		if len(v) == 0 {
			return 0
		}
		sort.Ints(v)
		return v[len(v)/2]
	}
	prod := func(s sample) int { return s.ProductKB }
	supv := func(s sample) int { return s.SupervisorKB }
	base, end := median(*warmup, *warmup+*window, prod), median(elapsed-*window, elapsed, prod)
	supBase, supEnd := median(*warmup, *warmup+*window, supv), median(elapsed-*window, elapsed, supv)
	peak := 0
	for _, s := range samples {
		peak = max(peak, s.ProductKB)
	}
	last := samples[len(samples)-1]
	growth := 100 * float64(end-base) / float64(max(base, 1))
	supGrowth := 100 * float64(supEnd-supBase) / float64(max(supBase, 1))
	summary := map[string]any{
		"kit_version": lcd349kit.Version, "started": startT.Format(time.RFC3339), "ran": time.Since(startT).Round(time.Second).String(),
		"iterations": it, "samples": len(samples), "failure": failure,
		"window": window.String(), "warmup": warmup.String(),
		"product_rss_kb":     map[string]int{"first": samples[0].ProductKB, "median_after_warmup": base, "median_at_end": end, "peak": peak},
		"product_growth_pct": round1(growth), "supervisor_rss_kb": map[string]int{"median_after_warmup": supBase, "median_at_end": supEnd},
		"supervisor_growth_pct": round1(supGrowth), "frames": last.Frames,
		"pass": failure == "" && elapsed >= *duration && growth < 10,
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	check(os.WriteFile(filepath.Join(*outDir, "summary.json"), b, 0o644))
	fmt.Println(string(b))
	if failure != "" {
		os.Exit(1)
	}
}

// detachedEnv marks the soak that -detach started.
const detachedEnv = "LCD349_SOAK_DETACHED"

// startDetached runs this program again with args minus -detach, in a new
// process session (so a session's end, or a signal to the caller's process
// group, doesn't reach it), with no input and its output in outDir/soak.log.
func startDetached(args []string, outDir string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	var keep []string
	for _, a := range args {
		if f := strings.TrimLeft(a, "-"); f == "detach" || strings.HasPrefix(f, "detach=") {
			continue
		}
		keep = append(keep, a)
	}
	log, err := os.OpenFile(filepath.Join(outDir, "soak.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(exe, keep...)
	cmd.Env = append(os.Environ(), detachedEnv+"=1")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, os.WriteFile(filepath.Join(outDir, "soak.pid"), []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

// keepAwake holds off idle sleep while this process runs: the runner's own
// assertion ends when it stops for a checkpoint.
func keepAwake() {
	cmd := exec.Command("/usr/bin/caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "soak: can't keep the Mac awake (caffeinate: %v)\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "soak: caffeinate pid %d keeps the Mac awake while soak pid %d runs\n", cmd.Process.Pid, os.Getpid())
	go cmd.Wait()
}

// iteration is one round of what an agent or a person does to an instance.
func iteration(ctx context.Context, d **drive.Conn, in *instance.Info, it int) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := *d
	if it%25 == 0 { // a fresh connection now and then, as each CLI command makes
		c.Close()
		var err error
		if c, err = drive.Connect(ctx, in); err != nil {
			return err
		}
		*d = c
	}
	var w rpc.Wait
	steps := []func() error{
		func() error { return touch(ctx, c, map[string]any{"tap": []int{320, 88}}) },
		func() error {
			return c.Call(ctx, "wait", map[string]any{"log": fmt.Sprintf("tap count %d$", it), "timeout_ms": 5000}, &w)
		},
		func() error { return c.Call(ctx, "input.press", map[string]any{"button": "boot", "ms": 120}, nil) },
		func() error {
			return touch(ctx, c, map[string]any{"swipe": map[string]any{"from": []int{80, 30}, "to": []int{560, 30}}, "ms": 200})
		},
		func() error { _, err := c.Screenshot(ctx, drive.ShotOptions{}); return err },
		func() error { return c.Call(ctx, "ui.tree", map[string]any{}, nil) },
		func() error { return c.Call(ctx, "ui.lint", map[string]any{}, nil) },
		func() error {
			return c.Call(ctx, "state.set", map[string]any{"battery": map[string]any{"mv": 3600 + 100*(it%4)}}, nil)
		},
	}
	if it%50 == 0 { // Wi-Fi comes into range, then goes
		nets := []any{}
		if it%100 == 0 {
			nets = []any{map[string]any{"ssid": "lcd349-test", "password": "password123"}}
		}
		steps = append(steps, func() error {
			return c.Call(ctx, "state.set", map[string]any{"wifi": map[string]any{"networks": nets}}, nil)
		})
	}
	if it%60 == 0 { // light sleep, and a wake
		steps = append(steps,
			func() error { return c.Call(ctx, "input.press", map[string]any{"button": "boot", "ms": 1200}, nil) },
			func() error {
				return c.Call(ctx, "wait", map[string]any{"log": "sleeping until a button", "timeout_ms": 5000}, &w)
			},
			// The line comes just before the product sleeps; a press before
			// then is missed, as on the board.
			func() error { return waitPower(ctx, c, "light_sleep") },
			func() error { return c.Call(ctx, "input.press", map[string]any{"button": "pwr", "ms": 100}, nil) },
			func() error { return c.Call(ctx, "wait", map[string]any{"log": "woke by PWR", "timeout_ms": 5000}, &w) },
		)
	}
	for i, s := range steps {
		w = rpc.Wait{Matched: true}
		if err := s(); err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		if !w.Matched {
			return fmt.Errorf("step %d: wait timed out; last lines: %s", i, strings.Join(w.Tail, " | "))
		}
	}
	return nil
}

// touch fails unless every sample reached the product: a touch on a dark
// display is dropped.
func touch(ctx context.Context, c *drive.Conn, p map[string]any) error {
	var t rpc.Touch
	if err := c.Call(ctx, "input.touch", p, &t); err != nil {
		return err
	}
	if !t.Delivered {
		return fmt.Errorf("touch not delivered: %s", t.Reason)
	}
	return nil
}

// waitPower polls until the instance's power state is want.
func waitPower(ctx context.Context, c *drive.Conn, want string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var st rpc.State
		if err := c.Call(ctx, "state.get", nil, &st); err != nil {
			return err
		}
		if st.Power == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("power stayed %s, not %s", st.Power, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// rss is a process's resident memory in KB, from ps (a fixed argument list).
func rss(pid int) int {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "soak: "+format+"\n", a...)
	os.Exit(1)
}

// captureFailure keeps the evidence before the instance is stopped: the
// supervisor's goroutines (SIGQUIT makes a Go program print them), a stack
// sample of the product, and the instance's files.
func captureFailure(in *instance.Info, dir string) {
	os.MkdirAll(dir, 0o755)
	cur, err := instance.Read(in.Dir)
	if err != nil {
		return
	}
	// The device's state now, and whether its log moves on after a pause:
	// late lines mean output was held up, none mean nothing was logged.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if c, err := drive.Connect(ctx, cur); err == nil {
		var st map[string]any
		c.Call(ctx, "state.get", nil, &st)
		var now, later rpc.Wait
		never := map[string]any{"log": "^lcd349-soak-never$", "timeout_ms": 50}
		c.Call(ctx, "wait", never, &now)
		time.Sleep(3 * time.Second)
		c.Call(ctx, "wait", never, &later)
		c.Close()
		b, _ := json.MarshalIndent(map[string]any{"state": st, "tail": now.Tail, "tail_after_3s": later.Tail}, "", "  ")
		os.WriteFile(filepath.Join(dir, "after.json"), b, 0o644)
	}
	if cur.PID > 0 {
		if out, err := exec.Command("/usr/sbin/lsof", "-p", strconv.Itoa(cur.PID)).Output(); err == nil {
			os.WriteFile(filepath.Join(dir, "product-lsof.txt"), out, 0o644)
		}
		exec.Command("/usr/bin/sample", strconv.Itoa(cur.PID), "2", "-file", filepath.Join(dir, "product-sample.txt")).Run()
	}
	for _, f := range []string{"instance.json", "serial.log"} {
		if b, err := os.ReadFile(filepath.Join(in.Dir, f)); err == nil {
			os.WriteFile(filepath.Join(dir, f), b, 0o644)
		}
	}
	if cur.SupervisorPID > 0 {
		syscall.Kill(cur.SupervisorPID, syscall.SIGQUIT)
		time.Sleep(2 * time.Second)
	}
	if b, err := os.ReadFile(filepath.Join(in.Dir, "supervisor.log")); err == nil {
		os.WriteFile(filepath.Join(dir, "supervisor-goroutines.txt"), b, 0o644)
	}
}
