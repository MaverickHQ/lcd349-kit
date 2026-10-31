package clitest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSerialPort: a host tool talks to the product through the published
// pty exactly as pager-bridge provision would over USB (R5.7), and the port
// is owner-only (KT8).
func TestSerialPort(t *testing.T) {
	c := newCLI(t)
	in := c.start()
	st, err := os.Stat(in.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("serial port %s mode %v, want 0600", in.Serial, st.Mode().Perm())
	}
	f := port(t, in.Serial)
	// A key generated for this test, not a real host's.
	key := "@P echo key ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK3qWRc7itPCx1YzCPqg//0uPVqN8uE/PSokpwszv2fA pager-01"
	if _, err := f.Write([]byte("@P ping\r\n" + key + "\n")); err != nil {
		t.Fatal(err)
	}
	lines := readUntil(t, f, 5*time.Second, func(l []string) bool { return count(l, "@P pong") > 0 && count(l, key) > 0 })
	// Raw, as a USB port: what the tool wrote isn't echoed back.
	if count(lines, "@P ping") != 0 || count(lines, key) != 1 {
		t.Fatalf("the port echoed input:\n%s", strings.Join(lines, "\n"))
	}
	// The same output is in serial.log, numbered for lcd349 logs.
	var logs struct {
		Lines []struct {
			N    int    `json:"n"`
			Text string `json:"text"`
		} `json:"lines"`
		Next int `json:"next"`
	}
	json.Unmarshal([]byte(c.must("logs", in.ID, "--grep", "^@P ", "--json")), &logs)
	if len(logs.Lines) != 2 || logs.Lines[0].Text != "@P pong" || logs.Lines[0].N < 2 || logs.Next <= logs.Lines[1].N {
		t.Fatalf("logs --grep: %+v", logs)
	}
	json.Unmarshal([]byte(c.must("logs", in.ID, "--since", "1000000", "--json")), &logs)
	if len(logs.Lines) != 0 {
		t.Fatalf("logs --since past the end gave %d lines", len(logs.Lines))
	}
}

// TestRestartsFollowExitCodes: 3 restarts, 5 restarts in download mode, 4
// stays off until PWR or power on (task 6.1).
func TestRestartsFollowExitCodes(t *testing.T) {
	c := newCLI(t)
	in := c.start()
	pid := in.PID

	c.must("power", in.ID, "reset")
	in = c.waitFor(in.ID, "a software boot", 10*time.Second, func(i inst) bool { return i.Boots == 2 && i.State == "running" })
	if in.PID == pid || in.BootReason != "software" || in.LastExit == nil || in.LastExit.Meaning != "reset" {
		t.Fatalf("after reset: %+v", in)
	}
	if out := c.must("logs", in.ID, "--grep", "boot count 2, boot reason 1"); out == "" {
		t.Fatal("the product didn't report boot 2 with reason software")
	}

	c.must("power", in.ID, "download")
	in = c.waitState(in.ID, "download", 10*time.Second)
	c.must("power", in.ID, "reset")
	in = c.waitFor(in.ID, "boot 4", 10*time.Second, func(i inst) bool { return i.Boots == 4 && i.State == "running" })

	c.must("power", in.ID, "off")
	in = c.waitState(in.ID, "off", 10*time.Second)
	if in.PID != 0 || in.LastExit.Meaning != "off" {
		t.Fatalf("after off: %+v", in)
	}
	if _, errOut, code := c.run("power", in.ID, "reset"); code == 0 || !strings.Contains(errOut, "press PWR") {
		t.Fatalf("reset while off: exit %d, %q", code, errOut)
	}
	c.must("power", in.ID, "on")
	in = c.waitFor(in.ID, "power-on boot", 10*time.Second, func(i inst) bool { return i.Boots == 5 && i.State == "running" })
	if in.BootReason != "power-on" {
		t.Fatalf("boot reason %q after power on", in.BootReason)
	}
}

