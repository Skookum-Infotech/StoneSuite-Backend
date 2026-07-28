package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

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

// missingRequiredAttrs returns, sorted, every key attrSchema[accountType]
// marks required that is absent from attrs or present but blank. It checks
// presence and non-blankness only -- not the full ValidateAttributes rules
// (unknown-key rejection, string-type enforcement) -- because it is used to
// re-check an account's EXISTING stored attributes on a type change, and
// those attrs may legitimately carry keys (accountNumberLast4) that
// ValidateAttributes would reject outright. An unknown accountType (schema
// absent) reports every field as satisfied; ValidateAttributes is the one
// place that rejects an unknown type.
func missingRequiredAttrs(accountType string, attrs map[string]any) []string {
	schema := attrSchema[accountType]
	var missing []string
	for k, f := range schema {
		if !f.required {
			continue
		}
		v, ok := attrs[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	return missing
}

// disallowedAttrs returns, sorted, every key present in attrs that
// attrSchema[accountType] does not permit. It is the symmetric counterpart to
// missingRequiredAttrs, used on the same re-check of an account's EXISTING
// stored attributes on a type change: the new type may allow fewer keys than
// the old one did. An unknown accountType (schema absent) reports every key as
// disallowed, same as ValidateAttributes would.
//
// accountNumberLast4Key is skipped only when it accompanies BankAccountNumberKey.
// It is server-derived (see EncryptAttributes), tracks BankAccountNumberKey, and
// is deliberately absent from every type's allowed key set, so reporting it
// alongside its own source would be noise -- the change is already rejected on
// accountNumber. Orphaned, it is a real violation: a row holding only the last-4
// hint would otherwise pass bank -> general and keep a bank artefact on a type
// that forbids it (AD-9). No current writer produces that shape, but seeded data
// or a manual SQL repair can.
func disallowedAttrs(accountType string, attrs map[string]any) []string {
	schema := attrSchema[accountType]
	_, hasAccountNumber := attrs[BankAccountNumberKey]
	var extra []string
	for k := range attrs {
		if k == accountNumberLast4Key && hasAccountNumber {
			continue
		}
		if _, allowed := schema[k]; !allowed {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	return extra
}

// boolStr renders a bool for the audit trail.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// changedString trims in and reports whether the result differs from cur. A
// nil in means the caller made no change (ok is false). Used by the Name and
// Description branches in Update so both trim-and-compare exactly once
// instead of computing strings.TrimSpace repeatedly, and so a whitespace-only
// edit (e.g. " Foo " for a name already "Foo") is correctly treated as no
// change and produces no phantom audit row.
func changedString(in *string, cur string) (string, bool) {
	if in == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(*in)
	if trimmed == cur {
		return "", false
	}
	return trimmed, true
}
