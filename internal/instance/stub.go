package instance

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// The stand-in control socket: while the product isn't running (powered off,
// or crashed) the supervisor answers on control.sock itself, so the CLI, MCP
// server and viewer get a clear answer instead of a missing socket, and can
// power the device on again.

type rpcReq struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Landscape and panel sizes, for a dark screenshot.
const (
	landW, landH   = 640, 172
	panelW, panelH = 172, 640
)

type stub struct {
	ln      net.Listener
	mu      sync.Mutex
	conns   map[net.Conn]bool
	restart chan string // the boot reason to start with
	s       *supervisor
}

// serveStub answers on control.sock until a request powers the device on
// (returning the boot reason) or ctx ends (returning "").
func (s *supervisor) serveStub(ctx context.Context) string {
	path := filepath.Join(s.dir, "control.sock")
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		s.log.note("can't serve control.sock while the device is down: " + err.Error())
		<-ctx.Done()
		return ""
	}
	os.Chmod(path, 0o600)
	st := &stub{ln: ln, conns: map[net.Conn]bool{}, restart: make(chan string, 1), s: s}
	go st.accept()
	var reason string
	select {
	case reason = <-st.restart:
	case <-ctx.Done():
	}
	ln.Close()
	os.Remove(path)
	st.mu.Lock()
	for c := range st.conns {
		c.Close()
	}
	st.mu.Unlock()
	return reason
}

func (st *stub) accept() {
	for {
		c, err := st.ln.Accept()
		if err != nil {
			return
		}
		st.mu.Lock()
		st.conns[c] = true
		st.mu.Unlock()
		go st.serve(c)
	}
}

func (st *stub) serve(c net.Conn) {
	defer func() {
		st.mu.Lock()
		delete(st.conns, c)
		st.mu.Unlock()
		c.Close()
	}()
	r := bufio.NewReaderSize(c, 64<<10)
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			writeResp(c, nil, nil, &rpc.Error{Code: rpc.CodeInvalidRequest, Message: "line longer than 64 KB"})
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			continue
		}
		if err != nil {
			return
		}
		var req rpcReq
		if json.Unmarshal(bytes.TrimSpace(line), &req) != nil || req.Method == "" {
			writeResp(c, nil, nil, &rpc.Error{Code: rpc.CodeParse, Message: "not a JSON-RPC request"})
			continue
		}
		res, rerr, reason := st.handle(req.Method, req.Params)
		if req.ID != nil {
			writeResp(c, req.ID, res, rerr)
		}
		if reason != "" {
			select {
			case st.restart <- reason:
			default:
			}
			return
		}
	}
}

func writeResp(c net.Conn, id json.RawMessage, res any, e *rpc.Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		m["error"] = e
	} else {
		m["result"] = res
	}
	b, _ := json.Marshal(m)
	c.Write(append(b, '\n'))
}

// handle answers one request. A non-empty reason starts the product again.
func (st *stub) handle(method string, raw json.RawMessage) (any, *rpc.Error, string) {
	info := st.s.snapshot()
	var p struct {
		Action string `json:"action"`
		Button string `json:"button"`
		State  string `json:"state"`
		Raw    bool   `json:"raw"`
		Scale  int    `json:"scale"`
		Format string `json:"format"`
		Log    string `json:"log"`
	}
	json.Unmarshal(raw, &p)

	if info.State == StateCrashed {
		if method == "power" && (p.Action == "on" || p.Action == "reset") {
			return map[string]any{"action": p.Action, "boot_reason": "crash"}, nil, "crash"
		}
		return nil, exitedError(info), ""
	}

	// Powered off.
	off := &rpc.Error{Code: rpc.CodeUnavailable, Message: `the device is off: press PWR, or send power {"action": "on"}, to turn it on`}
	switch method {
	case "ping":
		return map[string]any{"pong": true, "power": "off"}, nil, ""
	case "info":
		return map[string]any{"power": "off", "kit_version": info.KitVersion, "instance_dir": info.Dir, "supervisor": true,
			"methods": []string{"ping", "info", "state.get", "power", "input.press", "screenshot"}}, nil, ""
	case "state.get":
		return map[string]any{"power": "off", "power_hold": false, "display": map[string]any{"dark": true, "backlight": 0}}, nil, ""
	case "power":
		if p.Action == "on" {
			return map[string]any{"action": "on"}, nil, "power-on"
		}
		return nil, off, ""
	case "input.press":
		if strings.EqualFold(p.Button, "pwr") && p.State != "up" {
			return map[string]any{"button": "PWR", "powered_on": true}, nil, "power-on"
		}
		return nil, off, ""
	case "screenshot":
		return darkShot(p.Raw, p.Scale, p.Format), nil, ""
	case "wait":
		if p.Log != "" {
			return st.s.waitLastBoot(p.Log)
		}
	}
	return nil, off, ""
}

func exitedError(info Info) *rpc.Error {
	how := "exited"
	var lines []string
	if e := info.LastExit; e != nil {
		if e.Signal != "" {
			how = "was killed by " + e.Signal
		} else {
			how = fmt.Sprintf("exited with code %d", e.Code)
		}
		lines = e.LastLines
	}
	data, _ := json.Marshal(map[string]any{"exit": info.LastExit, "last_lines": lines})
	return &rpc.Error{Code: rpc.CodeExited,
		Message: fmt.Sprintf(`instance_exited: the product %s; its last serial lines are in data and %s. Fix it and run again, or send power {"action": "reset"} to restart it`, how, filepath.Join(info.Dir, "serial.log")),
		Data:    data}
}

// darkShot is the screen of a device that is off: black.
func darkShot(raw bool, scale int, format string) map[string]any {
	if scale < 1 || scale > 4 {
		scale = 1
	}
	w, h := landW*scale, landH*scale
	if raw {
		w, h = panelW*scale, panelH*scale
	}
	res := map[string]any{"frame_seq": 0, "backlight": 0, "power": "off", "dark": true}
	if format == "rgb565be" {
		res["format"], res["width"], res["height"] = "rgb565be", panelW, panelH
		res["data"] = base64.StdEncoding.EncodeToString(make([]byte, panelW*panelH*2))
		return res
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	png.Encode(&b, img)
	res["format"], res["width"], res["height"] = "png", w, h
	res["data"] = base64.StdEncoding.EncodeToString(b.Bytes())
	return res
}

// waitLastBoot answers wait {log} for a device that is off: nothing new can
// be logged, so it searches the last boot's lines (after the supervisor's
// "boot N" note) and answers at once.
func (s *supervisor) waitLastBoot(pattern string) (any, *rpc.Error, string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "log isn't a valid regular expression: " + err.Error()}, ""
	}
	lines := s.log.last(CrashLines)
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "lcd349: boot ") {
			start = i + 1
		}
	}
	for _, l := range lines[start:] {
		if re.MatchString(l) {
			return map[string]any{"matched": true, "by": "log", "line": l, "power": "off"}, nil, ""
		}
	}
	tail := lines[start:]
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	return map[string]any{"matched": false, "power": "off", "tail": tail,
		"note": "the device is off, so only its last boot's lines were searched"}, nil, ""
}
