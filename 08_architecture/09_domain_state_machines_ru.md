# Domain State Machines

Обновлено: 2026-06-21

## Назначение

Этот документ задает минимальные state machines, без которых нельзя начинать master specification, schema design или feature backlog для checkout, inventory, tickets, scanner, refunds, payouts и reports.

Статусы ниже являются архитектурной нормой для спецификации. Реализация может уточнить названия, но не должна убирать ключевые состояния без отдельного decision record.

## Общие правила переходов

1. Любой mutating transition должен иметь idempotency key, request ID, actor, interface type, organization/sales channel/service account context и audit entry.
2. Переходы, влияющие на inventory, payment, ticket validity, external allocation, scanner authority, payout or settlement, должны публиковать domain event через outbox.
3. Payment provider calls не выполняются внутри inventory lock.
4. Scanner и external systems получают изменения через signed events/webhooks или approved import batches, не через прямое чтение core tables.
5. Recovery/manual-review state обязателен для неоднозначных платежных, refund, scan, reconciliation и payout ситуаций.

## Reservation

States:

```text
draft
active
expired
converted_to_order
cancelled
release_pending
release_failed
```

Core transitions:

| From | To | Trigger | Inventory Impact | Event |
|---|---|---|---|---|
| draft | active | reservation create succeeds | reserve seats/capacity | reservation.created |
| active | active | reservation update succeeds | point update reserve/release | reservation.updated |
| active | expired | TTL passes | mark for release | reservation.expired |
| expired | release_pending | release job starts | release seats/capacity | inventory.release_requested |
| release_pending | cancelled | release succeeds | capacity available | inventory.released |
| release_pending | release_failed | release fails | no silent release | inventory.release_failed |
| active | converted_to_order | order created | keep hold for order/payment | order.created |
| active | cancelled | customer/system cancels | release seats/capacity | reservation.cancelled |

Hard invariants:

- A seat/capacity unit can belong to only one active reservation/order allocation at a time.
- Expired reservation cannot be paid directly unless deterministic recovery policy allows manual review or re-reservation.
- Reservation TTL is configurable by event/session/sales channel.

## CheckoutSession

States:

```text
created
pricing_confirmed
payment_started
completed
abandoned
expired
manual_review
```

Core transitions:

| From | To | Trigger | Payment Impact | Event |
|---|---|---|---|---|
| created | pricing_confirmed | platform totals calculated | no provider call | checkout.pricing_confirmed |
| pricing_confirmed | payment_started | payment intent created | provider intent reference created | payment.intent_created |
| payment_started | completed | order paid and tickets issued | capture/confirm recorded | checkout.completed |
| created/pricing_confirmed | expired | checkout TTL passes | no capture | checkout.expired |
| payment_started | manual_review | payment result ambiguous | hold provider result | checkout.manual_review |
| created/pricing_confirmed | abandoned | user abandons | release via reservation policy | checkout.abandoned |

Hard invariants:

- Platform totals are source of truth.
- Client-side or WordPress-side recalculation cannot confirm checkout.
- Mandatory fees must be represented before payment confirmation according to compliance region policy.

## Order

States:

```text
created
awaiting_payment
paid
partially_refunded
refunded
cancelled
failed
manual_review
chargeback_open
chargeback_lost
chargeback_won
```

Core transitions:

| From | To | Trigger | Inventory/Ticket Impact | Event |
|---|---|---|---|---|
| created | awaiting_payment | checkout confirms order | reservation remains held | order.created |
| awaiting_payment | paid | payment succeeded | issue tickets | order.paid |
| awaiting_payment | failed | payment failed/expired | release by policy | order.failed |
| created/awaiting_payment | cancelled | user/operator cancels | release by policy | order.cancelled |
| paid | partially_refunded | partial refund succeeds | selected tickets refunded/revoked | order.partially_refunded |
| paid/partially_refunded | refunded | full refund succeeds | all refundable tickets revoked | order.refunded |
| paid/partially_refunded | chargeback_open | dispute received | risk/ledger hold | dispute.created |
| chargeback_open | chargeback_lost | provider decision | payout reserve/liability impact | dispute.lost |
| chargeback_open | chargeback_won | provider decision | release dispute reserve | dispute.won |
| any | manual_review | ambiguous state | no silent mutation | order.manual_review |

Hard invariants:

- Paid order cannot be deleted.
- Order status cannot imply ticket validity without checking ticket state.
- Chargebacks and refunds are separate business flows.

## PaymentIntent And Capture

PaymentIntent states:

```text
created
requires_action
processing
authorized
succeeded
failed
cancelled
expired
manual_review
```

PaymentCapture states:

```text
not_required
pending
captured
failed
reversed
manual_review
```

Hard invariants:

- SCA/3DS challenge state must be representable for EEA card flows.
- Provider webhook is not trusted until signature and idempotency checks pass.
- Provider IDs are references, not primary platform IDs.
- Payment success after reservation expiry goes to deterministic recovery: revalidate inventory, issue if safe, refund, or manual review.

## Refund

States:

```text
requested
approved
rejected
provider_pending
succeeded
failed
cancelled
manual_review
```

Core transitions:

| From | To | Trigger | Inventory/Ticket Impact | Event |
|---|---|---|---|---|
| requested | approved | authorized approval | no provider call yet | refund.approved |
| requested | rejected | policy/role rejects | none | refund.rejected |
| approved | provider_pending | provider refund started | tickets pending revoke/refund policy | refund.provider_pending |
| provider_pending | succeeded | provider confirms | tickets refunded/revoked; release inventory if policy allows | refund.succeeded |
| provider_pending | failed | provider fails | no silent ticket change | refund.failed |
| any | manual_review | ambiguity or exception | freeze automation | refund.manual_review |

