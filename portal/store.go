package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of pgx used here, satisfied by both *pgxpool.Pool and
// pgx.Tx. Defined at the point of use, per the project's interface convention.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Sentinel errors for portal-user lookups.
var (
	ErrPortalUserNotFound  = errors.New("portal user not found")
	ErrCustomerNotEligible = errors.New("customer is not an approved customer")
)

// Status values a portal login may hold. Only StatusActive may sign in;
// StatusSuspended is a reversible pause (resumed without re-checking
// CustomerEligible) and StatusRevoked is the permanent withdrawal (re-granting
// goes back through CustomerEligible, same as a first-time grant). There is no
// intermediate "invited" state here, because the invitation is tracked
// separately in the control plane (portal_invites), which is where the token
// lives.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusRevoked   = "revoked"
)

// User is a customer-facing login within one tenant.
type User struct {
	ID           string
	IdentityID   string
	CustomerID   int
	CustomerUUID string
	CustomerName string
	Email        string
	FullName     string
	Phone        string
	Status       string
	CreatedAt    time.Time
	SuspendedAt  *time.Time
	RevokedAt    *time.Time
}

// Session is the resolved identity of a portal request: which customer the
// caller is, within the tenant already selected by the JWT.
//
// CustomerID is the internal SERIAL, which is what every document table's
// <doc>_customer_id column holds. It is resolved here from the session and
// passed to the store functions as a parameter — it is never accepted from a
// request, which is what makes the portal predicate a security boundary rather
// than a filter a caller could change.
type Session struct {
	PortalUserID string
	CustomerID   int
	CustomerUUID string
	CustomerName string
	Email        string
	FullName     string
	Phone        string
}

// ResolveSession loads the portal session for an identity in this tenant.
//
// Fails closed on every miss. The joins assert, in one query, that:
//   - the portal user row exists and is active,
//   - its customer is not soft-deleted,
//   - that customer is still a CUST-stage record.
//
// Note what is NOT asserted: customer_is_approved. That flag is reset on every
// CRM state entry, so a routine renewal transition would otherwise lock the
// customer out mid-session. Approval is a precondition of GRANTING access, not
// of exercising it; live access is governed by customer_portal_user.status.
func ResolveSession(ctx context.Context, q Querier, identityID string) (*Session, error) {
	var s Session
	err := q.QueryRow(ctx, `
		SELECT cpu.id, cpu.customer_id, c.customer_uuid, c.customer_name,
		       cpu.email, cpu.full_name, cpu.phone
		FROM customer_portal_user cpu
		JOIN customer c   ON c.customer_id  = cpu.customer_id
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE cpu.identity_id = $1
		  AND cpu.status = 'active'
		  AND c.customer_deleted_at IS NULL
		  AND rt.record_type_code = 'CUST'`, identityID,
	).Scan(&s.PortalUserID, &s.CustomerID, &s.CustomerUUID, &s.CustomerName,
		&s.Email, &s.FullName, &s.Phone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPortalUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve portal session: %w", err)
	}
	return &s, nil
}

// CustomerEligible reports whether a customer may be GRANTED portal access:
// it must be a live, CUST-stage, approved record.
//
// This is the one place customer_is_approved is consulted. Returns the internal
// customer id alongside, so the caller does not re-query.
func CustomerEligible(ctx context.Context, q Querier, customerUUID string) (int, string, error) {
	var (
		id       int
		name     string
		approved bool
		typeCode string
	)
	err := q.QueryRow(ctx, `
		SELECT c.customer_id, c.customer_name, c.customer_is_approved, rt.record_type_code
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		WHERE c.customer_uuid = $1 AND c.customer_deleted_at IS NULL`, customerUUID,
	).Scan(&id, &name, &approved, &typeCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrPortalUserNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("customer eligible: %w", err)
	}
	if typeCode != "CUST" || !approved {
		return 0, "", ErrCustomerNotEligible
	}
	return id, name, nil
}

