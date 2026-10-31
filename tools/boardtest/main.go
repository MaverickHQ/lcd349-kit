// Command boardtest runs on-board tests (seam S1-4, design D1-6). It builds
// and flashes an esp32s3 Unity app that uses the board harness
// (components/lcd349/test_apps/board, or another project's with -C), drives
// the harness over the board's USB Serial/JTAG port, and reports Unity's
// results. Exit 0: every test passed; 1: a test failed; 2: the tests
// couldn't be run.
//
//	go run ./tools/boardtest [-only <subsystem>] [-C <project>] [-p <port>]
//	go run ./tools/boardtest -guide -C <project> [-restart | -from <step>] [-log <file>]
//
// With -guide it runs the project's on-device guide (the guided checkpoint,
// seam S1-5; guide.go) instead of Unity tests.
//
// Each flash-and-run holds the board lock (~/.lcd349-board.lock, flock), so
// runs from parallel lanes queue instead of colliding; LCD349_BOARD_LOCK_HELD=1
// skips it when the caller holds it. The Wi-Fi credentials come from
// test-wifi.toml, read only when the board asks and only while its mode is
// 0600, and are masked in everything boardtest prints.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/build"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const kitBoardApp = "components/lcd349/test_apps/board"

func run(args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("boardtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	only := fs.String("only", "", "run only this subsystem's tests (their Unity tag, such as power)")
	project := fs.String("C", "", "build, flash and run this project's Unity app instead of the kit's "+kitBoardApp)
	port := fs.String("p", "", "the board's serial port (default: the only /dev/cu.usbmodem*)")
	wifi := fs.String("wifi", filepath.Join(home, ".config", "herdr-pager", "test-wifi.toml"), "the Wi-Fi credentials file, mode 0600")
	lock := fs.String("lock", filepath.Join(home, ".lcd349-board.lock"), "the board lock")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up after this long on the board (with -guide, default 2h)")
	noFlash := fs.Bool("no-flash", false, "don't build or flash: run what the board has")
	guide := fs.Bool("guide", false, "run the -C project's on-device guide (the guided checkpoint) instead of Unity tests")
	restart := fs.Bool("restart", false, "with -guide: start the guide over instead of resuming it")
	from := fs.String("from", "", "with -guide: go on from this `step` (its id, such as wifi), keeping the other steps' results")
	logPath := fs.String("log", "", "also append everything boardtest prints to this file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "boardtest: unexpected arguments %q\n", fs.Args())
		return 2
	}
	if *guide && *project == "" {
		fmt.Fprintln(stderr, "boardtest: -guide needs -C <project>, the app with the guide (examples/inputs)")
		return 2
	}
	if *restart && *from != "" {
		fmt.Fprintln(stderr, "boardtest: -restart and -from contradict each other")
		return 2
	}
	if *guide {
		set := false
		fs.Visit(func(f *flag.Flag) { set = set || f.Name == "timeout" })
		if !set {
			*timeout = 2 * time.Hour // the owner unplugs, measures and plugs back in
		}
	}
	w := stdout
	if *logPath != "" {
		if err := os.MkdirAll(filepath.Dir(*logPath), 0o700); err != nil {
			fmt.Fprintf(stderr, "boardtest: the log: %v\n", err)
			return 2
		}
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "boardtest: the log: %v\n", err)
			return 2
		}
		defer f.Close()
		w = io.MultiWriter(stdout, f)
	}
	out := &masker{w: w}
	logf := func(format string, a ...any) { out.line(fmt.Sprintf("boardtest: "+format, a...)) }
	if *logPath != "" {
		logf("started %s: boardtest %s", time.Now().Format(time.RFC3339), strings.Join(args, " "))
	}

	dir, err := projectDir(*project)
	if err == nil && *port == "" {
		*port, err = findPort()
	}
	if err != nil {
		logf("%v", err)
		return 2
	}
	what := filepath.Base(dir)
	if *only != "" {
		what += " [" + *only + "]"
	}
	if *guide {
		what += " (guide)"
	}
	release, err := takeLock(*lock, fmt.Sprintf("boardtest pid %d: %s, since %s", os.Getpid(), what, time.Now().Format(time.RFC3339)), logf)
	if err != nil {
		logf("%v", err)
		return 2
	}
	defer release()
	if !*noFlash {
		if err := flash(dir, *port, logf); err != nil {
			logf("%v", err)
			return 2
		}
	}
	s := &session{out: out, logf: logf, wifiPath: *wifi, what: what, tag: "*", pending: "@P all"}
	if *guide {
		cmd := "@P guide"
		switch {
		case *restart:
			cmd = "@P guide restart"
		case *from != "":
			cmd = "@P guide from " + *from
		}
		return s.guide(*port, *timeout, cmd)
	}
	if *only != "" {
		s.tag, s.pending = *only, "@P only "+*only
	}
	return s.run(*port, *timeout)
}

