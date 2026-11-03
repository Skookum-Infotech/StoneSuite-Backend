package controllers

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// deliveryNumberSource names where one resource's display number lives. Every
// field is a code constant (never request input), so it is safe to splice into SQL.
type deliveryNumberSource struct {
	Table, UUIDColumn, NumberColumn string
}

// deliveryNumberIdentRE is a defence-in-depth check on the spliced identifiers.
var deliveryNumberIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// deliveryNumberSources maps an automation_deliveries.resource to its number
// column: the AR and expiry resources, plus every approval-chain workflow key
// (approval_stuck stores the workflow key as its resource) that has a number.
var deliveryNumberSources = buildDeliveryNumberSources()

func buildDeliveryNumberSources() map[string]deliveryNumberSource {
	out := map[string]deliveryNumberSource{
		"invoice":  {Table: "invoice", UUIDColumn: "invoice_uuid", NumberColumn: "invoice_number"},
		"quote":    {Table: "quote", UUIDColumn: "quote_uuid", NumberColumn: "quote_number"},
		"estimate": {Table: "estimate", UUIDColumn: "estimate_uuid", NumberColumn: "estimate_number"},
	}
	for _, key := range approvalchain.Keys() {
		cfg, ok := approvalchain.ForWorkflowKey(key)
		if !ok || cfg.Record.NumberColumn == "" {
			continue
		}
		if _, exists := out[key]; exists {
			continue
		}
		out[key] = deliveryNumberSource{
			Table: cfg.Record.Table, UUIDColumn: cfg.Record.UUIDColumn, NumberColumn: cfg.Record.NumberColumn,
		}
	}
	return out
}

// fillRecordNumbers sets RecordNumber on every row it can resolve, with one
// indexed query per resource present on the page. Soft-deleted records still
// resolve (the row is kept); unknown resources and non-uuid record ids stay "".
func fillRecordNumbers(ctx context.Context, pool *pgxpool.Pool, rows []AutomationDelivery) error {
	idsByResource := map[string][]string{}
	for _, d := range rows {
		if _, ok := deliveryNumberSources[d.Resource]; !ok {
			continue
		}
		if !deliveryUUIDRE.MatchString(d.RecordID) {
			continue
		}
		idsByResource[d.Resource] = append(idsByResource[d.Resource], d.RecordID)
	}
	resources := make([]string, 0, len(idsByResource))
	for res := range idsByResource {
		resources = append(resources, res)
	}
	sort.Strings(resources)

	numbers := map[string]string{}
	for _, res := range resources {
		src := deliveryNumberSources[res]
		if !deliveryNumberIdentRE.MatchString(src.Table) || !deliveryNumberIdentRE.MatchString(src.UUIDColumn) ||
			!deliveryNumberIdentRE.MatchString(src.NumberColumn) {
			return fmt.Errorf("record number source for %s has an invalid identifier", res)
		}
		q := fmt.Sprintf(`SELECT %[2]s::text, COALESCE(%[3]s, '') FROM %[1]s WHERE %[2]s = ANY($1::uuid[])`,
			src.Table, src.UUIDColumn, src.NumberColumn)
		recs, err := pool.Query(ctx, q, idsByResource[res])
		if err != nil {
			return fmt.Errorf("resolve %s record numbers: %w", res, err)
		}
		for recs.Next() {
			var id, number string
			if err := recs.Scan(&id, &number); err != nil {
				recs.Close()
				return fmt.Errorf("scan %s record number: %w", res, err)
			}
			numbers[res+"|"+id] = number
		}
		if err := recs.Err(); err != nil {
			return fmt.Errorf("iterate %s record numbers: %w", res, err)
		}
	}
	for i := range rows {
		rows[i].RecordNumber = numbers[rows[i].Resource+"|"+rows[i].RecordID]
	}
	return nil
}
