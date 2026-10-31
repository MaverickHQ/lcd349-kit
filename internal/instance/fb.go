package instance

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
)

// FrameHeader is the part of fb.bin's 64-byte header the Go tools read
// (src/emu/emu.h: emu_fb_hdr_t; offsets are checked there).
type FrameHeader struct {
	Seq       uint32
	Frames    uint32
	PID       uint32
	Backlight uint8
	Power     uint8
}

// ReadFrameHeader reads the framebuffer header of the instance in dir.
func ReadFrameHeader(dir string) (FrameHeader, error) {
	f, err := os.Open(filepath.Join(dir, "fb.bin"))
	if err != nil {
		return FrameHeader{}, err
	}
	defer f.Close()
	var b [64]byte
	if _, err := f.ReadAt(b[:], 0); err != nil {
		return FrameHeader{}, err
	}
	if string(b[:8]) != "L349FB01" {
		return FrameHeader{}, errors.New("fb.bin has no valid header")
	}
	le := binary.LittleEndian
	return FrameHeader{Seq: le.Uint32(b[16:]), Frames: le.Uint32(b[20:]), PID: le.Uint32(b[32:]), Backlight: b[36], Power: b[37]}, nil
}
