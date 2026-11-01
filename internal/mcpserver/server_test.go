package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var lcd349Bin string // runs the supervisors of instances the server starts

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "lcd349-mcp-bin-")
	if err != nil {
		panic(err)
	}
	lcd349Bin = filepath.Join(dir, "lcd349")
	if out, err := exec.Command("go", "build", "-o", lcd349Bin, "github.com/MaverickHQ/lcd349-kit/cmd/lcd349").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building lcd349: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type session struct {
	t    *testing.T
	srv  *Server
	cs   *mcp.ClientSession
	done chan error
	root string
}

// newSession starts a server on a fresh workspace with its own instance root,
// and connects a client over the in-memory transport.
func newSession(t *testing.T) *session {
	t.Helper()
	inst, err := os.MkdirTemp("/tmp", "l349m-") // short: socket paths
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LCD349_INSTANCES", inst)
	root := t.TempDir()
	srv, err := New(Options{Root: root, Exe: lcd349Bin})
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{t: t, srv: srv, done: make(chan error, 1), root: srv.o.Root}
	go func() { s.done <- ss.Wait(ctx) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	if s.cs, err = client.Connect(ctx, ct, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.close()
		os.RemoveAll(inst)
	})
	return s
}

// close ends the session and waits for the server to finish its cleanup.
func (s *session) close() {
	if s.cs == nil {
		return
	}
	s.cs.Close()
	s.cs = nil
	select {
	case <-s.done:
	case <-time.After(30 * time.Second):
		s.t.Error("the server didn't end the session")
	}
}

// call calls a tool; wantErr says whether IsError is expected.
func (s *session) call(name string, args map[string]any, wantErr bool) *mcp.CallToolResult {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: %v", name, err)
	}
	if res.IsError != wantErr {
		s.t.Fatalf("%s %v: IsError %v, want %v: %s", name, args, res.IsError, wantErr, text(res))
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func decode(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text(res)), v); err != nil {
		t.Fatalf("%v: %s", err, text(res))
	}
}

func images(res *mcp.CallToolResult) [][]byte {
	var out [][]byte
	for _, c := range res.Content {
		if im, ok := c.(*mcp.ImageContent); ok {
			out = append(out, im.Data)
		}
	}
	return out
}

func kitComponent(t *testing.T) string {
	abs, _ := filepath.Abs(filepath.Join("..", "..", "components", "lcd349"))
	return abs
}

