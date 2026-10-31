package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/pty"
)

// The test binary is also the fake idf.py: linked as idf.py on PATH, it
// records its arguments and succeeds, as `idf.py build flash` would.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "idf.py" {
		f, err := os.OpenFile(os.Getenv("FAKE_IDF_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
		f.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type rig struct {
	dir, project, idfLog string
	port                 *pty.Pty
	board                *bufio.Reader // what boardtest sends, as the board reads it
}

// newRig puts the fake idf.py on PATH, makes a project to flash and a pty
// standing in for the board's port.
func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{dir: t.TempDir()}
	bin := filepath.Join(r.dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(bin, "idf.py")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	r.idfLog = filepath.Join(r.dir, "idf.log")
	t.Setenv("FAKE_IDF_LOG", r.idfLog)
	t.Setenv("LCD349_BOARD_LOCK_HELD", "")
	r.project = filepath.Join(r.dir, "board_checks")
	if err := os.Mkdir(r.project, 0o755); err != nil {
		t.Fatal(err)
	}
	cm := "cmake_minimum_required(VERSION 3.16)\ninclude($ENV{IDF_PATH}/tools/cmake/project.cmake)\nproject(board_checks)\n"
	if err := os.WriteFile(filepath.Join(r.project, "CMakeLists.txt"), []byte(cm), 0o644); err != nil {
		t.Fatal(err)
	}
	if r.port, err = pty.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.port.Close() })
	r.board = bufio.NewReader(r.port.Master)
	return r
}

// say writes board output, as the board's USB console would.
func (r *rig) say(t *testing.T, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if _, err := io.WriteString(r.port.Master, l+"\r\n"); err != nil {
			t.Errorf("board write: %v", err)
		}
	}
}

// hear returns the next line boardtest sent the board.
func (r *rig) hear(t *testing.T) string {
	t.Helper()
	s, err := r.board.ReadString('\n')
	if err != nil {
		t.Errorf("board read: %v", err)
	}
	return strings.TrimRight(s, "\r\n")
}

func (r *rig) run(args ...string) (string, int) {
	var out bytes.Buffer
	base := []string{"-p", r.port.Slave, "-lock", filepath.Join(r.dir, "board.lock"), "-timeout", "20s"}
	code := run(append(base, args...), &out, &out)
	return out.String(), code
}

// fake boards, each in its own goroutine; got reports what the board heard.
func (r *rig) fakeBoard(t *testing.T, script func(got chan<- string)) chan string {
	got := make(chan string, 16)
	go func() {
		defer close(got)
		script(got)
	}()
	return got
}

