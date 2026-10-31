package main

import (
	"fmt"
	"path/filepath"

	"github.com/MaverickHQ/lcd349-kit/internal/scaffold"
)

func init() {
	register(&command{name: "new", usage: "<dir> [--name app] [--kit-path kit/components/lcd349] [--json]", summary: "create a product: one screen, a scenario with its golden, and a CLAUDE.md", run: cmdNew})
}

func cmdNew(c *cmdCtx) int {
	c.jsonFlag()
	name := c.flags.String("name", "", "the project `name` (default: from the directory)")
	kit := c.flags.String("kit-path", "", "use this local kit component `directory` instead of pinning a release")
	if code, ok := c.parse(); !ok {
		return code
	}
	if len(c.args) != 1 {
		return c.usageErr("name the directory to create")
	}
	o := scaffold.Options{Dir: c.args[0], Name: *name, KitPath: *kit}
	files, err := scaffold.New(o)
	if err != nil {
		return c.fail(err)
	}
	if o.Name == "" {
		o.Name = scaffold.NameFrom(o.Dir)
	}
	abs, _ := filepath.Abs(o.Dir)
	if c.json {
		c.printJSON(map[string]any{"dir": abs, "name": o.Name, "files": files})
		return exitOK
	}
	fmt.Fprintf(c.stdout, "created %s in %s:\n", o.Name, abs)
	for _, f := range files {
		fmt.Fprintf(c.stdout, "  %s\n", f)
	}
	fmt.Fprintf(c.stdout, "\nnext:\n  cd %s\n  lcd349 run                                  # build and open it in the emulator\n  lcd349 scenario tests/app.scenario.yaml     # its check\n  idf.py set-target esp32s3 build flash       # the board\n", c.args[0])
	return exitOK
}
