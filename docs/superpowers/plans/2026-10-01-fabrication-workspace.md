# Fabrication Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Execution method awaits user selection; no implementation has started.

**Goal:** Deliver the approved action-driven fabrication workflow across backend and frontend, with independent pieces and accurate inventory and delivery accounting.

**Architecture:** Keep fabrication as the job aggregate and reuse inventory, purchasing, approvals, attachments, and tenant authorization. Explicit commands derive state; versioned jobs preserve legacy history. Deliver vertical slices behind a workflow-version boundary, then enable the complete workflow after end-to-end verification.

**Tech Stack:** Go, pgx/PostgreSQL, existing approvalchain and query packages; React/TypeScript, TanStack Query, Tailwind and existing UI primitives; Go tests and Vitest.

**Spec:** ../specs/2026-10-01-fabrication-workspace-design.md (approved by user).

## Global Constraints

- Pieces progress independently; the job summarizes their progress and blockers.
- Each cutting run serves one job. Remnants return to inventory before allocation to another job or sales order.
- Replacement pieces require an approved remake request.
- Supply-only jobs finish at acknowledged handover. Installed jobs finish after installation and customer sign-off.
- Reuse these records and services. Do not introduce a second inventory ledger.
- Every employee route uses TenantResolver, resource/action permission checks, and record scope enforcement; out-of-scope records return 404.
- All database changes are additive and idempotent in the canonical tenant schema.
- Desktop, tablet, and phone are supported at release. Offline operation and automatic slab nesting are excluded.
- Preserve unrelated dirty files in both repositories. No deployment, push, or production data migration is part of execution without separate authorization.
- Use scoped conventional commits when executing; never stage the entire existing dirty tree. Keep new files focused and below 300 lines.

## Repository roots and ownership

`BE` = /Users/prasannakumarnagaboyina/pkumardev/StoneSuite-Backend

`FE` = /Users/prasannakumarnagaboyina/pkumardev/StoneSuite-WebUI

Paths below are relative to these roots. Do not implement in the old StoneSuite monorepo. Read each repository's AGENTS.md before execution. Existing files named below were located during planning; new files are explicitly marked. Audit exact surrounding contracts before editing because another task may have changed the checkout since this plan was written.

## Review Focus

1. A response lost after physical consumption must be safely replayed without a second ledger posting (Task 2 and Task 7).
2. An approved revision changed in another browser must block stale purchasing/cutting decisions (Task 3 and Task 7).
3. A rejected slab moved to an ordinary bin must remain unavailable (Task 5).
4. A remake after partial handover must not fulfill the same sales-order obligation twice (Tasks 9 and 10).
5. Existing jobs with consumed slabs and missing historical evidence must retain truthful history (Task 13).

## Contract decisions

Use explicit routes below `/api/tenant/fabrication-jobs/{uuid}`. Commands include `expectedVersion` and `requestId`; IDs are UUIDs, record versions are integers. Responses include the changed record and refreshed job summary/actions. Use 400 for malformed inputs, 403 for permission denial, 404 for inaccessible records, and 409 for stale state or unmet operational prerequisites. Existing JSON errors retain `success: false` and `message`, with optional structured blocker codes.

Proposed shared API types, defined in Task 1 and mirrored in TypeScript:

```go
// CommandMeta identifies a replay-safe mutation against a known record version.
type CommandMeta struct {
    ExpectedVersion int64 `json:"expectedVersion"`
    RequestID string `json:"requestId"`
}
// ActionBlocker describes a resolvable operational prerequisite.
type ActionBlocker struct {
    Code string `json:"code"`
    Message string `json:"message"`
    TargetID string `json:"targetId,omitempty"`
}
// AvailableAction is a server-computed command affordance.
type AvailableAction struct {
    Code string `json:"code"`
    Label string `json:"label"`
    Enabled bool `json:"enabled"`
    InputType string `json:"inputType"`
    Blockers []ActionBlocker `json:"blockers"`
}
```

Use typed payloads for each command, not arbitrary maps. TypeScript uses a discriminated union keyed by action code. Database JSON evidence snapshots are typed at the API boundary. Summary shape: `requiredPieces`, `completedPieces`, `stageCounts`, `blockers`, `availableActions`, `version`, `workflowVersion`. Authorization filters actions before serialization; enabled actions are rechecked inside the write transaction.

