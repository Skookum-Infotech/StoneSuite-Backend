// Command backfill-attached-profile-splash-waterfall converts every drawing
// side still carrying the old plain splashHeightMm/waterfallHeightMm fields
// (removed from drawing.PieceEdge on 2026-11-19, superseded by the Profiles
// tool's AttachedProfile) into an attachedProfile pointing at that tenant's
// seeded "backsplash"/"waterfall" drawing_attached_profile_type row, with the
// old height carried over as the chosen measurement.
//
// Only the LIVE sales_order_drawing.areas row is migrated. Entries inside
// sales_order_drawing_version.snapshot (append-only save history) are left
// untouched on purpose — they are a historical record of what was actually
// saved at the time, not a working document; restoring an old version just
// renders without whatever Backsplash/Waterfall it had; it is not corrupted.
//
// A side that somehow carries both a splash and a waterfall height (the old
// model allowed it; the new one is one profile per side) keeps the
// backsplash and logs the dropped waterfall height for manual follow-up —
// this has never been physically sensible on one side and should not occur
// in real data.
//
// Usage:
//
//	go run ./cmd/backfill-attached-profile-splash-waterfall \
//	  [--tenant slug] [--tenant slug] …   # default: every active tenant
//	  [--dry-run]                         # report what would be written, change nothing
//
// Reads CONTROL_PLANE_DB_URL and (if set) SECRET_ENCRYPTION_KEY from the
// environment / .env file, same as the server. Idempotent: a second run
// finds no more splashHeightMm/waterfallHeightMm keys and writes nothing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/config"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

type tenantFlags []string

func (t *tenantFlags) String() string { return fmt.Sprint([]string(*t)) }
func (t *tenantFlags) Set(v string) error {
	*t = append(*t, v)
	return nil
}

func main() {
	var (
		tenants tenantFlags
		dryRun  bool
	)
	flag.Var(&tenants, "tenant", "tenant slug to backfill; repeatable; default is every active tenant")
	flag.BoolVar(&dryRun, "dry-run", false, "report what would be written, then change nothing")
	flag.Parse()

	config.Load()
	if config.AppConfig.ControlPlaneDBURL == "" {
		log.Fatal("CONTROL_PLANE_DB_URL is required")
	}

	ctx := context.Background()

	cp, err := tenancy.NewControlPlane(ctx, config.AppConfig.ControlPlaneDBURL)
	if err != nil {
		log.Fatalf("connect control plane: %v", err)
	}

	var dsnResolver tenancy.DSNResolver
	if config.AppConfig.SecretEncryptionKey != "" {
		cipher, cErr := secret.New(config.AppConfig.SecretEncryptionKey)
		if cErr != nil {
			log.Fatalf("invalid SECRET_ENCRYPTION_KEY: %v", cErr)
		}
		dsnResolver = func(_ context.Context, t *tenancy.Tenant) (string, error) {
			return cipher.Decrypt(t.DBConnectionRef)
		}
	}
	router := tenancy.NewRouter(dsnResolver)

	targets, err := resolveTenants(ctx, cp, tenants)
	if err != nil {
		log.Fatalf("resolve tenants: %v", err)
	}
	if len(targets) == 0 {
		log.Fatal("no tenants to process")
	}

	var drawingsChanged, sidesConverted, conflicts, tenantsSkipped int
	for _, t := range targets {
		if t.Status != "active" {
			log.Printf("tenant %s: skipped (status=%s, not active)", t.Slug, t.Status)
			tenantsSkipped++
			continue
		}
		pool, err := router.PoolFor(ctx, &t)
		if err != nil {
			log.Printf("tenant %s: ERROR: tenant pool: %v", t.Slug, err)
			tenantsSkipped++
			continue
		}
		res, err := backfillTenant(ctx, pool, dryRun)
		if err != nil {
			log.Printf("tenant %s: ERROR: %v", t.Slug, err)
			tenantsSkipped++
			continue
		}
		if res.drawingsChanged == 0 {
			log.Printf("tenant %s: skipped (nothing to convert)", t.Slug)
			continue
		}
		log.Printf("tenant %s: %d drawing(s), %d side(s) %s%s", t.Slug, res.drawingsChanged, res.sidesConverted, verb(dryRun), conflictSuffix(res.conflicts))
		drawingsChanged += res.drawingsChanged
		sidesConverted += res.sidesConverted
		conflicts += res.conflicts
	}
	log.Printf("done: %d drawing(s), %d side(s) %s, %d conflict(s) needing a manual look, %d tenant(s) skipped, %d tenant(s) total",
		drawingsChanged, sidesConverted, verb(dryRun), conflicts, tenantsSkipped, len(targets))
}

func verb(dryRun bool) string {
	if dryRun {
		return "would be converted"
	}
	return "converted"
}

func conflictSuffix(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d side(s) had both a backsplash and a waterfall height — kept the backsplash, see warnings above)", n)
}

