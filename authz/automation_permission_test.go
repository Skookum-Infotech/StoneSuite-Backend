package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAutomationPermissionsInCatalog(t *testing.T) {
	for _, a := range []Action{ActionRead, ActionCreate, ActionUpdate, ActionDelete} {
		assert.True(t, IsValidPermission(ResourceAutomation, a), "automation:%s must be grantable", a)
	}
	assert.False(t, IsValidPermission(ResourceAutomation, ActionApprove))
}
