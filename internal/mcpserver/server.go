// Package mcpserver is `lcd349 mcp`: the CLI's features as MCP tools over
// stdio (requirement 7.4, design §MCP server). It confines paths to lcd349
// projects (7.5), stops the instances it started when the session ends
// (7.6), and caps every result at MaxOutput (KT5).
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
)

// MaxOutput caps a tool result's text (KT5).
const MaxOutput = 1 << 20

// Options configure a server.
type Options struct {
	// Root is the session's workspace (default: the working directory).
	// Relative paths resolve against it, and new_project writes only inside it.
	Root string
	// Exe is the lcd349 binary, which runs instance supervisors (default:
	// this executable).
	Exe string
	// KitPath is new_project's default kit_path (LCD349_KIT_PATH), for
	// development builds that have no release to pin.
	KitPath string
}

// Server is one MCP server.
type Server struct {
	o         Options
	mcp       *mcp.Server
	maxOutput int

	mu      sync.Mutex
	started map[string]string // instance id → directory, for instances this server started
	scale   *viewer.Scale     // the display's physical scale, once known
}

// New makes a server with every tool.
func New(o Options) (*Server, error) {
	if o.Root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Root = wd
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return nil, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	o.Root = root
	if o.Exe == "" {
		if o.Exe, err = os.Executable(); err != nil {
			return nil, err
		}
	}
	if o.KitPath == "" {
		o.KitPath = os.Getenv("LCD349_KIT_PATH")
	}
	s := &Server{o: o, maxOutput: MaxOutput, started: map[string]string{}}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "lcd349", Version: lcd349kit.Version}, &mcp.ServerOptions{
		Instructions: instructions,
		Capabilities: &mcp.ServerCapabilities{},
	})
	s.addTools()
	return s, nil
}

const instructions = `Tools for the Waveshare ESP32-S3-Touch-LCD-3.49: a 640x172 landscape capacitive touch bar, two buttons (BOOT, PWR), a speaker and a battery, emulated from the product's own sources. ` +
	`Workflow: device_info for the board's limits; new_project or an existing lcd349 project; build; run (headless); then screenshot, ui_tree, ui_lint, touch, press, wait and logs; run_scenario for the project's checks. ` +
	`Coordinates are landscape pixels (x 0-639, y 0-171; 7.5 px per mm). Instance arguments may be left out when exactly one instance is running. Instances this server starts stop when the session ends. ` +
	`build and run execute the project's own code: use them only on projects you trust.`

// Serve runs one session on t until it closes or ctx ends, then stops the
// instances this server started.
func (s *Server) Serve(ctx context.Context, t mcp.Transport) error {
	ss, err := s.Connect(ctx, t)
	if err != nil {
		return err
	}
	return ss.Wait(ctx)
}

// Session is one connected MCP session.
type Session struct {
	s  *Server
	ss *mcp.ServerSession
}

// Connect starts a session on t (connect the server before the client).
func (s *Server) Connect(ctx context.Context, t mcp.Transport) (*Session, error) {
	ss, err := s.mcp.Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}
	return &Session{s: s, ss: ss}, nil
}

// Wait waits for the session to end (or ctx), then stops the instances the
// server started (R7.6).
func (se *Session) Wait(ctx context.Context) error {
	defer se.s.StopAll()
	done := make(chan error, 1)
	go func() { done <- se.ss.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		se.ss.Close()
		<-done
		return ctx.Err()
	}
}

// StopAll stops the instances this server started (R7.6).
func (s *Server) StopAll() {
	s.mu.Lock()
	dirs := make([]string, 0, len(s.started))
	for _, d := range s.started {
		dirs = append(dirs, d)
	}
	s.started = map[string]string{}
	s.mu.Unlock()
	sort.Strings(dirs)
	var wg sync.WaitGroup
	for _, d := range dirs {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			instance.Stop(d)
		}(d)
	}
	wg.Wait()
}

func (s *Server) track(in *instance.Info) {
	s.mu.Lock()
	s.started[in.ID] = in.Dir
	s.mu.Unlock()
}

func (s *Server) untrack(id string) {
	s.mu.Lock()
	delete(s.started, id)
	s.mu.Unlock()
}

// ---- Paths (R7.5, KT3) ----

func (s *Server) abs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(s.o.Root, p)
}

// project accepts only a directory holding an ESP-IDF project that depends
// on lcd349, after resolving symlinks.
func (s *Server) project(p string) (*project.Project, error) {
	if p == "" {
		return nil, fmt.Errorf("project is required: the directory of an lcd349 project (relative to %s, or absolute)", s.o.Root)
	}
	proj, err := project.LoadKitProject(s.abs(p))
	if err != nil {
		return nil, fmt.Errorf("refused: %v. Tools act only on lcd349 projects (an ESP-IDF project whose idf_component.yml depends on lcd349); new_project makes one", err)
	}
	return proj, nil
}

// inRoot resolves a path that doesn't exist yet and requires it inside the
// workspace root.
func (s *Server) inRoot(p string) (string, error) {
	rootProj := &project.Project{Dir: s.o.Root}
	resolved, err := rootProj.Inside(s.abs(p))
	if err != nil {
		return "", fmt.Errorf("refused: %v; new projects go inside the workspace %s", err, s.o.Root)
	}
	return resolved, nil
}

// pick resolves an instance argument: an id, an instance directory in the
// instance roots (relative paths resolve against the workspace), or empty
// for the only running instance. instance.Find refuses any other
// directory, whatever it holds, so stop can't remove it.
func (s *Server) pick(ref string) (*instance.Info, error) {
	if ref == "" {
		return instance.Only()
	}
	if strings.ContainsRune(ref, '/') || ref == "." || ref == ".." {
		ref = s.abs(ref)
	}
	dir, err := instance.Find(ref)
	if err != nil {
		return nil, err
	}
	return instance.Read(dir)
}

// ---- Results ----

// jsonResult is a tool result of JSON text, capped at maxOutput.
func (s *Server) jsonResult(v any, isError bool) *mcp.CallToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		b = []byte(fmt.Sprintf(`{"error": %q}`, err.Error()))
	}
	text := string(b)
	if len(text) > s.maxOutput {
		text = text[:s.maxOutput] + fmt.Sprintf("\n… cut at the %s tool output limit", s.limitText())
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

func discard() io.Writer { return io.Discard }

func (s *Server) limitText() string {
	if s.maxOutput == MaxOutput {
		return "1 MB"
	}
	return fmt.Sprintf("%d-byte", s.maxOutput)
}
