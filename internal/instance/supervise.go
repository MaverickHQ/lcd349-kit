package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/pty"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Process exit codes from the product (src/emu/emu.h).
const (
	ExitReset         = 3 // power {action: reset}: start again
	ExitOff           = 4 // powered off: stay off until PWR is pressed
	ExitResetBootHeld = 5 // reset with BOOT held: start again in download mode
)

// Environment variables passed through from the user's environment; every
// other LCD349_* variable is set by the supervisor alone.
var passThrough = []string{"LCD349_FLUSH_MS=", "LCD349_WIFI_SCAN_INTERVAL_S="}

type supervisor struct {
	dir        string
	mu         sync.Mutex
	info       Info
	closing    bool // set by cleanup: instance.json is no longer written
	log        *serialLog
	a, b       *pty.Pty // product side, published serial port
	viewer     *exec.Cmd
	viewerDone chan struct{}
}

type proc struct {
	cmd  *exec.Cmd
	done chan exitResult
}

type exitResult struct {
	code   int // -1 when ended by a signal
	signal string
}

// Supervise runs the prepared instance in dir until ctx ends: it starts the
// product, relays its serial console, restarts it as its exit code says,
// records crashes and hangs, and finally stops everything and removes dir.
// echo, if not nil, gets a copy of the serial output.
func Supervise(ctx context.Context, dir string, echo io.Writer) (err error) {
	lk, err := lock(dir)
	if err != nil {
		return err
	}
	defer lk.Close()
	info, err := Read(dir)
	if err != nil {
		return err
	}
	s := &supervisor{dir: dir, info: *info}
	defer s.cleanup()
	if s.a, err = pty.Open(); err != nil {
		return s.fail(err)
	}
	if s.b, err = pty.Open(); err != nil {
		return s.fail(err)
	}
	s.log, err = openSerialLog(dir, echo, func(before int) {
		s.update(func(in *Info) { in.LogLinesBefore = before })
	})
	if err != nil {
		return s.fail(err)
	}
	(&relay{a: s.a, b: s.b, log: s.log}).start()
	s.update(func(in *Info) {
		in.SupervisorPID = os.Getpid()
		in.Serial = s.b.Slave
		in.State = StateStarting
	})
	return s.run(ctx)
}

func (s *supervisor) run(ctx context.Context) error {
	reason, bootHeld, first := "power-on", false, true
	for {
		p, err := s.start(reason, bootHeld, first)
		if err != nil {
			return s.fail(err)
		}
		first = false
		if s.viewer == nil && s.snapshot().Options.Viewer != "" {
			s.startViewer()
		}
		mctx, stopMonitor := context.WithCancel(ctx)
		go s.monitor(mctx, p.cmd.Process.Pid)
		var ex exitResult
		select {
		case ex = <-p.done:
		case <-ctx.Done():
			stopMonitor()
			p.cmd.Process.Kill()
			<-p.done
			return nil
		}
		stopMonitor()
		s.log.quiet(100*time.Millisecond, time.Second)
		bootHeld = false
		switch ex.code {
		case ExitReset:
			s.exited(ex, "reset", nil, StateStarting)
			reason = "software"
		case ExitResetBootHeld:
			s.exited(ex, "reset_boot_held", nil, StateStarting)
			reason, bootHeld = "software", true
		case ExitOff:
			s.exited(ex, "off", nil, StateOff)
			s.log.note("powered off; press PWR (lcd349 press pwr) to turn it on")
			if reason = s.serveStub(ctx); reason == "" {
				return nil
			}
		default:
			lines := s.log.last(CrashLines)
			s.exited(ex, "crash", lines, StateCrashed)
			s.log.note(fmt.Sprintf("the product %s; lcd349 power %s reset restarts it", describe(ex), s.snapshot().ID))
			if reason = s.serveStub(ctx); reason == "" {
				return nil
			}
		}
	}
}

func describe(ex exitResult) string {
	if ex.signal != "" {
		return "was killed by " + ex.signal
	}
	return fmt.Sprintf("exited with code %d", ex.code)
}

