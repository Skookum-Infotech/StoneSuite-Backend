package accountingperiod

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// threeMonths builds Jan/Feb/Mar 2026 with the given statuses.
func threeMonths(jan, feb, mar string) []PeriodState {
	return []PeriodState{
		{ID: "jan", Name: "Jan 2026", Start: day(2026, 1, 1), End: day(2026, 1, 31), Status: jan},
		{ID: "feb", Name: "Feb 2026", Start: day(2026, 2, 1), End: day(2026, 2, 28), Status: feb},
		{ID: "mar", Name: "Mar 2026", Start: day(2026, 3, 1), End: day(2026, 3, 31), Status: mar},
	}
}

func ids(states []PeriodState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.ID)
	}
	return out
}

// threeMonthsLock mirrors threeMonths but sets the given lock's status field
// instead of Status, leaving Status at its zero value (irrelevant here since
// PlanCloseLock/PlanReopenLock never read it).
func threeMonthsLock(lock LockField, jan, feb, mar string) []PeriodState {
	states := []PeriodState{
		{ID: "jan", Name: "Jan 2026", Start: day(2026, 1, 1), End: day(2026, 1, 31)},
		{ID: "feb", Name: "Feb 2026", Start: day(2026, 2, 1), End: day(2026, 2, 28)},
		{ID: "mar", Name: "Mar 2026", Start: day(2026, 3, 1), End: day(2026, 3, 31)},
	}
	values := []string{jan, feb, mar}
	for i := range states {
		switch lock {
		case LockAP:
			states[i].APStatus = values[i]
		case LockAR:
			states[i].ARStatus = values[i]
		case LockGL:
			states[i].GLStatus = values[i]
		}
	}
	return states
}

func TestPlanClose(t *testing.T) {
	tests := []struct {
		name        string
		ids         []string
		all         []PeriodState
		wantOrder   []string
		wantErrIs   error
		wantClient  bool
		wantNoError bool
	}{
		{
			name:        "close the earliest open period",
			ids:         []string{"jan"},
			all:         threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan"},
			wantNoError: true,
		},
		{
			name:        "close the next period once the earlier one is closed",
			ids:         []string{"feb"},
			all:         threeMonths(StatusClosed, StatusOpen, StatusOpen),
			wantOrder:   []string{"feb"},
			wantNoError: true,
		},
		{
			name:      "closing out of order is refused",
			ids:       []string{"mar"},
			all:       threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantErrIs: ErrPriorPeriodOpen,
		},
		{
			name:      "closing an already-closed period is refused",
			ids:       []string{"jan"},
			all:       threeMonths(StatusClosed, StatusOpen, StatusOpen),
			wantErrIs: ErrAlreadyClosed,
		},
		{
			name:        "a bulk close threads each step onto the next",
			ids:         []string{"jan", "feb", "mar"},
			all:         threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan", "feb", "mar"},
			wantNoError: true,
		},
		{
			name:        "a bulk close is reordered chronologically",
			ids:         []string{"mar", "jan", "feb"},
			all:         threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan", "feb", "mar"},
			wantNoError: true,
		},
		{
			name:      "a bulk close that skips a period is still refused",
			ids:       []string{"jan", "mar"},
			all:       threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantErrIs: ErrPriorPeriodOpen,
		},
		{
			name:        "duplicate ids collapse to one step",
			ids:         []string{"jan", "jan"},
			all:         threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan"},
			wantNoError: true,
		},
		{
			name:      "an unknown id is not found",
			ids:       []string{"apr"},
			all:       threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantErrIs: ErrNotFound,
		},
		{
			name:       "an empty list is a client error",
			ids:        nil,
			all:        threeMonths(StatusOpen, StatusOpen, StatusOpen),
			wantClient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanClose(tt.ids, tt.all)
			switch {
			case tt.wantClient:
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want ClientError, got %T: %v", err, err)
			case tt.wantErrIs != nil:
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs), "want %v, got %v", tt.wantErrIs, err)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.wantOrder, ids(got))
			}
		})
	}
}

