package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"
)

func TestDecodeFirstGIFFrame_ReturnsImage(t *testing.T) {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.Transparent}
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), pal)
	for y := range 2 {
		for x := range 2 {
			img.SetColorIndex(x, y, 0)
		}
	}
	g := &gif.GIF{
		Image: []*image.Paletted{img},
		Delay: []int{0},
		Config: image.Config{
			Width:      2,
			Height:     2,
			ColorModel: pal,
		},
		BackgroundIndex: 1,
	}

	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encode gif: %v", err)
	}

	out, err := decodeFirstGIFFrame(buf.Bytes())
	if err != nil {
		t.Fatalf("decodeFirstGIFFrame: %v", err)
	}
	if out == nil {
		t.Fatalf("expected image, got nil")
	}

	jpg := encodeJPEG(out, 80)
	if len(jpg) == 0 {
		t.Fatalf("expected jpeg bytes")
	}
	if _, err := jpeg.Decode(bytes.NewReader(jpg)); err != nil {
		t.Fatalf("jpeg decode failed: %v", err)
	}
}
