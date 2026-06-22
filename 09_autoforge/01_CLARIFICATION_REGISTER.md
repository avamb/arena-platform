# Clarification Register

Обновлено: 2026-06-21

Этот документ фиксирует вопросы, которые AutoForge должен уточнить до генерации полной спецификации и feature backlog. Вопросы можно закрывать постепенно. Если вопрос помечен как blocking, реализация соответствующей области не начинается до ответа.

Second-pass note: вопросы Q64+ добавлены после повторного архитектурного аудита. Их нужно закрыть или явно принять как proposed defaults перед заполнением `../08_architecture/12_master_platform_specification_ru.md`.

## 1. Product Scope

### Q1. Какие интерфейсы строим первыми?

Decision needed: порядок первых интерфейсов.

Recommended default: backend core + platform admin API + organizer web console skeleton + WordPress plugin contract, while reserving explicit architecture for box office/POS, external ticketing systems, agent sales interfaces, scanner service, and developer/API console.

Impact if different: backlog и API приоритеты изменятся.

Blocking level: blocking для master feature backlog.

### Q2. Какие два текущих сайта нужно мигрировать?

Decision needed: назвать оба сайта и их различия.

Recommended default: Vino&Co как primary reference, второй сайт добавить после аудита.

Impact if different: WordPress plugin requirements могут отличаться.

Blocking level: can proceed with placeholder.

## 2. Technology Stack

### Q3. Подтверждаем PostgreSQL как основную DB?

Decision needed: DB stack.

Recommended default: PostgreSQL core, JSONB для гибких свойств, Redis для cache/locks, object storage для media/assets.

Impact if different: schema, migrations, HA, scaling меняются.

Blocking level: blocking before implementation.

### Q4. Какой backend framework выбираем?

Decision needed: язык и framework.

Decision status: accepted 2026-06-21.

Accepted decision: Go as primary backend language. Backend shape: lightweight `net/http`-based modular monolith first. Exact router/tooling remains a smaller scaffold decision; `chi` is the proposed default.

Required source: `../08_architecture/13_backend_go_initial_specification_ru.md`.

Impact if different: весь scaffold и hiring/support profile меняются.

Blocking level: resolved for language/framework family; exact router/migration/sql tooling remains blocking before scaffold.

### Q5. Какая стратегия ID?

Decision needed: BIGINT/Snowflake/UUIDv7/ULID.

Recommended default: public IDs as strings; internal IDs confirm separately.

Impact if different: API, DB indexes, imports and compatibility layer.

Blocking level: blocking before schema.

## 3. Tenancy And Organizations

### Q6. Модель tenant: organization, organizer, agent, sales channel?

Decision needed: hierarchy and ownership.

Recommended default: `Platform -> Organization -> Membership -> SalesChannel -> Event`.

Impact if different: permissions and reporting.

Blocking level: blocking before identity/schema.

### Q7. Может ли один пользователь состоять в нескольких организациях?

Decision needed: membership model.

Recommended default: yes, with per-organization roles.

Impact if different: auth/session/permissions simplify or change.

Blocking level: blocking before auth implementation.

## 4. Auth And Permissions

### Q8. Какие auth providers включаем первыми?

Decision needed: initial auth methods.

Recommended default: email without mandatory confirmation for checkout identity, password/magic link for admin, Telegram identity later.

Impact if different: frontend and security scope.

Blocking level: blocking before auth implementation.

### Q9. Кто может создавать country/city/venue?

Decision needed: self-service vs moderation policy.

Recommended default: organizers can create pending venues/cities if permission allows; operator can verify/merge.

Impact if different: management API and moderation queue.

Blocking level: blocking before management API implementation.

## 5. Venues And Seating Plans

### Q10. Требуется ли operator verification перед продажами на новой venue?

Decision needed: venue trust policy.

Recommended default: allow sales on organization-owned pending venue for low-risk general admission, require verification for public/canonical venue and complex seating.

Impact if different: launch speed vs data quality.

Blocking level: blocking for venue workflow.

### Q11. Какой формат seating plan geometry выбираем?

Decision needed: JSON geometry model, SVG source, or hybrid.

