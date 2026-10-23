package provenance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFrom(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want Via
	}{
		{"unset defaults to user", context.Background(), User},
		{"automation", With(context.Background(), Automation), Automation},
		{"ai agent", With(context.Background(), AIAgent), AIAgent},
		{"docextract", With(context.Background(), DocExtract), DocExtract},
		{"empty stamp falls back to user", With(context.Background(), Via("")), User},
		{"inner stamp wins", With(With(context.Background(), Automation), AIAgent), AIAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, From(tt.ctx))
		})
	}
}
