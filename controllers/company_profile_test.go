package controllers

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func sampleJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func sampleGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.RGBA{A: 0}, color.RGBA{R: 255, A: 255}})
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

// A minimal but well-formed SVG document -- enough to exercise the sniff +
// rasterize path without needing a real logo file on disk.
const sampleSVGDoc = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 40"><rect x="10" y="10" width="80" height="20" fill="#ff0000"/></svg>`

// There's no dedicated "valid webp" case here: golang.org/x/image/webp is
// decode-only (no Encode), so a valid sample can't be synthesized in-test the
// way PNG/JPEG/GIF/SVG can. Its decoder registration is exercised by
// TestDecodeLogoImage's format dispatch compiling and running at all; the
// decode behavior itself is covered by the upstream library's own tests.
func TestDecodeLogoImage(t *testing.T) {
	tests := []struct {
		name    string
		body    func(t *testing.T) []byte
		wantErr bool
	}{
		{"valid png", samplePNG, false},
		{"valid jpeg", sampleJPEG, false},
		{"valid gif", sampleGIF, false},
		{"valid svg", func(t *testing.T) []byte { return []byte(sampleSVGDoc) }, false},
		{"malformed svg-like bytes", func(t *testing.T) []byte { return []byte("<svg><this is not valid xml") }, true},
		{"garbage bytes", func(t *testing.T) []byte { return []byte("not an image") }, true},
		{"empty body", func(t *testing.T) []byte { return nil }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := decodeLogoAsPNG(tc.body(t))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(out, []byte("\x89PNG")), "output must be re-encoded as PNG")
		})
	}
}

func TestDecodeLogoImage_OversizeRejected(t *testing.T) {
	big := make([]byte, maxLogoSizeBytes+1)
	_, err := decodeLogoAsPNG(big)
	assert.Error(t, err)
}

func TestDecodeLogoImage_SVGIsRasterized(t *testing.T) {
	out, err := decodeLogoAsPNG([]byte(sampleSVGDoc))
	require.NoError(t, err)

	decoded, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Greater(t, decoded.Bounds().Dx(), 0)
	assert.Greater(t, decoded.Bounds().Dy(), 0)
}

// A malicious SVG can declare an arbitrarily large viewBox; rasterizeSVG
// must cap the rendered size rather than allocate however much memory that
// viewBox asks for.
func TestRasterizeSVG_OversizedViewBoxIsCapped(t *testing.T) {
	huge := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100000 100000"><rect width="100000" height="100000" fill="#000"/></svg>`
	img, err := rasterizeSVG([]byte(huge))
	require.NoError(t, err)

	b := img.Bounds()
	assert.LessOrEqual(t, b.Dx(), svgRasterMaxPx)
	assert.LessOrEqual(t, b.Dy(), svgRasterMaxPx)
}

func TestCropToContent(t *testing.T) {
	// 100x100 fully transparent canvas with a solid 20x10 red mark at
	// (40,45)-(60,55), centered -- simulates a padded logo export.
	padded := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := 45; y < 55; y++ {
		for x := 40; x < 60; x++ {
			padded.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	cropped := cropToContent(padded)
	assert.Equal(t, 20, cropped.Bounds().Dx(), "crop width should match the content mark, not the padded canvas")
	assert.Equal(t, 10, cropped.Bounds().Dy(), "crop height should match the content mark, not the padded canvas")

	// Content already fills the canvas -- must return unchanged.
	filled := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			filled.Set(x, y, color.NRGBA{B: 255, A: 255})
		}
	}
	same := cropToContent(filled)
	assert.Equal(t, filled.Bounds(), same.Bounds())

	// Fully blank/transparent image -- must return unchanged, not panic or
	// produce a degenerate (zero-size) crop.
	blank := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	stillBlank := cropToContent(blank)
	assert.Equal(t, blank.Bounds(), stillBlank.Bounds())
}

func TestDecodeLogoImage_CropsPaddedContent(t *testing.T) {
	padded := image.NewNRGBA(image.Rect(0, 0, 200, 200))
	for y := 90; y < 110; y++ {
		for x := 20; x < 180; x++ {
			padded.Set(x, y, color.NRGBA{R: 10, G: 10, B: 10, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, padded))

	out, err := decodeLogoAsPNG(buf.Bytes())
	require.NoError(t, err)

	decoded, err := png.Decode(bytes.NewReader(out))
	require.NoError(t, err)
	assert.Equal(t, 160, decoded.Bounds().Dx(), "upload should be cropped to the actual mark, not stored at the full padded canvas size")
	assert.Equal(t, 20, decoded.Bounds().Dy())
}
