package anomaly

import (
	"context"
	"fmt"
	"time"

	"stonesuite-backend/approvalchain"
)

const (
	selfApprovalSubjectSep = ":"
	selfApprovalDetail     = "The person who owns this record also signed it off, so nobody else reviewed it. " +
		"If that is expected for your team, mark this finding as dismissed."
)

// SelfApproval flags an approval signed by the owner of the very record it
// approves (no separation of duties). It covers every approval-chain module
// that is wired for notifications, so a new module is covered by registering
// it in approvalchain/registry.go, with no change here.
type SelfApproval struct{}

// Key implements Rule.
func (SelfApproval) Key() string { return RuleSelfApproval }

// Detect implements Rule.
func (SelfApproval) Detect(ctx context.Context, q Querier, _ time.Time) ([]Finding, error) {
	var out []Finding
	for _, key := range approvalchain.Keys() {
		cfg, ok := approvalchain.ForWorkflowKey(key)
		if !ok || !selfApprovalCovers(cfg) {
			continue
		}
		room := MaxFindingsPerRule - len(out)
		if room <= 0 {
			break
		}
		found, err := selfApprovalsFor(ctx, q, key, cfg, room)
		if err != nil {
			return out, fmt.Errorf("self approval scan of %s: %w", key, err)
		}
		out = append(out, found...)
	}
	return out, nil
}

// selfApprovalCovers reports whether cfg has the owner and number columns the
// scan reads. DisplayName is the registry's own "wired for notifications" mark.
func selfApprovalCovers(cfg approvalchain.ModuleConfig) bool {
	r := cfg.Record
	return cfg.DisplayName != "" && cfg.Resource != "" && r.OwnerColumn != "" && r.NumberColumn != ""
}

// selfApprovalsFor reads one module's un-reported self-approvals. Every
// identifier interpolated below comes from the compiled-in approvalchain
// registry, never from a request.
func selfApprovalsFor(ctx context.Context, q Querier, key string, cfg approvalchain.ModuleConfig, limit int) ([]Finding, error) {
	r := cfg.Record
	notDeleted := ""
	if r.DeletedAtColumn != "" {
		notDeleted = fmt.Sprintf("AND r.%s IS NULL", r.DeletedAtColumn)
	}
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT r.%[1]s::text, COALESCE(r.%[2]s::text, ''), ap.approver_employee_id, u.identity_id::text
		FROM %[3]s ap
		JOIN %[4]s r ON r.%[5]s = ap.%[5]s
		JOIN employee e ON e.employee_id = ap.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE r.%[6]s = ap.approver_employee_id %[7]s
		  AND NOT EXISTS (
		      SELECT 1 FROM anomaly a
		      WHERE a.anomaly_rule_key = $1
		        AND a.anomaly_subject_key = $2::text || '%[8]s' || r.%[1]s::text || '%[8]s' || ap.approver_employee_id::text)
		ORDER BY ap.approved_at DESC
		LIMIT $3`,
		r.UUIDColumn, r.NumberColumn, cfg.ApprovalTable, r.Table, r.IDColumn, r.OwnerColumn, notDeleted, selfApprovalSubjectSep),
		RuleSelfApproval, key, limit)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []Finding
	for rows.Next() {
		var recordUUID, number, identity string
		var approverEmployeeID int
		if err := rows.Scan(&recordUUID, &number, &approverEmployeeID, &identity); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, Finding{
			RuleKey:            RuleSelfApproval,
			SubjectKey:         fmt.Sprintf("%s%s%s%s%d", key, selfApprovalSubjectSep, recordUUID, selfApprovalSubjectSep, approverEmployeeID),
			Severity:           SeverityWarning,
			Resource:           cfg.Resource,
			RecordUUID:         recordUUID,
			Title:              selfApprovalTitle(cfg.DisplayName, number),
			Detail:             selfApprovalDetail,
			InvolvedIdentityID: identity,
			Audience:           Audience{SuperAdmins: true},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return out, nil
}

// selfApprovalTitle names the record, falling back to the module name alone
// when the record has no number yet.
func selfApprovalTitle(displayName, number string) string {
	if number == "" {
		return fmt.Sprintf("%s was approved by its own owner", displayName)
	}
	return fmt.Sprintf("%s %s was approved by its own owner", displayName, number)
}
