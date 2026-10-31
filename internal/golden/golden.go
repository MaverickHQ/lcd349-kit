// Package golden compares screenshots with reference images (requirement
// 7.8): exact at the panel's RGB565 depth unless a tolerance allows some
// differing pixels, with actual and diff images beside the golden on a
// mismatch, and a missing golden written but failing until accepted.
package golden

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// Result of one compare.
type Result struct {
	OK      bool   `json:"ok"`
	Golden  string `json:"golden"`
	Written bool   `json:"written,omitempty"` // the golden was created or replaced
	Differ  int    `json:"differ"`            // pixels that differ in RGB565
	Actual  string `json:"actual,omitempty"`  // <name>_actual.png, on a mismatch
	Diff    string `json:"diff,omitempty"`    // <name>_diff.png, on a mismatch
	Message string `json:"message"`
}

// Options for a compare.
type Options struct {
	TolerancePx int  // differing pixels allowed
	Update      bool // write the golden from the actual image when it is missing or differs
}

// Compare checks actual (its PNG bytes, written as the golden or the actual
// image) against the golden at path.
func Compare(actualPNG []byte, path string, o Options) (*Result, error) {
	actual, err := png.Decode(bytes.NewReader(actualPNG))
	if err != nil {
		return nil, fmt.Errorf("the screenshot isn't a readable PNG: %w", err)
	}
	r := &Result{Golden: path}
	actualPath, diffPath := sidePaths(path)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := write(path, actualPNG); err != nil {
			return nil, err
		}
		r.Written = true
		removeSide(actualPath, diffPath)
		if o.Update {
			r.OK, r.Message = true, "golden written"
		} else {
			r.Message = "the golden was missing, so it was written from this screenshot: check it, then run again (or use --update-goldens)"
		}
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	golden, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("golden %s isn't a readable PNG: %w", path, err)
	}
	if actual.Bounds().Size() != golden.Bounds().Size() {
		r.Differ = -1
		r.Message = fmt.Sprintf("the screenshot is %v but the golden is %v", actual.Bounds().Size(), golden.Bounds().Size())
	} else {
		diff, n := compare(actual, golden)
		r.Differ = n
		if n <= o.TolerancePx {
			r.OK = true
			r.Message = "matches"
			if n > 0 {
				r.Message = fmt.Sprintf("%d pixels differ, within the tolerance of %d", n, o.TolerancePx)
			}
			removeSide(actualPath, diffPath)
			return r, nil
		}
		r.Message = fmt.Sprintf("%d pixels differ (tolerance %d) in %v", n, o.TolerancePx, bounds(actual, golden))
		if !o.Update {
			if err := writeImage(diffPath, diff); err != nil {
				return nil, err
			}
			r.Diff = diffPath
		}
	}
	if o.Update {
		if err := write(path, actualPNG); err != nil {
			return nil, err
		}
		removeSide(actualPath, diffPath)
		r.OK, r.Written = true, true
		r.Message += "; golden updated"
		return r, nil
	}
	if err := write(actualPath, actualPNG); err != nil {
		return nil, err
	}
	r.Actual = actualPath
	return r, nil
}

// sidePaths are <name>_actual.png and <name>_diff.png beside the golden.
func sidePaths(golden string) (string, string) {
	base := strings.TrimSuffix(golden, filepath.Ext(golden))
	return base + "_actual.png", base + "_diff.png"
}

func removeSide(paths ...string) {
	for _, p := range paths {
		os.Remove(p)
	}
}

func rgb565(c color.Color) uint16 {
	r, g, b, _ := c.RGBA()
	return uint16(r>>11)<<11 | uint16(g>>10)<<5 | uint16(b>>11)
}

// compare counts pixels that differ in RGB565 and draws a diff: the golden
// dimmed to grey, with differing pixels in magenta.
func compare(a, g image.Image) (*image.RGBA, int) {
	ab, gb := a.Bounds(), g.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, ab.Dx(), ab.Dy()))
	n := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ca, cg := a.At(ab.Min.X+x, ab.Min.Y+y), g.At(gb.Min.X+x, gb.Min.Y+y)
			if rgb565(ca) != rgb565(cg) {
				n++
				out.Set(x, y, color.RGBA{255, 0, 255, 255})
				continue
			}
			gray := color.GrayModel.Convert(cg).(color.Gray).Y
			v := 40 + gray/3
			out.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	return out, n
}

// bounds describes where the differing pixels are.
func bounds(a, g image.Image) string {
	ab, gb := a.Bounds(), g.Bounds()
	r := image.Rectangle{}
	first := true
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			if rgb565(a.At(ab.Min.X+x, ab.Min.Y+y)) == rgb565(g.At(gb.Min.X+x, gb.Min.Y+y)) {
				continue
			}
			p := image.Rect(x, y, x+1, y+1)
			if first {
				r, first = p, false
			} else {
				r = r.Union(p)
			}
		}
	}
	return fmt.Sprintf("x %d-%d, y %d-%d", r.Min.X, r.Max.X-1, r.Min.Y, r.Max.Y-1)
}

// write saves a screenshot compressed: the instance sends stored (fast,
// uncompressed) PNGs, and goldens live in repositories. A pHYs chunk (the
// panel's pixel density) is kept.
func write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, Compact(b), 0o644)
}

// Compact re-encodes a PNG at the best compression, keeping its pHYs chunk.
func Compact(b []byte) []byte {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return b
	}
	var out bytes.Buffer
	if (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img) != nil || out.Len() >= len(b) {
		return b
	}
	c := out.Bytes()
	const ihdrEnd = 8 + 4 + 4 + 13 + 4
	if i := bytes.Index(b, []byte("pHYs")); i >= 4 && i+4+9+4 <= len(b) && len(c) > ihdrEnd {
		chunk := b[i-4 : i+4+9+4] // length, type, data, CRC
		c = append(append(append([]byte{}, c[:ihdrEnd]...), chunk...), c[ihdrEnd:]...)
	}
	return c
}

func writeImage(path string, img image.Image) error {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return err
	}
	return write(path, b.Bytes())
}
