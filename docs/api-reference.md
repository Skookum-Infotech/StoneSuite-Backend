# StoneSuite Backend — API Reference

> **Generated file — do not edit by hand.**
> Regenerate with `go run ./cmd/gen-apidocs`.
> Narrative and architecture live in [architecture-overview.md](architecture-overview.md).

462 endpoints across 7 surfaces, read from `main.go`.

## Auth posture at a glance

| Requires | Endpoints |
|---|---:|
| staff token + tenant | 396 |
| none (rate-limited) | 20 |
| none | 17 |
| portal token + tenant | 17 |
| staff token | 7 |
| portal token | 3 |
| customer token | 2 |

## Unauthenticated endpoints

Every endpoint reachable with no credential. Worth re-reading whenever this list grows.

| Method | Path | Notes |
|---|---|---|
| `ANY` | `/api` | no rate limit |
| `POST` | `/api/auth/forgot-password` | rate-limited per IP |
| `POST` | `/api/auth/identify` | rate-limited per IP |
| `POST` | `/api/auth/logout` | no rate limit |
| `POST` | `/api/auth/refresh` | rate-limited per IP |
| `POST` | `/api/auth/reset-password` | rate-limited per IP |
| `GET` | `/api/auth/reset-password/{token}` | no rate limit |
| `POST` | `/api/auth/saml/discover` | rate-limited per IP |
| `POST` | `/api/auth/saml/exchange` | rate-limited per IP |
| `POST` | `/api/auth/saml/{provider}/acs` | rate-limited per IP |
| `GET` | `/api/auth/saml/{provider}/initiate` | rate-limited per IP |
| `GET` | `/api/auth/saml/{provider}/logout-response` | no rate limit |
| `GET` | `/api/auth/saml/{provider}/metadata` | no rate limit |
| `GET` | `/api/auth/saml/{provider}/sp-info` | no rate limit |
| `ANY` | `/api/auth/tenant-login` | rate-limited per IP |
| `POST` | `/api/customer/auth/accept-invite` | rate-limited per IP |
| `POST` | `/api/customer/auth/login` | rate-limited per IP |
| `GET` | `/api/healthz` | no rate limit |
| `GET` | `/api/metrics` | no rate limit |
| `ANY` | `/api/onboarding/apply` | no rate limit |
| `ANY` | `/api/onboarding/apply/` | no rate limit |
| `ANY` | `/api/onboarding/form-schema` | no rate limit |
| `ANY` | `/api/onboarding/set-password` | no rate limit |
| `ANY` | `/api/onboarding/set-password/` | no rate limit |
| `POST` | `/api/onboarding/user-invite/accept` | no rate limit |
| `GET` | `/api/onboarding/user-invite/{token}` | no rate limit |
| `POST` | `/api/platform/activate` | rate-limited per IP |
| `GET` | `/api/platform/setup/status` | no rate limit |
| `POST` | `/api/portal/auth/accept-invite` | rate-limited per IP |
| `POST` | `/api/portal/auth/forgot-password` | rate-limited per IP |
| `GET` | `/api/portal/auth/invite/{token}` | rate-limited per IP |
| `POST` | `/api/portal/auth/login` | rate-limited per IP |
| `POST` | `/api/portal/auth/logout` | rate-limited per IP |
| `POST` | `/api/portal/auth/refresh` | rate-limited per IP |
| `POST` | `/api/portal/auth/reset-password` | rate-limited per IP |
| `GET` | `/api/portal/auth/reset-password/{token}` | rate-limited per IP |
| `GET` | `/api/readyz` | no rate limit |

## `system` — 4 endpoints

Liveness, readiness and metrics. No credential.

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api` | none | `json.NewEncoder` |
| `GET` | `/api/healthz` | none | `health.Healthz` |
| `GET` | `/api/metrics` | none | `` |
| `GET` | `/api/readyz` | none | `health.Readyz` |

## `auth` — 16 endpoints

Staff sign-in, session rotation, password reset and SAML SSO.

### change-password

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/change-password` | staff token | `tenantOps.ChangePassword` |

### forgot-password

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/forgot-password` | none (rate-limited) | `tenantOps.ForgotPassword` |

### identify

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/identify` | none (rate-limited) | `tenantOps.Identify` |

### logout

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/logout` | none | `tenantOps.Logout` |

### refresh

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/refresh` | none (rate-limited) | `tenantOps.RefreshSession` |

### reset-password

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/reset-password` | none (rate-limited) | `tenantOps.ResetPassword` |
| `GET` | `/api/auth/reset-password/{token}` | none | `tenantOps.ValidateResetToken` |

### saml

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/auth/saml/discover` | none (rate-limited) | `samlAuth.Discover` |
| `POST` | `/api/auth/saml/exchange` | none (rate-limited) | `samlAuth.Exchange` |
| `POST` | `/api/auth/saml/{provider}/acs` | none (rate-limited) | `samlAuth.ACS` |
| `GET` | `/api/auth/saml/{provider}/initiate` | none (rate-limited) | `samlAuth.Initiate` |
| `POST` | `/api/auth/saml/{provider}/logout` | staff token + tenant | `samlAuth.Logout` |
| `GET` | `/api/auth/saml/{provider}/logout-response` | none | `samlAuth.LogoutResponse` |
| `GET` | `/api/auth/saml/{provider}/metadata` | none | `samlAuth.Metadata` |
| `GET` | `/api/auth/saml/{provider}/sp-info` | none | `samlAuth.SPInfo` |

### tenant-login

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/auth/tenant-login` | none (rate-limited) | `tenantOps.TenantLogin` |

## `onboarding` — 7 endpoints

Public tenant onboarding and workspace-user invitations.

### apply

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/onboarding/apply` | none | `tenantOps.SubmitApply` |
| `ANY` | `/api/onboarding/apply/` | none | `tenantOps.GetApply` |

### form-schema

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/onboarding/form-schema` | none | `tenantOps.FormSchema` |

### set-password

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/onboarding/set-password` | none | `tenantOps.SetPassword` |
| `ANY` | `/api/onboarding/set-password/` | none | `tenantOps.GetSetPassword` |

### user-invite

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/onboarding/user-invite/accept` | none | `userOps.AcceptUserInvite` |
| `GET` | `/api/onboarding/user-invite/{token}` | none | `userOps.GetUserInvite` |

## `portal` — 28 endpoints

Customer portal — scoped read access, invitations, workspace switching.

### auth

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/portal/auth/accept-invite` | none (rate-limited) | `portalAuthOps.AcceptInvite` |
| `POST` | `/api/portal/auth/change-password` | portal token | `portalAuthOps.ChangePassword` |
| `POST` | `/api/portal/auth/forgot-password` | none (rate-limited) | `portalAuthOps.ForgotPassword` |
| `GET` | `/api/portal/auth/invite/{token}` | none (rate-limited) | `portalAuthOps.GetInvite` |
| `POST` | `/api/portal/auth/login` | none (rate-limited) | `portalAuthOps.Login` |
| `POST` | `/api/portal/auth/logout` | none (rate-limited) | `portalAuthOps.Logout` |
| `POST` | `/api/portal/auth/refresh` | none (rate-limited) | `portalAuthOps.Refresh` |
| `POST` | `/api/portal/auth/reset-password` | none (rate-limited) | `portalAuthOps.ResetPassword` |
| `GET` | `/api/portal/auth/reset-password/{token}` | none (rate-limited) | `portalAuthOps.ValidateResetToken` |
| `POST` | `/api/portal/auth/switch-workspace` | portal token | `portalAuthOps.SwitchWorkspace` |

