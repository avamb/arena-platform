# Current Implementation Overview

Обновлено: 2026-07-18

Статус: `living document`, синхронный с кодом по состоянию на 2026-07-18 (AutoForge PR2 audit / Wave 20+, 374/380 passing features).

Цель документа: зафиксировать фактически реализованный scope backend-моноolith'а `arena_new`, чтобы устранить рассинхрон между master-спецификацией (`12_master_platform_specification_ru.md`), initial Go спецификацией (`13_backend_go_initial_specification_ru.md`) и реальным кодом в `apps/backend/`.

Этот файл НЕ заменяет master specification. Он является inventory-снимком: что уже есть, где живёт, через какие migrations/handlers это видно. Любое расширение скоупа сверх описанного здесь требует ADR (см. раздел "ADR-protocol on scope expansion" в `11_architecture_decision_log_ru.md`).

## 1. Architectural Shape (актуальная)

- Modular monolith на Go 1.24, `net/http` + chi.
- Layout `apps/backend/internal/`:
  - `adapters/` — внешние интеграции: `http` (handlers + сгенерированные OpenAPI типы), `postgres` (hand-maintained gen/ + `.sql` queries; следует sqlc v1 conventions), `redis`, `stripe`, `stripebilling`, `allpay`, `email`.
  - `platform/` — cross-cutting сервисы: `auth`, `permissions`, `audit`, `outbox`, `idempotency`, `ratelimit`, `i18n`, `ids`, `clock`, `logging`, `observability`, `telemetry`, `httpserver`, `worker`, `database`/`db`, `config`, `delivery`, `reporting`, `reportdelivery`, `redissession`, `users`.
  - `payments/` — payment provider abstraction + routing + webhook.
  - `migrations/sql/` — embedded goose migrations 0001..0069.
  - `domain/`, `app/` — каталоги существуют, но намеренно пусты. Бизнес-логика живёт в `platform/httpserver/h*` handlers и `platform/*` сервисах поверх gen/ queries (handler-centric layout, принят как ADR-033). Вынесение в классические DDD application/domain слои отложено до появления конкретного bounded-context boundary.

## 2. Bounded Contexts / Inventory

Список фактически реализованных контекстов с привязкой к migrations и sqlc query-файлам в `apps/backend/internal/adapters/postgres/queries/`:

| Контекст | Migrations (ключевые) | Query file(s) |
|---|---|---|
| Identity & users | 0001..0005, 0065 | `users.sql`, `password_reset_tokens.sql`, `refresh_tokens.sql`, `sessions.sql` |
| Organizations & memberships | early | `orgs.sql`, `memberships.sql`, `rbac.sql`, `operator_networks.sql` |
| Catalog (events, venues, geo) | 00xx, 0050, 0051, 0052 | `events.sql`, `venues.sql`, `geo.sql`, `channels.sql`, `event_publications.sql` |
| Ticket tiers & pricing | 0022, 0023 | `ticket_tiers.sql`, `promo_codes.sql` |
| Inventory & reservations | early..0024, 0063 | `inventory_ledger.sql`, `reservations.sql`, `reservation_seats.sql`, `reservation_ga_items.sql` |
| Checkout & payments | 0024, 0025, 0037, 0060 | `checkout_sessions.sql`, `payment_intents.sql`, `payment_provider_configs.sql`, `stripe_billing.sql` + `internal/payments/` |
| Tickets & credentials | 0026, 0027, 0029, 0039, 0059, 0066 | `tickets.sql`, `ticket_credentials.sql`, `barcodes.sql`, `barcode_batches.sql` |
| Refunds | 0028 | `refunds.sql` |
| Delivery (email/print/etc.) | 0030, 0064, 0067 | `delivery_jobs.sql` |
| Scanner & scan events | 0055 | `scan_events.sql` |
| Seating plans & assigned seats | 0057, 0058 | `seating_plans.sql`, `session_seats.sql`, `session_seating_public.sql` |
| External allocations / scanner ingestion | 0035 | `external_allocations.sql` |
| Complimentary tickets | 0036, 0038 | `complimentary_issuances.sql` |
| Reporting | 0032 | `event_reports.sql` |
| Billing | 0033, 0037 | `billing_ledger.sql`, `stripe_billing.sql` |
| Superadmin | 0034 | `superadmin.sql` |
| Webhook delivery | 0040 | `webhook_subscribers.sql` |
| Reconciliation | 0041 | `reconciliation.sql` |
| Public feed (WordPress/Bil24 compat) | 00xx, 0061 | `public_feed.sql`, `feed_tokens.sql` |
| GDPR | 00xx | `gdpr.sql` |
| Widget analytics | 0062 | `widget_funnel_events.sql` |
| Outbox / background worker | 0068, 0069 | `internal/platform/outbox/`, `internal/platform/worker/` |
| System / infra | 00xx | `system.sql` |
| Bank accounts | 0056 | `bank_accounts.sql` |

