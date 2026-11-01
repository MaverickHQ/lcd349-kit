package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
	"github.com/MaverickHQ/lcd349-kit/internal/scaffold"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
)

// ---- Inputs ----

type noInput struct{}

type instanceIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id from run or lcd349 list; optional when exactly one instance is running"`
}

type newProjectIn struct {
	Dir     string `json:"dir" jsonschema:"directory to create, inside the workspace; it must not exist or must be empty"`
	Name    string `json:"name,omitempty" jsonschema:"ESP-IDF project name: lower-case letters, digits and underscores (default: from dir)"`
	KitPath string `json:"kit_path,omitempty" jsonschema:"a local lcd349 kit's components/lcd349, used instead of pinning a release (needed before the kit's first release)"`
}

type projectIn struct {
	Project string `json:"project" jsonschema:"an lcd349 project directory (relative to the workspace, or absolute)"`
}

type runIn struct {
	Project   string `json:"project" jsonschema:"an lcd349 project directory"`
	Build     *bool  `json:"build,omitempty" jsonschema:"build first (default true; incremental, about a second when nothing changed)"`
	Window    bool   `json:"window,omitempty" jsonschema:"also open the window at physical size for a person to watch (default headless)"`
	Clock     string `json:"clock,omitempty" jsonschema:"start the clock at this time (RFC 3339 or Unix seconds), for repeatable screens"`
	Wipe      bool   `json:"wipe,omitempty" jsonschema:"start with empty storage (instances started here always get fresh storage; this also clears a --storage directory)"`
	BatteryMV int    `json:"battery_mv,omitempty" jsonschema:"battery cell millivolts at first boot, 2500-4500"`
	USB       *bool  `json:"usb,omitempty" jsonschema:"true: powered from USB; false: on battery (first boot)"`
}

type screenshotIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Raw      bool   `json:"raw,omitempty" jsonschema:"the panel-native portrait framebuffer (172x640) instead of the landscape screen"`
	Scale    int    `json:"scale,omitempty" jsonschema:"integer zoom 1-4"`
	Physical bool   `json:"physical,omitempty" jsonschema:"resize to the screen's real size on this computer's display, as the window shows it"`
}

type treeIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Path     string `json:"path,omitempty" jsonschema:"start at this object path from an earlier tree, such as screen/1:button"`
	Depth    int    `json:"depth,omitempty" jsonschema:"only this many levels (default all)"`
}

type pressIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Button   string `json:"button" jsonschema:"boot, pwr, or boot+pwr for a chord"`
	MS       int    `json:"ms,omitempty" jsonschema:"hold time in ms, 20-60000 (default 100); leave a margin over a hold threshold (1100 for 1 s)"`
}

type touchIn struct {
	Instance  string           `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Tap       *drive.Point     `json:"tap,omitempty" jsonschema:"tap at [x, y]"`
	LongPress *drive.Point     `json:"long_press,omitempty" jsonschema:"press and hold at [x, y] for ms (default 800)"`
	Swipe     *drive.Swipe     `json:"swipe,omitempty" jsonschema:"swipe {from: [x, y], to: [x, y]} over ms (default 300)"`
	Points    []drive.RawPoint `json:"points,omitempty" jsonschema:"raw samples {x, y, down}, one per 30 ms touch period"`
	MS        int              `json:"ms,omitempty" jsonschema:"long_press or swipe duration in ms"`
}

type logsIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Since    int    `json:"since,omitempty" jsonschema:"start at this line number (from an earlier next); default: the last lines"`
	Grep     string `json:"grep,omitempty" jsonschema:"only lines matching this regular expression"`
	Limit    int    `json:"limit,omitempty" jsonschema:"at most this many lines (default 200)"`
}

type waitIn struct {
	Instance  string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Log       string `json:"log,omitempty" jsonschema:"a serial line matching this extended regular expression (by default from the last press, touch, set_state or power on)"`
	Text      string `json:"text,omitempty" jsonschema:"this text visible on screen"`
	IdleMS    int    `json:"idle_ms,omitempty" jsonschema:"no new frame for this many ms (the screen has settled)"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"give up after this many ms (default 5000, at most 120000)"`
	Since     int64  `json:"since,omitempty" jsonschema:"match log lines from this serial line number (get_state serial_next)"`
}

type setStateIn struct {
	Instance string         `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Set      map[string]any `json:"set" jsonschema:"partial state: battery {mv}, source (usb or battery), board_rev (V1 or V2), clock (RFC 3339), wifi {networks: [{ssid, password, band 2.4 or 5, rssi}]}"`
}

type powerIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Action   string `json:"action" jsonschema:"on, off, reset, or download (reset with BOOT held)"`
}

type scenarioIn struct {
	Project       string `json:"project" jsonschema:"the lcd349 project the scenario belongs to"`
	File          string `json:"file" jsonschema:"the *.scenario.yaml file, inside the project (relative to the project or absolute)"`
	UpdateGoldens bool   `json:"update_goldens,omitempty" jsonschema:"write goldens that are missing or differ, and pass"`
	Build         *bool  `json:"build,omitempty" jsonschema:"build first (default true)"`
}

type recordIn struct {
	Instance string `json:"instance,omitempty" jsonschema:"instance id; optional when exactly one is running"`
	Action   string `json:"action" jsonschema:"start or stop; stop writes an animated GIF of the frames"`
	MaxS     int    `json:"max_s,omitempty" jsonschema:"start: stop by itself after this many seconds (default 10, at most 60)"`
}

func (s *Server) addTools() {
	tool := func(name, desc string) *mcp.Tool { return &mcp.Tool{Name: name, Description: desc} }
	mcp.AddTool(s.mcp, tool("device_info", "The board's facts, limits and design rules (screen size and density, touch, buttons, power, timing), and what the emulator doesn't model. Read it before designing a screen."), s.deviceInfo)
	mcp.AddTool(s.mcp, tool("new_project", "Create an lcd349 product: an ESP-IDF project with one screen, a scenario with its golden screenshot, and a CLAUDE.md. Returns the files written."), s.newProject)
	mcp.AddTool(s.mcp, tool("build", "Build a project for the emulator (into <project>/build-emu; the board build is untouched). Returns ok, or the errors with file, line and, for ESP-IDF components the emulator lacks, the kit API to use instead."), s.build)
	mcp.AddTool(s.mcp, tool("run", "Build and start an emulated instance of a project (headless unless window). Returns the instance id and its virtual serial port. It stops when this session ends."), s.run)
	mcp.AddTool(s.mcp, tool("stop", "Stop an instance."), s.stop)
	mcp.AddTool(s.mcp, tool("screenshot", "The instance's screen as a PNG image (640x172 landscape), with its frame number and backlight. Black when the display is dark."), s.screenshot)
	mcp.AddTool(s.mcp, tool("ui_tree", "The screen's LVGL object tree: class, position and size, text, font, colours, flags and tags for each object, and paths to address them."), s.uiTree)
	mcp.AddTool(s.mcp, tool("ui_lint", "Check the screen against the board's design rules (touch target size, text size, contrast, clipping, off-screen objects, kinetic scrolling, gradients). Each finding has the object path and a fix."), s.uiLint)
	mcp.AddTool(s.mcp, tool("press", "Press a button (boot, pwr) or both (boot+pwr) for ms milliseconds; answers after the release. Pressing pwr while the device is off turns it on."), s.press)
	mcp.AddTool(s.mcp, tool("touch", "Touch the screen: exactly one of tap, long_press, swipe or points, in landscape pixels (x 0-639, y 0-171). Refused while the display is dark: touch never wakes the board."), s.touch)
	mcp.AddTool(s.mcp, tool("logs", "The instance's serial output, numbered. Returns lines and next (pass it as since to read on)."), s.logs)
	mcp.AddTool(s.mcp, tool("wait", "Wait for a serial line (log), text on screen (text) or a settled screen (idle_ms). On a timeout it returns the last lines and the screen's path."), s.wait)
	mcp.AddTool(s.mcp, tool("get_state", "The device state: power, battery, display, Wi-Fi, clock, buttons, memory warnings, and serial_next."), s.getState)
	mcp.AddTool(s.mcp, tool("set_state", "Change the simulated world: battery voltage, USB or battery power, board revision, clock, and the Wi-Fi networks in range. Returns the new state."), s.setState)
	mcp.AddTool(s.mcp, tool("power", "Turn the device on or off, reset it, or reset it with BOOT held (download mode). A reset keeps storage."), s.power)
	mcp.AddTool(s.mcp, tool("run_scenario", "Run a *.scenario.yaml headless: steps until the first failure, with the golden, actual and diff images of a failing screenshot."), s.runScenario)
	mcp.AddTool(s.mcp, tool("record", "Record the screen: start, then stop to get an animated GIF of every frame in between."), s.record)
}

