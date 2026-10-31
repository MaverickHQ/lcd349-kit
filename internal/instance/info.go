package instance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	infoFile = "instance.json"
	lockFile = "supervisor.lock"
)

// States of an instance, in Info.State.
const (
	StateStarting = "starting" // the product is starting; its socket isn't answering yet
	StateRunning  = "running"
	StateDownload = "download" // started with BOOT held: ROM download mode
	StateHung     = "hung"     // running, but no answer to ping for HangAfter
	StateOff      = "off"      // powered off; pressing PWR starts it again
	StateCrashed  = "crashed"  // the product exited abnormally; see LastExit
	StateStopping = "stopping"
)

// HangAfter is how long ping may go unanswered before the instance is
// reported as hung (task 6.1; design §Threading on the linux target).
const HangAfter = 5 * time.Second

// CrashLines is how many serial lines a crash record keeps.
const CrashLines = 200

// Info is instance.json: written by the supervisor, read by everyone else.
type Info struct {
	ID            string    `json:"id"`
	Dir           string    `json:"dir"`
	KitVersion    string    `json:"kit_version"`
	Project       string    `json:"project,omitempty"`
	ELF           string    `json:"elf"`
	Serial        string    `json:"serial"` // the virtual serial port: a pty slave path
	SupervisorPID int       `json:"supervisor_pid"`
	PID           int       `json:"pid"` // the product process; 0 while it isn't running
	State         string    `json:"state"`
	Started       time.Time `json:"started"`
	Boots         int       `json:"boots"`
	BootReason    string    `json:"boot_reason,omitempty"` // of the current boot: power-on, software, crash
	Options       Options   `json:"options"`
	LastExit      *Exit     `json:"last_exit,omitempty"`
	Hung          *Hang     `json:"hung,omitempty"`
	Viewer        string    `json:"viewer,omitempty"` // running, no window, closed, or an error
	ViewerPID     int       `json:"viewer_pid,omitempty"`
	// LogLinesBefore counts serial lines in rotated-out logs older than
	// serial.log.1, so line numbers stay stable across rotation.
	LogLinesBefore int    `json:"log_lines_before,omitempty"`
	Error          string `json:"error,omitempty"` // why the supervisor gave up
}

// Options are the run settings. First-boot settings (clock, wipe, battery,
// power source) apply only to the first start: a reset keeps what the
// instance has since become, as the board would.
type Options struct {
	Clock     string `json:"clock,omitempty"`
	Wipe      bool   `json:"wipe,omitempty"`
	Storage   string `json:"storage,omitempty"`
	BatteryMV int    `json:"battery_mv,omitempty"`
	Source    string `json:"source,omitempty"` // usb or battery
	Viewer    string `json:"viewer,omitempty"` // lcd349-view to start; empty = headless
}

// Exit describes how the product process last ended.
type Exit struct {
	Code      int       `json:"code"` // -1 when ended by a signal
	Signal    string    `json:"signal,omitempty"`
	Meaning   string    `json:"meaning"` // reset, reset_boot_held, off, crash
	At        time.Time `json:"at"`
	LastLines []string  `json:"last_lines,omitempty"` // crashes: the last CrashLines serial lines
}

// Hang records an instance that stopped answering.
type Hang struct {
	Since       time.Time `json:"since"`
	Sample      string    `json:"sample,omitempty"` // a stack sample of every thread
	SampleError string    `json:"sample_error,omitempty"`
}

// Read loads dir/instance.json.
func Read(dir string) (*Info, error) {
	b, err := os.ReadFile(filepath.Join(dir, infoFile))
	if err != nil {
		return nil, err
	}
	var in Info
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, err
	}
	in.Dir = dir
	return &in, nil
}

// write saves instance.json atomically.
func (in *Info) write() error {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(in.Dir, infoFile), append(b, '\n'))
}

func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// Alive reports whether the instance's supervisor is still running: it holds
// a lock on supervisor.lock for its whole life, which no recycled pid can fake.
func (in *Info) Alive() bool { return supervisorAlive(in.Dir) }

func supervisorAlive(dir string) bool {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK)
}

// lock takes the supervisor lock, or fails if another supervisor has it.
func lock(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another supervisor runs this instance")
	}
	return f, nil
}

// Status is State, or "gone" when the supervisor has died without cleaning up.
func (in *Info) Status() string {
	if !in.Alive() {
		return "gone"
	}
	return in.State
}
