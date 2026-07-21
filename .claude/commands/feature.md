---
description: Drive a feature end-to-end — design, plan, one approval gate, then implement, test, review and report.
argument-hint: <what you want built>
---

Build this feature in StoneSuite Backend: **$ARGUMENTS**

You are the orchestrator. You hold the context; the workers do not. Run the phases
below in order. There is **exactly one approval gate** — after the plan. Everything
after it runs unattended, so the guardrails in Phase 5 are not optional.

Create a task per phase with `TaskCreate` and keep it current as you go.

---

## Phase 0 — Classify

Work out what this touches before designing anything:

- Which repo skill covers it, if any — `new-module`, `new-tenant-endpoint`,
  `add-migration`, `add-filter-field`. Invoke it; it encodes the corrected skeleton.
- Which packages the change will land in, and for document-module work, **which
  reference module to copy from** (`controllers/payment.go` for the auth skeleton;
  `estimate/` or `quote/` for money+lines+approval; `payment/` for applications
  against another doc).
- Whether it touches schema, the `query` filter engine, or `ai/` — each has its own
  invariants.

## Phase 1 — Design and plan

Invoke `superpowers:brainstorming` → spec at
`docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`.

Then `superpowers:writing-plans` → plan at
`docs/superpowers/plans/YYYY-MM-DD-<topic>.md`.

Those are the paths and the naming this repo already uses across 9 specs and 7 plans —
match them.

Break the plan into steps that are each small enough for a cold-start worker to execute
from the step text alone. Mark each step **sequential** or **independent**.

## GATE — call `ExitPlanMode`

Nothing below this line runs without approval. If the user rejects, stop; the spec and
plan stay on disk and nothing outside `docs/superpowers/` has been written.

---

## Phase 2 — Implement

Dispatch `go-implementer` (Sonnet 5), one dispatch per plan step.

**Sequential by default.** Fan out in parallel only when steps are provably
independent — the canonical case is hand-porting one cross-cutting fix across the
clone modules (`quote`, `estimate`, `salesorder`, `invoice`, `payment`, `creditmemo`,
`refund`, `vendors`), which have no shared base and so cannot conflict. Follow
`superpowers:dispatching-parallel-agents` when you do.

Every dispatch must carry, because the worker starts cold:

- the plan step **verbatim**,
- the reference module or file to copy the shape from,
- the relevant `references/` file from the Phase 0 skill,
- the explicit file list it may touch.

If a worker returns `BLOCKED`, resolve the conflict yourself — read the code, decide,
re-dispatch with the decision spelled out. Do not pass ambiguity back down.

## Phase 3 — Verify

Dispatch `go-verifier` (Sonnet 5) once, on the accumulated diff.

## Phase 4 — Review fan-out (conditional)

Get the touch set:

```
git diff --name-only $(git merge-base HEAD develop)...HEAD
```

Run **only** the matching specialists (haiku), all in **one parallel batch**:

| If the diff touches | Dispatch |
|---|---|
| `database/migrations/**/schema.sql` | `migration-auditor` |
| a document/inventory module package, `controllers/*.go`, `authz/catalog.go`, `main.go` | `module-drift-checker` |
| `controllers/`, any `*store*/`, `workflow/`, `crmstore/` | `tenancy-security-reviewer` |
| `query/**`, `workflow/filter.go`, `crmstore/relational_filter.go` | `filter-invariant-checker` |

Skip the ones whose paths are untouched — running them costs tokens and returns noise.

If a specialist reports that one of its baselines no longer matches the code, surface
that in the final report — the rule needs updating, and a stale rule quietly produces
false findings that the fix loop would then "fix".

## Phase 5 — Fix loop (capped and scoped)

Everything here runs unattended behind the single gate, so the boundary matters more
than the throughput. Triage the consolidated findings yourself:

- **CRITICAL and HIGH → auto-fix.** Dispatch `go-implementer` with the finding list,
  then re-run `go-verifier`.
- **MEDIUM → report, do not fix.** It is not worth an unattended edit.
- **Discard** anything a reviewer flagged that contradicts the approved plan — the plan
  won an explicit approval; a haiku rule list did not.

A fix dispatch is **strictly narrower** than an implementation dispatch. It may only:

- edit files **already in the diff**, at the lines the finding names;
- make the **minimal** change that clears the finding.

It may **not** create new files, touch `database/migrations/**`, change routes in
`main.go` or rows in `authz/catalog.go`, or alter any public signature. A finding that
genuinely requires one of those is **out of scope for the loop** — report it unresolved
with the reason. Widening the blast radius with no human in the loop is the failure mode
this cap exists to prevent.

**Hard cap: 2 fix rounds.** If CRITICAL or HIGH findings survive round 2, stop and
report them unresolved. Do not loop a third time.

If a fix round makes verification *worse* than the round before it, revert that round's
edits and report the finding unresolved rather than continuing.

## Phase 6 — Report

Print, in this order:

1. **What changed** — files, grouped by package, one line each.
2. **Verification** — the `BUILD/VET/TEST/DBTEST` status block, plus tests added.
3. **Findings** — severity-ranked, each marked `fixed` / `unresolved` / `reported only`.
4. **Artifacts** — paths to the spec and the plan.
5. **Suggested commit message** — Conventional Commits, specific.

Then **stop**. Do not `git add`, `commit`, `push`, or open a PR. The working tree stays
dirty for the user to inspect; committing is their call.

Report honestly: if tests failed, show the output. If a step was skipped, say so.