A run locks its input units on start and posts consumption/recovery on completion. Commands never release an already-started run as untouched stock. Existing legacy consumption remains unchanged for version-1 jobs until explicitly migrated.

## Implementation tasks

Each task follows the test-first checklist below. Code fragments specify central behavior; implement surrounding adapters against the existing code, not by copying an unrelated module wholesale. The independent acceptance cases are mandatory even if the implementation differs internally.

### Task 1: Workflow schema and typed states

**Files:** BE modify fabrication/types.go, database/migrations/tenant/schema.sql; create fabrication/actions_types.go, fabrication/piece_state.go, fabrication/piece_state_test.go, fabrication/schema_dbtest_test.go. FE create src/types/fabricationActions.ts.

**Interfaces:** Produce CommandMeta, ActionBlocker, AvailableAction and PieceStage constants; existing Job gains workflowVersion/version and typed summary. Do not change existing statuses for old jobs.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```go
func TestPieceCompletionMode(t *testing.T) {
    assert.True(t, PieceComplete(StageHandedOver, DeliverySupplyOnly))
    assert.False(t, PieceComplete(StageHandedOver, DeliveryInstalled))
    assert.True(t, PieceComplete(StageSignedOff, DeliveryInstalled))
}
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Add workflow_version default 1, piece stage/version and delivery mode using guarded ALTER statements. Add version-2 action-event and delivery-obligation tables with tenant-local foreign keys and unique request identity. New workflow remains disabled until Task 13. Piece states are planned, ready_cutting, cutting, edging, qc, qc_passed, handed_over, installed, signed_off; holds are orthogonal.
- [ ] Add and run these failure/concurrency cases: Reapply tenant schema twice; verify no changes to legacy statuses or inventory. Test zero-piece jobs never complete and cancelled/replaced originals do not inflate required counts.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication -run TestPieceCompletionMode -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication workflow schema and typed states` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 2: Replay-safe action execution and authorization

**Files:** BE create fabrication/action_store.go, fabrication/action_policy.go, fabrication/action_store_dbtest_test.go, controllers/fabrication_actions.go, controllers/fabrication_actions_test.go; modify controllers/fabrication.go, main.go. FE modify src/services/fabricationService.ts.

**Interfaces:** Consume CommandMeta. Produce ExecuteAction(ctx, pool, jobUUID, actorEmployeeID, command) through typed command adapters and a shared internal transaction wrapper; Policy(snapshot, permissions) returns []AvailableAction. No arbitrary target status command.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// DB acceptance sequence:
first := executeValidCommand(requestID, version)
retry := executeValidCommand(requestID, version)
assert.Equal(t, first.EventID, retry.EventID)
assert.Equal(t, 1, countEvents(requestID))
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Within one transaction authorize subject, lock job/version, claim request identity, compare payload hash, apply typed command, update version and store result/event. Scope request IDs to tenant, actor and command target. Authenticate replays before returning a stored result; same ID/different payload returns 409. Register routes through tenantChain. Refresh actions from committed state.
- [ ] Add and run these failure/concurrency cases: Add DB tests with two connections racing the same version; exactly one distinct command wins. Test replay after response loss, altered payload, missing permission, and cross-scope UUID. Return 404 without record metadata on IDOR denial.
- [ ] Run verification from the appropriate repository root:

```text
go test ./controllers ./fabrication -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication replay-safe action execution and authorization` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 3: Template revision and sales-order comparison

**Files:** BE create fabrication/template_types.go, fabrication/template_store.go, fabrication/template_diff.go, fabrication/template_diff_test.go, controllers/fabrication_templates.go; modify fabrication/store_create.go, database/migrations/tenant/schema.sql, main.go. FE create src/pages/sales/components/FabricationTemplateReview.tsx, src/lib/fabricationTemplateDiff.ts, src/lib/fabricationTemplateDiff.test.ts.

