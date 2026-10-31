// Command lcd349 builds, runs and drives emulated Waveshare
// ESP32-S3-Touch-LCD-3.49 products, and serves the same over MCP
// (`lcd349 mcp`). See `lcd349 help`.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
)

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1 // the command ran and failed (a build error, a failing step)
	exitUsage = 2
)

type command struct {
	name    string
	usage   string // arguments, after the name
	summary string
	run     func(c *cmdCtx) int
	hidden  bool
}

var commands []*command

func register(c *command) { commands = append(commands, c) }

// cmdCtx is one command invocation: its flags, positional arguments and output.
type cmdCtx struct {
	cmd    *command
	flags  *flag.FlagSet
	argv   []string // as given
	args   []string // positional, after parse
	stdout io.Writer
	stderr io.Writer
	json   bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 || argv[0] == "-h" || argv[0] == "--help" || argv[0] == "help" {
		if len(argv) > 1 {
			if c := find(argv[1]); c != nil {
				return invoke(c, []string{"-h"}, stdout, stderr)
			}
		}
		usage(stdout)
		return exitOK
	}
	if argv[0] == "--version" || argv[0] == "version" {
		fmt.Fprintf(stdout, "lcd349 %s\n", lcd349kit.Version)
		return exitOK
	}
	c := find(argv[0])
	if c == nil {
		fmt.Fprintf(stderr, "lcd349: unknown command %q; lcd349 help lists them\n", argv[0])
		return exitUsage
	}
	return invoke(c, argv[1:], stdout, stderr)
}

func find(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "lcd349 %s: build, run and drive emulated ESP32-S3-Touch-LCD-3.49 products.\n\nUsage: lcd349 <command> [arguments]\n\nCommands:\n", lcd349kit.Version)
	cs := append([]*command(nil), commands...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].name < cs[j].name })
	for _, c := range cs {
		if !c.hidden {
			fmt.Fprintf(w, "  %-9s %s\n", c.name, c.summary)
		}
	}
	fmt.Fprintf(w, "\nInstance arguments may be left out when exactly one instance is running.\n`lcd349 help <command>` shows a command's flags.\n")
}

// invoke parses flags anywhere among the arguments (flag stops at the first
// positional one, so it is called again on the rest).
func invoke(c *command, argv []string, stdout, stderr io.Writer) int {
	cc := &cmdCtx{cmd: c, argv: argv, stdout: stdout, stderr: stderr}
	cc.flags = flag.NewFlagSet(c.name, flag.ContinueOnError)
	cc.flags.SetOutput(stderr)
	cc.flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: lcd349 %s %s\n\n%s\n", c.name, c.usage, c.summary)
		var any bool
		cc.flags.VisitAll(func(*flag.Flag) { any = true })
		if any {
			fmt.Fprintln(stderr, "\nFlags:")
			cc.flags.PrintDefaults()
		}
	}
	return c.run(cc)
}

// parse parses the flags the command defined, taking positional arguments
// from anywhere among them. When it returns false the command returns code:
// 0 after -h, exitUsage after a bad flag (already reported).
func (c *cmdCtx) parse() (code int, ok bool) {
	rest := c.argv
	for {
		if err := c.flags.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitOK, false
			}
			return exitUsage, false
		}
		rest = c.flags.Args()
		if len(rest) == 0 {
			return 0, true
		}
		if rest[0] == "--" {
			c.args = append(c.args, rest[1:]...)
			return 0, true
		}
		c.args = append(c.args, rest[0])
		rest = rest[1:]
	}
}

// jsonFlag adds --json.
func (c *cmdCtx) jsonFlag() { c.flags.BoolVar(&c.json, "json", false, "print JSON") }

func (c *cmdCtx) printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(c.stdout, string(b))
}

// fail prints an error and returns exitFail.
func (c *cmdCtx) fail(err error) int {
	fmt.Fprintf(c.stderr, "lcd349 %s: %s\n", c.cmd.name, errText(err))
	return exitFail
}

func (c *cmdCtx) usageErr(format string, a ...any) int {
	fmt.Fprintf(c.stderr, "lcd349 %s: %s\nUsage: lcd349 %s %s\n", c.cmd.name, fmt.Sprintf(format, a...), c.cmd.name, c.cmd.usage)
	return exitUsage
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