Recommended default: hybrid: structured JSON as source of truth, SVG/render assets as derived/imported representation.

Impact if different: editor, scanner, seat selection UI, imports.

Blocking level: blocking before seating editor/schema.

### Q12. Можно ли использовать чужой seating plan без согласия владельца?

Decision needed: visibility defaults.

Recommended default: only if `shared_read`, `public_template`, or `operator_verified`.

Impact if different: permissions and marketplace/library behavior.

Blocking level: blocking before seating permissions.

## 6. Events, Inventory, Reservations

### Q13. Какие типы admission поддерживаем первыми?

Decision needed: assigned seats, general admission, mixed.

Recommended default: general admission + assigned seats, mixed later unless current sites require it.

Impact if different: inventory schema and checkout UX.

Blocking level: blocking before inventory schema.

### Q14. Reservation TTL default?

Decision needed: hold duration.

Recommended default: 15-20 minutes configurable per event/session/sales channel.

Impact if different: checkout and inventory release jobs.

Blocking level: can proceed with configurable placeholder.

## 7. Checkout And Payments

### Q15. Первый checkout mode для WordPress?

Decision needed: hosted checkout, embedded checkout, or both.

Recommended default: hosted checkout first, embedded checkout second.

Impact if different: plugin complexity and PCI/payment surface.

Blocking level: blocking before WordPress plugin implementation.

### Q16. Первый payment provider?

Decision needed: provider to implement first.

Recommended default: choose based on current business need; do not hardcode AllPay into core.

Impact if different: payment adapter backlog.

Blocking level: blocking before payment implementation.

### Q17. Refunds: who can initiate and where?

Decision needed: refund workflow.

Recommended default: platform admin/organizer role can request refund; payment provider adapter executes; ticket status updated by platform.

Impact if different: permissions, ledger, ticket lifecycle.

Blocking level: blocking before refunds.

## 8. WordPress Plugin

### Q18. Нужен ли WooCommerce mirror после миграции?

Decision needed: keep WooCommerce orders or bypass WooCommerce fully.

Recommended default: bypass WooCommerce for new orders; keep historical WooCommerce orders read-only.

Impact if different: plugin scope grows significantly.

Blocking level: blocking before WordPress plugin backlog.

### Q19. Страницы мероприятий: CPT или чистый widget?

Decision needed: WordPress content model.

Recommended default: Custom Post Type `arena_event` for SEO-visible pages.

Impact if different: SEO, editor workflow, sync design.

Blocking level: blocking before WordPress plugin implementation.

## 9. Compatibility API

### Q20. Какой приоритет Bil24-compatible API?

Decision needed: first release or later.

Recommended default: later/parallel safety layer; not blocking for our two sites if new WordPress plugin is confirmed.

Impact if different: early backlog grows.

Blocking level: can proceed after priority decision.

### Q21. Какие Bil24 commands нужны для third-party compatibility?

Decision needed: command coverage.

Recommended default: derive from Vino&Co and current integrations, then prioritize.

Impact if different: gateway scope and tests.

Blocking level: blocking before compatibility implementation.

## 10. Scanner Service

### Q22. Какой текущий Bil24 -> scanner webhook contract нужно сохранить?

Decision needed: payloads, event types, auth/signature, retry/idempotency, ticket/barcode identifiers, event/session mapping, app/offline behavior, and scan result callbacks for the existing scanner system.

Recommended default: keep scanner as the existing independent system with application and `macs.arenasoldout.com` surface; replace Bil24 sync with signed platform webhook notifications that preserve the current operational behavior.

Impact if different: scanner app compatibility, event-day operations, barcode imports, webhook event catalog, and migration plan change.

Blocking level: can proceed with boundary, blocking before scanner sync implementation.

## 11. Deployment And Operations

### Q23. Где будет первый production deploy?

Decision needed: infrastructure target.

Recommended default: define before implementation; architecture should support Docker-based deployment.

Impact if different: CI/CD, secrets, HA.

Blocking level: blocking before deployment specs.

### Q24. HA модель: два сервера плюс witness или manual failover?

Decision needed: failover model.

