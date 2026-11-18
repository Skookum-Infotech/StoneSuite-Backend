# QA Report — Progress Bar (StoneSuite WebUI / Backend / Notify)

Date: 2026-11-18 · Environment: **local Docker stack only** (backend :8080, Postgres, Notify :8090, Mailpit, Vite). **The shared DEV environment was NOT tested.**
All planned verification is complete. Remaining open items are decisions and deployment steps (§9), not pending tests.

## 0. Scope note

The app has no task-based progress bar. Progress bars are **derived from records**; scenarios were mapped as: creation/update = receipt create/edit, completion = PO at 100%, reopening = **voiding** a receipt, deletion = delete receipt/PO, concurrency = parallel posts/voids. Three bars exist: PO receiving % (list + per-line), the workflow stage bar, and the Plan-tab usage meters.

## 1. Result summary

| Area | Result |
|---|---|
| Backend build + vet (final code) | PASS (`go build ./...`, `go vet ./controllers/`) |
| `controllers` unit package (final code) | PASS |
| Backend full `go test -race ./...` (**final code**) | build + vet PASS · **88 pkgs PASS** · the same 2 FAIL as before (Windows CRLF, not defects) |
| Backend DB-backed: purchaseorder 68, itemreceipt 61, usage 17 | **146 PASS** (before wider fix; those packages untouched) |
| Backend DB-backed: `controllers` (**final code**) | **646 PASS · 1 FAIL · 1 skip.** The failure, `TestAIOpsAskStream_CtxDoneEmitsErrorEvent` (expects 200, gets 500), is **pre-existing**: it fails identically on the untouched original commit (needs the AI backend, Ollama, which isn't running locally); no `ai_*` file was modified (F11) |
| Notify build / vet / tests (incl. new tests) | PASS (10 pkgs) |
| Frontend lint | PASS (0 errors, 104 pre-existing warnings) |
| Frontend type-check (`tsc -b`) | PASS |
| Frontend build | PASS |
| Frontend unit tests, full run | 7,403 passed / 3 failed — **all 3 were 5 s timeouts under load** in unrelated files; **re-run in isolation: 108/108 PASS** incl. those 3 files and the 3 new tests |
| API suite (45 checks) | original build 44/45 → **final fixed build 45/45** |
| Authorization suite (25 checks, 5 roles) | **25/25** (original and final build) |
| DB consistency (6 SQL checks) | PASS, 3 anomalies explained (§4) |
| Malformed-id probe (311 id-bearing tenant routes) | **145 × HTTP 500 → 0** |
| Browser UI | PASS with observations (§5) |

## 2. API suite (progress logic) — highlights

PASS: 0/33/67%, 999/1000 → **99% (never 100)**, 1/100000 → **1%**, mixed lines, fractional qty; reopen cycle RCVD → PART 50% → **SENT 0%** → re-receive → RCVD; void needs a reason; double-void rejected; over-receipt ≤5% accepted, >5% needs approver + reason; negative/foreign/unknown lines, unsent PO, empty receipt, bad date rejected; posted receipt immutable; PO with receipts not deletable; client cannot set `qtyReceived`/status; 8 parallel posts → stored qty = accepted × 2, never above ceiling; 4 parallel voids → exactly one wins, qty 0.
My own first run had 5 failures: 3 were bugs in my script (reserved `$pid`), 1 a false PASS (stale variable); corrected and re-run. Only S8.6 (malformed id → 500) was a real defect.

## 3. Authorization (25/25)

Viewer: reads OK, **403** on receive/void/post/transition/edit/delete. No-PO user: 403 on list/detail/receipts. Usage meters need `subscription:read`. Own-scope user: other user's PO absent from list and **404** on direct open (IDOR guard). Create-only user: can save pending receipt, cannot post/void. Poster (no approve): can post and void, cannot over-receive even with a reason. Forged JWT → 401.

## 4. Database consistency

C1 stored `qty_received` vs posted receipts: **0 mismatches** (after concurrency + void tests). No negative quantities; all voids have reason + timestamp. Three POs read "Received" while not fully received: PORD-000022/000038 (my manual-transition test) and **PORD-000008** (manual PART→RCVD at 05:39, not from a receipt, not from my scripts) → finding F5.

## 5. UI

PASS: list bars match API at 0/25/75/100% (0/3/8/10 segments; orange/orange/green); unsent POs show no bar; per-line bars (100% green, 50% orange); stage bar "Receiving: N records" = receipt count; ARIA value = display. Mobile 375 px: no page overflow, detail bars visible. Missing PO → "Purchase order not found."; malformed id → generic "Failed to load" (was the backend 500).
Observations: **Received column is hidden on phones by design** (`max-sm:hidden`). A manually "Received" PO beside 50–100% line bars is contradictory (F5).
Not tested: tablet width, keyboard-only use, screen readers (only ARIA attributes), non-Chromium browsers, a second tenant, load/performance.

## 6. Defects

| # | Sev | Finding | Status |
|---|---|---|---|
| F1 | Med | **Malformed id in a URL → HTTP 500.** Baseline probe of all 311 id-bearing tenant routes: **145 returned 500** (PO, receipts, vendors, vendor bills, quotes, sales orders, estimates, expenses, requisitions, fabrication jobs + sub-resources, feedback, accounting periods, records, workflows, users, invites, SSO, approvers, customer transactions…) | **Fixed — 0 of 311 now 500** (145 → 17 → 2 → 0 over three passes), verified live on a build of the final code |
| F2 | Low-Med | Notify SMTP rejects `EMAIL_FROM="Name <addr>"` (README's recommended form) — raw string sent as envelope sender | **Fixed**, regression test reproduces `501 invalid FROM parameter` on old code |
| F3 | Low | Usage bar turns **green at 100% of a limit** | **Fixed** (red + "Limit reached" text); frontend tests pass |
| F4 | **Med-High** | Vendor email fails at SENT → backend says "not sent" and leaves PO unsent, but **Notify keeps retrying and delivers the email later** (Mailpit has "PORD-000004 sent" while that PO is Approved) | **Open — needs a design decision** |
| F5 | Med | Manual status change to PART/RCVD ignores quantities → "Received" with a 0% bar | **Open — product decision** |
| F6 | Info | Concurrent voids: losers get a mix of 404/409 (safe, inconsistent) | Open |
| F7 | Info | Plan limits don't exist in the backend (always null) → quota bars never render in real use; checked only with mocked limits | Feature gap |
| F8 | Info | 11 sub-resource GETs (`…/{uuid}/audit`, `/refunds`, `/payments`, `/credit-memos` of invoices, payments, refunds, credit memos, cash transfers, vendor credits/payments) answer **200 with an empty list** for a malformed id instead of 400/404 | Open, harmless |
| F9 | Env | 2 backend tests fail on Windows (Git CRLF): `TestGeneratedAPIDocsAreCurrent`, 7 `docextract` tests; both passed on an LF copy earlier. Linux CI unaffected | Environment |
| F11 | Info | DB test `TestAIOpsAskStream_CtxDoneEmitsErrorEvent` fails on the original commit too (500 instead of 200 without Ollama) — not caused by these changes | Pre-existing |

How F1 was fixed: one shared helper `failLoad` (maps client-caused Postgres errors such as an invalid uuid to 400, everything else stays 500) applied at **54 call sites**, plus dedicated lookups for PO and item receipt (`loadPOOrFail`, `loadIROrFail`). Messages for missing records (404) are unchanged.

## 7. Changes (all uncommitted, in the three worktrees)

**Backend** — 24 files modified, 3 new (`+99 / −70`): `controllers/fail_load.go` **(new)**, `fail_load_test.go` **(new)**, `record_load_errors_test.go` **(new)**; modified: `accountingperiod, approvalchain, attachments, crm, crm_admin, customer_transactions, estimate, expense, fabrication, fabrication_errors, fabrication_source_so, feedback, itemreceipt, itemreceipt_errors, purchaseorder, purchaseorder_errors, quote, requisition, salesorder, sso, user, vendor, vendorbill, workflow` (.go).
**Notify** — `channels/email.go`; new `channels/smtp_envelope_test.go`.
**WebUI** — `SegmentedProgressBar.tsx` (+test), `UsageMeter.tsx`, `UsagePanel.test.tsx`.

Regression tests (mutation-checked where noted): `TestFailLoad` (5 cases), `TestLoadPOOrFail`, `TestLoadIROrFail` (old behaviour restored → expected 400, got 500 ✔), Notify `TestSMTPEnvelopeFrom` + `TestSendViaSMTP_DisplayNameSender…` (old code → `501 invalid FROM parameter` ✔), frontend: 2 in `SegmentedProgressBar.test.tsx`, 1 in `UsagePanel.test.tsx`. The 311-route probe is a script, not yet a committed test (§9).

## 8. Local state left behind

**All 78 test purchase orders, 98 lines and 95 receipts were deleted at the user's request** (receipts voided first so stock on the test items is back to 0; also removed 72 email-send records). Remaining test data: vendor, items, warehouse; 4 QA users (`qa.*@example.test`, password in scripts) and 5 `qa_*` roles; seeded usage rows; Notify running locally (Resend disabled via an override file, mail → Mailpit). The temporary backend on :8092 is stopped. This report is untracked.

## 9. DEV readiness

**Conditional GO for the progress-bar logic/display and the malformed-id fix. Not a DEV sign-off.**
Before promoting:
1. ✅ Final full backend `-race` run and `controllers` DB tests on the final code are done: no regression (the only DB-test failure is pre-existing, F11).
2. Decide F4 and F5.
3. The fixes are in open pull requests and are **not yet deployed**.
4. The DEV environment itself was never exercised.
5. Consider promoting the 311-route probe into a CI test so new routes can't reintroduce 500s.
