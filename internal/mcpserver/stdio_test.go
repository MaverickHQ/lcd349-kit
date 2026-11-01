package mcpserver

import (
	"bufio"
	"context"
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

	"github.com/MaverickHQ/lcd349-kit/internal/build"
)

// stdioServer is the real `lcd349 mcp` process, spoken to over its stdin and stdout.
type stdioServer struct {
	t     *testing.T
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Scanner
	id    int
	inst  string
	debug strings.Builder
}

func startStdio(t *testing.T, root string) *stdioServer {
	t.Helper()
	inst, err := os.MkdirTemp("/tmp", "l349s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(inst) })
	s := &stdioServer{t: t, inst: inst}
	s.cmd = exec.Command(lcd349Bin, "mcp", "--root", root)
	s.cmd.Env = append(os.Environ(), "LCD349_INSTANCES="+inst)
	s.in, _ = s.cmd.StdinPipe()
	stdout, _ := s.cmd.StdoutPipe()
	s.cmd.Stderr = &s.debug
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.cmd.Process.Kill(); s.cmd.Wait() })
	s.out = bufio.NewScanner(stdout)
	s.out.Buffer(make([]byte, 4<<20), 4<<20)
	s.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "t", "version": "1"}})
	fmt.Fprintln(s.in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return s
}

func (s *stdioServer) request(method string, params any) json.RawMessage {
	s.t.Helper()
	s.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	fmt.Fprintln(s.in, string(b))
	for s.out.Scan() {
		var m struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(s.out.Bytes(), &m) == nil && m.ID == s.id {
			if m.Error != nil {
				s.t.Fatalf("%s: %s", method, m.Error)
			}
			return m.Result
		}
	}
	s.t.Fatalf("%s: the server closed stdout (stderr: %s)", method, s.debug.String())
	return nil
}

// runInstance calls the run tool and returns the instance's product pid.
func (s *stdioServer) runInstance(proj string) int {
	s.t.Helper()
	res := s.request("tools/call", map[string]any{"name": "run", "arguments": map[string]any{"project": proj, "build": false}})
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(res, &r)
	var run struct {
		PID int `json:"pid"`
	}
	if r.IsError || len(r.Content) == 0 || json.Unmarshal([]byte(r.Content[0].Text), &run) != nil || run.PID == 0 {
		s.t.Fatalf("run: %s", res)
	}
	return run.PID
}

func waitDead(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		t.Fatalf("%s: product %d still runs", what, pid)
	}
}

// TestStdioSessionEnd: the plugin's real path. Closing stdin ends the
// session and stops the instances (R7.6); a server killed outright still
// takes its instances with it, through the supervisors' tie.
func TestStdioSessionEnd(t *testing.T) {
	ws := newSession(t) // for the fixture project's workspace
	proj := ws.fixture("stdio")

	s := startStdio(t, ws.root)
	pid := s.runInstance(proj)
	s.in.Close()
	waitDead(t, pid, "after stdin closed")
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("lcd349 mcp exited with %v: %s", err, s.debug.String())
	}

	s = startStdio(t, ws.root)
	pid = s.runInstance(proj)
	s.cmd.Process.Signal(syscall.SIGKILL)
	waitDead(t, pid, "after the server was killed")
	if ents, _ := os.ReadDir(s.inst); len(ents) != 0 {
		t.Fatalf("instance directories left: %v", ents)
	}
}

// TestBuildTool: build errors come back as data, with the kit API that
// replaces a component the emulator lacks.
func TestBuildTool(t *testing.T) {
	if _, err := build.Environment(context.Background(), false); err != nil {
		t.Skipf("ESP-IDF isn't set up here: %v", err)
	}
	s := newSession(t)
	dir := filepath.Join(s.root, "uses_lcd")
	for f, body := range map[string]string{
		"CMakeLists.txt":         "cmake_minimum_required(VERSION 3.16)\nset(COMPONENTS main)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(uses_lcd)\n",
		"main/CMakeLists.txt":    "idf_component_register(SRCS \"main.c\" REQUIRES esp_lcd)\n",
		"main/main.c":            "void app_main(void) {}\n",
		"main/idf_component.yml": "dependencies:\n  lcd349:\n    override_path: " + kitComponent(t) + "\n",
	} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
	}
	var res struct {
		OK     bool `json:"ok"`
		Errors []struct {
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"errors"`
	}
	decode(t, s.call("build", map[string]any{"project": "uses_lcd"}, true), &res)
	if res.OK || len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Hint, "lcd349_display()") {
		t.Fatalf("build: %+v", res)
	}
}

// TestStdioAnswersAfterStdinCloses: a client that sends its requests and
// closes stdin at once (printf ... | lcd349 mcp) gets every answer before
// the server exits, promptly, with or without a newline after the last
// request. go-sdk on its own writes nothing once it has read EOF.
func TestStdioAnswersAfterStdinCloses(t *testing.T) {
	for _, end := range []string{"\n", ""} {
		t.Run(fmt.Sprintf("ending %q", end), func(t *testing.T) { stdioAnswersAfterStdinCloses(t, end) })
	}
}

func stdioAnswersAfterStdinCloses(t *testing.T, end string) {
	inst, err := os.MkdirTemp("/tmp", "l349s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(inst) })
	cmd := exec.Command(lcd349Bin, "mcp", "--root", t.TempDir())
	cmd.Env = append(os.Environ(), "LCD349_INSTANCES="+inst)
	cmd.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"three","method":"tools/call","params":{"name":"device_info","arguments":{}}}`,
	}, "\n") + end)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lcd349 mcp: %v\n%s", err, stderr.String())
		}
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatal("lcd349 mcp didn't exit after stdin closed")
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("lcd349 mcp took %s to exit after stdin closed", d)
	}
	answered := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(line), &m) == nil && m.Result != nil {
			answered[string(m.ID)] = true
		}
	}
	for _, id := range []string{`1`, `2`, `"three"`} {
		if !answered[id] {
			t.Errorf("no answer to request %s; stdout:\n%s\nstderr:\n%s", id, stdout.String(), stderr.String())
		}
	}
}

// TestStdioLastRequestWithoutNewline: a last request with no newline after
// it, answered before stdin closes, doesn't hold the exit back: the server
// exits promptly once stdin closes.
func TestStdioLastRequestWithoutNewline(t *testing.T) {
	inst, err := os.MkdirTemp("/tmp", "l349s-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(inst) })
	cmd := exec.Command(lcd349Bin, "mcp", "--root", t.TempDir())
	cmd.Env = append(os.Environ(), "LCD349_INSTANCES="+inst)
	in, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	io.WriteString(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	out := bufio.NewScanner(stdout)
	for answered := 0; answered < 2; {
		if !out.Scan() {
			t.Fatalf("stdout closed before both answers: %s", stderr.String())
		}
		if strings.Contains(out.Text(), `"result"`) {
			answered++
		}
	}
	in.Close()
	start := time.Now()
	done := make(chan error, 1)
	go func() { io.Copy(io.Discard, stdout); done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lcd349 mcp: %v\n%s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("lcd349 mcp didn't exit after stdin closed")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("lcd349 mcp took %s to exit after stdin closed", d)
	}
}
