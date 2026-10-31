package pty

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// OpenSerial opens a serial port, such as the board's USB Serial/JTAG port,
// in raw mode with DTR and RTS low: esptool's "run" state, so the chip is
// neither held in reset nor started in download mode (tools/boardtest).
// Opening still resets the ESP32-S3, because the OS raises both lines first.
// The descriptor is blocking, so each reader gets its own thread, as for a
// pty master. A port that has no modem lines (a pty) only gets raw mode.
func OpenSerial(path string) (*os.File, error) {
	// O_NONBLOCK so the open doesn't wait for a carrier; cleared below.
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	fail := func(what string, err error) (*os.File, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s on %s: %w", what, path, err)
	}
	if err := MakeRaw(fd); err != nil {
		return fail("raw mode", err)
	}
	var t syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, unsafe.Pointer(&t)); err != nil {
		return fail("terminal settings", err)
	}
	t.Cflag |= syscall.CLOCAL | syscall.CREAD
	if err := ioctl(fd, ioctlSetTermios, unsafe.Pointer(&t)); err != nil {
		return fail("terminal settings", err)
	}
	if err := syscall.SetNonblock(fd, false); err != nil {
		return fail("blocking mode", err)
	}
	lines := int32(syscall.TIOCM_DTR | syscall.TIOCM_RTS)
	if err := ioctl(fd, syscall.TIOCMBIC, unsafe.Pointer(&lines)); err != nil && err != syscall.ENOTTY && err != syscall.EINVAL {
		return fail("lowering DTR and RTS", err)
	}
	return os.NewFile(uintptr(fd), path), nil
}