Recommended default: PostgreSQL primary + hot standby, app active-active, third witness if automatic failover required.

Impact if different: operational safety and split-brain risk.

Blocking level: blocking before production infrastructure.

## 12. Documentation And AutoForge Operation

### Q25. На каком языке писать AutoForge spec?

Decision needed: Russian, English, or bilingual.

Recommended default: English for agent-facing implementation specs, Russian summary for owner-facing architecture notes.

Impact if different: maintainability and review clarity.

Blocking level: can proceed with chosen convention.

### Q26. Нужно ли AutoForge сначала создать только skeleton без бизнес-логики?

Decision needed: first coding milestone.

Recommended default: after approved spec, create skeleton with architecture boundaries, test setup, lint/build, DB migrations baseline, no complex domain logic.

Impact if different: risk of premature domain decisions.

Blocking level: blocking before first coding run.

## 13. Large Venue Performance

### Q27. Какой performance profile задаем для первого production release?

Decision needed: small, medium, large или только архитектурная заготовка.

Recommended default: first release targets Small Profile, while architecture must preserve path to Medium/Large.

Impact if different: cache, status payloads, load tests and infrastructure scope change.

Blocking level: can proceed with architecture guardrail, blocking before performance test spec.

### Q28. Нужен ли actual SVG layout в первой версии?

Decision needed: SVG actual layout, schema JSON renderer, or both.

Recommended default: schema JSON as source of truth; SVG/render assets can be precomputed where needed.

Impact if different: seating editor, frontend renderer, cache and CDN design change.

Blocking level: blocking before seating API/frontend implementation.

### Q29. Какой cache layer используем для dynamic seat status?

Decision needed: Redis, in-process cache, DB-backed cache, or dedicated inventory/status service.

Recommended default: Redis or equivalent external cache for production path; in-process only for early local development if clearly isolated.

Impact if different: HA, cache invalidation, point updates and hot-sale readiness.

Blocking level: blocking before inventory/status implementation.

### Q30. Нужен ли waiting room в первой версии?

Decision needed: implement now or reserve architecture only.

Recommended default: reserve architecture and API hooks now; implement waiting room when first high-demand event is planned.

Impact if different: frontend flow, rate limiting and deployment complexity.

Blocking level: can proceed with placeholder, blocking before high-demand launch.

## 14. Interface Taxonomy And Complimentary Tickets

### Q31. Какие interface families включаем в master specification как обязательные архитектурные классы?

Decision needed: подтвердить список interface families.

Recommended default: включить `platform_public_checkout`, `agent_sales_interface`, `box_office_pos`, `external_ticketing_system`, `organizer_backoffice`, `operator_backoffice`, `management_api_client`, `scanner_service`, `developer_api_console`.

Impact if different: sales channel model, permissions, payment ownership, reporting and API scopes изменятся.

Blocking level: blocking для master specification.

### Q32. Входит ли box office/POS в первый production release или только резервируется архитектурно?

Decision needed: первый scope кассы.

Recommended default: reserve architecture now; implement minimal POS later after core checkout/order/ticket issuance is stable, unless current operations require event-day cashier flow immediately.

Impact if different: early backlog должен включить shifts, cash/terminal payments, printing/fiscal data, cashier permissions, POS reports and refund/reprint flow.

Blocking level: can proceed with architecture placeholder, blocking before POS implementation.

### Q33. Какой режим external ticketing system нужен первым?

Decision needed: external systems use platform checkout/payment, own checkout with platform inventory/ticket issuance, or settlement-only reporting.

Recommended default: first support platform-owned inventory/ticket issuance with explicit sales channel scopes; external checkout/payment mode only after partner requirements are known.

Impact if different: API contracts, payment ownership, settlement, fraud/risk, and inventory allocation model change.

Blocking level: can proceed with architecture placeholder, blocking before external-ticketing-system implementation.

### Q34. Подтверждаем пригласительные билеты как простой organizer workflow без оплаты?

Decision needed: role and workflow baseline.

Recommended default: yes. Organizer owner/admin and event manager with `complimentary_ticket.issue.own_event` can issue invitation tickets for their own events from organizer backoffice; POS and operator can also issue where permissions allow.

