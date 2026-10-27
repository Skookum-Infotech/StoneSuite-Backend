package anomaly

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is satisfied by a pgx.Tx and a pgxpool.Pool.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Rule is one anomaly check. Detect is read-only and returns only findings that
// are not yet stored: each rule's SQL excludes subjects already in the anomaly
// table, so MaxFindingsPerRule makes progress across scans instead of
// re-reading the same newest rows forever. today is the tenant-local date; a
// rule must use it, never the database clock, for anything date-based.
type Rule interface {
	Key() string
	Detect(ctx context.Context, q Querier, today time.Time) ([]Finding, error)
}

// DefaultRules is every rule the scheduled scan runs.
func DefaultRules() []Rule {
	rules := []Rule{SelfApproval{}, UnbalancedJournal{}, DuplicateVendorBill{}}
	for _, group := range [][]Rule{salesRules(), purchasingRules(), inventoryRules(), fabricationRules(), financeRules()} {
		rules = append(rules, group...)
	}
	return rules
}
