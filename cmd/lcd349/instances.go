package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/MaverickHQ/lcd349-kit/internal/viewer"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/drive"
	"github.com/MaverickHQ/lcd349-kit/internal/instance"
)

func init() {
	register(&command{name: "list", usage: "[--json]", summary: "list instances and their state", run: cmdList})
	register(&command{name: "stop", usage: "[instance...] [--all]", summary: "stop instances and remove their directories", run: cmdStop})
	register(&command{name: "logs", usage: "[instance] [-n lines] [--since line] [--grep regexp] [-f] [--json]", summary: "print an instance's serial output", run: cmdLogs})
	register(&command{name: "view", usage: "[instance]", summary: "open the window for a running instance", run: cmdView})
	register(&command{name: "power", usage: "[instance] on|off|reset|download", summary: "power on, off or reset an instance (download = reset with BOOT held)", run: cmdPower})
	register(&command{name: "supervise", usage: "[--tied] [--echo] <dir>", summary: "run an instance's supervisor (started by lcd349 run)", run: cmdSupervise, hidden: true})
}

// pick resolves an optional leading instance argument. With want positional
// arguments after it, an extra leading one names the instance; otherwise the
// only running instance is used.
func (c *cmdCtx) pick(want int) (*instance.Info, []string, error) {
	args := c.args
	var ref string
	if len(args) > want {
		ref, args = args[0], args[1:]
	}
	if len(args) != want && want >= 0 {
		return nil, nil, fmt.Errorf("expected %d argument(s) after the instance; usage: lcd349 %s %s", want, c.cmd.name, c.cmd.usage)
	}
	if ref == "" {
		in, err := instance.Only()
		return in, args, err
	}
	dir, err := instance.Find(ref)
	if err != nil {
		return nil, nil, err
	}
	in, err := instance.Read(dir)
	return in, args, err
}

// call makes one request to an instance with a timeout.
func call(in *instance.Info, timeout time.Duration, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	d, err := drive.Connect(ctx, in)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Call(ctx, method, params, result)
}

