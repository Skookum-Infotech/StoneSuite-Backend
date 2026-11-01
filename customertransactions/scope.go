package customertransactions

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// customerTypeCode is the lkp_record_type code of a CRM record that is a customer
// (as opposed to a lead or prospect).
const customerTypeCode = "CUST"

// uuidRe is the canonical text form of a UUID.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ErrCustomerNotFound is returned when the uuid is not a live customer (it may be
// a lead, a prospect, deleted, or unknown); handlers map it to 404.
var ErrCustomerNotFound = errors.New("customer not found")

// scope is everything a request needs to know about the caller and the customer.
type scope struct {
	customerID int       // customer.customer_id, the FK the documents reference
	employee   int       // caller's employee id; 0 matches no row (ids start at 1)
	sources    []granted // readable types, in registry order
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
// so the figures cover fewer records than the customer has.
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

// loadScope resolves the customer, the caller's grants and (only when some grant
// is own-scoped) the caller's employee id.
func loadScope(ctx context.Context, pool *pgxpool.Pool, identityID, customerUUID string) (scope, error) {
	var sc scope
	var err error
	if sc.customerID, err = resolveCustomer(ctx, pool, customerUUID); err != nil {
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

// resolveCustomer maps a customer uuid to its internal id; only a live record of
// the customer stage qualifies.
func resolveCustomer(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (int, error) {
	if !uuidRe.MatchString(customerUUID) {
		return 0, ErrCustomerNotFound
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT c.customer_id
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE c.customer_uuid = $1::uuid
		  AND rt.record_type_code = $2
		  AND c.customer_deleted_at IS NULL`, customerUUID, customerTypeCode).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrCustomerNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve customer: %w", err)
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