### invoices

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/portal/invoices` | portal token + tenant | `portalDocOps.ListInvoices` |
| `POST` | `/api/portal/invoices/search` | portal token + tenant | `portalDocOps.SearchInvoices` |
| `GET` | `/api/portal/invoices/{uuid}` | portal token + tenant | `portalDocOps.GetInvoice` |
| `ANY` | `/api/portal/invoices/{uuid}/messages` | portal token + tenant | `portalDocOps.MessagesFor` |

### me

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/portal/me` | portal token + tenant | `portalDocOps.Me` |

### payments

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/portal/payments` | portal token + tenant | `portalDocOps.ListPayments` |
| `POST` | `/api/portal/payments/search` | portal token + tenant | `portalDocOps.SearchPayments` |
| `GET` | `/api/portal/payments/{uuid}` | portal token + tenant | `portalDocOps.GetPayment` |
| `ANY` | `/api/portal/payments/{uuid}/messages` | portal token + tenant | `portalDocOps.MessagesFor` |

### refunds

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/portal/refunds` | portal token + tenant | `portalDocOps.ListRefunds` |
| `POST` | `/api/portal/refunds/search` | portal token + tenant | `portalDocOps.SearchRefunds` |
| `GET` | `/api/portal/refunds/{uuid}` | portal token + tenant | `portalDocOps.GetRefund` |
| `ANY` | `/api/portal/refunds/{uuid}/messages` | portal token + tenant | `portalDocOps.MessagesFor` |

### sales-orders

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/portal/sales-orders` | portal token + tenant | `portalDocOps.ListSalesOrders` |
| `POST` | `/api/portal/sales-orders/search` | portal token + tenant | `portalDocOps.SearchSalesOrders` |
| `GET` | `/api/portal/sales-orders/{uuid}` | portal token + tenant | `portalDocOps.GetSalesOrder` |
| `ANY` | `/api/portal/sales-orders/{uuid}/messages` | portal token + tenant | `portalDocOps.MessagesFor` |

### workspaces

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/portal/workspaces` | portal token | `portalAuthOps.Workspaces` |

## `customer` — 4 endpoints

Second customer surface from PR #140. See the overlap note in architecture-overview.md.

### auth

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/customer/auth/accept-invite` | none (rate-limited) | `customerAuthOps.AcceptInvite` |
| `POST` | `/api/customer/auth/login` | none (rate-limited) | `customerAuthOps.Login` |

### notes

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/customer/notes` | customer token | `customerPortal.ListMyNotes` |
| `POST` | `/api/customer/notes` | customer token | `customerPortal.CreateNote` |

## `platform` — 8 endpoints

Platform-admin operations across tenants.

### activate

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/platform/activate` | none (rate-limited) | `tenantOps.Activate` |

### ai

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/platform/ai/reindex-help` | staff token | `aiOps.ReindexHelp` |

### invites

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/platform/invites` | staff token | `tenantOps.InviteCustomer` |

### setup

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/platform/setup/status` | none | `tenantOps.SetupStatus` |

### tenants

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/platform/tenants` | staff token | `tenantOps.CreateTenant` |
| `ANY` | `/api/platform/tenants/` | staff token | `tenantOps.TenantLifecycle` |
| `POST` | `/api/platform/tenants/{id}/repair-bucket` | staff token | `tenantOps.RepairBucket` |
| `POST` | `/api/platform/tenants/{id}/repair-cors` | staff token | `tenantOps.RepairBucketCORS` |

## `tenant` — 395 endpoints

The staff application. Every route requires a JWT and resolves a tenant database.

### admin

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/admin/design-version` | staff token + tenant | `crmAdminOps.GetDesignVersion` |
| `POST` | `/api/tenant/admin/design-version` | staff token + tenant | `crmAdminOps.SetDesignVersion` |

### ai

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/tenant/ai/ask` | staff token + tenant | `aiOps.Ask` |
| `POST` | `/api/tenant/ai/reindex` | staff token + tenant | `aiOps.Reindex` |

### audit

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/audit` | staff token + tenant | `auditOps.ListAudit` |

### auth

| Method | Path | Requires | Handler |
|---|---|---|---|
| `POST` | `/api/tenant/auth/switch-role` | staff token + tenant | `rbac.SwitchRole` |

### config

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/config/approvers` | staff token + tenant | `crmAdminOps.ListApprovers` |
| `POST` | `/api/tenant/config/approvers` | staff token + tenant | `crmAdminOps.CreateApprover` |
| `DELETE` | `/api/tenant/config/approvers/{id}` | staff token + tenant | `crmAdminOps.DeleteApprover` |

### credit-memos

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/credit-memos` | staff token + tenant | `cmOps.List` |
| `POST` | `/api/tenant/credit-memos` | staff token + tenant | `cmOps.Create` |
| `POST` | `/api/tenant/credit-memos/search` | staff token + tenant | `cmOps.Search` |
| `DELETE` | `/api/tenant/credit-memos/{uuid}` | staff token + tenant | `cmOps.Delete` |
| `GET` | `/api/tenant/credit-memos/{uuid}` | staff token + tenant | `cmOps.Get` |
| `PATCH` | `/api/tenant/credit-memos/{uuid}` | staff token + tenant | `cmOps.Update` |
| `POST` | `/api/tenant/credit-memos/{uuid}/apply` | staff token + tenant | `cmOps.Apply` |
| `POST` | `/api/tenant/credit-memos/{uuid}/approve` | staff token + tenant | `cmOps.Approve` |
| `GET` | `/api/tenant/credit-memos/{uuid}/audit` | staff token + tenant | `cmOps.Audit` |
| `GET` | `/api/tenant/credit-memos/{uuid}/refunds` | staff token + tenant | `cmOps.Refunds` |
| `POST` | `/api/tenant/credit-memos/{uuid}/transition` | staff token + tenant | `cmOps.Transition` |
| `POST` | `/api/tenant/credit-memos/{uuid}/unapply` | staff token + tenant | `cmOps.Unapply` |

