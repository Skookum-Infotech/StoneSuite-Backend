package vendortransactions

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// uuidRe is the canonical text form of a UUID.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrVendorNotFound is returned when the uuid is not a live vendor (it may be
// deleted or unknown); handlers map it to 404.
var ErrVendorNotFound = errors.New("vendor not found")

// scope is everything a request needs to know about the caller and the vendor.
// The list and the summary both build it with loadScope, so they always read
// the same documents.
type scope struct {
	vendorID int       // vendor.vendor_id, the FK the documents reference
	employee int       // caller's employee id; 0 matches no row (ids start at 1)
	sources  []granted // readable types, in registry order
}

// byKey finds a readable source by key.
func (s scope) byKey(key string) (granted, bool) {
	for _, g := range s.sources {
		if g.Key == key {
			return g, true
		}
	}
	return granted{}, false
}

// limited is true when any of the registered types is unreadable or own-scoped,
// so the figures cover fewer records than the vendor has.
func (s scope) limited() bool {
	if len(s.sources) < len(Sources()) {
		return true
	}
	for _, g := range s.sources {
		if g.own {
			return true
		}
	}
	return false
}

// loadScope resolves the vendor, the caller's grants and (only when some grant
// is own-scoped) the caller's employee id.
func loadScope(ctx context.Context, pool *pgxpool.Pool, identityID, vendorUUID string) (scope, error) {
	var sc scope
	var err error
	if sc.vendorID, err = resolveVendor(ctx, pool, vendorUUID); err != nil {
		return scope{}, err
	}
	grants, err := authz.EffectiveGrants(ctx, pool, identityID)
	if err != nil {
		return scope{}, fmt.Errorf("load grants: %w", err)
	}
	needEmployee := false
	for _, s := range Sources() {
		if g, ok := grantFor(s, grants); ok {
			sc.sources = append(sc.sources, g)
			needEmployee = needEmployee || g.own
		}
	}
	if needEmployee {
		if sc.employee, err = resolveEmployee(ctx, pool, identityID); err != nil {
			return scope{}, err
		}
	}
	return sc, nil
}

// resolveVendor maps a vendor uuid to its internal id; only a live vendor
// qualifies. The uuid is bound as a parameter.
func resolveVendor(ctx context.Context, pool *pgxpool.Pool, vendorUUID string) (int, error) {
	if !uuidRe.MatchString(vendorUUID) {
		return 0, ErrVendorNotFound
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT vendor_id
		FROM vendor
		WHERE vendor_uuid = $1::uuid
		  AND vendor_deleted_at IS NULL`, vendorUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrVendorNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve vendor: %w", err)
	}
	return id, nil
}

// resolveEmployee maps a control-plane identity to its employee id, or 0 when it
// has none (which matches no owned record, so an own-scoped read sees nothing).
func resolveEmployee(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, error) {
	var employee int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id
		FROM users u
		JOIN employee e ON e.employee_user_id = u.id AND e.employee_deleted_at IS NULL
		WHERE u.identity_id = $1::uuid`, identityID).Scan(&employee)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve employee: %w", err)
	}
	return employee, nil
}

// narrowTo keeps only the source with the given key ("" keeps all).
func narrowTo(all []granted, key string) []granted {
	if key == "" {
		return all
	}
	for _, g := range all {
		if g.Key == key {
			return []granted{g}
		}
	}
	return nil
}

// typeInfos lists the readable types for the tab's type filter.
func typeInfos(all []granted) []TypeInfo {
	out := make([]TypeInfo, len(all))
	for i, g := range all {
		out[i] = TypeInfo{Type: g.Key, Label: g.Label, Domain: g.Domain, Module: g.Module}
	}
	return out
}
