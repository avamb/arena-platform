# Compliance, Security, And Privacy Architecture

Обновлено: 2026-06-21

## Назначение

Этот документ превращает рыночные, платежные, security и privacy требования в архитектурные ограничения для master specification. Он не заменяет юридическую консультацию, но задает технические invariants, которые нельзя игнорировать при проектировании checkout, API, WordPress plugin, scanner, reports, billing и superadmin console.

## External Baseline

Проверенные источники на дату обновления:

- PCI DSS: https://www.pcisecuritystandards.org/standards/pci-dss/
- Stripe Strong Customer Authentication: https://docs.stripe.com/strong-customer-authentication
- EU Accessibility Act, Directive (EU) 2019/882: https://eur-lex.europa.eu/eli/dir/2019/882/oj
- WCAG 2.2: https://www.w3.org/TR/WCAG22/
- EU GDPR legal framework: https://commission.europa.eu/law/law-topic/data-protection/legal-framework-eu-data-protection_en
- OWASP API Security Top 10 2023: https://owasp.org/API-Security/editions/2023/en/0x11-t10/

## Non-Negotiable Architecture Rules

1. Public checkout must present platform-calculated totals and mandatory fees according to region policy before payment confirmation.
2. Platform must minimize PCI scope by default: card data should be collected by payment provider hosted/embedded components, not by WordPress or platform app servers.
3. EEA card payments must support SCA/3DS states in the payment state machine.
4. Buyer-facing checkout and account flows should target WCAG 2.2 AA unless a later legal review chooses a different explicit target.
5. Marketing pixels and social publishing must not receive private order/payment/ticket/scan data by default.
6. Personal data must be classified, minimized, retained by policy, exportable where required and deletable/anonymizable where legally allowed.
7. API design must include object-level, property-level and function-level authorization tests.
8. High-demand ticket-buying flows must include abuse protection, not only code-level authorization.

## Required Concepts

```text
ComplianceRegion
PricingDisplayPolicy
FeeTaxBreakdown
PaymentComplianceProfile
PCIBoundary
SCAChallengeFlow
ConsentRecord
MarketingPixelPolicy
DataClassification
DataRetentionPolicy
DataSubjectRequest
PrivacyExport
PrivacyDeletionOrAnonymization
AccessibilityStandard
SecurityControlProfile
ApiAbusePolicy
CredentialRotationPolicy
```

## Pricing And Fees

Architecture invariant:

```text
displayed_total = ticket_price + mandatory_platform_fees + mandatory_organizer_fees + mandatory_channel_fees + applicable mandatory charges
```

Rules:

- Platform owns `sum`, `discount`, `charge`, `fee`, `tax`, `total` semantics.
- WordPress, widgets, partner apps and compatibility gateway cannot recalculate final payable total.
- Region-specific price display policy must be explicit.
- Optional add-ons can be separate, but mandatory fees must not appear late in checkout where region policy requires upfront display.
- Reports must preserve both buyer-facing total and accounting breakdown.

Required specification outputs:

- fee taxonomy
- tax handling placeholder
- all-in display policy by region
- buyer receipt fields
- admin/reporting fields

## PCI And Payment Security Boundary

Preferred default:

- platform-hosted checkout or payment-provider hosted/embedded component
- no card number, CVC or sensitive authentication data stored in platform or WordPress
- provider tokens/IDs stored as references only
- payment webhooks verified, idempotent and replay-protected

Must define:

- which systems can affect cardholder data environment
- whether embedded checkout changes PCI responsibilities
- whether POS terminal/card-present flow is provider-managed
- secrets storage and rotation
- payment provider webhook signing
- least-privilege credentials per environment

## SCA / 3DS

Payment state machine must represent:

```text
requires_action
processing
authorized
succeeded
failed
manual_review
```

Rules:

- Client can return from 3DS/SCA challenge without losing reservation context.
- Payment success after reservation expiry goes through deterministic recovery.
- Failed or abandoned SCA does not issue tickets.
- Provider-specific SCA fields do not leak into core model as primary concepts.

## Accessibility

Target default:

```text
WCAG 2.2 AA for public checkout, event pages controlled by platform, hosted checkout, embedded checkout, buyer order status pages and critical email/PDF ticket flows where applicable.
```

Architecture requirements:

