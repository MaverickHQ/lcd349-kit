// Package pty opens pseudo-terminal pairs for an instance's serial console
// (design §Instances, serial and the lcd349 CLI). It uses only the standard
// library: /dev/ptmx and the platform's ioctls.
package pty

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Pty is one pseudo-terminal: the master side, kept by the supervisor, and the
// path of the slave side, which the product or a host tool opens.
type Pty struct {
	Master *os.File
	Slave  string
	// A slave descriptor held open for the pty's life: macOS resets a
	// terminal's settings when its last descriptor closes, and raw mode must
	// survive host tools opening and closing the port.
	hold *os.File
}

// Open creates a pty whose slave is owner-only (mode 0600, threat KT8) and in
// raw mode: bytes pass unchanged, with no echo and no line editing, as on a
// USB serial port.
func Open() (*Pty, error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	slave, err := unlockpt(fd)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	if err := os.Chmod(slave, 0o600); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("chmod %s: %w", slave, err)
	}
	// Blocking: each reader gets its own thread. The runtime's poller
	// (kqueue on macOS) isn't used for pty masters, which it doesn't serve
	// reliably (found in task 10.2: a relay stopped reading after 33 minutes).
	p := &Pty{Master: os.NewFile(uintptr(fd), "ptmx"), Slave: slave}
	if p.hold, err = p.OpenSlave(); err != nil {
		p.Master.Close()
		return nil, err
	}
	if err := control(p.hold, MakeRaw); err != nil {
		p.Close()
		return nil, fmt.Errorf("raw mode on %s: %w", slave, err)
	}
	return p, nil
}

// Close closes both sides held by this process.
func (p *Pty) Close() error {
	p.hold.Close()
	return p.Master.Close()
}

// FlushInput discards output the slave side hasn't read yet. A writer
// blocked on the master because the slave's buffer was full carries on.
func (p *Pty) FlushInput() error {
	return control(p.hold, flushInput)
}

// control runs fn on f's descriptor without Fd(), which would switch the
// descriptor to blocking mode for good.
func control(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return err
	}
	return ferr
}

// OpenSlave opens the slave side without making it the controlling terminal.
func (p *Pty) OpenSlave() (*os.File, error) {
	return os.OpenFile(p.Slave, os.O_RDWR|syscall.O_NOCTTY, 0)
}

// SlaveForChild opens the slave side in blocking mode, to become a child
// process's standard input and output.
func (p *Pty) SlaveForChild() (*os.File, error) {
	fd, err := syscall.Open(p.Slave, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p.Slave, err)
	}
	if err := MakeRaw(fd); err != nil { // in case anything reset it
		syscall.Close(fd)
		return nil, fmt.Errorf("raw mode on %s: %w", p.Slave, err)
	}
	return os.NewFile(uintptr(fd), p.Slave), nil
}

// MakeRaw puts the terminal on fd into raw mode, as cfmakeraw does.
func MakeRaw(fd int) error {
	var t syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, unsafe.Pointer(&t)); err != nil {
		return err
	}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	return ioctl(fd, ioctlSetTermios, unsafe.Pointer(&t))
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if e != 0 {
		return e
	}
	return nil
}