Impact if different: permission model, audit requirements, inventory reporting and organizer UX change.

Blocking level: blocking before ticket issuance and organizer backoffice backlog.

### Q35. Должны ли пригласительные билеты иметь квоты/лимиты?

Decision needed: per-event, per-role, per-user, or no default quota.

Recommended default: support optional per-event and per-role limits, but do not require limits for the first internal trusted organizer flow. Always keep audit and reporting.

Impact if different: schema, permission checks, organizer UX and abuse prevention differ.

Blocking level: can proceed with configurable placeholder, blocking before batch issuance implementation.

### Q36. Какие delivery methods нужны для пригласительных первыми?

Decision needed: email, PDF download, manual print, SMS/WhatsApp, or later.

Recommended default: PDF download/manual print first; email delivery next; SMS/WhatsApp later.

Impact if different: notification service, templates, delivery tracking and support workflows change.

Blocking level: can proceed with placeholder, blocking before delivery implementation.

### Q37. Как работает отзыв пригласительного билета?

Decision needed: revoke policy and inventory release behavior.

Recommended default: organizer can revoke unscanned complimentary tickets for own events before event start; inventory is released if event/session policy allows it; scanned tickets cannot be silently revoked.

Impact if different: ticket lifecycle, scanner behavior, reporting and support policy change.

Blocking level: blocking before complimentary revocation implementation.

## 15. Notifications, Billing, And Reports

### Q38. Какие webhook/event subscribers нужны в первом production release?

Decision needed: подтвердить first-release subscribers.

Recommended default: WordPress/plugin cache invalidation, scanner service, payment/refund lifecycle, organizer backoffice notifications, and internal billing/report jobs first; social publishing and external partners can use the same architecture but may be enabled later.

Impact if different: event catalog, subscription filters, delivery retries, monitoring and security scope change.

Blocking level: blocking before webhook/event catalog backlog.

### Q39. Какой payload policy для webhooks?

Decision needed: full resource snapshot, lightweight notification, or mixed.

Recommended default: lightweight signed notification with resource IDs, event type, organization/sales channel, version, and timestamps; receiver fetches full resource when needed. Full payload only for small non-sensitive events or explicit integration contracts.

Impact if different: privacy, payload size, API load, cache invalidation and compatibility behavior change.

Blocking level: blocking before webhook implementation.

### Q40. Какие social publishing channels включаем первыми?

Decision needed: Telegram, Facebook/Instagram, generic webhook/export, or later.

Recommended default: generic social publishing job model now; implement Telegram or generic webhook/export first if operationally needed; Facebook/Instagram adapters later after channel requirements and permissions are known.

Impact if different: asset pipeline, approval workflow, localization, UTM/tracking and duplicate-post prevention change.

Blocking level: can proceed with architecture placeholder, blocking before social publishing implementation.

### Q41. Social publishing автоматический или approval-based?

Decision needed: auto-publish policy.

Recommended default: approval-based by default; allow automatic publishing only for explicitly configured channels/campaigns and public catalog events.

Impact if different: risk of accidental public posts, moderation workflow and audit requirements change.

Blocking level: blocking before social publishing implementation.

### Q42. Какие первые approved tariffs для service billing?

Decision needed: тарифная модель для инвойсов клиентам.

Recommended default: define tariff versions before implementation; support fixed monthly fee, per paid ticket fee, percentage of revenue, and custom contract fee as architecture options.

Impact if different: billing metrics, invoice lines, usage capture and reports change.

Blocking level: blocking before billing implementation.

### Q43. Stripe Billing используем для invoice sending и auto-collection в первой версии?

Decision needed: Stripe Billing role.

Recommended default: yes, Stripe Billing as first provider adapter for service invoices and card/bank collection, while internal billing records remain the source of truth.

Impact if different: provider abstraction, customer onboarding, invoice lifecycle, failed-payment workflow and accounting exports change.

Blocking level: blocking before Stripe billing implementation.

### Q44. Инвойсы автоматически списывать или сначала создавать draft?

Decision needed: collection policy.