### crm

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/crm/customer/records/{id}/notes` | staff token + tenant | `customerNoteStaff.List` |
| `DELETE` | `/api/tenant/crm/customer/records/{id}/notes/{noteId}` | staff token + tenant | `customerNoteStaff.Delete` |
| `PATCH` | `/api/tenant/crm/customer/records/{id}/notes/{noteId}` | staff token + tenant | `customerNoteStaff.UpdateStatus` |
| `POST` | `/api/tenant/crm/customer/records/{id}/portal-invite` | staff token + tenant | `customerPortalAdmin.PortalInvite` |
| `GET` | `/api/tenant/crm/lookups` | staff token + tenant | `crmLookups.GetLookups` |
| `GET` | `/api/tenant/crm/statuses` | staff token + tenant | `crm.AllStatuses` |
| `GET` | `/api/tenant/crm/{workflowKey}/approvals/pending` | staff token + tenant | `crm.PendingApprovals` |
| `GET` | `/api/tenant/crm/{workflowKey}/records` | staff token + tenant | `crm.ListRecords` |
| `POST` | `/api/tenant/crm/{workflowKey}/records` | staff token + tenant | `crm.CreateRecord` |
| `POST` | `/api/tenant/crm/{workflowKey}/records/search` | staff token + tenant | `crm.SearchRecords` |
| `DELETE` | `/api/tenant/crm/{workflowKey}/records/{id}` | staff token + tenant | `crm.DeleteRecord` |
| `GET` | `/api/tenant/crm/{workflowKey}/records/{id}` | staff token + tenant | `crm.GetRecord` |
| `PATCH` | `/api/tenant/crm/{workflowKey}/records/{id}` | staff token + tenant | `crm.UpdateRecord` |
| `GET` | `/api/tenant/crm/{workflowKey}/records/{id}/activities` | staff token + tenant | `crmActivity.List` |
| `POST` | `/api/tenant/crm/{workflowKey}/records/{id}/activities` | staff token + tenant | `crmActivity.Create` |
| `DELETE` | `/api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}` | staff token + tenant | `crmActivity.Delete` |
| `PATCH` | `/api/tenant/crm/{workflowKey}/records/{id}/activities/{activityId}` | staff token + tenant | `crmActivity.Update` |
| `POST` | `/api/tenant/crm/{workflowKey}/records/{id}/approve` | staff token + tenant | `crm.ApproveRecord` |
| `GET` | `/api/tenant/crm/{workflowKey}/records/{id}/audit` | staff token + tenant | `crm.RecordAudit` |
| `POST` | `/api/tenant/crm/{workflowKey}/records/{id}/convert` | staff token + tenant | `crm.ConvertRecord` |
| `POST` | `/api/tenant/crm/{workflowKey}/records/{id}/transition` | staff token + tenant | `crm.TransitionRecord` |
| `GET` | `/api/tenant/crm/{workflowKey}/records/{id}/transitions` | staff token + tenant | `crm.AvailableTransitions` |
| `GET` | `/api/tenant/crm/{workflowKey}/statuses` | staff token + tenant | `crm.WorkflowStatuses` |

### customers

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/customers/{customerUuid}/portal-users` | staff token + tenant | `portalAccessOps.ListPortalUsers` |
| `POST` | `/api/tenant/customers/{customerUuid}/portal-users` | staff token + tenant | `portalAccessOps.CreatePortalUser` |
| `DELETE` | `/api/tenant/customers/{customerUuid}/portal-users/{id}` | staff token + tenant | `portalAccessOps.RevokePortalUser` |
| `POST` | `/api/tenant/customers/{customerUuid}/portal-users/{id}/resend` | staff token + tenant | `portalAccessOps.ResendPortalInvite` |
| `POST` | `/api/tenant/customers/{customerUuid}/portal-users/{id}/resume` | staff token + tenant | `portalAccessOps.ResumePortalUser` |
| `POST` | `/api/tenant/customers/{customerUuid}/portal-users/{id}/suspend` | staff token + tenant | `portalAccessOps.SuspendPortalUser` |

### dashboard

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/dashboard/widgets/me` | staff token + tenant | `dashboardUI.Me` |
| `ANY` | `/api/tenant/dashboard/widgets/roles` | staff token + tenant | `dashboardUI.RoleAllocations` |

### estimates

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/estimates` | staff token + tenant | `est.List` |
| `POST` | `/api/tenant/estimates` | staff token + tenant | `est.Create` |
| `POST` | `/api/tenant/estimates/search` | staff token + tenant | `est.Search` |
| `DELETE` | `/api/tenant/estimates/{uuid}` | staff token + tenant | `est.Delete` |
| `GET` | `/api/tenant/estimates/{uuid}` | staff token + tenant | `est.Get` |
| `PATCH` | `/api/tenant/estimates/{uuid}` | staff token + tenant | `est.Update` |
| `POST` | `/api/tenant/estimates/{uuid}/approve` | staff token + tenant | `est.Approve` |
| `GET` | `/api/tenant/estimates/{uuid}/audit` | staff token + tenant | `est.Audit` |
| `POST` | `/api/tenant/estimates/{uuid}/convert` | staff token + tenant | `est.Convert` |
| `POST` | `/api/tenant/estimates/{uuid}/transition` | staff token + tenant | `est.Transition` |

