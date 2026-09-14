package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

var (
	backdrop = color.RGBA{R: 22, G: 19, B: 15, A: 255}
	accent   = color.RGBA{R: 224, G: 138, B: 44, A: 255}
	// Panel status colors: green = translating service healthy, red =
	// stopped or faulted. Same drawing as the brand icon, recolored so the
	// state reads at 16px next to the clock.
	okAccent  = color.RGBA{R: 63, G: 185, B: 80, A: 255}
	errAccent = color.RGBA{R: 248, G: 81, B: 73, A: 255}
	// dimAccent is the blink-off phase while translating: same hue family
	// as healthy, clearly darker, never the fault red.
	dimAccent = color.RGBA{R: 24, G: 70, B: 34, A: 255}
)

func render(size int) *image.RGBA {
	return renderMark(size, accent)
}

func renderStatus(size int, ok bool) *image.RGBA {
	mark := okAccent
	if !ok {
		mark = errAccent
	}
	return renderMark(size, mark)
}

func renderDim(size int) *image.RGBA {
	return renderMark(size, dimAccent)
}

func renderMark(size int, mark color.RGBA) *image.RGBA {
	if size < 16 {
		size = 32
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: backdrop}, image.Point{}, draw.Src)
	inset := size / 8
	draw.Draw(img, image.Rect(inset, inset, size-inset, size-inset), &image.Uniform{C: mark}, image.Point{}, draw.Src)
	cut := &image.Uniform{C: backdrop}
	draw.Draw(img, image.Rect(size/3, size/4, size-size/5, size/2), cut, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(size/3, size/2, (size*3)/5, size-size/4), cut, image.Point{}, draw.Src)
	return img
}

func RenderPNG(size int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, render(size))
	return buf.Bytes()
}

// IconPixmap returns the icon as StatusNotifierItem ARGB32 pixel data.
func IconPixmap(size int) (int32, int32, []byte) {
	return IconPixmapFor(size, true)
}

// IconPixmapFor renders the green (healthy) or red (stopped/faulted)
// panel variant.
func IconPixmapFor(size int, ok bool) (int32, int32, []byte) {
	img := renderStatus(size, ok)
	return pixmapBytes(img)
}

// IconPixmapDim renders the blink-off phase shown while translating.
func IconPixmapDim(size int) (int32, int32, []byte) {
	img := renderDim(size)
	return pixmapBytes(img)
}

func pixmapBytes(img *image.RGBA) (int32, int32, []byte) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	out := make([]byte, 0, w*h*4)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			out = append(out, byte(a>>8), byte(r>>8), byte(g>>8), byte(b>>8))
		}
	}
	return int32(w), int32(h), out
}

func WritePNG(path string) error {
	return os.WriteFile(path, RenderPNG(128), 0o644)
}
