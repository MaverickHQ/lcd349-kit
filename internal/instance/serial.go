package instance

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/pty"
)

// LogLimit is the size at which serial.log is rotated to serial.log.1.
const LogLimit = 10 << 20

// serialLog is the instance's serial.log, plus the last lines kept for crash
// records and an optional copy for a foreground `lcd349 run`.
type serialLog struct {
	mu        sync.Mutex
	dir       string
	f         *os.File
	size      int64
	limit     int64
	echo      io.Writer
	ring      [CrashLines]string
	ringN     int // lines pushed so far
	partial   []byte
	linesCur  int // complete lines in serial.log
	linesPrev int // in serial.log.1
	before    int // in logs rotated out before serial.log.1
	onRotate  func(before int)
	lastWrite time.Time
}

func openSerialLog(dir string, echo io.Writer, onRotate func(int)) (*serialLog, error) {
	f, err := os.OpenFile(filepath.Join(dir, "serial.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &serialLog{dir: dir, f: f, size: st.Size(), limit: LogLimit, echo: echo, onRotate: onRotate}, nil
}

func (l *serialLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastWrite = time.Now()
	if l.size+int64(len(p)) > l.limit && l.size > 0 {
		l.rotate()
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	if l.echo != nil {
		l.echo.Write(p)
	}
	for _, c := range p {
		if c == '\n' {
			l.push(string(bytes.TrimSuffix(l.partial, []byte{'\r'})))
			l.partial = l.partial[:0]
			l.linesCur++
		} else if len(l.partial) < 4096 {
			l.partial = append(l.partial, c)
		}
	}
	return n, err
}

// note writes a supervisor line (prefixed "lcd349: ") to serial.log and the
// echo only: never to the serial port, which carries the product's bytes.
func (l *serialLog) note(s string) {
	l.Write([]byte("lcd349: " + s + "\n"))
}

func (l *serialLog) rotate() {
	l.f.Close()
	old := filepath.Join(l.dir, "serial.log")
	os.Rename(old, old+".1")
	f, err := os.OpenFile(old, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		f, _ = os.Open(os.DevNull)
	}
	l.f = f
	l.size = 0
	l.before += l.linesPrev
	l.linesPrev, l.linesCur = l.linesCur, 0
	if l.onRotate != nil {
		l.onRotate(l.before)
	}
}

func (l *serialLog) push(line string) {
	l.ring[l.ringN%CrashLines] = line
	l.ringN++
}

// last returns up to the last n complete lines.
func (l *serialLog) last(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n > CrashLines {
		n = CrashLines
	}
	if n > l.ringN {
		n = l.ringN
	}
	out := make([]string, 0, n)
	for i := l.ringN - n; i < l.ringN; i++ {
		out = append(out, l.ring[i%CrashLines])
	}
	return out
}

// quiet waits, from now, until nothing has been written for gap (at most
// max): the product's last output can still be in the pty after it exits.
func (l *serialLog) quiet(gap, max time.Duration) {
	start := time.Now()
	for time.Since(start) < max {
		l.mu.Lock()
		last := l.lastWrite
		l.mu.Unlock()
		if last.Before(start) {
			last = start
		}
		if time.Since(last) >= gap {
			return
		}
		time.Sleep(gap / 4)
	}
}

func (l *serialLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Close()
}

// relay copies between the product's pty (A) and the published serial port
// (B), teeing the product's output into the log. Output nobody reads is
// discarded oldest first, as a USB serial port with no host reading drops
// it: the relay never stalls the product.
//
// Every pty descriptor is blocking and read by its own goroutine (a thread
// in a system call): the runtime's poller doesn't serve pty masters reliably
// on macOS. Output for B waits in a bounded queue for one writer, so a port
// nobody reads can block only that writer, never the product's output.
type relay struct {
	a, b *pty.Pty
	log  *serialLog

	mu      sync.Mutex
	ready   *sync.Cond // the queue has bytes
	pending []byte     // output waiting for the port
}

// hostQueue is how much output may wait for the port before the oldest is
// discarded: a port opened after a long time unread shows a little of the
// past, then the present.
const hostQueue = 16 << 10

func (r *relay) start() {
	r.ready = sync.NewCond(&r.mu)
	go r.productToHost()
	go r.hostWriter()
	go r.hostToProduct()
}

func (r *relay) productToHost() {
	buf := make([]byte, 4096)
	for {
		n, err := r.a.Master.Read(buf)
		if n > 0 {
			r.log.Write(buf[:n])
			r.toHost(buf[:n])
		}
		if err != nil && !retryable(err) {
			return // the pty was closed: the supervisor is stopping
		}
		if err != nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// retryable: interrupted, or the slave side hung up (between product
// boots); the next boot opens it again.
func retryable(err error) bool {
	return errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EIO) || errors.Is(err, io.EOF)
}

// toHost queues output for the port without waiting. Past hostQueue the
// oldest is discarded, along with what the port itself holds unread: that
// frees the writer if it's stuck behind a port nobody reads.
func (r *relay) toHost(p []byte) {
	r.mu.Lock()
	r.pending = append(r.pending, p...)
	over := len(r.pending) > hostQueue
	if over {
		r.pending = append(r.pending[:0], r.pending[len(r.pending)-hostQueue:]...)
	}
	r.ready.Signal()
	r.mu.Unlock()
	if over {
		r.b.FlushInput()
	}
}

func (r *relay) hostWriter() {
	chunk := make([]byte, 0, 4096)
	for {
		r.mu.Lock()
		for len(r.pending) == 0 {
			r.ready.Wait()
		}
		n := min(len(r.pending), cap(chunk))
		chunk = append(chunk[:0], r.pending[:n]...)
		r.pending = append(r.pending[:0], r.pending[n:]...)
		r.mu.Unlock()
		for p := chunk; len(p) > 0; {
			n, err := r.b.Master.Write(p)
			p = p[max(n, 0):]
			if err != nil && !retryable(err) {
				return
			}
			if err != nil {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}
}

func (r *relay) hostToProduct() {
	buf := make([]byte, 4096)
	for {
		n, err := r.b.Master.Read(buf)
		if n > 0 {
			if _, werr := r.a.Master.Write(buf[:n]); werr != nil && !retryable(werr) {
				return
			}
		}
		if err != nil && !retryable(err) {
			return
		}
		if err != nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
}
