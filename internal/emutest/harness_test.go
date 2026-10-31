// Package emutest holds integration tests for the emulator backend (task
// 4.4): they start examples/inputs built for the linux target and drive it
// over its control socket.
//
// Build the example first, or the tests skip:
//
//	cd examples/inputs && idf.py -B build-emu -D SDKCONFIG=build-emu/sdkconfig --preview set-target linux build
//
// LCD349_INPUTS_ELF overrides the binary's path.
package emutest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Exit codes from the instance (src/emu/emu.h).
const (
	exitReset         = 3
	exitOff           = 4
	exitResetBootHeld = 5
)

func inputsELF(t *testing.T) string {
	t.Helper()
	p := os.Getenv("LCD349_INPUTS_ELF")
	if p == "" {
		p = filepath.Join("..", "..", "examples", "inputs", "build-emu", "inputs.elf")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("no emulator build of examples/inputs at %s; build it for the linux target first", abs)
	}
	return abs
}

type instance struct {
	t      *testing.T
	elf    string
	dir    string
	cmd    *exec.Cmd
	done   chan struct{}
	exit   int
	client *rpc.Client
}

// newDir makes a short instance directory: macOS limits socket paths to 104 bytes.
func newDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "l349t-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// start runs the example in dir (a new one if empty) with extra environment.
func start(t *testing.T, dir string, env ...string) *instance {
	t.Helper()
	elf := inputsELF(t)
	if dir == "" {
		dir = newDir(t)
	}
	in := &instance{t: t, elf: elf, dir: dir, done: make(chan struct{})}
	serial, err := os.OpenFile(filepath.Join(dir, "serial.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	in.cmd = exec.Command(elf)
	in.cmd.Env = append(os.Environ(), append([]string{"LCD349_INSTANCE_DIR=" + dir}, env...)...)
	in.cmd.Stdout = serial
	in.cmd.Stderr = serial
	if err := in.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := in.cmd.Wait()
		serial.Close()
		in.exit = 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			in.exit = ee.ExitCode()
		}
		close(in.done)
	}()
	t.Cleanup(in.kill)
	in.connect()
	return in
}

func (in *instance) connect() {
	in.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		c, err := rpc.Dial(ctx, in.dir)
		if err == nil {
			if err = c.Call(ctx, "ping", nil, nil); err == nil {
				cancel()
				in.client = c
				in.t.Cleanup(func() { c.Close() })
				return
			}
			c.Close()
		}
		cancel()
		select {
		case <-in.done:
			in.t.Fatalf("instance exited (code %d) before its control socket answered:\n%s", in.exit, in.serial())
		default:
		}
		if time.Now().After(deadline) {
			in.t.Fatalf("control socket didn't answer within 10 s: %v\n%s", err, in.serial())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (in *instance) kill() {
	select {
	case <-in.done:
	default:
		in.cmd.Process.Signal(syscall.SIGKILL)
		<-in.done
	}
}

// waitExit waits for the process to end and returns its exit code.
func (in *instance) waitExit(d time.Duration) int {
	in.t.Helper()
	select {
	case <-in.done:
		return in.exit
	case <-time.After(d):
		in.t.Fatalf("instance still running after %v", d)
		return -1
	}
}

func (in *instance) serial() string {
	b, _ := os.ReadFile(filepath.Join(in.dir, "serial.log"))
	return string(b)
}

// call makes one request with a 30 s limit and fails the test on error.
func (in *instance) call(method string, params, result any) {
	in.t.Helper()
	if err := in.try(method, params, result); err != nil {
		in.t.Fatalf("%s: %v\nserial:\n%s", method, err, tail(in.serial(), 15))
	}
}

func (in *instance) try(method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return in.client.Call(ctx, method, params, result)
}

// waitLog waits for a serial line matching re, looking from line `since`.
func (in *instance) waitLog(re string, since int64) rpc.Wait {
	in.t.Helper()
	var w rpc.Wait
	in.call("wait", map[string]any{"log": re, "since": since, "timeout_ms": 8000}, &w)
	if !w.Matched {
		in.t.Fatalf("no serial line matched %q within 8 s; last lines:\n%s", re, strings.Join(w.Tail, "\n"))
	}
	return w
}

// ready waits until the example has built its screen.
func (in *instance) ready() {
	in.t.Helper()
	in.waitLog(`inputs: ready`, 1)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func countMatches(s, re string) int {
	return len(regexp.MustCompile(re).FindAllStringIndex(s, -1))
}

func fbHeader(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "fb.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 64 || !bytes.Equal(b[:8], []byte("L349FB01")) {
		t.Fatalf("fb.bin has no valid header")
	}
	return b[:64]
}
