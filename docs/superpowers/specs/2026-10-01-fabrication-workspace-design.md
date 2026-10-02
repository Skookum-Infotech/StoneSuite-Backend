# Fabrication workspace redesign

Status: proposed design for user review; implementation has not started.

## Purpose and confirmed decisions

Replace status-driven fabrication forms with an action-driven workspace that follows physical work. Start from the current StoneSuite-WebUI and StoneSuite-Backend code. Support desktop, tablet, and phone from the first release; desktop is the initial primary device.

- A work order is a fabrication job created from an approved sales order.
- Pieces progress independently; the job summarizes their progress and blockers.
- Measure on-site, validate the template against the sales order, then secure materials.
- Measurement adjustments require internal approval. Changes to price, material, or scope also require customer approval.
- Support customer digital approval and staff-recorded approval with evidence.
- Purchase shortages; inspect received slabs. Reject an entire failed slab and pursue supplier replacement or refund. Never partially accept a defective slab.
- Track storage and WIP locations. A WIP bin may identify a saw machine or a general production area.
- Each cutting run serves one job. Remnants return to inventory before allocation to another job or sales order.
- Track cutting, edging, QC, partial handovers, and optional installation per piece.
- Routine rework repairs an existing piece and returns it to QC. Replacement pieces require an approved remake request.
- Remake approval supports one approver or sequential approval, configurable per business.
- Supply-only jobs finish at acknowledged handover. Installed jobs finish after installation and customer sign-off.
- External installers are staff-managed by default, with optional restricted access.

## Existing implementation and necessary changes

The backend fabrication module already owns jobs, pieces, steps, material allocations, holds, approvals, and cancellations. Its transition map allocates material before templating and progresses the entire job linearly. Entering Cutting consumes allocated slabs. The frontend mirrors transitions through FabricationStatusControl and offers a separate editable checklist.

Inventory already has physical units, bin hierarchies, transfers, cut operations, remnants, lineage, and consumption accounting. Reuse these records and services. Do not introduce a second inventory ledger. Existing inventory CutUnit and fabrication consumption are separate entry points; consolidate their transactional primitives before adding cutting runs to prevent double consumption.

The approvalchain module supports fabrication gate approval. Verify and extend its sequencing and record-specific configuration for revision and remake approvals; do not assume that current fabrication gates already implement sequential approval. Customer approval and external installer access need explicit authorization boundaries beyond employee permissions.

## Workspace and interaction design

### Production queue

Default to a searchable table with job number, customer, due date, owner, piece-stage counts, active blockers, and next action. Offer stage-board view with summary cards; dragging cards must not change status. Filters include due dates, assigned team, stage, WIP location, approval pending, shortages, and remakes. Filters and pagination run server-side through the existing query engine.

Use text and icons alongside color. Display counts such as “3 cutting · 2 QC · 1 handed over”; do not show a misleading single percentage as the only progress indicator. Keep holds and blockers distinct from production stage. Bulk actions operate only on explicitly selected, eligible pieces and disclose exclusions.

### Job workspace

Header: job number, linked sales order, customer, delivery mode, due date, owner, and hold state. Below it: stage counts and a clearly prioritized next-action panel with blockers and links to resolve them.

Sections: Overview; Template & approvals; Pieces; Materials & cutting; Quality & remakes; Handovers & installation; Files & activity. Show contextual counts and surface pending work in Overview so users do not need to search tabs. Editing contact, dates, and notes stays separate from lifecycle actions.

Piece rows show name, material, dimensions, revision, current stage, responsible person, and next action. A detail drawer presents source slab, cutting run, QC results, rework/remake relationships, and handover history. Preserve table context when the drawer closes.

### Template review

Compare measured requirements with the sales-order revision: dimensions, material, quantity, scope, and price effects. Highlight changes and show supporting drawings/photos. Submit an immutable revision for internal approval; require customer approval when price, material, or scope changes. Capture customer evidence and identity for staff-recorded approval, or a digital approval event against that exact revision.

Changing submitted requirements creates a new revision and invalidates affected pending approvals. Approved evidence is retained. Proposed default: purchasing for changed requirements and cutting remain blocked until all required approvals finish. Existing unrelated work may proceed only if explicitly outside the revision's affected pieces and materials.

### Materials and cutting

Show required material, allocated stock, shortages, linked purchase orders/receipts, inspection status, and actual bin location. A sufficient total area alone does not prove a slab fits: material/finish/thickness and confirmed layout suitability also gate cutting. Automated nesting is outside this design.