// fixture makes an lcd349 project in the workspace whose emulator build is
// the prebuilt examples/inputs, so tools can run it without building.
func (s *session) fixture(name string) string {
	t := s.t
	t.Helper()
	elf := os.Getenv("LCD349_INPUTS_ELF")
	if elf == "" {
		elf = filepath.Join("..", "..", "examples", "inputs", "build-emu", "inputs.elf")
	}
	b, err := os.ReadFile(elf)
	if err != nil {
		t.Skipf("no emulator build of examples/inputs (%v); build it first (lcd349 build -C examples/inputs)", err)
	}
	dir := filepath.Join(s.root, name)
	files := map[string]string{
		"CMakeLists.txt":                     "project(" + name + ")\n",
		"main/idf_component.yml":             "dependencies:\n  lcd349:\n    override_path: " + kitComponent(t) + "\n",
		"build-emu/project_description.json": `{"target": "linux", "app_elf": "` + name + `.elf", "build_dir": "build-emu"}`,
	}
	for f, body := range files {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755)
		os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644)
	}
	if err := os.WriteFile(filepath.Join(dir, "build-emu", name+".elf"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func alive(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

func TestToolsListed(t *testing.T) {
	s := newSession(t)
	res, err := s.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
		if tl.Description == "" {
			t.Errorf("%s has no description", tl.Name)
		}
	}
	// Requirement 7.4's tools, plus power.
	for _, n := range []string{"device_info", "new_project", "build", "run", "stop", "screenshot", "ui_tree", "ui_lint",
		"press", "touch", "logs", "wait", "get_state", "set_state", "run_scenario", "record", "power"} {
		if !got[n] {
			t.Errorf("no %s tool", n)
		}
	}
	info := text(s.call("device_info", nil, false))
	if !strings.Contains(info, "7.5 px per mm") || !strings.Contains(info, "# What the emulator") {
		t.Errorf("device_info:\n%.400s", info)
	}
}

// TestPathsAreConfined: project tools take only lcd349 projects, new_project
// writes only inside the workspace, and symlinks can't escape (R7.5, KT3).
func TestPathsAreConfined(t *testing.T) {
	s := newSession(t)
	outside := t.TempDir()

	// new_project: inside the workspace yes; outside, or through a symlink out, no.
	res := s.call("new_project", map[string]any{"dir": "apps/first", "kit_path": kitComponent(t)}, false)
	var np struct {
		Dir   string   `json:"dir"`
		Files []string `json:"files"`
	}
	decode(t, res, &np)
	if np.Dir != filepath.Join(s.root, "apps", "first") || len(np.Files) < 8 {
		t.Fatalf("new_project: %+v", np)
	}
	if msg := text(s.call("new_project", map[string]any{"dir": filepath.Join(outside, "x"), "kit_path": kitComponent(t)}, true)); !strings.Contains(msg, "outside") {
		t.Errorf("new_project outside the workspace: %s", msg)
	}
	os.Symlink(outside, filepath.Join(s.root, "link"))
	if msg := text(s.call("new_project", map[string]any{"dir": "link/x", "kit_path": kitComponent(t)}, true)); !strings.Contains(msg, "outside") {
		t.Errorf("new_project through a symlink out: %s", msg)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("files were written outside the workspace: %v", ents)
	}

	// Project tools: not a project, a project without the kit, a symlink to them.
	os.WriteFile(filepath.Join(outside, "CMakeLists.txt"), []byte("project(plain)\n"), 0o644)
	for _, p := range []string{t.TempDir(), outside, filepath.Join(s.root, "link")} {
		for _, tool := range []string{"build", "run", "run_scenario"} {
			args := map[string]any{"project": p}
			if tool == "run_scenario" {
				args["file"] = "tests/a.scenario.yaml"
			}
			if msg := text(s.call(tool, args, true)); !strings.Contains(msg, "refused") {
				t.Errorf("%s on %s: %s", tool, p, msg)
			}
		}
	}

	// run_scenario: the file must be inside the project, symlinks evaluated.
	proj := filepath.Join(s.root, "apps", "first")
	scen := filepath.Join(outside, "evil.scenario.yaml")
	os.WriteFile(scen, []byte("steps:\n  - lint:\n"), 0o644)
	os.Symlink(scen, filepath.Join(proj, "tests", "evil.scenario.yaml"))
	for _, f := range []string{scen, "../../../x.scenario.yaml", "tests/evil.scenario.yaml"} {
		msg := text(s.call("run_scenario", map[string]any{"project": proj, "file": f, "build": false}, true))
		if !strings.Contains(msg, "outside the project") {
			t.Errorf("run_scenario file %s: %s", f, msg)
		}
	}
}

// TestInstanceArgumentsStayInsideTheRoots: an instance argument is an id or
// an instance directory in the instance roots, never another directory that
// holds an instance.json (stop on "./" removed the workspace).
func TestInstanceArgumentsStayInsideTheRoots(t *testing.T) {
	s := newSession(t)
	victim := filepath.Join(s.root, "victim")
	for _, d := range []string{s.root, victim} {
		os.MkdirAll(d, 0o700)
		if err := os.WriteFile(filepath.Join(d, "instance.json"), []byte(`{"id":"victim","supervisor_pid":1,"pid":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range []string{"./", ".", s.root, s.root + "/", victim, "victim/", "../" + filepath.Base(s.root)} {
		for _, tool := range []string{"stop", "logs"} {
			if msg := text(s.call(tool, map[string]any{"instance": ref}, true)); !strings.Contains(msg, "no such instance") {
				t.Errorf("%s %q: %s", tool, ref, msg)
			}
		}
	}
	for _, d := range []string{s.root, victim} {
		if _, err := os.Stat(filepath.Join(d, "instance.json")); err != nil {
			t.Fatalf("stop removed %s: %v", d, err)
		}
	}
}

// TestInstanceTools drives one instance through every instance tool.
func TestInstanceTools(t *testing.T) {
	s := newSession(t)
	proj := s.fixture("fixture")
	var run struct {
		Instance string `json:"instance"`
		Serial   string `json:"serial"`
		PID      int    `json:"pid"`
	}
	decode(t, s.call("run", map[string]any{"project": proj, "build": false, "clock": "2026-10-12T09:00:00Z", "battery_mv": 3800, "usb": false}, false), &run)
	if run.Instance != "fixture" || run.Serial == "" || !alive(run.PID) {
		t.Fatalf("run: %+v", run)
	}
	id := map[string]any{"instance": run.Instance}
	with := func(kv ...any) map[string]any {
		m := map[string]any{"instance": run.Instance}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	s.call("wait", with("log", "inputs: ready"), false)

	res := s.call("screenshot", id, false)
	imgs := images(res)
	if len(imgs) != 1 {
		t.Fatalf("screenshot: %d images", len(imgs))
	}
	img, err := png.Decode(bytes.NewReader(imgs[0]))
	if err != nil || img.Bounds().Dx() != 640 || img.Bounds().Dy() != 172 {
		t.Fatalf("screenshot image: %v %v", img.Bounds(), err)
	}
	var meta struct {
		FrameSeq int       `json:"frame_seq"`
		MM       []float64 `json:"mm"`
	}
	decode(t, res, &meta)
	if meta.FrameSeq == 0 || len(meta.MM) != 2 || meta.MM[0] != 85.3 {
		t.Fatalf("screenshot meta: %s", text(res))
	}
	if len(imgs[0]) > 64<<10 {
		t.Errorf("the screenshot is %d bytes: it should be compressed", len(imgs[0]))
	}

	// physical: resized to the window's real size here, or 1:1 with the reason.
	var phys struct {
		Physical string `json:"physical"`
		Width    int    `json:"width"`
	}
	decode(t, s.call("screenshot", with("physical", true), false), &phys)
	if phys.Physical == "" || (strings.Contains(phys.Physical, "sent at 1:1") != (phys.Width == 640)) {
		t.Fatalf("physical screenshot: %+v", phys)
	}
	t.Logf("physical: %d px wide, %s", phys.Width, phys.Physical)

	if tr := text(s.call("ui_tree", id, false)); !strings.Contains(tr, `"TAP`) {
		t.Fatalf("ui_tree: %.300s", tr)
	}
	var lint struct {
		Errors int `json:"errors"`
	}
	decode(t, s.call("ui_lint", id, false), &lint)
	if lint.Errors != 0 {
		t.Fatalf("ui_lint: %+v", lint)
	}

	// Input, then wait for its log line in a separate tool call.
	s.call("touch", with("tap", []int{320, 88}), false)
	s.call("wait", with("log", "tap count 1"), false)
	s.call("press", with("button", "boot", "ms", 150), false)
	s.call("wait", with("log", "gesture SHORT BOOT"), false)
	// A second wait after the same input still looks back to where that input
	// began, and no further. (The scenario runner's waits look back only to
	// their previous request, which eval runs 2, 4 and 5 tripped over.)
	s.call("wait", with("log", "button BOOT up"), false)
	s.call("wait", with("log", "tap count 1", "timeout_ms", 200), true)
	if msg := text(s.call("touch", with("tap", []int{1, 2}, "swipe", map[string]any{"from": []int{1, 2}, "to": []int{3, 4}}), true)); !strings.Contains(msg, "exactly one") {
		t.Errorf("two gestures: %s", msg)
	}
	if msg := text(s.call("press", with("button", "reset"), true)); !strings.Contains(msg, "boot, pwr or boot+pwr") {
		t.Errorf("press reset: %s", msg)
	}
	w := s.call("wait", with("text", "NOT ON SCREEN", "timeout_ms", 200), true)
	if !strings.Contains(text(w), "wait-timeout.png") {
		t.Errorf("wait timeout: %s", text(w))
	}

	var logs struct {
		Lines []struct {
			N    int    `json:"n"`
			Text string `json:"text"`
		} `json:"lines"`
		Next int `json:"next"`
	}
	decode(t, s.call("logs", with("grep", "tap count"), false), &logs)
	if len(logs.Lines) != 1 || logs.Next < 2 {
		t.Fatalf("logs: %+v", logs)
	}
	decode(t, s.call("logs", with("since", logs.Next), false), &logs)
	for _, l := range logs.Lines {
		if strings.Contains(l.Text, "tap count") {
			t.Fatalf("logs since next repeated an old line: %+v", logs)
		}
	}

	var st struct {
		Source  string `json:"source"`
		Battery struct {
			MV int `json:"mv"`
		} `json:"battery"`
	}
	decode(t, s.call("get_state", id, false), &st)
	if st.Battery.MV != 3800 || st.Source != "battery" {
		t.Fatalf("get_state: %+v", st)
	}
	decode(t, s.call("set_state", with("set", map[string]any{"battery": map[string]any{"mv": 3600}}), false), &st)
	if st.Battery.MV != 3600 {
		t.Fatalf("set_state: %+v", st)
	}

	var pw struct {
		State      string `json:"state"`
		BootReason string `json:"boot_reason"`
	}
	decode(t, s.call("power", with("action", "reset"), false), &pw)
	if pw.State != "running" || pw.BootReason != "software" {
		t.Fatalf("power reset: %+v", pw)
	}
	s.call("wait", with("log", "boot count 2"), false) // the cursor follows the restart

	// Record a tap: the GIF lands in the project's build-emu.
	s.call("record", with("action", "start", "max_s", 5), false)
	s.call("touch", with("tap", []int{320, 88}), false)
	s.call("wait", with("idle_ms", 300), false)
	var rec struct {
		GIF    string `json:"gif"`
		Frames int    `json:"frames"`
	}
	decode(t, s.call("record", with("action", "stop"), false), &rec)
	if rec.Frames == 0 || !strings.HasPrefix(rec.GIF, filepath.Join(proj, "build-emu", "recordings")) {
		t.Fatalf("record: %+v", rec)
	}
	if st, err := os.Stat(rec.GIF); err != nil || st.Size() == 0 {
		t.Fatalf("gif: %v", err)
	}

	// With one instance running, instance may be left out.
	s.call("get_state", nil, false)
	s.call("stop", id, false)
	if alive(run.PID) {
		t.Fatal("the product still runs after stop")
	}
}

// TestRunScenario: a missing golden fails with the screenshot as image
// content, then passes; failures carry the actual and diff images.
func TestRunScenario(t *testing.T) {
	s := newSession(t)
	proj := s.fixture("scn")
	os.MkdirAll(filepath.Join(proj, "tests"), 0o755)
	os.WriteFile(filepath.Join(proj, "tests", "home.scenario.yaml"), []byte(`steps:
  - wait: { log: "wifi disconnected", timeout_ms: 5000 }
  - wait: { text: "wifi disconnected", timeout_ms: 3000 }
  - wait: { idle_ms: 200 }
  - shot: goldens/home.png
  - lint:
`), 0o644)
	args := map[string]any{"project": proj, "file": "tests/home.scenario.yaml", "build": false}
	res := s.call("run_scenario", args, true)
	if !strings.Contains(text(res), "missing") {
		t.Fatalf("missing golden: %s", text(res))
	}
	var r struct {
		OK    bool `json:"ok"`
		Steps []struct {
			OK bool `json:"ok"`
		} `json:"steps"`
	}
	decode(t, s.call("run_scenario", args, false), &r)
	if !r.OK || len(r.Steps) != 5 {
		t.Fatalf("second run: %+v", r)
	}
	// Change the golden: the failure shows the actual screen and the diff.
	gold := filepath.Join(proj, "tests", "goldens", "home.png")
	b, _ := os.ReadFile(gold)
	img, _ := png.Decode(bytes.NewReader(b))
	var nb bytes.Buffer
	png.Encode(&nb, flip(img))
	os.WriteFile(gold, nb.Bytes(), 0o644)
	res = s.call("run_scenario", args, true)
	if n := len(images(res)); n != 2 || !strings.Contains(text(res), "home_diff.png") {
		t.Fatalf("changed golden: %d images\n%s", n, text(res))
	}
	if len(s.srv.started) != 0 {
		t.Fatalf("the scenario's instance is still tracked: %v", s.srv.started)
	}
}

// TestSessionEndStopsInstances: every instance the server started stops when
// the session closes (R7.6).
func TestSessionEndStopsInstances(t *testing.T) {
	s := newSession(t)
	proj := s.fixture("ends")
	var pids []int
	var dirs []string
	for i := 0; i < 2; i++ {
		var run struct {
			PID int    `json:"pid"`
			Dir string `json:"dir"`
		}
		decode(t, s.call("run", map[string]any{"project": proj, "build": false}, false), &run)
		pids, dirs = append(pids, run.PID), append(dirs, run.Dir)
	}
	s.close()
	for i, pid := range pids {
		if alive(pid) {
			t.Errorf("instance %d (pid %d) still runs after the session ended", i, pid)
		}
		if _, err := os.Stat(dirs[i]); !os.IsNotExist(err) {
			t.Errorf("%s still exists", dirs[i])
		}
	}
}

// TestOversizeTreeIsTruncated: over the output cap, ui_tree sheds levels and
// says how to narrow it (KT5); logs stay under it too.
func TestOversizeTreeIsTruncated(t *testing.T) {
	s := newSession(t)
	proj := s.fixture("big")
	var run struct {
		Instance string `json:"instance"`
	}
	decode(t, s.call("run", map[string]any{"project": proj, "build": false}, false), &run)
	s.call("wait", map[string]any{"log": "inputs: ready"}, false)
	full := text(s.call("ui_tree", nil, false))
	s.srv.maxOutput = len(full) - 1
	res := s.call("ui_tree", nil, false)
	var tr struct {
		Truncated bool   `json:"truncated"`
		Note      string `json:"note"`
	}
	decode(t, res, &tr)
	if !tr.Truncated || !strings.Contains(tr.Note, "Narrow it with path") || !strings.Contains(text(res), "children_omitted") ||
		len(text(res)) > s.srv.maxOutput {
		t.Fatalf("truncated tree (%d bytes, cap %d): %.500s", len(text(res)), s.srv.maxOutput, text(res))
	}
	// The log so far is a few hundred bytes: a cap just over the result's
	// own overhead leaves room for a couple of lines.
	s.srv.maxOutput = 1200
	var logs struct {
		Lines []any `json:"lines"`
		Next  int   `json:"next"`
		More  bool  `json:"more"`
	}
	res = s.call("logs", map[string]any{"since": 1}, false)
	decode(t, res, &logs)
	if len(text(res)) > 1200 || !logs.More || len(logs.Lines) == 0 || logs.Next != len(logs.Lines)+1 {
		t.Fatalf("logs under a 1200-byte cap: %d bytes, %d lines, next %d, more %v", len(text(res)), len(logs.Lines), logs.Next, logs.More)
	}
}

func TestResize(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, flip(nil))
	img, _ := png.Decode(&b)
	r := resize(img, 1.339)
	if r.Bounds().Dx() != 857 || r.Bounds().Dy() != 230 {
		t.Fatalf("resized to %v", r.Bounds())
	}
}

// TestRecordingsStayPrivate: a recording of an instance with no project
// goes to a directory only this user can use, under the same checks as the
// instance roots, and is never written through a planted file or symlink.
func TestRecordingsStayPrivate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir, err := recordingsDir("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Clean(tmp) || !strings.Contains(filepath.Base(dir), fmt.Sprint(os.Getuid())) {
		t.Errorf("recordings go to %s, want a per-user directory in %s", dir, tmp)
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("%s: %v, %v; want a 0700 directory", dir, st.Mode(), err)
	}
	// Planted as a symlink, it's refused.
	os.RemoveAll(dir)
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := recordingsDir(""); err == nil {
		t.Error("recordingsDir accepted a symlink")
	}
	// A project's recordings go into its build-emu.
	proj := t.TempDir()
	if got, err := recordingsDir(proj); err != nil || got != filepath.Join(proj, "build-emu", "recordings") {
		t.Errorf("recordingsDir(project) = %s, %v", got, err)
	}

	// framesToGIF never writes through a file planted at its name.
	frames := t.TempDir()
	var b bytes.Buffer
	png.Encode(&b, flip(nil))
	os.WriteFile(filepath.Join(frames, "frame-1-0.png"), b.Bytes(), 0o600)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	outDir := t.TempDir()
	planted := filepath.Join(outDir, "rec.gif")
	if err := os.Symlink(victim, planted); err != nil {
		t.Fatal(err)
	}
	_, got, err := framesToGIF(frames, outDir, "rec")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(outDir, "rec-2.gif") {
		t.Errorf("the recording went to %s, want rec-2.gif beside the planted name", got)
	}
	if body, _ := os.ReadFile(victim); string(body) != "keep" {
		t.Error("framesToGIF wrote through a planted symlink")
	}
	if st, err := os.Lstat(planted); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the planted symlink was replaced: %v", err)
	}
}
