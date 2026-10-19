package automation

import (
	"fmt"
	"regexp"
	"sort"

	"stonesuite-backend/approvalchain"
)

// identPattern is the shape every registry identifier interpolated into SQL must match.
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// approvalSpec is one (module x gate) the approval_stuck job scans.
// Values come only from the approvalchain registry, never from input.
type approvalSpec struct {
	Workflow       string
	DisplayName    string
	RecordTypeCode string
	Gate           approvalchain.Gate
	Cfg            approvalchain.ModuleConfig
}

// approvalSpecs derives one spec per notify-wired module and gate that can be
// rejected back to an earlier status, ordered by workflow key then gate order.
// In-place gates are excluded: "pending since" is meaningless for them.
func approvalSpecs() []approvalSpec {
	keys := approvalchain.Keys()
	sort.Strings(keys)
	var out []approvalSpec
	for _, key := range keys {
		cfg, ok := approvalchain.ForWorkflowKey(key)
		if !ok || cfg.DisplayName == "" {
			continue
		}
		for _, g := range cfg.Gates {
			if g.Reject == approvalchain.RejectInPlace {
				continue
			}
			out = append(out, approvalSpec{
				Workflow: key, DisplayName: cfg.DisplayName,
				RecordTypeCode: cfg.RecordTypeCode, Gate: g, Cfg: cfg,
			})
		}
	}
	return out
}

// ident validates every registry identifier a job interpolates into SQL.
func (s approvalSpec) ident() error {
	r := s.Cfg.Record
	for name, v := range map[string]string{
		"approver table": s.Cfg.ApproverTable, "approval table": s.Cfg.ApprovalTable,
		"table": r.Table, "history table": r.HistoryTable, "id column": r.IDColumn,
		"uuid column": r.UUIDColumn, "status column": r.StatusColumn,
		"approval status column": r.ApprovalStatusColumn, "deleted column": r.DeletedAtColumn,
		"created column": r.CreatedAtColumn, "owner column": r.OwnerColumn, "number column": r.NumberColumn,
	} {
		if !identPattern.MatchString(v) {
			return fmt.Errorf("approval spec %s: invalid %s %q", s.Workflow, name, v)
		}
	}
	return nil
}