func resolveTenants(ctx context.Context, cp *tenancy.ControlPlane, slugs tenantFlags) ([]tenancy.Tenant, error) {
	if len(slugs) == 0 {
		return cp.ListTenants(ctx)
	}
	out := make([]tenancy.Tenant, 0, len(slugs))
	for _, slug := range slugs {
		t, err := cp.TenantBySlug(ctx, slug)
		if err != nil {
			return nil, fmt.Errorf("tenant %q: %w", slug, err)
		}
		out = append(out, *t)
	}
	return out, nil
}

type tenantResult struct {
	drawingsChanged int
	sidesConverted  int
	conflicts       int
}

// systemProfileTypeID looks up the tenant's seeded profile type by
// system_key. Every tenant gets one from the schema.sql seed, so a miss means
// the tenant's schema predates that seed and this backfill cannot run for it
// yet (it has nothing to point the converted side at).
func systemProfileTypeID(ctx context.Context, pool *pgxpool.Pool, systemKey string) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM drawing_attached_profile_type WHERE system_key = $1`, systemKey).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("find the seeded %q profile type: %w", systemKey, err)
	}
	return id, nil
}

// convertEdge mutates one edge map in place: true if it changed anything.
// `edge` is a parsed JSON object (map[string]any); a conflict is reported
// through conflict, never silently dropped.
func convertEdge(edge map[string]any, backsplashID, waterfallID string, conflict *bool) bool {
	splash, hasSplash := edge["splashHeightMm"].(float64)
	waterfall, hasWaterfall := edge["waterfallHeightMm"].(float64)
	if !hasSplash && !hasWaterfall {
		return false
	}
	delete(edge, "splashHeightMm")
	delete(edge, "waterfallHeightMm")
	if hasSplash && hasWaterfall {
		*conflict = true
	}
	switch {
	case hasSplash:
		edge["attachedProfile"] = map[string]any{"profileTypeId": backsplashID, "measurementMm": splash}
	case hasWaterfall:
		edge["attachedProfile"] = map[string]any{"profileTypeId": waterfallID, "measurementMm": waterfall}
	}
	return true
}

func backfillTenant(ctx context.Context, pool *pgxpool.Pool, dryRun bool) (tenantResult, error) {
	var res tenantResult

	backsplashID, err := systemProfileTypeID(ctx, pool, "backsplash")
	if err != nil {
		return res, err
	}
	waterfallID, err := systemProfileTypeID(ctx, pool, "waterfall")
	if err != nil {
		return res, err
	}

	rows, err := pool.Query(ctx, `SELECT sales_order_id, areas, drawing_record_version FROM sales_order_drawing`)
	if err != nil {
		return res, fmt.Errorf("read sales_order_drawing: %w", err)
	}
	defer rows.Close()

	type pending struct {
		salesOrderID int
		areas        []byte
		recordVer    int
	}
	var toWrite []pending

	for rows.Next() {
		var salesOrderID int
		var areasRaw []byte
		var recordVer int
		if err := rows.Scan(&salesOrderID, &areasRaw, &recordVer); err != nil {
			return res, fmt.Errorf("scan sales_order_drawing: %w", err)
		}

		var areas []map[string]any
		if err := json.Unmarshal(areasRaw, &areas); err != nil {
			log.Printf("sales order %d: ERROR: unmarshal areas: %v", salesOrderID, err)
			continue
		}

		sidesHere, conflictsHere := 0, 0
		for _, area := range areas {
			pieces, _ := area["pieces"].([]any)
			for _, p := range pieces {
				piece, _ := p.(map[string]any)
				if piece == nil {
					continue
				}
				edges, _ := piece["edges"].([]any)
				for _, e := range edges {
					edge, _ := e.(map[string]any)
					if edge == nil {
						continue
					}
					conflict := false
					if convertEdge(edge, backsplashID, waterfallID, &conflict) {
						sidesHere++
						if conflict {
							conflictsHere++
							log.Printf("sales order %d side %v: had both a backsplash and a waterfall height — kept the backsplash", salesOrderID, edge["index"])
						}
					}
				}
			}
		}
		if sidesHere == 0 {
			continue
		}
		newAreas, err := json.Marshal(areas)
		if err != nil {
			log.Printf("sales order %d: ERROR: marshal areas: %v", salesOrderID, err)
			continue
		}
		res.drawingsChanged++
		res.sidesConverted += sidesHere
		res.conflicts += conflictsHere
		toWrite = append(toWrite, pending{salesOrderID: salesOrderID, areas: newAreas, recordVer: recordVer})
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("read sales_order_drawing: %w", err)
	}

	if dryRun {
		return res, nil
	}
	for _, p := range toWrite {
		// Guarded by the version read moments ago, so a concurrent edit
		// during this run is never silently overwritten — it is just left
		// for the next run to pick up instead.
		tag, err := pool.Exec(ctx,
			`UPDATE sales_order_drawing SET areas = $1 WHERE sales_order_id = $2 AND drawing_record_version = $3`,
			p.areas, p.salesOrderID, p.recordVer)
		if err != nil {
			log.Printf("sales order %d: ERROR: write areas: %v", p.salesOrderID, err)
			continue
		}
		if tag.RowsAffected() == 0 {
			log.Printf("sales order %d: skipped (edited concurrently — rerun this tool to pick it up)", p.salesOrderID)
		}
	}
	return res, nil
}
