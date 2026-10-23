# Plan — domain event outbox + ProposedAction

Steps (S = sequential, I = independent after step 1-2).

1. S: `provenance` pkg + audit `via` (workflow/audit.go Via on AuditEvent + details.via; automation.WithSystemActor stamps it) + tests.
2. S: schema.sql append: `domain_event`, `domain_event_cursor`, `ai_proposed_action`, indexes, guarded CHECKs. config.AppConfig knobs (retention days, proposal TTL minutes).
3. I: `domainevent` pkg (event.go, emit.go, registry.go, drain.go, prune.go, metrics) + dbtest tests (rollback drops event, ordering, single drainer, failure doesn't advance, depth guard, prune).
4. S (needs 3): `automation/domain_event_job.go` + `Deps.Events` + DefaultJobs wiring; `DrainNow` seam.
5. I: `proposedaction` pkg (types, registry, store propose/get/list, confirm, cancel, sweep, test executor) + dbtest tests.
6. S (needs 4,5): `automation` sweep job; `controllers/proposed_action.go` (+ store adapter), routes in main.go, handler tests.
7. S: go-verifier; migration-auditor, tenancy-security-reviewer in parallel; capped fix loop.
8. S: frontend client/hook/card/tests on `feat/proposed-action-client` (Sonnet).
9. S: CLAUDE.md sections; final report.
