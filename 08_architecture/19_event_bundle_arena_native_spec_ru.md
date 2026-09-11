# Пакетное создание мероприятия («event bundle») с сайта и из ботов — спецификация W1-E

Статус: design authority для мини-волны **W1-E** (AutoForge, фичи #523–#527). Реализовано 2026-09-11, head 4c5991a.
Дата: 2026-09-11. Решение владельца: транспорт создания мероприятий с сайта — вариант C
(один пакетный идемпотентный эндпоинт), Lampyris первым, staging только.
Дополняет `18_bil24_compat_wave1_specification_ru.md` §13 (далее «спека W1»); при расхождении
для ветки `source=arena` главенствует этот документ, для `source=bil24` — спека W1 §13.2.

## 1. Цель и границы

Сайт (`lampyris-ops`, параллельная вкладка «Мероприятия» на staging), а в следующих итерациях
Telegram/WhatsApp-бот и любой другой канал, создаёт в arena мероприятие **без мест** одним
HTTP-вызовом: событие + сеанс + площадка + категории цен + афиша + публикация. Arena — источник
истины по учёту; сайт получает мероприятие обратно штатным путём (вебхук `event.created` при
публикации, `GET_ALL_ACTIONS` с `actionId ≥ 1e9`, импорт `bil24-acf-sync` создаёт WooCommerce-продукт).

Один и тот же формат тела обслуживает три сценария:

| Сценарий | `source` | Кто присылает ID | Места |
|---|---|---|---|
| Мероприятие из Bil24 (с местами), ретранслятор сайта | `bil24` | Bil24, `< 1e9`, обязательны | `seatList` + `svg` (sbt) — как в спеке W1 §13.2 |
| Мероприятие без мест с сайта / из бота (эта волна) | `arena` | arena минтит `≥ 1e9`, клиент возвращает их при правках | не поддерживаются, предупреждение |
| Свой модуль разметки (волна 1.1) | `arena` | arena | `seatList` + `svg` (arena-формат) — зарезервировано |

Вне этой волны: удаление сеансов/категорий через пакет, окно начала продаж на уровне сеанса,
сборы канала, несколько сеансов в одном пакете, seated-ветка для `source=arena`.

## 2. Маршруты и права

- **Новый** `POST /v1/organizations/{org_id}/imports/event-bundle` — принимает `source` ∈ {`bil24`,`arena`}.
- **Существующий** `POST /v1/organizations/{org_id}/imports/bil24-session` остаётся без изменений
  контракта и становится тонким алиасом: тот же обработчик с принудительным `source=bil24`
  (поле `source` в теле, если прислано, должно быть `bil24`, иначе 422 `import.source_mismatch`).
  Все существующие тесты #517/#518 остаются зелёными без правок.
- Право: `import.bil24_session` (переиспользуется; переименование — косметика, отложено).
  Аутентификация — org API-ключ `Authorization: Bearer ak_…` (спека W1 §13.1) или JWT члена
  организации; членство проверяется как сегодня в `himports.HandleBil24Session`.
- Rate limit, аудит (`actor='api_key:<id>'`), лимит тела 8 МБ — как у существующего маршрута.

## 3. Тело запроса

Форма `ImportBil24SessionRequest` (openapi.yaml, `bil24compat.ImportSessionRequest`) плюс два
верхнеуровневых поля:

```json
{
  "source": "arena",
  "externalRef": "wp:lampyris-staging:product:4711",
  "action":      {"actionId": null, "actionName": "…", "fullActionName": "…", "description": "…",
                  "bigPosterUrl": "https://staging.lampyrisevents.com/wp-content/uploads/….jpg",
                  "age": "12+", "organizerName": "Lampyris"},
  "actionEvent": {"actionEventId": null, "day": "26.10.2026", "time": "19:00", "endTime": "22:00",
                  "currency": "CZK", "sellEndTime": "2026-10-26T18:00:00+01:00",
                  "sellStartTime": "2026-09-15T10:00:00+02:00"},
  "venue":       {"venueId": null, "venueName": "Palác Akropolis", "address": "Kubelíkova 27",
                  "cityName": "Praha", "countryName": "Czechia", "timezone": "Europe/Prague"},
  "categoryList": [
     {"categoryPriceId": null, "categoryPriceName": "Standard", "price": 450, "availability": 300},
     {"categoryPriceId": null, "categoryPriceName": "VIP",      "price": 900, "availability": 40}
  ],
  "publish": true
}
```

Новые поля (все — в `bil24compat.ImportSessionRequest`, camelCase допустим: пакет в allowlist
snake_case-гардрейла):

| Поле | Тип | Правило |
|---|---|---|
| `source` | string | Обязательно на `/imports/event-bundle`; `bil24` \| `arena`. |
| `externalRef` | string ≤ 200 | **Обязательно при `source=arena`**, опционально при `bil24`. Ключ идемпотентности создания, уникален в организации. Рекомендуемый формат `wp:<site>:product:<wc_product_id>`, для бота `tg:<bot>:<chat>:<msg>`. |
| `actionEvent.endTime` | `"HH:MM"` | Опционально. Конец сеанса в `venue.timezone`; если `endTime ≤ time` — следующий день. Без него — текущее правило импорта (`defaultSessionDuration` = 3 часа). |
| `actionEvent.sellStartTime` | RFC3339 | Опционально. Начало продаж → `ticket_tiers.sale_window_start` каждой категории (колонка есть с миграции 0019; CHECK `sale_window_end > sale_window_start`). |

Все остальные поля — как в спеке W1 §13.2. `chargePercent`, `seatingPlanId`, `seatingPlanName`
для `source=arena` игнорируются с предупреждением.

### 3.1 Правила ID по `source`

`source=bil24` — без изменений: все `*Id` обязательны и `< 1e9` (`ValidateExternalIDs`).

`source=arena`:

- `action.actionId`, `actionEvent.actionEventId`, `venue.venueId`, `categoryList[].categoryPriceId`
  **необязательны**. Если присланы — должны быть `≥ 1e9` (иначе 422 `import.arena_id_out_of_range`)
  и резолвиться через `compatids.Resolve` в объект **этой** организации (иначе 404
  `import.compat_id_unknown`; чужая организация тоже 404, без утечки).
- Отсутствующий ID означает «создай и выдай ID»: после создания строки —
  `compatids.Ensure(kind, uuid)` (`source='arena'`, `compatibility_system_id_seq`). Ensure
  вызывается и для уже существующих объектов без записи в map (идемпотентен).
- `seatList[].seatId` не участвуют (места не поддерживаются).

### 3.2 Сопоставление объектов при `source=arena` (в одной транзакции)

1. **Сеанс по `externalRef`** — первый шаг: `session_external_refs(org_id, external_ref)` →
   найден → это повтор/правка: `session_id` известен, событие и площадка берутся от него.
   Если при этом прислан `actionEventId`, не совпадающий с найденным сеансом → 409
   `import.external_ref_conflict`.
2. **Сеанс по `actionEventId`** (если `externalRef` не найден, а ID прислан) → правка; `externalRef`
   записывается в `session_external_refs`, если ещё не записан (если записан другой → 409
   `import.external_ref_conflict`).
3. **Событие**: `actionId` прислан → существующее; иначе если сеанс найден по шагам 1–2 → его событие;
   иначе создаётся новое (`status='draft'`). Сопоставления по имени для событий **нет**
   (два разных мероприятия с одним названием — норма).
4. **Площадка**: `venueId` прислан → существующая; иначе поиск активной площадки организации по
   `lower(btrim(name)) = lower(btrim(venueName))`; не найдена → создаётся (`timezone` обязателен →
   422 `venue.timezone_required`; город/страна через `geo`, как сегодня). Найденная по имени
   площадка **не обновляется** полями пакета (только предупреждение `import.venue_matched_by_name`).
5. **Категории**: `categoryPriceId` прислан → существующий тариф этого сеанса (тариф другого сеанса →
   409 `import.category_bound_elsewhere`); иначе поиск тарифа этого сеанса по
   `lower(btrim(name))`; не найден → создаётся. Тарифы сеанса, отсутствующие в пакете, не
   трогаются, предупреждение `import.tier_not_in_payload` с их ID.
6. Далее — общий путь существующего `executeImport`: `upsertTiers`, `syncInventoryLedger`
   (вместимость GA = сумма `availability`), `applyPublish` при `publish:true`.
7. `session_external_refs` пишется в той же транзакции, что и создание сеанса.

Правки после создания: клиент присылает тот же пакет с полученными ID (или тем же `externalRef`).
Смена валюты при продажах — как сегодня (`WarnCurrencyLocked`). Уменьшение `availability` ниже
выданных холдов — как сегодня (ledger не уменьшается ниже обязательств, предупреждение).

### 3.3 Афиша

`bigPosterUrl` — side-load в `media_objects` с `owner_type='event_poster'` и запись
`events.poster_media_id` (как в существующем `sideLoadPoster`; вне транзакции, ошибка → предупреждение
`import.poster_skipped`, существующий `WarnPosterSkipped`). Сегодня импорт создаёт новый media-объект при
каждом вызове; для пакета это недопустимо (правки будут частыми). Правило: после скачивания
считается контрольная сумма; если она равна сумме текущего `poster_media_id` события — новый объект
не создаётся и ссылка не меняется; иначе создаётся новый объект и `poster_media_id` переключается.
Именно `poster_media_id` кормит `bigPosterUrl/smallPosterUrl` в `GET_ALL_ACTIONS`; `events.image_url`
не используется.

## 4. Ответ

`ImportBil24SessionResponse` расширяется (оба маршрута, оба `source`) обязательным объектом:

```json
{
  "event_id": "…", "session_id": "…", "tier_ids": {"1000000012": "…"},
  "seating_plan_version_id": null, "seats_materialized": 0,
  "warnings": [], "created": true,
  "external_ref": "wp:lampyris-staging:product:4711",
  "compat_ids": {
    "action_id": 1000000007,
    "action_event_id": 1000000008,
    "venue_id": 1000000003,
    "category_price_ids": [1000000012, 1000000013]
  }
}
```

- `compat_ids.category_price_ids` — массив, **выровненный по порядку `categoryList` запроса**.
- `tier_ids` по-прежнему ключуется десятичной строкой `categoryPriceId` (для `arena` — выданным ID).
- `external_ref` — `null`, если не присылался.
- Для `source=bil24` `compat_ids` просто эхо присланных ID (после регистрации в map).

Сайт сохраняет `compat_ids.action_event_id` в `bil24_action_event_id` продукта сразу, не дожидаясь
`GET_ALL_ACTIONS`.

## 5. Ошибки (дополнение к спеке W1 §13.2)

| HTTP | code | Когда |
|---|---|---|
| 422 | `import.source_invalid` | `source` не из {bil24, arena} или отсутствует на новом маршруте |
| 422 | `import.source_mismatch` | на `/imports/bil24-session` прислан `source=arena` |
| 422 | `import.external_ref_required` | `source=arena` без `externalRef` |
| 422 | `import.external_ref_invalid` | длина > 200 или пустая после trim |
| 422 | `import.arena_id_out_of_range` | `source=arena`, прислан ID `< 1e9` |
| 404 | `import.compat_id_unknown` | ID не резолвится в объект этой организации |
| 409 | `import.external_ref_conflict` | `externalRef` привязан к другому сеансу / сеанс уже имеет другой ref |
| 422 | `import.end_time_invalid` | `endTime` не `HH:MM` |
| 422 | `import.invalid_sell_start_time` | `sellStartTime` не RFC3339 или ≥ `sellEndTime` |
| 409 | `import.action_mismatch` | прислан `actionId`, не совпадающий с событием найденного сеанса (сеанс нельзя перенести между событиями) — добавлено в #525 |
| 422 | `import.venue_name_required` | `source=arena`, нет ни `venueId`, ни `venueName`, ни сеанса, у которого можно унаследовать площадку — добавлено в #525 |

Предупреждения (не ошибки): `import.seating_not_imported`,
`import.venue_matched_by_name`, `import.tier_not_in_payload`, `import.poster_skipped`,
`import.field_ignored_for_source` (для `chargePercent`/`seatingPlan*`).

## 6. Данные: миграция `0099_session_external_refs.sql`

```sql
CREATE TABLE session_external_refs (
    org_id       uuid NOT NULL REFERENCES organizations(id),
    external_ref text NOT NULL CHECK (length(external_ref) BETWEEN 1 AND 200),
    session_id   uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, external_ref)
);
CREATE UNIQUE INDEX session_external_refs_session_uq ON session_external_refs (session_id);
```

Один сеанс — не более одного ref; один ref в организации — не более одного сеанса. Down-блок —
`DROP TABLE`. Пин головы в `migrations_head_test.go` → `0099_session_external_refs.sql`.
Запросы: `queries/session_external_refs.sql` + ручной `gen/session_external_refs.sql.go`
(`GetSessionByExternalRef(org_id, external_ref)`, `GetExternalRefBySession(session_id)`,
`InsertSessionExternalRef`). Стиль — `bank_accounts.sql.go`.

## 7. Что видит сайт после создания

- `publish:true` → событие `published`, сеанс `scheduled` → outbox `v1.event.published` →
  `bil24wire.Dispatcher` → вебхук `event.created` подписчику `bil24_wp` канала → сайт запускает
  синк → `GET_ALL_ACTIONS` отдаёт `actionId/actionEventId/venueId/categoryPriceId` из `compat_ids`,
  `day/time` в `venue.timezone`, `bigPosterUrl` = `/v1/media-files/{poster_media_id}`.
- `publish:false` → черновик, в `GET_ALL_ACTIONS` не виден; сайт публикует повторным пакетом с
  `publish:true`.
- Покупка билетов на такое мероприятие идёт через шлюз как на любое другое; `order.paid` в MACS
  несёт `actionEvent.id = compat_ids.action_event_id` (≥ 1e9) — MACS создаёт своё событие сам.

## 8. Совместимость с будущим модулем разметки (волна 1.1)

Зарезервировано, в этой волне не реализуется: для `source=arena` `svg` может прийти в arena-формате
(`svgFormat: "arena"|"sbt"`, по умолчанию `sbt`), `seatList[].seatId` необязателен (arena выдаёт
`system_seat_id ≥ 1e9`), `categoryList[].placement=true` включает `assigned_seats/hybrid`. Никакое
решение этой волны не должно этому мешать: в частности, `compat_ids` и `externalRef` не зависят от
режима рассадки, а валидация `seatList` для `arena` сейчас — только предупреждение, не 422.

## 9. Тестовая стратегия

Юнит (без БД, `himports`): ladder валидации для `source` (обязательность, mismatch на алиасе,
`externalRef`, диапазон ID, `endTime`, `sellStartTime`); `ValidateExternalIDs` ветвится по source.

Интеграция (`//go:build integration`, `himports`, живой PG):
1. `arena`: создание GA-пакета → 200, `created=true`, все `compat_ids ≥ 1e9`, строки в
   `compatibility_id_map` с `source='arena'`, `session_external_refs` записан, `poster_media_id`
   заполнен (постер — локальный `httptest`-сервер); повтор с тем же постером не создаёт второй media-объект.
2. Повтор того же пакета (без ID, тот же `externalRef`) → `created=false`, **те же** ID, тарифы не
   задублированы.
3. Правка: тот же `externalRef`, изменённая цена и новая категория → цена обновлена, категория
   добавлена, `tier_not_in_payload` не выдан (все присланы); затем пакет без одной категории →
   предупреждение, тариф на месте.
4. Прислан `actionEventId` чужой организации → 404; `< 1e9` при `arena` → 422; `externalRef` другого
   сеанса → 409.
5. Площадка по имени: второй пакет с тем же `venueName` без `venueId` → та же `venue_id`, предупреждение.
6. `publish:true` → через `hbil24` `GET_ALL_ACTIONS` (fid канала организации) мероприятие видно с
   теми же `actionId/actionEventId/categoryPriceId`, `day/time` в таймзоне площадки, `bigPosterUrl`
   указывает на `/v1/media-files/…`.
7. Алиас `/imports/bil24-session` с `source=arena` → 422; без `source` — поведение #517 без изменений
   (существующие тесты).

Фикстура: `apps/backend/tests/compat/bil24/testdata/wp/event_bundle/arena_ga_lampyris.json` — тело
из §3 (значения staging Lampyris) — используется интеграционными тестами и как контракт для PHP.

## 10. Контракт для сайта (`lampyris-ops`, вне этого репо)

Параллельная вкладка «Мероприятия (arena)» на staging, старая вкладка и импорт из Bil24 не трогаются.
Настройки: `arena_base_url`, `arena_org_id`, `arena_api_key` (рядом с `bil24_acf_sync`). Форма: название,
полное название, описание, афиша (медиатека WP → публичный URL), возраст, площадка (название, адрес,
город, страна, таймзона), дата, время начала/конца, валюта, начало/конец продаж, категории
(название, цена, количество), флаг «опубликовать». Сохранение = один `POST …/imports/event-bundle`;
`externalRef = wp:<site-slug>:product:<id>`; ответные `compat_ids` пишутся в мету продукта
(`bil24_action_id`, `bil24_action_event_id`, `bil24_venue_id`, категории). Повторное сохранение —
тот же пакет с ID. Ошибки — `ErrorEnvelope` arena, показываются как есть.

## 11. Разбиение на фичи AutoForge (W1-E, #523–#527)

| # | Код | Суть | Сложность |
|---|---|---|---|
| 523 | W1-E1a | Миграция 0099 + queries/gen + пин головы + `compatids` без изменений; юнит на голову | 2 |
| 524 | W1-E1b | Wire: `source`, `externalRef`, `endTime`, `sellStartTime`; валидация по source; коды ошибок; юнит-ladder | 2 |
| 525 | W1-E1c | Исполнитель `source=arena`: сопоставление §3.2, минт `Ensure`, `compat_ids`/`external_ref` в ответе, афиша без перекачки; интеграционные тесты 1–5 | 3 |
| 526 | W1-E1d | Маршрут `/imports/event-bundle`, алиас, OpenAPI + codegen Go/TS, `buildDriftTestServer`, docs (`docs/ops/bil24_gateway.md` §8, спека W1 §13.4 → пакет), фикстура `arena_ga_lampyris.json` | 2 |
| 527 | W1-E [EPIC-VERIFY] | Сквозной интеграционный тест 6–7 через `hbil24 GET_ALL_ACTIONS`; полный гейт `-count=1`; прогресс-нота | 2 |

Зависимости: 524 → 523; 525 → 524; 526 → 525; 527 → 526.
