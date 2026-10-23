package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"stonesuite-backend/provenance"
)

func TestSystemActorMarker(t *testing.T) {
	ctx := context.Background()
	assert.False(t, IsSystemActor(ctx))
	assert.True(t, IsSystemActor(WithSystemActor(ctx)))
	assert.Equal(t, provenance.User, provenance.From(ctx))
	assert.Equal(t, provenance.Automation, provenance.From(WithSystemActor(ctx)))
}