### expenses

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/expenses` | staff token + tenant | `expOps.List` |
| `POST` | `/api/tenant/expenses` | staff token + tenant | `expOps.Create` |
| `GET` | `/api/tenant/expenses/categories` | staff token + tenant | `expOps.Categories` |
| `POST` | `/api/tenant/expenses/search` | staff token + tenant | `expOps.Search` |
| `DELETE` | `/api/tenant/expenses/{uuid}` | staff token + tenant | `expOps.Delete` |
| `GET` | `/api/tenant/expenses/{uuid}` | staff token + tenant | `expOps.Get` |
| `PATCH` | `/api/tenant/expenses/{uuid}` | staff token + tenant | `expOps.Update` |
| `POST` | `/api/tenant/expenses/{uuid}/approve` | staff token + tenant | `expOps.Approve` |
| `GET` | `/api/tenant/expenses/{uuid}/audit` | staff token + tenant | `expOps.Audit` |
| `POST` | `/api/tenant/expenses/{uuid}/reject` | staff token + tenant | `expOps.Reject` |
| `POST` | `/api/tenant/expenses/{uuid}/transition` | staff token + tenant | `expOps.Transition` |

### fabrication-jobs

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/fabrication-jobs` | staff token + tenant | `fj.List` |
| `POST` | `/api/tenant/fabrication-jobs` | staff token + tenant | `fj.Create` |
| `POST` | `/api/tenant/fabrication-jobs/search` | staff token + tenant | `fj.Search` |
| `DELETE` | `/api/tenant/fabrication-jobs/{uuid}` | staff token + tenant | `fj.Delete` |
| `GET` | `/api/tenant/fabrication-jobs/{uuid}` | staff token + tenant | `fj.Get` |
| `PATCH` | `/api/tenant/fabrication-jobs/{uuid}` | staff token + tenant | `fj.Update` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/approve` | staff token + tenant | `fj.Approve` |
| `PUT` | `/api/tenant/fabrication-jobs/{uuid}/fabrication/status` | staff token + tenant | `fj.Transition` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/hold` | staff token + tenant | `fj.Hold` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/pieces` | staff token + tenant | `fj.AddPiece` |
| `DELETE` | `/api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid}` | staff token + tenant | `fj.RemovePiece` |
| `PATCH` | `/api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid}` | staff token + tenant | `fj.UpdatePiece` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/resume` | staff token + tenant | `fj.Resume` |
| `GET` | `/api/tenant/fabrication-jobs/{uuid}/slabs` | staff token + tenant | `fj.JobSlabs` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/slabs` | staff token + tenant | `fj.AllocateSlab` |
| `DELETE` | `/api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}` | staff token + tenant | `fj.DeallocateSlab` |
| `POST` | `/api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}/disposition` | staff token + tenant | `fj.Disposition` |
| `GET` | `/api/tenant/fabrication-jobs/{uuid}/steps` | staff token + tenant | `fj.Steps` |
| `PATCH` | `/api/tenant/fabrication-jobs/{uuid}/steps/{stepCode}` | staff token + tenant | `fj.UpdateStep` |

### finance

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/finance/account-defaults` | staff token + tenant | `coa.Defaults` |
| `PATCH` | `/api/tenant/finance/account-defaults/{slotKey}` | staff token + tenant | `coa.RepointDefault` |
| `GET` | `/api/tenant/finance/accounting-calendar` | staff token + tenant | `apOps.Calendar` |
| `POST` | `/api/tenant/finance/accounting-calendar/setup` | staff token + tenant | `apOps.Setup` |
| `GET` | `/api/tenant/finance/accounting-periods` | staff token + tenant | `apOps.List` |
| `POST` | `/api/tenant/finance/accounting-periods/close` | staff token + tenant | `apOps.Close` |
| `GET` | `/api/tenant/finance/accounting-periods/current` | staff token + tenant | `apOps.Current` |
| `POST` | `/api/tenant/finance/accounting-periods/lock-ap` | staff token + tenant | `apOps.LockAP` |
| `POST` | `/api/tenant/finance/accounting-periods/lock-ar` | staff token + tenant | `apOps.LockAR` |
| `POST` | `/api/tenant/finance/accounting-periods/lock-gl` | staff token + tenant | `apOps.LockGL` |
| `POST` | `/api/tenant/finance/accounting-periods/reopen` | staff token + tenant | `apOps.Reopen` |
| `POST` | `/api/tenant/finance/accounting-periods/unlock-ap` | staff token + tenant | `apOps.UnlockAP` |
| `POST` | `/api/tenant/finance/accounting-periods/unlock-ar` | staff token + tenant | `apOps.UnlockAR` |
| `POST` | `/api/tenant/finance/accounting-periods/unlock-gl` | staff token + tenant | `apOps.UnlockGL` |
| `GET` | `/api/tenant/finance/accounting-periods/{uuid}` | staff token + tenant | `apOps.Get` |
| `GET` | `/api/tenant/finance/accounting-periods/{uuid}/history` | staff token + tenant | `apOps.History` |
| `GET` | `/api/tenant/finance/accounts` | staff token + tenant | `coa.List` |
| `POST` | `/api/tenant/finance/accounts` | staff token + tenant | `coa.Create` |
| `PATCH` | `/api/tenant/finance/accounts/bulk` | staff token + tenant | `coa.BulkUpdate` |
| `GET` | `/api/tenant/finance/accounts/categories` | staff token + tenant | `coa.Categories` |
| `POST` | `/api/tenant/finance/accounts/search` | staff token + tenant | `coa.Search` |
| `GET` | `/api/tenant/finance/accounts/tree` | staff token + tenant | `coa.Tree` |
| `DELETE` | `/api/tenant/finance/accounts/{uuid}` | staff token + tenant | `coa.Delete` |
| `GET` | `/api/tenant/finance/accounts/{uuid}` | staff token + tenant | `coa.Get` |
| `PATCH` | `/api/tenant/finance/accounts/{uuid}` | staff token + tenant | `coa.Update` |
| `GET` | `/api/tenant/finance/accounts/{uuid}/history` | staff token + tenant | `coa.History` |
| `GET` | `/api/tenant/finance/cash-transfers` | staff token + tenant | `ctOps.List` |
| `POST` | `/api/tenant/finance/cash-transfers` | staff token + tenant | `ctOps.Create` |
| `POST` | `/api/tenant/finance/cash-transfers/search` | staff token + tenant | `ctOps.Search` |
| `DELETE` | `/api/tenant/finance/cash-transfers/{uuid}` | staff token + tenant | `ctOps.Delete` |
| `GET` | `/api/tenant/finance/cash-transfers/{uuid}` | staff token + tenant | `ctOps.Get` |
| `PATCH` | `/api/tenant/finance/cash-transfers/{uuid}` | staff token + tenant | `ctOps.Update` |
| `GET` | `/api/tenant/finance/cash-transfers/{uuid}/audit` | staff token + tenant | `ctOps.Audit` |
| `POST` | `/api/tenant/finance/cash-transfers/{uuid}/post` | staff token + tenant | `ctOps.Post` |
| `POST` | `/api/tenant/finance/cash-transfers/{uuid}/reverse` | staff token + tenant | `ctOps.Reverse` |
| `POST` | `/api/tenant/finance/cash-transfers/{uuid}/transition` | staff token + tenant | `ctOps.Transition` |
| `GET` | `/api/tenant/finance/fiscal-years` | staff token + tenant | `apOps.FiscalYears` |
| `POST` | `/api/tenant/finance/fiscal-years` | staff token + tenant | `apOps.GenerateYear` |

