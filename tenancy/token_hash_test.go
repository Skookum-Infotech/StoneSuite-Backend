package tenancy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashToken(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"known value", "abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HashToken(tt.raw)
			assert.Equal(t, tt.want, got)
			assert.Len(t, got, 64, "must fit every token column, incl. VARCHAR(64)")
		})
	}
}