// ---- Handlers ----

func (s *Server) deviceInfo(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	text := fmt.Sprintf("lcd349-kit %s (ESP-IDF %s).\n\n", lcd349kit.Version, lcd349kit.IDFVersion)
	for _, f := range []string{"device.md", "fidelity.md"} {
		b, err := fs.ReadFile(lcd349kit.DeviceReference, "plugin/skills/lcd349/references/"+f)
		if err != nil {
			return nil, nil, err
		}
		text += strings.TrimPrefix(string(b), "<!-- Code generated by internal/devicegen from device/device.json. DO NOT EDIT. -->\n") + "\n"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
}

func (s *Server) newProject(ctx context.Context, _ *mcp.CallToolRequest, in newProjectIn) (*mcp.CallToolResult, any, error) {
	if in.Dir == "" {
		return nil, nil, fmt.Errorf("dir is required")
	}
	dir, err := s.inRoot(in.Dir)
	if err != nil {
		return nil, nil, err
	}
	kit := in.KitPath
	if kit == "" {
		kit = s.o.KitPath
	}
	if kit != "" {
		kit = s.abs(kit)
	}
	files, err := scaffold.New(scaffold.Options{Dir: dir, Name: in.Name, KitPath: kit})
	if err != nil {
		return nil, nil, err
	}
	name := in.Name
	if name == "" {
		name = scaffold.NameFrom(dir)
	}
	return s.jsonResult(map[string]any{"dir": dir, "name": name, "files": files,
		"next": "build, then run; its check is tests/app.scenario.yaml (run_scenario)"}, false), nil, nil
}

func (s *Server) buildProject(ctx context.Context, dir string) (*build.Result, error) {
	idf, err := build.Environment(ctx, false)
	if err != nil {
		return nil, err
	}
	return build.Build(ctx, idf, dir, nil)
}

func (s *Server) build(ctx context.Context, _ *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, any, error) {
	proj, err := s.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.buildProject(ctx, proj.Dir)
	if err != nil {
		return nil, nil, err
	}
	return s.jsonResult(res, !res.OK), nil, nil
}

func (s *Server) run(ctx context.Context, _ *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, any, error) {
	proj, err := s.project(in.Project)
	if err != nil {
		return nil, nil, err
	}
	var elf string
	if in.Build == nil || *in.Build {
		res, err := s.buildProject(ctx, proj.Dir)
		if err != nil {
			return nil, nil, err
		}
		if !res.OK {
			return s.jsonResult(map[string]any{"build": res, "started": false}, true), nil, nil
		}
		elf = res.ELF
	} else if elf, err = build.ELF(proj.Dir); err != nil {
		return nil, nil, err
	}
	o := instance.Options{Clock: in.Clock, Wipe: in.Wipe, BatteryMV: in.BatteryMV}
	if in.USB != nil {
		o.Source = map[bool]string{true: "usb", false: "battery"}[*in.USB]
	}
	if in.Window {
		if o.Viewer, err = viewer.Path(discard()); err != nil {
			return nil, nil, fmt.Errorf("no window: %v; run without window", err)
		}
	}
	info, err := instance.Prepare(instance.Config{Project: proj.Dir, ELF: elf, Options: o})
	if err != nil {
		return nil, nil, err
	}
	sup, err := instance.Spawn(info, s.o.Exe, true, false, nil)
	if err != nil {
		return nil, nil, err
	}
	s.track(info)
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err = instance.WaitReady(wctx, info.Dir, sup)
	if err != nil {
		s.untrack(info.ID)
		instance.Stop(info.Dir)
		return nil, nil, err
	}
	return s.jsonResult(map[string]any{"instance": info.ID, "state": info.State, "pid": info.PID, "serial": info.Serial,
		"dir": info.Dir, "window": in.Window}, false), nil, nil
}

func (s *Server) stop(ctx context.Context, _ *mcp.CallToolRequest, in instanceIn) (*mcp.CallToolResult, any, error) {
	info, err := s.pick(in.Instance)
	if err != nil {
		return nil, nil, err
	}
	if err := instance.Stop(info.Dir); err != nil {
		return nil, nil, err
	}
	s.untrack(info.ID)
	return s.jsonResult(map[string]any{"stopped": info.ID}, false), nil, nil
}

// conn connects to an instance for one tool call.
func (s *Server) conn(ctx context.Context, ref string) (*drive.Conn, error) {
	info, err := s.pick(ref)
	if err != nil {
		return nil, err
	}
	return drive.Connect(ctx, info)
}

// callJSON makes one request and returns its result as JSON text.
func (s *Server) callJSON(ctx context.Context, ref, method string, params any, mark bool) (*mcp.CallToolResult, any, error) {
	d, err := s.conn(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	if mark {
		d.MarkInput(ctx)
	}
	var raw json.RawMessage
	if err := d.Call(ctx, method, params, &raw); err != nil {
		return nil, nil, err
	}
	return s.jsonResult(raw, false), nil, nil
}

func (s *Server) uiLint(ctx context.Context, _ *mcp.CallToolRequest, in instanceIn) (*mcp.CallToolResult, any, error) {
	return s.callJSON(ctx, in.Instance, "ui.lint", map[string]any{}, false)
}

func (s *Server) press(ctx context.Context, _ *mcp.CallToolRequest, in pressIn) (*mcp.CallToolResult, any, error) {
	p, err := drive.PressParams(in.Button, in.MS)
	if err != nil {
		return nil, nil, err
	}
	return s.callJSON(ctx, in.Instance, "input.press", p, true)
}

func (s *Server) touch(ctx context.Context, _ *mcp.CallToolRequest, in touchIn) (*mcp.CallToolResult, any, error) {
	t := drive.Touch{Tap: in.Tap, LongPress: in.LongPress, Swipe: in.Swipe, Points: in.Points, MS: in.MS}
	if err := t.Validate(); err != nil {
		return nil, nil, err
	}
	d, err := s.conn(ctx, in.Instance)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	d.MarkInput(ctx)
	var res rpc.Touch
	if err := d.Call(ctx, "input.touch", t, &res); err != nil {
		return nil, nil, err
	}
	if !res.Delivered {
		return nil, nil, fmt.Errorf("the touch wasn't delivered: %s", res.Reason)
	}
	return s.jsonResult(res, false), nil, nil
}

func (s *Server) logs(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, any, error) {
	info, err := s.pick(in.Instance)
	if err != nil {
		return nil, nil, err
	}
	var re *regexp.Regexp
	if in.Grep != "" {
		if re, err = regexp.Compile(in.Grep); err != nil {
			return nil, nil, fmt.Errorf("grep: %v", err)
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	lines, next, err := instance.ReadLogs(info, in.Since)
	if err != nil {
		return nil, nil, err
	}
	var out []instance.LogLine
	for _, l := range lines {
		if re == nil || re.MatchString(l.Text) {
			out = append(out, l)
		}
	}
	more := false
	if in.Since > 0 && len(out) > limit {
		// Reading on from a cursor: the first lines, and next continues after them.
		next, out, more = out[limit].N, out[:limit], true
	} else if len(out) > limit {
		out = out[len(out)-limit:]
	}
	// Keep within the output cap, dropping from the end and moving next back.
	size := 0
	for i, l := range out {
		size += len(l.Text) + 24
		if size > s.maxOutput-1024 {
			next, out, more = l.N, out[:i], true
			break
		}
	}
	if out == nil {
		out = []instance.LogLine{}
	}
	return s.jsonResult(map[string]any{"lines": out, "next": next, "more": more}, false), nil, nil
}

func (s *Server) wait(ctx context.Context, _ *mcp.CallToolRequest, in waitIn) (*mcp.CallToolResult, any, error) {
	if in.Log == "" && in.Text == "" && in.IdleMS == 0 {
		return nil, nil, fmt.Errorf("give log, text or idle_ms")
	}
	timeout := in.TimeoutMS
	if timeout == 0 {
		timeout = 5000
	}
	p := map[string]any{"timeout_ms": timeout}
	if in.Log != "" {
		p["log"] = in.Log
	}
	if in.Text != "" {
		p["text"] = in.Text
	}
	if in.IdleMS > 0 {
		p["idle_ms"] = in.IdleMS
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond+30*time.Second)
	defer cancel()
	d, err := s.conn(ctx, in.Instance)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	since := in.Since
	if since == 0 && in.Log != "" {
		since = d.InputCursor()
	}
	if since > 0 {
		p["since"] = since
	}
	var w rpc.Wait
	if err := d.Call(ctx, "wait", p, &w); err != nil {
		return nil, nil, err
	}
	return s.jsonResult(w, !w.Matched), nil, nil
}

func (s *Server) getState(ctx context.Context, _ *mcp.CallToolRequest, in instanceIn) (*mcp.CallToolResult, any, error) {
	return s.callJSON(ctx, in.Instance, "state.get", map[string]any{}, false)
}

func (s *Server) setState(ctx context.Context, _ *mcp.CallToolRequest, in setStateIn) (*mcp.CallToolResult, any, error) {
	if len(in.Set) == 0 {
		return nil, nil, fmt.Errorf("set is required: the state to change")
	}
	return s.callJSON(ctx, in.Instance, "state.set", in.Set, true)
}

func (s *Server) power(ctx context.Context, _ *mcp.CallToolRequest, in powerIn) (*mcp.CallToolResult, any, error) {
	p := map[string]any{"action": in.Action}
	switch in.Action {
	case "on", "off", "reset":
	case "download":
		p = map[string]any{"action": "reset", "boot_held": true}
	default:
		return nil, nil, fmt.Errorf("action is on, off, reset or download")
	}
	info, err := s.pick(in.Instance)
	if err != nil {
		return nil, nil, err
	}
	d, err := drive.Connect(ctx, info)
	if err != nil {
		return nil, nil, err
	}
	var res map[string]any
	err = d.Call(ctx, "power", p, &res)
	d.Close()
	if err != nil {
		return nil, nil, err
	}
	if in.Action == "off" || res["already_on"] == true {
		return s.jsonResult(res, false), nil, nil
	}
	// Report once the new boot answers, so the next tool finds it ready.
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for wctx.Err() == nil {
		cur, err := instance.Read(info.Dir)
		if err == nil && cur.PID != info.PID && (cur.State == instance.StateRunning || cur.State == instance.StateDownload) {
			return s.jsonResult(map[string]any{"action": in.Action, "state": cur.State, "pid": cur.PID, "boot_reason": cur.BootReason}, false), nil, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil, nil, fmt.Errorf("the instance didn't come back within 30 s; lcd349 list shows its state")
}
