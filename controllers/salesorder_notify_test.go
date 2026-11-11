package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSoVisibleToCustomer(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"DRFT", false},
		{"PAPV", false},
		{"APPV", true},
		{"OPEN", true},
		{"PART", true},
		{"FILL", true},
		{"CANC", true},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			assert.Equal(t, tc.want, soVisibleToCustomer(tc.status))
		})
	}
}
