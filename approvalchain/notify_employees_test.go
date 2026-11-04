package approvalchain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildEmployeesRequest(t *testing.T) {
	ev := EmployeesEvent{
		EventType: "install_appointment.scheduled", Resource: "install_appointment", RecordUUID: "u-1",
		ActorUserID: "actor", Title: "T", Body: "B", StatusLink: "/x", IdempotencyKey: "k",
	}
	req := buildEmployeesRequest("tenant-1", []contact{{UserID: "id1", Email: "a@x.test"}}, ev)
	assert.Equal(t, "tenant-1", req.TenantID)
	assert.Equal(t, "actor", req.ActorUserID)
	assert.Equal(t, "k", req.IdempotencyKey)
	assert.Equal(t, "/x", req.StatusLink)
	assert.Equal(t, "u-1", req.ResourceID)
	assert.Equal(t, []string{"email"}, req.Channels)
	if assert.Len(t, req.Recipients, 1) {
		assert.Equal(t, "id1", req.Recipients[0].UserID)
	}
}