Actions include Allocate slab/remnant, Create shortage purchase order, Inspect receipt, Transfer to storage, Transfer to WIP, Start cutting, and Complete cutting. Purchasing actions reuse purchasing permissions and records; fabrication permissions do not grant purchasing authority.

Complete cutting captures output pieces, retained remnants with measured dimensions and destination bins, and waste/variance information. Provide an explicit “No usable remnants” choice. Show the material-accounting summary before submission. Remnants receive unique inventory identities and retain parent/root slab lineage.

### Quality, rework, remakes, and delivery

QC records pass or fail for each piece, inspector, findings, and evidence. Failed pieces cannot be handed over. Repairable failures create rework tasks, keeping prior QC results, and return to QC after repair. Remakes record original job/SO, affected piece, reason, supporting evidence, requested replacement scope, and approval history. Rejection leaves the original failure unresolved; approval creates replacement work linked to the original piece rather than resetting its history.

Partial handovers list specific QC-passed pieces, installer, date, and acknowledgment. Installation jobs additionally track completion evidence and customer sign-off for those pieces. A replacement must satisfy the original delivery obligation; it must not increment sales-order fulfillment a second time.

### Responsive and accessible behavior

Desktop: dense queue, comparison panels, contextual drawers. Tablet: compact tables and collapsible panels. Phone: piece/job cards, one-column forms, and prominent contextual actions. Keep measurements and action labels readable without horizontal scrolling for primary tasks. Support keyboard navigation, visible focus, labeled fields, error summaries, and focus restoration after dialogs. Use confirmation only for consequential actions such as physical consumption, rejection, and cancellation. Preserve draft input after errors. Offline operation is excluded from the initial release; never indicate success before the server confirms it.

## Domain model

Keep the existing job and piece identities. Add explicit linked records rather than overloading checklist payloads:

| Record | Responsibility |
|---|---|
| Job extensions | Delivery mode, workflow version, aggregate progress, hold context |
| Template revision and measured pieces | Immutable approved requirements and sales-order revision link |
| Approval request and decisions | Revision/remake subject, configured sequence, actor, evidence, outcome |
| Receipt inspection and resolution | Per-slab acceptance/rejection, quarantine location, supplier replacement/refund references |
| Cutting run, inputs, outputs | One job, WIP location, locked input units, output pieces, consumption and recovery links |
| Piece operation and QC inspection | Independent production state, assignments, results, rework cycles |
| Remake request and replacement links | Approval and original-to-replacement lineage |
| Handover and lines | Installer, selected pieces, acknowledgment, delivery mode |
| Installation/sign-off records | Completion and customer acceptance of delivered pieces |
| Installer contact/access grant | External contact, optional account, explicitly assigned work |
| Action events | Immutable operational history and request identity |

Reuse existing attachment, inventory, purchasing, employee, and customer identities where applicable. Final SQL names and contract types belong in the implementation plan after schema tracing.

## Action contract and state rules

Expose contextual availableActions on job/piece/run reads, with action code, label, enabled state, structured blockers, and required input type. Explicit typed command endpoints perform the operations; a generic “set status” payload must not bypass their requirements. Frontend types describe action forms, while backend policy remains authoritative. Hide unauthorized actions; explain operational blockers for authorized users.

Commands carry an expected record version and idempotency key. Revalidate permissions, ownership, state, revision approvals, and stock inside the transaction. Lock affected jobs/pieces/units in deterministic order. A stale command returns 409 and refresh guidance; retries with the same key return the original result. Publish notifications only after successful commit through the established mechanism.

Proposed piece path: Planned → Ready for cutting → Cutting → Edging → QC → QC passed → Handed over → Installed → Signed off. Supply-only pieces finish at acknowledged handover. Rework routes from QC back to the necessary operation and then QC. Holds and remake-pending conditions are explicit blockers, not destructive resets. Start/complete actions distinguish work underway from completed work.

Job summary is derived from current required pieces and preparation gates. It exposes stage counts, blocked counts, and next actionable work. Completion requires every delivery obligation to reach its job-specific endpoint, with no unresolved mandatory replacement. A job with no pieces cannot complete. Original replaced pieces remain in history but are excluded from the active completion denominator.

## Inventory integrity

Allocation reserves accepted available units; it does not consume them. Transfer changes physical location without deducting inventory. Start cutting locks input units against competing allocations and cutting actions. Proposed accounting point: Complete cutting atomically consumes the parent and creates retained remnants using the shared ledger primitive. A started run cannot simply release stock as unused; abandonment requires a recorded disposition.

