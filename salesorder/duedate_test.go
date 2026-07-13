package salesorder

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeRow scans a single preset net-days int (or returns a preset error),
// enough to stand in for the lkp_payment_terms lookup in resolvePaymentDueDate.
type fakeRow struct {
	netDays int
	err     error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*int); ok {
			*p = r.netDays
		}
	}
	return nil
}

// fakeQuerier is a workflow.Querier whose QueryRow always returns a fakeRow.
type fakeQuerier struct {
	netDays int
	err     error
}

func (q fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (q fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{netDays: q.netDays, err: q.err}
}
func (q fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func intPtr(i int) *int { return &i }

// TestResolvePaymentDueDate covers AD-8 due-date derivation: explicit wins,
// otherwise order_date + terms.net_days, otherwise NULL; plus validation.
func TestResolvePaymentDueDate(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit value on/after order date is kept as-is", func(t *testing.T) {
		got, err := resolvePaymentDueDate(ctx, fakeQuerier{}, "2026-07-01", "2026-08-01", intPtr(3))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "2026-08-01" {
			t.Errorf("got %v, want 2026-08-01", got)
		}
	})

	t.Run("explicit value before order date is rejected", func(t *testing.T) {
		if _, err := resolvePaymentDueDate(ctx, fakeQuerier{}, "2026-07-10", "2026-07-01", nil); !IsClientError(err) {
			t.Errorf("expected ClientError, got %v", err)
		}
	})

	t.Run("bad explicit format is rejected", func(t *testing.T) {
		if _, err := resolvePaymentDueDate(ctx, fakeQuerier{}, "2026-07-01", "07/01/2026", nil); !IsClientError(err) {
			t.Errorf("expected ClientError, got %v", err)
		}
	})

	t.Run("bad order-date format is rejected", func(t *testing.T) {
		if _, err := resolvePaymentDueDate(ctx, fakeQuerier{}, "not-a-date", "", intPtr(1)); !IsClientError(err) {
			t.Errorf("expected ClientError, got %v", err)
		}
	})

	t.Run("no terms and no explicit yields NULL", func(t *testing.T) {
		got, err := resolvePaymentDueDate(ctx, fakeQuerier{}, "2026-07-01", "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("derives order_date + net_days", func(t *testing.T) {
		got, err := resolvePaymentDueDate(ctx, fakeQuerier{netDays: 30}, "2026-07-01", "", intPtr(3))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "2026-07-31" {
			t.Errorf("got %v, want 2026-07-31", got)
		}
	})

	t.Run("blank order date derives from today", func(t *testing.T) {
		got, err := resolvePaymentDueDate(ctx, fakeQuerier{netDays: 15}, "", "", intPtr(3))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Now().AddDate(0, 0, 15).Format("2006-01-02")
		if got != want {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