Recommended default: support both; first operational mode should be draft invoice review for new clients, then auto-collection after billing agreement/payment method is confirmed.

Impact if different: finance approval workflow, dunning, customer communication and dispute handling change.

Blocking level: blocking before billing collection implementation.

### Q45. Когда генерировать post-event reports?

Decision needed: report cutoff.

Recommended default: generate initial report after event/session end plus configurable delay; generate finalized report after scan/refund/settlement cutoff if needed.

Impact if different: organizer expectations, refund visibility, scan/no-show accuracy and billing inputs change.

Blocking level: blocking before report generation implementation.

### Q46. Кому отправлять post-event reports и как дедуплицировать organizer/agent?

Decision needed: recipient resolution and deduplication policy.

Recommended default: send by verified user/organization relationship and report subscription; if same user or organization is both organizer and agent, send one package with both role sections. Do not merge only by equal email unless identity mapping confirms same recipient.

Impact if different: duplicate emails, missing role sections, privacy and support issues.

Blocking level: blocking before report delivery implementation.

## 16. Scanner Federation And External Allocations

### Q47. Какие barcode authorities должен поддерживать scanner в первой версии?

Decision needed: список источников билетов/баркодов.

Recommended default: existing scanner-supported Bil24 tickets first, then platform-issued tickets, complimentary tickets, imported legacy tickets, imported external-platform barcode batches, and manual guest lists. External online lookup can be reserved for later.

Impact if different: scanner contract, barcode namespace model, offline cache, import workflow and event-day support change.

Blocking level: blocking before scanner implementation.

### Q48. Можно ли принимать external barcode imports для сканирования до полной финансовой reconciliation?

Decision needed: operational policy for event-day validation.

Recommended default: yes, if the barcode batch is approved by an authorized organizer/operator and scoped to event/session/authority; financial status can remain reported/estimated until reconciliation.

Impact if different: event-day readiness, fraud risk, support burden and reporting labels change.

Blocking level: blocking before external barcode import implementation.

### Q49. Какие внешние платформы сейчас получают квоты?

Decision needed: first external platform targets and operational patterns.

Recommended default: collect current platform names, formats, quota workflows, and settlement expectations before implementation; design generic `ExternalPlatform` and `ExternalAllocation` now.

Impact if different: connector priorities, import schemas, barcode authority mapping and organizer UI change.

Blocking level: can proceed with generic architecture, blocking before platform-specific connectors.

### Q50. Какие типы external quota allocation нужны первыми?

Decision needed: assigned seats, general admission quantities, category-level quotas, or mixed.

Recommended default: support quantity/category quotas first and keep assigned-seat allocation model in schema; implement assigned-seat external allocations when first real case requires it.

Impact if different: inventory locking, seat status, reconciliation and external barcode import complexity change.

Blocking level: blocking before external allocation implementation.

### Q51. Какие форматы внешних отчетов встречаются чаще всего?

Decision needed: source format priorities.

Recommended default: CSV/XLSX first, then PDF/email attachments, then screenshots/images, then API/MCP connectors where useful.

Impact if different: ingestion tooling, parser backlog, review UI and AI normalization scope change.

Blocking level: can proceed with ingestion architecture, blocking before parser/connector implementation.

### Q52. Какой уровень AI normalization разрешен для внешних отчетов?

Decision needed: draft-only, review-required, or auto-approve above confidence threshold.

Recommended default: AI normalization creates draft/staging records only; human review is required for financial, inventory, settlement, or scanner-affecting imports until confidence and regression tests are proven.

Impact if different: operational risk, audit requirements, cost, speed and trust in reports change.

Blocking level: blocking before AI-assisted ingestion implementation.

### Q53. Нужен ли MCP server как первый способ интеграции?

Decision needed: MCP connector priority.

Recommended default: reserve MCP/connector architecture now; implement only after choosing first target such as email inbox, Google Drive folder, external portal, SFTP, or API.

Impact if different: integration tooling, auth, operator workflow and support model change.

Blocking level: can proceed with placeholder, blocking before MCP connector implementation.

### Q54. Что делать, если external reported sales превышают allocated quota?

Decision needed: over-sale policy.

