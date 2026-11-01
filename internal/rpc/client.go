// Package rpc is a client for an emulator instance's control socket:
// JSON-RPC 2.0, one message per line, over <instance>/control.sock.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"time"
)

// Error codes the instance uses besides the JSON-RPC standard ones.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
	CodeUnavailable    = 1 // not possible in the device's current state
	// CodeExited: the product process has exited (crashed); the supervisor
	// answers with the exit and the last serial lines in data.
	CodeExited = -32001
)

// MaxDepth is the deepest a request may nest, arrays and objects, the
// request object itself counting as one. An instance parses each request
// recursively on its control task's small stack, so it refuses deeper ones
// (RPC_MAX_DEPTH in components/lcd349/src/emu/control.h); Call refuses them
// before sending.
const MaxDepth = 16

// maxReply caps a reply line. The largest replies, screenshots and trees,
// are a few hundred kilobytes.
var maxReply = 32 << 20

// Depth returns how deeply the JSON text b nests arrays and objects:
// 0 for a scalar, 1 for {} or []. Brackets inside strings don't count.
func Depth(b []byte) int {
	depth, deepest, inString := 0, 0, false
	for i := 0; i < len(b); i++ {
		switch ch := b[i]; {
		case inString && ch == '\\':
			i++
		case ch == '"':
			inString = !inString
		case inString:
		case ch == '[' || ch == '{':
			depth++
			deepest = max(deepest, depth)
		case ch == ']' || ch == '}':
			depth--
		}
	}
	return deepest
}

// Error is a JSON-RPC error returned by the instance.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("lcd349 instance: %s (code %d)", e.Message, e.Code)
}

// IsCode reports whether err is an instance error with this code.
func IsCode(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Client talks to one instance. Calls are serialised: the instance answers a
// connection's requests in order, one at a time.
type Client struct {
	mu   sync.Mutex
	conn net.Conn
	r    *bufio.Reader
	id   int64
}

// SocketPath is the control socket inside an instance directory.
func SocketPath(instanceDir string) string { return filepath.Join(instanceDir, "control.sock") }

// Dial connects to the instance in dir.
func Dial(ctx context.Context, dir string) (*Client, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", SocketPath(dir))
	if err != nil {
		return nil, err
	}
	return &Client{conn: c, r: bufio.NewReaderSize(c, 1<<20)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Call sends method with params (any JSON-encodable value, or nil) and decodes
// the result into result (a pointer, or nil to discard it).
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.id++
	id := c.id
	line, err := json.Marshal(request{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	if err != nil {
		return err
	}
	if d := Depth(line); d > MaxDepth {
		return fmt.Errorf("%s: the request nests %d arrays and objects deep; an instance takes at most %d", method, d, MaxDepth)
	}
	return c.roundTrip(ctx, append(line, '\n'), id, result)
}

// Raw sends one raw line (for tests of malformed input) and returns the next reply.
func (c *Client) Raw(ctx context.Context, line []byte) (json.RawMessage, *Error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.write(ctx, line); err != nil {
		return nil, nil, err
	}
	resp, err := c.read(ctx)
	if err != nil {
		return nil, nil, err
	}
	return resp.Result, resp.Error, nil
}

func (c *Client) roundTrip(ctx context.Context, line []byte, id int64, result any) error {
	if err := c.write(ctx, line); err != nil {
		return err
	}
	for {
		resp, err := c.read(ctx)
		if err != nil {
			return err
		}
		var got int64
		if json.Unmarshal(resp.ID, &got) != nil || got != id {
			continue // an unsolicited error (id null), such as a refused oversize line
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, result)
	}
}

func (c *Client) write(ctx context.Context, line []byte) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	_, err := c.conn.Write(line)
	return err
}

func (c *Client) read(ctx context.Context) (*response, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(line)+len(chunk) > maxReply {
			return nil, fmt.Errorf("a reply over %d bytes", maxReply)
		}
		line = append(line, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("unreadable reply %.200q: %w", line, err)
	}
	return &resp, nil
}