For each input unit, retained area cannot exceed input area, and declared production plus recoverable material and waste must reconcile within documented measurement tolerance. Keep physical accounting separate from order fulfillment; do not interpret all net consumed area as finished product. Remnant availability requires a valid destination bin and successful recovery posting. Rejected slabs remain unavailable even if located in an ordinary bin.

Replacement receipt creates/links the actual replacement slab and does not silently change the rejected slab to accepted. Refund resolution links to purchasing/credit records; recording a rejection does not itself claim a refund has been received.

## Security and audit

Every employee route uses TenantResolver, resource/action permission checks, and record scope enforcement; out-of-scope records return 404. Validate linked sales orders, inventory, POs, attachments, and handovers within the same tenant and caller's permitted scope. External installers access only assigned handovers and necessary installation details, without customer commercial data, other jobs, or inventory browsing.

Digital customer approvals use authenticated customer access or scoped, expiring, revocable approval links bound to an exact revision. Resolve tenant context server-side from the verified grant; never trust a caller-supplied tenant ID. Consuming an approval grant is audited and replay-safe. Staff-recorded decisions identify both the customer approver and recording employee.

Record actor, timestamp, subject, prior/resulting state, revision, evidence, and reason for consequential actions. Approval configuration changes must not retroactively rewrite decisions already recorded. Use existing security logging and notify registration patterns.

## Delivery sequence and compatibility

1. Trace existing schema, fulfillment semantics, customer portal access, purchasing receipt/returns, and approval sequencing; finalize explicit contracts and migration mappings.
2. Add domain records, action policy, piece progress, audit, and additive migrations. Introduce a job workflow version so existing work remains interpretable.
3. Build template revision and approval flow, receipt inspection, material allocation, and WIP transfers using existing modules.
4. Implement transactional cutting runs, remnant recovery, piece QC, rework, and remake approval.
5. Build the responsive queue and job workspace against those contracts, replacing status dropdowns and lifecycle-changing checklist inputs.
6. Add partial handovers, staff-managed installers, customer sign-off, and optional restricted installer/digital approval access. These are part of the agreed scope, not silently deferred features.
7. Verify legacy migration and exercise complete scenarios before enabling the new workflow for production jobs.

All database changes are additive and idempotent in the canonical tenant schema. Existing columns are added via ALTER TABLE ADD COLUMN IF NOT EXISTS, with guarded constraints. Never infer missing historical measurements, inspections, or customer approvals. Completed legacy jobs remain historical. Active jobs require an explicit migration mapping; already consumed slabs must never be consumed again. Disable legacy status-changing endpoints for migrated jobs. Legacy clients receive an explanatory conflict rather than bypassing new gates.

## Verification and acceptance

- Backend policy tests cover each action's prerequisites, permission denial, stale versions, approval sequencing, and independent piece progression.
- DB-backed tests cover concurrent allocation/cutting, retry safety, rejected stock exclusion, ledger conservation, remnant reuse on another job, and no duplicate order fulfillment after remakes.
- Migration tests cover fresh tenants, repeated application, and active legacy jobs with consumed stock.
- Frontend tests cover blocker explanations, required evidence, stale-action recovery, partial selections, keyboard behavior, and mobile layouts.
- End-to-end scenarios cover stocked and purchased material, revised customer approval, rejected receipt with replacement/refund tracking, two WIP locations, rework, approved/rejected remake, partial handover, both delivery modes, and external installer restrictions.
- Run build/vet/tests and frontend lint/test/build. Run applicable module drift, tenancy, migration, and filter reviews when those code areas change.

Acceptance example: an approved sales order produces a job with three pieces; two pass QC and are handed over while the third awaits approved remake work. A cutting remnant is stored and later allocated to another job. The first job reports its outstanding replacement accurately and completes only when its delivery obligations finish. Inventory is consumed/recovered exactly once, and every action is attributable.

## Proposed defaults requiring design review

These are design recommendations, not additional business decisions already confirmed: lock slabs at cutting start and post consumption at cutting completion; require all affected revision approvals before purchasing changed requirements; use expiring scoped links as an alternative to customer portal approval; use aggregate counts rather than a manually selectable job stage. Exact approval thresholds, sign-off evidence fields, and measurement tolerances should be configurable or explicitly specified in the implementation plan.