**Interfaces:** Produce TemplateRevision with immutable submitted piece/material/scope/price snapshot, source SO version and revision number. Actions: save_template_draft, submit_template, supersede_template. GET/POST templates and POST templates/{revision}/submit.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```go
func TestTemplateApprovalClassification(t *testing.T) {
    assert.False(t, ClassifyChange(Change{DimensionsChanged: true}).CustomerRequired)
    assert.True(t, ClassifyChange(Change{MaterialChanged: true}).CustomerRequired)
    assert.True(t, ClassifyChange(Change{PriceChanged: true}).CustomerRequired)
    assert.True(t, ClassifyChange(Change{ScopeChanged: true}).CustomerRequired)
}
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Enforce approved SO on both job creation entry points. Snapshot source line identities and measured requirements. Classify dimension-only changes as internal approval; material, price or scope changes also require customer approval. Do not silently modify the approved order. Apply the approved change through an explicit SO revision/update integration with source-version checks. Freeze cut-piece requirements; later physical changes use remake work.
- [ ] Add and run these failure/concurrency cases: Test stale SO versions, edited submitted template, cross-job line references and post-cut edits. UI comparison preserves draft on errors and highlights units/material differences without depending on color.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./salesorder ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication template revision and sales-order comparison` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 4: Internal and customer revision approvals

**Files:** BE create fabrication/revision_approval.go, fabrication/approval_requests.go, fabrication/approval_requests_dbtest_test.go, controllers/fabrication_customer_approval.go; modify approvalchain/registry.go, approvalchain/route.go, database/migrations/tenant/schema.sql, main.go. FE create src/pages/sales/components/FabricationApprovalPanel.tsx and src/pages/customer/FabricationApprovalPage.tsx; modify src/router/index.tsx.

**Interfaces:** Produce revision approval requests containing snapshotted stages and immutable decisions. Actions: approve_revision, reject_revision, record_customer_approval, issue_customer_approval_link. Reuse approvalchain decision/notification primitives; do not encode piece progress into document gate transitions.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Required acceptance cases:
// stage 2 before stage 1 => 409; wrong approver => 403
// superseded revision link => 409; expired/revoked grant => denied
// approval of current revision => exactly one recorded decision
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Approve internal stages in order; record customer approval against exact revision. Staff evidence includes customer name, received date, channel and attachment/reference, plus staff actor. Scoped links store only hashed tokens, expire after 7 days, allow revocation and bind tenant/subject/revision server-side. Portal actions verify customer relationship. Public grant handlers never accept arbitrary tenant context. Revision replacement invalidates outstanding grants.
- [ ] Add and run these failure/concurrency cases: Test token replay, customer from another account, attachment ownership, approval config edited during an active request and transaction rollback. Required customer approval cannot be bypassed by an internal approval override. Notifications link directly to the pending subject.
- [ ] Run verification from the appropriate repository root:

```text
go test ./approvalchain ./fabrication ./controllers ./middleware -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication internal and customer revision approvals` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 5: Slab receipt inspection and supplier resolution

**Files:** BE modify itemreceipt/slabs.go, itemreceipt/store_post.go, inventory/unit_receive.go, inventory/unit_store_search.go, inventory/allocation.go; create itemreceipt/inspection.go, itemreceipt/inspection_dbtest_test.go, controllers/itemreceipt_inspection.go; modify database/migrations/tenant/schema.sql, main.go. FE create src/pages/sales/components/FabricationReceiptInspection.tsx.

**Interfaces:** Produce per-unit inspection disposition pending/accepted/rejected independent of bin and physical existence; supplier resolution open/replacement/refund with actual purchasing-document references.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// DB acceptance sequence:
// post slab receipt -> search allocatable => absent
// reject; move to normal bin -> still absent
// replacement accepted -> replacement available, original rejected
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Posted new serialized receipts create physically tracked units pending inspection, excluded from allocatable stock. Accept makes stock eligible once; reject entire slab excludes it everywhere and records reason/photos and quarantine location. Preserve legacy units as legacy-unverified without inventing a pass. Link rejected stock to return movement and replacement receipt or credit/refund evidence; preserve financial posting separately from available stock.
- [ ] Add and run these failure/concurrency cases: Test duplicate inspection requests, partially inspected multi-slab receipts, voiding receipts with reserved units, and refund recorded without a financial document. Physical stock totals and available totals must remain separately correct. Trace existing receipt accounting before adding the predicate to every reservation path.
- [ ] Run verification from the appropriate repository root:

