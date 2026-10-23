# Domain event outbox + ProposedAction store — design

Shared foundation for anomaly rules / auto-PO (events) and the AI agent / document
auto-create (propose-then-confirm). Ships dark: no real producers, consumers or executors.

## Verified facts (code, not brief)
- (a) `workflow.LogAuditFull` is called with the **pool** (not a tx) at every sampled module site
  (`controllers/*_audit.go`), after the business commit. The observer `Querier` is therefore NOT the
  caller's tx. Events cannot come from the observer; `domainevent.Emit(ctx, tx, ev)` is called inside
  each module store's own tx.
- (b) `automation_outbox` (single `processed_at`, no payload, UUID actor) is not reused.
- (c) `automation.Runner.runTenant` runs every `Job` per servable tenant under a per-tenant
  advisory lock, inside `POST /api/internal/automation/tick` (platform switch gated). Jobs are built
  by `DefaultJobs(Deps)`; the drain job is added there, its registry arrives via `Deps`.
- (d) `recordInScope`, `logSecurityEvent`, `authz.Check/Require`, `automation.WithSystemActor/IsSystemActor` exist as described.
- Deviation: `users.id` and `identities.id` are **UUID**, record ids are text/UUID. So `identity_id`,
  `user_id`, `actor_user_id` are `UUID`; `record_id` is `TEXT`. (Brief said BIGINT.)

## Packages
- `provenance` (leaf): `With(ctx, via)` / `From(ctx)`; consts `User, Automation, AIAgent, DocExtract`.
  `automation.WithSystemActor` also stamps `Automation`; `workflow.LogAuditFull` records `details.via`
  and `AuditEvent.Via` from ctx. Leaf package avoids an automation<->domainevent import cycle.
- `domainevent`: `Event`, `Emit`, `Registry` (consumer name -> `Handler`), `Drain`, `DrainNow`, `Prune`,
  `Job`-facing adapter lives in `automation/domain_event_job.go`.
- `proposedaction`: `Proposal`, `Executor` (consumer-side), `Registry`, `Store` (Propose/Get/List/
  Confirm/Cancel/Sweep), typed errors.
- `controllers/proposed_action.go`: 4 routes via `tenantChain`.

## Event outbox
Tables `domain_event`, `domain_event_cursor` (append-only DDL, DO$$ CHECK guards: depth 0..MaxDepth,
non-empty types). `Emit` refuses depth > `MaxDepth` (3). Payload is a typed struct marshalled by
the caller-provided `Payload any` (ids / changed keys only). Drain: per consumer, tx with
`SELECT ... FROM domain_event_cursor WHERE consumer=$1 FOR UPDATE SKIP LOCKED` (row seeded by
`INSERT ... ON CONFLICT DO NOTHING`), read `id > last_id ORDER BY id LIMIT batch`, handler per event
inside a SAVEPOINT-free design: handler gets the drain tx; on handler error the tx rolls back to
before that event (cursor advanced only through events already handled OK in a *separate* commit of
the prefix), failures counted in `domain_event_cursor.failures`, after `MaxAttempts` the consumer logs
via slog + metric and is skipped for the rest of the tick (never wedges others). Per-tenant per-tick cap
`MaxEventsPerTick`. Handlers MUST be idempotent (at-least-once). Retention: delete events older than
`DomainEventRetentionDays` and `id <` min(cursors).

## ProposedAction
Table `ai_proposed_action` per brief (UUID ids). `Propose` validates kind/resource against the injected
registry, runs `Validate` + `Preview`, stores. Confirm (single tx): load by id+identity (404 + security
event `proposed_action_idor` on miss) -> atomic `UPDATE ... status='confirmed' WHERE pending AND
expires_at>now() RETURNING` -> if no row: already confirmed => 200 stored `result_ref`; expired => **410
Gone**; cancelled/failed => 409 -> `authz.Check(target resource, create|update)` with current roles +
executor `InScope` (denied => security event, 403, status reverted by rollback) -> re-`Validate` ->
`Execute(ctx, tx, caller, payload)` under `provenance.AIAgent|DocExtract` ctx -> `Emit` event
`proposed_action.confirmed` -> store `result_ref`. Executor error: rollback, then a second small tx
marks `failed` with a sanitized message and returns 422. LLM never holds credentials: proposals are
data rows, only an authenticated human's confirm runs a write.
Lazy expiry: list/get flip stale pending rows to `expired`; `automation` job `proposed_action_sweep`
bulk-expires and deletes resolved rows older than retention.

## Frontend
Typed client, `useProposedActions` hook (TanStack Query if repo uses it), `ProposedActionCard`, tests.
Not mounted.
