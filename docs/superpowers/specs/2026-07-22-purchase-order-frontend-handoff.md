# Purchase Order — Frontend Implementation Handoff

**Date:** 2026-07-22
**Backend:** PR #76 (`thursday-work-planner` → `develop`), spec `2026-07-22-purchase-order-module-design.md`
**Frontend repo:** Skookum-Infotech/StoneSuite (React 19 + TypeScript + Vite)
**Sidebar slot:** Purchases → Purchase Orders (icon already present)

Every endpoint requires the JWT + tenant context (same auth wrapper as Estimates/Quotes/Invoices). All routes are `/api/tenant/purchase-orders...`. Follow the existing Estimate/Quote screens as the structural template — the API is shape-identical, with a vendor instead of a customer and one ship-to address instead of a billing/shipping pair.

## 1. Screens

### 1.1 List page (`/purchases/purchase-orders`)
- **Data:** `GET /api/tenant/purchase-orders?limit=25&cursor=...&search=...` or `POST .../search` with `{ filters, sort, limit, cursor, search }`.
- Columns: PO # (`purchaseOrderNumber`), Vendor (`vendor.name`), Order Date, Expected Date, Status chip (`statusCode` → color), Approval badge (`approvalStatus`: none/pending/approved), Grand Total, Owner.
- Keyset pagination: pass back `nextCursor` while `hasMore` — **no page numbers/offset**.
- Global search box (matches PO #, vendor name/number, reference, memo/notes, line SKU/name).
- Filter drawer keys (whitelisted server-side): `status`, `vendor_id`, `vendor_name`, `record_number`, `reference_number`, `order_date`, `expected_date`, `grand_total`, `owner_id`, `created_at`, `updated_at`, plus `cf:<key>` for custom fields. Unknown key ⇒ HTTP 400.
- Sortable columns only: `created_at`, `updated_at`, `record_number`, `document_number`, `order_date`, `grand_total`, `status`, `vendor_id`. (`expected_date` is intentionally NOT sortable — nullable.)
- Response note: list rows do **not** include `items` (fetch detail for lines).
- RBAC: hide the page unless `purchase_order:read`; "New Purchase Order" button gated on `purchase_order:create`. Response echoes effective `scope` (`all`/`own`).

### 1.2 Create / Edit form
- **Create:** `POST /api/tenant/purchase-orders` · **Edit:** `PATCH /api/tenant/purchase-orders/{uuid}`.
- **Editing is DRFT-only.** For any other status render read-only and surface the rule: "Recall to draft to edit." (Backend enforces with 400.)
- Header fields:
  - `vendorUuid` (required, create-only — vendor picker from `GET /api/tenant/vendors`; immutable after creation, render as static text on edit)
  - `orderDate` (default today), `expectedDate`, `referenceNumber`
  - `paymentTermsId`, `currencyId` (existing lookup dropdowns), `salesTaxPercent`
  - `ownerEmployeeId` (employee picker), `memo`, `notes`, `internalNotes`, `termsConditions`
  - `shipTo` block — single deliver-to address `{ name, attention, addrLine1, addrLine2, suiteUnit, city, stateId, zip, countryId, phone, fax, email }` (state/country from existing lookups). No billing block, no "same as billing" toggle.
  - `shippingCharge`, `adjustment`
  - `customFields` — render from the `purchase_order` workflow's field definitions (same dynamic custom-field renderer used elsewhere; ≤15 fields, validated server-side)
- Line items grid (`items[]`, at least one required):
  - Each row: `lineNumber` (unique, positive), **either** `inventoryItemUuid` (SKU picker from `GET /api/tenant/inventory-items`) **or** free-form `description` (required if no SKU)
  - `quantity` (> 0), `unitPrice` (0 ⇒ server snapshots the catalog price for SKU lines), `discountPercent`, `taxRateId` (optional; falls back to item's rate, then header `salesTaxPercent`)
  - Live client-side preview of line math, mirroring the server exactly: `subtotal = round2(qty×price)`, `discount = round2(sub×disc%)`, `tax = round2((sub−disc)×tax%)`, `total = sub−disc+tax`; header: `Σ lines + shippingCharge + adjustment = grandTotal`. **Server totals are authoritative** — always re-render from the response.
- Errors: 400 body `{ success:false, message }` — show message verbatim (they're user-ready: "Line 2: quantity must be greater than zero.").

### 1.3 Detail page (`/purchases/purchase-orders/:uuid`)
- **Data:** `GET /api/tenant/purchase-orders/{uuid}` → `{ success, purchaseOrder }` (includes `items`).
- Header card: PO #, status chip, approval badge, vendor (link to vendor detail), dates, totals summary (subtotal / discount / tax / shipping / adjustment / grand total).
- Lines table incl. **Qty Received** column and a per-line receiving progress bar (`qtyReceived / quantity`) — 0 for now, live once Item Receipt ships.
- Action buttons (see §2 for the state machine):
  - **Transition menu:** `GET` detail's `statusCode` + the map below decides which buttons show; call `POST .../{uuid}/transition` with `{ "toStatusCode": "PAPV" }` etc.
  - **Approve:** `POST .../{uuid}/approve` — show only when `approvalStatus === "pending"`; 403 body message = caller isn't a configured approver; 409 = not required.
  - **Edit** (DRFT only), **Delete** (DRFT/CANC only, confirm dialog) `DELETE .../{uuid}`.
- Audit tab: `GET .../{uuid}/audit` → `{ audit: [...] }` — same timeline component as estimate/invoice audit tabs.

## 2. Status machine (drive the UI from this map)

| From | Allowed targets | Button labels |
|---|---|---|
| `DRFT` | `PAPV`, `CANC` | Submit for Approval · Cancel |
| `PAPV` | `APPV`*, `DRFT`, `CANC` | Approve & Advance* · Recall to Draft · Cancel |
| `APPV` | `SENT`, `DRFT`, `CANC` | Send to Vendor · Revise (to Draft) · Cancel |
| `SENT` | `PART`, `RCVD`, `CLSD`, `CANC` | Mark Partially Received · Mark Received · Short-Close · Cancel |
| `PART` | `RCVD`, `CLSD` | Mark Received · Short-Close |
| `RCVD` | `CLSD` | Close |
| `CLSD` / `CANC` | — (terminal) | — |

\* `PAPV → APPV` is **blocked with 409** while `approvalStatus === "pending"` (configured approvers haven't all signed off). UI: disable the button with tooltip "Awaiting approval sign-off", show the Approve action instead. If no approvers are configured for PORD/PAPV, `approvalStatus` is `"none"` and the transition is a plain button.

Status chip colors (suggested, consistent with siblings): DRFT gray · PAPV amber · APPV blue · SENT indigo · PART orange · RCVD green · CLSD slate · CANC red.

## 3. Configuration section touchpoint

- **Workflows:** the `purchase_order` workflow already appears in Configuration → Workflows; its custom-field editor is what feeds the PO form's dynamic fields. Nothing new to build.
- **Approvers:** PO approvals are configured per (record type PORD, status PAPV) in `purchase_order_approver`. If the existing workflow-approvers UI is wired for CRM types only, add PORD to whichever approver-config screen the team standardizes on (same pattern as estimate/sales-order approvers).
- **Record Numbering:** `PORD` numbering appears automatically (`PORD-000001`); the existing Record Numbering screen needs no changes.
- **Roles & Access:** the role editor already lists `purchase_order` × create/read/update/delete/transition from the catalog endpoint — verify the Purchases group renders it.

## 4. RBAC / UX guards recap

- Gate route + buttons on `purchase_order:<action>` from `GET /api/tenant/users/me/permissions`.
- A 404 on a known uuid can mean out-of-scope (`own` scope) — treat as not-found, never "no permission".
- 409 responses (illegal transition, approval required/not required) carry user-ready messages — surface them as toasts.

## 5. TypeScript contract (response shapes)

```ts
interface PurchaseOrderLine {
  id: string; lineNumber: number; inventoryItemId?: string;
  sku: string; itemName: string; description: string; unitCode: string;
  quantity: number; qtyReceived: number; unitPrice: number;
  discountPercent: number; taxPercent: number;
  lineSubtotal: number; lineDiscount: number; lineTax: number; lineTotal: number;
}

interface PurchaseOrder {
  id: string; purchaseOrderNumber: string;
  status: string; statusCode: 'DRFT'|'PAPV'|'APPV'|'SENT'|'PART'|'RCVD'|'CLSD'|'CANC';
  approvalStatus: 'none'|'pending'|'approved';
  vendor: { id: string; name: string; number?: string };
  orderDate: string; expectedDate?: string; referenceNumber?: string;
  memo?: string; notes?: string; internalNotes?: string; termsConditions?: string;
  paymentTermsId: number|null; currencyId: number|null; ownerEmployeeId: number|null;
  salesTaxPercent: number;
  shipTo: { name: string; attention: string; addrLine1: string; addrLine2: string;
            suiteUnit: string; city: string; stateId: number|null; zip: string;
            countryId: number|null; phone: string; fax: string; email: string };
  customFields?: Record<string, unknown>;
  subtotal: number; discountTotal: number; taxTotal: number;
  shippingCharge: number; adjustment: number; grandTotal: number;
  createdAt: string; updatedAt: string;
  items?: PurchaseOrderLine[]; // detail only
}

interface PurchaseOrderPage {
  success: true; scope: 'all'|'own';
  records: PurchaseOrder[]; nextCursor: string; hasMore: boolean;
}
```

## 6. Suggested build order

1. API client + types (§5) and RBAC gating
2. List page with search/pagination (filters can follow)
3. Detail page (read-only) + status chips + audit tab
4. Create form (vendor picker, lines grid, live totals)
5. Edit (DRFT-only) + Delete
6. Transition buttons + Approve flow (409/403 handling)
7. Filter drawer + custom-field rendering
