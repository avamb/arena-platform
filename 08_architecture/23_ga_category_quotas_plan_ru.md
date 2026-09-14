# 23. GA-квоты по категориям, как в Bil24 — план перехода

Статус: **план утверждён владельцем 2026-09-14, работа не начата.** Начинать с раздела «Решения» и шага 1.

## Зачем

Сейчас в arena два способа завести зал General Admission:

1. **GA-схема зала** (`seating_plans.plan_type = general_admission`, `hseating/bind.go` ~510). У схемы категории с количеством. При привязке к сеансу у каждой категории свои места `ga|c<index>|<n>` с жёсткой категорией, вместимость сеанса = сумма категорий. Это модель Bil24 Editor.
2. **Сеанс без схемы** (`hcatalog/sessions.go` ~599, `himports/import_arena.go` ~426). Одна общая пачка мест `ga|pool|<n>` с `tier_id NULL` на `sessions.capacity_total`. Лимит категории (`ticket_tiers.capacity`) проверяется при брони (`hcheckout/ga_units.go`, `CountGAUnitsHeldSoldByTier`). Бронь ставит на место категорию, освобождение её стирает.

Второй способ хранит правду о количестве в двух местах: вместимость сеанса и лимиты категорий. Их никто не держит равными, и каждый подсчёт приходится писать под пул. Живой пример — staging 2026-09-14: сеанс 60 мест (Standard 50, VIP 10), продано по 2. `GET_SEAT_LIST` и `GET_ALL_ACTIONS` отдали 0 по обеим категориям, сайт показал «Sold out». Временно исправлено в `60a2779` и следующем коммите; после миграции эти ветки удаляются.

**Решение владельца: единственная модель GA — квоты по категориям, как в Bil24.**

## Целевая модель

- Категория владеет своими местами: места создаются на её количество, у каждого места жёсткая категория.
- Вместимость GA-сеанса — всегда сумма количеств категорий. Отдельно не редактируется.
- Количество меняется явно: увеличение добавляет места категории, уменьшение убирает только свободные и не опускается ниже проданных и забронированных.
- Цена меняется свободно (уже есть, включая цены по расписанию, миграция 0087).
- Категорию можно закрыть и открыть. Закрытая не принимает новых броней.
- Один механизм для сеансов со схемой и без неё.

## Решения, нужные от владельца до начала

| # | Вопрос | Рекомендация |
|---|---|---|
| 1 | Закрыли категорию внутри 20-минутного окна оплаты — можно ли оплатить уже оформленный заказ? | Да, места уже закреплены за покупателем |
| 2 | Удаление категории с продажами | Запретить, только закрыть. Удалять — только без проданных и забронированных |
| 3 | Категории без количества | Запретить для GA |
| 4 | Как сайт покажет закрытую категорию (в протоколе Bil24 нет признака «закрыта») | Как «Sold out» (availability 0), без доработки плагина |
| 5 | `ticket_tiers.sale_window_start/end` сейчас нигде не проверяется | Проверять при брони: это автоматическое закрытие по дате |
| 6 | Бесплатные билеты (`htickets/complimentary.go`) и внешние квоты (`hinventory/external_allocations.go`) списывают только ledger и не трогают места — можно продать сверх зала | Перевести на места категории в этой же задаче |

## Шаги

### Шаг 1. Проверка данных (ничего не меняется)

Прогнать на стенде и локально, собрать отчёт «что и как будет переведено». Реальных продаж в arena ещё нет, данные — только стенд и локальная база.

