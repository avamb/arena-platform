# Critical Architecture Audit

Обновлено: 2026-06-21

Назначение: зафиксировать критические архитектурные находки перед следующим заходом. Этот файл должен читаться после `00_AGENT_GUARDRAILS.md` и `01_CLARIFICATION_REGISTER.md`, до генерации master specification, feature backlog или начала coding phase.

## Executive Verdict

Текущая архитектура задает правильные границы: Bil24-compatible gateway отдельно от core, payment layer отдельно от ticketing domain, scanner как внешний boundary, event backbone, external allocations, superadmin/observability.

Но архитектура пока не готова к реализации. Это production-grade рамка с большим числом blocking decisions, внутренними конфликтами в финансовой и MVP-стратегии, а также недоописанными state machines, compliance и ticket credential model.

До исправления P0 пунктов coding phase должен оставаться заблокированным.

## P0 Blockers

### P0-1. Coding Phase Must Remain Blocked

Симптом:
- `00_AGENT_GUARDRAILS.md` запрещает начинать coding phase до подтвержденной master specification и initial feature backlog.
- `01_CLARIFICATION_REGISTER.md` содержит множество blocking-вопросов: DB, ID strategy, tenancy, auth, payments, refunds, WordPress, scanner, billing, observability, role mapping.
- Backend language/framework family is now accepted as Go, but exact Go scaffold tooling still needs confirmation before coding.

Риск:
- AutoForge или разработчик начнет scaffold/schema/business logic на неподтвержденных assumptions.
- Потом придется ломать фундаментальные модели: IDs, tenancy, auth, payment/refund flow, inventory, scanner integration.

Что исправить:
- Создать `Decision Log / ADR` для P0 решений.
- Разделить вопросы на:
  - `must answer before any code`
  - `must answer before schema`
  - `can proceed with explicit placeholder`
  - `later product detail`
- Не начинать implementation plan, пока P0 decisions не закрыты человеком.

Документы для правки:
- `09_autoforge/01_CLARIFICATION_REGISTER.md`
- `08_architecture/11_architecture_decision_log_ru.md`
- `08_architecture/12_master_platform_specification_ru.md`

### P0-2. Financial Architecture Conflict: Instant Payouts vs Risk Control

Симптом:
- `ticketing_market_research_ru.md` продвигает Stripe Connect instant/direct payouts как сильное УТП.
- `production_strategy_risks_ru.md` прямо говорит, что бесконтрольные instant payouts создают риск банкротства организатора, chargebacks и кассового разрыва платформы.
- `00_backend_architecture_brief_ru.md` перечисляет `Payout`, `Settlement`, `Dispute`, но не задает reserve/escrow/risk policy model.

Риск:
- Платформа может юридически и финансово отвечать за refund/chargeback, даже если деньги уже ушли организатору.
- Отмена события после выводов средств может создать непокрытый liability.
- Stripe Connect сам по себе не снимает риск платформы, если platform account остается ответственным за disputes/negative balances по выбранной модели.

Что исправить:
- Запретить в архитектуре формулировку "instant payouts by default".
- Ввести explicit payout risk subsystem:
  - `OrganizerRiskProfile`
  - `KYB/KYCStatus`
  - `PayoutPolicy`
  - `RollingReserve`
  - `ReserveLedger`
  - `PayoutSchedule`
  - `EventCancellationLiability`
  - `ChargebackReserve`
  - `NegativeBalanceRecovery`
- Payout policy должна зависеть от organizer risk, event risk, country, payment method, refund window, chargeback exposure и history.

Документы для правки:
- `08_architecture/00_backend_architecture_brief_ru.md`
- `08_architecture/06_event_notifications_billing_reporting_ru.md`
- `02_product_and_market_research/ticketing_market_research_ru.md`
- `02_product_and_market_research/production_strategy_risks_ru.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`

Decision needed:
- Первый production mode: instant payout, delayed payout, rolling reserve, escrow-like hold, tranche payout, or hybrid?

Recommended default:
- Hybrid payout policy. New/untrusted organizers use delayed payout plus reserve. Trusted organizers can receive faster payouts after KYB/KYC, risk score, signed agreement, and event history.

### P0-3. First Market Scope Conflict: GA + Scanner vs Assigned Seating

Симптом:
- Risk review рекомендует первым делом делать reliable offline scanner и universal external barcode import.
- Risk review рекомендует отказаться от сложных seating charts в первом этапе и сфокусироваться на General Admission.
- Clarification register предлагает first default: general admission + assigned seats, mixed later.
- Scanner architecture говорит, что first implementation can use platform online validation plus imported barcode batches, while preserving offline path.

Риск:
- Первый production scope станет слишком широким: seating editor, seat map rendering, seat status cache, assigned-seat external allocations, orphan-seat rules.
- При этом самая сильная market-fit боль текущего сегмента - multi-seller quotas and one scanner - может уйти во второй план.

