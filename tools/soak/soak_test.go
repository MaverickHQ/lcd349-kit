package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The test binary plays both parts: with SOAK_TEST_MAIN=1 it is the soak
// command (main), and as the detached child it stands in for a running soak.
func TestMain(m *testing.M) {
	switch {
	case os.Getenv("LCD349_SOAK_DETACHED") == "1":
		fmt.Println("soak: the detached soak is running")
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case os.Getenv("SOAK_TEST_MAIN") == "1":
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDetachReturnsWhileTheSoakRuns(t *testing.T) {
	out := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-detach", "-duration", "8h", "-out", out)
	cmd.Env = append(os.Environ(), "SOAK_TEST_MAIN=1")
	start := time.Now()
	said, err := cmd.CombinedOutput()
	took := time.Since(start)
	if err != nil {
		t.Fatalf("soak -detach: %v\n%s", err, said)
	}
	if took > 3*time.Second {
		t.Errorf("soak -detach took %s: it waited for the soak", took)
	}
	m := regexp.MustCompile(`pid (\d+)`).FindSubmatch(said)
	if m == nil {
		t.Fatalf("soak -detach didn't give the soak's PID:\n%s", said)
	}
	pid, _ := strconv.Atoi(string(m[1]))
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the soak (pid %d) isn't running after -detach returned: %v", pid, err)
	}
	mine, _ := unix.Getsid(0)
	its, err := unix.Getsid(pid)
	if err != nil || its == mine {
		t.Errorf("the soak's session is %d (%v), the caller's %d: it must have its own", its, err, mine)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "soak.pid")); strings.TrimSpace(string(b)) != strconv.Itoa(pid) {
		t.Errorf("soak.pid holds %q, want %d", b, pid)
	}
	var log []byte
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if log, _ = os.ReadFile(filepath.Join(out, "soak.log")); strings.Contains(string(log), "the detached soak is running") {
			return
		}
	}
	t.Errorf("the soak's output didn't reach %s/soak.log: %q", out, log)
}
