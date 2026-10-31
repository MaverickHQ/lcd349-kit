package instance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// stackSample writes a stack sample of every thread of pid to path: `sample`
// on macOS, gdb on Linux, or the threads' kernel wait channels when gdb
// isn't there or may not attach.
func stackSample(pid int, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	var err error
	switch runtime.GOOS {
	case "darwin":
		// The absolute path: python.org installers put their own `sample` on PATH.
		cmd := exec.CommandContext(ctx, "/usr/bin/sample", fmt.Sprint(pid), "1", "-mayDie", "-file", path)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err = cmd.Run(); err == nil {
			return nil
		}
		err = fmt.Errorf("sample: %v: %s", err, strings.TrimSpace(out.String()))
		out.Reset()
	case "linux":
		gdb, lerr := exec.LookPath("gdb")
		if lerr == nil {
			cmd := exec.CommandContext(ctx, gdb, "-batch", "-nx", "-p", fmt.Sprint(pid), "-ex", "thread apply all bt")
			cmd.Stdout, cmd.Stderr = &out, &out
			err = cmd.Run()
		}
		if lerr != nil || err != nil {
			if lerr != nil {
				fmt.Fprintf(&out, "gdb isn't installed: install it for full stacks.\n")
			} else {
				fmt.Fprintf(&out, "gdb couldn't attach (%v); /proc/sys/kernel/yama/ptrace_scope may forbid it.\n", err)
			}
			out.WriteString(procWaits(pid))
			err = nil
		}
	default:
		err = fmt.Errorf("no stack sampler on %s", runtime.GOOS)
	}
	if out.Len() > 0 {
		if werr := os.WriteFile(path, out.Bytes(), 0o600); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// procWaits lists each thread's name, state and kernel wait channel.
func procWaits(pid int) string {
	var b strings.Builder
	b.WriteString("Threads (name, state, wait channel):\n")
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*", pid))
	for _, t := range tasks {
		comm, _ := os.ReadFile(filepath.Join(t, "comm"))
		wchan, _ := os.ReadFile(filepath.Join(t, "wchan"))
		stat, _ := os.ReadFile(filepath.Join(t, "stat"))
		state := "?"
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
			state = string(stat[i+2])
		}
		fmt.Fprintf(&b, "  %s %-16s %s %s\n", filepath.Base(t), strings.TrimSpace(string(comm)), state, strings.TrimSpace(string(wchan)))
	}
	return b.String()
}
