// Command eval runs the KN6 agent evaluation (task 10.3): fresh, isolated
// Claude Code sessions with only the lcd349 plugin, each given one brief,
// then checks each result independently. evals/run.sh calls it.
//
// A session sees the lcd349 skill (--plugin-dir) and the plugin's MCP server
// (--mcp-config, the same launcher command), and none of the user's own
// settings, plugins, skills or MCP servers (--setting-sources project in an
// empty workspace, --strict-mcp-config). It may read and write files and use
// the lcd349 tools; it has no shell. Spend is capped per session.
//
// Checks (KN6): the project builds for the emulator and for esp32s3, its
// first screen has no lint errors, and it has scenarios that all pass.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

type session struct {
	CostUSD   float64        `json:"cost_usd"`
	Turns     int            `json:"turns"`
	DurationS float64        `json:"duration_s"`
	Subtype   string         `json:"subtype"` // success, error_max_budget_usd, ...
	IsError   bool           `json:"is_error"`
	ToolCalls map[string]int `json:"tool_calls"`
	FinalText string         `json:"final_text"`
}

type checks struct {
	Project     string   `json:"project"`
	EmulatorOK  bool     `json:"emulator_build"`
	BoardOK     bool     `json:"esp32s3_build"`
	LintErrors  int      `json:"first_screen_lint_errors"`
	LintOK      bool     `json:"lint_ok"`
	Scenarios   int      `json:"scenarios"`
	ScenariosOK bool     `json:"scenarios_pass"`
	ShotSteps   int      `json:"shot_steps"`
	LintSteps   int      `json:"lint_steps"`
	Notes       []string `json:"notes,omitempty"`
}

type result struct {
	Run     int     `json:"run"`
	Model   string  `json:"model"`
	Session session `json:"session"`
	Checks  checks  `json:"checks"`
	Pass    bool    `json:"pass"`
}

func main() {
	runs := flag.Int("runs", 5, "sessions to run")
	first := flag.Int("first", 1, "number of the first run (to add runs to a results directory)")
	model := flag.String("model", "claude-opus-5-5", "the fixed model")
	budget := flag.Float64("budget", 15, "spend cap per session, US dollars")
	briefPath := flag.String("brief", "evals/briefs/three-screen.md", "the brief")
	out := flag.String("out", filepath.Join("evals", "results", time.Now().Format("2006-01-02")), "results directory")
	timeout := flag.Duration("timeout", 60*time.Minute, "wall-clock limit per session")
	flag.Parse()

	repo, _ := filepath.Abs(".")
	brief, err := os.ReadFile(*briefPath)
	check(err)
	tmp, err := os.MkdirTemp("", "lcd349-eval-")
	check(err)
	bin := filepath.Join(tmp, "bin")
	check(os.MkdirAll(bin, 0o755))
	if o, err := exec.Command("go", "build", "-o", filepath.Join(bin, "lcd349"), "./cmd/lcd349").CombinedOutput(); err != nil {
		fail("building lcd349: %v\n%s", err, o)
	}
	inst, err := os.MkdirTemp("/tmp", "l349e-")
	check(err)
	defer os.RemoveAll(inst)
	mcpConfig := filepath.Join(tmp, "mcp.json")
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"lcd349": map[string]any{
		"command": filepath.Join(repo, "plugin", "scripts", "lcd349-launch"), "args": []string{"mcp"}}}})
	check(os.WriteFile(mcpConfig, cfg, 0o644))
	env := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LCD349_KIT_PATH="+filepath.Join(repo, "components", "lcd349"),
		"LCD349_INSTANCES="+inst)
	check(os.MkdirAll(*out, 0o755))
	ctx := context.Background()
	idf, err := build.Environment(ctx, false)
	check(err)

	var all []result
	for i := *first; i < *first+*runs; i++ {
		runDir := filepath.Join(*out, fmt.Sprintf("run-%d", i))
		check(os.MkdirAll(runDir, 0o755))
		ws := filepath.Join(tmp, fmt.Sprintf("ws-%d", i))
		check(os.MkdirAll(ws, 0o755))
		fmt.Fprintf(os.Stderr, "eval: run %d (%s, cap $%.2f) in %s\n", i, *model, *budget, ws)
		r := result{Run: i, Model: *model}
		r.Session = runSession(ctx, ws, runDir, string(brief), *model, *budget, *timeout, mcpConfig, repo, env)
		r.Checks = checkResult(ctx, idf, ws, filepath.Join(bin, "lcd349"), env)
		r.Pass = r.Checks.EmulatorOK && r.Checks.BoardOK && r.Checks.LintOK && r.Checks.ScenariosOK
		copySources(r.Checks.Project, filepath.Join(runDir, "project"))
		b, _ := json.MarshalIndent(r, "", "  ")
		check(os.WriteFile(filepath.Join(runDir, "result.json"), b, 0o644))
		fmt.Fprintf(os.Stderr, "eval: run %d: pass %v, $%.2f, %d turns, %.0f s\n", i, r.Pass, r.Session.CostUSD, r.Session.Turns, r.Session.DurationS)
		all = append(all, r)
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	fmt.Println(string(b))
}

