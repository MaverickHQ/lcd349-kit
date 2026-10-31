package mcpserver

import (
	"image"
	"image/color"
)

// flip returns img with its colours inverted (a 640x172 test card when img is nil).
func flip(img image.Image) image.Image {
	if img == nil {
		out := image.NewRGBA(image.Rect(0, 0, 640, 172))
		for y := 0; y < 172; y++ {
			for x := 0; x < 640; x++ {
				out.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
			}
		}
		return out
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			out.Set(x, y, color.RGBA{255 - uint8(r>>8), 255 - uint8(g>>8), 255 - uint8(bl>>8), 255})
		}
	}
	return out
}
