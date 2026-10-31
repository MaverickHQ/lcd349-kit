package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Dir is the emulator build directory inside a project (R7.2): its own
// sdkconfig, so the board build in build/ is never disturbed.
const Dir = "build-emu"

// LogName is the raw build log, inside Dir.
const LogName = "lcd349-build.log"

// Result is the outcome of one emulator build.
type Result struct {
	OK          bool          `json:"ok"`
	ELF         string        `json:"elf,omitempty"`
	Diagnostics []Diagnostic  `json:"errors,omitempty"`
	Log         string        `json:"log"`
	Configured  bool          `json:"configured"` // set-target ran (the first build)
	Duration    time.Duration `json:"-"`
	Seconds     float64       `json:"seconds"`
}

// Diagnostic is one error found in the build output.
type Diagnostic struct {
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"` // the kit API that replaces an unsupported component
}

func (d Diagnostic) String() string {
	var b strings.Builder
	if d.File != "" {
		b.WriteString(d.File)
		if d.Line > 0 {
			fmt.Fprintf(&b, ":%d", d.Line)
			if d.Column > 0 {
				fmt.Fprintf(&b, ":%d", d.Column)
			}
		}
		b.WriteString(": ")
	}
	b.WriteString(d.Message)
	if d.Hint != "" {
		b.WriteString("\n  hint: " + d.Hint)
	}
	return b.String()
}

// CheckProject returns the absolute project directory if dir holds an
// ESP-IDF project (a CMakeLists.txt calling project()).
func CheckProject(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(abs, "CMakeLists.txt"))
	if err != nil || !regexp.MustCompile(`(?m)^\s*project\(`).Match(b) {
		return "", fmt.Errorf("%s isn't an ESP-IDF project (no CMakeLists.txt with project()); -C names the project directory", abs)
	}
	return abs, nil
}