func runSession(ctx context.Context, ws, runDir, brief, model string, budget float64, timeout time.Duration, mcpConfig, repo string, env []string) session {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	cmd := exec.CommandContext(ctx, "claude", "-p", brief,
		"--model", model, "--max-budget-usd", fmt.Sprint(budget),
		"--setting-sources", "project", "--strict-mcp-config", "--mcp-config", mcpConfig,
		"--plugin-dir", filepath.Join(repo, "plugin"),
		"--permission-mode", "acceptEdits",
		"--allowedTools", "Read,Write,Edit,Glob,Grep,Skill,TodoWrite,mcp__lcd349",
		"--disallowedTools", "Bash",
		"--output-format", "stream-json", "--verbose", "--no-session-persistence")
	cmd.Dir = ws
	cmd.Env = env
	tf, err := os.Create(filepath.Join(runDir, "transcript.jsonl"))
	check(err)
	defer tf.Close()
	ef, _ := os.Create(filepath.Join(runDir, "stderr.log"))
	defer ef.Close()
	cmd.Stdout, cmd.Stderr = tf, ef
	runErr := cmd.Run()
	s := session{ToolCalls: map[string]int{}, DurationS: time.Since(start).Seconds()}
	tf.Seek(0, 0)
	sc := bufio.NewScanner(tf)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Name string `json:"name"`
				} `json:"content"`
			} `json:"message"`
			Cost    float64 `json:"total_cost_usd"`
			Turns   int     `json:"num_turns"`
			IsError bool    `json:"is_error"`
			Result  string  `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "assistant":
			for _, c := range m.Message.Content {
				if c.Type == "tool_use" {
					s.ToolCalls[strings.TrimPrefix(c.Name, "mcp__lcd349__")]++
				}
			}
		case "result":
			s.CostUSD, s.Turns, s.Subtype, s.IsError, s.FinalText = m.Cost, m.Turns, m.Subtype, m.IsError, m.Result
		}
	}
	if runErr != nil && s.Subtype == "" {
		s.Subtype = "ended: " + runErr.Error()
		s.IsError = true
	}
	return s
}

// findProject returns the first lcd349 project in the workspace, in walk
// order, outside managed components and build directories; "" if none.
func findProject(ws string) string {
	var found string
	_ = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(p, "managed_components") || strings.Contains(p, "build") {
			return filepath.SkipDir
		}
		if _, err := project.LoadKitProject(p); err == nil {
			found = p
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func checkResult(ctx context.Context, idf *build.IDF, ws, lcd349 string, env []string) checks {
	c := checks{Project: findProject(ws)}
	if c.Project == "" {
		c.Notes = append(c.Notes, "no lcd349 project in the workspace")
		return c
	}
	res, err := build.Build(ctx, idf, c.Project, nil)
	c.EmulatorOK = err == nil && res.OK
	if !c.EmulatorOK {
		c.Notes = append(c.Notes, fmt.Sprintf("emulator build failed: %v %v", err, res))
		return c
	}
	// The board build, in its own directory.
	bdir := filepath.Join(c.Project, "build-eval-esp32s3")
	board := exec.CommandContext(ctx, idf.Python, filepath.Join(idf.Path, "tools", "idf.py"), "-C", c.Project, "-B", bdir,
		"-D", "SDKCONFIG="+filepath.Join(bdir, "sdkconfig"), "set-target", "esp32s3", "build")
	board.Env = idf.Env
	if o, err := board.CombinedOutput(); err != nil {
		c.Notes = append(c.Notes, "esp32s3 build failed: "+tail(string(o), 15))
	} else {
		c.BoardOK = true
	}
	// The first screen's lint, as it comes up.
	in, err := instance.Prepare(instance.Config{Project: c.Project, ELF: res.ELF, Options: instance.Options{Clock: "2026-10-12T09:00:00Z", Wipe: true}})
	if err == nil {
		var sup *instance.Supervisor
		if sup, err = instance.Spawn(in, lcd349, true, false, nil); err == nil {
			wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			in, err = instance.WaitReady(wctx, in.Dir, sup)
			cancel()
			if err == nil {
				if d, derr := drive.Connect(ctx, in); derr == nil {
					var w rpc.Wait
					d.Call(ctx, "wait", map[string]any{"idle_ms": 500, "timeout_ms": 10000}, &w)
					var l rpc.Lint
					if lerr := d.Call(ctx, "ui.lint", map[string]any{}, &l); lerr == nil {
						c.LintErrors, c.LintOK = l.Errors, l.Errors == 0
						for _, f := range l.Findings {
							if f.Severity == "error" {
								c.Notes = append(c.Notes, fmt.Sprintf("lint: %s %s: %s", f.Rule, f.Path, f.Message))
							}
						}
					}
					d.Close()
				}
			}
			instance.Stop(in.Dir)
			sup.Wait()
		}
	}
	if err != nil {
		c.Notes = append(c.Notes, "couldn't run the product: "+err.Error())
	}
	// Its own scenarios, every one passing.
	files, _ := filepath.Glob(filepath.Join(c.Project, "tests", "*.scenario.yaml"))
	c.Scenarios = len(files)
	c.ScenariosOK = len(files) > 0
	for _, f := range files {
		b, _ := os.ReadFile(f)
		c.ShotSteps += strings.Count(string(b), "shot:")
		c.LintSteps += strings.Count(string(b), "lint:")
		cmd := exec.CommandContext(ctx, lcd349, "scenario", f, "--no-build")
		cmd.Env = env
		o, err := cmd.CombinedOutput()
		if err != nil {
			c.ScenariosOK = false
			c.Notes = append(c.Notes, fmt.Sprintf("scenario %s failed: %s", filepath.Base(f), tail(string(o), 12)))
		}
	}
	if len(files) == 0 {
		c.Notes = append(c.Notes, "no scenarios in tests/")
	}
	return c
}

// copySources keeps the project's own files for review, without builds.
func copySources(src, dst string) {
	if src == "" {
		return
	}
	filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && (strings.HasPrefix(d.Name(), "build") || d.Name() == "managed_components") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() == "sdkconfig" || d.Name() == "sdkconfig.old" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		return nil
	})
}

func tail(s string, n int) string {
	l := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

func check(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "eval: "+format+"\n", a...)
	os.Exit(1)
}
