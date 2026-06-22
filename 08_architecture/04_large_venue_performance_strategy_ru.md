# Large Venue Performance Strategy

Обновлено: 2026-06-21

## Контекст

На старте проекта у нас, скорее всего, не будет продаж уровня большого стадиона. Но архитектура должна учитывать этот сценарий с самого начала, чтобы ранние решения по данным, API, кешу и checkout не закрыли путь к high-demand режиму.

Ориентиром является стресс-тест ArenaSoldOut для больших площадок:

- Stadium 1: 25,841 assigned seats + 6,000 general admission, всего 31,841.
- SVG actual layout: около 4.8 MB raw / 435 KB compressed.
- `GET_SCHEMA`: около 2.25 MB raw / 307 KB compressed.
- `GET_SEAT_LIST`: около 4 MB raw / 180 KB compressed.
- После оптимизаций: 1000 клиентов получили SVG actual layout примерно за 30 секунд.
- Через `GET_SCHEMA + GET_SEAT_LIST`: около 1793 из 1800 клиентов получили данные примерно за 35 секунд.
- Full ticket cycle: 1000 клиентов купили 4000 билетов примерно за 54 секунды.

Эти цифры не являются обязательной целью первого запуска. Это performance envelope, который архитектура должна позволить достичь после отдельной оптимизации и инфраструктурной подготовки.

## Главный вывод

Самая тяжелая часть high-demand продажи - не платеж и не создание заказа, а массовая раздача актуальной схемы зала в момент старта продаж.

Нельзя строить актуальную схему из БД, сериализовать большой JSON/SVG и сжимать ответ заново для каждого покупателя. Такая архитектура быстро упрется в CPU, сеть, serialization и database reads.

## Целевой принцип

```text
Static Seating Geometry
  - sections
  - rows
  - seats
  - x/y coordinates
  - SVG/geometry assets
  - versioned and mostly immutable

Dynamic Seat Status
  - available
  - reserved
  - sold
  - blocked
  - held by channel/order/session
  - short-lived and frequently changing
```

Статическая геометрия должна жить отдельно от динамических статусов.

## Large Venue Mode

В платформе должен быть режим для больших площадок и hot sales.

Примерные условия включения:

- venue/session capacity больше 5000
- event marked as high demand
- known sale opening time
- expected traffic spike
- manual operator/organizer flag

Название режима может быть:

```text
large_venue_mode
hot_sale_mode
high_demand_mode
```

Это не отдельная платформа. Это набор policy и инфраструктурных переключателей для конкретного event/session.

## Static Schema Strategy

Для seating plan/event session нужно поддержать:

- precomputed schema JSON
- precomputed SVG или render asset, если используется SVG-layout
- versioned schema assets
- checksum/hash
- gzip/brotli precompressed variants
- CDN/cache headers
- invalidation only on schema version change

Важное правило: schema geometry не должна пересобираться при каждом изменении seat status.

Рекомендуемые endpoints:

```text
GET /v1/seating-plans/{id}/schema
GET /v1/event-sessions/{id}/schema
GET /v1/event-sessions/{id}/layout.svg
```

Ответ должен содержать:

```text
schema_version
asset_version
status_endpoint
status_version optional
cache headers
```

## Dynamic Seat Status Strategy

Для каждого event session нужен hot cache статусов мест.

Требования:

- cache keyed by `event_session_id`
- compact status representation
- point updates on reservation/order/refund/release/block
- version/counter for status snapshot
- ability to serve full status snapshot
- ability to serve delta since version later
- no DB scan per request during hot sale

Рекомендуемые endpoints:

```text
GET /v1/event-sessions/{id}/seat-status
GET /v1/event-sessions/{id}/seat-status?since_version=...
```

Возможные форматы:

- compact JSON arrays
- seat id -> status map for smaller venues
- bitset/status arrays for large venues
- binary/MessagePack later if needed
- delta stream/WebSocket/SSE later if justified

Первый запуск может использовать JSON, но model/API должны не мешать более компактному формату позже.

## Point Update Model

Каждая операция, меняющая места, должна обновлять dynamic status cache точечно.

Примеры:

```text
available -> reserved
reserved -> available
reserved -> sold
sold -> refunded/released policy dependent
available -> blocked
blocked -> available
```

Нельзя при каждом изменении пересобирать весь status list из БД.

## Reservation And Inventory Rules

Reservation должна быть атомарной.

Требования:

- короткая транзакция
- защита от double-sell
- idempotency key
- audit log
- reservation TTL
- release job
- deterministic conflict response
- no payment provider call inside inventory lock

Рекомендуемый flow:

