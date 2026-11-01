package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTimeout: the command's own status comes back; a command that runs
// over the limit is killed promptly with status 124; a bad limit is 125.
func TestTimeout(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run([]string{"10", "sh", "-c", "echo hi; exit 3"}, nil, &out, &errs); code != 3 || out.String() != "hi\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 3 and hi", code, &out, &errs)
	}
	start := time.Now()
	errs.Reset()
	if code := run([]string{"0.5", "sleep", "30"}, nil, &out, &errs); code != 124 || !strings.Contains(errs.String(), "killed") {
		t.Fatalf("exit %d, stderr %q; want 124", code, &errs)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the limit took %s to act", d)
	}
	for _, bad := range []string{"x", "0", "-1", "NaN", "Inf", "+Inf", "-Inf"} {
		if code := run([]string{bad, "true"}, nil, &out, &errs); code != 125 {
			t.Errorf("limit %q: exit %d, want 125", bad, code)
		}
	}
}

// TestTimeoutKillsTheGroup: at the limit the command's whole process group
// is killed, so a child it started doesn't outlive it.
func TestTimeoutKillsTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	var out, errs bytes.Buffer
	start := time.Now()
	if code := run([]string{"1", "sh", "-c", "sleep 30 & echo $! > " + pidFile + "; wait"}, nil, &out, &errs); code != 124 {
		t.Fatalf("exit %d, want 124; stderr %q", code, &errs)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("timeout returned after %s: the command's child held its output open", d)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	deadline := time.Now().Add(5 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the command's child %d outlived the limit", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTimeoutExitStatus: a command killed by a signal gives 128 plus the
// signal, as a shell reports it; one that can't run gives 126, and one
// that isn't there 127.
func TestTimeoutExitStatus(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run([]string{"10", "sh", "-c", "kill -TERM $$"}, nil, &out, &errs); code != 128+15 {
		t.Errorf("killed by TERM: exit %d, want 143", code)
	}
	if code := run([]string{"10", "sh", "-c", "kill -INT $$"}, nil, &out, &errs); code != 128+2 {
		t.Errorf("killed by INT: exit %d, want 130", code)
	}
	plain := filepath.Join(t.TempDir(), "plain")
	os.WriteFile(plain, []byte("not a program\n"), 0o644)
	if code := run([]string{"10", plain}, nil, &out, &errs); code != 126 {
		t.Errorf("not executable: exit %d, want 126", code)
	}
	if code := run([]string{"10", "no-such-command-lcd349"}, nil, &out, &errs); code != 127 {
		t.Errorf("not on PATH: exit %d, want 127", code)
	}
	if code := run([]string{"10", filepath.Join(t.TempDir(), "missing")}, nil, &out, &errs); code != 127 {
		t.Errorf("no such file: exit %d, want 127", code)
	}
}

// TestTimeoutForwardsSignals: a TERM to timeout reaches the command's whole
// group, and timeout exits as the command does.
func TestTimeoutForwardsSignals(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "timeout")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(bin, "60", "sh", "-c", `trap 'exit 7' TERM; touch `+ready+`; sleep 30 & wait`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatal("the command never started")
		}
	}
	cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("timeout didn't pass TERM on")
	}
	if code := cmd.ProcessState.ExitCode(); code != 7 {
		t.Fatalf("exit %d, want the command's 7", code)
	}
}

// alive says whether pid is a live process. A killed child whose parent has
// gone is reparented, and in GitHub's job container PID 1 (tail -f
// /dev/null) never reaps it, so it stays a zombie that kill(pid, 0) still
// finds (CI run 41338616154; internal/clitest's alive does the same).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // Linux only
	if i := bytes.LastIndexByte(stat, ')'); err == nil && i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
		return false
	}
	return true
}
