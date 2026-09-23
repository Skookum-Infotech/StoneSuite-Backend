package crmstore

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lookupFK says where the display label for one FK column of the customer
// table lives: the referenced table, its key column, and its *_name column.
type lookupFK struct{ table, pk, label string }

// lookupFKCache holds the customer table's FK→label map. It is derived from
// the catalog, and every tenant database is built from the same template, so
// one successful introspection serves the whole process. A failed attempt is
// not cached, so the next record retries.
var lookupFKCache struct {
	mu     sync.Mutex
	loaded bool
	fks    map[string]lookupFK
}

// customerLookupFKs introspects the customer table's single-column foreign
// keys whose target has a matching <prefix>_name column (e.g.
// customer_type → lkp_customer_type.customer_type_name). Identifiers come
// from the catalog, never from a request.
func customerLookupFKs(ctx context.Context, pool *pgxpool.Pool) (map[string]lookupFK, error) {
	lookupFKCache.mu.Lock()
	defer lookupFKCache.mu.Unlock()
	if lookupFKCache.loaded {
		return lookupFKCache.fks, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT a.attname, rt.relname, ra.attname, la.attname
		FROM pg_constraint c
		JOIN pg_class t      ON t.oid = c.conrelid
		JOIN pg_namespace n  ON n.oid = t.relnamespace AND n.nspname = current_schema()
		JOIN pg_class rt     ON rt.oid = c.confrelid
		JOIN pg_attribute a  ON a.attrelid = c.conrelid  AND a.attnum = c.conkey[1]
		JOIN pg_attribute ra ON ra.attrelid = c.confrelid AND ra.attnum = c.confkey[1]
		JOIN pg_attribute la ON la.attrelid = c.confrelid AND NOT la.attisdropped
		                    AND la.attname = regexp_replace(ra.attname, '_id$', '_name')
		WHERE c.contype = 'f' AND t.relname = 'customer' AND cardinality(c.conkey) = 1`)
	if err != nil {
		return nil, fmt.Errorf("introspect customer lookups: %w", err)
	}
	defer rows.Close()
	fks := map[string]lookupFK{}
	for rows.Next() {
		var col string
		var fk lookupFK
		if err := rows.Scan(&col, &fk.table, &fk.pk, &fk.label); err != nil {
			return nil, fmt.Errorf("scan customer lookup: %w", err)
		}
		fks[col] = fk
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("introspect customer lookups: %w", err)
	}
	lookupFKCache.fks, lookupFKCache.loaded = fks, true
	return fks, nil
}

// withLookupLabels returns a copy of core with every FK id replaced by its
// display label, so the assistant reads "Customer Type: Commercial" instead
// of "Customer Type: 3" — a bare id is noise to the model and matches nothing
// a user would search for. Best-effort: an id whose label can't be resolved
// is left as-is.
func withLookupLabels(ctx context.Context, pool *pgxpool.Pool, core map[string]any) map[string]any {
	out := make(map[string]any, len(core))
	for k, v := range core {
		out[k] = v
	}
	if pool == nil {
		return out
	}
	fks, err := customerLookupFKs(ctx, pool)
	if err != nil {
		return out
	}
	for key, v := range core {
		fk, ok := fks[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil {
			continue
		}
		sql := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = $1`,
			pgx.Identifier{fk.label}.Sanitize(), pgx.Identifier{fk.table}.Sanitize(), pgx.Identifier{fk.pk}.Sanitize())
		var label string
		if err := pool.QueryRow(ctx, sql, id).Scan(&label); err == nil && label != "" {
			out[key] = label
		}
	}
	return out
}
