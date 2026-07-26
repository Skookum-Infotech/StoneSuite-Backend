package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/secret"
)

// currentAccount is the pre-update snapshot the guard and audit trail need.
type currentAccount struct {
	id          int
	code        string
	name        string
	description string
	acctType    string
	attrs       map[string]any
	isPostable  bool
	isActive    bool
	isVisible   bool
	isSystem    bool
	version     int
}

// Update applies a partial change to one account. Code, sub-category and
// parent are immutable after create; a seeded (is_system) account may be
// renamed, described, retyped and toggled, but never recoded or deleted.
//
// Any change that retires the account -- deactivate, hide, or un-post -- runs
// guardRetire first (AD-7). is_postable never flips automatically (AD-6).
func Update(ctx context.Context, pool *pgxpool.Pool, c *secret.Cipher, uuid string, in UpdateInput, employeeID int) (*Account, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := loadCurrent(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if in.RecordVersion != 0 && in.RecordVersion != cur.version {
		return nil, ConflictError{Msg: "This account was changed by someone else. Reload and try again."}
	}

	// The guard covers every retiring transition in one place.
	retiring := (in.IsActive != nil && !*in.IsActive && cur.isActive) ||
		(in.IsVisible != nil && !*in.IsVisible && cur.isVisible) ||
		(in.IsPostable != nil && !*in.IsPostable && cur.isPostable)
	if retiring {
		if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
			return nil, err
		}
	}

	next := *cur
	var audits []historyRow

	if in.Name != nil && strings.TrimSpace(*in.Name) != cur.name {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, ClientError{Msg: "An account name is required."}
		}
		next.name = strings.TrimSpace(*in.Name)
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "name", OldValue: cur.name, NewValue: next.name, EmployeeID: employeeID})
	}
	if in.Description != nil && *in.Description != cur.description {
		next.description = strings.TrimSpace(*in.Description)
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "description", OldValue: cur.description, NewValue: next.description, EmployeeID: employeeID})
	}
	if in.Type != nil && *in.Type != cur.acctType {
		next.acctType = *in.Type
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "type", OldValue: cur.acctType, NewValue: next.acctType, EmployeeID: employeeID})
	}
	if in.Attributes != nil {
		validated, err := ValidateAttributes(next.acctType, in.Attributes)
		if err != nil {
			return nil, err
		}
		stored, err := EncryptAttributes(c, validated)
		if err != nil {
			return nil, err
		}
		next.attrs = stored
		// The bank number itself never reaches history (AD-10); appendHistory
		// redacts this field, and only the fact of a change is recorded.
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "attributes", EmployeeID: employeeID})
	}
	if in.IsPostable != nil && *in.IsPostable != cur.isPostable {
		next.isPostable = *in.IsPostable
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "is_postable", OldValue: boolStr(cur.isPostable),
			NewValue: boolStr(next.isPostable), EmployeeID: employeeID})
	}
	if in.IsActive != nil && *in.IsActive != cur.isActive {
		next.isActive = *in.IsActive
		act := actionActivate
		if !next.isActive {
			act = actionDeactivate
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_active", OldValue: boolStr(cur.isActive),
			NewValue: boolStr(next.isActive), EmployeeID: employeeID})
	}
	if in.IsVisible != nil && *in.IsVisible != cur.isVisible {
		next.isVisible = *in.IsVisible
		act := actionShow
		if !next.isVisible {
			act = actionHide
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
	}

	// AD-8: active implies visible. Reject rather than silently coercing --
	// the DB CHECK would fire anyway, and a 400 explains why.
	if next.isActive && !next.isVisible {
		return nil, ClientError{Msg: "An active account must stay visible. Deactivate it before hiding it."}
	}

	if len(audits) == 0 {
		acct, err := scanAccount(tx.QueryRow(ctx,
			accountSelect+` WHERE `+liveOnly+` AND a.coa_account_id = $1`, cur.id))
		if err != nil {
			return nil, fmt.Errorf("read back account: %w", err)
		}
		return acct, tx.Commit(ctx)
	}

	_, err = tx.Exec(ctx, `
		UPDATE coa_account SET
			coa_account_name = $2, coa_account_description = $3, coa_account_type = $4,
			coa_account_attributes = $5, coa_account_is_postable = $6,
			coa_account_is_active = $7, coa_account_is_visible = $8,
			coa_account_updated_at = CURRENT_TIMESTAMP, coa_account_updated_by = $9,
			coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, next.name, next.description, next.acctType, next.attrs,
		next.isPostable, next.isActive, next.isVisible, nullableInt(employeeID))
	if err != nil {
		return nil, fmt.Errorf("update account: %w", err)
	}

	for _, a := range audits {
		if err := appendHistory(ctx, tx, a); err != nil {
			return nil, err
		}
	}

	acct, err := scanAccount(tx.QueryRow(ctx,
		accountSelect+` WHERE `+liveOnly+` AND a.coa_account_id = $1`, cur.id))
	if err != nil {
		return nil, fmt.Errorf("read back updated account: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update account: %w", err)
	}
	return acct, nil
}

// loadCurrent reads the pre-update snapshot, locking the row so two concurrent
// updates cannot both pass the record-version check.
func loadCurrent(ctx context.Context, q rowQuerier, uuid string) (*currentAccount, error) {
	var c currentAccount
	err := q.QueryRow(ctx, `
		SELECT coa_account_id, coa_account_code, coa_account_name, coa_account_description,
		       coa_account_type, coa_account_attributes, coa_account_is_postable,
		       coa_account_is_active, coa_account_is_visible, coa_account_is_system,
		       coa_account_record_version
		FROM coa_account
		WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL
		FOR UPDATE`, uuid).
		Scan(&c.id, &c.code, &c.name, &c.description, &c.acctType, &c.attrs,
			&c.isPostable, &c.isActive, &c.isVisible, &c.isSystem, &c.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load account for update: %w", err)
	}
	if c.attrs == nil {
		c.attrs = map[string]any{}
	}
	return &c, nil
}

// boolStr renders a bool for the audit trail.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