### inventory

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/inventory/adjustments` | staff token + tenant | `invAdj.List` |
| `POST` | `/api/tenant/inventory/adjustments` | staff token + tenant | `invAdj.Create` |
| `POST` | `/api/tenant/inventory/adjustments/search` | staff token + tenant | `invAdj.Search` |
| `DELETE` | `/api/tenant/inventory/adjustments/{uuid}` | staff token + tenant | `invAdj.Delete` |
| `GET` | `/api/tenant/inventory/adjustments/{uuid}` | staff token + tenant | `invAdj.Get` |
| `PATCH` | `/api/tenant/inventory/adjustments/{uuid}` | staff token + tenant | `invAdj.Update` |
| `GET` | `/api/tenant/inventory/adjustments/{uuid}/history` | staff token + tenant | `invAdj.History` |
| `POST` | `/api/tenant/inventory/adjustments/{uuid}/post` | staff token + tenant | `invAdj.Post` |
| `POST` | `/api/tenant/inventory/adjustments/{uuid}/transition` | staff token + tenant | `invAdj.Transition` |
| `GET` | `/api/tenant/inventory/bins` | staff token + tenant | `invBin.List` |
| `POST` | `/api/tenant/inventory/bins` | staff token + tenant | `invBin.Create` |
| `GET` | `/api/tenant/inventory/bins/tree` | staff token + tenant | `invBin.Tree` |
| `DELETE` | `/api/tenant/inventory/bins/{uuid}` | staff token + tenant | `invBin.Delete` |
| `GET` | `/api/tenant/inventory/bins/{uuid}` | staff token + tenant | `invBin.Get` |
| `PATCH` | `/api/tenant/inventory/bins/{uuid}` | staff token + tenant | `invBin.Update` |
| `GET` | `/api/tenant/inventory/bundles` | staff token + tenant | `invBundle.List` |
| `POST` | `/api/tenant/inventory/bundles` | staff token + tenant | `invBundle.Create` |
| `DELETE` | `/api/tenant/inventory/bundles/{uuid}` | staff token + tenant | `invBundle.Delete` |
| `GET` | `/api/tenant/inventory/bundles/{uuid}` | staff token + tenant | `invBundle.Get` |
| `PATCH` | `/api/tenant/inventory/bundles/{uuid}` | staff token + tenant | `invBundle.Update` |
| `PATCH` | `/api/tenant/inventory/bundles/{uuid}/bin` | staff token + tenant | `invBundle.MoveBin` |
| `POST` | `/api/tenant/inventory/bundles/{uuid}/break` | staff token + tenant | `invBundle.Break` |
| `DELETE` | `/api/tenant/inventory/bundles/{uuid}/members` | staff token + tenant | `invBundle.RemoveMembers` |
| `GET` | `/api/tenant/inventory/bundles/{uuid}/members` | staff token + tenant | `invBundle.Members` |
| `POST` | `/api/tenant/inventory/bundles/{uuid}/members` | staff token + tenant | `invBundle.AddMembers` |
| `POST` | `/api/tenant/inventory/bundles/{uuid}/seal` | staff token + tenant | `invBundle.Seal` |
| `GET` | `/api/tenant/inventory/counts` | staff token + tenant | `invCnt.List` |
| `POST` | `/api/tenant/inventory/counts` | staff token + tenant | `invCnt.Create` |
| `POST` | `/api/tenant/inventory/counts/search` | staff token + tenant | `invCnt.Search` |
| `DELETE` | `/api/tenant/inventory/counts/{uuid}` | staff token + tenant | `invCnt.Delete` |
| `GET` | `/api/tenant/inventory/counts/{uuid}` | staff token + tenant | `invCnt.Get` |
| `PATCH` | `/api/tenant/inventory/counts/{uuid}` | staff token + tenant | `invCnt.Update` |
| `POST` | `/api/tenant/inventory/counts/{uuid}/counts` | staff token + tenant | `invCnt.RecordCounts` |
| `POST` | `/api/tenant/inventory/counts/{uuid}/freeze` | staff token + tenant | `invCnt.Freeze` |
| `GET` | `/api/tenant/inventory/counts/{uuid}/history` | staff token + tenant | `invCnt.History` |
| `POST` | `/api/tenant/inventory/counts/{uuid}/post` | staff token + tenant | `invCnt.Post` |
| `POST` | `/api/tenant/inventory/counts/{uuid}/transition` | staff token + tenant | `invCnt.Transition` |
| `POST` | `/api/tenant/inventory/counts/{uuid}/unexpected` | staff token + tenant | `invCnt.AddUnexpected` |
| `GET` | `/api/tenant/inventory/items` | staff token + tenant | `inv.List` |
| `POST` | `/api/tenant/inventory/items` | staff token + tenant | `inv.Create` |
| `POST` | `/api/tenant/inventory/items/search` | staff token + tenant | `inv.Search` |
| `DELETE` | `/api/tenant/inventory/items/{uuid}` | staff token + tenant | `inv.Delete` |
| `GET` | `/api/tenant/inventory/items/{uuid}` | staff token + tenant | `inv.Get` |
| `PATCH` | `/api/tenant/inventory/items/{uuid}` | staff token + tenant | `inv.Update` |
| `GET` | `/api/tenant/inventory/items/{uuid}/history` | staff token + tenant | `inv.History` |
| `GET` | `/api/tenant/inventory/lookups` | staff token + tenant | `invLookup.All` |
| `GET` | `/api/tenant/inventory/lookups/{kind}` | staff token + tenant | `invLookup.List` |
| `POST` | `/api/tenant/inventory/lookups/{kind}` | staff token + tenant | `invLookup.Create` |
| `DELETE` | `/api/tenant/inventory/lookups/{kind}/{id}` | staff token + tenant | `invLookup.Delete` |
| `PATCH` | `/api/tenant/inventory/lookups/{kind}/{id}` | staff token + tenant | `invLookup.Update` |
| `POST` | `/api/tenant/inventory/slabs` | staff token + tenant | `invUnit.Create` |
| `GET` | `/api/tenant/inventory/slabs/{uuid}` | staff token + tenant | `invUnit.Get` |
| `POST` | `/api/tenant/inventory/slabs/{uuid}/scrap` | staff token + tenant | `invUnit.Scrap` |
| `GET` | `/api/tenant/inventory/transfers` | staff token + tenant | `invTrf.List` |
| `POST` | `/api/tenant/inventory/transfers` | staff token + tenant | `invTrf.Create` |
| `GET` | `/api/tenant/inventory/transfers/in-transit` | staff token + tenant | `invTrf.InTransit` |
| `POST` | `/api/tenant/inventory/transfers/search` | staff token + tenant | `invTrf.Search` |
| `DELETE` | `/api/tenant/inventory/transfers/{uuid}` | staff token + tenant | `invTrf.Delete` |
| `GET` | `/api/tenant/inventory/transfers/{uuid}` | staff token + tenant | `invTrf.Get` |
| `PATCH` | `/api/tenant/inventory/transfers/{uuid}` | staff token + tenant | `invTrf.Update` |
| `GET` | `/api/tenant/inventory/transfers/{uuid}/history` | staff token + tenant | `invTrf.History` |
| `POST` | `/api/tenant/inventory/transfers/{uuid}/receive` | staff token + tenant | `invTrf.Receive` |
| `POST` | `/api/tenant/inventory/transfers/{uuid}/ship` | staff token + tenant | `invTrf.Ship` |
| `POST` | `/api/tenant/inventory/transfers/{uuid}/transition` | staff token + tenant | `invTrf.Transition` |
| `GET` | `/api/tenant/inventory/units` | staff token + tenant | `invUnit.List` |
| `POST` | `/api/tenant/inventory/units` | staff token + tenant | `invUnit.Create` |
| `GET` | `/api/tenant/inventory/units/remnants` | staff token + tenant | `invUnit.Remnants` |
| `POST` | `/api/tenant/inventory/units/search` | staff token + tenant | `invUnit.Search` |
| `GET` | `/api/tenant/inventory/units/{uuid}` | staff token + tenant | `invUnit.Get` |
| `PATCH` | `/api/tenant/inventory/units/{uuid}/bin` | staff token + tenant | `invUnit.MoveBin` |
| `POST` | `/api/tenant/inventory/units/{uuid}/cut` | staff token + tenant | `invUnit.Cut` |
| `GET` | `/api/tenant/inventory/units/{uuid}/history` | staff token + tenant | `invUnit.History` |
| `POST` | `/api/tenant/inventory/units/{uuid}/scrap` | staff token + tenant | `invUnit.Scrap` |
| `GET` | `/api/tenant/inventory/warehouses` | staff token + tenant | `invWh.List` |
| `POST` | `/api/tenant/inventory/warehouses` | staff token + tenant | `invWh.Create` |
| `DELETE` | `/api/tenant/inventory/warehouses/{uuid}` | staff token + tenant | `invWh.Delete` |
| `GET` | `/api/tenant/inventory/warehouses/{uuid}` | staff token + tenant | `invWh.Get` |
| `PATCH` | `/api/tenant/inventory/warehouses/{uuid}` | staff token + tenant | `invWh.Update` |
| `POST` | `/api/tenant/inventory/warehouses/{uuid}/set-default` | staff token + tenant | `invWh.SetDefault` |

### invites

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/invites` | staff token + tenant | `userOps.ListInvites` |
| `DELETE` | `/api/tenant/invites/{id}` | staff token + tenant | `userOps.RevokeInvite` |
| `POST` | `/api/tenant/invites/{id}/resend` | staff token + tenant | `userOps.ResendInvite` |

