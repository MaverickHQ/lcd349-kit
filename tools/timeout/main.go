// Command timeout runs a command and kills it if it runs too long:
//
//	go run ./tools/timeout <seconds> <command> [args...]
//
// It exits as GNU timeout(1) does: with the command's status (128 plus the
// signal when a signal ended it), 124 when the limit killed it, 125 for a
// bad limit, 126 when the command can't be run and 127 when it isn't there.
// The command runs in a process group of its own: the limit kills the whole
// group, and a TERM, INT or HUP sent to timeout is passed on to it.
//
// macOS has no timeout(1), and an alarm set before exec (perl -e 'alarm N;
// exec ...') can't limit a program built for ESP-IDF's linux target: its
// FreeRTOS tick takes SIGALRM over. This one signals from the parent
// process instead, as CLAUDE.md asks. The phase gates run the host tests
// under it.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: timeout <seconds> <command> [args...]")
		return 125
	}
	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil || math.IsNaN(secs) || math.IsInf(secs, 0) || secs <= 0 || secs > 1e9 {
		fmt.Fprintf(stderr, "timeout: %q isn't a number of seconds\n", args[0])
		return 125
	}
	limit := time.Duration(secs * float64(time.Second))

	// Signals are caught before the command starts, so none is lost.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)

	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The command's children may keep its output open after it's killed:
	// don't wait for them.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "timeout:", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}
	group := -cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case err := <-done:
			return status(err, stderr)
		case s := <-sigs:
			syscall.Kill(group, s.(syscall.Signal))
		case <-timer.C:
			syscall.Kill(group, syscall.SIGKILL)
			<-done
			fmt.Fprintf(stderr, "timeout: %s ran for over %s; killed\n", args[1], limit)
			return 124
		}
	}
}

// status is the exit status for the command's end: its own, or 128 plus
// the signal that ended it.
func status(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exit.ExitCode()
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return 0 // the command exited 0; a child still held its output
	}
	fmt.Fprintln(stderr, "timeout:", err)
	return 125
}
