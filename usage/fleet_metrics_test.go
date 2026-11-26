package usage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRecordsCreatedKey(t *testing.T) {
	tests := []struct {
		name, resource, want string
	}{
		{"simple", "invoice", "records_created.invoice"},
		{"underscore", "sales_order", "records_created.sales_order"},
		{"empty", "", "records_created."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RecordsCreatedKey(tt.resource))
		})
	}
}
