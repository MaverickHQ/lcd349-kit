package pty

import (
	"bytes"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerOnlyRaw(t *testing.T) {
	p, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	st, err := os.Stat(p.Slave)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("slave %s mode %v, want 0600", p.Slave, st.Mode().Perm())
	}
	s, err := p.OpenSlave()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Raw both ways: CR, LF and control bytes pass unchanged, with no echo.
	msg := []byte("a\r\nb\x03\x04\n")
	if _, err := p.Master.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := readN(t, s, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("slave read %q, want %q", got, msg)
	}
	if _, err := s.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, p.Master, 2); string(got) != "x\n" {
		t.Fatalf("master read %q, want %q (no echo, no CR added)", got, "x\n")
	}
}

// TestRawSurvivesReopen: host tools open and close the port; macOS resets a
// terminal's settings at its last close, so the pty holds a descriptor.
func TestRawSurvivesReopen(t *testing.T) {
	p, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := 0; i < 2; i++ {
		s, err := p.OpenSlave()
		if err != nil {
			t.Fatal(err)
		}
		p.Master.Write([]byte("\x04x\n"))
		if got := readN(t, s, 3); string(got) != "\x04x\n" {
			t.Fatalf("open %d: read %q; the slave isn't raw", i+1, got)
		}
		s.Close()
	}
}

// TestFlushInputFreesABlockedWriter: with nobody reading the port, a
// writer blocks once the buffer is full (under a kilobyte on macOS);
// discarding the waiting input lets it carry on. The relay relies on this.
func TestFlushInputFreesABlockedWriter(t *testing.T) {
	p, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var written atomic.Int64
	go func() {
		chunk := make([]byte, 256)
		for {
			n, err := p.Master.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	// Let the buffer fill and the writer block.
	var stuck int64
	for i := 0; ; i++ {
		time.Sleep(100 * time.Millisecond)
		n := written.Load()
		if n == stuck && n > 0 {
			break
		}
		if i == 50 {
			t.Fatalf("the writer never blocked (%d bytes written)", n)
		}
		stuck = n
	}
	if err := p.FlushInput(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for written.Load() == stuck {
		if time.Now().After(deadline) {
			t.Fatalf("the writer stayed blocked at %d bytes although its pending input was discarded", stuck)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFlushInput(t *testing.T) {
	p, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	s, err := p.OpenSlave()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p.Master.Write([]byte("stale\n"))
	time.Sleep(50 * time.Millisecond)
	if err := p.FlushInput(); err != nil {
		t.Fatal(err)
	}
	p.Master.Write([]byte("new\n"))
	if got := readN(t, s, 4); string(got) != "new\n" {
		t.Fatalf("read %q after flush, want %q", got, "new\n")
	}
}

func readN(t *testing.T, f *os.File, n int) []byte {
	t.Helper()
	ch := make(chan []byte, 1)
	go func() {
		b := make([]byte, n)
		io.ReadFull(f, b)
		ch <- b
	}()
	select {
	case b := <-ch:
		return b
	case <-time.After(3 * time.Second):
		t.Fatalf("no %d bytes within 3 s", n)
		return nil
	}
}