### invoices

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/invoices` | staff token + tenant | `invOps.List` |
| `POST` | `/api/tenant/invoices` | staff token + tenant | `invOps.Create` |
| `POST` | `/api/tenant/invoices/search` | staff token + tenant | `invOps.Search` |
| `DELETE` | `/api/tenant/invoices/{uuid}` | staff token + tenant | `invOps.Delete` |
| `GET` | `/api/tenant/invoices/{uuid}` | staff token + tenant | `invOps.Get` |
| `PATCH` | `/api/tenant/invoices/{uuid}` | staff token + tenant | `invOps.Update` |
| `POST` | `/api/tenant/invoices/{uuid}/approve` | staff token + tenant | `invOps.Approve` |
| `GET` | `/api/tenant/invoices/{uuid}/audit` | staff token + tenant | `invOps.Audit` |
| `GET` | `/api/tenant/invoices/{uuid}/credit-memos` | staff token + tenant | `invOps.CreditMemos` |
| `POST` | `/api/tenant/invoices/{uuid}/payment` | staff token + tenant | `invOps.RecordPayment` |
| `GET` | `/api/tenant/invoices/{uuid}/payments` | staff token + tenant | `invOps.Payments` |
| `ANY` | `/api/tenant/invoices/{uuid}/portal-messages` | staff token + tenant | `portalMessageOps.MessagesFor` |
| `POST` | `/api/tenant/invoices/{uuid}/transition` | staff token + tenant | `invOps.Transition` |

### item-receipts

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/item-receipts` | staff token + tenant | `irOps.List` |
| `POST` | `/api/tenant/item-receipts` | staff token + tenant | `irOps.Create` |
| `POST` | `/api/tenant/item-receipts/search` | staff token + tenant | `irOps.Search` |
| `DELETE` | `/api/tenant/item-receipts/{uuid}` | staff token + tenant | `irOps.Delete` |
| `GET` | `/api/tenant/item-receipts/{uuid}` | staff token + tenant | `irOps.Get` |
| `PATCH` | `/api/tenant/item-receipts/{uuid}` | staff token + tenant | `irOps.Update` |
| `GET` | `/api/tenant/item-receipts/{uuid}/audit` | staff token + tenant | `irOps.Audit` |
| `POST` | `/api/tenant/item-receipts/{uuid}/post` | staff token + tenant | `irOps.Post` |
| `POST` | `/api/tenant/item-receipts/{uuid}/transition` | staff token + tenant | `irOps.Transition` |
| `POST` | `/api/tenant/item-receipts/{uuid}/void` | staff token + tenant | `irOps.Void` |

### me

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/tenant/me` | staff token + tenant | `` |

### payments

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/payments` | staff token + tenant | `payOps.List` |
| `POST` | `/api/tenant/payments` | staff token + tenant | `payOps.Create` |
| `POST` | `/api/tenant/payments/search` | staff token + tenant | `payOps.Search` |
| `DELETE` | `/api/tenant/payments/{uuid}` | staff token + tenant | `payOps.Delete` |
| `GET` | `/api/tenant/payments/{uuid}` | staff token + tenant | `payOps.Get` |
| `PATCH` | `/api/tenant/payments/{uuid}` | staff token + tenant | `payOps.Update` |
| `POST` | `/api/tenant/payments/{uuid}/apply` | staff token + tenant | `payOps.Apply` |
| `POST` | `/api/tenant/payments/{uuid}/approve` | staff token + tenant | `payOps.Approve` |
| `GET` | `/api/tenant/payments/{uuid}/audit` | staff token + tenant | `payOps.Audit` |
| `ANY` | `/api/tenant/payments/{uuid}/portal-messages` | staff token + tenant | `portalMessageOps.MessagesFor` |
| `GET` | `/api/tenant/payments/{uuid}/refunds` | staff token + tenant | `payOps.Refunds` |
| `POST` | `/api/tenant/payments/{uuid}/transition` | staff token + tenant | `payOps.Transition` |
| `POST` | `/api/tenant/payments/{uuid}/unapply` | staff token + tenant | `payOps.Unapply` |

### permissions

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/tenant/permissions/catalog` | staff token + tenant | `rbac.Catalog` |

### portal-users

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/portal-users` | staff token + tenant | `portalAccessOps.ListAllPortalUsers` |

### purchase-orders

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/purchase-orders` | staff token + tenant | `poOps.List` |
| `POST` | `/api/tenant/purchase-orders` | staff token + tenant | `poOps.Create` |
| `POST` | `/api/tenant/purchase-orders/search` | staff token + tenant | `poOps.Search` |
| `DELETE` | `/api/tenant/purchase-orders/{uuid}` | staff token + tenant | `poOps.Delete` |
| `GET` | `/api/tenant/purchase-orders/{uuid}` | staff token + tenant | `poOps.Get` |
| `PATCH` | `/api/tenant/purchase-orders/{uuid}` | staff token + tenant | `poOps.Update` |
| `POST` | `/api/tenant/purchase-orders/{uuid}/approve` | staff token + tenant | `poOps.Approve` |
| `GET` | `/api/tenant/purchase-orders/{uuid}/audit` | staff token + tenant | `poOps.Audit` |
| `POST` | `/api/tenant/purchase-orders/{uuid}/convert-to-bill` | staff token + tenant | `poOps.ConvertToBill` |
| `GET` | `/api/tenant/purchase-orders/{uuid}/receipts` | staff token + tenant | `irOps.ForPurchaseOrder` |
| `POST` | `/api/tenant/purchase-orders/{uuid}/transition` | staff token + tenant | `poOps.Transition` |

### quotes

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/quotes` | staff token + tenant | `quo.List` |
| `POST` | `/api/tenant/quotes` | staff token + tenant | `quo.Create` |
| `POST` | `/api/tenant/quotes/search` | staff token + tenant | `quo.Search` |
| `DELETE` | `/api/tenant/quotes/{uuid}` | staff token + tenant | `quo.Delete` |
| `GET` | `/api/tenant/quotes/{uuid}` | staff token + tenant | `quo.Get` |
| `PATCH` | `/api/tenant/quotes/{uuid}` | staff token + tenant | `quo.Update` |
| `POST` | `/api/tenant/quotes/{uuid}/approve` | staff token + tenant | `quo.Approve` |
| `GET` | `/api/tenant/quotes/{uuid}/audit` | staff token + tenant | `quo.Audit` |
| `POST` | `/api/tenant/quotes/{uuid}/convert` | staff token + tenant | `quo.Convert` |
| `POST` | `/api/tenant/quotes/{uuid}/transition` | staff token + tenant | `quo.Transition` |

