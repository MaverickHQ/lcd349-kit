package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/MaverickHQ/lcd349-kit/internal/mcpserver"
)

func init() {
	register(&command{name: "mcp", usage: "[--root dir]", summary: "serve the tools over MCP on stdin and stdout (the Claude Code plugin runs this)", run: cmdMCP})
}

func cmdMCP(c *cmdCtx) int {
	root := c.flags.String("root", "", "the workspace `directory` (default: the working directory)")
	if code, ok := c.parse(); !ok {
		return code
	}
	s, err := mcpserver.New(mcpserver.Options{Root: *root})
	if err != nil {
		return c.fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	// The session ends when the client closes stdin, once every request
	// read has been answered; the instances started here stop with it (R7.6).
	if err := s.Serve(ctx, drainingStdio(os.Stdin, os.Stdout, drainLimit)); err != nil && ctx.Err() == nil {
		return c.fail(err)
	}
	return exitOK
}

// drainLimit bounds how long the end of stdin waits for answers.
const drainLimit = 2 * time.Minute

// drainingStdio is the stdio transport, except that the end of in is held
// back until every request read from it has been answered on out, or
// limit passes. go-sdk writes nothing once its reader has seen EOF, so a
// client that sends its requests and closes stdin at once
// (printf ... | lcd349 mcp) would otherwise get no answers.
func drainingStdio(in io.ReadCloser, out io.Writer, limit time.Duration) mcp.Transport {
	d := &drain{pending: map[string]bool{}, answered: map[string]bool{}, changed: make(chan struct{})}
	return &mcp.IOTransport{
		Reader: &drainReader{d: d, r: in, limit: limit},
		Writer: &drainWriter{d: d, w: out},
	}
}

// drain tracks the ids of the requests read and not yet answered. go-sdk
// decodes a request as soon as its JSON ends, so it can answer one whose
// line's newline hasn't arrived yet (the last line of stdin, say): that
// answer goes in answered, and cancels the request when its line ends.
type drain struct {
	mu       sync.Mutex
	pending  map[string]bool
	answered map[string]bool
	changed  chan struct{} // closed, and replaced, whenever pending shrinks
}

// scan finds the complete lines in *buf after appending p, and for each
// message on them adds a request's id (in) or removes a response's (out).
func (d *drain) scan(buf *[]byte, p []byte, in bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*buf = append(*buf, p...)
	for {
		i := bytes.IndexByte(*buf, '\n')
		if i < 0 {
			return
		}
		d.line((*buf)[:i], in)
		*buf = (*buf)[i+1:]
	}
}

// line handles one line: a message, or a batch of them.
func (d *drain) line(line []byte, in bool) {
	var batch []json.RawMessage
	if json.Unmarshal(line, &batch) != nil {
		batch = []json.RawMessage{line}
	}
	for _, raw := range batch {
		var m struct {
			ID     json.RawMessage  `json:"id"`
			Method *json.RawMessage `json:"method"`
		}
		if json.Unmarshal(raw, &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
			continue
		}
		var id any
		if json.Unmarshal(m.ID, &id) != nil {
			continue
		}
		key, _ := json.Marshal(id) // one spelling for 1 and 1.0, say
		k := string(key)
		switch {
		case in && m.Method != nil && d.answered[k]: // a request answered already
			delete(d.answered, k)
		case in && m.Method != nil: // a request
			d.pending[k] = true
		case !in && m.Method == nil && d.pending[k]: // its response
			delete(d.pending, k)
			close(d.changed)
			d.changed = make(chan struct{})
		case !in && m.Method == nil: // a response to a request whose line hasn't ended
			d.answered[k] = true
		}
	}
}

// wait returns once nothing is pending, or at deadline.
func (d *drain) wait(deadline time.Time) {
	for {
		d.mu.Lock()
		n, changed := len(d.pending), d.changed
		d.mu.Unlock()
		if n == 0 {
			return
		}
		select {
		case <-changed:
		case <-time.After(time.Until(deadline)):
			return
		}
	}
}

type drainReader struct {
	d     *drain
	r     io.ReadCloser
	buf   []byte
	limit time.Duration
}

func (r *drainReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.d.scan(&r.buf, p[:n], true)
	if err == io.EOF {
		r.d.scan(&r.buf, []byte("\n"), true) // a last line with no newline
		r.d.wait(time.Now().Add(r.limit))
	}
	return n, err
}

func (r *drainReader) Close() error { return r.r.Close() }

type drainWriter struct {
	d   *drain
	w   io.Writer
	buf []byte
}

func (w *drainWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.d.scan(&w.buf, p[:n], false)
	return n, err
}

func (w *drainWriter) Close() error { return nil }