Что исправить:
- Зафиксировать first production profile:
  - General Admission first.
  - External barcode import first.
  - Scanner offline-capable architecture first.
  - Assigned seating modeled in schema, but implementation deferred unless current launch site requires it.
- SeatingPlanVersion и SeatStatus оставить в архитектуре сразу, но не делать full seating editor P0.

Документы для правки:
- `08_architecture/04_large_venue_performance_strategy_ru.md`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `08_architecture/07_external_allocations_scanner_ingestion_ru.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`

Decision needed:
- First release admission mode: GA only, GA + assigned seats, or site-driven exception?

Recommended default:
- GA first, with assigned seating preserved as data model and compatibility path.

### P0-4. Missing Formal State Machines

Симптом:
- Architecture mentions lifecycle/status/events, but does not define complete state transition tables.
- Refund workflow remains blocking.

Affected state machines:
- `Reservation`
- `CheckoutSession`
- `Order`
- `PaymentIntent`
- `PaymentCapture`
- `Refund`
- `Ticket`
- `TicketCredential`
- `ComplimentaryIssuance`
- `ExternalAllocation`
- `ExternalBarcodeBatch`
- `ScanDecision`
- `Payout`
- `Invoice`

Риск:
- Double-sell, duplicate tickets, stale reservations, refunds after scan, payment success after reservation expiry, inconsistent inventory release, non-idempotent webhooks.

Что исправить:
- Для каждой state machine добавить:
  - states
  - allowed transitions
  - actor/interface allowed to trigger transition
  - permission required
  - idempotency key scope
  - inventory impact
  - payment/ledger impact
  - emitted domain event
  - webhook side effects
  - recovery/manual-review state

Документы для правки:
- `08_architecture/09_domain_state_machines_ru.md`
- `08_architecture/00_backend_architecture_brief_ru.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`

### P0-5. Compliance Is Not Yet Architecture

Симптом:
- Architecture contains payment totals and masking rules, but lacks dedicated compliance architecture.
- Missing explicit acceptance criteria for:
  - all-in pricing / transparent fees
  - PCI scope / CDE boundary
  - SCA / 3DS for EEA card payments
  - GDPR/privacy/data-subject rights
  - WCAG/accessibility target
  - tax/fee presentation
  - marketing pixels consent

Market/legal baseline:
- EU Accessibility Act applies to e-commerce services provided to consumers after 2025-06-28.
- Stripe SCA guidance requires SCA-ready payment flows for impacted EEA card payments.
- PCI DSS applies to entities that store, process, transmit, or can impact cardholder data/security.
- OWASP API Security Top 10 includes broken object/function authorization, unrestricted resource consumption, and unrestricted access to sensitive business flows.

Риск:
- Checkout can be non-compliant in the US/EU.
- Embedded checkout/payment flow can accidentally expand PCI scope.
- Marketing pixels can leak personal/order/payment data without consent model.
- Public checkout may fail accessibility requirements and lose market access.

Что исправить:
- Add dedicated compliance architecture:
  - `PricingDisplayPolicy`
  - `FeeTaxBreakdown`
  - `ComplianceRegion`
  - `PaymentComplianceProfile`
  - `PCIBoundary`
  - `SCAChallengeFlow`
  - `ConsentRecord`
  - `MarketingPixelPolicy`
  - `DataSubjectRequest`
  - `DataRetentionPolicy`
  - `AccessibilityStandard`
- Define checkout invariant: first price display must include all mandatory platform/organizer/service fees where required by region.
- Target WCAG 2.2 AA for public checkout and buyer-facing flows unless legal review selects another explicit target.

Документы для правки:
- `08_architecture/10_compliance_security_privacy_ru.md`
- `08_architecture/02_wordpress_integration_contract_ru.md`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `09_autoforge/01_CLARIFICATION_REGISTER.md`

## P1 High-Risk Gaps

### P1-1. Ticket Credential Model Is Too Thin

Симптом:
- Architecture mostly says tickets/barcodes.
- Market research treats Apple Wallet, Google Wallet, NFC, rotating QR, screenshot protection, PDF/print and offline mobile tickets as core market expectations.

Риск:
- Scanner and ticket delivery get hard-coded around static QR/PDF.
- Later wallet/NFC/rotating credentials require redesign of ticket issuance, revocation, scan validation and offline cache.

Что исправить:
- Introduce `TicketCredential` separate from `Ticket`.
- Suggested credential types:
  - `static_qr`
  - `rotating_qr`
  - `pdf_print`
  - `apple_wallet_pass`
  - `google_wallet_pass`
  - `nfc_pass`
  - `manual_guest_list`
  - `external_barcode`
- Model:
  - credential status
  - rotation policy
  - offline validation rules
  - revocation propagation
  - scanner display policy
  - raw barcode masking/hash policy

Документы для правки:
- `08_architecture/07_external_allocations_scanner_ingestion_ru.md`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `08_architecture/09_domain_state_machines_ru.md`

### P1-2. API Security Needs Threat Model And Test Matrix

