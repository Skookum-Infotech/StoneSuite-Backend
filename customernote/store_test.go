package customernote

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"trims whitespace", "  I have an issue with X  ", "I have an issue with X", false},
		{"empty rejected", "", "", true},
		{"whitespace-only rejected", "   ", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateBody(tc.body)
			if tc.wantErr {
				assert.Error(t, err)
				assert.True(t, IsClientError(err))
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNullableInt(t *testing.T) {
	assert.Nil(t, nullableInt(0))
	assert.Nil(t, nullableInt(-1))
	assert.Equal(t, 5, nullableInt(5))
}

func TestActorOrSystem(t *testing.T) {
	assert.Equal(t, systemEmployeeID, actorOrSystem(0))
	assert.Equal(t, 7, actorOrSystem(7))
}
