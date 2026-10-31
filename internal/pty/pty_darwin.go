package pty

import (
	"bytes"
	"fmt"
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)

func unlockpt(fd int) (string, error) {
	if err := ioctl(fd, syscall.TIOCPTYGRANT, nil); err != nil {
		return "", fmt.Errorf("grant pty: %w", err)
	}
	if err := ioctl(fd, syscall.TIOCPTYUNLK, nil); err != nil {
		return "", fmt.Errorf("unlock pty: %w", err)
	}
	var name [128]byte
	if err := ioctl(fd, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		return "", fmt.Errorf("pty name: %w", err)
	}
	return string(name[:bytes.IndexByte(name[:], 0)]), nil
}

func flushInput(fd int) error {
	which := int32(1) // FREAD
	return ioctl(fd, syscall.TIOCFLUSH, unsafe.Pointer(&which))
}