Recommended default: create reconciliation exception, block automatic confirmation, keep external data labeled disputed, and require operator/organizer resolution before settlement/report finalization.

Impact if different: settlement, support, fraud/risk and inventory reports change.

Blocking level: blocking before external reconciliation implementation.

## 17. Platform Superadmin And Observability

### Q55. Кто получает роль `platform_superadmin` на первом production launch?

Decision needed: initial superadmin assignment and approval process.

Recommended default: only platform owner/core technical operator accounts; no shared accounts; mandatory MFA; every assignment logged and reviewable.

Impact if different: security, incident response, support operations and audit exposure change.

Blocking level: blocking before auth/role implementation.

### Q56. Какие superadmin действия требуют step-up authentication?

Decision needed: high-risk action policy.

Recommended default: role/permission changes, credential changes, support impersonation, break-glass access, sensitive log/raw data access, destructive cross-tenant actions, payment/billing provider configuration.

Impact if different: support speed vs account-takeover and insider-risk profile change.

Blocking level: blocking before superadmin console implementation.

### Q57. Нужна ли support impersonation и в каком режиме?

Decision needed: support access model.

Recommended default: allow scoped temporary "view as" read-only sessions first; elevated support impersonation only with explicit permission, reason, audit, and optional step-up.

Impact if different: support workflow, privacy, audit requirements and security model change.

Blocking level: blocking before support access implementation.

### Q58. Какой observability stack используем первым?

Decision needed: metrics/logs/traces/errors backend.

Recommended default: provider-neutral platform abstraction; implementation can start with self-hosted Grafana/Prometheus/Loki/Tempo or deployment-provider tooling, but the admin panel must show product-level health, load, errors, queues, webhooks and jobs.

Impact if different: infrastructure, retention, cost, dashboards and alerting implementation change.

Blocking level: can proceed with architecture placeholder, blocking before observability implementation.

### Q59. Какие retention policies нужны для logs, audit и operational events?

Decision needed: retention duration and sensitive data policy.

Recommended default: audit logs long retention, operational logs shorter retention, sensitive logs masked by default, raw sensitive data access permission-gated and logged.

Impact if different: storage cost, compliance, debugging and privacy posture change.

Blocking level: blocking before production operations spec.

### Q60. Какие alerts обязательны до первого production release?

Decision needed: mandatory alert catalog.

Recommended default: API error rate, checkout/reservation failures, payment/refund failures, webhook failures, scanner sync failures, queue backlog, worker failures, billing invoice failures, external ingestion failures, DB/cache degradation and suspicious admin activity.

Impact if different: production readiness, incident response and support SLA change.

Blocking level: blocking before production launch checklist.

## 18. Bil24-style Role Mapping

### Q61. Подтверждаем базовые бизнес-роли: agent, organizer, operator, superoperator?

Decision needed: role taxonomy.

Recommended default: yes. Use `agent`, `organizer`, `platform_operator`, `external_ticketing_operator`, and `platform_superadmin`/`superoperator` as first-class roles/role families.

Impact if different: permissions, backoffice navigation, reports, settlement, support workflows and migration mapping change.

Blocking level: blocking before auth/permissions master spec.

### Q62. Как разделяем `platform_operator` и `external_ticketing_operator`?

Decision needed: terminology and permission boundary.

Recommended default: `platform_operator` is internal platform moderation/support/operations; `external_ticketing_operator` is a third-party ticketing system/operator with own processing, customers, allocated quota, external reports, barcode batches and settlement flow.

Impact if different: external integration scope, support permissions, reconciliation, reports and security model change.

Blocking level: blocking before external-ticketing-system and operator backoffice backlog.

### Q63. Может ли один пользователь быть одновременно organizer и agent?

Decision needed: multi-role membership behavior.

Recommended default: yes, via memberships. Reports and notifications deduplicate by verified identity/organization relationship and include both role sections where needed.

Impact if different: reporting, notification routing, commission/settlement and access model change.

Blocking level: blocking before reports/notifications and role implementation.

## 19. Specification Gate And First Production Profile

### Q64. Подтверждаем первый production profile как GA-first, scanner/import-first?

