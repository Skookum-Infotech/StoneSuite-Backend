# Resend Delivery Status (WebUI side) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the user who sent an email what actually happened to it — a status badge on every list the backend now decorates, and an "Email history" card on document pages (which had no send history at all).

**Architecture:** One shared, presentational `EmailStatusBadge` (label + icon + tone, never colour alone) driven by the `emailStatus…` fields the backend adds to rows. A new `DocumentSendHistory` card (react-query over a new `documentService.listSends`) is dropped into the five document detail pages. Existing invite/portal/tenant-invite views get the badge. No polling, no websockets: data refreshes on mount, window focus, a manual refresh button, and after a send.

**Tech Stack:** React 19, TypeScript, Vite, TanStack Query v5, Tailwind, lucide-react, Vitest + Testing Library (+ jest-dom).

**Spec:** `StoneSuite-Backend/docs/superpowers/specs/2026-10-01-resend-delivery-status-design.md` (§6).
**Depends on:** the backend plan (`…-backend.md`) Task 5 — the JSON contract below. The UI degrades gracefully against an older backend: no `emailStatus` field ⇒ no badge.
**Repo / branch:** `P:\WORKSPACE-SKOOKUM\StoneSuite-WebUI`, currently on `fix/webhooks`. This directory is **not** in the session's working directories — ask the user to add it (or run from a session opened there) before executing.

## Backend contract this plan consumes

Rows gain these **camelCase** fields (all optional on the wire):

| Field | Meaning |
|---|---|
| `emailStatus` | `queued · sent · retrying · delayed · delivered · complained · bounced · failed · suppressed · skipped · unknown` |
| `emailStatusAt` | ISO time of the state |
| `emailStatusMessage` | client-safe sentence; present for problem states, empty for sent/delivered |
| `emailRecipients` | `[{ email, status }]`, one per recipient |

On: `GET /tenant/records/{id}/document/sends` → `sends[]` (keys `id, recordId, workflowKey, sentTo, cc?, subject?, sentAt`), `GET /tenant/invites` → `invites[]` (**PascalCase** legacy keys such as `Status`, `Email`), `GET /tenant/customers/{uuid}/portal-users` → `portalUsers[]` (only rows that have an invite), `GET /platform/tenants/{id}/invites` → `invites[]`.

## Global Constraints

- **`unknown` / missing ⇒ render nothing.** A legacy row, a notify outage, and an old backend must all look like "no badge", never an error or a blank chip.
- **Status is communicated by text and icon, not colour alone** (the label is always visible; colour is reinforcement). Tooltips (`title`) are supplementary — problem messages are rendered as visible text wherever `showMessage` is used.
- Show the badge only where the email is still relevant: **pending** staff invites, **pending/expired** portal invites, **pending** tenant invites. Accepted/revoked invites show none.
- Never display provider text; only the backend's `emailStatusMessage` (already client-safe).
- Reuse existing styling vocabulary: pill classes as in `InviteStatusBadge`, sidebar card classes as in the detail pages (`rounded-xl border border-stone-200 bg-white shadow-sm p-4 space-y-3 mb-4`, title `text-xs font-semibold text-stone-400`), custom sizes `text-label` / `text-2xs`. Add `dark:` variants on new classes (as `Badge` in `components/tenant/ui.tsx` does).
- All API access goes through `src/services/*Service.ts`; no tenant/user ids as params; the server already scopes lists.
- New files use single quotes and the `@/` alias (like `SendToCustomerDialog.tsx`); inside an existing file, match that file's quote style (`UsersPage.tsx`, `InviteDetail.tsx` use double quotes).
- Tests: Vitest + Testing Library, `vi.mock` for services, `QueryClient` with `retry: false`.

## How to run things here

The frontend runs natively (Node), not through docker:

```bash
cd /p/WORKSPACE-SKOOKUM/StoneSuite-WebUI
npx vitest run <paths>        # focused tests
npx tsc -b                    # type-check the whole project
npx eslint <files>            # lint specific files
npm run ci                    # lint + test + build — what CI runs (final task)
```

The full suite has one known parallel-load flake (`EditCreditMemoPage.test.tsx`) that passes alone.

**Checkpoints:** each task ends with files to stage and a suggested Conventional-Commit message. **Do not commit unless the user has asked you to.**

## File Structure

| File | Responsibility |
|---|---|
| `src/types/emailStatus.ts` | `EmailStatus`, `EmailRecipientStatus`, `EmailStatusFields` |
| `src/lib/emailStatus.ts` (+test) | pure: `normalizeEmailStatus`, `EMAIL_STATUS_LABEL` |
| `src/components/tenant/EmailStatusBadge.tsx` (+test) | the badge (icon + label + tone, optional visible message) |
| `src/services/documentService.ts` (+test) | `listSends`, `DocumentSendRecord` |
| `src/components/tenant/DocumentSendHistory.tsx` (+test) | "Email history" sidebar card; exports `documentSendsKey` |
| `src/components/tenant/SendToCustomerDialog.tsx` (+test) | refresh the history after a send |
| `src/pages/sales/{Estimate,Invoice,Quote,SalesOrder}DetailPage.tsx`, `purchases/purchase-order/PurchaseOrderDetailPage.tsx` | mount the card; PO also refreshes it after a transition |
| `src/types/tenant.ts`, `src/types/portalUser.ts` | `UserInvite`, `TenantInvite`, `PortalUser` extend `EmailStatusFields` |
| `src/pages/config/users/UsersPage.tsx`, `components/InviteDetail.tsx` (+test) | staff-invite badge |
| `src/pages/crm/customer/components/PortalAccessPanel.tsx` (+test) | portal-invite badge |
| `src/components/customer/TenantInvitesPanel.tsx` | platform tenant-invite badge |

---

### Task 1: Types, pure helpers, and `EmailStatusBadge`

**Files:**
- Create: `src/types/emailStatus.ts`
- Create: `src/lib/emailStatus.ts`, `src/lib/emailStatus.test.ts`
- Create: `src/components/tenant/EmailStatusBadge.tsx`, `src/components/tenant/EmailStatusBadge.test.tsx`