// Build builds project's emulator target: `idf.py --preview set-target linux`
// the first time, then `idf.py build`, each with a fixed argument list and no
// shell (R13.2). Output goes to build-emu/lcd349-build.log and, if out isn't
// nil, to out as it comes.
func Build(ctx context.Context, idf *IDF, project string, out io.Writer) (*Result, error) {
	start := time.Now()
	bdir := filepath.Join(project, Dir)
	res := &Result{Log: filepath.Join(bdir, LogName)}
	var all bytes.Buffer
	base := []string{filepath.Join(idf.Path, "tools", "idf.py"), "-C", project, "-B", bdir,
		"-D", "SDKCONFIG=" + filepath.Join(bdir, "sdkconfig"), "--preview"}
	run := func(w io.Writer, step ...string) error {
		if out != nil {
			w = io.MultiWriter(w, out)
		}
		cmd := exec.CommandContext(ctx, idf.Python, append(append([]string{}, base...), step...)...)
		cmd.Dir = project
		cmd.Env = idf.Env
		cmd.Stdout, cmd.Stderr = w, w
		return cmd.Run()
	}
	fail := func(step string, err error) (*Result, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		res.Diagnostics = Parse(all.String(), project)
		if len(res.Diagnostics) == 0 {
			res.Diagnostics = []Diagnostic{{Message: fmt.Sprintf("idf.py %s failed (%v) with no error the parser recognised; see the log", step, err)}}
		}
		res.Duration = time.Since(start)
		res.Seconds = res.Duration.Seconds()
		return res, nil
	}

	if !configuredForLinux(bdir) {
		// set-target runs fullclean, which refuses a directory holding files
		// that isn't a CMake build directory yet: so no log file in it until
		// configuring is done (a failed first attempt may have left one).
		os.Remove(res.Log)
		os.Remove(bdir) // only if empty
		res.Configured = true
		err := run(&all, "set-target", "linux")
		if merr := os.MkdirAll(bdir, 0o755); merr != nil {
			return nil, merr
		}
		if werr := os.WriteFile(res.Log, all.Bytes(), 0o644); werr != nil {
			return nil, werr
		}
		if err != nil {
			return fail("set-target", err)
		}
	}
	logf, err := os.OpenFile(res.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	if !res.Configured {
		logf.Truncate(0)
	}
	if err := run(io.MultiWriter(logf, &all), "build"); err != nil {
		return fail("build", err)
	}
	res.OK = true
	res.ELF, err = ELF(project)
	res.Duration = time.Since(start)
	res.Seconds = res.Duration.Seconds()
	return res, err
}

func configuredForLinux(bdir string) bool {
	b, err := os.ReadFile(filepath.Join(bdir, "CMakeCache.txt"))
	return err == nil && bytes.Contains(b, []byte("IDF_TARGET:STRING=linux"))
}

// ELF returns the emulator program of a built project, from build-emu's
// project_description.json.
func ELF(project string) (string, error) {
	b, err := os.ReadFile(filepath.Join(project, Dir, "project_description.json"))
	if err != nil {
		return "", fmt.Errorf("%s has no emulator build yet; run lcd349 build -C %s", project, project)
	}
	var d struct {
		Target string `json:"target"`
		AppELF string `json:"app_elf"`
		Build  string `json:"build_dir"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return "", err
	}
	if d.Target != "linux" {
		return "", fmt.Errorf("%s/%s targets %s, not linux; remove it and rebuild", project, Dir, d.Target)
	}
	elf := filepath.Join(project, Dir, d.AppELF)
	if _, err := os.Stat(elf); err != nil {
		return "", errors.New(elf + " is missing; rebuild")
	}
	return elf, nil
}

var (
	ccRe       = regexp.MustCompile(`^(.+?):(\d+):(\d+): (?:fatal error|error): (.*)$`)
	cmakeRe    = regexp.MustCompile(`^CMake Error at (.+?):(\d+) \((.+)\):$`)
	undefGNU   = regexp.MustCompile("undefined reference to [`'](.+)'")
	undefApple = regexp.MustCompile(`^\s+"(.+)", referenced from:$`)
	resolveRe  = regexp.MustCompile(`Failed to resolve component '([^']+)'`)
	errLineRe  = regexp.MustCompile(`^(?:ERROR|Error|error): (.*)$`)
)

// Parse finds errors in build output: compiler errors (file, line, column),
// CMake errors, undefined symbols and component resolution failures. Each
// gets a hint when it names an ESP-IDF component the emulator lacks.
// Files under project are made relative to it.
func Parse(output, project string) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}
	add := func(d Diagnostic) {
		if d.Hint == "" {
			d.Hint = HintFor(d.Message)
		}
		key := d.File + ":" + strconv.Itoa(d.Line) + ":" + d.Message
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r")
		if m := ccRe.FindStringSubmatch(l); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			add(Diagnostic{File: rel(m[1], project), Line: ln, Column: col, Message: m[4]})
			continue
		}
		if m := cmakeRe.FindStringSubmatch(l); m != nil {
			// The message is the indented block up to a blank line or the call stack.
			var msg []string
			j := i + 1
			for ; j < len(lines) && j < i+12; j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" && len(msg) == 0 {
					continue
				}
				if t == "" || strings.HasPrefix(t, "Call Stack") || strings.HasPrefix(t, "-- ") {
					break
				}
				msg = append(msg, t)
			}
			d := Diagnostic{Message: strings.Join(msg, " ")}
			// Name the file only when it is the project's: an error raised
			// inside ESP-IDF's own CMake code points nowhere useful.
			if f := rel(m[1], project); !filepath.IsAbs(f) {
				d.File = f
				d.Line, _ = strconv.Atoi(m[2])
			}
			add(d)
			i = j - 1
			continue
		}
		if m := resolveRe.FindStringSubmatch(l); m != nil {
			add(Diagnostic{Message: strings.TrimSpace(l)})
			continue
		}
		if m := undefGNU.FindStringSubmatch(l); m != nil {
			add(Diagnostic{Message: "undefined symbol " + m[1]})
			continue
		}
		if m := undefApple.FindStringSubmatch(l); m != nil {
			add(Diagnostic{Message: "undefined symbol " + strings.TrimPrefix(m[1], "_")})
			continue
		}
		if m := errLineRe.FindStringSubmatch(l); m != nil && !strings.Contains(l, "ninja") {
			add(Diagnostic{Message: m[1]})
		}
	}
	return out
}

func rel(path, project string) string {
	if project == "" {
		return path
	}
	if r, err := filepath.Rel(project, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}
