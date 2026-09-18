package controllers

import (
	"bytes"
	"image"
	"image/color"
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

func TestDecodeLogoImage(t *testing.T) {
	tests := []struct {
		name    string
		body    func(t *testing.T) []byte
		wantErr bool
	}{
		{"valid png", samplePNG, false},
		{"valid jpeg", sampleJPEG, false},
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
