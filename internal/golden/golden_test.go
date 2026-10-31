package golden

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int, paint func(x, y int) color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, paint(x, y))
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

var blue = func(x, y int) color.Color { return color.RGBA{0, 0, 255, 255} }

func TestMissingGoldenIsWrittenAndFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "goldens", "a.png")
	r, err := Compare(pngOf(t, 8, 4, blue), path, Options{})
	if err != nil || r.OK || !r.Written || !strings.Contains(r.Message, "missing") {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the golden wasn't written")
	}
	// The same image now matches.
	if r, err = Compare(pngOf(t, 8, 4, blue), path, Options{}); err != nil || !r.OK || r.Differ != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	// With --update-goldens a missing golden passes.
	path2 := filepath.Join(t.TempDir(), "b.png")
	if r, err = Compare(pngOf(t, 8, 4, blue), path2, Options{Update: true}); err != nil || !r.OK || !r.Written {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestMismatchWritesActualAndDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(path, pngOf(t, 8, 4, blue), 0o644)
	changed := func(x, y int) color.Color {
		if x == 2 && y == 1 {
			return color.RGBA{255, 0, 0, 255}
		}
		return blue(x, y)
	}
	r, err := Compare(pngOf(t, 8, 4, changed), path, Options{})
	if err != nil || r.OK || r.Differ != 1 || r.Actual == "" || r.Diff == "" || !strings.Contains(r.Message, "x 2-2, y 1-1") {
		t.Fatalf("%+v %v", r, err)
	}
	b, _ := os.ReadFile(r.Diff)
	diff, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if cr, _, cb, _ := diff.At(2, 1).RGBA(); cr>>8 != 255 || cb>>8 != 255 {
		t.Fatalf("the differing pixel isn't marked in the diff")
	}
	// A tolerance of one pixel passes and cleans the side images up.
	if r, err = Compare(pngOf(t, 8, 4, changed), path, Options{TolerancePx: 1}); err != nil || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".png") + "_diff.png"); !os.IsNotExist(err) {
		t.Fatal("the diff image was left after a pass")
	}
	// --update-goldens replaces it.
	if r, err = Compare(pngOf(t, 8, 4, changed), path, Options{Update: true}); err != nil || !r.OK || !r.Written {
		t.Fatalf("%+v %v", r, err)
	}
	if r, _ = Compare(pngOf(t, 8, 4, changed), path, Options{}); !r.OK {
		t.Fatal("the updated golden doesn't match")
	}
}

// TestComparesAtRGB565: differences the panel can't show don't count.
func TestComparesAtRGB565(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.png")
	os.WriteFile(path, pngOf(t, 4, 4, func(x, y int) color.Color { return color.RGBA{200, 100, 50, 255} }), 0o644)
	near := func(x, y int) color.Color { return color.RGBA{203, 101, 52, 255} } // same RGB565 value
	if r, err := Compare(pngOf(t, 4, 4, near), path, Options{}); err != nil || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if r, _ := Compare(pngOf(t, 4, 3, near), path, Options{}); r.OK || !strings.Contains(r.Message, "golden is") {
		t.Fatalf("a size change: %+v", r)
	}
}

func TestCompactKeepsPixelsAndDensity(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for x := 0; x < 64; x++ {
		img.Set(x, x/2, color.RGBA{255, 255, 255, 255})
	}
	var b bytes.Buffer
	(&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&b, img)
	// A pHYs chunk after IHDR, as drive.WithDensity writes (7500 px/m).
	phys := []byte{0, 0, 0, 9, 'p', 'H', 'Y', 's', 0, 0, 0x1d, 0x4c, 0, 0, 0x1d, 0x4c, 1, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(phys[17:], crc32.ChecksumIEEE(phys[4:17]))
	raw := append(append(append([]byte{}, b.Bytes()[:33]...), phys...), b.Bytes()[33:]...)
	c := Compact(raw)
	if len(c) >= len(raw) || !bytes.Contains(c, phys[4:17]) {
		t.Fatalf("compact: %d -> %d bytes, pHYs kept %v", len(raw), len(c), bytes.Contains(c, phys[4:17]))
	}
	got, err := png.Decode(bytes.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	if _, diff := compare(got, img); diff != 0 {
		t.Fatalf("%d pixels changed", diff)
	}
}