Decision needed: первый рыночный профиль релиза.

Recommended default: first production profile = general admission first, platform-hosted checkout, WordPress integration, scanner sync, external barcode batch import, manual/admin reconciliation, simple organizer console, superadmin observability. Complex assigned seating editor and high-demand stadium mode remain architecture-ready but not launch-critical unless current sites require them.

Impact if different: inventory schema, frontend scope, performance tests, scanner contract and migration sequence change.

Blocking level: blocking for master specification.

### Q65. Включаем assigned seats в первый production release или только сохраняем архитектурный путь?

Decision needed: actual launch scope for seating.

Recommended default: reserve assigned-seat architecture now; implement only if one of the first migrating sites requires assigned-seat sales before launch.

Impact if different: first backlog grows significantly: seat map editor/import, seat status cache, seat-level holds, seat selection UI, accessibility testing and scanner mapping.

Blocking level: blocking before inventory and checkout backlog.

### Q66. Какая форма owner approval нужна для master specification?

Decision needed: кто и как утверждает spec-ready state.

Recommended default: one explicit approval note that lists accepted ADR IDs, deferred areas, first production profile and remaining non-blocking assumptions.

Impact if different: AutoForge may treat proposed defaults as confirmed decisions.

Blocking level: blocking before implementation tickets.

## 20. Payouts, Settlement Risk, And Financial Controls

### Q67. Какой payout model выбираем для первого production release?

Decision needed: platform collects then pays out, Stripe Connect direct charges, destination charges, manual payouts, or hybrid.

Recommended default: hybrid architecture, but operational first mode = platform-owned checkout ledger with delayed/manual or scheduled payout after refund/dispute window policy is defined. Direct/instant payouts require explicit KYB/KYC, reserves and negative balance handling.

Impact if different: risk, provider choice, ledger, refunds, chargebacks, accounting exports and organizer onboarding change.

Blocking level: blocking before payments/payout spec.

### Q68. Какая reserve/hold policy нужна для organizer payouts?

Decision needed: percentage, fixed period, event-based hold, rolling reserve, or no reserve.

Recommended default: event-based hold until event completion plus configurable reserve for high-risk organizers/events.

Impact if different: organizer cashflow, refund safety, disputes, reporting and market positioning change.

Blocking level: blocking before payout implementation.

### Q69. Кто несет chargeback/refund/dispute exposure?

Decision needed: platform, organizer, payment provider flow, or contract-specific allocation.

Recommended default: platform records exposure in ledger; organizer payout can be offset by refunds/disputes according to approved contract/tariff.

Impact if different: legal terms, ledger model, payout timing and support operations change.

Blocking level: blocking before payment/refund/payout implementation.

### Q70. Нужна ли KYB/KYC проверка organizer перед продажами или перед payout?

Decision needed: onboarding gate.

Recommended default: allow low-risk catalog setup before KYB; require KYB/KYC and payout method verification before money is released.

Impact if different: onboarding friction, fraud risk, provider setup and launch speed change.

Blocking level: blocking before organizer payout spec.

## 21. Compliance, Security, Privacy, And Accessibility

### Q71. Какие jurisdictions входят в первый launch?

Decision needed: страны/регионы для платежей, tax/fee disclosure, privacy and accessibility baseline.

Recommended default: explicitly list first launch countries before checkout/payment spec; do not assume one global legal baseline.

Impact if different: pricing display, tax rules, payment methods, consumer rights, refund language and data retention requirements change.

Blocking level: blocking before checkout/payment master spec.

### Q72. Как показываем fees, taxes and all-in pricing?

Decision needed: включены ли fees/taxes в advertised price, где показываются service fees, и какой source of truth для totals.

Recommended default: platform total calculation is source of truth; checkout supports all-in price presentation and clear fee/tax breakdown before payment confirmation.

Impact if different: market compliance, conversion, reports and disputes change.

Blocking level: blocking before checkout implementation.

### Q73. Какой PCI/SCA/3DS posture принимаем?

Decision needed: hosted payment page/elements, card data scope, SCA/3DS state handling and payment provider responsibility.