- keyboard-accessible checkout
- focus management for modals/embedded checkout
- accessible seat/category selection fallback
- clear errors and recovery
- no color-only status indicators
- timeouts visible and extendable where feasible
- PDF/ticket delivery must include accessible alternatives where possible
- locale and RTL support must not break accessibility

## Privacy And Personal Data

Data classification:

```text
public_catalog
business_operational
buyer_personal
payment_reference
ticket_credential_sensitive
scanner_operational
external_import_sensitive
audit_immutable
secret
```

Rules:

- Buyer PII must be minimized by interface type.
- Guest checkout must not force unnecessary account creation.
- Raw barcode values are sensitive.
- Raw logs must mask secrets, payment data, personal data and raw barcodes by default.
- Report delivery must not merge recipients by equal email unless identity mapping confirms same person/organization relationship.
- External imported reports must retain source artifact, checksum, parser/model version, reviewer and confidence metadata.

Data subject workflows to model:

- export personal data
- correct personal data
- delete/anonymize where legally allowed
- suppress marketing
- revoke consent
- audit refusal where legal retention prevents deletion

## Consent And Marketing Pixels

Rules:

- Marketing pixels are explicit subscribers with scoped event allowlist.
- Pixel payloads must use minimized data.
- Private order/payment/refund/ticket/scan data must not be published to social channels.
- Consent state must be checked before non-essential tracking where required.
- Purchase analytics must preserve accurate amount/currency but avoid raw personal data unless policy allows.

Required entities:

```text
ConsentRecord
TrackingPreference
MarketingDestination
PixelEventPolicy
```

## API Security

OWASP API risk coverage required in specification:

- object-level authorization for every endpoint that accepts resource IDs
- object-property authorization for create/update/patch payloads
- function-level authorization for admin, support and superadmin actions
- resource consumption limits for checkout, search, webhook replay, imports and scanner sync
- sensitive business flow protection for reservation/order/ticket purchase flows
- SSRF controls for connector/MCP/external URL ingestion
- security configuration baseline by environment
- API inventory and versioning
- safe consumption of third-party APIs and webhooks

Required tests:

```text
cross_tenant_read_denied
cross_tenant_write_denied
field_overpost_denied
superadmin_read_without_write
support_impersonation_audited
expired_credential_denied
revoked_credential_denied
rate_limit_enforced
idempotency_replay_safe
webhook_signature_required
```

## Abuse, Bots, And Hot Sale

Required concepts:

```text
ApiAbusePolicy
HotSalePolicy
WaitingRoomToken
PurchaseLimitPolicy
RateLimitBucket
BotRiskSignal
SuspiciousActivityEvent
```

Rules:

- Purchase limits must be event/session/sales-channel aware.
- Waiting room/token gate must be possible before seat selection or reservation.
- Rate limits apply by IP, account, anonymous session, sales channel and event/session where relevant.
- Bot protection must not make accessibility impossible.

## Secrets And Credentials

Rules:

- WordPress plugin stores only scoped rotatable channel credentials.
- No global platform admin token in WordPress or frontend HTML.
- Webhook secrets are per endpoint/channel and rotatable.
- Service accounts have explicit scopes.
- Credential reads/changes are audited.
- Superadmin credential actions require step-up policy.

## Retention Defaults To Confirm

Proposed default categories:

| Data | Proposed Retention Direction |
|---|---|
| audit log | long retention, immutable where legally allowed |
| operational logs | shorter retention, masked by default |
| payment provider refs | retained for accounting/dispute period |
| raw barcode values | shortest feasible, hash/reference for reports |
| scan events | hot window plus archived aggregate/reporting |
| buyer PII | retain by ticket/support/accounting need, minimize after event |
| external source artifacts | retain while reconciliation/reporting/audit needs exist |
| analytics/pixel events | consent-bound and minimized |

## Master Specification Must Answer

1. Which compliance regions are supported in first production?
2. What is the exact all-in pricing display invariant for first region?
3. Which checkout mode minimizes PCI scope for first launch?
4. Which payment provider handles SCA/3DS first?
5. What accessibility target is accepted?
6. What personal data is collected in guest checkout?
7. What consent is required for marketing pixels?
8. What is the retention policy by data class?
9. What are mandatory API security tests?
10. What abuse controls are active for first launch?