```text
go test ./itemreceipt ./inventory ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication slab receipt inspection and supplier resolution` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 6: Material shortages and WIP transfers

**Files:** BE modify fabrication/materials.go, fabrication/allocation.go, inventory/types_bin.go, inventory/bin_store_write.go, controllers/inventory_bins.go; create fabrication/procurement.go, fabrication/wip.go, fabrication/wip_dbtest_test.go; reuse inventorytransfer/store_write.go and store_legs.go; modify database/migrations/tenant/schema.sql. FE modify FabricationMaterialsTab.tsx under src/pages/sales/components; create FabricationWipTransferDialog.tsx alongside it.

**Interfaces:** Actions: allocate_material, create_shortage_po, transfer_to_wip. PO lines reference job/template/material requirements. WIP bin has optional machine label; general WIP requires no machine.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// DB acceptance cases:
// sufficient area but incompatible thickness => allocation blocked
// insufficient approval => shortage PO command blocked
// transfer to general WIP and saw-specific WIP => both valid
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Allocate only compatible accepted units or usable remnants; verify dimensional/layout suitability explicitly rather than area alone. Create draft shortage POs using purchase_order:create permission and existing PO approval flow; command replay cannot create duplicate POs. Bin transfer requires source/destination validation and inventory movement permission. Posted transfer updates actual location before start-cutting becomes enabled.
- [ ] Add and run these failure/concurrency cases: Test concurrent reservation, inactive/cross-warehouse bins, held jobs and stale template approval. Transfers preserve on-hand quantity. Stock selection shows dimensions, finish, inspection, availability and bin path.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./inventory ./inventorytransfer ./purchaseorder ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication material shortages and wip transfers` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 7: Cutting runs and single-ledger accounting

**Files:** BE create fabrication/cutting_types.go, fabrication/cutting_start.go, fabrication/cutting_complete.go, fabrication/cutting_dbtest_test.go, inventory/cut_tx.go; modify inventory/unit_cut.go, inventory/consumption.go, fabrication/store_transition.go, database/migrations/tenant/schema.sql; create controllers/fabrication_cutting.go. FE create FabricationCuttingRunDialog.tsx and FabricationRemnantFields.tsx under src/pages/sales/components.

**Interfaces:** Actions: start_cutting_run, complete_cutting_run, dispose_started_run. Run links one job, input units, selected pieces, approved revision and WIP bin. Extract shared inventory transaction primitive accepting pgx.Tx so fabrication owns the encompassing transaction.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// DB acceptance sequence:
// complete same request twice => one parent consumption
// one parent 10 area, recovered 3 => net inventory decrease 7
// second job allocates recovered unit after completion => success
// second active run using parent => conflict
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Lock units in stable order. Start verifies current approval, inspection, reservation, WIP location and no other active run. Complete consumes each input once, records output pieces and individually measured remnants with destination bins, and links recovered ledger entries. Validate per-parent conservation using existing unit conversion/area rounding; tolerance is one stored area precision increment (0.001 in the unit area). Reject non-finite dimensions and impossible recovered totals. No remnant silently defaults to a bin.
- [ ] Add and run these failure/concurrency cases: Test two concurrent completions, failure halfway through recovery, mixed area units, zero remnants, repeated serial and later remnant-of-remnant lineage. Started-run cancellation requires measured disposition; never release a physically cut slab as untouched. Legacy CUTG posting must not run for v2 jobs.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./inventory -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication cutting runs and single-ledger accounting` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 8: Piece operations, QC and routine rework

**Files:** BE create fabrication/operations.go, fabrication/quality.go, fabrication/quality_dbtest_test.go, controllers/fabrication_quality.go; modify fabrication/pieces.go, fabrication/steps.go, database/migrations/tenant/schema.sql. FE create FabricationPieceDrawer.tsx, FabricationQualityDialog.tsx under src/pages/sales/components.