### records

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/records/approvals/pending` | staff token + tenant | `wf.PendingApprovalsQueue` |
| `GET` | `/api/tenant/records/{id}` | staff token + tenant | `wf.GetRecord` |
| `PATCH` | `/api/tenant/records/{id}` | staff token + tenant | `wf.UpdateRecord` |
| `POST` | `/api/tenant/records/{id}/approve` | staff token + tenant | `wf.ApproveRecord` |
| `GET` | `/api/tenant/records/{id}/attachments` | staff token + tenant | `attachOps.ListAttachments` |
| `POST` | `/api/tenant/records/{id}/attachments` | staff token + tenant | `attachOps.ConfirmAttachments` |
| `POST` | `/api/tenant/records/{id}/attachments/presign-batch` | staff token + tenant | `attachOps.PresignBatch` |
| `DELETE` | `/api/tenant/records/{id}/attachments/{attachmentId}` | staff token + tenant | `attachOps.DeleteAttachment` |
| `GET` | `/api/tenant/records/{id}/attachments/{attachmentId}/download` | staff token + tenant | `attachOps.DownloadAttachment` |
| `GET` | `/api/tenant/records/{id}/document/pdf` | staff token + tenant | `docOps.GetPDF` |
| `POST` | `/api/tenant/records/{id}/document/send` | staff token + tenant | `docOps.Send` |
| `GET` | `/api/tenant/records/{id}/document/sends` | staff token + tenant | `docOps.Sends` |
| `POST` | `/api/tenant/records/{id}/transition` | staff token + tenant | `wf.TransitionRecord` |

### refunds

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/refunds` | staff token + tenant | `rfndOps.List` |
| `POST` | `/api/tenant/refunds` | staff token + tenant | `rfndOps.Create` |
| `POST` | `/api/tenant/refunds/search` | staff token + tenant | `rfndOps.Search` |
| `DELETE` | `/api/tenant/refunds/{uuid}` | staff token + tenant | `rfndOps.Delete` |
| `GET` | `/api/tenant/refunds/{uuid}` | staff token + tenant | `rfndOps.Get` |
| `PATCH` | `/api/tenant/refunds/{uuid}` | staff token + tenant | `rfndOps.Update` |
| `POST` | `/api/tenant/refunds/{uuid}/apply` | staff token + tenant | `rfndOps.Apply` |
| `POST` | `/api/tenant/refunds/{uuid}/approve` | staff token + tenant | `rfndOps.Approve` |
| `GET` | `/api/tenant/refunds/{uuid}/audit` | staff token + tenant | `rfndOps.Audit` |
| `ANY` | `/api/tenant/refunds/{uuid}/portal-messages` | staff token + tenant | `portalMessageOps.MessagesFor` |
| `POST` | `/api/tenant/refunds/{uuid}/transition` | staff token + tenant | `rfndOps.Transition` |
| `POST` | `/api/tenant/refunds/{uuid}/unapply` | staff token + tenant | `rfndOps.Unapply` |

### requisitions

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/requisitions` | staff token + tenant | `reqnOps.List` |
| `POST` | `/api/tenant/requisitions` | staff token + tenant | `reqnOps.Create` |
| `POST` | `/api/tenant/requisitions/search` | staff token + tenant | `reqnOps.Search` |
| `DELETE` | `/api/tenant/requisitions/{uuid}` | staff token + tenant | `reqnOps.Delete` |
| `GET` | `/api/tenant/requisitions/{uuid}` | staff token + tenant | `reqnOps.Get` |
| `PATCH` | `/api/tenant/requisitions/{uuid}` | staff token + tenant | `reqnOps.Update` |
| `POST` | `/api/tenant/requisitions/{uuid}/approve` | staff token + tenant | `reqnOps.Approve` |
| `GET` | `/api/tenant/requisitions/{uuid}/audit` | staff token + tenant | `reqnOps.Audit` |
| `POST` | `/api/tenant/requisitions/{uuid}/convert` | staff token + tenant | `reqnOps.Convert` |
| `POST` | `/api/tenant/requisitions/{uuid}/transition` | staff token + tenant | `reqnOps.Transition` |

### roles

| Method | Path | Requires | Handler |
|---|---|---|---|
| `ANY` | `/api/tenant/roles` | staff token + tenant | `rbac.Roles` |
| `ANY` | `/api/tenant/roles/` | staff token + tenant | `rbac.Role` |

### sales-orders

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/sales-orders` | staff token + tenant | `so.List` |
| `POST` | `/api/tenant/sales-orders` | staff token + tenant | `so.Create` |
| `POST` | `/api/tenant/sales-orders/search` | staff token + tenant | `so.Search` |
| `DELETE` | `/api/tenant/sales-orders/{uuid}` | staff token + tenant | `so.Delete` |
| `GET` | `/api/tenant/sales-orders/{uuid}` | staff token + tenant | `so.Get` |
| `PATCH` | `/api/tenant/sales-orders/{uuid}` | staff token + tenant | `so.Update` |
| `POST` | `/api/tenant/sales-orders/{uuid}/approve` | staff token + tenant | `so.Approve` |
| `GET` | `/api/tenant/sales-orders/{uuid}/audit` | staff token + tenant | `so.Audit` |
| `POST` | `/api/tenant/sales-orders/{uuid}/convert` | staff token + tenant | `so.Convert` |
| `POST` | `/api/tenant/sales-orders/{uuid}/fabricate` | staff token + tenant | `fj.Fabricate` |
| `GET` | `/api/tenant/sales-orders/{uuid}/inventory` | staff token + tenant | `so.Inventory` |
| `ANY` | `/api/tenant/sales-orders/{uuid}/portal-messages` | staff token + tenant | `portalMessageOps.MessagesFor` |
| `POST` | `/api/tenant/sales-orders/{uuid}/transition` | staff token + tenant | `so.Transition` |

### sso-configs

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/sso-configs` | staff token + tenant | `sso.ListConfigs` |
| `POST` | `/api/tenant/sso-configs` | staff token + tenant | `sso.CreateConfig` |
| `DELETE` | `/api/tenant/sso-configs/{id}` | staff token + tenant | `sso.DeleteConfig` |
| `GET` | `/api/tenant/sso-configs/{id}` | staff token + tenant | `sso.GetConfig` |
| `PUT` | `/api/tenant/sso-configs/{id}` | staff token + tenant | `sso.UpdateConfig` |
| `GET` | `/api/tenant/sso-configs/{id}/domains` | staff token + tenant | `sso.ListDomains` |
| `POST` | `/api/tenant/sso-configs/{id}/domains` | staff token + tenant | `sso.CreateDomain` |
| `DELETE` | `/api/tenant/sso-configs/{id}/domains/{domainId}` | staff token + tenant | `sso.DeleteDomain` |
| `POST` | `/api/tenant/sso-configs/{id}/refresh-metadata` | staff token + tenant | `sso.RefreshMetadata` |

### users

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/users` | staff token + tenant | `userOps.ListUsers` |
| `ANY` | `/api/tenant/users/` | staff token + tenant | `rbac.UserRoles` |
| `POST` | `/api/tenant/users/invite` | staff token + tenant | `userOps.InviteUser` |
| `GET` | `/api/tenant/users/me/permissions` | staff token + tenant | `rbac.MyPermissions` |
| `DELETE` | `/api/tenant/users/{id}` | staff token + tenant | `userOps.DeactivateUser` |
| `GET` | `/api/tenant/users/{id}` | staff token + tenant | `userOps.GetUser` |
| `PATCH` | `/api/tenant/users/{id}` | staff token + tenant | `userOps.UpdateUser` |