**Interfaces:**
- Produces:
  - `type EmailStatus = 'queued'|'sent'|'retrying'|'delayed'|'delivered'|'complained'|'bounced'|'failed'|'suppressed'|'skipped'|'unknown'`
  - `interface EmailRecipientStatus { email: string; status: EmailStatus }`
  - `interface EmailStatusFields { emailStatus?: EmailStatus; emailStatusAt?: string; emailStatusMessage?: string; emailRecipients?: EmailRecipientStatus[] }`
  - `normalizeEmailStatus(raw: string | undefined): EmailStatus` — anything not in the vocabulary (including `undefined`) is `'unknown'`
  - `EMAIL_STATUS_LABEL: Record<Exclude<EmailStatus, 'unknown'>, string>`
  - `<EmailStatusBadge source={EmailStatusFields} showMessage?: boolean />` — renders `null` for unknown/missing

- [ ] **Step 1: Write the failing tests**

`src/lib/emailStatus.test.ts`:

```ts
import { describe, it, expect } from 'vitest';
import { EMAIL_STATUS_LABEL, normalizeEmailStatus } from './emailStatus';

describe('normalizeEmailStatus', () => {
  it.each([
    ['queued', 'queued'],
    ['sent', 'sent'],
    ['retrying', 'retrying'],
    ['delayed', 'delayed'],
    ['delivered', 'delivered'],
    ['complained', 'complained'],
    ['bounced', 'bounced'],
    ['failed', 'failed'],
    ['suppressed', 'suppressed'],
    ['skipped', 'skipped'],
    ['unknown', 'unknown'],
  ])('keeps the known status %s', (raw, want) => {
    expect(normalizeEmailStatus(raw)).toBe(want);
  });

  it.each([[undefined], [''], ['exploded'], ['DELIVERED'], ['delivery_delayed']])(
    'maps %s to unknown so a new backend value can never reach the UI unrendered',
    (raw) => {
      expect(normalizeEmailStatus(raw)).toBe('unknown');
    },
  );
});

describe('EMAIL_STATUS_LABEL', () => {
  it('has a human label for every displayable status', () => {
    expect(EMAIL_STATUS_LABEL).toMatchObject({
      queued: 'Queued',
      sent: 'Sent',
      retrying: 'Retrying',
      delayed: 'Delayed',
      delivered: 'Delivered',
      complained: 'Marked as spam',
      bounced: 'Bounced',
      failed: 'Failed',
      suppressed: 'Not sent',
      skipped: 'Skipped',
    });
  });
});
```

`src/components/tenant/EmailStatusBadge.test.tsx`:

```tsx
import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { EmailStatusBadge } from './EmailStatusBadge';

const BOUNCE_MESSAGE = "The recipient's mail server rejected this email. Check the address and try again.";

describe('EmailStatusBadge', () => {
  it.each([
    ['queued', 'Queued'],
    ['sent', 'Sent'],
    ['retrying', 'Retrying'],
    ['delayed', 'Delayed'],
    ['delivered', 'Delivered'],
    ['complained', 'Marked as spam'],
    ['bounced', 'Bounced'],
    ['failed', 'Failed'],
    ['suppressed', 'Not sent'],
    ['skipped', 'Skipped'],
  ] as const)('shows the %s status as the text "%s"', (status, label) => {
    render(<EmailStatusBadge source={{ emailStatus: status }} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it.each([[undefined], ['unknown' as const]])('renders nothing for %s', (status) => {
    const { container } = render(<EmailStatusBadge source={{ emailStatus: status }} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('renders nothing when the row carries no email fields at all (older backend)', () => {
    const { container } = render(<EmailStatusBadge source={{}} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the backend message as visible text when asked, not only as a tooltip', () => {
    render(<EmailStatusBadge source={{ emailStatus: 'bounced', emailStatusMessage: BOUNCE_MESSAGE }} showMessage />);
    expect(screen.getByText(BOUNCE_MESSAGE)).toBeVisible();
  });

  it('keeps the message out of the page by default (compact list rows)', () => {
    render(<EmailStatusBadge source={{ emailStatus: 'bounced', emailStatusMessage: BOUNCE_MESSAGE }} />);
    expect(screen.queryByText(BOUNCE_MESSAGE)).not.toBeInTheDocument();
  });

  it('lists each recipient when the send had several, in the tooltip', () => {
    render(
      <EmailStatusBadge
        source={{
          emailStatus: 'bounced',
          emailRecipients: [
            { email: 'a@acme.com', status: 'delivered' },
            { email: 'b@acme.com', status: 'bounced' },
          ],
        }}
      />,
    );
    const badge = screen.getByText('Bounced').closest('[data-email-status]');
    expect(badge).toHaveAttribute('data-email-status', 'bounced');
    expect(badge).toHaveAttribute('title', expect.stringContaining('a@acme.com: Delivered'));
    expect(badge).toHaveAttribute('title', expect.stringContaining('b@acme.com: Bounced'));
  });

  it('does not repeat a single recipient in the tooltip', () => {
    render(<EmailStatusBadge source={{ emailStatus: 'delivered', emailRecipients: [{ email: 'a@acme.com', status: 'delivered' }] }} />);
    expect(screen.getByText('Delivered').closest('[data-email-status]')).not.toHaveAttribute('title');
  });

  it('treats an unrecognised recipient status as unknown instead of crashing', () => {
    render(
      <EmailStatusBadge
        source={{
          emailStatus: 'sent',
          emailRecipients: [
            { email: 'a@acme.com', status: 'sent' },
            // a value from a newer backend than this bundle knows about
            { email: 'b@acme.com', status: 'weird' as never },
          ],
        }}
      />,
    );
    expect(screen.getByText('Sent').closest('[data-email-status]')).toHaveAttribute(
      'title',
      expect.stringContaining('b@acme.com: Unknown'),
    );
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/lib/emailStatus.test.ts src/components/tenant/EmailStatusBadge.test.tsx`
Expected: FAIL — cannot resolve `./emailStatus` / `./EmailStatusBadge`.

