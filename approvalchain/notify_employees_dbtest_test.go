//go:build dbtest

package approvalchain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

func TestNotifyEmployees_SkipsNoLogin(t *testing.T) {
	pool := notifyTestPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1e12)

	var userID, identityID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name) VALUES (gen_random_uuid(), $1, 'Linked')
		 RETURNING id::text, identity_id::text`, "linked"+suffix+"@example.test").Scan(&userID, &identityID))
	var linkedEmp, orphanEmp int
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email)
		 VALUES ($1,'Linked','T',$2) RETURNING employee_id`, userID, "linked"+suffix+"@example.test").Scan(&linkedEmp))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO employee (employee_first_name, employee_last_name, employee_email)
		 VALUES ('Orphan','T',$1) RETURNING employee_id`, "orphan"+suffix+"@example.test").Scan(&orphanEmp))

	var got services.NotificationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	prev := config.AppConfig
	config.AppConfig = config.Config{NotifyURL: server.URL, NotifyAPIKey: "nk_dev_test_secret"}
	t.Cleanup(func() { config.AppConfig = prev })

	_, err := notifyEmployees(ctx, pool, "tenant-1", EmployeesEvent{
		EventType: "install_appointment.scheduled", Resource: "install_appointment", RecordUUID: "u-1",
		ActorUserID: "actor-id", Title: "T", IdempotencyKey: "k1", EmployeeIDs: []int{linkedEmp, orphanEmp},
	})
	require.NoError(t, err)
	require.Len(t, got.Recipients, 1)
	assert.Equal(t, identityID, got.Recipients[0].UserID)
	assert.Equal(t, "actor-id", got.ActorUserID)
	assert.Equal(t, "k1", got.IdempotencyKey)

	// Only login-less crew: no error, no send.
	got = services.NotificationRequest{}
	_, err = notifyEmployees(ctx, pool, "tenant-1", EmployeesEvent{EventType: "x", EmployeeIDs: []int{orphanEmp}})
	require.NoError(t, err)
	assert.Empty(t, got.Recipients)
}