**Interfaces:** Actions: start_edging, complete_edging, submit_qc, record_qc, record_rework, complete_rework. Operation records have piece, actor, time, result and prior cycle link.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Acceptance sequence:
// piece A passes QC; piece B fails -> A can hand over, B cannot
// B rework completes -> QC required again, old failure retained
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Cutting completion makes output pieces eligible for edging; completed edging makes QC available. Record pass/fail and findings per piece; failed QC requires rework or remake resolution. Routine rework specifies edging/polishing repair, assignment and reason; completing repair returns to QC. Preserve every prior QC decision and prohibit changing lifecycle through old checklist status edits.
- [ ] Add and run these failure/concurrency cases: Test unauthorized inspector, bulk selection containing ineligible pieces, repeated QC submissions and held jobs. Prefer atomic bulk execution: reject the selection with per-piece blockers instead of silently partially applying it.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication piece operations, qc and routine rework` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 9: Remake request and configurable approval

**Files:** BE create fabrication/remake_types.go, fabrication/remake_store.go, fabrication/remake_approval.go, fabrication/remake_dbtest_test.go, controllers/fabrication_remakes.go; modify approvalchain/registry.go, database/migrations/tenant/schema.sql. FE create FabricationRemakeDialog.tsx and FabricationRemakesPanel.tsx under src/pages/sales/components.

**Interfaces:** Actions: request_remake, approve_remake, reject_remake. Reuse Task 4 approval requests with subject kind remake. Replacement piece references original piece and the same fulfillment obligation.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Acceptance sequence:
// request -> cutting blocked; final approval -> replacement planned
// replay final approval -> still one replacement
// replacement delivery -> original obligation completed once
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Capture fault reason, selected pieces, extra material requirement and evidence. Snapshot configured single/sequential approval stages. Missing configured approvers blocks submission and exposes a configuration link; never auto-approve a remake. Approval creates replacement piece work exactly once; rejection preserves the failure. A remake of a replacement retains complete lineage and only one current active successor per obligation.
- [ ] Add and run these failure/concurrency cases: Test self-approval according to existing configured policy, stage order, concurrent requests for same piece, repeated replacement and remakes reported after handover. If an already completed job needs a remake, reopen its outstanding operational obligation without adding another SO fulfilled quantity.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./approvalchain ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication remake request and configurable approval` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 10: Partial handover, installation and sign-off

**Files:** BE create fabrication/handover_types.go, fabrication/handover_store.go, fabrication/installation.go, fabrication/fulfillment.go, fabrication/handover_dbtest_test.go, controllers/fabrication_handovers.go; modify fabrication/store_transition.go, database/migrations/tenant/schema.sql. FE create FabricationHandoverDialog.tsx, FabricationInstallationPanel.tsx under src/pages/sales/components.

**Interfaces:** Actions: assign_installer, create_handover, acknowledge_handover, record_installation, record_signoff. Handover lines reference specific current pieces; delivery obligation has one fulfillment posting identity.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Acceptance sequence:
// three pieces; hand over two -> job remains open
// supply-only third acknowledged -> complete
// installed third handed over -> incomplete until sign-off
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Allow only QC-passed selected pieces and record recipient, handover date and acknowledgment evidence. Supply-only finishes on acknowledgment; installed mode needs installation completion and customer sign-off. Capture signer, date, acknowledgment text, attachments/reference and recording actor. Account for fulfilled quantity in the SO line unit using explicit obligation quantities; never add a flat one to area lines or reuse slab-consumed area as delivered quantity.
- [ ] Add and run these failure/concurrency cases: Test overlapping concurrent handovers, duplicate acknowledgment, remake of delivered piece, and line quantities shared across jobs. Keep financial fulfillment audit separate from operational replacement progress.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./salesorder ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication partial handover, installation and sign-off` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 11: Optional external installer access

**Files:** BE create fabrication/installer_access.go, fabrication/installer_access_dbtest_test.go, controllers/installer_fabrication.go, middleware/installer.go; modify main.go, database/migrations/tenant/schema.sql. FE create src/pages/installer/InstallerWorkPage.tsx, src/services/installerService.ts; modify src/router/index.tsx.

**Interfaces:** Produce contact records plus optional authenticated installer account grants for explicit handover assignments. Installer responses contain only assigned piece/job-site instructions and necessary contact details.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Security acceptance cases:
// assigned handover => minimal details
// other handover UUID => 404
// sales order price/inventory lists => denied
// revoked assignment with existing session => denied
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Reuse identity authentication primitives but add explicit installer containment and tenant resolution; do not give an installer an employee role merely to enable login. Staff can perform all installer operations without external accounts. Grant acknowledge/upload/completion actions only for assigned work; revocation blocks future reads and writes. Customer sign-off remains customer evidence, not an installer approval.
- [ ] Add and run these failure/concurrency cases: Test file download/upload scope, guessed piece IDs, tenant switching and stale sessions. Never serialize financial/internal QC blame notes into installer responses. UI supports both staff-managed and account-enabled contacts.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./controllers ./middleware -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication optional external installer access` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 12: Responsive action-driven workspace and production queue