- [ ] **Step 3: Implement**

`src/types/emailStatus.ts`:

```ts
// The real outcome of an email, as the backend reports it on any row that
// triggered one. Mirrors the backend's services.EmailSummary JSON
// (emailStatus…). Every field is optional: an older backend, a row that
// predates delivery tracking, and a notification-service outage all omit them,
// and the UI renders nothing in that case.
export const EMAIL_STATUSES = [
  'queued',
  'sent',
  'retrying',
  'delayed',
  'delivered',
  'complained',
  'bounced',
  'failed',
  'suppressed',
  'skipped',
  'unknown',
] as const;

export type EmailStatus = (typeof EMAIL_STATUSES)[number];

export interface EmailRecipientStatus {
  email: string;
  status: EmailStatus;
}

export interface EmailStatusFields {
  emailStatus?: EmailStatus;
  emailStatusAt?: string;
  // Client-safe sentence; present for problem states, absent for sent/delivered.
  emailStatusMessage?: string;
  emailRecipients?: EmailRecipientStatus[];
}
```

`src/lib/emailStatus.ts`:

```ts
import { EMAIL_STATUSES, type EmailStatus } from '@/types/emailStatus';

// What the user reads. "Not sent" is the suppression-list case: the provider
// refused to send because the address is on a list of past bounces/complaints.
export const EMAIL_STATUS_LABEL: Record<Exclude<EmailStatus, 'unknown'>, string> = {
  queued: 'Queued',
  sent: 'Sent',
  retrying: 'Retrying',
  delayed: 'Delayed',
  delivered: 'Delivered',
  complained: 'Marked as spam',
  bounced: 'Bounced',
  failed: 'Failed',
  suppressed: 'Not sent',
  skipped: 'Skipped',
};

// normalizeEmailStatus narrows a wire value to the known vocabulary. Anything
// else — undefined, a typo, a state a newer backend added — is 'unknown', which
// the UI renders as nothing, so an unrecognised value is never shown raw.
export function normalizeEmailStatus(raw: string | undefined): EmailStatus {
  return (EMAIL_STATUSES as readonly string[]).includes(raw ?? '') ? (raw as EmailStatus) : 'unknown';
}
```

`src/components/tenant/EmailStatusBadge.tsx`:

```tsx
import { AlertTriangle, CheckCircle2, Clock, MailWarning, RefreshCw, Send, XCircle, type LucideIcon } from 'lucide-react';
import { cn } from '@/lib/utils';
import { EMAIL_STATUS_LABEL, normalizeEmailStatus } from '@/lib/emailStatus';
import type { EmailStatus, EmailStatusFields } from '@/types/emailStatus';

type Tone = 'ok' | 'progress' | 'warn' | 'bad';

// Colour reinforces the label and icon; it is never the only signal.
const TONE_CLASS: Record<Tone, string> = {
  ok: 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-300',
  progress: 'border-stone-200 bg-stone-100 text-stone-600 dark:border-stone-700 dark:bg-stone-800 dark:text-stone-300',
  warn: 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-300',
  bad: 'border-red-200 bg-red-50 text-red-600 dark:border-red-800 dark:bg-red-950 dark:text-red-300',
};

const VISUAL: Record<Exclude<EmailStatus, 'unknown'>, { tone: Tone; icon: LucideIcon }> = {
  queued: { tone: 'progress', icon: Clock },
  sent: { tone: 'progress', icon: Send },
  retrying: { tone: 'warn', icon: RefreshCw },
  delayed: { tone: 'warn', icon: Clock },
  delivered: { tone: 'ok', icon: CheckCircle2 },
  complained: { tone: 'bad', icon: AlertTriangle },
  bounced: { tone: 'bad', icon: XCircle },
  failed: { tone: 'bad', icon: XCircle },
  suppressed: { tone: 'bad', icon: MailWarning },
  skipped: { tone: 'progress', icon: Send },
};

const UNKNOWN_LABEL = 'Unknown';

function labelOf(status: EmailStatus): string {
  return status === 'unknown' ? UNKNOWN_LABEL : EMAIL_STATUS_LABEL[status];
}

interface EmailStatusBadgeProps {
  source: EmailStatusFields;
  // Render the backend's explanation as visible text under the badge. Use it
  // in detail views; leave it off in compact list rows (the tooltip still has it).
  showMessage?: boolean;
}

// EmailStatusBadge shows what happened to an email a user sent: queued, sent,
// delivered, or a problem (bounced, delayed, …). Renders nothing when the row
// carries no usable status, so legacy rows and a notification-service outage
// look like "no information", not like an error.
export function EmailStatusBadge({ source, showMessage = false }: EmailStatusBadgeProps) {
  const status = normalizeEmailStatus(source.emailStatus);
  if (status === 'unknown') return null;

  const { tone, icon: Icon } = VISUAL[status];
  const message = source.emailStatusMessage;
  const recipients = source.emailRecipients ?? [];

  // The tooltip adds detail; it is never the only place a message appears.
  const tooltipLines = [
    message,
    ...(recipients.length > 1
      ? recipients.map((r) => `${r.email}: ${labelOf(normalizeEmailStatus(r.status))}`)
      : []),
  ].filter(Boolean);

  return (
    <span className="inline-flex flex-col items-start gap-0.5">
      <span
        data-email-status={status}
        title={tooltipLines.length ? tooltipLines.join('\n') : undefined}
        className={cn(
          'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-label font-semibold whitespace-nowrap',
          TONE_CLASS[tone],
        )}
      >
        <Icon className="size-3" aria-hidden="true" />
        {EMAIL_STATUS_LABEL[status]}
      </span>
      {showMessage && message && <span className="text-2xs text-stone-500 dark:text-stone-400">{message}</span>}
    </span>
  );
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `npx vitest run src/lib/emailStatus.test.ts src/components/tenant/EmailStatusBadge.test.tsx && npx eslint src/types/emailStatus.ts src/lib/emailStatus.ts src/components/tenant/EmailStatusBadge.tsx && npx tsc -b`
Expected: PASS, lint clean, no type errors.

- [ ] **Step 5: Checkpoint** — stage `src/types/emailStatus.ts src/lib/emailStatus.ts src/lib/emailStatus.test.ts src/components/tenant/EmailStatusBadge.tsx src/components/tenant/EmailStatusBadge.test.tsx`; suggested message `feat(ui): add EmailStatusBadge for real email delivery status`.

---

### Task 2: `documentService.listSends` and the `DocumentSendHistory` card

**Files:**
- Modify: `src/services/documentService.ts`
- Create: `src/services/documentService.test.ts`
- Create: `src/components/tenant/DocumentSendHistory.tsx`, `src/components/tenant/DocumentSendHistory.test.tsx`

**Interfaces:**
- Consumes: Task 1 `EmailStatusFields`, `<EmailStatusBadge>`; `relativeTime(iso)` from `@/lib/recentRecordRoute`.
- Produces:
  - `interface DocumentSendRecord extends EmailStatusFields { id: string; recordId: string; workflowKey: string; sentTo: string; cc?: string; subject?: string; sentAt: string }`
  - `documentService.listSends(recordId: string): Promise<DocumentSendRecord[]>` — never rejects on an empty/`null` list, returns `[]`
  - `documentSendsKey(recordId: string): readonly ['document-sends', string]` (exported from `DocumentSendHistory.tsx`)
  - `<DocumentSendHistory recordId={string} />`

- [ ] **Step 1: Write the failing tests**

`src/services/documentService.test.ts`:

```ts
import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('@/api/tenantClient', () => ({
  tenantClient: { get: vi.fn(), post: vi.fn() },
}));

