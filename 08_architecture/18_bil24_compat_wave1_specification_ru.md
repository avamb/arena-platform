# Спецификация волны 1: WP-сайты (Lampyris, Vino&Co) на arena через Bil24-совместимый шлюз

Дата: 2026-09-04. Статус: спецификация к реализации (design authority для бэклога
`09_autoforge/wp_bil24_compat_backlog.md`). Базируется на решениях владельца из
`docs/migration/wp_sites_to_arena_wave1_2026-09-04.md` §7 и на фактах кода `c689d44`
(миграции 0001–0089, шлюз `hbil24`/`bil24compat`, MACS `internal/platform/macs`).

Исполняемая спецификация сайта — PHP плагинов `bil24-acf-sync`, `bil24-ticket-mailer`,
`lampyris-ops`, mu-плагинов `bil24-cart-bridge`, `bil24-notification-receiver`
(`C:\Projects\lampyrisevents`, `C:\Projects\vinoandco-prod-rebuild`). Реальные тела —
`vinoandco-prod-rebuild/docs/samples/*.json` (420 заказов, 819 билетов). Все ссылки
`file:line` ниже — на эти репозитории и на arena `c689d44`.

Единицы: деньги в БД — bigint в минорных единицах; на проводе Bil24 — числа в мажорных
единицах с 2 знаками (`25`, `1.25`). Все ID на проводе — целые (`int64`), без исключений.

---

## 0. Что изменилось относительно отчёта 2026-09-04

Спецификация уточняет отчёт в местах, где код оказался не таким, как предполагалось:

| Тема | Отчёт | Факт / решение спецификации |
|---|---|---|
| `fid` | «fid = UUID канала» | Сайт делает `(int)$o['fid']` (`class-bil24-client.php:12`) — UUID превращается в 0. **`fid` = `sales_channels.display_number`** (0072, bigint UNIQUE). |
| Токен на чтении | «читающие команды без токена» | Сайт всегда автоинжектит `fid`+`token`+`locale` (`class-bil24-client.php:12-14`). **Токен обязателен на всех командах**, кроме `GET /compat/bil24/image` (там URL без токена — `class-bil24-seat-picker.php:377-381`). |
| Права для ключа канала | `event.write`, `session.write`… | Таких прав нет. Каталог: `event.create/read/update/delete/publish`, `session.*`, `tier.*`, `seating_plan.create/read/update.own/fork`, `event_session.assign_seating_plan`, `media.write/read` (0074, 0057, 0053). |
| `holderStatus` | `NEVER_USE` / `REFUND` | Выгрузки: `NEVER_USE` ×724, `REFUND` ×95. Приёмник сайта пишет `REFUNDED` и сравнивает с `REFUNDED` (`bil24-notification-receiver.php:209`, `class-btm-data.php:39`). **Отдаём `NEVER_USE`/`REFUND`, принимаем оба.** |
| Промокоды | «нативный движок есть» | Есть: `hcheckout.ValidatePromoForLines` (`promo_codes.go:119`); один код на checkout (`checkout_sessions.promo_code_id`). |
| Заказ | «агрегата нет» | Подтверждено. `tickets.holder_email` **никогда** не пишется (`htickets/tickets.go` → `InsertTicket(..., nil /* holderEmail */)`), имя/телефон отбрасываются (`hfeed/public_feed_checkout.go:308-338`). |
| Штрих-код | «EAN-13 нужен» | В репозитории нет ни генератора, ни контрольной суммы; `"EAN-13"` — литерал в `macs/export.go:475-478` при 64-hex `static_qr`. Сайт печатает `EAN13` только при валидной контрольной цифре (`class-btm-renderer.php:170-176`), иначе Code128. |
| `RevokeTicketArtifactsTx` | — | Ревокует типы `"qr","pdf"`, а CHECK допускает `static_qr` (`htickets/cancel.go:568`, 0027:32): **`static_qr` при отмене не ревокуется**. Чинится вместе с Ф2. |
| Бронь | «одна reservation = один чекаут» | `checkout_sessions.reservation_id` и `reservations.session_id` — единичные NOT NULL FK; runbook волны 4 запрещает это менять. **Корзина = одна изменяемая reservation на сеанс**, не набор reservation'ов. |
| Гардрейл #6 | «нативный плагин — целевой путь для наших сайтов» | Решение владельца 2026-09-04 №1 разворачивает его для двух собственных сайтов → ADR-034 (§17). |

---

## 1. Границы и принципы

**Входит (arena-сторона):** каркас шлюза (К1–К6), 15 команд + расширение `REFUND_TICKET`,
SVG-роут `image?type=seatingPlan`, вебхуки в сайт в форме Bil24, EAN-13, агрегат заказа
(минимум A1), покупатель (C6) и инструмент импорта баз (C7), правки MACS (М1–М5), сервисные
ключи канала (C1), приём импортированного из Bil24 сеанса с сохранением ID (C3-arena).

**Не входит:** платёжные адаптеры в рантайме (деньги на сайте — решение №2), налоги (№8),
миграция истории заказов (№4), эмуляция легаси-виджета (№2), модуль разметки Р2 (волна 1.1),
организаторский shell, изменения в MACS (№7), PHP-код сайтов (описан в §13.4 как контракт).

**Принципы:**

