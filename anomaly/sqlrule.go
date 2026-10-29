package anomaly

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/approvalchain"
)

// audience says who a sqlRule's findings go to, before the person involved is
// removed. approvers is an approvalchain registry key whose live approvers are
// added (and "" adds none).
type audience struct {
	superAdmins bool
	owner       bool
	approvers   string
}

// sqlRule is a Rule defined by one query plus how to word and route its rows.
//
// query is an inner SELECT that must return exactly these columns, in order:
//
//	subject            text       unique per (rule, condition); see Finding.SubjectKey
//	record_uuid        text       NULL when the finding is not about one record
//	resource           text       the affected record's RBAC resource
//	label              text       what to call the record, e.g. 'Invoice INV-000012'
//	detail             text       one plain sentence on what is wrong
//	owner_identity     text       NULL, or the record owner's identity id
//	involved_identity  text       NULL, or the person whose action caused it (never alerted)
//	happened_at        timestamp  when it happened, newest first
//
// Bound parameters: $1 rule key, $2 row limit, $3 tenant-local today (date),
// $4.. the rule's args. The wrapper adds the "not already stored" filter and the
// limit, so a query only describes the condition.
type sqlRule struct {
	key      string
	severity Severity
	title    string // fmt pattern with one %s for the label
	query    string
	args     []any
	audience audience
}

// Key implements Rule.
func (r sqlRule) Key() string { return r.key }

const sqlRuleWrapper = `
	SELECT q.subject, q.record_uuid, q.resource, q.label, q.detail, q.owner_identity, q.involved_identity
	FROM (
		%s
	) q(subject, record_uuid, resource, label, detail, owner_identity, involved_identity, happened_at)
	WHERE $3::date IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM anomaly a
		WHERE a.anomaly_rule_key = $1 AND a.anomaly_subject_key = q.subject)
	ORDER BY q.happened_at DESC NULLS LAST, q.subject
	LIMIT $2`

// Detect implements Rule.
func (r sqlRule) Detect(ctx context.Context, q Querier, today time.Time) ([]Finding, error) {
	args := append([]any{r.key, MaxFindingsPerRule, today}, r.args...)
	rows, err := q.Query(ctx, fmt.Sprintf(sqlRuleWrapper, r.query), args...)
	if err != nil {
		return nil, fmt.Errorf("%s scan: %w", r.key, err)
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var (
			subject, resource, label, detail string
			record, owner, involved          *string
		)
		if err := rows.Scan(&subject, &record, &resource, &label, &detail, &owner, &involved); err != nil {
			return nil, fmt.Errorf("%s scan: scan: %w", r.key, err)
		}
		f := Finding{
			RuleKey: r.key, SubjectKey: subject, Severity: r.severity, Resource: resource,
			Title: fmt.Sprintf(r.title, label), Detail: detail,
			Audience: Audience{SuperAdmins: r.audience.superAdmins},
		}
		if record != nil {
			f.RecordUUID = *record
		}
		if involved != nil {
			f.InvolvedIdentityID = *involved
		}
		if r.audience.owner && owner != nil {
			f.Audience.IdentityIDs = append(f.Audience.IdentityIDs, *owner)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s scan: iterate: %w", r.key, err)
	}
	if r.audience.approvers == "" || len(out) == 0 {
		return out, nil
	}
	cfg, ok := approvalchain.ForWorkflowKey(r.audience.approvers)
	if !ok {
		return nil, fmt.Errorf("%s scan: %q is not in the approval registry", r.key, r.audience.approvers)
	}
	approvers, err := liveApproverIdentities(ctx, q, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s scan: %w", r.key, err)
	}
	for i := range out {
		out[i].Audience.IdentityIDs = append(out[i].Audience.IdentityIDs, approvers...)
	}
	return out, nil
}

// tpl fills {name} placeholders in a SQL template. It is only ever given
// compiled-in identifiers and fragments, never request data.
func tpl(sql string, kv ...string) string {
	return strings.NewReplacer(kv...).Replace(sql)
}

// ownerJoin joins the employee and user behind alias.col (an employee id) so the
// query can select ownerIdentity. It always uses the aliases eo and uo.
func ownerJoin(alias, col string) string {
	return fmt.Sprintf(`LEFT JOIN employee eo ON eo.employee_id = %s.%s
		LEFT JOIN users uo ON uo.id = eo.employee_user_id`, alias, col)
}

// ownerIdentity and involvedNone are the select-list fragments that go with
// ownerJoin and with a rule that has no person to exclude.
const (
	ownerIdentity = `uo.identity_id::text`
	involvedNone  = `NULL::text`
)

// stNotIn is a WHERE fragment keeping only rows whose status code (joined as
// alias st) is not in the text[] bound to param.
func stNotIn(param string) string {
	return fmt.Sprintf("st.record_status_code <> ALL(%s::text[])", param)
}

// unionAll joins per-module queries into one inner query.
func unionAll(parts []string) string { return strings.Join(parts, "\nUNION ALL\n") }