import { tenantClient } from '@/api/tenantClient';
import { documentService } from './documentService';

describe('documentService.listSends', () => {
  beforeEach(() => vi.clearAllMocks());

  it('reads the record-keyed send history, including the email status fields', async () => {
    vi.mocked(tenantClient.get).mockResolvedValue({
      data: {
        success: true,
        sends: [
          {
            id: 's1', recordId: 'rec-1', workflowKey: 'invoice', sentTo: 'a@acme.com', sentAt: '2026-10-01T12:00:00Z',
            emailStatus: 'bounced', emailStatusMessage: 'The recipient rejected this email.',
            emailRecipients: [{ email: 'a@acme.com', status: 'bounced' }],
          },
        ],
      },
    });

    const sends = await documentService.listSends('rec-1');

    expect(tenantClient.get).toHaveBeenCalledWith('/tenant/records/rec-1/document/sends');
    expect(sends).toHaveLength(1);
    expect(sends[0].emailStatus).toBe('bounced');
    expect(sends[0].emailRecipients?.[0].email).toBe('a@acme.com');
  });

  it('returns an empty list when the backend sends null or omits the key', async () => {
    vi.mocked(tenantClient.get).mockResolvedValueOnce({ data: { success: true, sends: null } });
    await expect(documentService.listSends('rec-1')).resolves.toEqual([]);

    vi.mocked(tenantClient.get).mockResolvedValueOnce({ data: { success: true } });
    await expect(documentService.listSends('rec-1')).resolves.toEqual([]);
  });
});
```

`src/components/tenant/DocumentSendHistory.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

vi.mock('@/services/documentService', () => ({
  documentService: { listSends: vi.fn() },
}));

import { DocumentSendHistory, documentSendsKey } from './DocumentSendHistory';
import { documentService, type DocumentSendRecord } from '@/services/documentService';

function send(over: Partial<DocumentSendRecord> = {}): DocumentSendRecord {
  return {
    id: 's1', recordId: 'rec-1', workflowKey: 'invoice', sentTo: 'buyer@acme.com',
    sentAt: new Date(Date.now() - 5 * 60_000).toISOString(), ...over,
  };
}

function renderCard() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <DocumentSendHistory recordId="rec-1" />
    </QueryClientProvider>,
  );
  return queryClient;
}

beforeEach(() => vi.clearAllMocks());

