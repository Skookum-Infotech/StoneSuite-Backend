package chartofaccounts

import (
	"context"
	"fmt"
	"strings"

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
	if !validAccountUUID(uuid) {
		return nil, ClientError{Msg: fmt.Sprintf("%q is not a valid account id.", uuid)}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := loadCurrent(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	// RecordVersion is a plain int, so an omitted field and an explicit 0 are
	// indistinguishable, and both skip the check below. coa_account_record_version
	// starts at 1, so 0 is never a real version -- this is a deliberate opt-in:
	// a client that wants optimistic concurrency sends the version it read; one
	// that does not, does not. Consequence: two concurrent callers who both omit
	// it and change the same field will last-write-wins silently.
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

	if v, changed := changedString(in.Name, cur.name); changed {
		if v == "" {
			return nil, ClientError{Msg: "An account name is required."}
		}
		next.name = v
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "name", OldValue: cur.name, NewValue: next.name, EmployeeID: employeeID})
	}
	if v, changed := changedString(in.Description, cur.description); changed {
		next.description = v
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "description", OldValue: cur.description, NewValue: next.description, EmployeeID: employeeID})
	}
	if in.Type != nil && *in.Type != cur.acctType {
		if _, ok := attrSchema[*in.Type]; !ok {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Unknown account type %q. Valid types: %s.",
				*in.Type, strings.Join(ValidAccountTypes(), ", "))}
		}
		next.acctType = *in.Type
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionUpdate,
			Field: "type", OldValue: cur.acctType, NewValue: next.acctType, EmployeeID: employeeID})
	}
	if in.Attributes != nil {
		// Unlike the six sibling branches around it, this one is deliberately
		// exempt from change detection: next.attrs holds ciphertext once a bank
		// account number is set, and comparing ciphertext to ciphertext to
		// decide "did this change" is meaningless (a fresh nonce makes a
		// byte-identical resend look different anyway, and decrypting just to
		// compare would defeat the point of never handling plaintext outside
		// EncryptAttributes). So a resend of the same logical attributes still
		// bumps coa_account_record_version and appends an audit row. That is an
		// accepted, understood cost -- not a bug to fix with fuzzy comparison.
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
	} else if next.acctType != cur.acctType {
		// Type changed but no new attributes were supplied, so next.attrs is
		// still cur.attrs exactly as loaded from the DB. We deliberately do NOT
		// run that through ValidateAttributes: for a bank account it holds the
		// ENCRYPTED account number plus the server-derived accountNumberLast4
		// key (see EncryptAttributes/masking.go), and accountNumberLast4 is not
		// in any type's allowed key set -- a full re-validation would reject a
		// legitimately-stored account. Re-encrypting or otherwise touching
		// already-encrypted data here would also be wrong. So we only check
		// that every key the NEW type requires is still present (and non-blank)
		// in the existing attributes; this can't be fooled by the encrypted
		// value's shape because presence, not content, is all that matters for
		// keys other than accountNumber, and the account number key itself is
		// still present (as ciphertext) whenever it was ever set. A type change
		// that would leave a required key missing is rejected so the caller can
		// resupply attributes in the same request.
		if missing := missingRequiredAttrs(next.acctType, next.attrs); len(missing) > 0 {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Changing type to %q requires attribute(s): %s. Include them in this request.",
				next.acctType, strings.Join(missing, ", "))}
		}
		// Symmetric with the check above: the new type may also forbid keys the
		// old type allowed (e.g. bank -> general keeps a stored, encrypted
		// accountNumber). Reject rather than silently prune -- pruning would
		// drop data with no audit row. The caller's escape hatch is an explicit
		// "attributes" object in the same request (even {}), which takes the
		// in.Attributes != nil branch above and clears the stale keys through
		// ValidateAttributes/EncryptAttributes with a proper audit row.
		if extra := disallowedAttrs(next.acctType, next.attrs); len(extra) > 0 {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Changing type to %q does not allow attribute(s): %s. Send an explicit %q object in this request.",
				next.acctType, strings.Join(extra, ", "), "attributes")}
		}
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
	// AD-8: active implies visible. When the caller activates a hidden
	// account without also specifying isVisible, that is an implicit un-hide,
	// not a request to hide it -- without this, the check further down would
	// reject the activation with a message describing the opposite
	// transition ("Deactivate it before hiding it"), matching BulkUpdate.
	if in.IsActive != nil && *in.IsActive && in.IsVisible == nil && !cur.isVisible {
		next.isVisible = true
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionShow,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
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
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit update account (no-op): %w", err)
		}
		return acct, nil
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