### vendor-bills

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/vendor-bills` | staff token + tenant | `vbOps.List` |
| `POST` | `/api/tenant/vendor-bills` | staff token + tenant | `vbOps.Create` |
| `POST` | `/api/tenant/vendor-bills/search` | staff token + tenant | `vbOps.Search` |
| `DELETE` | `/api/tenant/vendor-bills/{uuid}` | staff token + tenant | `vbOps.Delete` |
| `GET` | `/api/tenant/vendor-bills/{uuid}` | staff token + tenant | `vbOps.Get` |
| `PATCH` | `/api/tenant/vendor-bills/{uuid}` | staff token + tenant | `vbOps.Update` |
| `POST` | `/api/tenant/vendor-bills/{uuid}/approve` | staff token + tenant | `vbOps.Approve` |
| `GET` | `/api/tenant/vendor-bills/{uuid}/audit` | staff token + tenant | `vbOps.Audit` |
| `POST` | `/api/tenant/vendor-bills/{uuid}/payment` | staff token + tenant | `vbOps.RecordPayment` |
| `GET` | `/api/tenant/vendor-bills/{uuid}/payments` | staff token + tenant | `vbOps.Payments` |
| `DELETE` | `/api/tenant/vendor-bills/{uuid}/payments/{paymentId}` | staff token + tenant | `vbOps.RemovePayment` |
| `POST` | `/api/tenant/vendor-bills/{uuid}/transition` | staff token + tenant | `vbOps.Transition` |

### vendor-credits

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/vendor-credits` | staff token + tenant | `vcOps.List` |
| `POST` | `/api/tenant/vendor-credits` | staff token + tenant | `vcOps.Create` |
| `POST` | `/api/tenant/vendor-credits/search` | staff token + tenant | `vcOps.Search` |
| `DELETE` | `/api/tenant/vendor-credits/{uuid}` | staff token + tenant | `vcOps.Delete` |
| `GET` | `/api/tenant/vendor-credits/{uuid}` | staff token + tenant | `vcOps.Get` |
| `PATCH` | `/api/tenant/vendor-credits/{uuid}` | staff token + tenant | `vcOps.Update` |
| `POST` | `/api/tenant/vendor-credits/{uuid}/apply` | staff token + tenant | `vcOps.Apply` |
| `POST` | `/api/tenant/vendor-credits/{uuid}/approve` | staff token + tenant | `vcOps.Approve` |
| `GET` | `/api/tenant/vendor-credits/{uuid}/audit` | staff token + tenant | `vcOps.Audit` |
| `POST` | `/api/tenant/vendor-credits/{uuid}/reverse` | staff token + tenant | `vcOps.Reverse` |
| `POST` | `/api/tenant/vendor-credits/{uuid}/transition` | staff token + tenant | `vcOps.Transition` |

### vendor-payments

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/vendor-payments` | staff token + tenant | `vpOps.List` |
| `POST` | `/api/tenant/vendor-payments` | staff token + tenant | `vpOps.Create` |
| `POST` | `/api/tenant/vendor-payments/search` | staff token + tenant | `vpOps.Search` |
| `DELETE` | `/api/tenant/vendor-payments/{uuid}` | staff token + tenant | `vpOps.Delete` |
| `GET` | `/api/tenant/vendor-payments/{uuid}` | staff token + tenant | `vpOps.Get` |
| `PATCH` | `/api/tenant/vendor-payments/{uuid}` | staff token + tenant | `vpOps.Update` |
| `POST` | `/api/tenant/vendor-payments/{uuid}/apply` | staff token + tenant | `vpOps.Apply` |
| `POST` | `/api/tenant/vendor-payments/{uuid}/approve` | staff token + tenant | `vpOps.Approve` |
| `GET` | `/api/tenant/vendor-payments/{uuid}/audit` | staff token + tenant | `vpOps.Audit` |
| `POST` | `/api/tenant/vendor-payments/{uuid}/transition` | staff token + tenant | `vpOps.Transition` |
| `POST` | `/api/tenant/vendor-payments/{uuid}/unapply` | staff token + tenant | `vpOps.Unapply` |

### vendors

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/vendors` | staff token + tenant | `vnd.List` |
| `POST` | `/api/tenant/vendors` | staff token + tenant | `vnd.Create` |
| `POST` | `/api/tenant/vendors/search` | staff token + tenant | `vnd.Search` |
| `DELETE` | `/api/tenant/vendors/{uuid}` | staff token + tenant | `vnd.Delete` |
| `GET` | `/api/tenant/vendors/{uuid}` | staff token + tenant | `vnd.Get` |
| `PATCH` | `/api/tenant/vendors/{uuid}` | staff token + tenant | `vnd.Update` |
| `GET` | `/api/tenant/vendors/{uuid}/audit` | staff token + tenant | `vnd.Audit` |
| `POST` | `/api/tenant/vendors/{uuid}/transition` | staff token + tenant | `vnd.Transition` |

### workflows

| Method | Path | Requires | Handler |
|---|---|---|---|
| `GET` | `/api/tenant/workflows` | staff token + tenant | `wf.ListWorkflows` |
| `GET` | `/api/tenant/workflows/enabled` | staff token + tenant | `wf.ListEnabledWorkflows` |
| `GET` | `/api/tenant/workflows/{id}` | staff token + tenant | `wf.GetWorkflow` |
| `GET` | `/api/tenant/workflows/{id}/approval-chain` | staff token + tenant | `wf.GetApprovalChain` |
| `PUT` | `/api/tenant/workflows/{id}/approval-chain` | staff token + tenant | `wf.SetApprovalChain` |
| `GET` | `/api/tenant/workflows/{id}/approvers` | staff token + tenant | `wf.GetWorkflowApprovers` |
| `PATCH` | `/api/tenant/workflows/{id}/approvers` | staff token + tenant | `wf.SetWorkflowApprovers` |
| `POST` | `/api/tenant/workflows/{id}/enabled` | staff token + tenant | `wf.SetWorkflowEnabled` |
| `POST` | `/api/tenant/workflows/{id}/fields` | staff token + tenant | `wf.CreateField` |
| `DELETE` | `/api/tenant/workflows/{id}/fields/{fieldId}` | staff token + tenant | `wf.DeleteField` |
| `GET` | `/api/tenant/workflows/{id}/numbering` | staff token + tenant | `wf.GetNumberingConfig` |
| `PUT` | `/api/tenant/workflows/{id}/numbering` | staff token + tenant | `wf.SetNumberingConfig` |
| `GET` | `/api/tenant/workflows/{id}/records` | staff token + tenant | `wf.ListRecords` |
| `POST` | `/api/tenant/workflows/{id}/records` | staff token + tenant | `wf.CreateRecord` |
| `POST` | `/api/tenant/workflows/{id}/records/search` | staff token + tenant | `wf.SearchRecords` |

