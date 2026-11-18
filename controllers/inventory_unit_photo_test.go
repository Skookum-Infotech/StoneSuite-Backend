package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckUnitPhoto(t *testing.T) {
	tests := []struct {
		name        string
		body        []byte
		wantType    string
		wantExt     string
		wantMsgPart string
	}{
		{name: "png accepted", body: samplePNG(t), wantType: "image/png", wantExt: "png"},
		{name: "jpeg accepted", body: sampleJPEG(t), wantType: "image/jpeg", wantExt: "jpg"},
		{name: "gif refused", body: sampleGIF(t), wantMsgPart: "JPEG, PNG or WEBP"},
		{name: "empty refused", body: nil, wantMsgPart: "No image data"},
		{name: "not an image refused", body: []byte("hello, not a picture"), wantMsgPart: "not a readable image"},
		{name: "over the size cap refused", body: make([]byte, maxUnitPhotoBytes+1), wantMsgPart: "too large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			contentType, ext, msg := checkUnitPhoto(tc.body)
			if tc.wantMsgPart != "" {
				assert.Contains(t, msg, tc.wantMsgPart)
				assert.Empty(t, contentType)
				return
			}
			assert.Empty(t, msg)
			assert.Equal(t, tc.wantType, contentType)
			assert.Equal(t, tc.wantExt, ext)
		})
	}
}
