// accountingperiod/store_locks_test.go -- table-driven tests for
// LockField.column()/.lockAction()/.unlockAction(), the pure fixed-mapping
// methods store_locks.go builds every lock-change SQL statement and history
// row from. Deliberately NOT dbtest-tagged: these methods touch no database
// (see store_locks.go's comment on column() -- the SQL identifier is a
// closed, compile-time enum, never derived from request data), so they are
// exactly the kind of pure function CLAUDE.md's "table-driven tests for all
// pure functions" rule targets, and they must be verifiable without
// TEST_DATABASE_URL. Before this file, these three methods were exercised
// only indirectly, inside dbtest-tagged tests that never run without a live
// Postgres.
package accountingperiod

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLockField_ColumnAndHistoryActions(t *testing.T) {
	tests := []struct {
		name           string
		field          LockField
		wantColumn     string
		wantLockAction string
		wantUnlock     string
	}{
		{"AP", LockAP, "ap_lock_status", "ap_lock", "ap_unlock"},
		{"AR", LockAR, "ar_lock_status", "ar_lock", "ar_unlock"},
		{"GL", LockGL, "gl_lock_status", "gl_lock", "gl_unlock"},
		{"unrecognized value fails safe, never a blank SQL identifier by accident",
			LockField(99), "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantColumn, tt.field.column())
			assert.Equal(t, tt.wantLockAction, tt.field.lockAction())
			assert.Equal(t, tt.wantUnlock, tt.field.unlockAction())
		})
	}
}

// TestLockField_ColumnNamesAreDistinct guards against a copy-paste error
// collapsing two of the three lock columns onto the same name, which would
// make applyLock silently overwrite the wrong sub-ledger's lock.
func TestLockField_ColumnNamesAreDistinct(t *testing.T) {
	cols := map[string]bool{}
	for _, f := range []LockField{LockAP, LockAR, LockGL} {
		c := f.column()
		assert.False(t, cols[c], "column %q reused by more than one LockField", c)
		cols[c] = true
	}
	assert.Len(t, cols, 3)
}

// TestLockField_HistoryActionsMatchSchema guards against the six action
// verbs applyLock writes ever drifting from the widened chk_ap_history_action
// CHECK constraint in database/migrations/tenant/schema.sql -- a mismatch
// here would surface only as a runtime 500 (a CHECK violation) on the first
// lock/unlock call against a real database, not at compile time.
func TestLockField_HistoryActionsMatchSchema(t *testing.T) {
	allowed := map[string]bool{
		"generate": true, "close": true, "reopen": true, "base_setup": true,
		"ap_lock": true, "ap_unlock": true,
		"ar_lock": true, "ar_unlock": true,
		"gl_lock": true, "gl_unlock": true,
	}
	for _, f := range []LockField{LockAP, LockAR, LockGL} {
		assert.True(t, allowed[f.lockAction()], "lockAction %q not in chk_ap_history_action", f.lockAction())
		assert.True(t, allowed[f.unlockAction()], "unlockAction %q not in chk_ap_history_action", f.unlockAction())
	}
}
