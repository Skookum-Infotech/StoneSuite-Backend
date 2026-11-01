package vendortransactions

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBucketIndex(t *testing.T) {
	tests := []struct {
		name string
		days int
		want string
	}{
		{"due in future", -5, AgingCurrent},
		{"due today", 0, AgingCurrent},
		{"due yesterday", 1, Aging1To30},
		{"30 days late", 30, Aging1To30},
		{"31 days late", 31, Aging31To60},
		{"60 days late", 60, Aging31To60},
		{"61 days late", 61, Aging61Plus},
		{"very late", 400, Aging61Plus},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, agingLabels[bucketIndex(tc.days)])
		})
	}
}