func TestPlanReopen(t *testing.T) {
	withBase := func(states []PeriodState, beforeBase ...string) []PeriodState {
		flagged := map[string]bool{}
		for _, id := range beforeBase {
			flagged[id] = true
		}
		for i := range states {
			states[i].IsBeforeBase = flagged[states[i].ID]
		}
		return states
	}

	tests := []struct {
		name        string
		ids         []string
		all         []PeriodState
		wantOrder   []string
		wantErrIs   error
		wantNoError bool
	}{
		{
			name:        "reopen the latest closed period",
			ids:         []string{"mar"},
			all:         threeMonths(StatusClosed, StatusClosed, StatusClosed),
			wantOrder:   []string{"mar"},
			wantNoError: true,
		},
		{
			name:      "reopening out of order is refused",
			ids:       []string{"jan"},
			all:       threeMonths(StatusClosed, StatusClosed, StatusClosed),
			wantErrIs: ErrLaterPeriodClosed,
		},
		{
			name:      "reopening an already-open period is refused",
			ids:       []string{"mar"},
			all:       threeMonths(StatusClosed, StatusClosed, StatusOpen),
			wantErrIs: ErrAlreadyOpen,
		},
		{
			name:        "a bulk reopen runs newest-first",
			ids:         []string{"jan", "feb", "mar"},
			all:         threeMonths(StatusClosed, StatusClosed, StatusClosed),
			wantOrder:   []string{"mar", "feb", "jan"},
			wantNoError: true,
		},
		{
			name:      "a bulk reopen that skips a period is refused",
			ids:       []string{"mar", "jan"},
			all:       threeMonths(StatusClosed, StatusClosed, StatusClosed),
			wantErrIs: ErrLaterPeriodClosed,
		},
		{
			name:      "a pre-go-live period can never be reopened",
			ids:       []string{"jan"},
			all:       withBase(threeMonths(StatusClosed, StatusOpen, StatusOpen), "jan"),
			wantErrIs: ErrBeforeBasePeriod,
		},
		{
			name:      "an unknown id is not found",
			ids:       []string{"apr"},
			all:       threeMonths(StatusClosed, StatusClosed, StatusClosed),
			wantErrIs: ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanReopen(tt.ids, tt.all)
			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs), "want %v, got %v", tt.wantErrIs, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrder, ids(got))
		})
	}
}

func TestPlanCloseLock(t *testing.T) {
	tests := []struct {
		name        string
		ids         []string
		all         []PeriodState
		wantOrder   []string
		wantErrIs   error
		wantNoError bool
	}{
		{
			name:        "close the earliest open period",
			ids:         []string{"jan"},
			all:         threeMonthsLock(LockAP, StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan"},
			wantNoError: true,
		},
		{
			name:        "close the next period once the earlier one is closed",
			ids:         []string{"feb"},
			all:         threeMonthsLock(LockAP, StatusClosed, StatusOpen, StatusOpen),
			wantOrder:   []string{"feb"},
			wantNoError: true,
		},
		{
			name:      "closing out of order is refused",
			ids:       []string{"mar"},
			all:       threeMonthsLock(LockAP, StatusOpen, StatusOpen, StatusOpen),
			wantErrIs: ErrPriorPeriodOpen,
		},
		{
			name:      "closing an already-closed period is refused",
			ids:       []string{"jan"},
			all:       threeMonthsLock(LockAP, StatusClosed, StatusOpen, StatusOpen),
			wantErrIs: ErrAlreadyClosed,
		},
		{
			name:        "a bulk close threads each step onto the next",
			ids:         []string{"jan", "feb", "mar"},
			all:         threeMonthsLock(LockAP, StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan", "feb", "mar"},
			wantNoError: true,
		},
		{
			name:        "a bulk close is reordered chronologically",
			ids:         []string{"mar", "jan", "feb"},
			all:         threeMonthsLock(LockAP, StatusOpen, StatusOpen, StatusOpen),
			wantOrder:   []string{"jan", "feb", "mar"},
			wantNoError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanCloseLock(tt.ids, tt.all, LockAP)
			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs), "want %v, got %v", tt.wantErrIs, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrder, ids(got))
		})
	}

	_, err := PlanCloseLock([]string{"mar"}, threeMonthsLock(LockAP, StatusOpen, StatusOpen, StatusOpen), LockAP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AP for", "message should name the lock")
}

