package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	lcd349kit "github.com/MaverickHQ/lcd349-kit"
	"github.com/MaverickHQ/lcd349-kit/internal/rpc"
)

// Config describes a new instance.
type Config struct {
	ID      string // "" = a free id from the project's or program's name
	Project string // the project directory, if known
	ELF     string // the product built for the linux target
	Options Options
}

// Prepare creates the instance directory and its instance.json, ready for a
// supervisor.
func Prepare(cfg Config) (*Info, error) {
	elf, err := filepath.Abs(cfg.ELF)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(elf); err != nil || st.IsDir() {
		return nil, fmt.Errorf("no emulator program at %s; build it with lcd349 build", elf)
	}
	id := cfg.ID
	if id == "" {
		base := filepath.Base(cfg.Project)
		if cfg.Project == "" {
			base = filepath.Base(elf)
			base = base[:len(base)-len(filepath.Ext(base))]
		}
		id = NewID(base)
	}
	dir, err := Create(id)
	if err != nil {
		return nil, err
	}
	in := &Info{ID: id, Dir: dir, KitVersion: lcd349kit.Version, Project: cfg.Project, ELF: elf,
		State: StateStarting, Started: time.Now().UTC(), Options: cfg.Options}
	if err := in.write(); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return in, nil
}

// Spawn starts a supervisor process for a prepared instance: exe (the lcd349
// binary) with the arguments `supervise <dir>`. Tied, it stops when this
// process exits, through a pipe it watches; detached, it outlives it in its
// own session. out gets its output (the serial echo when echo is set); nil
// means supervisor.log in the instance directory.
func Spawn(in *Info, exe string, tied, echo bool, out io.Writer) (*Supervisor, error) {
	args := []string{"supervise"}
	if tied {
		args = append(args, "--tied")
	}
	if echo {
		args = append(args, "--echo")
	}
	cmd := exec.Command(exe, append(args, in.Dir)...)
	if out == nil {
		f, err := os.OpenFile(filepath.Join(in.Dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		out = f
	}
	cmd.Stdout, cmd.Stderr = out, out
	if tied {
		r, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		cmd.ExtraFiles = []*os.File{r} // fd 3 in the supervisor
		// w stays open, unreferenced, for the life of this process.
		tiePipes = append(tiePipes, w)
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		os.RemoveAll(in.Dir)
		return nil, err
	}
	sup := &Supervisor{Cmd: cmd, done: make(chan struct{})}
	go func() {
		sup.err = cmd.Wait()
		close(sup.done)
	}()
	return sup, nil
}

// Supervisor is a spawned supervisor process.
type Supervisor struct {
	Cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Done is closed when the supervisor process has exited.
func (s *Supervisor) Done() <-chan struct{} { return s.done }

// Wait waits for the supervisor process to exit.
func (s *Supervisor) Wait() error {
	<-s.done
	return s.err
}

var tiePipes []*os.File

// SuperviseMain is `lcd349 supervise [--tied] [--echo] <dir>`: it runs the
// supervisor until SIGTERM, SIGINT or SIGHUP, or, tied, until its parent's
// pipe closes. It returns the process exit code.
func SuperviseMain(dir string, tied, echo bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	if tied {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		syscall.CloseOnExec(3) // the product and viewer mustn't inherit it
		go func() {
			tie := os.NewFile(3, "tie")
			io.Copy(io.Discard, tie) // returns when the parent exits
			cancel()
		}()
	}
	var out io.Writer
	if echo {
		out = os.Stdout
	}
	if err := Supervise(ctx, dir, out); err != nil {
		fmt.Fprintf(os.Stderr, "lcd349 supervise: %v\n", err)
		return 1
	}
	return 0
}

// WaitReady waits until the instance in dir answers (running or in download
// mode). It fails with the reason when the instance crashes, powers off or
// its supervisor gives up first.
func WaitReady(ctx context.Context, dir string, sup *Supervisor) (*Info, error) {
	var exited <-chan struct{}
	if sup != nil {
		exited = sup.Done()
	}
	for {
		in, err := Read(dir)
		if err == nil {
			switch in.State {
			case StateRunning, StateDownload:
				return in, nil
			case StateOff:
				return in, errors.New("the instance powered off while starting")
			case StateCrashed:
				return in, fmt.Errorf("the instance crashed while starting (%s); last lines:\n%s", exitText(in.LastExit), tail(in.LastExit))
			case StateHung:
				return in, errors.New("the instance hung while starting; see lcd349 list")
			}
			if in.Error != "" {
				return in, errors.New(in.Error)
			}
		}
		select {
		case <-ctx.Done():
			return in, fmt.Errorf("the instance didn't answer in time: %w", ctx.Err())
		case <-exited:
			msg, _ := os.ReadFile(filepath.Join(dir, "supervisor.log"))
			return in, fmt.Errorf("the supervisor exited: %s", msg)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func exitText(e *Exit) string {
	if e == nil {
		return "unknown exit"
	}
	if e.Signal != "" {
		return "killed by " + e.Signal
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

func tail(e *Exit) string {
	if e == nil {
		return ""
	}
	lines := e.LastLines
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	out := ""
	for _, l := range lines {
		out += "  " + l + "\n"
	}
	return out
}

// Stop stops the instance in dir and removes the directory. With a live
// supervisor it asks it to stop (SIGTERM); with a dead one it stops the
// product only after the product itself confirms it belongs to dir.
func Stop(dir string) error {
	in, err := Read(dir)
	if err != nil {
		return os.RemoveAll(dir)
	}
	if in.Alive() && in.SupervisorPID > 0 {
		syscall.Kill(in.SupervisorPID, syscall.SIGTERM)
		deadline := time.Now().Add(10 * time.Second)
		for in.Alive() && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if in.Alive() {
			syscall.Kill(in.SupervisorPID, syscall.SIGKILL)
			time.Sleep(100 * time.Millisecond)
		}
	}
	killOrphan(in)
	return os.RemoveAll(dir)
}

// killOrphan kills the product of an instance whose supervisor is gone, if
// it answers on the instance's socket with the recorded pid and directory.
func killOrphan(in *Info) {
	if in.PID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, err := rpc.Dial(ctx, in.Dir)
	if err != nil {
		return
	}
	defer c.Close()
	var info rpc.Info
	if c.Call(ctx, "info", nil, &info) == nil && info.PID == in.PID && filepath.Clean(info.InstanceDir) == filepath.Clean(in.Dir) {
		syscall.Kill(in.PID, syscall.SIGKILL)
	}
}