// ContactInfoForInvite resolves the email and display name to use when
// auto-inviting a customer to the portal the moment their record is approved
// (see controllers/crm.go's ApproveRecord). Prefers the named authorized
// contact; falls back to the customer/company name when no person is on file.
//
// Returns email == "" (not an error) when the customer has no contact email
// on record — callers should treat that as "nothing to invite", not a failure.
func ContactInfoForInvite(ctx context.Context, q Querier, customerUUID string) (email, fullName string, err error) {
	var companyName, fname, lname string
	err = q.QueryRow(ctx, `
		SELECT c.customer_contact_email, c.customer_name,
		       c.customer_authorized_person_fname, c.customer_authorized_person_lname
		FROM customer c
		WHERE c.customer_uuid = $1 AND c.customer_deleted_at IS NULL`, customerUUID,
	).Scan(&email, &companyName, &fname, &lname)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrPortalUserNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("contact info for invite: %w", err)
	}
	fullName = strings.TrimSpace(fname + " " + lname)
	if fullName == "" {
		fullName = companyName
	}
	return strings.TrimSpace(email), fullName, nil
}

// CustomerIDByUUID resolves a live customer without any eligibility check.
//
// Used by the list and revoke paths, which must keep working for a customer
// that has since been transitioned or un-approved — otherwise access could be
// granted and then never withdrawn. Only CustomerEligible gates granting.
func CustomerIDByUUID(ctx context.Context, q Querier, customerUUID string) (int, string, error) {
	var (
		id   int
		name string
	)
	err := q.QueryRow(ctx, `
		SELECT customer_id, customer_name FROM customer
		WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID,
	).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrPortalUserNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("customer by uuid: %w", err)
	}
	return id, name, nil
}

// CreateUser inserts a portal login for a customer.
func CreateUser(ctx context.Context, q Querier, identityID string, customerID int,
	email, fullName string, createdByEmployeeID *int) (*User, error) {
	var u User
	err := q.QueryRow(ctx, `
		INSERT INTO customer_portal_user (identity_id, customer_id, email, full_name, created_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (identity_id) DO UPDATE
			SET status = 'active', suspended_at = NULL, suspended_by = NULL,
			    revoked_at = NULL, revoked_by = NULL,
			    full_name = EXCLUDED.full_name, updated_at = CURRENT_TIMESTAMP
		RETURNING id, identity_id, customer_id, email, full_name, phone, status, created_at, suspended_at, revoked_at`,
		identityID, customerID, email, fullName, createdByEmployeeID,
	).Scan(&u.ID, &u.IdentityID, &u.CustomerID, &u.Email, &u.FullName, &u.Phone,
		&u.Status, &u.CreatedAt, &u.SuspendedAt, &u.RevokedAt)
	if err != nil {
		return nil, fmt.Errorf("create portal user: %w", err)
	}
	return &u, nil
}

// ListUsersForCustomer returns every portal login attached to a customer,
// including revoked ones so staff can see the access history.
func ListUsersForCustomer(ctx context.Context, q Querier, customerID int) ([]User, error) {
	rows, err := q.Query(ctx, `
		SELECT id, identity_id, customer_id, email, full_name, phone, status, created_at, suspended_at, revoked_at
		FROM customer_portal_user
		WHERE customer_id = $1
		ORDER BY created_at`, customerID)
	if err != nil {
		return nil, fmt.Errorf("list portal users: %w", err)
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.IdentityID, &u.CustomerID, &u.Email, &u.FullName,
			&u.Phone, &u.Status, &u.CreatedAt, &u.SuspendedAt, &u.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan portal user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate portal users: %w", err)
	}
	return users, nil
}

// GetUser loads one portal login by its id, scoped to a customer so a handler
// cannot act on a login belonging to a different customer.
func GetUser(ctx context.Context, q Querier, customerID int, id string) (*User, error) {
	var u User
	err := q.QueryRow(ctx, `
		SELECT id, identity_id, customer_id, email, full_name, phone, status, created_at, suspended_at, revoked_at
		FROM customer_portal_user
		WHERE id = $1 AND customer_id = $2`, id, customerID,
	).Scan(&u.ID, &u.IdentityID, &u.CustomerID, &u.Email, &u.FullName, &u.Phone,
		&u.Status, &u.CreatedAt, &u.SuspendedAt, &u.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPortalUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get portal user: %w", err)
	}
	return &u, nil
}

// RevokeUser withdraws a portal login. Never deletes — the row is the audit
// record that access was once granted.
func RevokeUser(ctx context.Context, q Querier, customerID int, id string, byEmployeeID *int) error {
	tag, err := q.Exec(ctx, `
		UPDATE customer_portal_user
		SET status = 'revoked', revoked_at = CURRENT_TIMESTAMP, revoked_by = $3,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND customer_id = $2 AND status = 'active'`, id, customerID, byEmployeeID)
	if err != nil {
		return fmt.Errorf("revoke portal user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalUserNotFound
	}
	return nil
}

// SuspendUser pauses a portal login without withdrawing it permanently. Only
// an active login may be suspended — suspending an already-suspended row is
// a no-op miss (ErrPortalUserNotFound), and a revoked login cannot be
// suspended, it must be re-granted instead.
func SuspendUser(ctx context.Context, q Querier, customerID int, id string, byEmployeeID *int) error {
	tag, err := q.Exec(ctx, `
		UPDATE customer_portal_user
		SET status = 'suspended', suspended_at = CURRENT_TIMESTAMP, suspended_by = $3,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND customer_id = $2 AND status = 'active'`, id, customerID, byEmployeeID)
	if err != nil {
		return fmt.Errorf("suspend portal user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalUserNotFound
	}
	return nil
}

// ResumeUser reactivates a suspended portal login. Deliberately does not
// re-check CustomerEligible — that gate is a precondition of the FIRST grant
// only (see CustomerEligible's doc comment and ResolveSession's, which
// likewise never consults customer_is_approved for a live session), so a
// customer suspended mid-renewal is never stuck behind a re-approval. Only a
// suspended login may be resumed; a revoked one must be re-granted through
// CreatePortalUser instead.
func ResumeUser(ctx context.Context, q Querier, customerID int, id string) error {
	tag, err := q.Exec(ctx, `
		UPDATE customer_portal_user
		SET status = 'active', suspended_at = NULL, suspended_by = NULL,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND customer_id = $2 AND status = 'suspended'`, id, customerID)
	if err != nil {
		return fmt.Errorf("resume portal user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalUserNotFound
	}
	return nil
}

// TenantUser is one row of the tenant-wide portal-user roster: a portal login
// plus the customer it belongs to and who granted it, for the Config-level
// list staff use to see every external login into the workspace at a glance
// (ListUsersForCustomer answers the narrower "this one customer's logins").
type TenantUser struct {
	User
	GrantedByName string
}

// ListUsersForTenant returns every portal login in the tenant, newest first,
// including revoked ones so the list doubles as an access history.
func ListUsersForTenant(ctx context.Context, q Querier) ([]TenantUser, error) {
	rows, err := q.Query(ctx, `
		SELECT cpu.id, cpu.identity_id, cpu.customer_id, c.customer_uuid, c.customer_name,
		       cpu.email, cpu.full_name, cpu.phone, cpu.status, cpu.created_at, cpu.suspended_at, cpu.revoked_at,
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), '')
		FROM customer_portal_user cpu
		JOIN customer c ON c.customer_id = cpu.customer_id
		LEFT JOIN employee e ON e.employee_id = cpu.created_by
		ORDER BY cpu.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tenant portal users: %w", err)
	}
	defer rows.Close()

	users := []TenantUser{}
	for rows.Next() {
		var u TenantUser
		if err := rows.Scan(&u.ID, &u.IdentityID, &u.CustomerID, &u.CustomerUUID, &u.CustomerName,
			&u.Email, &u.FullName, &u.Phone, &u.Status, &u.CreatedAt, &u.SuspendedAt, &u.RevokedAt,
			&u.GrantedByName); err != nil {
			return nil, fmt.Errorf("scan tenant portal user: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenant portal users: %w", err)
	}
	return users, nil
}

// UpdateProfile changes the fields a portal user may edit about themselves.
//
// Email is deliberately absent: it is the login and is globally unique on the
// control-plane identities table, so changing it is an identity operation, not
// a profile edit.
func UpdateProfile(ctx context.Context, q Querier, identityID, fullName, phone string) error {
	tag, err := q.Exec(ctx, `
		UPDATE customer_portal_user
		SET full_name = $2, phone = $3, updated_at = CURRENT_TIMESTAMP
		WHERE identity_id = $1 AND status = 'active'`, identityID, fullName, phone)
	if err != nil {
		return fmt.Errorf("update portal profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrPortalUserNotFound
	}
	return nil
}
