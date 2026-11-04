package installschedule

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/approvalchain"
)

func TestIdempotencyKey(t *testing.T) {
	base := IdempotencyKey("u-1", 2, EventRescheduled, []int{3, 1, 2})
	tests := []struct {
		name string
		got  string
		same bool
	}{
		{"same input", IdempotencyKey("u-1", 2, EventRescheduled, []int{3, 1, 2}), true},
		{"order insensitive", IdempotencyKey("u-1", 2, EventRescheduled, []int{1, 2, 3}), true},
		{"duplicates ignored", IdempotencyKey("u-1", 2, EventRescheduled, []int{1, 2, 2, 3}), true},
		{"version changes", IdempotencyKey("u-1", 3, EventRescheduled, []int{1, 2, 3}), false},
		{"event changes", IdempotencyKey("u-1", 2, EventCancelled, []int{1, 2, 3}), false},
		{"recipients change", IdempotencyKey("u-1", 2, EventRescheduled, []int{1, 2}), false},
		{"uuid changes", IdempotencyKey("u-2", 2, EventRescheduled, []int{1, 2, 3}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.same, tt.got == base)
		})
	}
	assert.Len(t, base, 64)
}

func apptWithCrew(ids ...int) *Appointment {
	a := &Appointment{ID: "a-1", Version: 2, VisitType: VisitInstall, JobNumber: "FJ1"}
	for _, id := range ids {
		a.Crew = append(a.Crew, CrewMember{EmployeeID: id})
	}
	return a
}

func TestRecipients(t *testing.T) {
	tests := []struct {
		name  string
		res   *ChangeResult
		actor int
		want  map[string][]int
	}{
		{"create excludes actor", &ChangeResult{After: apptWithCrew(1, 2, 3), Actions: []string{ActionCreate}}, 2, map[string][]int{groupAll: {1, 3}}},
		{"cancel notifies all crew", &ChangeResult{After: apptWithCrew(1, 2), Actions: []string{ActionCancel}}, 9, map[string][]int{groupAll: {1, 2}}},
		{"reschedule is old union new", &ChangeResult{Before: apptWithCrew(1, 2), After: apptWithCrew(2, 3), Actions: []string{ActionReschedule}}, 9, map[string][]int{groupAll: {1, 2, 3}}},
		{"reassign splits added and removed", &ChangeResult{Before: apptWithCrew(1, 2), After: apptWithCrew(2, 3), Actions: []string{ActionReassign}, AddedCrew: []int{3}, RemovedCrew: []int{1}}, 9, map[string][]int{groupAdded: {3}, groupRemoved: {1}}},
		{"reassign excludes actor", &ChangeResult{After: apptWithCrew(2, 3), Actions: []string{ActionReassign}, AddedCrew: []int{3}, RemovedCrew: []int{1}}, 3, map[string][]int{groupRemoved: {1}}},
		{"reschedule plus reassign keeps groups disjoint", &ChangeResult{Before: apptWithCrew(1, 2), After: apptWithCrew(2, 3), Actions: []string{ActionReschedule, ActionReassign}, AddedCrew: []int{3}, RemovedCrew: []int{1}}, 9, map[string][]int{groupAll: {2}, groupAdded: {3}, groupRemoved: {1}}},
		{"complete is empty", &ChangeResult{After: apptWithCrew(1, 2), Actions: []string{ActionComplete}}, 9, map[string][]int{}},
		{"notes-only update is empty", &ChangeResult{After: apptWithCrew(1, 2), Actions: []string{ActionUpdate}}, 9, map[string][]int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, recipients(tt.res, tt.actor))
		})
	}
}

func TestNotifyChange_SendsPerGroupWithKeys(t *testing.T) {
	var sent []approvalchain.EmployeesEvent
	send := func(_ context.Context, _ *pgxpool.Pool, ev approvalchain.EmployeesEvent) ([]string, error) {
		sent = append(sent, ev)
		return []string{"n-" + ev.IdempotencyKey[:4]}, nil
	}
	res := &ChangeResult{Before: apptWithCrew(1, 2), After: apptWithCrew(2, 3), Actions: []string{ActionReassign}, AddedCrew: []int{3}, RemovedCrew: []int{1}}
	ok, ids := notifyChange(context.Background(), nil, res, "actor-uuid", 9, send)
	require.True(t, ok)
	require.Len(t, sent, 2)
	assert.Len(t, ids, 2)
	assert.NotEqual(t, sent[0].IdempotencyKey, sent[1].IdempotencyKey)
	for _, ev := range sent {
		assert.Equal(t, "actor-uuid", ev.ActorUserID)
		assert.Equal(t, "/calendar/install-schedule?appointment=a-1", ev.StatusLink)
	}

	ok, ids = notifyChange(context.Background(), nil, &ChangeResult{After: apptWithCrew(1), Actions: []string{ActionComplete}}, "x", 9, send)
	assert.False(t, ok)
	assert.Empty(t, ids)
}