Recommended default: minimize PCI scope through provider-hosted or provider-tokenized components; represent SCA/3DS required/succeeded/failed/canceled states in platform payment lifecycle.

Impact if different: compliance burden, security tests, frontend integration and payment state machine change.

Blocking level: blocking before payment provider implementation.

### Q74. Какой accessibility target задаем для public checkout и admin?

Decision needed: WCAG level and critical flows.

Recommended default: WCAG 2.2 AA target for public checkout, ticket delivery, account/auth and essential admin flows.

Impact if different: frontend components, design QA, keyboard/screen-reader testing and market readiness change.

Blocking level: blocking before frontend Definition of Done.

### Q75. Какие privacy/retention defaults принимаем?

Decision needed: сроки хранения orders/tickets/audit/logs, deletion/export policy, marketing consent, analytics/pixels.

Recommended default: long retention for financial/audit records where legally required; shorter masked operational logs; explicit consent for marketing/tracking; data export/delete workflows scoped by legal constraints.

Impact if different: data model, admin tools, support tooling, analytics and compliance workload change.

Blocking level: blocking before production launch checklist.

### Q76. Какой API security model выбираем первым?

Decision needed: service accounts, OAuth/API keys, scopes, rate limits, webhook signatures and developer console scope.

Recommended default: scoped service accounts/API keys for server integrations first; signed webhooks; resource-aware ABAC; OAuth/app marketplace later if needed.

Impact if different: partner integrations, external apps, abuse protection and developer console backlog change.

Blocking level: blocking before public/external API implementation.

## 22. Ticket Credentials And Scanner Trust

### Q77. Какие ticket credential types нужны первыми?

Decision needed: static QR/PDF, Wallet pass, rotating QR, NFC, printed ticket, external barcode import.

Recommended default: platform ticket identity plus credential layer; static QR/PDF first, external barcode batch import first for scanner readiness, Wallet pass next, rotating QR/NFC reserved by model.

Impact if different: delivery service, scanner cache, credential replacement, fraud protection and mobile UX change.

Blocking level: blocking before ticket delivery and scanner contract.

### Q78. Какая screenshot/offline validation policy нужна?

Decision needed: static screenshots allowed, rotating credentials required for some events, online lookup required, or offline cache accepted.

Recommended default: offline scanner cache must validate platform-issued static QR and approved imported batches; high-risk events can later require rotating credentials or online checks.

Impact if different: scanner app requirements, event-day reliability, fraud risk and credential design change.

Blocking level: blocking before scanner implementation.

### Q79. Как работает credential replacement/revocation?

Decision needed: кто может revoke/reissue credential, когда old credential stops working, and how scanner receives updates.

Recommended default: credential replacement creates new credential version, revokes old active credential, publishes scanner sync event, and preserves ticket identity/history.

Impact if different: support operations, fraud response, scanner cache consistency and audit change.

Blocking level: blocking before credential implementation.

### Q80. Как различаем platform ticket, complimentary ticket, legacy import and external barcode?

Decision needed: barcode namespace and authority model.

Recommended default: every scan decision includes authority/source metadata; raw barcode value alone is never globally trusted.

Impact if different: duplicate barcode risk, reconciliation errors, scanner false positives and reporting ambiguity.

Blocking level: blocking before scanner/barcode design.

## 23. Specification Readiness

### Q81. Какие proposed defaults из decision log можно принять без отдельной дискуссии?

Decision needed: список ADR IDs that owner accepts as defaults.

Recommended default: accept only low-risk defaults explicitly. Backend framework family is accepted as Go; keep exact Go tooling, first payment provider, payout risk, jurisdictions and first production profile as owner-confirmed decisions.

Impact if different: specification may encode hidden business assumptions.

Blocking level: blocking before master specification approval.

### Q82. Какие документы должны считаться source of truth при конфликте?

Decision needed: precedence order between market research, risk docs, architecture docs, clarification register, decision log and future specification.

Recommended default: after approval, master specification wins for implementation; before approval, guardrails + decision log + clarification register override older research notes where conflicts exist.

Impact if different: AutoForge may follow stale strategy notes over newer architecture decisions.

Blocking level: blocking before implementation planning.
