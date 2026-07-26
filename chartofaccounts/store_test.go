package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidAccountUUID covers the client-input guard (review finding 4) that
// turns a malformed uuid string into a 400 ClientError before it ever reaches
// a query, instead of a 22P02 the store can only render as a 500.
func TestValidAccountUUID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "well-formed lowercase uuid", in: "3fa85f64-5717-4562-b3fc-2c963f66afa6", want: true},
		{name: "well-formed uppercase uuid", in: "3FA85F64-5717-4562-B3FC-2C963F66AFA6", want: true},
		{name: "empty string", in: "", want: false},
		{name: "garbage string", in: "not-a-uuid", want: false},
		{name: "missing hyphens", in: "3fa85f6457174562b3fc2c963f66afa6", want: false},
		{name: "one character short", in: "3fa85f64-5717-4562-b3fc-2c963f66afa", want: false},
		{name: "one character long", in: "3fa85f64-5717-4562-b3fc-2c963f66afa66", want: false},
		{name: "non-hex character", in: "3fa85f64-5717-4562-b3fc-2c963f66afg6", want: false},
		{name: "sql injection attempt", in: "'; DROP TABLE coa_account; --", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, validAccountUUID(tt.in))
		})
	}
}