1. Шлюз — адаптер поверх ядра (гардрейл #7). Ни одна таблица ядра не получает Bil24-имён;
   всё Bil24-специфичное живёт в `internal/adapters/bil24compat` (провод), `hbil24`
   (оркестрация), `internal/platform/compatids` (целочисленные ID), `internal/platform/bil24wire`
   (кодировщик Order/Ticket в форму Bil24 для вебхуков).
2. Тотал считает платформа (гардрейл #15): `sum − discount + charge = totalSum`, где
   `charge = round((sum − discount) × chargePercent / 100)`, `chargePercent` =
   `sales_channels.fee_percent`. `expectedPrice`/`total`/`amount` клиента — только для
   логирования и флага расхождения, никогда для расчёта.
3. Один открытый заказ на покупателя и сеанс (§14.3); истёкшая бронь никогда не ломает
   оплаченный заказ (§7.9, `manual_review`).
4. Всё, что видит сайт как ID, — `int64` из §4; всё, что сайт видит как дату, — в TZ
   площадки (`venues.timezone`, единственная TZ-колонка в схеме, 0050:41).
5. Каждая команда — golden-тест на байтовое совпадение с фикстурой (§15); фича считается
   сделанной, когда её golden-тесты зелёные, а не когда «команда реализована».

---

## 2. Размещение кода

| Пакет | Назначение | Новый? |
|---|---|---|
| `internal/adapters/bil24compat` | `Request`/`Response`, коды, **новые именованные структуры ответов** (§7), денежный форматтер, парсер `locale` | расширяется (гейт `bil24compat_layout_188_test.go`: не импортировать `httpserver`) |
| `internal/platform/httpserver/hbil24` | диспетчер и хендлеры команд; сегодня 1 920 строк в одном файле — разбить по командам (`cmd_catalog.go`, `cmd_cart.go`, `cmd_order.go`, `cmd_tickets.go`, `auth.go`) | расширяется |
| `internal/platform/httpserver/bil24_shims.go` | mount `POST /compat/bil24/json` и `GET /compat/bil24/image`; ≤400 строк (гейт 175) | расширяется |
| `internal/platform/compatids` | `Ensure(ctx, q, kind, uuid) (int64, error)`, `EnsureMany`, `Resolve(kind, int64) (uuid, error)`, `RegisterExternal(kind, int64, uuid)` | новый |
| `internal/platform/customers` | нормализация идентичностей, `Resolve(ctx, tx, ResolveInput) (Customer, error)`, слияние-кандидаты | новый |
| `internal/platform/ordering` | `CreateOrderFromCheckout`, `MarkPaid`, `Cancel`, `Expire`; вызывается из `hcheckout`, `hfeed`, `hbil24` | новый (разворот ADR-033 только для заказа — ADR-035) |
| `internal/platform/orderexport` | нейтральная проекция БД → `Order{Tickets[]}` (перенос запросов из `macs/export.go`) | новый |
| `internal/platform/bil24wire` | кодировщик `orderexport.Order` → Bil24 JSON (строковый `holderStatus`, `category` строкой, `showTime` локальный) + диспетчер вебхуков `kind='bil24_wp'` | новый |
| `internal/platform/macs` | остаётся кодировщиком для MACS поверх `orderexport` (М1–М5) | меняется |
| `internal/platform/apikeys` + `httpserver/hapikeys` | сервисные ключи канала (ADR-029) и middleware | новый |
| `internal/domain/seating/sbt_import.go` | парсер sbt-SVG формата Bil24 (§13.3) | новый |
| `httpserver/himports` | `POST /v1/organizations/{org}/imports/bil24-session` | новый |
| `internal/platform/barcodes/ean13` | генерация/проверка EAN-13 | новый |
| `tests/compat/bil24` | контрактный харнесс, фикстуры WP, стабы приёмника сайта и MACS | расширяется |

Обязательные гейты для всего нового кода: файл в `httpserver/` ≤400 строк; `panic(` только с
`// allow:panic:`; не-RFC3339 формат времени только с `// allow:timeformat:` (для
`DD.MM.YYYY`, `HH:MM`, `showTime`); OpenAPI 3.1 без `nullable:`; drift в обе стороны
(`/compat/bil24/*` документируется в `openapi.yaml` — сегодня не описан); миграционный пин
`migrations_head_test.go:43`; `timestamptz` only.

---

## 3. Данные: миграции 0090–0096

Номера последовательные, следующий свободный — **0090**. Каждая миграция — свой блок ниже;
DDL — обязательная форма (имена колонок и CHECK'и — часть контракта тестов).

### 3.1 `0090_compatibility_ids.sql`

```sql
CREATE SEQUENCE compatibility_system_id_seq START WITH 1000000000;

CREATE TABLE compatibility_id_map (
    kind        text        NOT NULL CHECK (kind IN
                  ('action','action_event','category_price','venue','city','country')),
    system_id   bigint      NOT NULL,
    platform_id uuid        NOT NULL,
    source      text        NOT NULL CHECK (source IN ('arena','bil24')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, system_id),
    UNIQUE (kind, platform_id)
);
-- Ряды source='arena' минтятся лениво при первом показе сущности шлюзу
-- (INSERT ... ON CONFLICT (kind, platform_id) DO NOTHING RETURNING), system_id = nextval.
-- Ряды source='bil24' пишет только импорт (§13.2) и только если system_id < 1e9.

ALTER TABLE session_seats ADD COLUMN system_seat_id_source text NOT NULL DEFAULT 'arena'
    CHECK (system_seat_id_source IN ('arena','bil24'));
```

Пояснение к `kind`: сущности с собственной строковой идентичностью (билет, место, заказ,
покупатель) получают bigint-колонку в своей таблице (0088 уже так сделал для билетов и мест);
map — для сущностей каталога, которым импорт может назначить чужой ID. `seat` в map не
входит: Bil24 `seatId` живёт в геометрии плана (`external_id`, §13.3) и копируется в
`session_seats.system_seat_id` при материализации — так ID переживает rebind.

### 3.2 `0091_customers.sql`

```sql
CREATE TABLE customers (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    system_id     bigint NOT NULL UNIQUE DEFAULT nextval('compatibility_system_id_seq'),
    display_name  text,
    locale        text,
    merged_into   uuid REFERENCES customers(id),
    anonymized_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE customer_identities (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id      uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    kind             text NOT NULL CHECK (kind IN
                       ('email','phone','telegram','device','wc_customer','bil24_user')),
    value_normalized text NOT NULL,
    channel_id       uuid REFERENCES sales_channels(id) ON DELETE SET NULL,
    verified_at      timestamptz,
    first_seen_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    source           text NOT NULL DEFAULT 'live'
);
-- сильные ключи уникальны на всю платформу
CREATE UNIQUE INDEX customer_identities_strong_uq
    ON customer_identities (kind, value_normalized)
    WHERE kind IN ('email','phone','telegram');
-- слабые — уникальны в пределах канала
CREATE UNIQUE INDEX customer_identities_weak_uq
    ON customer_identities (kind, value_normalized, channel_id)
    WHERE kind IN ('device','wc_customer','bil24_user');
CREATE INDEX customer_identities_customer_idx ON customer_identities (customer_id);

CREATE TABLE customer_consents (
    customer_id  uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('terms','marketing')),
    given_at     timestamptz NOT NULL,
    withdrawn_at timestamptz,
    source       text NOT NULL,          -- 'order:<order_id>' | 'import:<label>' | 'site'
    PRIMARY KEY (customer_id, org_id, kind)
);

CREATE TABLE customer_org_links (
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    first_order_at timestamptz,
    last_order_at  timestamptz,
    orders_count   int NOT NULL DEFAULT 0,
    tickets_count  int NOT NULL DEFAULT 0,
    source         text NOT NULL CHECK (source IN ('order','import')),
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (customer_id, org_id)
);

CREATE TABLE customer_attributes (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id      uuid REFERENCES organizations(id) ON DELETE CASCADE,  -- NULL = платформенный
    key         text NOT NULL,
    value       jsonb NOT NULL,
    source      text NOT NULL,
    imported_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, org_id, key)
);

CREATE TABLE customer_merge_candidates (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_a  uuid NOT NULL REFERENCES customers(id),
    customer_b  uuid NOT NULL REFERENCES customers(id),
    reason      text NOT NULL,          -- 'email_of_a_phone_of_b' | 'import:<label>'
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolution  text CHECK (resolution IN ('merged','kept_separate'))
);

CREATE TABLE gateway_sessions (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    session_token text NOT NULL UNIQUE,                 -- Bil24 sessionId, 43 символа base64url
    customer_id   uuid NOT NULL REFERENCES customers(id),
    org_id        uuid NOT NULL REFERENCES organizations(id),
    channel_id    uuid NOT NULL REFERENCES sales_channels(id),
    locale        text NOT NULL DEFAULT 'en',
    promo_codes   text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);
CREATE INDEX gateway_sessions_customer_idx ON gateway_sessions (customer_id);

ALTER TABLE reservations
    ADD COLUMN gateway_session_id uuid REFERENCES gateway_sessions(id) ON DELETE SET NULL,
    ADD COLUMN customer_id        uuid REFERENCES customers(id);
CREATE INDEX reservations_gateway_session_active_idx
    ON reservations (gateway_session_id) WHERE state = 'active';

INSERT INTO permissions (code, description) VALUES
  ('customer.read',   'Read customers linked to the organization'),
  ('customer.import', 'Platform-level customer database import');
```

Нормализация: e-mail — `lower(trim)`, IDN как есть; телефон — E.164 через `libphonenumber`-порт
(`github.com/nyaruka/phonenumbers`), регион по умолчанию из `venues.country` организации
(`IL` для Vino, `CZ` для Lampyris), невалидный телефон — не идентичность, а атрибут.

### 3.3 `0092_orders.sql`

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- trusted extension, как btree_gist в 0087

CREATE TABLE orders (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    system_id           bigint NOT NULL UNIQUE DEFAULT nextval('compatibility_system_id_seq'),
    org_id              uuid NOT NULL REFERENCES organizations(id),
    channel_id          uuid NOT NULL REFERENCES sales_channels(id),
    event_id            uuid NOT NULL REFERENCES events(id),
    session_id          uuid NOT NULL REFERENCES sessions(id),
    customer_id         uuid REFERENCES customers(id),
    checkout_session_id uuid NOT NULL UNIQUE REFERENCES checkout_sessions(id),
    reservation_id      uuid NOT NULL REFERENCES reservations(id),
    external_ref        text,                          -- номер WC-заказа (CREATE_ORDER_EXT.orderId)
    source              text NOT NULL CHECK (source IN
                          ('bil24_gateway','public_feed','checkout_api','complimentary')),
    status              text NOT NULL CHECK (status IN
                          ('pending_payment','paid','cancelled','expired','abandoned',
                           'refunded','partially_refunded','manual_review')),
    currency            char(3) NOT NULL,
    subtotal            bigint NOT NULL DEFAULT 0,
    discount            bigint NOT NULL DEFAULT 0,
    charge              bigint NOT NULL DEFAULT 0,
    total               bigint NOT NULL DEFAULT 0,
    charge_percent_bp   int    NOT NULL DEFAULT 0,      -- 125 = 1.25 %
    promo_code_id       uuid REFERENCES promo_codes(id),
    buyer_name          text,
    buyer_email         text,
    buyer_phone         text,
    payment_method      text,                          -- PAY_ORDER.method / provider slug
    paid_at             timestamptz,
    cancelled_at        timestamptz,
    expires_at          timestamptz,
    metadata            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (total = subtotal - discount + charge)
);
CREATE INDEX orders_org_created_idx     ON orders (org_id, created_at DESC);
CREATE INDEX orders_customer_idx        ON orders (customer_id);
CREATE INDEX orders_session_status_idx  ON orders (session_id, status);
CREATE UNIQUE INDEX orders_channel_external_ref_uq
    ON orders (channel_id, external_ref) WHERE external_ref IS NOT NULL;
CREATE INDEX orders_buyer_email_trgm ON orders USING gin (buyer_email gin_trgm_ops);
CREATE INDEX orders_buyer_name_trgm  ON orders USING gin (buyer_name  gin_trgm_ops);
CREATE INDEX orders_buyer_phone_trgm ON orders USING gin (buyer_phone gin_trgm_ops);

CREATE TABLE order_items (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id        uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    ordinal         int  NOT NULL,
    kind            text NOT NULL DEFAULT 'ticket' CHECK (kind IN ('ticket')),
    tier_id         uuid NOT NULL REFERENCES ticket_tiers(id),
    session_seat_id uuid REFERENCES session_seats(id),   -- место или GA-юнит (AB-51)
    ticket_id       uuid REFERENCES tickets(id),          -- проставляется при выпуске
    unit_price      bigint NOT NULL,
    discount        bigint NOT NULL DEFAULT 0,
    charge          bigint NOT NULL DEFAULT 0,
    total           bigint NOT NULL,
    UNIQUE (order_id, ordinal)
);

CREATE TABLE order_events (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    order_id   uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    type       text NOT NULL,      -- created|lines_reconciled|paid|amount_mismatch|hold_expired|
                                   -- hold_reacquired|cancelled|ticket_refunded|note
    actor      text NOT NULL,      -- 'gateway:<channel display_number>' | 'user:<uuid>' | 'system'
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX order_events_order_idx ON order_events (order_id, created_at);

ALTER TABLE tickets ADD COLUMN order_id uuid REFERENCES orders(id);
CREATE INDEX tickets_order_idx ON tickets (order_id);

INSERT INTO permissions (code, description) VALUES
  ('order.read',  'Read orders of the organization'),
  ('order.write', 'Cancel / annotate orders of the organization');
```

Backfill существующих `checkout_sessions.state='completed'` в `orders` **не делается** в
миграции (стендовые данные; решение №4 — история на сайтах). Одна строка `order_items` на
одну единицу (билет), не на категорию: это то, что нужно `GET_CART.seatList` и `ticketList`.

### 3.4 `0093_ean13_credentials.sql`

```sql
ALTER TABLE ticket_credentials DROP CONSTRAINT ticket_credentials_type_check;
ALTER TABLE ticket_credentials ADD CONSTRAINT ticket_credentials_type_check
    CHECK (type IN ('static_qr','pdf','ean13'));
-- payload для ean13 — ровно 13 цифр с валидной контрольной; проверяется в Go и CHECK'ом:
ALTER TABLE ticket_credentials ADD CONSTRAINT ticket_credentials_ean13_shape
    CHECK (type <> 'ean13' OR payload ~ '^[0-9]{13}$');
```

`barcodes` (0029) получает по одной строке на билет: `authority = platform`,
`external_ref = <ean13>`, `ticket_id` — это делает EAN-13 видимым для `SCAN_TICKET`,
`/v1/scanner/*` и `barcode_batches` без нового authority-типа.

### 3.5 `0094_wp_webhook_subscribers.sql`

```sql
ALTER TABLE webhook_subscribers ADD COLUMN channel_id uuid REFERENCES sales_channels(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX uq_webhook_subscribers_bil24_wp_per_channel
    ON webhook_subscribers (channel_id) WHERE kind = 'bil24_wp' AND active = TRUE;
```

### 3.6 `0095_api_keys.sql`

```sql
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    channel_id   uuid REFERENCES sales_channels(id) ON DELETE CASCADE,
    name         text NOT NULL,
    key_prefix   text NOT NULL UNIQUE,           -- 12 символов, ищется по нему
    key_hash     text NOT NULL,                  -- bcrypt секрета
    scopes       text[] NOT NULL,                -- коды permissions
    created_by   uuid NOT NULL REFERENCES users(id),
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
INSERT INTO permissions (code, description) VALUES
  ('api_key.manage', 'Issue and revoke organization API keys'),
  ('import.bil24_session', 'Import a Bil24 session (event, tiers, plan, seats) preserving ids');
```

Формат ключа на проводе: `ak_<prefix12>_<secret43>`; показывается один раз в ответе на
создание (как `signing_secret` MACS-вебхука, `hcatalog/macs_webhook.go:92`).

### 3.7 `0096_customer_imports.sql`

```sql
CREATE TABLE customer_imports (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid REFERENCES organizations(id),      -- NULL = мультиорг-файл
    source_label    text NOT NULL,                          -- 'bil24_orders_json' | 'wc_customers_csv' | 'gsheets_csv' | 'brevo_csv' | 'generic_csv'
    file_media_id   uuid NOT NULL REFERENCES media_objects(id),
    mapping         jsonb NOT NULL,                         -- §12.4
    legal_basis     text NOT NULL CHECK (legal_basis IN ('organizer_contract','legitimate_interest','explicit_consent')),
    status          text NOT NULL DEFAULT 'uploaded' CHECK (status IN
                      ('uploaded','dry_run_running','dry_run_done','applying','applied','failed')),
    dry_run_report  jsonb,
    apply_report    jsonb,
    created_by      uuid NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE customer_import_rows (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    import_id            uuid NOT NULL REFERENCES customer_imports(id) ON DELETE CASCADE,
    row_no               int  NOT NULL,
    row_hash             text NOT NULL,                     -- sha256 нормализованной строки → идемпотентность
    raw                  jsonb NOT NULL,
    resolved_customer_id uuid REFERENCES customers(id),
    org_id               uuid REFERENCES organizations(id),
    action               text CHECK (action IN ('created','matched','merge_candidate','skipped')),
    reason               text,
    UNIQUE (import_id, row_hash)
);
```

---

## 4. Правило целочисленных ID (нормативное)

| Что видит сайт | Откуда | Диапазон |
|---|---|---|
| `fid` | `sales_channels.display_number` | 1… (0072) |
| `actionId`, `actionEventId`, `categoryPriceId`, `venueId`, `cityId`, `countryId` | `compatibility_id_map` (`kind`) | arena: ≥ 1 000 000 000; bil24: как в Bil24 |
| `seatId` | `session_seats.system_seat_id` (места **и** GA-юниты) | arena: 1… (0088); bil24: как в Bil24 (2.5e9+), источник — `geometry.seats[].external_id` |
| `ticketId`, `ticketList[].id` | `tickets.system_ticket_id` | 1… (0088) |
| `orderId` (ответ CREATE_ORDER_EXT) | `orders.system_id` | ≥ 1e9 |
| `userId` | `customers.system_id` | ≥ 1e9 |
| `organizerId` | `organizations.display_number` | 1… |
| MACS GA `seatId` | без изменений: `1e9 + system_ticket_id` (17_macs) | — |

Инварианты: один UUID ↔ один `system_id` навсегда; повторный импорт того же `actionEventId` —
обновление; `RegisterExternal` для `system_id ≥ 1e9` → ошибка (`compat.external_id_out_of_range`);
`Resolve` неизвестного ID → `-3`/`101` по таблице §6. Ленивый минтинг делается в той же
транзакции, что и чтение (`INSERT … ON CONFLICT DO NOTHING RETURNING`, затем `SELECT`).

---

## 5. Аутентификация шлюза

1. **Глобальный флаг остаётся выключателем маршрутов** (`BIL24_COMPAT_ENABLED`), **включение — per
   channel**: `sales_channels.settings.gateway = {enabled: bool, token_hash: <bcrypt>,
   token_rotated_at, default_locale}`. Существующий ключ `gateway_token_hash` читается как
   fallback ещё одну волну и переносится admin-эндпоинтом.
2. **Разрешение `fid`**: `fid` (int или строка с числом) → `sales_channels.display_number` →
   канал → `org_id`. Канал удалён/не включён/нет хэша → `-4`. Все команды (§7) требуют fid+token,
   `BIL24_REQUIRE_TOKEN=false` допустим только вне production (как сейчас,
   `config.go:1033-1039`).
3. **Скоуп**: каждая команда работает только с сущностями `org_id` канала. `SCAN_TICKET`
   ищет билет через `barcodes` и проверяет `tickets → sessions → events.org_id = org`;
   `GetSalesChannelByIDGlobal` удаляется.
4. **Провижининг**: `PUT /v1/organizations/{org_id}/channels/{id}/gateway-credential`
   (permission `channel.update`, `X-Admin-Reason`) → генерирует 32-байтный секрет, пишет bcrypt,
   ставит `enabled=true`, отвечает `{fid, token, base_url, image_url, rotated_at}` — **токен один
   раз**. `DELETE` → `enabled=false`. `GET` → `{fid, enabled, rotated_at}` без токена.
   Admin-web: секция «Bil24-совместимый шлюз» в карточке канала по образцу MACS-секции
   (`organizations.tsx:1041-1060`).
5. **`GET /compat/bil24/image`** (§8): аутентификация только по `fid` (сайт не шлёт токен), плюс
   требование `events.status='published'` — та же поверхность, что публичный `layout.svg`.

---

## 6. Коды результата, описания, локализация

| Код | Смысл | Когда |
|---|---|---|
| `0` | OK | всегда, когда операция выполнена, в т. ч. идемпотентные повторы |
| `1` | сессия шлюза не найдена / истекла | любой `userId`/`sessionId`, не найденный в `gateway_sessions` или с `expires_at < now()`. Сайт после этого пересоздаёт пользователя (`class-bil24-seat-picker.php:757`, `vino-checkout-rest.php:77`) |
| `101` | бизнес-ошибка, `description` показывается покупателю | место занято, категория распродана, продажи закрыты, промокод невалиден, бронь истекла, событие не найдено в каталоге канала |
| `-1` | временная ошибка, повторите | ошибки БД/пула, дедлок, таймаут worker'а. **Больше не «неизвестная команда»** |
| `-2` | неверный запрос | неизвестная команда, отсутствующее/некорректное поле, JSON не разобран |
| `-3` | не найдено | ID вне каталога/скоупа канала (там, где это не пользовательская ситуация) |
| `-4` | нет доступа | fid/token |
| `-5` | не реализовано | остаётся для команд легаси-виджета (`AUTH`, `GET_ORDERS`, …) |
| `-99` | внутренняя ошибка | panic-recovery |

`description` локализуется по `locale` запроса (`ru-RU`→`ru`, `en-GB`→`en`, `he-IL`→`he`,
`cs-CZ`→`cs`; неизвестный → `default_locale` канала → `en`) через существующий `platform/i18n`
бандл: добавить `locales/ru.toml`, `he.toml`, `cs.toml` с ключами `bil24.*`
(`bil24.seat_taken`, `bil24.category_sold_out` с параметрами `{name}`, `{available}`,
`bil24.session_expired`, `bil24.sales_closed`, `bil24.promo_invalid`, `bil24.hold_expired`,
`bil24.order_not_found`, …). Для `0` описание `"OK"`. Ответ всегда HTTP 200 (кроме
отключённого шлюза — 404 и `image` — 404/304).

`resultCode` пишется всегда (сайт `GET_ALL_ACTIONS` требует его присутствия,
`bil24-acf-sync.php:337`).

---

## 7. Команды: контракт на проводе

Общий конверт: `{command, fid, token, locale, …}` → `{resultCode, description, command, …}`.
Числовые ID принимаются и как число, и как строка с числом (`"2593277"`); в ответах — только
числа. Деньги в ответах — число с ≤2 знаками (`26.25`), не строка.

Ниже для каждой команды: запрос (ключи, которые реально шлёт PHP), ответ (именованная Go-структура
в `bil24compat`, все поля обязательны, если не сказано `omitempty`), семантика, ошибки.

### 7.1 `GET_ALL_ACTIONS`

Запрос: только конверт. Ответ — `GetAllActionsResponse`:

```json
{
  "resultCode": 0, "description": "OK", "command": "GET_ALL_ACTIONS",
  "countryList": [{"countryId": 1000000001, "countryName": "Czechia"}],
  "cityList": [{"cityId": 1000000002, "cityName": "Praha", "countryId": 1000000001,
    "venueList": [{"venueId": 1000000003, "venueName": "Palác Akropolis",
                   "address": "Kubelíkova 27", "geoLat": 50.0806, "geoLon": 14.4508}]}],
  "actionList": [{
    "actionId": 1000000010, "actionName": "…", "fullActionName": "…",
    "description": "<html как в events.description>",
    "smallPosterUrl": "https://…", "bigPosterUrl": "https://…",
    "minPrice": 350, "maxPrice": 900, "age": "12+",
    "organizerId": 7, "organizerName": "Lampyris Events s.r.o.",
    "firstEventDate": "26.04.2026", "lastEventDate": "27.04.2026",
    "actionEventList": [{
      "actionEventId": 1000000011, "cityId": 1000000002, "venueId": 1000000003,
      "day": "26.04.2026", "time": "19:00", "currency": "CZK",
      "sellEndTime": "2026-04-26T18:00:00+02:00",
      "seatingPlanId": 1000000030, "seatingPlanName": "Big hall",
      "eTicket": true, "availability": 212, "minPrice": 350, "chargePercent": 5,
      "categoryLimitList": [{"categoryList": [
        {"categoryPriceId": 1000000021, "categoryPriceName": "Standing", "placement": false,
         "price": 350, "availability": 120, "tariffIdMap": {}}]}],
      "tariffPlanList": []
    }]
  }]
}
```

Семантика:
- Только события `org_id` канала со `status='published'` и хотя бы одним сеансом со
  `status='scheduled'` и `start_at > now() − 6h`. `visibility` не фильтр (сайт сам решает,
  что показывать). Удалённые — нет.
- `day`/`time` — `sessions.start_at` в `venues.timezone`; NULL timezone → сеанс **не отдаётся**,
  в лог `warn bil24.venue_timezone_missing` (акцепт-чеклист стенда требует TZ у всех площадок).
- `sellEndTime` — `min(tier.sale_window_end)` по тарифам, иначе `start_at`; RFC3339 с офсетом.
- `availability` сеанса — остаток: `count(session_seats.status='available')` для сеансов с
  местами/юнитами, иначе `capacity − sold − held` из `inventory_ledger`.
- `categoryLimitList[0].categoryList` — **только GA-тарифы** (тарифы без мест: `admission_mode
  = general_admission` или GA-юниты в hybrid), `placement:false`. Тарифы с местами сюда не
  попадают — так сайт различает «чисто рассадка» (`categoryLimitList` пуст) и «combined»
  (`bil24-acf-sync.php:434-446`). `price` — `priceresolve.ForTier` на момент ответа.
- `seatingPlanId` = `actionEventId` для сеансов с местами (сайт использует его только как
  признак «есть план» и ключ кэша пробы `_bil24_combined_probe`, `bil24-acf-sync.php:434-446`),
  `0` для чисто GA; `seatingPlanName` = `seating_plans.name`.
- `minPrice/maxPrice` события — по всем показанным тарифам; `age` — `events.age_rating`
  (`NR` → `""`).
- Постеры — публичный URL `media_objects` постера сеанса (0082) с fallback на `events.image_url`.
- Локализация `actionName/description` — как сейчас (`i18n_text`, `events.sql:98-109`).

### 7.2 `GET_SEAT_LIST`

Запрос: `{actionEventId, availableOnly?: bool}`. Ответ — `GetSeatListResponse`:

```json
{"resultCode": 0, "description": "OK", "command": "GET_SEAT_LIST",
 "currency": "CZK",
 "categoryList": [
   {"categoryPriceId": 1000000020, "categoryPriceName": "Parter", "price": 900,
    "availability": 84, "placement": true, "tariffIdMap": {}},
   {"categoryPriceId": 1000000021, "categoryPriceName": "Standing", "price": 350,
    "availability": 120, "placement": false, "tariffIdMap": {}}],
 "seatList": [
   {"seatId": 1731, "categoryPriceId": 1000000020, "tariffPlanId": null, "price": 900,
    "available": true, "location": {"sector": "Parter", "row": "3", "number": "12"}}]}
```

- `placement` — тристейт как у Bil24: `true` для тарифов с местами, `false` для GA, ключ
  отсутствует у тарифов сеансов без плана (чисто GA) — сайт считает отсутствие ключа
  рассадкой только внутри плана (`class-bil24-seat-picker.php:554-555`), а для чисто GA
  использует только `categoryList`.
- `seatList`: для `assigned_seats` — все места (`kind='seat'`); для `hybrid` — места **и**
  GA-юниты как псевдо-места (`location` = `{"sector": "<tier name>", "row": "", "number": ""}`,
  категория с `placement:false`); для `general_admission` — `[]`.
- `available` = `status='available'`; `availableOnly:true` фильтрует `seatList`, `categoryList`
  всегда полный; `availability` категории = число доступных мест/юнитов, для безлимитного GA
  без юнитов — `capacity − sold − held`.
- `price` — цена места: `priceresolve.ForTier(tier)`; `tariffPlanId` всегда `null`,
  `tariffIdMap` всегда `{}` (тарифных планов в arena нет — сайт создаёт вариацию `default-tariff`).
- Сеанс не в скоупе/не опубликован → `-3`.

### 7.3 `CREATE_USER`

Запрос: `{email?, firstName?, lastName?, phone?}` (все опциональны, `class-bil24-orders.php:1404`).
Ответ — `CreateUserResponse{userId int64, sessionId string}`.

- Покупатель резолвится по §12.2 (`email`/`phone` сильные ключи); без ключей — новый
  анонимный покупатель. `display_name` = `firstName + " " + lastName` если задано.
- `gateway_sessions`: `session_token` — 32 байта `crypto/rand` base64url (43 символа);
  `expires_at = now() + 30d` (скользящее: продлевается при каждой команде с этой сессией);
  `locale` из запроса.
- Идемпотентность не нужна (сайт хранит `userId`/`sessionId` в WC-сессии/мете).

### 7.4 `RESERVATION`

Запросы (четыре формы, `class-bil24-orders.php:170-181`, `:1097-1116`,
`class-bil24-seat-picker.php:750-777`):

```
{type:"RESERVE"|"UN_RESERVE", userId, sessionId, actionEventId, categoryList:[{categoryPriceId, quantity, tariffPlanId?}]}
{type:"RESERVE"|"UN_RESERVE", userId, sessionId, actionEventId, seatList:[{seatId}]}
{type:"UN_RESERVE_ALL", userId, sessionId}
```

Ответ — `ReservationResponse`:

```json
{"resultCode": 0, "description": "OK", "command": "RESERVATION",
 "cartTimeout": 1487,
 "currency": "CZK", "sum": 1250, "discount": 0, "charge": 62.5, "totalSum": 1312.5,
 "seatList": [
   {"seatId": 1731, "actionEventId": 1000000011, "categoryPriceId": 1000000020,
    "tariffPlanId": null, "price": 900, "discount": 0},
   {"seatId": 900012, "actionEventId": 1000000011, "categoryPriceId": 1000000021,
    "tariffPlanId": null, "price": 350, "discount": 0}]}
```

Семантика корзины:
- **Корзина сессии шлюза** = множество `reservations` со `state='active'` и
  `gateway_session_id = <сессия>`, ровно одна на `session_id`. `RESERVE` для сеанса без
  reservation создаёт её (`hcheckout.CreateSeatedHold`/`CreateGAHold`); для существующей —
  **расширяет** (новые функции `hcheckout.ExtendHold(tx, reservationID, seats|ga)` и
  `ShrinkHold(tx, reservationID, seats|ga)`, та же блокировка `LockSessionSeatsForHold`,
  `AllocateGAUnitsTx`, `IncrementSessionSeatStatusVersion`, запись `reservation_ga_items`
  с `unit_price` из `priceresolve`). `UN_RESERVE categoryList` снимает N последних юнитов
  категории; `UN_RESERVE seatList` — конкретные места; если после снятия корзина сеанса пуста —
  reservation → `cancelled`.
- `UN_RESERVE_ALL` — все reservation'ы сессии → `cancelled`, ответ с пустым `seatList`,
  `cartTimeout: 0`.
- Каждый успешный `RESERVE` продлевает `expires_at` всех reservation'ов сессии до
  `now() + TTL` (TTL: `reservation_ttl_override` канала, иначе `DefaultReservationTTL`).
  `cartTimeout` = секунд до ближайшего `expires_at` (0, если корзина пуста).
- `seatList` в ответе — **вся корзина сессии по всем сеансам** (сайт матчит по `seatId`,
  `class-bil24-seat-picker.php:937-950`, и считает количество строками, `:474`). GA-юнит =
  строка с его `system_seat_id`.
- Валюты: корзина одной сессии — одна валюта; попытка добавить сеанс в другой валюте → `101`
  `bil24.currency_mismatch`.
- Ошибки: `userId`/`sessionId` не найдены или истекли → `1`; сеанс не в скоупе → `-3`; продажи
  закрыты (`sale_window` тарифа, сеанс `cancelled`, событие не `published`) → `101`; место
  `held/sold/blocked` → `101 bil24.seat_taken {sector,row,number}` (сайт шлёт по одному месту,
  `class-bil24-seat-picker.php:769-797`, так что описание точечное); категория: недостаточно
  юнитов → `101 bil24.category_sold_out {name, available}`; `pwyw`-тариф → `101
  bil24.pricing_mode_unsupported`; `seatList` и `categoryList` вместе → `-2`.
- Существующие проверки конфликтов SEAT-C1 сохраняются; `seat_status_version` растёт при
  каждом изменении — виджет arena видит те же холды.

### 7.5 `GET_CART`

Запрос: `{userId, sessionId}`. Ответ — `GetCartResponse`:

```json
{"resultCode": 0, "description": "OK", "command": "GET_CART",
 "cartTimeout": 1400, "currency": "CZK",
 "sum": 1250, "discountAmount": 125, "chargeAmount": 56.25, "totalSum": 1181.25,
 "actionEventList": [{
   "actionEventId": 1000000011, "chargePercent": 5,
   "seatList": [{"seatId": 1731, "categoryPriceId": 1000000020, "tariffPlanId": null,
                 "price": 900, "discount": 90}]}]}
```

- `totalSum` — единственный итоговый ключ (сайт читает `totalAmount`→`estimatedTotal`→
  `estimateTotal`→`totalSum`, последний непустой побеждает, `class-bil24-orders.php:452-456`;
  отдаём только `totalSum`).
- `discountAmount` — скидка по промокодам сессии (§7.6), распределённая по строкам
  пропорционально (`discount` в строке; остаток — на последнюю строку, как в AB-50i).
- `chargePercent` — `sales_channels.fee_percent` (целое, `int(fee_percent)`; дробный
  `fee_percent` → округление вниз в `chargePercent`, но `charge` считается точно из bp).
- Пустая корзина → `actionEventList: []`, суммы `0`, `cartTimeout: 0`, `resultCode 0`.

### 7.6 `ADD_PROMO_CODES`, `CHECK_KDP`

Запросы: `{userId, sessionId, promoCodeList?: [..], promoCodes?: [..]}` (оба ключа, `cart-bridge.php:1127-1133`;
иногда только `promoCodes`, `:1021`); `CHECK_KDP {userId, sessionId, promoCode}`.

Ответ `ADD_PROMO_CODES` — `AddPromoCodesResponse{newPromoCodeList, existPromoCodeList, errorPromoCodeList []string}`;
`CHECK_KDP` — только конверт.

- Объединение обоих ключей, дедуп, ≤10 кодов, регистр как у `promo_codes.code` (сравнение
  без регистра).
- Классификация: код уже в `gateway_sessions.promo_codes` → `exist`; валиден по
  `ValidatePromoForLines` против строк корзины (org корзины; пустая корзина → проверяется
  только существование/активность/окно) → `new` и добавляется; иначе → `error`,
  `description` = локализованная причина первого ошибочного.
- Скидка в `GET_CART`/`CREATE_ORDER_EXT`: применяется **один** код — первый из списка
  сессии, дающий ненулевую скидку (ограничение `checkout_sessions.promo_code_id`;
  задокументировать в `BEHAVIOR_DIFFERENCES.md`). Redemption пишется в `PAY_ORDER`.
- `CHECK_KDP`: валиден → `0`, иначе `101` с причиной. Порядок `RESERVATION → ADD_PROMO_CODES →
  CREATE_ORDER_EXT` не обязателен (движок пересчитывает по текущей корзине), но поддерживается.

### 7.7 `CREATE_ORDER_EXT`

Запрос (`class-bil24-orders.php:1489-1499`, `:886-923`):

```json
{"command":"CREATE_ORDER_EXT","orderId":"12345","userId":1000000100,"sessionId":"…",
 "currency":"CZK","total":1312.5,"actionEventId":1000000011,"longReservation":false,
 "lines":[{"categoryPriceId":1000000020,"quantity":2,"tariffPlanId":null}],
 "email":"a@b.cz","phone":"+420…","fullName":"Jan Novák","chargePercent":5,
 "promoCodes":["SPRING"]}
```

Ответ — `CreateOrderResponse`:

```json
{"resultCode": 0, "description": "OK", "command": "CREATE_ORDER_EXT",
 "orderId": 1000000500, "externalOrderId": "12345",
 "sum": 1250, "discount": 0, "charge": 62.5, "totalSum": 1312.5, "currency": "CZK",
 "expiration": "2026-04-16T14:54:55+02:00"}
```

Алгоритм (одна транзакция, `ordering.CreateOrderFromCheckout`):

1. Сессия шлюза (`1`), сеанс в скоупе (`-3`), продажи открыты (`101`).
2. Reservation корзины для `actionEventId`; нет — создаётся пустой и заполняется по `lines`
   (preflight-сценарий сайта, `bil24_reserve_preflight`).
3. **Сверка `lines` с корзиной** по `categoryPriceId`: `quantity > held` → дозарезервировать
   разницу (как `RESERVE`, ошибка → `101`), `quantity < held` → снять лишнее (последние
   юниты; для мест — не трогать, места сайт сверяет сам через `reconcile_seat_holds`); категории
   корзины, отсутствующие в `lines`, снимаются. Итог: корзина сеанса == `lines`. Событие
   `order_events.lines_reconciled` с дельтой.
4. Покупатель: дорезолв по `email`/`phone`/`fullName` (§12.2), привязка `reservations.customer_id`.
5. **Правило одного открытого заказа**: если у покупателя есть `orders.status='pending_payment'`
   на этот сеанс и его reservation жива — вернуть **тот же `orderId`**, обновив `external_ref`,
   строки и суммы (заказ не дублируется, WC может создавать свои pending-заказы). Если
   reservation истекла — старый заказ → `expired`, создаётся новый.
6. `checkout_sessions`: `InsertCheckoutSessionWithToken` + `ConfirmCheckoutSession`
   (`pricing_confirmed`) с `PricingRules{PlatformFeeBP: fee_percent×100}` и промокодом
   (первый валидный из `promoCodes` запроса ∪ `gateway_sessions.promo_codes`).
7. `orders` + `order_items` (одна строка на юнит/место, `unit_price` из
   `reservation_ga_items.unit_price`), `status='pending_payment'`, `expires_at =
   reservation.expires_at`, `external_ref = orderId` запроса, `source='bil24_gateway'`.
8. Ответ: суммы из `PricingBreakdown`. `total`/`chargePercent`/`expectedPrice` запроса — только
   в `order_events.created.payload.client_reported`.

Ошибки: `lines` пуст или без валидных категорий → `-2`; `orderId` пуст → `-2`
(сайт всегда шлёт); категория другого сеанса → `101 bil24.line_wrong_session`.

### 7.8 `GET_ORDER_INFO`

Запрос: `{orderId, userId?, sessionId?}` (`bil24-gsheets-sync.php:102-111`). Ответ:
`{…конверт, "order": <Bil24 Order без ticketList (§9.3)>}` — строгий режим
`01_api_compatibility_gateway_ru.md`. Поле `userMessage` дублирует `description` при
`resultCode ≠ 0`. Заказ другого канала/org → `-3`.

### 7.9 `PAY_ORDER`

Запрос (`class-bil24-orders.php:1210-1218`): `{orderId, userId, sessionId, amount, currency, method}`.
Ответ: только конверт.

Алгоритм:

1. Заказ по `orders.system_id` в скоупе канала (`-3`); `status='paid'` → `0` (идемпотентно);
   `cancelled/refunded` → `101 bil24.order_cancelled`.
2. Бронь жива → шаг 4. Бронь истекла: попытка **повторно захватить те же места/юниты**
   (`hcheckout.ReacquireHold`); успех → `order_events.hold_reacquired`; неуспех → заказ и
   checkout_session → `manual_review`, `order_events.hold_expired`, ответ `101
   bil24.hold_expired` **и** алерт оператору (аудит + лог `error`): деньги уже взяты сайтом,
   ситуация разрешается вручную. (Это единственный `101` после оплаты; сайт ставит
   `bil24_ext_status=pay_failed` и заказ виден в консоли.)
3. `amount` ≠ `orders.total` (с точностью до 0.01) → **не блокировать**, `order_events.amount_mismatch`.
4. Транзакция: `payment_intents` (`provider='manual'`, `provider_payment_id='wc:<external_ref>:<method>'`,
   `state='succeeded'`, `amount=orders.total`), `checkout_sessions → completed`,
   reservation → `converted` (`convertjob` логика inline), promo redemption, `orders.status='paid'`,
   `paid_at`, `payment_method=method`, `customer_org_links` upsert, `customer_identities.verified_at`
   для e-mail/телефона заказа (факт оплаты).
5. После коммита — **синхронный** `htickets.IssueTicketsForCheckout` (идемпотентен по
   `tickets_checkout_ordinal_uq`) с `holder_email = orders.buyer_email`, `tickets.order_id`,
   `order_items.ticket_id`; затем EAN-13 (§11) и событие `v1.order.paid` (§9.1). Job
   `checkout.issue_tickets` ставится дополнительно как страховка. Сайт опрашивает
   `GET_TICKETS_BY_ORDER` 5 раз с задержками 2/4/8 с (`:1683-1707`) — синхронный выпуск
   гарантирует ответ с первого раза.
6. Delivery-письма arena для заказов шлюза **не ставятся** (сайт шлёт свой PDF; `SEND_TICKETS_TO_EMAIL`
   ставит их явно). Флаг на канале `settings.gateway.platform_email = false` по умолчанию.

### 7.10 `GET_TICKETS_BY_ORDER`

Запрос: `{orderId (int|string), userId, sessionId, rawCoordinates?}`. Ответ —
`GetTicketsByOrderResponse`:

```json
{"resultCode": 0, "description": "OK", "command": "GET_TICKETS_BY_ORDER",
 "ticketList": [{"ticketId": 4021, "pdfUrl": "https://api.…/v1/public/checkout/<token>/tickets/<uuid>/pdf",
                 "downloadUrl": "<тот же>", "barcode": "2100000040218", "seatId": 1731,
                 "categoryPriceId": 1000000020}],
 "ticketIdList": [4021]}
```

Заказ не оплачен / билеты ещё не выпущены → `0` с пустыми списками. `pdfUrl` требует
`PUBLIC_BASE_URL` (новая обязательная переменная конфигурации при `BIL24_COMPAT_ENABLED`).

### 7.11 `SEND_TICKETS_TO_EMAIL`

`{userId, sessionId, email, ticketIdList:[…]}` → `0`; ставит `delivery_jobs` на каждый билет
заказа с `recipient_email`. На обоих сайтах команда отключена фильтром, реализуется для полноты.

### 7.12 `CANCEL_RESERVATION`, `CANCEL_ORDER`

- `CANCEL_RESERVATION {reservationId?, orderId?}` — снимает холд **неоплаченного** заказа
  (`orders.status='pending_payment'` → `cancelled`, reservation → `cancelled`); ID не найден →
  `0` (сайт не проверяет код, `class-bil24-orders.php:1278-1282`); оплаченный → `101`.
- `CANCEL_ORDER {orderId}` — то же для неоплаченного; оплаченный → `101 bil24.use_refund_ticket`.

### 7.13 `REFUND_TICKET` (расширение arena)

Запрос: `{ticketId, reason?, refundPrice?}`. Ответ: конверт + `{ticketId, refundDate}`.
Обёртка над `htickets.HandleCancelTicket`-логикой (`cancel.go:174-276`) с `refund_mode='manual'`
и `reason` по умолчанию `"REFUND_TICKET via gateway fid=<fid>"`; актор аудита —
`gateway:<fid>`. Билет должен принадлежать заказу канала (`-3` иначе). Уже отменённый → `0`.
Дальше штатно: `v1.ticket.cancelled` → вебхук `ticket.refunded` в сайт (§9.2) и в MACS.
`refundPrice` пишется в `tickets.refund_price`, `orders.status` → `refunded`/`partially_refunded`.

### 7.14 `SCAN_TICKET`

Как сейчас, плюс: канал по `fid` (§5), поиск штрих-кода по `barcodes` **всех** authority
(`platform` для EAN-13, `legacy_bil24` для импортированных партий), проверка `org_id`.

### 7.15 `GET_SCHEMA`

Остаётся для виджета/партнёров; сайту не нужен. Ключи `seatId` — `system_seat_id` (не UUID).

---

## 8. SVG-схема: `GET /compat/bil24/image`

URL: `GET /compat/bil24/image?type=seatingPlan&actionEventId=<int>&userId=0&fid=<int>&locale=<loc>`
(`class-bil24-seat-picker.php:377-381`; host = API-base без `/json`, поэтому сайт получит
`https://api.arena…/compat/bil24/image`). `type ≠ seatingPlan` → 404.

Формат (JS читает `sbt:`-префикс и namespace `http://www.w3.org/2015/sbt/1.0`,
`bil24-seat-picker.js:389-394`):

```xml
<svg xmlns="http://www.w3.org/2000/svg" xmlns:sbt="http://www.w3.org/2015/sbt/1.0"
     viewBox="0 0 1200 800" sbt:statusVersion="42">
  <metadata>
    <sbt:category sbt:id="1000000020" sbt:index="1" sbt:name="Parter" sbt:color="#e53935"
                  sbt:price="900" sbt:class="cat-1"/>
  </metadata>
  <g id="Decor">…</g>
  <g sbt:sect="Parter">
    <g sbt:row="3">
      <circle sbt:id="1731" sbt:state="1" sbt:cat="1" sbt:seat="12" cx="…" cy="…" r="6" fill="#e53935"/>
    </g>
  </g>
</svg>
```

- `sbt:id` = `system_seat_id`; `sbt:cat` = **индекс** категории (`sbt:index`), не ID;
  `sbt:state` — `1` свободно, `4` занято (`held/sold/blocked`); GA-зоны — декор без `sbt:id`.
- Категории в `<metadata>` — `localName='category'`; порядок индексов = `categoryIndex`
  геометрии. `viewBox` обязателен; `width/height` не нужны (JS их удаляет).
- Кэш: `ETag = "<geometry_checksum>:<seat_status_version>"`, `Cache-Control: no-cache`,
  `If-None-Match` → 304 (как `layout.svg`). Сайт кэширует 90 с и перештамповывает состояния из
  `GET_SEAT_LIST`, так что SVG может быть слегка устаревшим.
- Реализация — второй кодировщик рядом с `RenderBSSLayoutSVG` (`hseating/layout_svg.go:211`)
  над той же геометрией; `hseating.RenderSBT10SVG`. Роут монтируется в `bil24_shims.go` без
  JWT; `fid` → канал → org, сеанс должен быть `published` и в org.

---

## 9. Вебхуки в сайт (`kind='bil24_wp'`)

### 9.1 События платформы

Новые outbox-события (`aggregate_type='order'`):

| Тип | Кто публикует | Payload |
|---|---|---|
| `v1.order.paid` | `ordering.MarkPaid` после выпуска **всех** билетов заказа (последний ordinal) | `{order_id, org_id, channel_id, session_id, ticket_count}` |
| `v1.order.cancelled` | `ordering.Cancel` (неоплаченный) | `{order_id, org_id, channel_id, session_id, reason}` |
| `v1.event.published`, `v1.event.updated`, `v1.session.updated`, `v1.session.cancelled` | `hcatalog` (публикация/PATCH/статус) | `{event_id, org_id, session_ids[]}` |

`v1.ticket.cancelled/refunded/revoked` остаются как есть (`hscanner/scanner_events.go:74-86`).

### 9.2 Диспетчер `bil24wire.Dispatcher`

Третий участник `multiDispatcher` в `cmd/arena-worker/main.go:173-176`. Подписчик находится по
`webhook_subscribers.kind='bil24_wp' AND channel_id = <канал заказа/события> AND active`.
Для `v1.event.*` — все `bil24_wp`-подписчики каналов org, в которых событие опубликовано
(`event_publications` → feed-token → channel).

| Платформа | Сайт (`type`) | `data` |
|---|---|---|
| `v1.order.paid` | `order.paid` | Bil24 Order (§9.3) со `status:"PAID"`, `ticketList` |
| `v1.order.cancelled` | `order.cancelled` | `{id, status:"CANCELLED"}` |
| `v1.ticket.cancelled` / `refunded` / `revoked` | `ticket.refunded` | `{id, orderId, seatId, barcode, refundPrice, refundDate, category, holderStatus:"REFUND", actionEvent{…}}` |
| `v1.event.published` / `updated` / `session.*` | `event.created` / `event.changed` / `event.deleted` | `[{actionEventId}]` (сайт только считает элементы и запускает синк, `receiver.php:63-72`) |
| регистрация (PUT) | `test` | `null` |

Конверт: `{"id": <int из outbox uuid, как MACS>, "created": RFC3339 UTC, "type", "data"}` —
надмножество того, что читает приёмник (`type`, `data`). Заголовки: `Content-Type: application/json`,
`X-Arena-Event-Type`, `X-Arena-Signature: sha256=<hex>` (HMAC по телу, если `signing_secret`
задан — сайт не проверяет, но оставляем путь). Успех — HTTP 2xx (приёмник отвечает 200
`{"ok":true}`, 400 на битый конверт). Ретраи — outbox (`MaxAttempts 30`, ~24 ч), как MACS.
`ticket.refunded` дедупится сайтом по `data.id` (`receiver.php:171-185`) — повторы безопасны;
`order.paid` идемпотентен по статусу WC-заказа.

Регистрация: `PUT /v1/organizations/{org_id}/channels/{id}/wp-webhook {callback_url, signing_secret?}`
(`channel.update`) — деактивировать старую, создать новую, **синхронно** отправить `test` и
вернуть `{…, "test_delivery": {"ok": bool, "http_status": 200}}`; `GET`/`DELETE`.
Admin-web — секция в карточке канала рядом с §5.4.

### 9.3 Форма Order/Ticket (`bil24wire`)

Поверх нейтральной проекции `orderexport.Order` (перенос `macs/export.go:27-103, 465-530`):

```json
{"id": 1000000500, "date": "2026-04-16T14:23:55+02:00", "status": "PAID",
 "user": {"id": 1000000100, "email": "a@b.cz"},
 "agent": {"id": 7, "name": "Lampyris Events s.r.o."},
 "frontend": {"id": 3, "agentId": 7, "name": "https://lampyrisevents.com/", "type": {"id": 8, "name": "Ticketing system"}},
 "currency": "CZK", "paymentMethod": {"id": 0, "name": "stripe"}, "longReservation": false,
 "expiration": "…", "processing": "…",
 "sum": 1250, "discount": 0, "charge": 62.5, "totalSum": 1312.5, "ticketQuantity": 2,
 "filteredSum": 1250, "filteredDiscount": 0, "filteredCharge": 62.5, "filteredTotalSum": 1312.5, "filteredTicketQuantity": 2,
 "paymentBankMessage": "Paid per protocol", "paymentBankId": "", "paymentBankStatus": "",
 "email": "a@b.cz", "phone": "+420…", "fullName": "Jan Novák", "emailSent": null,
 "seatList": [], "gatewayOrderList": [],
 "acquiring": {"id": 0, "systemId": 0, "name": "", "systemName": "", "agentId": 0, "agentName": ""},
 "ticketList": [{
   "id": 4021, "seatId": 1731, "orderId": 1000000500,
   "seatLocation": {"sector": "Parter", "row": "3", "number": "12"},
   "category": "Parter", "tariff": null,
   "price": 900, "discount": 0, "charge": 45, "totalPrice": 945, "discountReason": null,
   "barcode": "2100000040218", "barcodeFormat": {"id": 0, "name": "EAN-13"},
   "actionEvent": {"id": 1000000011, "cityId": 1000000002, "cityName": "Praha",
     "venueId": 1000000003, "venueName": "Palác Akropolis",
     "actionId": 1000000010, "actionName": "…", "actionLegalOwner": "Lampyris Events s.r.o.",
     "actionLegalOwnerInn": "", "actionKind": {"id": 0, "name": "Events"},
     "currency": "CZK", "showTime": "2026-04-26T19:00:00", "eTickets": true,
     "gateway": {"id": 0, "systemId": 0, "name": "", "systemName": "NONE", "organizerId": null, "organizerName": null}},
   "holderStatus": "NEVER_USE", "refundDate": null, "refundPrice": null}]}
```

Правила: полный набор из 36 ключей заказа / 17 ключей билета / 14 ключей `actionEvent`
(инвентарь §6 отчёта агента, тест на набор ключей — **binding**, как AB-50i);
`category` — строка (имя тарифа); `seatLocation` — `null` для GA, объект для мест;
`showTime` — локальное время площадки без TZ; `actionEvent.id` = `actionEventId` **сеанса**;
`holderStatus` — `NEVER_USE` | `REFUND`; `refundDate` — RFC3339 с офсетом; `charge` билета —
пропорционально `orders.charge` (остаток на последний); `discountReason` — `"Промокод <code>"`
или `null`; `actionLegalOwner` = `organizations.legal_name` (fallback `name`),
`actionLegalOwnerInn` = `organizations.tax_id` (если есть) или `""`.

---

## 10. MACS (arena-сторона)

| # | Изменение |
|---|---|
| М1 | `macs.Dispatcher` слушает `v1.order.paid` (не `v1.scanner.ticket.issued`) и шлёт `order.paid` с `data = {id, status:"PAID", ticketList:[…]}` из `orderexport`; билеты без заказа (комплименты) — синтетический заказ из одного билета, как сегодня. `ticket.refunded` не меняется. |
| М2 | Успех — HTTP 2xx **и** тело `{"status":"OK"}`; `{"status":"Error"}` при 200 → ошибка → ретрай outbox (`class-lops-macs.php:134-137` подтверждает контракт). |
| М3 | `actionEvent.id` = `actionEventId` сеанса из `compatibility_id_map` (не хэш UUID события, `17_macs:72`); `actionEvent.actionId` = `actionId`. |
| М4 | `barcode` = EAN-13 из §11, `barcodeFormat {id:0, name:"EAN-13"}` (в выгрузках Bil24 `id:0`, MACS принимает и 1). URL подписчика должен оканчиваться на `/api/_wh/tickets` — валидация в PUT. |
| М5 | Стаб `macs/stub` отвергает `order.paid` без `data.ticketList` или `data.status != "PAID"` ответом 200 `{"status":"Error"}`; тесты round-trip AB-50g обновляются под М1–М4. |

Смена `actionEvent.id` (М3) меняет ключ события в MACS для уже импортированных тестовых
сеансов — на проде MACS сегодня нет arena-событий, предупредить в runbook.

---

## 11. Штрих-коды EAN-13

- Пакет `internal/platform/barcodes/ean13`: `Encode(prefix string, n int64) string`,
  `Valid(s string) bool` (стандартная контрольная цифра, weights 1/3).
- Номер: `"21" + zeroPad10(system_ticket_id) + check` — 13 цифр; префикс `21` (GS1
  «внутреннее использование» 20–29) отличает наши коды от Bil24 (`24…`) — штрих-код в MACS
  глобально уникален (`17_macs` / отчёт §2.3).
- При выпуске билета (`IssueTicketsForCheckout`) создаются `ticket_credentials(type='ean13')`
  и `barcodes(authority=platform, external_ref=<ean13>, ticket_id)`. Существующим билетам —
  бэкфилл-команда `arena-migrate --backfill-ean13` не нужна на проде (продаж ещё нет), для
  стенда — job `tickets.backfill_ean13`.
- Используется: `ticketList[].barcode` (сайт, MACS), `SCAN_TICKET`, `GET_TICKETS_BY_ORDER`.
  `static_qr` остаётся для виджета/PDF arena; PDF arena печатает номер EAN-13 текстом под QR
  (открытый вопрос №3 отчёта — читает ли MACS 1D в режиме QR).
- Отмена/ревокация: `RevokeTicketArtifactsTx` ревокует `static_qr`, `pdf`, `ean13` (исправление
  `"qr"` → `"static_qr"`) и `barcodes.status='revoked'`.

---

## 12. Покупатель

### 12.1 Модель — §3.2. Один покупатель на платформу; согласия и связи — по организациям.

### 12.2 Резолюция (`customers.Resolve`)

Вход: `{email?, phone?, name?, channel_id, device_token?, wc_customer_id?}`; выход — покупатель.

1. Нормализовать. Сильные ключи: `email`, `phone`. Слабые: `device` (= `session_token` шлюза),
   `wc_customer`.
2. Найти по каждому сильному ключу. Оба указывают на одного → он. Один найден → он, второй
   ключ добавляется как идентичность (unverified). **Оба найдены, но разные покупатели** →
   вернуть покупателя по e-mail, телефон **не** переприсваивать, создать
   `customer_merge_candidates(reason='email_of_a_phone_of_b')`.
3. Ничего не найдено → искать по слабому ключу в пределах канала; найден → добавить сильные
   ключи к нему; нет → создать нового.
4. `display_name`: не перезаписывать непустое имя пустым; новое непустое — обновить.
5. `last_seen_at` идентичностей обновляется всегда; `verified_at` ставит только `PAY_ORDER`
   (факт оплаты) или явное подтверждение.

Точки вызова: `CREATE_USER` (§7.3), `CREATE_ORDER_EXT` (§7.7 шаг 4), `PAY_ORDER` (verified),
нативный публичный чекаут (`hfeed/public_feed_checkout.go:308-338` — имя/телефон перестают
выбрасываться, `orders.buyer_*` заполняются).

### 12.3 Чтение

- `GET /v1/organizations/{org_id}/customers?q=` (`customer.read`) — только покупатели с
  `customer_org_links.org_id = org`; поиск по e-mail/телефону/имени (trgm по `orders`, точное
  совпадение по идентичностям).
- `GET /v1/organizations/{org_id}/customers/{id}` — карточка: идентичности (сильные —
  маскированные, кроме подтверждённых), заказы org, атрибуты org + платформенные, согласия org.
- `GET /v1/admin/customers/{id}` (`platform.superadmin`, `X-Admin-Reason`) — сквозной профиль.

### 12.4 Импорт баз (C7, `platform.superadmin`)

- `POST /v1/admin/customer-imports` `{file_media_id, source_label, org_id?, mapping, legal_basis}`
  → `uploaded`; `POST …/{id}/dry-run` → job `customer.import` с `mode=dry_run` → отчёт
  `{rows, created, matched, merge_candidates, skipped, by_org:{…}, errors[]}`;
  `POST …/{id}/apply` → тот же job `mode=apply`; `GET …/{id}`, `GET …/{id}/rows?action=`.
- `mapping` для CSV: `{columns: {email: "Email", phone: "Billing Phone", name: "…", org_rule: {column: "site", map: {"vinoandco.events": "<org_uuid>"}}}, attributes: ["city","tags"]}`;
  для `bil24_orders_json` фиксированный маппер (`fullName/phone/email/user.email`,
  `frontend.name` → org по `mapping.frontends`, `actionEvent.actionName` → атрибут
  `interests[]`, `discountReason` → `promo_codes_used[]`, `date` → `first/last_order_at`,
  `ticketQuantity` → `tickets_count`).
- Каждая строка проходит §12.2 в режиме «без автослияния сильных конфликтов»; согласия из
  импорта — `source='import:<label>'`, `kind='marketing'` **не** считается подтверждённым.
- Идемпотентность — `row_hash`; повторный apply того же файла → `matched` без изменений.
- Историю заказов не создаёт (решение №4).

---

## 13. Пишущий канал WP → arena (W1-C)

### 13.1 Сервисные ключи (C1, ADR-029)

- Таблица §3.6. Эндпоинты (`api_key.manage`, `X-Admin-Reason`):
  `POST /v1/organizations/{org_id}/api-keys {name, channel_id?, scopes[], expires_at?}` →
  `{id, key: "ak_…", …}` (один раз); `GET …/api-keys`; `DELETE …/api-keys/{id}`.
- Middleware в `applyAuth`: `Authorization: Bearer ak_<prefix>_<secret>` → поиск по `key_prefix`,
  bcrypt, `revoked_at/expires_at` → `auth.Actor{Type: service, ID: key.id}`, роли пустые,
  права = `scopes`. `enforceOrgMembership`/`enforceMembershipInOrg` и все `orgauth.go`-двойники
  (`hcatalog`, `hiam`, `hpayments`, `hbankaccounts`, `hseating`) считают сервисного актора членом
  **только** `api_keys.org_id`. `last_used_at` обновляется не чаще раза в минуту.
- Rate limit: существующий `platform/ratelimit`, ключ = `api_key.id`, 600 запросов/мин.
- Аудит: все мутации под ключом пишут `actor='api_key:<id>'`.
- Скоупы, допустимые для ключа: любые коды `permissions`, кроме `platform.*`, `admin.*`,
  `api_key.manage`. Набор для ивент-центра сайта: `event.create event.read event.update
  event.publish session.create session.read session.update tier.create tier.read tier.update
  venue.read seating_plan.create seating_plan.read seating_plan.update.own
  event_session.assign_seating_plan media.write media.read import.bil24_session`.
- Admin-web: вкладка «API-ключи» в организации (список, выпуск с выбором скоупов, отзыв).

### 13.2 `POST /v1/organizations/{org_id}/imports/bil24-session` (C3-arena)

Право `import.bil24_session`. Идемпотентен по `actionEvent.actionEventId`. Тело:

```json
{
  "action": {"actionId": 267271, "actionName": "…", "fullActionName": "…", "description": "…",
             "bigPosterUrl": "https://…", "age": "12+", "organizerName": "…"},
  "actionEvent": {"actionEventId": 7038720, "day": "26.04.2026", "time": "17:00",
                  "currency": "EUR", "sellEndTime": "2026-04-26T16:00:00+02:00",
                  "chargePercent": 5, "seatingPlanId": 1234, "seatingPlanName": "Big hall"},
  "venue": {"venueId": 9619, "venueName": "…", "address": "…", "cityId": 226, "cityName": "Madrid",
            "countryId": 34, "countryName": "Spain", "timezone": "Europe/Madrid",
            "geoLat": 40.4, "geoLon": -3.7},
  "categoryList": [{"categoryPriceId": 12345, "categoryPriceName": "Parter", "price": 25,
                    "placement": true, "availability": 100}],
  "seatList": [{"seatId": 2873098559, "categoryPriceId": 12345,
                "location": {"sector": "Parter", "row": "3", "number": "12"}, "available": true}],
  "svg": "<svg …sbt-формат Bil24…>",
  "publish": true
}
```

Алгоритм (одна транзакция + постусловия):

1. Все внешние ID < 1e9, иначе 422 `compat.external_id_out_of_range`.
2. Площадка: `venues.external_bil24_id = venueId` → существующая; иначе создаётся (`timezone`
   обязателен → 422 `venue.timezone_required`), город/страна по `cityName/countryName` через
   `geo` (`cities`/`countries`), map `venue/city/country`.
3. Событие: map `action` → существующее (обновить поля) или новое (`status='draft'`,
   `external_bil24_id`, постер — side-load `bigPosterUrl` в `media_objects`).
4. Сеанс: map `action_event` → существующий (обновить `start_at`, валюта — только если нет
   продаж) или новый: `start_at` = `day time` в `venue.timezone`, `currency`, `venue_id`,
   `admission_mode` = `assigned_seats` если есть `placement:true` и нет GA-категорий, `hybrid`
   если есть и те и другие, `general_admission` если нет мест.
5. Тарифы: на каждую категорию — `ticket_tiers` (map `category_price`), `price_amount` из
   `price`, `capacity` для GA из `availability`, `sale_window_end` из `sellEndTime`.
6. Схема: если есть `svg` — `seating.ImportSBTSVG` (§13.3) → `seating_plans` (`plan_type`
   по режиму, `name = seatingPlanName`, `external` в metadata) → версия → bind к сеансу с
   `category_tier_map` по индексу категории; материализация `session_seats` с
   `system_seat_id = geometry.seats[].external_id`, `system_seat_id_source='bil24'`.
   Места с `available:false` в `seatList` (проданы в Bil24 до импорта) → `status='blocked'`,
   предупреждение в ответе.
7. `fee_percent` канала не трогается (`chargePercent` — информационно, предупреждение при расхождении).
8. `publish:true` → событие `published`, сеанс `scheduled` (штатный publish-gate: сеанс с ценой).
9. Ответ 200: `{event_id, session_id, tier_ids: {categoryPriceId: uuid}, seating_plan_version_id,
   seats_materialized, warnings: [{code, message}], created: bool}`.
10. Повторный вызов: обновление; при изменении набора мест у сеанса с продажами → 409
    `import.session_has_sales`.

### 13.3 Парсер sbt-SVG (`seating.ImportSBTSVG`)

Вход — SVG, который Bil24 отдаёт по `image?type=seatingPlan` (формат §8, но с
`sbt:id` = Bil24 `seatId`, категории в `<metadata>`, `sbt:cat` = индекс). Выход —
каноническая `seating.Geometry` (§5.3 `seating_backlog.md`) + список категорий
`{index, external_id, name, color, price}`. Правила: `viewBox` обязателен; `<circle>` с
`sbt:id` и `sbt:state` — место; сектор — ближайший предок с `sbt:sect`; ряд — предок с `sbt:row`
или `<title>`; `sbt:seat` — номер; без `sbt:cat` → ошибка валидации; дубликат `sbt:id` →
ошибка; всё остальное — декор (`Decor`, с consolidate-transform не трогаем — arena рендерит
свой SVG). Геометрия получает поле `seats[].external_id` (int64, опционально) и
`categories[].external_id`; `Canonicalize`/`Checksum` учитывают их. Тест — на реальном
sbt-SVG тестового сеанса (снять интерактивно в W1-0) и на синтетическом.

### 13.4 Контракт для сайта (`lampyris-ops`, вкладка «Мероприятия»)

Не в этом репо; фиксируется, чтобы обе стороны сошлись:

- Без мест: `POST /v1/organizations/{org}/events` → `POST …/sessions` (площадка, дата, валюта)
  → `POST …/sessions/{id}/tiers` (категории, цены, вместимость, окно продаж) → `PUT
  /v1/sessions/{id}/media` (постер) → `POST …/events/{id}/status {published}`. После
  публикации — `event.created` вебхук и штатный `GET_ALL_ACTIONS`.
- С местами: модуль импорта сайта вызывает у Bil24 `GET_ALL_ACTIONS`, `GET_SEAT_LIST
  (availableOnly:false)`, `image?type=seatingPlan`, собирает тело §13.2 и шлёт под ключом.
- Ключ — `Authorization: Bearer ak_…`; ошибки — стандартный `ErrorEnvelope` arena.

---

## 14. Заказ

### 14.1 `ordering` — единая точка для трёх поверхностей

`CreateOrderFromCheckout(ctx, tx, in)` вызывается из `hcheckout` (`/checkout/{id}/confirm`),
`hfeed` (`confirmPublicCheckout`, `public_feed_checkout.go:985`) и `hbil24`
(`CREATE_ORDER_EXT`). `MarkPaid` — из вебхука платежа (`payment_intents.go:736-830`) и
`PAY_ORDER`. `Cancel/Expire` — из `abandon`, reaper истёкших броней (job `order.expire_sweep`
раз в минуту: `pending_payment` с `expires_at < now()` → `expired`, если нет `payment_intents
succeeded`). Логика цен остаётся в `ComputePricingLines` — `ordering` не дублирует её.

### 14.2 Списки

`GET /v1/organizations/{org_id}/orders?q=&status=&session_id=&from=&to=` (`order.read`),
`GET …/orders/{id}` (с `items`, `events`, `tickets`), `POST …/orders/{id}/cancel` (`order.write`,
только `pending_payment`/`manual_review`). Существующий `GET /v1/admin/orders` читает `orders`
вместо `checkout_sessions`. Admin-web: страница «Заказы» в org-скоупе (минимальная таблица
с поиском, карточка с таймлайном) — заменяет 403-карточку из отчёта TT-fit §2.2.

### 14.3 Инварианты

- Один `pending_payment` заказ на `(customer_id, session_id)` с живой бронью (partial unique
  index в 0092: `(customer_id, session_id) WHERE status='pending_payment' AND customer_id IS NOT NULL`).
- `orders.total = subtotal − discount + charge` (CHECK).
- `tickets.order_id` заполняется при выпуске для всех источников, где заказ есть.

---

## 15. Тестовая стратегия

### 15.1 Фикстуры (W1-0, фича #450 — строятся из этой спецификации до первой команды)

`apps/backend/tests/compat/bil24/testdata/wp/`:

- `requests/<COMMAND>/<case>.json` — тела, собранные из PHP (§7), включая четыре формы
  `RESERVATION`, оба ключа промокодов, строковый/числовой `orderId`.
- `golden/<COMMAND>/<case>.json` — ожидаемые ответы с плейсхолдерами `{{actionEventId}}`,
  `{{seatId:Parter-3-12}}`, `{{orderId}}`, `{{now+ttl}}`, которые харнесс подставляет из
  засеянных данных; сравнение — канонический JSON, порядок ключей нормализован, набор ключей
  **строгий** (лишний/недостающий ключ = fail).
- `bil24_orders_pseudonymized.json` — уже в репозитории: `docs/samples/history_orders.json`
  (68 заказов, 118 билетов, 10 REFUND) с псевдонимизированными персональными данными, структура
  и наборы ключей нетронуты; для binding-теста набора ключей `bil24wire` и для C7.
- `sbt_seatingplan_<testAE>.svg` — синтетический SVG по §8 из геометрии Akropolis (в заголовке
  файла — пометка); реальный SVG тестового сеанса Bil24 владелец добавляет позже, когда создаст
  тестового агента (решение №9).
- `wp_receiver/` — сценарии для стаба приёмника сайта.

### 15.2 Харнесс

`tests/compat/bil24/harness_test.go` (`//go:build integration`): поднимает `httpserver` с
реальными `gen.Queries` и пулом, сидит org/канал (`display_number`, токен)/площадку с TZ/событие
с двумя сеансами (Akropolis assigned_seats + GA-сеанс 590 юнитов, как AB-51)/тарифы/промокод,
подписчиков `bil24_wp` и `macs` на стабы. Сценарии как последовательности команд с проверкой
golden после каждой и проверкой БД-инвариантов (`session_seats.status`, `reservations`,
`orders`, `outbox_events`). Стаб сайта `tests/compat/bil24/wpstub` повторяет
`bil24-notification-receiver.php` (400 без `type`/`data`, дедуп `ticket.refunded` по `data.id`,
хранит `bil24_tickets`). Юнит-тесты — на кодировщики (`bil24wire`, SBT SVG, EAN-13, даты/TZ,
деньги) без БД.

### 15.3 Обязательные сценарии

1. Каталог: `GET_ALL_ACTIONS` для org с чужими событиями рядом (изоляция), GA/seated/hybrid,
   TZ площадки (Прага, Иерусалим — DST), `sellEndTime`.
2. GA-покупка: `CREATE_USER → RESERVE ×2 → GET_CART → ADD_PROMO_CODES → CREATE_ORDER_EXT →
   PAY_ORDER → GET_TICKETS_BY_ORDER` → `order.paid` в wpstub и MACS-стаб; суммы `sum/discount/charge/totalSum`.
3. Места: по одному месту `RESERVE`, конфликт с параллельной сессией (`101`), `UN_RESERVE_ALL`,
   `resultCode 1` после истечения `gateway_sessions`.
4. Возврат: `REFUND_TICKET` → `ticket.refunded` в оба стаба, повтор → дедуп; `orders.status`.
5. Истёкшая бронь при `PAY_ORDER`: reacquire OK; reacquire FAIL → `manual_review` + `101`.
6. Один открытый заказ: два `CREATE_ORDER_EXT` подряд → один `orderId`.
7. SVG `image`: соответствие §8, `304`, `sbt:cat` индексы ↔ `<metadata>`.
8. Импорт §13.2: дважды тот же payload → `created:false`, ID сохранены; `system_seat_id` ==
   Bil24 `seatId`; затем `GET_SEAT_LIST` отдаёт те же `seatId`.
9. Ключи API: скоуп-ограничение, чужая org → 403, отозванный → 401.
10. C7: dry-run на `bil24_orders_pseudonymized.json` → отчёт; apply → идемпотентность.

---

## 16. Конфигурация, стенды, откат

- Новые переменные: `PUBLIC_BASE_URL` (обязательна при `BIL24_COMPAT_ENABLED`), остальное —
  per-channel в БД. `.env.example`, `deploy/` compose и runbook стенда обновляются.
- Стенд (решение №9): организации `lampyris-staging`, `vino-staging`; каналы с `display_number`
  → `fid`; `bil24_acf_sync.base_url_prod = https://<api-стенда>/compat/bil24/json`;
  `LOPS_MACS_WEBHOOK_URL` staging-сайтов → MACS-стаб arena. Откат — вернуть URL в опции.
- OpenAPI: `/compat/bil24/json` (один path, `oneOf` по `command`), `/compat/bil24/image`, все
  новые `/v1/*` эндпоинты; Go-типы и TS-клиент регенерируются.

---

## 17. Решения, которые фиксируются этой спецификацией (ADR)

| ADR | Решение | Статус |
|---|---|---|
| ADR-034 | Для двух собственных WP-сайтов compat-шлюз — основной путь; гардрейл #6 и ADR-011/012/013 к ним не применяются (владелец, 2026-09-04 №1) | accepted |
| ADR-035 | Агрегат `orders` и пакет `ordering` — единственное место создания/оплаты/отмены заказа для всех поверхностей; частичный разворот ADR-033 | accepted |
| ADR-036 | Покупатель — платформенная сущность (`customers`), согласия — по организациям; сильные ключи не сливаются автоматически (владелец №11) | accepted |
| ADR-037 | Правило целочисленных ID §4 (map для каталога, колонки для билета/места/заказа/покупателя, Bil24-ID сохраняются, arena — из последовательности ≥1e9) | accepted |
| ADR-038 | Сервисные ключи организации (ADR-029, первая часть): bcrypt, скоупы = permissions, org-ограничение, показ один раз | accepted |

**Открыто (не блокирует старт):** промокоды — только один код на заказ (§7.6); 1D-штрихкод в
режиме QR у MACS (§11); `seatingPlanId` = `actionEventId` (§7.1) — если сайт когда-нибудь начнёт
использовать его как ключ плана, завести `kind='seating_plan'`.
