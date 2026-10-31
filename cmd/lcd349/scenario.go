package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MaverickHQ/lcd349-kit/internal/build"
	"github.com/MaverickHQ/lcd349-kit/internal/project"
	"github.com/MaverickHQ/lcd349-kit/internal/scenario"
)

func init() {
	register(&command{name: "scenario", usage: "<file.scenario.yaml>... [-C project] [--update-goldens] [--no-build] [--elf program] [--keep] [--json]",
		summary: "run scenarios headless; exit 1 at the first failing step", run: cmdScenario})
}

func cmdScenario(c *cmdCtx) int {
	c.jsonFlag()
	dir := c.flags.String("C", "", "the project `directory` (default: the one containing the first file)")
	update := c.flags.Bool("update-goldens", false, "write goldens that are missing or differ, and pass")
	noBuild := c.flags.Bool("no-build", false, "use the last build")
	elf := c.flags.String("elf", "", "run this emulator `program` instead of the project's build")
	keep := c.flags.Bool("keep", false, "on failure, leave the instance running to inspect (lcd349 stop ends it)")
	verbose := c.flags.Bool("v", false, "show idf.py's output")
	if code, ok := c.parse(); !ok {
		return code
	}
	if len(c.args) == 0 {
		return c.usageErr("name at least one scenario file")
	}
	var proj *project.Project
	var err error
	if *dir != "" {
		proj, err = project.LoadKitProject(*dir)
	} else {
		proj, err = project.Enclosing(c.args[0])
	}
	if err != nil {
		return c.fail(err)
	}
	var scs []*scenario.Scenario
	for _, f := range c.args {
		s, err := scenario.Load(f)
		if err != nil {
			return c.fail(err)
		}
		scs = append(scs, s)
	}
	program := *elf
	if program == "" {
		if *noBuild {
			program, err = build.ELF(proj.Dir)
		} else {
			var res *build.Result
			if res, err = buildProject(c, proj.Dir, *verbose); err == nil && !res.OK {
				reportBuild(c, res)
				return exitFail
			} else if err == nil {
				program = res.ELF
			}
		}
		if err != nil {
			return c.fail(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return c.fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := scenario.Options{Project: proj, ELF: program, Exe: exe, UpdateGoldens: *update, Keep: *keep}
	if !c.json {
		o.Progress = c.stdout
	}
	var results []*scenario.Result
	code := exitOK
	for _, s := range scs {
		if !c.json {
			fmt.Fprintf(c.stdout, "scenario %s (%s)\n", s.Name, s.File)
		}
		res, err := scenario.Run(ctx, s, o)
		if err != nil {
			return c.fail(fmt.Errorf("%s: %w", s.File, err))
		}
		results = append(results, res)
		if !c.json {
			if res.OK {
				fmt.Fprintf(c.stdout, "passed: %d steps in %.1fs\n", len(res.Steps), res.Seconds)
			} else {
				f := res.Failed()
				fmt.Fprintf(c.stdout, "FAILED at step %d (%s, line %d)\n", f.N, f.Step, f.Line)
				if res.Instance != "" {
					fmt.Fprintf(c.stdout, "the instance is still running: lcd349 shot %s, lcd349 logs %s; lcd349 stop %s ends it\n", res.Instance, res.Instance, res.Instance)
				}
			}
		}
		if !res.OK {
			code = exitFail
			break // the first failing step ends the run (R7.7)
		}
	}
	if c.json {
		c.printJSON(results)
	}
	return code
}