// projectDir is -C's project, or the kit's own board app found from the
// working directory up.
func projectDir(dir string) (string, error) {
	if dir != "" {
		return build.CheckProject(dir)
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		app := filepath.Join(d, kitBoardApp)
		if _, err := os.Stat(filepath.Join(app, "CMakeLists.txt")); err == nil {
			return app, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no %s above %s: run boardtest in the kit, or name a project with -C", kitBoardApp, wd)
		}
	}
}

func findPort() (string, error) {
	ports, _ := filepath.Glob("/dev/cu.usbmodem*")
	switch len(ports) {
	case 1:
		return ports[0], nil
	case 0:
		return "", errors.New("no board on USB (no /dev/cu.usbmodem*); plug it in, or name the port with -p")
	}
	return "", fmt.Errorf("several USB serial ports (%s): name the board's with -p", strings.Join(ports, ", "))
}

// takeLock holds the board lock: an exclusive flock on path, which then
// names its holder. While someone else holds it, it waits, saying so every
// minute.
func takeLock(path, holder string, logf func(string, ...any)) (func(), error) {
	if os.Getenv("LCD349_BOARD_LOCK_HELD") == "1" {
		logf("the board lock is the caller's (LCD349_BOARD_LOCK_HELD=1)")
		return func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the board lock: %w", err)
	}
	fd := int(f.Fd())
	var said time.Time
	for {
		err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("the board lock %s: %w", path, err)
		}
		if time.Since(said) >= time.Minute {
			who, _ := os.ReadFile(path)
			logf("waiting for the board lock %s, held by %s", path, strings.TrimSpace(string(who)))
			said = time.Now()
		}
		time.Sleep(2 * time.Second)
	}
	f.Truncate(0)
	f.WriteAt([]byte(holder+"\n"), 0)
	return func() {
		f.Truncate(0)
		syscall.Flock(fd, syscall.LOCK_UN)
		f.Close()
	}, nil
}

// flash runs `idf.py -C <project> -p <port> build flash`: idf.py from PATH
// (the runner loads ESP-IDF's environment), else the pinned ESP-IDF that
// internal/build finds. A fixed argument list, no shell.
func flash(project, port string, logf func(string, ...any)) error {
	name, args, env := "", []string(nil), os.Environ()
	if p, err := exec.LookPath("idf.py"); err == nil {
		name = p
	} else {
		idf, err := build.Environment(context.Background(), false)
		if err != nil {
			return err
		}
		name, args, env = idf.Python, []string{filepath.Join(idf.Path, "tools", "idf.py")}, idf.Env
	}
	args = append(args, "-C", project, "-p", port, "build", "flash")
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = project, env
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	logf("building and flashing %s on %s", project, port)
	start := time.Now()
	err := cmd.Run()
	logPath := filepath.Join(project, "build", "boardtest-idf.log")
	if _, serr := os.Stat(filepath.Dir(logPath)); serr == nil {
		os.WriteFile(logPath, buf.Bytes(), 0o644)
	}
	if err != nil {
		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		if len(lines) > 30 {
			lines = lines[len(lines)-30:]
		}
		for _, l := range lines {
			logf("| %s", l)
		}
		return fmt.Errorf("idf.py build flash failed (%v); the whole output is in %s", err, logPath)
	}
	logf("built and flashed in %.0f s", time.Since(start).Seconds())
	return nil
}
