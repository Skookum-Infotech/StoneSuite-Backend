# Domain Event Outbox — Claude rules

_Loaded automatically when Claude reads files under `domainevent/`. Moved verbatim from the root CLAUDE.md._

## Domain Event Outbox (`domainevent/` + `provenance/`)
Durable, per-tenant event log (`domain_event`) with per-consumer cursors (`domain_event_cursor`) for
anomaly rules / auto-PO. **Ships dark: no producer or consumer is wired into a business module yet.**
1. **`domainevent.Emit(ctx, tx, Event)` must receive the module store's TRANSACTION, never the pool.**
   `workflow.LogAuditFull` is called with the pool after the commit at every module site, so the audit
   observer cannot emit events atomically; emit inside the store tx so the write and event commit together
   (at-least-once; a rollback drops the event).
2. **Payloads are typed structs, ids / changed keys only.** No PII, secrets or free text.
3. **Loop guard.** `Event.Depth` is bounded by `MaxDepth` (also a DB CHECK); a consumer emitting a follow-on
   event passes `ChildDepth(ev)`. `provenance` (`With`/`From`) carries `via` (`user|automation|ai_agent|docextract`);
   `automation.WithSystemActor` stamps `automation`, and `LogAuditFull` records non-`user` `via` in `details`.
4. **Consumers** are registered by name in an injected `domainevent.Registry` (`automation.Deps.Events`, built in
   `main.go`; no globals) and MUST be idempotent. `Drain` takes the cursor row `FOR UPDATE SKIP LOCKED`
   (one drainer per consumer per tenant), runs each handler in a savepoint, advances the cursor in the same tx.
   A failing handler does not advance the cursor; after `MaxAttempts` the event is skipped with slog + metric so one
   poison event can't wedge the consumer. `DrainAll` caps events per tenant per tick (`MaxEventsPerTick`).
5. **Scheduling.** The `domain_event_drain` job (in `automation.DefaultJobs`) drains and prunes inside the existing
   tick (so it inherits the platform switch); `DrainNow` is the seam for a future worker process. Prune deletes events
   older than `DOMAIN_EVENT_RETENTION_DAYS` (30) AND at/below every cursor. Do not reuse the dormant `automation_outbox`.
