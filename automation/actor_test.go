package automation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSystemActorMarker(t *testing.T) {
	ctx := context.Background()
	assert.False(t, IsSystemActor(ctx))
	assert.True(t, IsSystemActor(WithSystemActor(ctx)))
}
