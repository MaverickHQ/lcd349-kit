// Package clitest runs the lcd349 binary end to end against
// examples/inputs built for the linux target (tasks 6.1-6.5). Build the
// example first, or the tests skip; LCD349_INPUTS_ELF overrides its path.
package clitest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var lcd349Bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lcd349-bin-")
	if err != nil {
		panic(err)
	}
	lcd349Bin = filepath.Join(dir, "lcd349")
	build := exec.Command("go", "build", "-o", lcd349Bin, "github.com/MaverickHQ/lcd349-kit/cmd/lcd349")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building lcd349: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func inputsELF(t *testing.T) string {
	t.Helper()
	p := os.Getenv("LCD349_INPUTS_ELF")
	if p == "" {
		p = filepath.Join("..", "..", "examples", "inputs", "build-emu", "inputs.elf")
	}
	abs, _ := filepath.Abs(p)
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("no emulator build of examples/inputs at %s; build it first (lcd349 build -C examples/inputs)", abs)
	}
	return abs
}

// cli runs lcd349 with its own instance root, and stops everything at the end.
type cli struct {
	t    *testing.T
	root string
	env  []string
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "l349c-") // short: socket paths
	if err != nil {
		t.Fatal(err)
	}
	c := &cli{t: t, root: root}
	c.env = append(os.Environ(), "LCD349_INSTANCES="+filepath.Join(root, "i"))
	t.Cleanup(func() {
		c.run("stop", "--all")
		os.RemoveAll(root)
	})
	return c
}

func (c *cli) cmd(args ...string) *exec.Cmd {
	cmd := exec.Command(lcd349Bin, args...)
	cmd.Env = c.env
	return cmd
}

func (c *cli) run(args ...string) (stdout, stderr string, code int) {
	var o, e bytes.Buffer
	cmd := c.cmd(args...)
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code = 0
	if err != nil {
		code = -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
	}
	return o.String(), e.String(), code
}

func (c *cli) must(args ...string) string {
	c.t.Helper()
	out, errOut, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("lcd349 %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

type exitRec struct {
	Code      int      `json:"code"`
	Signal    string   `json:"signal"`
	Meaning   string   `json:"meaning"`
	LastLines []string `json:"last_lines"`
}

type inst struct {
	ID            string   `json:"id"`
	Dir           string   `json:"dir"`
	Serial        string   `json:"serial"`
	PID           int      `json:"pid"`
	SupervisorPID int      `json:"supervisor_pid"`
	State         string   `json:"state"`
	Status        string   `json:"status"`
	Boots         int      `json:"boots"`
	BootReason    string   `json:"boot_reason"`
	LastExit      *exitRec `json:"last_exit"`
	Hung          *struct {
		Sample      string `json:"sample"`
		SampleError string `json:"sample_error"`
	} `json:"hung"`
	Viewer    string `json:"viewer"`
	ViewerPID int    `json:"viewer_pid"`
}

// start runs examples/inputs detached and headless.
func (c *cli) start(extra ...string) inst {
	c.t.Helper()
	args := append([]string{"run", "--elf", inputsELF(c.t), "--headless", "--detach", "--json"}, extra...)
	var r struct {
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal([]byte(c.must(args...)), &r); err != nil {
		c.t.Fatal(err)
	}
	return c.info(r.Instance)
}

func (c *cli) list() []inst {
	c.t.Helper()
	var all []inst
	if err := json.Unmarshal([]byte(c.must("list", "--json")), &all); err != nil {
		c.t.Fatal(err)
	}
	return all
}

func (c *cli) info(id string) inst {
	c.t.Helper()
	for _, in := range c.list() {
		if in.ID == id {
			return in
		}
	}
	c.t.Fatalf("instance %s isn't listed", id)
	return inst{}
}

// waitFor polls the instance until ok holds.
func (c *cli) waitFor(id, what string, limit time.Duration, ok func(inst) bool) inst {
	c.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		in := c.info(id)
		if ok(in) {
			return in
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("instance %s: %s didn't happen within %v (state %s)", id, what, limit, in.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *cli) waitState(id, state string, limit time.Duration) inst {
	c.t.Helper()
	return c.waitFor(id, "state "+state, limit, func(in inst) bool { return in.State == state })
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// port opens the instance's serial port as a host tool would.
func port(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// readUntil reads lines from r until want has matched count lines or the time is up.
func readUntil(t *testing.T, r io.Reader, limit time.Duration, done func(lines []string) bool) []string {
	t.Helper()
	ch := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			ch <- sc.Text()
		}
		close(ch)
	}()
	var lines []string
	deadline := time.After(limit)
	for !done(lines) {
		select {
		case l, ok := <-ch:
			if !ok {
				return lines
			}
			lines = append(lines, l)
		case <-deadline:
			t.Fatalf("timed out after %v; read:\n%s", limit, strings.Join(lines, "\n"))
		}
	}
	return lines
}

func count(lines []string, s string) int {
	n := 0
	for _, l := range lines {
		if l == s {
			n++
		}
	}
	return n
}

func contains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}