Симптом:
- RBAC/ABAC, scoped credentials and idempotency are mentioned.
- Missing explicit API threat model and test obligations for object-level, property-level, and function-level authorization.

Риск:
- Multi-tenant data leaks through IDs.
- External apps can mutate resources outside their organization/sales channel.
- Ticket-buying flows can be abused by bots without implementation bugs.

Что исправить:
- Add API security section:
  - object-level authorization tests for every endpoint with `{id}`
  - property-level authorization for PATCH/update
  - function-level tests for admin/support/superadmin boundaries
  - rate limits by IP/account/session/sales channel/event
  - bot/hot-sale controls
  - credential rotation and revocation
  - webhook endpoint allowlist and secret rotation
  - API inventory/versioning

Документы для правки:
- `08_architecture/03_platform_management_api_and_permissions_ru.md`
- `08_architecture/08_platform_superadmin_observability_ru.md`
- `08_architecture/10_compliance_security_privacy_ru.md`

### P1-3. Data Privacy And Retention Are Under-Specified

Симптом:
- Logs/sensitive data masking exists.
- Retention is an open question.
- No data subject rights or deletion/anonymization model.

Риск:
- Reports, scan logs, buyer identities, imported external reports and marketing exports can accumulate personal data without lifecycle control.

Что исправить:
- Define:
  - data classification
  - retention by object family
  - audit log immutability vs personal data minimization
  - raw barcode retention
  - buyer PII masking
  - data export/delete/anonymize request flow
  - processor/controller assumptions for organizers and external platforms

Документы для правки:
- `08_architecture/10_compliance_security_privacy_ru.md`
- `08_architecture/08_platform_superadmin_observability_ru.md`
- `08_architecture/06_event_notifications_billing_reporting_ru.md`

### P1-4. Discovery And Marketing Are Not Balanced Against Data Sovereignty

Симптом:
- Strategy positions platform as sovereign B2B partner.
- Risk review notes that pure SaaS without discovery engine is weak for many small/immigrant organizers.
- Architecture has social publishing, but not event discovery/marketplace/referral/waitlist as a first-class product boundary.

Риск:
- Product may solve checkout/backoffice but fail to drive demand for organizers who lack owned audience.

Что исправить:
- Decide whether first production includes:
  - public event discovery index
  - organizer-owned white-label only
  - hybrid discovery opt-in
  - referral/affiliate channels
  - waitlist and transfer/resale policy

Документы для правки:
- `02_product_and_market_research/ticketing_market_research_ru.md`
- `08_architecture/05_interface_taxonomy_and_complimentary_tickets_ru.md`
- `08_architecture/06_event_notifications_billing_reporting_ru.md`

## Required Next-Session Workflow

1. Read:
   - `09_autoforge/00_AGENT_GUARDRAILS.md`
   - `09_autoforge/01_CLARIFICATION_REGISTER.md`
   - this file
   - `09_autoforge/03_SPECIFICATION_STARTER.md`
   - `08_architecture/11_architecture_decision_log_ru.md`
   - `08_architecture/09_domain_state_machines_ru.md`
   - `08_architecture/10_compliance_security_privacy_ru.md`
   - `08_architecture/12_master_platform_specification_ru.md`
2. Close P0 decisions with owner-confirmed answers.
3. Update architecture docs with decisions, not just proposed defaults.
4. Reconcile product strategy:
   - instant payouts vs controlled payout risk
   - GA-first scanner-led launch vs assigned seating scope
   - B2B data sovereignty vs discovery engine need
5. Fill `08_architecture/12_master_platform_specification_ru.md` using confirmed decisions and explicitly marked proposed defaults.
6. Only after that produce feature backlog seed and implementation tickets.

## External Baseline References

- PCI DSS: https://www.pcisecuritystandards.org/standards/pci-dss/
- Stripe SCA: https://docs.stripe.com/strong-customer-authentication
- EU Accessibility Act: https://eur-lex.europa.eu/eli/dir/2019/882/oj
- WCAG 2.2: https://www.w3.org/TR/WCAG22/
- EU data protection / GDPR framework: https://commission.europa.eu/law/law-topic/data-protection/legal-framework-eu-data-protection_en
- OWASP API Security Top 10 2023: https://owasp.org/API-Security/editions/2023/en/0x11-t10/

## Done Criteria For This Audit File

This audit is resolved only when:

- P0 blockers are converted into accepted architecture decisions.
- Contradictory product strategy statements are corrected or explicitly marked obsolete.
- State machines exist for reservation/order/payment/refund/ticket/scanner/payout.
- Compliance/security/privacy doc exists and is referenced by guardrails.
- First production scope is explicitly stated and matches market strategy.
- AutoForge can generate backlog without guessing DB, framework, ID, tenancy, payment, scanner, compliance, or role boundaries.

Second-pass status on 2026-06-21:

- Supporting files now exist for state machines, compliance/security/privacy, architecture decisions, specification starter and master specification scaffold.
- Audit remains unresolved until owner-confirmed decisions close the P0 blockers.