```text
1. Client loads schema asset.
2. Client loads current seat status snapshot.
3. Client selects seats/ticket types.
4. Client sends reservation request with idempotency key.
5. Inventory service atomically reserves seats.
6. Status cache receives point updates.
7. Client receives reservation id, expires_at, totals preview.
```

## Read Surge vs Write Flow

High-demand sale has two different workloads:

- read surge: thousands of users loading schema/status
- write contention: many users trying to reserve the same limited inventory

Architecture must separate them.

Read surge should be handled by:

- CDN
- precomputed compressed assets
- in-memory/status cache
- read replicas only where necessary

Write flow should be handled by:

- bounded reservation API
- queue/backpressure where needed
- atomic inventory operations
- idempotent order creation
- async payment callbacks

## Backpressure And Waiting Room

Для high-demand событий нужен механизм защиты платформы и UX.

Возможные элементы:

- event-level waiting room
- tokenized access to seat selection
- per-IP/per-account/per-channel rate limits
- bot protection
- bounded worker pools
- queue position display
- overload response with retry-after
- per-event circuit breaker

Это может быть реализовано позже, но API и frontend flow должны учитывать возможность waiting room/token gate.

## Payload And Serialization Rules

Для больших ответов важны:

- не сжимать один и тот же большой ответ на лету многократно
- иметь precompressed cache
- избегать лишнего object-to-JSON conversion
- иметь compact DTOs для status payloads
- не включать тяжелые поля без запроса
- использовать ETag/version
- поддерживать conditional requests

## Client Rendering Rules

Внешние клиенты должны быть готовы к модели:

```text
load schema once -> load status snapshot -> apply updates/deltas
```

WordPress plugin, embeddable widget, mobile app и custom frontend не должны требовать от backend каждый раз отдавать полностью собранный actual layout, если можно отдать schema + status.

При этом compatibility mode может поддерживать legacy-style actual layout/SVG, но внутри он должен использовать precomputed schema + dynamic status cache, а не прямую сборку из БД.

## Payment And Order Rules

Payment provider не должен быть bottleneck для inventory lock.

Правильный порядок:

```text
reservation -> order/checkout session -> payment intent -> provider redirect/embed -> provider callback -> capture/confirm -> issue tickets
```

Требования:

- payment callbacks idempotent
- order confirmation idempotent
- ticket issuance idempotent
- reservation expiration clearly handled
- if payment succeeds after reservation expiry, deterministic recovery/refund/manual review policy

## Observability

Для hot sale нужны метрики отдельно по этапам:

- schema asset load latency
- seat status snapshot latency
- reservation latency
- reservation conflict rate
- order creation latency
- payment intent latency
- payment callback latency
- ticket issuance latency
- cache hit ratio
- status cache update lag
- queue length
- timeouts

Логи должны иметь:

- event_session_id
- request_id
- idempotency_key
- sales_channel_id
- user/session identity
- operation
- timing breakdown

## Performance Test Profiles

Первый production launch не обязан проходить stadium profile. Но в Definition of Done должны появиться уровни.

### Small Profile

- 500-1000 seats
- 50-100 concurrent users
- basic reservation/order flow

### Medium Profile

- 5000 seats
- 500-1000 concurrent layout/status loads
- 100-300 concurrent reservations

### Large Profile

- 25,000+ assigned seats
- 1000+ concurrent layout/status loads under about 30 seconds
- 1000 buyers / 4000 ticket full-cycle under about 60 seconds as aspirational benchmark

### Extreme/Future Profile

- 100,000 capacity mixed venue/festival
- CDN-first layout delivery
- queue/waiting room mandatory
- compact/binary status payloads likely needed

## Initial Implementation Guidance

На первом этапе не обязательно реализовывать весь large venue optimization.

Но обязательно с самого начала:

1. Разделить `SeatingPlanVersion` и `SeatStatus`.
2. Версионировать schema/layout.
3. Не смешивать static geometry с order/reservation state.
4. Проектировать reservation как atomic/idempotent operation.
5. Оставить место для status cache.
6. Не делать WordPress/WooCommerce источником истины для availability.
7. Не строить API так, будто actual layout всегда создается синхронно из БД.

## Open Questions

1. Нужен ли actual SVG layout в первой версии, или достаточно schema JSON + frontend renderer?
2. Какой формат schema geometry выбираем как source of truth?
3. Какой cache layer используем первым: Redis, in-process cache, отдельный inventory cache service?
4. Нужны ли WebSocket/SSE deltas на первом этапе, или хватит polling/status snapshots?
5. Какой минимальный performance profile задаем для первого production release?
6. Будет ли waiting room частью первой версии или только архитектурной заготовкой?
