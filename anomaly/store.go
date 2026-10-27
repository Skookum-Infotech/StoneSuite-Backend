package anomaly

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// activeSuperAdminsSQL is the same role filter approvalchain uses for its
// orphaned-gate fallback: active users holding the immutable super_admin role.
const activeSuperAdminsSQL = `
	SELECT DISTINCT u.identity_id::text
	FROM users u
	JOIN user_roles ur ON ur.user_id = u.id
	JOIN roles r ON r.id = ur.role_id
	WHERE r.key = 'super_admin' AND u.status = 'active'`

// mergeRecipients unions the identity-id lists, drops blanks and the excluded
// identity (the person who caused the finding), and returns a sorted result so
// the stored rows and the tests are deterministic.
func mergeRecipients(exclude string, lists ...[]string) []string {
	seen := map[string]struct{}{}
	for _, list := range lists {
		for _, id := range list {
			if id == "" || id == exclude {
				continue
			}
			seen[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// resolveRecipients turns a finding's audience into the identity ids to store,
// never including the person involved.
func resolveRecipients(ctx context.Context, q Querier, f Finding) ([]string, error) {
	var admins []string
	if f.Audience.SuperAdmins {
		var err error
		if admins, err = queryIdentityIDs(ctx, q, activeSuperAdminsSQL); err != nil {
			return nil, fmt.Errorf("resolve super admins: %w", err)
		}
	}
	return mergeRecipients(f.InvolvedIdentityID, f.Audience.IdentityIDs, admins), nil
}

func queryIdentityIDs(ctx context.Context, q Querier, sql string) ([]string, error) {
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// record stores f and its recipients through tx. It reports false, and writes
// nothing, when a finding for the same (rule, subject) already exists.
func record(ctx context.Context, tx pgx.Tx, f Finding, recipients []string) (bool, error) {
	var id int
	err := tx.QueryRow(ctx, `
		INSERT INTO anomaly (anomaly_rule_key, anomaly_subject_key, anomaly_severity, anomaly_resource,
		                     anomaly_record_uuid, anomaly_title, anomaly_detail, anomaly_involved_identity_id)
		VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8::uuid)
		ON CONFLICT (anomaly_rule_key, anomaly_subject_key) DO NOTHING
		RETURNING anomaly_id`,
		f.RuleKey, f.SubjectKey, string(f.Severity), f.Resource,
		nullIfEmpty(f.RecordUUID), clip(f.Title, maxTitleLen), f.Detail, nullIfEmpty(f.InvolvedIdentityID),
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert anomaly %s/%s: %w", f.RuleKey, f.SubjectKey, err)
	}
	if len(recipients) == 0 {
		return true, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO anomaly_recipient (anomaly_id, recipient_identity_id)
		SELECT $1, unnest($2::uuid[])
		ON CONFLICT DO NOTHING`, id, recipients); err != nil {
		return false, fmt.Errorf("insert recipients for anomaly %d: %w", id, err)
	}
	return true, nil
}

// nullIfEmpty maps "" to a SQL NULL.
func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