func TestPlanReopenLock(t *testing.T) {
	tests := []struct {
		name        string
		ids         []string
		all         []PeriodState
		wantOrder   []string
		wantErrIs   error
		wantNoError bool
	}{
		{
			name:        "reopen the latest closed period",
			ids:         []string{"mar"},
			all:         threeMonthsLock(LockAP, StatusClosed, StatusClosed, StatusClosed),
			wantOrder:   []string{"mar"},
			wantNoError: true,
		},
		{
			name:      "reopening out of order is refused",
			ids:       []string{"jan"},
			all:       threeMonthsLock(LockAP, StatusClosed, StatusClosed, StatusClosed),
			wantErrIs: ErrLaterPeriodClosed,
		},
		{
			name:      "reopening an already-open period is refused",
			ids:       []string{"mar"},
			all:       threeMonthsLock(LockAP, StatusClosed, StatusClosed, StatusOpen),
			wantErrIs: ErrAlreadyOpen,
		},
		{
			name:        "a bulk reopen runs newest-first",
			ids:         []string{"jan", "feb", "mar"},
			all:         threeMonthsLock(LockAP, StatusClosed, StatusClosed, StatusClosed),
			wantOrder:   []string{"mar", "feb", "jan"},
			wantNoError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanReopenLock(tt.ids, tt.all, LockAP)
			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs), "want %v, got %v", tt.wantErrIs, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrder, ids(got))
		})
	}

	_, err := PlanReopenLock([]string{"jan"}, threeMonthsLock(LockAP, StatusClosed, StatusClosed, StatusClosed), LockAP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AP for", "message should name the lock")
}

// TestLockField_GetAndLabel is a direct table-driven test of LockField's two
// helper methods over all three constants -- LockAR in particular is never
// otherwise exercised for .get()/.label() by name (TestPlanCloseLock/
// TestPlanReopenLock/TestPlanLocksAreIndependent only drive LockAP and
// LockGL through the full plan() pipeline), so this is the only place the AR
// branch of either switch is checked at all. Also covers the zero-value/
// out-of-range case: LockField is a closed, compile-time enum (rules.go), so
// an unrecognized value can only arise from a programming error, and both
// methods must fail safe (empty string, not a panic) rather than propagate a
// blank SQL identifier or a blank error-message prefix silently.
func TestLockField_GetAndLabel(t *testing.T) {
	p := PeriodState{APStatus: "ap-val", ARStatus: "ar-val", GLStatus: "gl-val"}
	tests := []struct {
		name      string
		field     LockField
		wantGet   string
		wantLabel string
	}{
		{"AP", LockAP, "ap-val", "AP"},
		{"AR", LockAR, "ar-val", "AR"},
		{"GL", LockGL, "gl-val", "GL"},
		{"unrecognized value fails safe", LockField(99), "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantGet, tt.field.get(p))
			assert.Equal(t, tt.wantLabel, tt.field.label())
		})
	}
}

// TestPlanCloseLockAndReopenLock_AR drives the full PlanCloseLock/
// PlanReopenLock pipeline over LockAR specifically. TestPlanCloseLock and
// TestPlanReopenLock only ever pass LockAP, and TestPlanLocksAreIndependent
// only adds LockGL -- AR's own chronological enforcement and its "AR for"
// message prefix are otherwise never proven end to end.
func TestPlanCloseLockAndReopenLock_AR(t *testing.T) {
	_, err := PlanCloseLock([]string{"mar"}, threeMonthsLock(LockAR, StatusOpen, StatusOpen, StatusOpen), LockAR)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPriorPeriodOpen), "got %v", err)
	assert.Contains(t, err.Error(), "AR for")

	got, err := PlanCloseLock([]string{"jan"}, threeMonthsLock(LockAR, StatusOpen, StatusOpen, StatusOpen), LockAR)
	require.NoError(t, err)
	assert.Equal(t, []string{"jan"}, ids(got))

	_, err = PlanReopenLock([]string{"jan"}, threeMonthsLock(LockAR, StatusClosed, StatusClosed, StatusClosed), LockAR)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLaterPeriodClosed), "got %v", err)
	assert.Contains(t, err.Error(), "AR for")

	got, err = PlanReopenLock([]string{"mar"}, threeMonthsLock(LockAR, StatusClosed, StatusClosed, StatusClosed), LockAR)
	require.NoError(t, err)
	assert.Equal(t, []string{"mar"}, ids(got))
}