```sql
-- A. Сеансы с пулом
SELECT s.id, s.capacity_total, count(*) AS pool_units,
       count(*) FILTER (WHERE ss.status='available') AS avail,
       count(*) FILTER (WHERE ss.status='held') AS held,
       count(*) FILTER (WHERE ss.status='sold') AS sold,
       count(*) FILTER (WHERE ss.status='available' AND ss.tier_id IS NOT NULL) AS stamped_available
FROM sessions s
JOIN session_seats ss ON ss.session_id=s.id AND ss.kind='ga_unit' AND ss.seat_key LIKE 'ga|pool|%'
WHERE s.deleted_at IS NULL GROUP BY s.id;

-- B. GA-сеансы без схемы и вообще без мест (импорт сеанса Bil24, arena-seed)
SELECT s.id, s.capacity_total FROM sessions s
WHERE s.deleted_at IS NULL AND s.admission_mode='general_admission' AND s.seating_plan_version_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM session_seats ss WHERE ss.session_id=s.id AND ss.kind='ga_unit');

-- C. Сумма категорий != вместимость, или категории без количества
SELECT s.id, s.capacity_total, sum(tt.capacity) AS sum_tier_cap,
       count(tt.id) FILTER (WHERE tt.capacity IS NULL) AS null_cap_tiers
FROM sessions s LEFT JOIN ticket_tiers tt ON tt.session_id=s.id AND tt.deleted_at IS NULL
WHERE s.deleted_at IS NULL AND s.admission_mode='general_admission' AND s.seating_plan_version_id IS NULL
GROUP BY s.id
HAVING count(tt.id) FILTER (WHERE tt.capacity IS NULL) > 0 OR COALESCE(sum(tt.capacity),0) <> s.capacity_total;

-- D. Занято по категории против её лимита, включая места с удалённой категорией
SELECT ss.session_id, ss.tier_id, tt.name, tt.capacity, tt.deleted_at,
       count(*) FILTER (WHERE ss.status IN ('held','sold')) AS used
FROM session_seats ss LEFT JOIN ticket_tiers tt ON tt.id=ss.tier_id
WHERE ss.kind='ga_unit' AND ss.seat_key LIKE 'ga|pool|%' AND ss.tier_id IS NOT NULL
GROUP BY 1,2,3,4,5;

-- E. Сеансы, где пул смешан с местами категорий (привязка схемы к сеансу с пулом, backfill 0084 для hybrid)
SELECT ss.session_id, s.admission_mode, s.seating_plan_version_id,
       count(*) FILTER (WHERE ss.seat_key LIKE 'ga|pool|%') AS pool,
       count(*) FILTER (WHERE ss.seat_key LIKE 'ga|c%') AS cat
FROM session_seats ss JOIN sessions s ON s.id=ss.session_id
WHERE ss.kind='ga_unit'
GROUP BY 1,2,3
HAVING count(*) FILTER (WHERE ss.seat_key LIKE 'ga|pool|%') > 0
   AND (s.seating_plan_version_id IS NOT NULL OR count(*) FILTER (WHERE ss.seat_key LIKE 'ga|c%') > 0);

-- F. Открытые брони и активные билеты на местах пула
SELECT r.id, r.session_id, r.state, r.expires_at FROM reservations r
WHERE r.state IN ('draft','active') AND EXISTS (
  SELECT 1 FROM reservation_seats rs JOIN session_seats ss ON ss.id=rs.session_seat_id
  WHERE rs.reservation_id=r.id AND ss.seat_key LIKE 'ga|pool|%');
SELECT session_id, count(*) FROM tickets WHERE status='active' AND seat_key LIKE 'ga|pool|%' GROUP BY 1;

-- G. Ledger против мест
SELECT s.id, s.capacity_total, il.capacity_total AS ledger_total, il.capacity_held, il.capacity_sold
FROM sessions s LEFT JOIN inventory_ledger il ON il.session_id=s.id AND il.tier_id IS NULL
WHERE s.deleted_at IS NULL AND s.admission_mode='general_admission';
```

### Шаг 2. База данных

- Признак открытия у категории (по умолчанию «открыта»).
- Стабильный номер категории внутри сеанса для ключей новых мест (не переиспользуется после удаления).
- Миграция перевода сеансов без схемы:
  - проданным и забронированным местам оставить их категорию;
  - свободные места раздать категориям по «количество − занято»;
  - лишние свободные удалить, недостающие добавить;
  - `capacity_total` и строка ledger сеанса = сумма.
