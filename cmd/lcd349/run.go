package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
)

func init() {
	register(&command{name: "build", usage: "[-C project] [-v] [--json]", summary: "build a project for the emulator into <project>/build-emu", run: cmdBuild})
	register(&command{name: "run", usage: "[-C project | --elf program] [--headless] [--detach] [--clock time] [--wipe] [--battery-mv mV] [--usb|--battery] [--storage dir] [--id id] [--no-build] [--timings] [--json]",
		summary: "build and start an instance, with a window unless --headless", run: cmdRun})
}

// buildProject builds project for the emulator and returns its program,
// printing compiler errors with the kit's hints.
func buildProject(c *cmdCtx, project string, verbose bool) (*build.Result, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	idf, err := build.Environment(ctx, false)
	if err != nil {
		return nil, err
	}
	var out io.Writer
	if verbose {
		out = c.stderr
	} else if !c.json {
		fmt.Fprintf(c.stderr, "lcd349: building %s for the emulator (log: %s)\n", filepath.Base(project), filepath.Join(build.Dir, build.LogName))
	}
	return build.Build(ctx, idf, project, out)
}

func reportBuild(c *cmdCtx, res *build.Result) {
	if res.OK {
		return
	}
	fmt.Fprintf(c.stderr, "lcd349: the build failed:\n")
	for _, d := range res.Diagnostics {
		fmt.Fprintf(c.stderr, "  %s\n", d)
	}
	fmt.Fprintf(c.stderr, "full log: %s\n", res.Log)
}

func cmdBuild(c *cmdCtx) int {
	c.jsonFlag()
	dir := c.flags.String("C", ".", "the project `directory`")
	verbose := c.flags.Bool("v", false, "show idf.py's output")
	if code, ok := c.parse(); !ok {
		return code
	}
	project, err := build.CheckProject(*dir)
	if err != nil {
		return c.fail(err)
	}
	res, err := buildProject(c, project, *verbose)
	if err != nil {
		return c.fail(err)
	}
	if c.json {
		c.printJSON(res)
	} else if res.OK {
		fmt.Fprintf(c.stdout, "built %s in %.1fs\n", res.ELF, res.Seconds)
	} else {
		reportBuild(c, res)
	}
	if !res.OK {
		return exitFail
	}
	return exitOK
}

type runTimings struct {
	BuildS      float64 `json:"build_s,omitempty"`
	ReadyS      float64 `json:"ready_s"`       // supervisor start to the control socket answering
	FirstFrameS float64 `json:"first_frame_s"` // supervisor start to the first frame (KN2)
}

