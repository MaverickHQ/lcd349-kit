package main

import (
	"context"
	"fmt"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"

	"github.com/MaverickHQ/lcd349-kit/internal/doctor"
)

func init() {
	register(&command{name: "doctor", usage: "[-C project] [--no-viewer] [--json]", summary: "check ESP-IDF, the emulator's prerequisites, SDL and a project's kit pin", run: cmdDoctor})
}

func cmdDoctor(c *cmdCtx) int {
	c.jsonFlag()
	dir := c.flags.String("C", "", "also check this project `directory`")
	noViewer := c.flags.Bool("no-viewer", false, "don't find or build the viewer")
	if code, ok := c.parse(); !ok {
		return code
	}
	o := doctor.Options{Project: *dir}
	if !*noViewer {
		o.Viewer = func() (string, error) { return viewer.Path(c.stderr) }
	}
	checks := doctor.Run(context.Background(), o)
	failed := false
	for _, ch := range checks {
		failed = failed || ch.Status == doctor.Fail
	}
	if c.json {
		c.printJSON(checks)
	} else {
		for _, ch := range checks {
			fmt.Fprintf(c.stdout, "%-4s  %-30s %s\n", ch.Status, ch.Name, ch.Detail)
			if ch.Fix != "" {
				fmt.Fprintf(c.stdout, "      %-30s fix: %s\n", "", ch.Fix)
			}
		}
	}
	if failed {
		return exitFail
	}
	return exitOK
}