- Ключи существующих мест (`ga|pool|000003`) **не переименовывать**: на них ссылаются `tickets.seat_key`.
- Добавить миграцию — обновить пин головы миграций в тестах (AGENTS.md).

### Шаг 3. Единый механизм квот

Одно место в коде для операций с GA-категорией: создать с количеством, изменить количество, закрыть или открыть, удалить. Каждая операция в одной транзакции пересчитывает вместимость сеанса и ledger и держит порядок блокировок (sessions → inventory_ledger → units, AGENTS.md).

Через него переводятся:
- `hcatalog/sessions.go` — создание сеанса и изменение вместимости (~501–618, ~950–1053, включая рост и сжатие пула);
- `hcatalog/ticket_tiers.go` — `HandleCreateTier`, `HandleUpdateTier`, `HandleDeleteTier`;
- `himports/import_arena.go` (`upsertArenaSession`, `upsertArenaTiers`) и `himports/import_exec.go` (`resolveSession`, `upsertTiers`, `syncInventoryLedger`);
- `cmd/arena-seed/main.go` (~632–674, сейчас сеанс без мест и без категорий);
- `hseating/bind.go` (создание категорий из GA-схемы).

### Шаг 4. Продажи

- `hcheckout/ga_units.go` `AllocateGAUnitsTx`: всегда брать места своей категории. Удалить ветку пула: пометку категорией, `ResetAvailableGAPoolTierStamps` (`hold_shrink.go:96`, `seat_reservations.go:417`), `CountGAUnitsHeldSoldByTier`.
- Места вычисления `planBound` для GA: `hold_api.go:398`, `hold_mutation.go:305`, `reservations.go:508`, `hfeed/public_feed_checkout.go:628` (жёстко `true`) и `:818`, `hfeed/public_checkout_recover.go:456`.
- `hcheckout/reservations.go:497-509`: запретить бронь без категории.
- Закрытая категория и окно продаж — на всех входах новой брони:
  - `hbil24/cmd_cart_reserve.go` (RESERVATION);
  - `hcheckout/hold_mutation.go` (корзина, CREATE_ORDER_EXT);
  - `hfeed/public_feed_checkout.go` (виджет);
  - `hcheckout/reservations.go` (REST);
  - `hfeed/public_checkout_recover.go`;
  - `hcheckout/hold_shrink.go` `ReacquireHoldTx` — по решению 1.
- Отмена билета (`htickets/cancel.go` `ReleaseCancelledTicketInventoryTx`) возвращает место в его категорию. Сейчас на пуле место остаётся «available» с категорией и не попадает в выборку `tier_id IS NULL`.
- По решению 6: бесплатные билеты и внешние квоты — через места категории.

### Шаг 5. Показ остатка

- `hbil24/cmd_seat_list.go` `seatListAvailability`: остаток = свободные места категории, у закрытой 0. Удалить временную ветку пула.
- `hbil24/cmd_catalog_events.go` `gaCategoryAvailability`: то же. Удалить временную ветку `planLess`. Остаток сеанса (`sessionAvailability`) = сумма свободных мест открытых категорий.
- `hbil24/cmd_cart_view.go:520-549`: `bil24.category_sold_out` с реальным остатком вместо жёсткого 0.
- Публичный API виджета (`hfeed/public_feed.go`) отдаёт остаток категории. Виджет (`apps/widget/src/components/GaTierCard.svelte`) ограничивает выбор остатком, а не объявленным количеством.
- `hcatalog/ticket_tiers.go` `HandleListTiers`: счётчики «продано / в брони / свободно».

### Шаг 6. API и админка

- `openapi.yaml`:
  - `CreateTicketTierRequest` / `UpdateTicketTierRequest`: количество обязательно для GA;
  - признак открытия;
  - `TicketTierItem`: счётчики;
  - вместимость GA-сеанса только для чтения (`capacity_override` для GA убрать);
  - перегенерировать `types_gen.go` и TS-клиент.
- Админка (`apps/admin-web/src/routes/events.tsx`):
  - таблица категорий как в Bil24 Editor — категория, количество, цена, продано, свободно, открыта;
  - минимальное количество = занято;
  - поле вместимости у GA-сеанса (`SessionEditor` ~3510) убрать.