func (s *supervisor) exited(ex exitResult, meaning string, lines []string, state string) {
	s.update(func(in *Info) {
		in.PID = 0
		in.State = state
		in.Hung = nil
		in.LastExit = &Exit{Code: ex.code, Signal: ex.signal, Meaning: meaning, At: time.Now().UTC(), LastLines: lines}
	})
}

func (s *supervisor) start(reason string, bootHeld, first bool) (*proc, error) {
	s.a.FlushInput() // input sent while the device was down
	slave, err := s.a.SlaveForChild()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	elf := s.snapshot().ELF
	cmd := exec.Command(elf) // a fixed argument list: the program alone (R13.2)
	cmd.Dir = s.dir
	cmd.Env = s.env(reason, bootHeld, first)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	// Its own session, so a terminal's Ctrl-C doesn't reach it. The pty is
	// deliberately not its controlling terminal: when a session leader
	// exits, macOS revokes that terminal, which would cut the relay and drop
	// raw mode for the next boot. If the supervisor dies, the product reads
	// end of file on the console and exits (src/emu/serial.c).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", elf, err)
	}
	p := &proc{cmd: cmd, done: make(chan exitResult, 1)}
	go func() {
		cmd.Wait()
		p.done <- exitOf(cmd.ProcessState)
	}()
	var boots int
	s.update(func(in *Info) {
		in.PID = cmd.Process.Pid
		in.State = StateStarting
		in.Boots++
		in.BootReason = reason
		in.Hung = nil
		boots = in.Boots
	})
	held := ""
	if bootHeld {
		held = ", BOOT held"
	}
	s.log.note(fmt.Sprintf("boot %d (%s%s), pid %d", boots, reason, held, cmd.Process.Pid))
	return p, nil
}

func (s *supervisor) env(reason string, bootHeld, first bool) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LCD349_") && !hasPrefixAny(kv, passThrough) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "LCD349_INSTANCE_DIR="+s.dir)
	o := s.snapshot().Options
	if o.Storage != "" {
		env = append(env, "LCD349_STORAGE_DIR="+o.Storage)
	}
	if first {
		if o.Clock != "" {
			env = append(env, "LCD349_CLOCK="+o.Clock)
		}
		if o.Wipe {
			env = append(env, "LCD349_WIPE=1")
		}
		if o.BatteryMV != 0 {
			env = append(env, fmt.Sprintf("LCD349_BATTERY_MV=%d", o.BatteryMV))
		}
		if o.Source != "" {
			env = append(env, "LCD349_SOURCE="+o.Source)
		}
	}
	if reason == "software" || reason == "crash" {
		env = append(env, "LCD349_BOOT_REASON="+reason)
	}
	if bootHeld {
		env = append(env, "LCD349_BOOT_HELD=1")
	}
	return env
}

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// monitor pings the product: the first answer marks it running (or in
// download mode); no answer for HangAfter marks it hung, with a stack sample.
func (s *supervisor) monitor(ctx context.Context, pid int) {
	var c *rpc.Client
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	up, hung := false, false
	started := time.Now()
	for ctx.Err() == nil {
		if c == nil {
			dctx, cancel := context.WithTimeout(ctx, time.Second)
			var err error
			c, err = rpc.Dial(dctx, s.dir)
			cancel()
			if err != nil {
				c = nil
				if !up && !hung && time.Since(started) > 3*HangAfter {
					hung = true
					s.hung(pid, fmt.Sprintf("no control socket %v after start", 3*HangAfter))
				}
				sleep(ctx, 50*time.Millisecond)
				continue
			}
		}
		pctx, cancel := context.WithTimeout(ctx, HangAfter)
		err := c.Call(pctx, "ping", nil, nil)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.Close()
			c = nil
			if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
				if !hung {
					hung = true
					s.hung(pid, fmt.Sprintf("no answer to ping for %v", HangAfter))
				}
				continue
			}
			sleep(ctx, 50*time.Millisecond) // the product closed it: exiting, or busy restarting
			continue
		}
		if !up || hung {
			if hung {
				s.log.note("the instance answers again")
			}
			up, hung = true, false
			state := StateRunning
			var info rpc.Info
			ictx, cancel := context.WithTimeout(ctx, HangAfter)
			if c.Call(ictx, "info", nil, &info) == nil && info.DownloadMode {
				state = StateDownload
			}
			cancel()
			s.update(func(in *Info) {
				if in.PID == pid {
					in.State = state
					in.Hung = nil
				}
			})
		}
		sleep(ctx, time.Second)
	}
}

