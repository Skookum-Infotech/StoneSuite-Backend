package itemreceipt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantOK   bool
		wantExpr string
		wantType query.DataType
	}{
		{"status", "status", true, "ir.item_receipt_status::text", query.TypeString},
		{"purchase order", "purchase_order_id", true, "po.purchase_order_uuid::text", query.TypeString},
		{"vendor", "vendor_id", true, "v.vendor_uuid::text", query.TypeString},
		{"warehouse", "warehouse_id", true, "ir.warehouse_id::text", query.TypeString},
		{"receipt date is a date", "receipt_date", true, "ir.item_receipt_date", query.TypeDate},
		{"owner", "owner_id", true, "ir.item_receipt_owner_id::text", query.TypeString},

		{"custom field", "cf:inspection_ref", true, "ir.item_receipt_custom_fields->>'inspection_ref'", query.TypeString},

		// Anything not whitelisted must be rejected — an unresolved key is a
		// 400, never interpolated SQL.
		{"unknown key", "not_a_field", false, "", ""},
		{"raw column name is not a key", "item_receipt_status", false, "", ""},
		{"sql injection attempt", "id; DROP TABLE item_receipt", false, "", ""},
		{"custom key with a quote", "cf:foo'bar", false, "", ""},
		{"custom key starting with a digit", "cf:1foo", false, "", ""},
		{"custom key with uppercase", "cf:Foo", false, "", ""},
		{"empty custom key", "cf:", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, dt, ok := resolver{}.Resolve(tt.key)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantExpr, expr)
				assert.Equal(t, tt.wantType, dt)
			}
		})
	}
}

func TestResolverSortExpr(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		wantOK bool
	}{
		{"record number", "record_number", true},
		{"receipt date", "receipt_date", true},
		{"warehouse", "warehouse_id", true},

		// Nullable columns must stay out of the sort whitelist: a NULL breaks
		// keyset-cursor comparison and silently drops rows from later pages.
		{"posted_at is nullable", "posted_at", false},
		{"voided_at is nullable", "voided_at", false},
		// status has no cursor value on the response struct — see
		// TestStatusIsFilterableButNotSortable.
		{"status is filter-only", "status", false},
		{"packing slip is not sortable", "packing_slip", false},
		{"unknown", "nope", false},
		{"custom fields are not sortable", "cf:anything", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := resolver{}.SortExpr(tt.key)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

// Every sortable field must have a case in store_search.go's sortValue, or the
// next cursor is minted from created_at while the query orders by something
// else — which corrupts page 2 onward and is invisible to any single-page test.
//
// The probe receipt gives every field a distinct value, so a field that fell
// through to the default is detectable by its value coming back as CreatedAt.
func TestSortValueCoversEverySortableField(t *testing.T) {
	probe := ItemReceipt{
		Number:      "IRCT-000007",
		ReceiptDate: "2026-07-23",
		WarehouseID: 42,
		CreatedAt:   time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for field := range sortableFields {
		t.Run(field, func(t *testing.T) {
			got := sortValue(probe, field)
			assert.NotEqual(t, probe.CreatedAt, got,
				"sortValue has no case for %q — it fell through to created_at, "+
					"so the keyset cursor would be minted from the wrong column", field)
		})
	}

	// The engine's own built-ins must resolve too.
	assert.Equal(t, probe.UpdatedAt, sortValue(probe, "updated_at"))
	assert.Equal(t, probe.CreatedAt, sortValue(probe, "created_at"))
}

// A field that cannot produce a cursor value must not be sortable. `status` is
// the live example: the sort expression would be the numeric lkp FK, which the
// response struct does not carry.
func TestStatusIsFilterableButNotSortable(t *testing.T) {
	_, _, filterOK := resolver{}.Resolve("status")
	assert.True(t, filterOK, "status must remain filterable")
	_, _, sortOK := resolver{}.SortExpr("status")
	assert.False(t, sortOK, "status must not be sortable — sortValue cannot mint its cursor")
}

// The search predicate must only ever embed the caller-supplied placeholder,
// never a value — the engine binds $n itself.
func TestSearchPredicateUsesPlaceholderOnly(t *testing.T) {
	got := resolver{}.SearchPredicate("$3")
	assert.Contains(t, got, "$3")
	assert.NotContains(t, got, "'%'||'")
	// The real invariant: every ILIKE is fed by the placeholder and none by a
	// literal. Asserting the counts match keeps this true as columns are added,
	// where a hardcoded total would just need updating each time.
	assert.Equal(t, countSubstr(got, "ILIKE"), countSubstr(got, "$3"),
		"every ILIKE must be bound to the placeholder, never to a literal")
}

func countSubstr(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
