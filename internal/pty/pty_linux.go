package pty

import (
	"fmt"
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = syscall.TCGETS
	ioctlSetTermios = syscall.TCSETS
)

func unlockpt(fd int) (string, error) {
	var n uint32
	if err := ioctl(fd, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		return "", fmt.Errorf("pty number: %w", err)
	}
	var unlock int32
	if err := ioctl(fd, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return "", fmt.Errorf("unlock pty: %w", err)
	}
	return fmt.Sprintf("/dev/pts/%d", n), nil
}

func flushInput(fd int) error {
	const tcflsh, tciflush = 0x540B, 0 // asm-generic ioctls.h, termbits.h
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), tcflsh, tciflush)
	if e != 0 {
		return e
	}
	return nil
}