func TestRunsAnotherProjectsChecks(t *testing.T) {
	r := newRig(t)
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, "ESP-ROM:esp32s3-20210327", `@R {"boot":{"reason":"power_on"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t,
			`@R {"run":{"tag":"pager","index":0,"stage":1,"stages":1,"name":"a"}}`,
			"main/test_pager.c:10:a:PASS",
			`@R {"run":{"tag":"pager","index":1,"stage":1,"stages":1,"name":"b"}}`,
			"main/test_pager.c:20:b:FAIL: Expected 1 Was 2",
			`@R {"run":{"tag":"pager","index":2,"stage":1,"stages":1,"name":"c"}}`,
			"main/test_pager.c:30:c:PASS",
			"-----------------------",
			"3 Tests 1 Failures 0 Ignored ",
			"FAIL",
			`@R {"done":true}`)
	})
	out, code := r.run("-C", r.project, "-only", "pager")

	if cmd := <-got; cmd != "@P only pager" {
		t.Errorf("the board was sent %q, want %q", cmd, "@P only pager")
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (one test failed)\n%s", code, out)
	}
	if !strings.Contains(out, "3 Tests 1 Failures 0 Ignored") || !strings.Contains(out, "b:FAIL: Expected 1 Was 2") {
		t.Errorf("the app's Unity summary and failure aren't reported:\n%s", out)
	}
	log, err := os.ReadFile(r.idfLog)
	if err != nil {
		t.Fatalf("idf.py never ran: %v", err)
	}
	if want := "-C " + r.project + " -p " + r.port.Slave + " build flash"; !strings.Contains(string(log), want) {
		t.Errorf("idf.py ran as %q, want it to build and flash the project: %q", log, want)
	}
	if strings.Contains(string(log), "test_apps/board") {
		t.Errorf("idf.py built the kit's own board app: %q", log)
	}
}

func writeWifi(t *testing.T, dir string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "test-wifi.toml")
	toml := "[[network]]\nssid = \"desk-net\"\npassword = \"pa\\\"ss-0rd!\"\n\n[[network]]\nssid = 'phone'\npassword = 'hotspot-pw'\n"
	if err := os.WriteFile(p, []byte(toml), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNeverPrintsTheWifiPassword(t *testing.T) {
	r := newRig(t)
	wifi := writeWifi(t, r.dir, 0o600)
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, `@R {"boot":{"reason":"power_on"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, `@R {"run":{"tag":"wifi","index":0,"stage":1,"stages":1,"name":"join"}}`, "@Q wifi")
		line := r.hear(t)
		got <- line
		r.say(t, "I (1234) wifi: echo "+line, "I (1235) wifi: password pa\"ss-0rd!", "@Q wifi 2")
		got <- r.hear(t)
		r.say(t, "main/test_wifi.c:9:join:PASS", "1 Tests 0 Failures 0 Ignored ", "OK", `@R {"done":true}`)
	})
	out, code := r.run("-only", "wifi", "-wifi", wifi)

	<-got
	if line := <-got; line != `@P wifi {"ssid":"desk-net","password":"pa\"ss-0rd!"}` {
		t.Errorf("the board was sent %q", line)
	}
	if line := <-got; line != `@P wifi {"ssid":"phone","password":"hotspot-pw"}` {
		t.Errorf("the second network was sent as %q", line)
	}
	if code != 0 {
		t.Errorf("exit %d, want 0\n%s", code, out)
	}
	for _, secret := range []string{`pa"ss-0rd!`, `pa\"ss-0rd!`, "hotspot-pw"} {
		if strings.Contains(out, secret) {
			t.Errorf("the output shows a password (%q):\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "1 Tests 0 Failures 0 Ignored") {
		t.Errorf("no summary:\n%s", out)
	}
}

func TestRefusesAWifiFileOthersCanRead(t *testing.T) {
	r := newRig(t)
	wifi := writeWifi(t, r.dir, 0o644)
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, `@R {"boot":{"reason":"power_on"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, `@R {"run":{"tag":"wifi","index":0,"stage":1,"stages":1,"name":"join"}}`, "@Q wifi")
	})
	out, code := r.run("-only", "wifi", "-wifi", wifi)
	<-got
	if code != 2 || !strings.Contains(out, "0600") {
		t.Errorf("exit %d, want 2 with a mode error\n%s", code, out)
	}
	if strings.Contains(out, "desk-net") || strings.Contains(out, "0rd") {
		t.Errorf("the refusal quotes the file:\n%s", out)
	}
}

func TestResumesAMultiStageTestAfterTheBoardRestarts(t *testing.T) {
	r := newRig(t)
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, `@R {"boot":{"reason":"power_on"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, "0 Tests 0 Failures 0 Ignored ", "OK",
			`@R {"run":{"tag":"kv","index":0,"stage":1,"stages":2,"name":"kv survives a reboot"}}`)
		time.Sleep(50 * time.Millisecond)
		r.say(t, "ESP-ROM:esp32s3-20210327", "rst:0xc (RTC_SW_CPU_RST)", `@R {"boot":{"reason":"software"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, `@R {"run":{"tag":"kv","index":0,"stage":2,"stages":2,"name":"kv survives a reboot"}}`,
			"main/test_kv.c:40:kv survives a reboot:PASS", "1 Tests 0 Failures 0 Ignored ", "OK", `@R {"done":true}`)
	})
	out, code := r.run("-only", "kv")
	if cmd := <-got; cmd != "@P only kv" {
		t.Errorf("first command %q", cmd)
	}
	if cmd := <-got; cmd != "@P resume kv 0 2" {
		t.Errorf("after the restart the board was sent %q, want %q", cmd, "@P resume kv 0 2")
	}
	if code != 0 || !strings.Contains(out, "1 test, 0 failures") {
		t.Errorf("exit %d, want 0 with one test counted\n%s", code, out)
	}
}

func TestWaitsForTheBoardLock(t *testing.T) {
	t.Setenv("LCD349_BOARD_LOCK_HELD", "")
	path := filepath.Join(t.TempDir(), "board.lock")
	var said bytes.Buffer
	logf := func(format string, a ...any) { fmt.Fprintf(&said, format+"\n", a...) }
	release, err := takeLock(path, "lane power", logf)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan func(), 1)
	go func() {
		r, err := takeLock(path, "lane io", func(string, ...any) {})
		if err != nil {
			t.Error(err)
		}
		got <- r
	}()
	select {
	case <-got:
		t.Fatal("a second run took the board lock while the first held it")
	case <-time.After(500 * time.Millisecond):
	}
	if who, _ := os.ReadFile(path); !strings.Contains(string(who), "lane power") {
		t.Errorf("the lock names %q, want its holder", who)
	}
	release()
	select {
	case r := <-got:
		defer r()
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting run never got the lock")
	}
	if who, _ := os.ReadFile(path); !strings.Contains(string(who), "lane io") {
		t.Errorf("the lock names %q, want the new holder", who)
	}
}

// TestRunsAGuideToItsEnd: -guide runs an app's on-device guide (the guided
// checkpoint): it sends "@P guide" until the board answers, answers the
// guide's requests, asks again after the board resets, logs every line
// (masked) to -log, and exits by the guide's summary.
func TestRunsAGuideToItsEnd(t *testing.T) {
	r := newRig(t)
	wifi := writeWifi(t, r.dir, 0o600)
	logPath := filepath.Join(r.dir, "out", "guide.log")
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, "I (300) inputs: ready")
		got <- r.hear(t) // the board isn't listening yet: ignored
		got <- r.hear(t)
		r.say(t, `@R {"guide":{"start":"new","at":"corners","index":1,"of":2}}`, "@Q ping")
		got <- r.hear(t)
		r.say(t, "@P pong", `@R {"guide":{"step":"corners","index":1,"of":2,"result":"PASS"}}`)
		// The board restarts on the same link (the emulator powered back on,
		// or a reset whose ROM banner was missed): the kit's start-up line
		// says the app is there to ask again.
		r.say(t, "I (1150) lcd349: lcd349 0.1.0 up (device.json 565466179df2), status 0x000", "I (300) inputs: ready")
		got <- r.hear(t)
		r.say(t, `@R {"guide":{"start":"resumed","at":"wifi","index":2,"of":2}}`, "@Q wifi")
		got <- r.hear(t)
		r.say(t, "I (9000) inputs: wifi connected  desk-net",
			`@R {"guide":{"step":"wifi","index":2,"of":2,"result":"FAIL","rssi":0}}`,
			`@R {"guide":{"summary":{"step":"corners","index":1,"of":2,"result":"PASS"}}}`,
			`@R {"guide":{"summary":{"step":"wifi","index":2,"of":2,"result":"FAIL","rssi":0}}}`,
			`@R {"guide":{"done":true,"passed":1,"failed":1,"of":2}}`)
	})
	out, code := r.run("-guide", "-C", r.project, "-wifi", wifi, "-log", logPath)

	wants := []string{"@P guide", "@P guide", "@P ping", "@P guide", `@P wifi {"ssid":"desk-net","password":"pa\"ss-0rd!"}`}
	for i, want := range wants {
		select {
		case line := <-got:
			if line != want {
				t.Errorf("line %d to the board: %q, want %q", i+1, line, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("exit %d, and the board heard %d lines, want %d\n%s", code, i, len(wants), out)
		}
	}
	if code != 1 {
		t.Errorf("exit %d, want 1 (a step failed)\n%s", code, out)
	}
	if !strings.Contains(out, "wifi: FAIL") || !strings.Contains(out, "1 of 2 steps passed") {
		t.Errorf("the verdict doesn't name the failed step:\n%s", out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no log: %v", err)
	}
	if !strings.Contains(string(log), `@R {"guide":{"done":true,"passed":1,"failed":1,"of":2}}`) || !strings.Contains(string(log), "1 of 2 steps passed") {
		t.Errorf("the log lacks the board's lines or the verdict:\n%s", log)
	}
	for _, secret := range []string{"desk-net", `pa"ss-0rd!`, `pa\"ss-0rd!`} {
		if strings.Contains(out, secret) || strings.Contains(string(log), secret) {
			t.Errorf("the output or the log shows %q:\n%s", secret, log)
		}
	}
}

// TestStartsTheGuideAtAStep: -from runs the guide from a named step (to
// repeat one), and a step the board doesn't know ends the run.
func TestStartsTheGuideAtAStep(t *testing.T) {
	r := newRig(t)
	got := r.fakeBoard(t, func(got chan<- string) {
		got <- r.hear(t)
		r.say(t, `@R {"guide":{"error":"no such step"}}`)
	})
	out, code := r.run("-guide", "-C", r.project, "-no-flash", "-from", "wifii")
	select {
	case line := <-got:
		if line != "@P guide from wifii" {
			t.Errorf("the board heard %q, want %q", line, "@P guide from wifii")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("exit %d, and the board heard nothing\n%s", code, out)
	}
	if code != 2 || !strings.Contains(out, "no such step") {
		t.Errorf("exit %d, want 2 with the board's error\n%s", code, out)
	}
}

func TestCountsARestartInsideATestAsAFailure(t *testing.T) {
	r := newRig(t)
	got := r.fakeBoard(t, func(got chan<- string) {
		r.say(t, `@R {"boot":{"reason":"power_on"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, `@R {"run":{"tag":"audio","index":0,"stage":1,"stages":1,"name":"tone"}}`)
		r.say(t, "Guru Meditation Error: Core  0 panic'ed", `@R {"boot":{"reason":"crash"}}`, "@Q cmd")
		got <- r.hear(t)
		r.say(t, `@R {"run":{"tag":"audio","index":1,"stage":1,"stages":1,"name":"stop"}}`,
			"main/test_audio.c:50:stop:PASS", "1 Tests 0 Failures 0 Ignored ", "OK", `@R {"done":true}`)
	})
	out, code := r.run("-only", "audio")
	<-got
	if cmd := <-got; cmd != "@P resume audio 1 1" {
		t.Errorf("after the crash the board was sent %q, want the next test", cmd)
	}
	if code != 1 || !strings.Contains(out, "2 tests, 1 failure") || !strings.Contains(out, "tone") {
		t.Errorf("exit %d, want 1 with the crashed test failed\n%s", code, out)
	}
}