func cmdList(c *cmdCtx) int {
	c.jsonFlag()
	if code, ok := c.parse(); !ok {
		return code
	}
	all, err := instance.List()
	if err != nil {
		return c.fail(err)
	}
	if c.json {
		type row struct {
			*instance.Info
			Status string `json:"status"`
		}
		rows := []row{}
		for _, in := range all {
			rows = append(rows, row{in, in.Status()})
		}
		c.printJSON(rows)
		return exitOK
	}
	if len(all) == 0 {
		fmt.Fprintln(c.stdout, "no instances; start one with lcd349 run")
		return exitOK
	}
	tw := tabwriter.NewWriter(c.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tPID\tBOOTS\tSERIAL\tUP\tPROGRAM")
	for _, in := range all {
		pid := "-"
		if in.PID > 0 {
			pid = fmt.Sprint(in.PID)
		}
		prog := in.Project
		if prog == "" {
			prog = in.ELF
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", in.ID, in.Status(), pid, in.Boots, in.Serial,
			time.Since(in.Started).Round(time.Second), prog)
	}
	tw.Flush()
	return exitOK
}

func cmdStop(c *cmdCtx) int {
	all := c.flags.Bool("all", false, "stop every instance, including ones whose supervisor is gone")
	if code, ok := c.parse(); !ok {
		return code
	}
	var dirs []string
	switch {
	case *all:
		list, err := instance.List()
		if err != nil {
			return c.fail(err)
		}
		for _, in := range list {
			dirs = append(dirs, in.Dir)
		}
	case len(c.args) == 0:
		in, err := instance.Only()
		if err != nil {
			return c.fail(err)
		}
		dirs = append(dirs, in.Dir)
	default:
		for _, ref := range c.args {
			dir, err := instance.Find(ref)
			if err != nil {
				return c.fail(err)
			}
			dirs = append(dirs, dir)
		}
	}
	code := exitOK
	for _, dir := range dirs {
		if err := instance.Stop(dir); err != nil {
			fmt.Fprintf(c.stderr, "lcd349 stop: %s: %v\n", filepath.Base(dir), err)
			code = exitFail
			continue
		}
		fmt.Fprintf(c.stdout, "stopped %s\n", filepath.Base(dir))
	}
	return code
}

func cmdLogs(c *cmdCtx) int {
	c.jsonFlag()
	last := c.flags.Int("n", 0, "only the last `lines` lines (0 = all)")
	since := c.flags.Int("since", 1, "start at line `number` (from an earlier --json's next)")
	grep := c.flags.String("grep", "", "only lines matching `regexp`")
	follow := c.flags.Bool("f", false, "keep printing new lines until the instance stops (Ctrl-C ends)")
	if code, ok := c.parse(); !ok {
		return code
	}
	in, _, err := c.pick(0)
	if err != nil {
		return c.fail(err)
	}
	var re *regexp.Regexp
	if *grep != "" {
		if re, err = regexp.Compile(*grep); err != nil {
			return c.usageErr("--grep: %v", err)
		}
	}
	filter := func(lines []instance.LogLine) []instance.LogLine {
		if re == nil {
			return lines
		}
		var out []instance.LogLine
		for _, l := range lines {
			if re.MatchString(l.Text) {
				out = append(out, l)
			}
		}
		return out
	}
	lines, next, err := instance.ReadLogs(in, *since)
	if err != nil {
		return c.fail(err)
	}
	lines = filter(lines)
	if *last > 0 && len(lines) > *last {
		lines = lines[len(lines)-*last:]
	}
	if c.json {
		if lines == nil {
			lines = []instance.LogLine{}
		}
		c.printJSON(map[string]any{"lines": lines, "next": next})
		return exitOK
	}
	for _, l := range lines {
		fmt.Fprintln(c.stdout, l.Text)
	}
	if !*follow {
		return exitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for ctx.Err() == nil {
		time.Sleep(200 * time.Millisecond)
		fresh, err := instance.Read(in.Dir)
		if err != nil {
			return exitOK // stopped
		}
		lines, n, err := instance.ReadLogs(fresh, next)
		if err != nil {
			return exitOK
		}
		next = n
		for _, l := range filter(lines) {
			fmt.Fprintln(c.stdout, l.Text)
		}
	}
	return exitOK
}

func cmdView(c *cmdCtx) int {
	if code, ok := c.parse(); !ok {
		return code
	}
	in, _, err := c.pick(0)
	if err != nil {
		return c.fail(err)
	}
	view, err := viewer.Path(c.stderr)
	if err != nil {
		return c.fail(err)
	}
	cmd := exec.Command(view, in.Dir)
	cmd.Stdout, cmd.Stderr = c.stdout, c.stderr
	if err := cmd.Run(); err != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 3 {
			return c.fail(errors.New("no window is available here (lcd349 doctor explains); the instance keeps running headless"))
		}
		return c.fail(err)
	}
	return exitOK
}

func cmdPower(c *cmdCtx) int {
	c.jsonFlag()
	if code, ok := c.parse(); !ok {
		return code
	}
	in, args, err := c.pick(1)
	if err != nil {
		return c.fail(err)
	}
	params := map[string]any{}
	switch args[0] {
	case "on", "off", "reset":
		params["action"] = args[0]
	case "download":
		params["action"], params["boot_held"] = "reset", true
	default:
		return c.usageErr("unknown action %q", args[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := drive.Connect(ctx, in)
	if err != nil {
		return c.fail(err)
	}
	defer d.Close()
	var res map[string]any
	if err := d.Call(ctx, "power", params, &res); err != nil {
		return c.fail(err)
	}
	if c.json {
		c.printJSON(res)
		return exitOK
	}
	switch args[0] {
	case "on":
		if res["already_on"] == true {
			fmt.Fprintf(c.stdout, "%s is already on\n", in.ID)
		} else {
			fmt.Fprintf(c.stdout, "%s: powering on\n", in.ID)
		}
	case "off":
		fmt.Fprintf(c.stdout, "%s: powering off\n", in.ID)
	default:
		fmt.Fprintf(c.stdout, "%s: resetting\n", in.ID)
	}
	return exitOK
}

func cmdSupervise(c *cmdCtx) int {
	tied := c.flags.Bool("tied", false, "stop when the parent's pipe (fd 3) closes")
	echo := c.flags.Bool("echo", false, "copy the serial output to stdout")
	if code, ok := c.parse(); !ok {
		return code
	}
	if len(c.args) != 1 {
		return c.usageErr("needs the instance directory")
	}
	return instance.SuperviseMain(c.args[0], *tied, *echo)
}
