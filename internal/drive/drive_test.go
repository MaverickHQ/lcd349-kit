package drive

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

func TestPressParams(t *testing.T) {
	for in, want := range map[string]string{"boot": "boot", "PWR": "pwr", "power": "pwr"} {
		p, err := PressParams(in, 0)
		if err != nil || p["button"] != want || p["ms"] != nil {
			t.Errorf("PressParams(%q) = %v, %v", in, p, err)
		}
	}
	if p, err := PressParams("boot+pwr", 1100); err != nil || p["chord"] != true || p["ms"] != 1100 {
		t.Errorf("chord: %v, %v", p, err)
	}
	for _, bad := range []string{"reset", "", "boot+boot"} {
		if _, err := PressParams(bad, 0); err == nil {
			t.Errorf("PressParams(%q) accepted", bad)
		}
	}
	if _, err := PressParams("boot", 10); err == nil {
		t.Error("a 10 ms press was accepted")
	}
}

func TestTouchValidate(t *testing.T) {
	if (Touch{Tap: &Point{1, 2}}).Validate() != nil {
		t.Error("a tap was refused")
	}
	if (Touch{}).Validate() == nil || (Touch{Tap: &Point{1, 2}, LongPress: &Point{1, 2}}).Validate() == nil {
		t.Error("zero or two gestures were accepted")
	}
}

// TestWithDensity: the pHYs chunk is valid (Go's decoder checks every CRC)
// and says 7500 pixels per metre at 7.5 px/mm.
func TestWithDensity(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 2)))
	out := WithDensity(b.Bytes(), 7.5)
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(out, []byte("pHYs"))
	if i < 0 || i != 8+8+13+4+4 {
		t.Fatalf("pHYs at %d, want right after IHDR", i)
	}
	if ppm := binary.BigEndian.Uint32(out[i+4:]); ppm != 7500 || out[i+12] != 1 {
		t.Fatalf("pHYs %d px/unit, unit %d", ppm, out[i+12])
	}
	if again := WithDensity(out, 7.5); !bytes.Equal(again, out) {
		t.Fatal("a second pHYs was added")
	}
}