func (s *supervisor) hung(pid int, why string) {
	h := &Hang{Since: time.Now().UTC()}
	s.update(func(in *Info) {
		if in.PID == pid {
			in.State = StateHung
			in.Hung = h
		}
	})
	path := filepath.Join(s.dir, fmt.Sprintf("hang-%d.txt", pid))
	s.log.note(fmt.Sprintf("instance hung: %s; sampling its threads into %s", why, path))
	go func() {
		err := stackSample(pid, path)
		s.update(func(in *Info) {
			if in.Hung == h {
				if err != nil {
					in.Hung.SampleError = err.Error()
				} else {
					in.Hung.Sample = path
				}
			}
		})
	}()
}

func (s *supervisor) startViewer() {
	f, err := os.OpenFile(filepath.Join(s.dir, "viewer.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	cmd := exec.Command(s.snapshot().Options.Viewer, s.dir)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		s.update(func(in *Info) { in.Viewer = "failed: " + err.Error() })
		s.log.note("the viewer didn't start (" + err.Error() + "); running headless")
		return
	}
	s.viewer = cmd
	s.viewerDone = make(chan struct{})
	s.update(func(in *Info) { in.Viewer, in.ViewerPID = "running", cmd.Process.Pid })
	go func() {
		defer close(s.viewerDone)
		cmd.Wait()
		state := "closed"
		switch code := cmd.ProcessState.ExitCode(); code {
		case 0, -1:
		case 3:
			state = "no window"
			s.log.note("no window is available, so the instance runs headless; lcd349 doctor explains")
		default:
			state = fmt.Sprintf("failed (exit %d); see viewer.log", code)
		}
		s.update(func(in *Info) { in.Viewer, in.ViewerPID = state, 0 })
	}()
}

func (s *supervisor) update(fn func(*Info)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.info)
	if !s.closing {
		s.info.write()
	}
}

func (s *supervisor) snapshot() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

func (s *supervisor) fail(err error) error {
	s.update(func(in *Info) { in.Error = err.Error() })
	return err
}

// cleanup stops the viewer, closes the ports and removes the directory.
// Nothing may write into the directory once its removal starts.
func (s *supervisor) cleanup() {
	s.update(func(in *Info) { in.State = StateStopping })
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	if s.viewer != nil {
		s.viewer.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.viewerDone:
		case <-time.After(2 * time.Second):
			s.viewer.Process.Kill()
			<-s.viewerDone
		}
	}
	if s.log != nil {
		s.log.close()
	}
	if s.a != nil {
		s.a.Close()
	}
	if s.b != nil {
		s.b.Close()
	}
	os.RemoveAll(s.dir)
}

func exitOf(st *os.ProcessState) exitResult {
	ws, ok := st.Sys().(syscall.WaitStatus)
	if ok && ws.Signaled() {
		return exitResult{code: -1, signal: signalName(ws.Signal())}
	}
	return exitResult{code: st.ExitCode()}
}

func signalName(sig syscall.Signal) string {
	names := map[syscall.Signal]string{
		syscall.SIGABRT: "SIGABRT", syscall.SIGSEGV: "SIGSEGV", syscall.SIGBUS: "SIGBUS",
		syscall.SIGKILL: "SIGKILL", syscall.SIGTERM: "SIGTERM", syscall.SIGILL: "SIGILL",
		syscall.SIGFPE: "SIGFPE", syscall.SIGTRAP: "SIGTRAP", syscall.SIGHUP: "SIGHUP",
		syscall.SIGINT: "SIGINT", syscall.SIGPIPE: "SIGPIPE",
	}
	if n, ok := names[sig]; ok {
		return n
	}
	return fmt.Sprintf("signal %d", int(sig))
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
