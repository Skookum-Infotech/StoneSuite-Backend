package kitref

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const sampleUUID = "3F2A9C1E-5B7D-4E8A-9C21-0A1B2C3D4E5F"

func TestNormalize(t *testing.T) {
	lower := strings.ToLower(sampleUUID)

	tests := []struct {
		name     string
		uuid     string
		kitName  string
		wantRef  Ref
		wantMsg  string
		wantKit  bool
		wantName string
	}{
		{name: "no kit", uuid: "", kitName: "", wantRef: Ref{}},
		{name: "blank uuid ignores a stray name", uuid: "  ", kitName: "Kitchen", wantRef: Ref{}},
		{name: "valid kit is lower-cased and trimmed", uuid: " " + sampleUUID + " ", kitName: "  Kitchen  ",
			wantRef: Ref{UUID: &lower, Name: "Kitchen"}, wantKit: true, wantName: "Kitchen"},
		{name: "malformed uuid", uuid: "not-a-uuid", kitName: "Kitchen", wantMsg: "the kit id is not valid."},
		{name: "uuid without hyphens", uuid: strings.ReplaceAll(sampleUUID, "-", ""), kitName: "Kitchen",
			wantMsg: "the kit id is not valid."},
		{name: "kit without a name", uuid: sampleUUID, kitName: "   ", wantMsg: "a kit line needs the kit's name."},
		{name: "name at the limit", uuid: sampleUUID, kitName: strings.Repeat("é", MaxNameLen),
			wantRef: Ref{UUID: &lower, Name: strings.Repeat("é", MaxNameLen)}, wantKit: true,
			wantName: strings.Repeat("é", MaxNameLen)},
		{name: "name over the limit", uuid: sampleUUID, kitName: strings.Repeat("a", MaxNameLen+1),
			wantMsg: "the kit name is too long (100 characters at most)."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, msg := Normalize(tc.uuid, tc.kitName)
			assert.Equal(t, tc.wantMsg, msg)
			assert.Equal(t, tc.wantRef, ref)
			assert.Equal(t, tc.wantKit, ref.IsSet())
			assert.Equal(t, tc.wantName, ref.Name)
		})
	}
}

func TestRefArgs(t *testing.T) {
	uuid, name := Ref{}.Args()
	assert.Nil(t, uuid, "a line outside any kit binds SQL NULL")
	assert.Equal(t, "", name)

	id := "3f2a9c1e-5b7d-4e8a-9c21-0a1b2c3d4e5f"
	uuid, name = Ref{UUID: &id, Name: "Kitchen"}.Args()
	assert.Equal(t, id, uuid)
	assert.Equal(t, "Kitchen", name)
}
