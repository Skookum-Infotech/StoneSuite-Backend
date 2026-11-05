package approvalinbox

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// List returns one keyset page of the caller's inbox plus the per-module counts. Modules the
// caller may not read are absent from both. now is injected for a deterministic ageDays.
func List(ctx context.Context, q workflow.Querier, check CheckFunc, c Caller, p Params, now time.Time) (Page, error) {
	if err := checkScope(c, p.Scope); err != nil {
		return Page{}, err
	}
	sources := Permitted(check)
	counts, err := countStatement(ctx, q, sources, c, p.Scope)
	if err != nil {
		return Page{}, err
	}
	page := Page{Total: counts.Total, Modules: counts.Modules, Items: []Item{}}

	pageSources := sources
	if p.Module != "" {
		pageSources = nil
		for _, s := range sources {
			if s.Key == p.Module {
				pageSources = []Source{s}
			}
		}
	}
	if len(pageSources) == 0 || counts.Total == 0 {
		return page, nil
	}

	sql, args, err := buildPage(pageQuery{
		Sources: pageSources, Scope: p.Scope, EmployeeID: c.EmployeeID, Cursor: p.Cursor, Limit: p.Limit + 1,
	})
	if err != nil {
		return Page{}, err
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return Page{}, fmt.Errorf("list approval inbox: %w", err)
	}
	defer rows.Close()

	transition := make(map[string]bool, len(pageSources))
	for _, s := range pageSources {
		transition[s.Key] = s.CanTransition
	}
	for rows.Next() {
		var it Item
		var isApprover bool
		if err := rows.Scan(&it.WorkflowKey, &it.RecordUUID, &it.EnteredAt, &it.Number, &it.StatusCode,
			&it.StatusName, &it.OwnerName, &it.PartyName, &it.Amount, &it.RequiredApprovals,
			&it.ApprovedCount, &isApprover); err != nil {
			return Page{}, fmt.Errorf("scan approval inbox row: %w", err)
		}
		fillItem(&it, isApprover, transition[it.WorkflowKey], c.IsSuperAdmin, now)
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate approval inbox: %w", err)
	}
	if len(page.Items) > p.Limit {
		page.Items = page.Items[:p.Limit]
		page.HasMore = true
		last := page.Items[len(page.Items)-1]
		page.NextCursor = Cursor{EnteredAt: last.EnteredAt, WorkflowKey: last.WorkflowKey, UUID: last.RecordUUID}.Encode()
	}
	return page, nil
}

// fillItem adds the registry-derived fields and the action flags. canApprove needs the
// transition permission AND to be an approver (or a super admin, who may override);
// canReject additionally needs the gate to support Reject.
func fillItem(it *Item, isApprover, canTransition, superAdmin bool, now time.Time) {
	meta := metas[it.WorkflowKey]
	it.Label, it.Domain, it.RouteModule = meta.Label, meta.Domain, meta.RouteModule
	it.EnteredAt = it.EnteredAt.UTC()
	if age := int(now.Sub(it.EnteredAt).Hours() / hoursPerDay); age > 0 {
		it.AgeDays = age
	}
	it.CanApprove = canTransition && (isApprover || superAdmin)
	if cfg, ok := approvalchain.ForWorkflowKey(it.WorkflowKey); ok {
		if gate, ok := cfg.GateFor(it.StatusCode); ok {
			it.CanReject = it.CanApprove && gate.Reject != approvalchain.RejectUnsupported
		}
	}
}

// isApproverAnywhere reports, in one statement, whether employeeID is a live, active approver
// at any gate of any given module -- the dashboard tile's "show 0, caught up" condition.
func isApproverAnywhere(ctx context.Context, q workflow.Querier, sources []Source, employeeID int) (bool, error) {
	mods, err := resolveModules(sources)
	if err != nil {
		return false, err
	}
	b := &builder{args: []any{employeeID}} // only $1 is shared here
	parts := make([]string, 0, len(mods))
	for _, m := range mods {
		code := b.param(m.cfg.RecordTypeCode)
		parts = append(parts, m.replacer().Replace(`SELECT 1 FROM {apr} ea {live}
JOIN lkp_record_type rt ON rt.record_type_id = ea.record_type_id AND rt.record_type_code = `+code+`
WHERE ea.approver_employee_id = `+pEmployee+` AND ea.is_active`))
	}
	var ok bool
	if err := q.QueryRow(ctx, "SELECT EXISTS ("+strings.Join(parts, "\nUNION ALL\n")+")", b.args...).Scan(&ok); err != nil {
		return false, fmt.Errorf("check approver eligibility: %w", err)
	}
	return ok, nil
}