- `queries/ticket_tiers.sql` `UpdateTicketTier`: сейчас `CASE WHEN $n IS NOT NULL` не даёт очистить поля, админка шлёт `null` и молча ничего не меняет. Исправить явным флагом, как `set_reservation_ttl_override` (AGENTS.md).

### Шаг 7. Импорты и привязка схемы

- Импорт сеанса Bil24 (`himports/import_exec.go` `resolveSession`) сейчас создаёт сеанс **без мест** — «распродан» с первой минуты. Создавать места по категориям.
- Повторный импорт меняет количество через механизм квот. Категорию, пропавшую из пакета, закрывать, а не удалять.
- `hseating/bind.go` ~358–423: первая привязка схемы к сеансу с пулом оставляет старые места рядом с новыми. Запретить или переводить.

### Шаг 8. Попутные ошибки в том же коде (перепроверить перед исправлением)

- `hbil24/cmd_order_create.go:445-452` `orderIsSeated` считает любое GA-место с категорией местом со схемой, поэтому сверка строк заказа (`orderReconcile`) пропускается для GA-корзин.
- `hfeed/public_feed_checkout.go`: в ветке чистого GA `ReserveCapacity` (~752) идёт раньше счётчика версий (~806), вопреки порядку блокировок из AGENTS.md.
- Миграция 0084 создала hybrid-сеансам места пула с `tier_id NULL`: продать их нельзя, но они учитываются в подсчётах.
- Миграция 0084 брала проданное из ledger с `tier_id IS NOT NULL`, которых в production не было, — исторические продажи, вероятно, заведены как 0 проданных.

### Шаг 9. Тесты

Новые:
- после каждой операции квот сумма категорий = вместимость = ledger;
- количество не опускается ниже занятого;
- закрытая категория и окно продаж отказывают на всех входах из шага 4;
- параллельные брони одной категории не продают сверх количества;
- отмена возвращает место в категорию;
- миграция пула на данных как на стенде;
- импорт сеанса Bil24 создаёт продаваемые места.

Обновить тесты, построенные на пуле:
- `gen/ga_units_burst_integration_test.go`;
- `hcheckout/hold_mutation_concurrency_integration_test.go`, `reservation_expire_sweep_integration_test.go`, `ga_hold_deadlock_race_integration_test.go`;
- `httpserver/ticket_cancel_ab49_integration_test.go`, `order_wiring_w1a6c_488_integration_test.go`, `public_feed_checkout_race_integration_test.go`, `webhook_widget_completion_integration_test.go`;
- `hbil24/bil24_476_catalog_test.go`, `seat_d1_312_test.go`;
- `himports/bil24_session_517_*`, `event_bundle_525_integration_test.go`;
- `httpserver/sessions_test.go`, `openapi_sessions_264_test.go`, `inventory_130_test.go`;
- `tests/compat/bil24/`: `seed_test.go`, `scenario01_catalog_test.go`, `scenario02_ga_purchase_test.go`, `seat_list_499_test.go`, `event_bundle_527_integration_test.go`, golden-файлы;
- `apps/admin-web/src/routes/events.test.ts`.

Полный прогон юнит- и интеграционных тестов на чистой базе, как в CI (рецепт в AGENTS.md).

### Шаг 10. Проверка и выкладка

1. Нагрузочные тесты локально (`ops/loadtest`): оба входа плюс гонка за последние места одной категории.
2. Стенд: отчёт миграции (шаг 1), применение, smoke.
3. Покупка на staging Lampyris, включая закрытие категории. После выкладки на staging запустить синхронизацию каталога: сайт хранит остаток в мете товара (`bil24_category_availability`).
4. Обновить AGENTS.md, `docs/ops/bil24_gateway.md`, спецификацию 18.

## Порядок работ

- **Волна A — бэкенд:** шаги 1–5, 7, 8 и их тесты.
- **Волна B — API и админка:** шаг 6.
- **Волна C — проверка:** шаг 10.