describe('DocumentSendHistory', () => {
  it('uses a stable query key other components can invalidate', () => {
    expect(documentSendsKey('rec-1')).toEqual(['document-sends', 'rec-1']);
  });

  it('says so when nothing has been emailed yet', async () => {
    vi.mocked(documentService.listSends).mockResolvedValue([]);
    renderCard();
    expect(await screen.findByText('Not emailed yet.')).toBeInTheDocument();
  });

  it('lists each send with its recipient, how long ago, and its real status', async () => {
    vi.mocked(documentService.listSends).mockResolvedValue([
      send({ id: 's1', sentTo: 'buyer@acme.com', emailStatus: 'delivered' }),
      send({
        id: 's2', sentTo: 'old@acme.com', cc: 'boss@acme.com', emailStatus: 'bounced',
        emailStatusMessage: "The recipient's mail server rejected this email. Check the address and try again.",
      }),
    ]);
    renderCard();

    expect(await screen.findByText('buyer@acme.com')).toBeInTheDocument();
    expect(screen.getByText('Delivered')).toBeInTheDocument();
    expect(screen.getByText('Bounced')).toBeInTheDocument();
    expect(screen.getByText(/old@acme\.com/)).toBeInTheDocument();
    // A problem is explained in words on the page, not only in a tooltip.
    expect(screen.getByText(/rejected this email/i)).toBeVisible();
    expect(screen.getAllByText(/5m ago/i).length).toBeGreaterThan(0);
  });

  it('shows no badge for a send with no status (an older send, or notify unreachable)', async () => {
    vi.mocked(documentService.listSends).mockResolvedValue([send({ emailStatus: 'unknown' })]);
    renderCard();

    expect(await screen.findByText('buyer@acme.com')).toBeInTheDocument();
    expect(document.querySelector('[data-email-status]')).toBeNull();
  });

  it('shows the latest five and reveals the rest on request', async () => {
    const many = Array.from({ length: 7 }, (_, i) => send({ id: `s${i}`, sentTo: `r${i}@acme.com` }));
    vi.mocked(documentService.listSends).mockResolvedValue(many);
    renderCard();

    expect(await screen.findByText('r0@acme.com')).toBeInTheDocument();
    expect(screen.queryByText('r5@acme.com')).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /show 2 more/i }));

    expect(screen.getByText('r5@acme.com')).toBeInTheDocument();
    expect(screen.getByText('r6@acme.com')).toBeInTheDocument();
  });

  it('degrades to a quiet message, not a crash, when the history cannot be loaded', async () => {
    vi.mocked(documentService.listSends).mockRejectedValue(new Error('network'));
    renderCard();
    expect(await screen.findByText(/couldn.t load email history/i)).toBeInTheDocument();
  });

  it('refetches when Refresh is pressed', async () => {
    vi.mocked(documentService.listSends).mockResolvedValue([send()]);
    renderCard();
    await screen.findByText('buyer@acme.com');
    expect(documentService.listSends).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole('button', { name: /refresh email history/i }));

    await waitFor(() => expect(documentService.listSends).toHaveBeenCalledTimes(2));
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/services/documentService.test.ts src/components/tenant/DocumentSendHistory.test.tsx`
Expected: FAIL — `documentService.listSends is not a function`; cannot resolve `./DocumentSendHistory`.

- [ ] **Step 3: Implement**

`src/services/documentService.ts` — add the import and type at the top, and the method inside `documentService`:

```ts
import type { EmailStatusFields } from '@/types/emailStatus';
```

```ts
// One row of a record's email history (GET …/document/sends). The email status
// fields report what the provider says happened to the email, not just that we
// handed it over; they are absent for sends that predate delivery tracking.
export interface DocumentSendRecord extends EmailStatusFields {
  id: string;
  recordId: string;
  workflowKey: string;
  // Comma-separated recipient list, as stored.
  sentTo: string;
  cc?: string;
  subject?: string;
  sentAt: string;
}
```

```ts
  // A record's email history, newest first. RBAC: <type>:read.
  listSends: (recordId: string): Promise<DocumentSendRecord[]> =>
    tenantClient
      .get<{ success: boolean; sends?: DocumentSendRecord[] | null }>(`/tenant/records/${recordId}/document/sends`)
      .then((r) => r.data.sends ?? []),
```

`src/components/tenant/DocumentSendHistory.tsx`:

```tsx
import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { RefreshCw } from 'lucide-react';
import { documentService } from '@/services/documentService';
import { EmailStatusBadge } from '@/components/tenant/EmailStatusBadge';
import { relativeTime } from '@/lib/recentRecordRoute';
import { cn } from '@/lib/utils';

const VISIBLE_SENDS = 5;

// documentSendsKey is the react-query key for one record's email history, so
// the code that sends (or transitions) a document can refresh it.
export const documentSendsKey = (recordId: string) => ['document-sends', recordId] as const;

// DocumentSendHistory is the "Email history" card on a document's detail page:
// each time the document was emailed, to whom, when, and what actually happened
// to the email. Refreshes on mount, on window focus, after a send, and on
// demand — a delivery result can arrive a minute after the send.
export function DocumentSendHistory({ recordId }: { recordId: string }) {
  const [showAll, setShowAll] = useState(false);
  const sends = useQuery({
    queryKey: documentSendsKey(recordId),
    queryFn: () => documentService.listSends(recordId),
    enabled: recordId !== '',
  });

  const rows = sends.data ?? [];
  const visible = showAll ? rows : rows.slice(0, VISIBLE_SENDS);
  const hidden = rows.length - visible.length;

  return (
    <div className="rounded-xl border border-stone-200 bg-white shadow-sm p-4 space-y-3 mb-4">
      <div className="flex items-center justify-between">
        <p className="text-xs font-semibold text-stone-400">Email history</p>
        <button
          type="button"
          onClick={() => void sends.refetch()}
          aria-label="Refresh email history"
          className="rounded p-1 text-stone-400 hover:bg-stone-100 hover:text-stone-700"
        >
          <RefreshCw className={cn('size-3.5', sends.isFetching && 'animate-spin')} />
        </button>
      </div>

      {sends.isError && <p className="text-2xs text-stone-500">Couldn’t load email history.</p>}
      {sends.isSuccess && rows.length === 0 && <p className="text-2xs text-stone-500">Not emailed yet.</p>}

      {visible.length > 0 && (
        <ul className="space-y-3">
          {visible.map((s) => (
            <li key={s.id} className="space-y-1 border-b border-stone-100 pb-3 last:border-0 last:pb-0">
              <p className="text-xs text-stone-700 break-words">{s.sentTo}</p>
              {s.cc && <p className="text-2xs text-stone-400 break-words">cc {s.cc}</p>}
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <EmailStatusBadge source={s} showMessage />
                <span className="text-2xs text-stone-400" title={new Date(s.sentAt).toLocaleString()}>
                  {relativeTime(s.sentAt)}
                </span>
              </div>
            </li>
          ))}
        </ul>
      )}

      {hidden > 0 && (
        <button type="button" onClick={() => setShowAll(true)} className="text-2xs font-semibold text-stone-500 hover:text-stone-800">
          Show {hidden} more
        </button>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `npx vitest run src/services/documentService.test.ts src/components/tenant/DocumentSendHistory.test.tsx && npx eslint src/services/documentService.ts src/components/tenant/DocumentSendHistory.tsx && npx tsc -b`
Expected: PASS, lint clean, types OK.

- [ ] **Step 5: Checkpoint** — stage `src/services/documentService.ts src/services/documentService.test.ts src/components/tenant/DocumentSendHistory.tsx src/components/tenant/DocumentSendHistory.test.tsx`; suggested message `feat(ui): add email history card and documentService.listSends`.

---

### Task 3: Mount the card on the five document pages and refresh it after a send

**Files:**
- Modify: `src/components/tenant/SendToCustomerDialog.tsx`, `src/components/tenant/SendToCustomerDialog.test.tsx`
- Modify: `src/pages/sales/EstimateDetailPage.tsx`, `InvoiceDetailPage.tsx`, `QuoteDetailPage.tsx`, `SalesOrderDetailPage.tsx`
- Modify: `src/pages/purchases/purchase-order/PurchaseOrderDetailPage.tsx`
- Modify (so they keep passing): `src/pages/sales/InvoiceDetailPage.test.tsx`, `src/pages/purchases/purchase-order/PurchaseOrderDetailPage.test.tsx`

**Interfaces:** Consumes Task 2 `DocumentSendHistory`, `documentSendsKey`.

- [ ] **Step 1: Write the failing test** — in `src/components/tenant/SendToCustomerDialog.test.tsx` add (the file already mocks `documentService.sendToCustomer` and `sonner`; widen the mock factory only if the compiler asks). Add a helper that exposes the client, then the tests:

```tsx
import { documentSendsKey } from './DocumentSendHistory';

function renderWithClient() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const spy = vi.spyOn(queryClient, 'invalidateQueries');
  render(
    <QueryClientProvider client={queryClient}>
      <SendToCustomerDialog
        recordId={RECORD_ID}
        open
        onOpenChange={vi.fn()}
        recipientEmail="buyer@acme.com"
        label="Invoice INV-1"
        onSent={vi.fn()}
      />
    </QueryClientProvider>,
  );
  return spy;
}

describe('SendToCustomerDialog — refreshes the email history', () => {
  it('refreshes the history after a send that went out', async () => {
    vi.mocked(documentService.sendToCustomer).mockResolvedValue({ sendId: 's1', sentTo: ['buyer@acme.com'], emailSent: true });
    const spy = renderWithClient();

    await userEvent.click(confirm());

    await waitFor(() => expect(spy).toHaveBeenCalledWith({ queryKey: documentSendsKey(RECORD_ID) }));
  });

  it('refreshes the history even when the email failed — the send is recorded either way', async () => {
    vi.mocked(documentService.sendToCustomer).mockResolvedValue({
      sendId: 's1', sentTo: ['buyer@acme.com'], emailSent: false, emailError: EMAIL_ERROR,
    });
    const spy = renderWithClient();

    await userEvent.click(confirm());

    await waitFor(() => expect(spy).toHaveBeenCalledWith({ queryKey: documentSendsKey(RECORD_ID) }));
  });
});
```

(Add `documentSendsKey` to the existing imports; the file already imports `render, screen, waitFor`, `userEvent`, `QueryClient`, `QueryClientProvider`, and defines `RECORD_ID`, `EMAIL_ERROR`, `confirm`.) If the existing `vi.mock('@/services/documentService', …)` factory only provides `sendToCustomer`, add `listSends: vi.fn()` to it so `DocumentSendHistory` (imported for its key) loads.

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/components/tenant/SendToCustomerDialog.test.tsx`
Expected: FAIL — `invalidateQueries` never called with the history key.

- [ ] **Step 3: Implement**

`src/components/tenant/SendToCustomerDialog.tsx`: change the react-query import to include `useQueryClient`, import the key, create the client inside the component, and refresh first thing in the mutation's `onSuccess`:

```tsx
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { documentSendsKey } from '@/components/tenant/DocumentSendHistory';
```

```tsx
  const queryClient = useQueryClient();
```

```tsx
    onSuccess: (result) => {
      // The send is recorded whether or not the email went, so the history
      // changes either way.
      void queryClient.invalidateQueries({ queryKey: documentSendsKey(recordId) });
      if (result.emailSent === false) {
```

(`if (result.emailSent === false) {` is the existing first line of that handler; keep the rest as is.)

**Mount the card** — in each of the five pages: add the import, and insert `<DocumentSendHistory recordId={id} />` inside `<SalesDetailSidebar …>` **immediately before the `{canDelete && (` line that precedes `<DangerZoneCard>`** (find it with `grep -n "canDelete &&" <file>`; the Status card's closing `</div>` is the line above). In each file `id` is the `useParams` value (`const { id = '' } = useParams();`).

```tsx
import { DocumentSendHistory } from '@/components/tenant/DocumentSendHistory';
```

```tsx
          <DocumentSendHistory recordId={id} />

          {canDelete && (
```

Files: `src/pages/sales/EstimateDetailPage.tsx`, `InvoiceDetailPage.tsx`, `QuoteDetailPage.tsx`, `SalesOrderDetailPage.tsx`, `src/pages/purchases/purchase-order/PurchaseOrderDetailPage.tsx`.

**Purchase order** sends through a status transition, not the dialog, so also refresh in its `transition` mutation. Add `import { documentSendsKey, DocumentSendHistory } from '@/components/tenant/DocumentSendHistory';` (single import) and, in `transition`'s `onSuccess`, after `queryClient.invalidateQueries({ queryKey: ['purchase-orders'] });`:

```tsx
      queryClient.invalidateQueries({ queryKey: documentSendsKey(id) });
```

**Keep the existing page tests green.** `InvoiceDetailPage.test.tsx` and `PurchaseOrderDetailPage.test.tsx` render these pages with only their own services mocked; the new card would call the real `documentService.listSends`. Add to each test file, with the other `vi.mock` calls:

```tsx
vi.mock('@/services/documentService', () => ({
  documentService: { sendToCustomer: vi.fn(), listSends: vi.fn().mockResolvedValue([]) },
}));
```

(If a test file already mocks `documentService`, add `listSends` to that factory instead.) The other three pages have no page test.

- [ ] **Step 4: Run to verify it passes**

Run: `npx vitest run src/components/tenant src/pages/sales src/pages/purchases/purchase-order && npx eslint src/components/tenant/SendToCustomerDialog.tsx src/pages/sales/EstimateDetailPage.tsx src/pages/sales/InvoiceDetailPage.tsx src/pages/sales/QuoteDetailPage.tsx src/pages/sales/SalesOrderDetailPage.tsx src/pages/purchases/purchase-order/PurchaseOrderDetailPage.tsx && npx tsc -b`
Expected: PASS, lint clean, types OK.

- [ ] **Step 5: Checkpoint** — stage `src/components/tenant/SendToCustomerDialog.tsx src/components/tenant/SendToCustomerDialog.test.tsx src/pages/sales src/pages/purchases/purchase-order`; suggested message `feat(ui): show email history on document pages and refresh it after a send`.

---

### Task 4: Staff invites (Users → Invites tab)

**Files:**
- Modify: `src/types/tenant.ts` (`UserInvite`)
- Modify: `src/pages/config/users/UsersPage.tsx` (list row)
- Modify: `src/pages/config/users/components/InviteDetail.tsx`, `InviteDetail.test.tsx`

- [ ] **Step 1: Write the failing tests** — in `src/pages/config/users/components/InviteDetail.test.tsx`, let `renderDetail` take an invite (default keeps every existing test untouched):

```tsx
function renderDetail(invite: UserInvite = PENDING_INVITE) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={queryClient}>
      <InviteDetail invite={invite} />
    </QueryClientProvider>,
  );
}
```

and append:

```tsx
describe('InviteDetail — real email status', () => {
  const BOUNCE_MESSAGE = "The recipient's mail server rejected this email. Check the address and try again.";

  it('explains a bounced invitation in words', () => {
    renderDetail({ ...PENDING_INVITE, emailStatus: 'bounced', emailStatusMessage: BOUNCE_MESSAGE });

    expect(screen.getByText('Bounced')).toBeInTheDocument();
    expect(screen.getByText(BOUNCE_MESSAGE)).toBeVisible();
  });

  it('shows delivery of a pending invitation', () => {
    renderDetail({ ...PENDING_INVITE, emailStatus: 'delivered' });
    expect(screen.getByText('Delivered')).toBeInTheDocument();
  });

  it('shows nothing when the status is unknown (older invite, or notify unreachable)', () => {
    renderDetail({ ...PENDING_INVITE, emailStatus: 'unknown' });
    expect(document.querySelector('[data-email-status]')).toBeNull();
  });

  it('shows no email status once the invitation was accepted', () => {
    renderDetail({ ...PENDING_INVITE, Status: 'accepted', AcceptedAt: new Date().toISOString(), emailStatus: 'bounced' });
    expect(document.querySelector('[data-email-status]')).toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/pages/config/users/components/InviteDetail.test.tsx`
Expected: FAIL (type error on `emailStatus` / no badge rendered).

- [ ] **Step 3: Implement**

`src/types/tenant.ts` — add the import near the top and extend the interface:

```ts
import type { EmailStatusFields } from '@/types/emailStatus';
```

```ts
export interface UserInvite extends EmailStatusFields {
```

`src/pages/config/users/components/InviteDetail.tsx` — import the badge (this file uses double quotes):

```tsx
import { EmailStatusBadge } from "@/components/tenant/EmailStatusBadge";
```

and add, as the first row inside the `mb-6 space-y-1.5 text-xs` details block (immediately before the `Expires` row):

```tsx
        {invite.Status === "pending" && invite.emailStatus && invite.emailStatus !== "unknown" && (
          <div className="flex justify-between gap-3">
            <span className="text-stone-400">Email</span>
            <EmailStatusBadge source={invite} showMessage />
          </div>
        )}
```

`src/pages/config/users/UsersPage.tsx` — import the badge (double quotes) and put it beside the invite status in the list row. Replace

```tsx
                          <div className="mt-0.5">
                            <InviteStatusBadge invite={inv} />
                          </div>
```

with

```tsx
                          <div className="mt-0.5 flex flex-wrap items-center gap-1">
                            <InviteStatusBadge invite={inv} />
                            {inv.Status === "pending" && <EmailStatusBadge source={inv} />}
                          </div>
```

- [ ] **Step 4: Run to verify it passes**

Run: `npx vitest run src/pages/config/users && npx eslint src/pages/config/users/UsersPage.tsx src/pages/config/users/components/InviteDetail.tsx src/types/tenant.ts && npx tsc -b`
Expected: PASS (existing `InviteDetail` and `InviteModal` tests included), lint clean, types OK.

- [ ] **Step 5: Checkpoint** — stage `src/types/tenant.ts src/pages/config/users`; suggested message `feat(ui): show real email status on workspace invitations`.

---

### Task 5: Portal invites (customer's Portal Access card)

**Files:**
- Modify: `src/types/portalUser.ts` (`PortalUser`)
- Modify: `src/pages/crm/customer/components/PortalAccessPanel.tsx`, `PortalAccessPanel.test.tsx`

- [ ] **Step 1: Write the failing tests** — append to `PortalAccessPanel.test.tsx` (it already has `makeUser`, `mockPermissions`, `renderPanel`):

```tsx
describe('PortalAccessPanel — real invitation email status', () => {
  const BOUNCE_MESSAGE = "The recipient's mail server rejected this email. Check the address and try again.";

  it('tells staff when a pending invitation bounced, and why', async () => {
    mockPermissions();
    vi.mocked(portalAccessService.listForCustomer).mockResolvedValue([
      makeUser({ inviteStatus: 'pending', emailStatus: 'bounced', emailStatusMessage: BOUNCE_MESSAGE }),
    ]);

    renderPanel();

    expect(await screen.findByText('Bounced')).toBeInTheDocument();
    expect(screen.getByText(BOUNCE_MESSAGE)).toBeVisible();
  });

  it('shows an expired invitation whose email was delivered', async () => {
    mockPermissions();
    vi.mocked(portalAccessService.listForCustomer).mockResolvedValue([
      makeUser({ inviteStatus: 'expired', emailStatus: 'delivered' }),
    ]);

    renderPanel();

    expect(await screen.findByText('Delivered')).toBeInTheDocument();
  });

  it('shows no email status once the invitation was accepted', async () => {
    mockPermissions();
    vi.mocked(portalAccessService.listForCustomer).mockResolvedValue([
      makeUser({ inviteStatus: 'accepted', emailStatus: 'delivered' }),
    ]);

    renderPanel();

    expect(await screen.findByText(CONTACT_NAME)).toBeInTheDocument();
    expect(document.querySelector('[data-email-status]')).toBeNull();
  });

  it('shows nothing for an unknown status', async () => {
    mockPermissions();
    vi.mocked(portalAccessService.listForCustomer).mockResolvedValue([
      makeUser({ inviteStatus: 'pending', emailStatus: 'unknown' }),
    ]);

    renderPanel();

    expect(await screen.findByText(CONTACT_NAME)).toBeInTheDocument();
    expect(document.querySelector('[data-email-status]')).toBeNull();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `npx vitest run src/pages/crm/customer/components/PortalAccessPanel.test.tsx`
Expected: FAIL (type error on `emailStatus`; no badge).

- [ ] **Step 3: Implement**

`src/types/portalUser.ts` — add the import at the top and extend:

```ts
import type { EmailStatusFields } from '@/types/emailStatus';
```

```ts
export interface PortalUser extends EmailStatusFields {
```

(`PortalUserRosterEntry extends PortalUser` inherits the optional fields harmlessly.)

`src/pages/crm/customer/components/PortalAccessPanel.tsx` (double quotes) — import:

```tsx
import { EmailStatusBadge } from "@/components/tenant/EmailStatusBadge";
```

and add, directly after the `Granted {fmtDate(user.createdAt)}` paragraph inside the left `min-w-0 flex-1` block:

```tsx
          {(user.inviteStatus === "pending" || user.inviteStatus === "expired") && (
            <div className="mt-1">
              <EmailStatusBadge source={user} showMessage />
            </div>
          )}
```

- [ ] **Step 4: Run to verify it passes**

Run: `npx vitest run src/pages/crm/customer && npx eslint src/pages/crm/customer/components/PortalAccessPanel.tsx src/types/portalUser.ts && npx tsc -b`
Expected: PASS, lint clean, types OK.

- [ ] **Step 5: Checkpoint** — stage `src/types/portalUser.ts src/pages/crm/customer/components`; suggested message `feat(ui): show real email status on customer portal invitations`.

---

### Task 6: Platform tenant invites, full verification, and a visual check

**Files:**
- Modify: `src/types/tenant.ts` (`TenantInvite`)
- Modify: `src/components/customer/TenantInvitesPanel.tsx`

- [ ] **Step 1: Implement** (type-driven; the behaviour is covered by the badge's own tests and `tsc`, and this panel has no test file):

`src/types/tenant.ts`:

```ts
export interface TenantInvite extends EmailStatusFields {
```

(`EmailStatusFields` is already imported there from Task 4.)

`src/components/customer/TenantInvitesPanel.tsx` — import:

```tsx
import { EmailStatusBadge } from '@/components/tenant/EmailStatusBadge';
```

and add, immediately after the `Expires …` `<p>` in each invite card (the paragraph containing `Expires {new Date(inv.expiresAt).toLocaleString()}`):

```tsx
              {inv.status === 'pending' && (
                <div className="mt-1.5">
                  <EmailStatusBadge source={inv} showMessage />
                </div>
              )}
```

- [ ] **Step 2: Whole-project verification**

Run: `npx tsc -b && npx eslint src && npm test`
Expected: types clean, lint clean, all tests PASS (re-run `EditCreditMemoPage.test.tsx` alone if it flakes under parallel load).

Then the CI bundle: `npm run ci` — lint + test + build PASS.

- [ ] **Step 3: Visual check of the new pieces** — the real pages need a running backend + notify, so check the components in isolation with the Browser pane. Start the dev server (`npm run dev`, via `preview_start` if `.claude/launch.json` defines it, else ask the user), then verify with a throwaway route or story rendering `EmailStatusBadge` for every status in light and dark mode (`resize_window` with `colorScheme`) and at phone width (375px): each of the ten states shows icon + text, problem states are legible in both themes, long recipient lists wrap, and `DocumentSendHistory` fits the 288px sidebar and the mobile bottom sheet without horizontal scroll. Remove any throwaway route afterwards (`git status` must show only the files listed in this plan).

- [ ] **Step 4: Report** — summarise tests and checks run, list the files changed, and note: nothing was committed; against an older backend every badge is invisible by design; the cross-service proof (send a document to `bounced@resend.dev` and watch the badge and the bell) happens after all three services are deployed (spec §10–11).

- [ ] **Step 5: Checkpoint** — stage `src/types/tenant.ts src/components/customer/TenantInvitesPanel.tsx`; suggested message `feat(ui): show real email status on platform tenant invitations`.

---

## Self-Review (run against the spec before handing off)

**Spec coverage (§6):**
- Shared `EmailStatusBadge`, text + icon not colour alone, tooltip with per-recipient detail and safe message → Task 1.
- Used in the send history, PO detail, `InviteDetail`, `PortalAccessPanel`, `TenantInvitesPanel` → Tasks 2–6. **Gap the spec did not anticipate:** the WebUI had no send-history view at all (`GET …/document/sends` had no consumer), so the spec's "badge in the send history" required a new `DocumentSendHistory` card (Task 2) mounted on all five document pages (Task 3). The PO page sends through a status transition, not `SendToCustomerDialog`, so it gets its own refresh (Task 3).
- Refresh on mount and focus (react-query default), after a send (dialog `onSuccess`, PO `transition`), plus a manual refresh button — no polling or websockets, as specified.
- Bell needs no change: the alert is an ordinary notification (`email.delivery_problem`). Verify after deploy that the bell renders the unfamiliar `eventType` generically — it only reads `title`, `body` and `link`.
- Unknown/missing ⇒ nothing, accepted/revoked invites ⇒ nothing → Global Constraints + tests in Tasks 1, 4, 5.

**Placeholder scan:** none. The only judgement calls are mechanical and bounded: Task 3's page insertions are anchored on a `grep` the implementer runs (`canDelete &&`), and the compiler flags any import slip.

**Type consistency:** `EmailStatus`/`EmailStatusFields`/`EmailRecipientStatus` (Task 1) are consumed unchanged by `DocumentSendRecord` (Task 2), `UserInvite` (Task 4), `PortalUser` (Task 5) and `TenantInvite` (Task 6); `documentSendsKey` is defined in Task 2 and used identically by the dialog and PO page in Task 3; the wire fields match the backend plan's Task 5 contract exactly (`emailStatus`, `emailStatusAt`, `emailStatusMessage`, `emailRecipients[{email,status}]`).
