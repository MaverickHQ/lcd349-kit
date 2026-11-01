package rpc

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// nest returns a value whose JSON nests n arrays deep.
func nest(n int) any {
	var v any = 1
	for i := 0; i < n; i++ {
		v = []any{v}
	}
	return v
}

// fakeInstance answers every request line on conn with reply.
func fakeInstance(t *testing.T, conn net.Conn, reply string) {
	t.Helper()
	go func() {
		r := bufio.NewReader(conn)
		for {
			if _, err := r.ReadBytes('\n'); err != nil {
				return
			}
			if _, err := conn.Write([]byte(reply)); err != nil {
				return
			}
		}
	}()
}

func TestDepth(t *testing.T) {
	for in, want := range map[string]int{
		`1`: 0, `{}`: 1, `[]`: 1, `[[1],{"a":[]}]`: 3, `{"a":"[[[{{"}`: 1, `{"a":"\"[["}`: 1,
	} {
		if got := Depth([]byte(in)); got != want {
			t.Errorf("Depth(%s) = %d, want %d", in, got, want)
		}
	}
}

// TestCallRefusesDeepRequests: a request nesting deeper than MaxDepth (the
// request object counting as one) is refused before it is sent: an
// instance parses requests recursively, on a small stack.
func TestCallRefusesDeepRequests(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	fakeInstance(t, b, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n")
	c := &Client{conn: a, r: bufio.NewReader(a)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The request, params and x take three levels.
	if err := c.Call(ctx, "state.set", map[string]any{"x": nest(MaxDepth - 2)}, nil); err != nil {
		t.Fatalf("a request %d deep: %v", MaxDepth, err)
	}
	err := c.Call(ctx, "state.set", map[string]any{"x": nest(MaxDepth - 1)}, nil)
	if err == nil || !strings.Contains(err.Error(), "nests") {
		t.Fatalf("a request %d deep: %v; want a refusal", MaxDepth+1, err)
	}
}

// TestReadRefusesAnOversizeReply: a reply line is read up to MaxReply
// bytes, never without bound.
func TestReadRefusesAnOversizeReply(t *testing.T) {
	old := maxReply
	maxReply = 1 << 10
	defer func() { maxReply = old }()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	fakeInstance(t, b, `{"jsonrpc":"2.0","id":1,"result":"`+strings.Repeat("x", 4<<10)+`"}`+"\n")
	c := &Client{conn: a, r: bufio.NewReaderSize(a, 256)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Call(ctx, "info", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("an oversize reply: %v; want a refusal", err)
	}
}