Hard invariants:

- Scanned tickets cannot be silently refunded/revoked.
- Refund approval policy must consider organizer policy, event cancellation, payment provider constraints, scan state, ticket transfer state and chargeback status.
- AI support may draft recommendation, but cannot execute refund without approved policy and audit.

## Ticket

States:

```text
issued
delivered
valid
used
revoked
refunded
cancelled
replaced
expired
manual_review
```

Core transitions:

| From | To | Trigger | Scanner Impact | Event |
|---|---|---|---|---|
| issued | delivered | delivery succeeds | no validity change | ticket.delivered |
| issued/delivered | valid | activation policy satisfied | scanner can accept | ticket.activated |
| valid | used | scan accepted | duplicate scans rejected/marked | scan.accepted |
| valid | revoked | revoke/refund/cancel | scanner must reject after sync | ticket.revoked |
| valid | refunded | refund succeeds | scanner must reject after sync | ticket.refunded |
| valid | replaced | replacement issued | old credential invalidated | ticket.replaced |
| any | manual_review | conflict/ambiguous scan/refund | supervisor policy | ticket.manual_review |

Hard invariants:

- Ticket is the entitlement. Credential is the presentation/verification artifact.
- Ticket validity must not depend on WordPress/local mirror state.
- Complimentary, replacement and paid tickets share validation semantics but differ by issue mode/reporting.

## TicketCredential

Credential types:

```text
static_qr
rotating_qr
pdf_print
apple_wallet_pass
google_wallet_pass
nfc_pass
manual_guest_list
external_barcode
```

States:

```text
created
active
rotating
suspended
revoked
expired
replaced
external_imported
manual_review
```

Hard invariants:

- Raw barcode values are sensitive and masked by default.
- Raw barcode string is not globally unique without authority/namespace context.
- Rotating credentials require server/scanner validation policy and offline fallback decision.
- Wallet/NFC credentials must be revocable or at least marked invalid in scanner authority state.

## ComplimentaryIssuance

States:

```text
draft
issued
partially_revoked
revoked
delivery_pending
delivery_failed
completed
manual_review
```

Hard invariants:

- No payment provider transaction is created for no-payment issuance.
- Assigned-seat complimentary tickets consume selected seats.
- General-admission complimentary tickets consume capacity.
- Reason/campaign, issuing user, organization, interface, sales channel/app credential and audit trail are mandatory.
- Batch issuance must be idempotent.

## ExternalAllocation

States:

```text
draft
approved
sent
acknowledged
active
partially_returned
returned
expired
reconciliation_pending
reconciled
disputed
cancelled
```

Hard invariants:

- External allocation reduces platform-sellable inventory while active.
- External sales consume from allocated quota, not from native platform checkout inventory.
- Oversales create reconciliation exceptions, not silent capacity increase.
- Assigned-seat external allocations require exact seat mapping or explicit category/quantity fallback.

## ExternalBarcodeBatch

States:

```text
uploaded
parsed
validation_failed
review_required
approved
activated
revoked
replaced
archived
```

Hard invariants:

- Activated batches are immutable except through explicit replacement/revocation batch.
- Batch is scoped to authority, event/session, source artifact, checksum and approving actor.
- Scanner may accept approved external barcodes before financial reconciliation only if policy allows.

## ScanDecision

States:

```text
accepted
duplicate
rejected
ambiguous
needs_supervisor_review
offline_accepted_pending_sync
offline_rejected_pending_sync
manual_override
sync_conflict
```

Hard invariants:

- Ambiguous barcode collisions must not grant entry automatically.
- Offline accepted scans must sync back with device/user/gate/timestamp and conflict policy.
- Duplicate scan cooldown is local scanner behavior; canonical duplicate resolution is platform/scanner authority behavior.

## Payout

States:

```text
not_eligible
scheduled
held
reserve_held
released
paid_out
failed
reversed
manual_review
```

Hard invariants:

- Payout is controlled by `PayoutPolicy`, not direct payment provider success alone.
- New/untrusted organizers default to delayed payout or reserve policy.
- Chargebacks, event cancellation risk and refunds can hold or reverse payout eligibility.
- Stripe Connect or another provider is an adapter, not the risk policy owner.

## Invoice

States:

```text
draft
approved
sent
collection_pending
paid
failed
voided
uncollectible
credited
manual_review
```

Hard invariants:

- Service billing invoice is separate from buyer ticket payment.
- Invoice lines reference approved tariff versions and reproducible usage records.
- Provider invoice IDs are references, not internal source of truth.

## Events Required By Master Specification

The master specification must include at least these event families:

```text
reservation.*
checkout.*
order.*
payment.*
refund.*
ticket.*
ticket_credential.*
complimentary_ticket.*
external_allocation.*
external_ticket_import.*
external_reconciliation.*
scan.*
payout.*
billing.*
report.*
```

## Specification Readiness Checklist

Before master specification is generated:

- Each state machine has accepted states and transitions.
- Each transition has owner, permission, idempotency and audit behavior.
- Inventory-affecting transitions define release/hold semantics.
- Payment/refund transitions define provider webhook behavior and manual review.
- Scanner transitions define online/offline and authority behavior.
- Payout transitions define reserve/risk behavior.