// TestPlanLocksAreIndependent proves the property the whole design depends
// on: two calls with different LockField values over the same underlying
// periods see independent state, because plan() threads working state
// through the field selected by get, not through a shared Status.
func TestPlanLocksAreIndependent(t *testing.T) {
	all := []PeriodState{
		{ID: "jan", Name: "Jan 2026", Start: day(2026, 1, 1), End: day(2026, 1, 31),
			APStatus: StatusClosed, GLStatus: StatusOpen},
		{ID: "feb", Name: "Feb 2026", Start: day(2026, 2, 1), End: day(2026, 2, 28),
			APStatus: StatusOpen, GLStatus: StatusOpen},
	}

	// AP's own chronological prefix (Jan closed) is satisfied, so closing Feb
	// on AP succeeds even though GL's prefix (Jan still open on GL) is not.
	_, err := PlanCloseLock([]string{"feb"}, all, LockAP)
	require.NoError(t, err)

	_, err = PlanCloseLock([]string{"feb"}, all, LockGL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPriorPeriodOpen), "want ErrPriorPeriodOpen, got %v", err)
	assert.Contains(t, err.Error(), "GL for")
}

// TestPlanConflictsAreConflicts guards the controller contract: every
// sequencing failure must map to 409, never 500, so each one has to be a
// ConflictError as well as matching its sentinel.
func TestPlanConflictsAreConflicts(t *testing.T) {
	_, err := PlanClose([]string{"mar"}, threeMonths(StatusOpen, StatusOpen, StatusOpen))
	require.Error(t, err)
	assert.True(t, IsConflict(err), "want a ConflictError, got %T", err)
	assert.Contains(t, err.Error(), "Jan 2026", "message should name the blocking period")
	assert.NotContains(t, err.Error(), "AP for", "PlanClose's empty label must not prefix the message")
	assert.NotContains(t, err.Error(), "AR for", "PlanClose's empty label must not prefix the message")
	assert.NotContains(t, err.Error(), "GL for", "PlanClose's empty label must not prefix the message")

	_, err = PlanReopen([]string{"jan"}, threeMonths(StatusClosed, StatusClosed, StatusClosed))
	require.Error(t, err)
	assert.True(t, IsConflict(err), "want a ConflictError, got %T", err)
	assert.Contains(t, err.Error(), "Feb 2026")
	assert.NotContains(t, err.Error(), "AP for", "PlanReopen's empty label must not prefix the message")
}

func TestDeriveYearStatus(t *testing.T) {
	tests := []struct {
		name    string
		periods []PeriodState
		want    string
	}{
		{"no periods is open", nil, StatusOpen},
		{"all closed", threeMonths(StatusClosed, StatusClosed, StatusClosed), StatusClosed},
		{"one open", threeMonths(StatusClosed, StatusOpen, StatusClosed), StatusOpen},
		{"none closed", threeMonths(StatusOpen, StatusOpen, StatusOpen), StatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeriveYearStatus(tt.periods))
		})
	}
}

func TestClosedThrough(t *testing.T) {
	tests := []struct {
		name    string
		periods []PeriodState
		want    *time.Time
	}{
		{"nothing closed", threeMonths(StatusOpen, StatusOpen, StatusOpen), nil},
		{"first closed", threeMonths(StatusClosed, StatusOpen, StatusOpen), ptrDay(2026, 1, 31)},
		{"two closed", threeMonths(StatusClosed, StatusClosed, StatusOpen), ptrDay(2026, 2, 28)},
		{"all closed", threeMonths(StatusClosed, StatusClosed, StatusClosed), ptrDay(2026, 3, 31)},
		{
			// The sequencing rules make this unreachable; ClosedThrough is the
			// function that would silently lie if they ever failed, so it stops
			// at the hole rather than reporting max(end).
			name:    "a hole stops the prefix",
			periods: threeMonths(StatusClosed, StatusOpen, StatusClosed),
			want:    ptrDay(2026, 1, 31),
		},
		{"no periods", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClosedThrough(tt.periods)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.True(t, got.Equal(*tt.want), "got %v, want %v", got, *tt.want)
		})
	}
}

// TestClosedThroughIgnoresInputOrder proves the prefix is computed over
// chronological order, not over whatever order the caller supplied.
func TestClosedThroughIgnoresInputOrder(t *testing.T) {
	ordered := threeMonths(StatusClosed, StatusClosed, StatusOpen)
	shuffled := []PeriodState{ordered[2], ordered[0], ordered[1]}

	got := ClosedThrough(shuffled)
	require.NotNil(t, got)
	assert.True(t, got.Equal(day(2026, 2, 28)), "got %v", got)
}

func ptrDay(y int, m time.Month, d int) *time.Time {
	t := day(y, m, d)
	return &t
}
