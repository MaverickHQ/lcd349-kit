package instance

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MaverickHQ/lcd349-kit/internal/pty"
)

// TestRelayNeverStallsTheProduct: with nobody reading the serial port, the
// product's output still flows into serial.log at full speed (task 10.2,
// KN9: a stalled relay blocked the product's console). A tool that opens
// the port later gets new output, and its input reaches the product.
func TestRelayNeverStallsTheProduct(t *testing.T) {
	a, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	log, err := openSerialLog(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer log.close()
	(&relay{a: a, b: b, log: log}).start()

	product, err := a.SlaveForChild()
	if err != nil {
		t.Fatal(err)
	}
	defer product.Close()

	// 200 KB, hundreds of times what the port holds.
	const lines = 4000
	done := make(chan error, 1)
	go func() {
		w := bufio.NewWriter(product)
		for i := range lines {
			fmt.Fprintf(w, "I (%d) soak: line %d of the product's output, nobody reading\n", i, i)
		}
		done <- w.Flush()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the product blocked writing its console: the relay stalled")
	}
	waitFor(t, func() bool { log.mu.Lock(); defer log.mu.Unlock(); return log.linesCur == lines }, "every line in serial.log")

	port, err := b.OpenSlave()
	if err != nil {
		t.Fatal(err)
	}
	defer port.Close()
	fmt.Fprintf(product, "after a reader came\n")
	got := make(chan string, 1)
	go func() {
		r := bufio.NewReader(port)
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "after") {
				got <- l
				return
			}
		}
	}()
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("a reader that opened the port later got no new output")
	}

	fmt.Fprintf(port, "@P ping\n")
	in := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(product).ReadString('\n')
		in <- l
	}()
	select {
	case l := <-in:
		if l != "@P ping\n" {
			t.Fatalf("the product read %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("input from the port never reached the product")
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