// TestCrashIsCaptured: an abnormal exit is recorded with the last serial
// lines, requests are answered with them, and a reset boots with reason crash.
func TestCrashIsCaptured(t *testing.T) {
	c := newCLI(t)
	in := c.start()
	f := port(t, in.Serial)
	f.Write([]byte("@P crash\n"))
	in = c.waitState(in.ID, "crashed", 10*time.Second)
	// The product's last line and ESP-IDF's own abort message, written just
	// before the process ends, are both kept.
	if in.LastExit == nil || in.LastExit.Meaning != "crash" || !contains(in.LastExit.LastLines, "crashing on request") ||
		!contains(in.LastExit.LastLines, "ERROR: Aborted") {
		t.Fatalf("crash record: %+v", in.LastExit)
	}
	if n := len(in.LastExit.LastLines); n == 0 || n > 200 {
		t.Fatalf("crash record keeps %d lines", n)
	}
	_, errOut, code := c.run("power", in.ID, "off")
	if code == 0 || !strings.Contains(errOut, "instance_exited") || !strings.Contains(errOut, "crashing on request") {
		t.Fatalf("a request after the crash: exit %d\n%s", code, errOut)
	}
	c.must("power", in.ID, "reset")
	in = c.waitFor(in.ID, "a boot after the crash", 10*time.Second, func(i inst) bool { return i.State == "running" && i.Boots == 2 })
	if in.BootReason != "crash" {
		t.Fatalf("boot reason %q, want crash", in.BootReason)
	}
	if out := c.must("logs", in.ID, "--grep", "boot reason 2"); out == "" {
		t.Fatal("the product didn't see boot reason crash")
	}
}

// TestHangIsReported: a product that stops answering is reported hung with
// a stack sample, and running again once it answers.
func TestHangIsReported(t *testing.T) {
	c := newCLI(t)
	in := c.start()
	syscall.Kill(in.PID, syscall.SIGSTOP)
	defer syscall.Kill(in.PID, syscall.SIGCONT)
	in = c.waitFor(in.ID, "a hang report with a sample", 30*time.Second, func(i inst) bool {
		return i.State == "hung" && i.Hung != nil && (i.Hung.Sample != "" || i.Hung.SampleError != "")
	})
	if in.Hung.Sample == "" {
		t.Logf("no stack sample: %s", in.Hung.SampleError)
	} else if b, err := os.ReadFile(in.Hung.Sample); err != nil || len(b) == 0 {
		t.Fatalf("stack sample %s: %v", in.Hung.Sample, err)
	}
	syscall.Kill(in.PID, syscall.SIGCONT)
	in = c.waitState(in.ID, "running", 10*time.Second)
	if in.Hung != nil {
		t.Fatalf("still marked hung: %+v", in.Hung)
	}
}

// TestProductDiesWithSupervisor: an instance never outlives its supervisor,
// and lcd349 stop cleans up after one that died.
func TestProductDiesWithSupervisor(t *testing.T) {
	c := newCLI(t)
	in := c.start()
	syscall.Kill(in.SupervisorPID, syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for alive(in.PID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(in.PID) {
		t.Fatalf("product %d still runs after its supervisor died", in.PID)
	}
	if got := c.info(in.ID).Status; got != "gone" {
		t.Fatalf("status %q, want gone", got)
	}
	c.must("stop", in.ID)
	if _, err := os.Stat(in.Dir); !os.IsNotExist(err) {
		t.Fatalf("%s still exists after stop", in.Dir)
	}
}

// TestForegroundRun: lcd349 run without --detach copies the serial output to
// the terminal, starts the viewer, and Ctrl-C stops everything.
func TestForegroundRun(t *testing.T) {
	c := newCLI(t)
	view := os.Getenv("LCD349_VIEW_BIN")
	if view == "" {
		view, _ = filepath.Abs(filepath.Join("..", "..", "viewer", "build", "lcd349-view"))
	}
	args := []string{"run", "--elf", inputsELF(t)}
	if _, err := os.Stat(view); err == nil {
		c.env = append(c.env, "LCD349_VIEW="+view, "SDL_VIDEODRIVER=dummy", "SDL_AUDIODRIVER=dummy")
	} else {
		t.Logf("no viewer at %s: running headless", view)
		args = append(args, "--headless")
		view = ""
	}
	cmd := c.cmd(args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a terminal's process group
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	errLines := readUntil(t, stderr, 15*time.Second, func(l []string) bool { return contains(l, "Ctrl-C stops it") })
	readUntil(t, stdout, 10*time.Second, func(l []string) bool { return contains(l, "inputs: ready") })
	id := ""
	for _, l := range errLines {
		if strings.HasPrefix(l, "lcd349: instance ") {
			id = strings.Fields(l)[2]
		}
	}
	in := c.info(id)
	if view != "" {
		in = c.waitFor(id, "the viewer", 10*time.Second, func(i inst) bool { return i.Viewer == "running" && i.ViewerPID > 0 })
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT) // Ctrl-C
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lcd349 run after Ctrl-C: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lcd349 run didn't stop after Ctrl-C")
	}
	time.Sleep(200 * time.Millisecond)
	if alive(in.PID) || (in.ViewerPID > 0 && alive(in.ViewerPID)) {
		t.Fatalf("product %d or viewer %d still runs", in.PID, in.ViewerPID)
	}
	if _, err := os.Stat(in.Dir); !os.IsNotExist(err) {
		t.Fatalf("%s still exists", in.Dir)
	}
}