func cmdRun(c *cmdCtx) int {
	c.jsonFlag()
	dir := c.flags.String("C", ".", "the project `directory`")
	elf := c.flags.String("elf", "", "run this emulator `program` instead of building a project")
	headless := c.flags.Bool("headless", false, "no window")
	detach := c.flags.Bool("detach", false, "keep the instance running after this command returns (lcd349 stop ends it)")
	noBuild := c.flags.Bool("no-build", false, "run the last build without building")
	id := c.flags.String("id", "", "instance `id` (default: from the project name)")
	var o instance.Options
	c.flags.StringVar(&o.Clock, "clock", "", "start the RTC at this `time` (RFC 3339 or Unix seconds) for repeatable screens")
	c.flags.BoolVar(&o.Wipe, "wipe", false, "start with empty storage")
	c.flags.IntVar(&o.BatteryMV, "battery-mv", 0, "battery cell `millivolts` at first boot (2500-4500)")
	usb := c.flags.Bool("usb", false, "powered from USB at first boot")
	battery := c.flags.Bool("battery", false, "running on battery at first boot")
	c.flags.StringVar(&o.Storage, "storage", "", "keep storage in this `directory` instead of the instance's")
	timings := c.flags.Bool("timings", false, "print how long the build, start and first frame took")
	verbose := c.flags.Bool("v", false, "show idf.py's output")
	if code, ok := c.parse(); !ok {
		return code
	}
	if len(c.args) > 0 {
		return c.usageErr("unexpected argument %q", c.args[0])
	}
	if *usb && *battery {
		return c.usageErr("--usb and --battery contradict each other")
	}
	if *usb {
		o.Source = "usb"
	} else if *battery {
		o.Source = "battery"
	}
	if o.BatteryMV != 0 && (o.BatteryMV < 2500 || o.BatteryMV > 4500) {
		return c.usageErr("--battery-mv must be 2500-4500")
	}
	if o.Storage != "" {
		abs, err := filepath.Abs(o.Storage)
		if err != nil {
			return c.fail(err)
		}
		o.Storage = abs
	}

	var t runTimings
	cfg := instance.Config{ID: *id, ELF: *elf, Options: o}
	if *elf == "" {
		project, err := build.CheckProject(*dir)
		if err != nil {
			return c.fail(err)
		}
		cfg.Project = project
		if *noBuild {
			if cfg.ELF, err = build.ELF(project); err != nil {
				return c.fail(err)
			}
		} else {
			res, err := buildProject(c, project, *verbose)
			if err != nil {
				return c.fail(err)
			}
			if !res.OK {
				reportBuild(c, res)
				return exitFail
			}
			cfg.ELF, t.BuildS = res.ELF, res.Seconds
		}
	}
	if !*headless {
		view, err := viewer.Path(c.stderr)
		if err != nil {
			fmt.Fprintf(c.stderr, "lcd349: no window: %v\n", errText(err))
		} else {
			cfg.Options.Viewer = view
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return c.fail(err)
	}
	in, err := instance.Prepare(cfg)
	if err != nil {
		return c.fail(err)
	}

	// Foreground: the supervisor shares the terminal and copies the serial
	// output to it; Ctrl-C reaches it too and stops the instance.
	start := time.Now()
	var out io.Writer
	if !*detach {
		out = c.stdout
		signal.Ignore(os.Interrupt) // the supervisor handles it; then this command returns
	}
	sup, err := instance.Spawn(in, exe, !*detach, !*detach && !c.json, out)
	if err != nil {
		return c.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	in, err = instance.WaitReady(ctx, in.Dir, sup)
	cancel()
	if err != nil {
		instance.Stop(in.Dir)
		return c.fail(err)
	}
	t.ReadyS = time.Since(start).Seconds()
	if fh, err := waitFirstFrame(in, 10*time.Second); err == nil && fh > 0 {
		t.FirstFrameS = time.Since(start).Seconds()
	}

	if c.json {
		c.printJSON(map[string]any{"instance": in.ID, "dir": in.Dir, "serial": in.Serial, "pid": in.PID,
			"state": in.State, "viewer": in.Options.Viewer != "", "timings": t})
	} else {
		fmt.Fprintf(c.stderr, "lcd349: instance %s is %s (pid %d)\n  serial port: %s\n  directory:   %s\n",
			in.ID, in.State, in.PID, in.Serial, in.Dir)
		if *detach {
			fmt.Fprintf(c.stderr, "  stop it with: lcd349 stop %s\n", in.ID)
		} else {
			fmt.Fprintf(c.stderr, "  Ctrl-C stops it\n")
		}
	}
	if *timings {
		fmt.Fprintf(c.stderr, "timings: build %.2fs, ready %.2fs, first frame %.2fs\n", t.BuildS, t.ReadyS, t.FirstFrameS)
	}
	if *detach {
		return exitOK
	}
	sup.Wait()
	return exitOK
}

// waitFirstFrame waits until the current product has presented a frame.
func waitFirstFrame(in *instance.Info, limit time.Duration) (uint32, error) {
	deadline := time.Now().Add(limit)
	for {
		fh, err := instance.ReadFrameHeader(in.Dir)
		if err == nil && int(fh.PID) == in.PID && fh.Frames > 0 {
			return fh.Frames, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = errors.New("no frame yet")
			}
			return 0, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