**Files:** FE modify src/pages/sales/FabricationJobListPage.tsx, FabricationJobDetailPage.tsx, EditFabricationJobPage.tsx; modify src/services/fabricationService.ts, src/types/fabrication.ts; create src/hooks/useFabricationAction.ts, src/pages/sales/components/FabricationActionPanel.tsx, FabricationProgressSummary.tsx, FabricationStageBoard.tsx, FabricationActivityTimeline.tsx and corresponding .test.tsx files. BE modify fabrication/store_search.go, fabrication/resolver.go; create fabrication/summary.go, fabrication/summary_test.go, fabrication/activity.go.

**Interfaces:** Consume summary and availableActions from Task 1/2 and typed dialogs from Tasks 3–11. Search supports server-side stage, due date, assignee, WIP and blocker filters with opaque cursors.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// Component test outline:
render(<FabricationActionPanel actions={blockedCuttingActions} />);
expect(screen.getByRole("button", { name: "Start cutting" })).toBeDisabled();
expect(screen.getByText("Transfer slabs to a WIP bin first")).toBeVisible();
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Replace v2 status controls with action panel; retain read-only badge. Backend computes aggregate counts from required pieces and pending obligations. Queue table is default, board optional and non-draggable. Detail uses approved sections, compact next-action overview, piece drawer and linked activity. Phone uses cards and stacked forms; tablet uses collapsible sections. Query hook sends one stable requestId per attempted command, preserves it for retries, invalidates affected stock/PO/job queries, displays server blockers and success toast.
- [ ] Add and run these failure/concurrency cases: Test loading permission state fails closed, zero-piece summary, filtered/paginated board counts, 409 recovery retaining user input, keyboard dialog focus and double clicks. Inspect desktop/tablet/phone at 1440/768/390px; no primary-action horizontal scrolling. Activity errors get retry UI rather than an empty-history claim.
- [ ] Run verification from the appropriate repository root:

```text
cd /Users/prasannakumarnagaboyina/pkumardev/StoneSuite-WebUI
npm test -- --run
cd /Users/prasannakumarnagaboyina/pkumardev/StoneSuite-Backend
go test ./fabrication ./query
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication responsive action-driven workspace and production queue` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 13: Legacy migration and rollout boundary

**Files:** BE create fabrication/migration_preview.go, fabrication/migration_apply.go, fabrication/migration_dbtest_test.go, controllers/fabrication_migration.go; modify fabrication/store_create.go, fabrication/store_transition.go, controllers/fabrication_steps.go, database/migrations/tenant/schema.sql. FE create src/pages/sales/components/FabricationMigrationReview.tsx.

**Interfaces:** Produce admin-only preview/apply migration command with expectedVersion and explicit reviewed piece/material mappings. New v2 job creation is enabled only after all prior tasks pass.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// DB acceptance sequence:
// legacy CUTG with posted consumption -> migrate -> ledger unchanged
// retry migration -> no new rows
// missing template evidence -> blocked, never fabricated approval
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Completed legacy jobs remain historical. For active jobs, preview existing allocations, consumed units and missing evidence; require explicit reviewed mapping. Already consumed units get historical references, never a second cutting ledger event. Missing approval/inspection remains unknown and blocks future affected work until newly evidenced. Retain legacy histories. V2 jobs reject legacy status and lifecycle checklist mutations with explanatory 409.
- [ ] Add and run these failure/concurrency cases: Test each legacy lifecycle status, cancelled/held jobs, pending old approval and migration concurrent with a legacy action. Reapply schema repeatedly. Rollback means disable new-job enablement and fix forward; no destructive down migration.
- [ ] Run verification from the appropriate repository root:

```text
go test ./fabrication ./controllers -count=1
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication legacy migration and rollout boundary` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

### Task 14: Cross-module verification and release documentation

**Files:** BE modify docs/fabrication.md; create docs/fabrication-v2-operations.md and integration scenario tests in fabrication/workflow_dbtest_test.go. FE add integration coverage alongside fabrication page tests using existing test tooling.

**Interfaces:** Consume completed Tasks 1–13. Produce verified workflow, migration operator instructions and record of actual checks; no deployment performed by this task.

- [ ] Add the targeted regression cases below to the named test files before production edits. Pseudocode sequences are behavioral contracts; use real repository fixtures and assertions, not mock-only ledger counts.

```text
// End-to-end acceptance:
// approved SO -> measurements -> both approvals -> inspect -> WIP
// cut -> remnants -> QC/rework/remake -> partial handover -> completion
// assert one inventory posting and one fulfillment per obligation
```

- [ ] Run the targeted new test and confirm it fails for the intended missing behavior; do not count fixture/auth setup failure as the expected red result.
- [ ] Implement this behavior in the listed files: Exercise the approved three-piece scenario, purchased replacement after rejection, two WIP bins, repeat remake, remnant reuse by another SO, both delivery modes and restricted installer access. Include user-visible loading/empty/error states and deterministic action availability. Document inspection/refund distinctions, partially completed jobs and stale action recovery.
- [ ] Add and run these failure/concurrency cases: Run module-drift-checker, tenancy-security-reviewer and migration-auditor as required by applicable repository instructions; run filter-invariant-checker for search changes. DB checks require an isolated test DB and cannot be reported passed when skipped. Resolve introduced findings and report unrelated pre-existing failures separately. Review both repository diffs and never include unrelated working-tree changes.
- [ ] Run verification from the appropriate repository root:

```text
BE: go build ./...; go vet ./...; go test ./...; go test -tags dbtest ./...
FE: npm run ci
```

- [ ] Review the scoped diff and record results. Commit only this task's files after its checks pass, using `feat: fabrication cross-module verification and release documentation` (or `test:`/`docs:` for verification-only changes). Never use `git add .`.

## Dependencies and execution checkpoints

Tasks 1–2 establish shared contracts. Task 3 precedes 4; 5 and 6 prepare inventory; 4–6 precede 7. Tasks 8–10 follow cutting; 11 follows handovers. Build each task's frontend slice against its backend contract; Task 12 integrates the workspace. Tasks 13–14 gate enablement. All tasks are in the agreed scope; do not call the project complete after the first vertical slice.

The feature crosses tightly coupled accounting and state boundaries. Recommended execution: native implementation in this session, one task at a time, retaining the repository-mandated specialist reviews at the relevant boundaries. Subagent-driven execution is an alternative if the user prefers independent implementer/reviewer handoffs. Do not spawn implementation agents before execution method selection.

## Self-review and coverage

- Work order/approved SO: Task 3. Piece/job state: Tasks 1, 8, 12.
- Internal/customer approvals and recorded evidence: Tasks 3–4.
- Shortage PO, receipt rejection/replacement/refund tracking: Tasks 5–6.
- WIP locations, cutting accounting and reusable remnants: Tasks 6–7.
- QC, rework, sequential remake approval: Tasks 8–9.
- Partial handover, supply/install completion, external installer access: Tasks 10–11.
- Responsive action-only UI, history and errors: Task 12 and each vertical slice.
- Legacy migration, security, concurrency, notifications and integrated verification: Tasks 2, 4, 11, 13–14.

Review finding: actual PO/receipt finance accounting and SO fulfillment semantics need careful tracing during Tasks 5 and 10; changing quantity meaning is not an incidental UI refactor. The explicit tests above are release gates. No existing dirty backend AI edits or frontend agent configuration belong in these commits.
