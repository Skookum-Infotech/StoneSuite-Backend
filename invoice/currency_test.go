package invoice

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCurrenciesConflict(t *testing.T) {
	one, two := 1, 2
	other := 1
	tests := []struct {
		name string
		a, b *int
		want bool
	}{
		{"both nil", nil, nil, false},
		{"nil vs set", nil, &one, false},
		{"set vs nil", &one, nil, false},
		{"equal values", &one, &other, false},
		{"different", &one, &two, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CurrenciesConflict(tc.a, tc.b))
		})
	}
}
