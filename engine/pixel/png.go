// Ported from pxpipe (https://github.com/teamchong/pxpipe), MIT License, Copyright (c) 2026 claude-image-proxy contributors.

package pixel

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

type RenderedImage struct {
	PNG               []byte
	Width, Height     int
	CharsRendered     int
	DroppedChars      int
	DroppedCodepoints map[rune]int
	// Layers is the number of interleaved text layers baked into this image: 1
	// for an ordinary page, 2 for a max-level red/blue overlay page. Zero is
	// treated as 1 by callers (existing single-layer renderers leave it unset).
	Layers int
}

func encodeGrayPNG(pixels []uint8, width, height int) ([]byte, error) {
	if len(pixels) != width*height {
		return nil, fmt.Errorf("encodeGrayPNG: pixels length %d != %dx%d", len(pixels), width, height)
	}
	img := image.NewGray(image.Rect(0, 0, width, height))
	copy(img.Pix, pixels)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeRGBPNG(pixels []uint8, width, height int) ([]byte, error) {
	if len(pixels) != width*height*3 {
		return nil, fmt.Errorf("encodeRGBPNG: pixels length %d != %dx%dx3", len(pixels), width, height)
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < width*height; i++ {
		img.SetNRGBA(i%width, i/width, color.NRGBA{
			R: pixels[i*3],
			G: pixels[i*3+1],
			B: pixels[i*3+2],
			A: 255,
		})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