Полный набор migrations: 0001..0069 в `apps/backend/internal/migrations/sql/`.

## 3. WordPress Plugin

`apps/wp-plugin/arena-events/` — WordPress-плагин с feed/checkout/accessibility слоями. Документирован в `02_wordpress_integration_contract_ru.md` и в фичах #155 (checkout), #165 (WCAG 2.2 AA).

## 4. CI / Generated Clients / Tests

- OpenAPI → Go server types через `oapi-codegen` (v2.4.1, known warning по OpenAPI 3.1).
- OpenAPI → TypeScript клиенты — `openapi-typescript` регенерирует `apps/backend/openapi/clients/ts/index.d.ts`; CI gate проверяет `git diff --exit-code` на drift (фича #376).
- `go test ./...` и `go test -race -coverprofile=... -covermode=atomic ./...` — зелёные.
- `docker-compose` runtime — healthy, миграции применяются до 0069.
- `golangci-lint` (v2 конфиг) — 563+ issues. Это блокирующий gate перед production-ready.
- sqlc gen/ — файлы hand-maintained (следуют sqlc v1 output conventions); `make sqlc-generate` требует `sqlc >= v1.26` на PATH.

## 5. Reconciliation Status vs Older Docs

- `12_master_platform_specification_ru.md` — был помечен `initial draft` от 2026-06-21 и ссылался на foundation-only scope. Не отражает текущий broad-scaffold. Должен быть либо переписан под текущую реальность, либо явно помечен как историческая стартовая спека (см. status-баннер в начале файла).
- `13_backend_go_initial_specification_ru.md` — описывает первый milestone (foundation only). Помечен как `historical / superseded`; для текущей картины смотри этот документ (14) и `00_backend_architecture_brief_ru.md`.
- `00_backend_architecture_brief_ru.md` — остаётся актуальным как high-level brief.
- `11_architecture_decision_log_ru.md` — расширен разделом "ADR-protocol on scope expansion" (см. шаг 6 фичи #180).

## 6. Known Gaps / Follow-ups

- Domain/app слои пусты — текущая фактическая архитектура отклоняется от изначально заявленной DDD-структуры в doc 13. ADR-033 формально принимает handler-centric layout как текущий статус-кво (см. `11_architecture_decision_log_ru.md`).
- `golangci-lint`: 563+ issues — блокирующий gate перед production-ready.
- Master spec (doc 12) требует переписывания под фактически реализованные контексты из раздела 2 этого документа.
- sqlc gen/: файлы были hand-maintained с заголовком `sqlc v2.3.0` (несуществующая версия). Заголовки исправлены на честное "hand-maintained, follows sqlc v1 conventions" (фича #380). Для восстановления codegen-гарантии необходимо запустить `make sqlc-generate` с реальным sqlc >= v1.26.

## 7. Update Cadence

Этот документ должен обновляться:

- при каждом merge новой миграции в `apps/backend/internal/migrations/sql/`;
- при добавлении нового query-файла или нового bounded context;
- при сменe архитектурного layout (`internal/`);
- при изменении CI-гейтов (lint, race, coverage).

Если апдейт не сделан в том же PR — фича считается архитектурно не-reconciled и не может быть marked passing в AutoForge.
