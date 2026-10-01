# Fabrication native execution ledger

Approved spec and 14-task plan: 2026-10-01-fabrication-workspace*. Native method approved.
Backend and frontend isolated on codex/fabrication-workspace under ~/.codex/worktrees/fabrication-workspace/.
Baseline: go test ./fabrication ./inventory ./approvalchain PASS (macOS linker version warnings).
Preflight: Tasks 1/2 supply typed commands consumed by all actions; tasks 3/4 supply immutable approvals consumed by 6/7; 7 supplies physical outputs consumed by 8; 9/10 share delivery obligations; 11 is restricted access to 10; 12 aggregates all states; 13 explicitly preserves legacy postings. No conflicting interfaces.
Ruling: frontend native worktree creation tool is tied to backend caller; use git worktree for sibling frontend. Preserve original checkouts untouched.
Ruling: plan pseudocode is acceptance intent, not compilable code. Define typed real test fixtures while implementing; no mock-only ledger tests.
Task 1 in progress. Remaining tasks 2–14 not started.
Task 1: schema/types + completion foundation implemented. RED: missing symbols and missing workflow_version observed. GREEN: pure tests, all fabrication DB tests (local port 55439), repeated canonical schema, frontend tsc. Migration auditor: no findings. Legacy delivery_mode and production_stage remain NULL rather than fabricated. Summaries are not exposed until integrated with obligations. Local DB initialized with PostgreSQL 17; schema must execute in one transaction.
Task 1 full backend go test ./... PASS; frontend tsc PASS. Migration/module reviewers no findings.
Task 2 core transaction and operation policy implemented, API adapters remain pending typed commands. DB tests PASS: replay, changed payload conflict, rollback, two connections competing for version, scope denial. No v2 write route is exposed yet.
