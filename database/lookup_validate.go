package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// crmLookupTables are the 12 reference tables that must be seeded for the CRM
// module to render its selects (ADR-002). The relational (v2) design FKs into
// them; they are created and seeded for every tenant regardless of design.
var crmLookupTables = []string{
	"lkp_country", "lkp_currency", "lkp_state",
	"lkp_record_type", "lkp_record_status", "lkp_crm_status",
	"lkp_customer_type", "lkp_customer_ar_status", "lkp_crm_lead_source",
	"lkp_contact_method", "lkp_payment_terms", "lkp_price_level",
}

// ValidateLookupSeeds reports any CRM lookup table that exists but holds no
// rows, so a tenant missing seed data is surfaced at startup rather than as a
// broken dropdown later. Table names are fixed constants (no injection). A
// table that does not exist yet (older schema version) is skipped, not failed.
func ValidateLookupSeeds(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var empty []string
	for _, t := range crmLookupTables {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT to_regclass($1) IS NOT NULL`, t).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check table %s: %w", t, err)
		}
		if !exists {
			continue
		}
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+t).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n == 0 {
			empty = append(empty, t)
		}
	}
	return empty, nil
}
