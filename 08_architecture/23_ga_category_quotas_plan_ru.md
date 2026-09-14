# 23. GA-квоты по категориям, как в Bil24 — план перехода

Статус: **план утверждён владельцем 2026-09-14, решения 1–10 приняты в рекомендованном виде 2026-09-14, работа начата.** Ревизия по коду 2026-09-14 добавила решения 7–10 и правки в шагах 1–8.

## Термины

- **arena** — наша платформа. Всё, что описано в этом плане, — код arena.
- **Bil24** — внешняя система, с которой взята модель категорий и квот как образец. Во время работы arena с Bil24 не связана.
- **Шлюз протокола Bil24** — собственный код arena (`/compat/bil24/*`, пакет `hbil24`), который говорит на wire-протоколе Bil24, чтобы WordPress-сайты с плагином Bil24 работали с arena без переделки плагина. «Корзина шлюза», «RESERVATION», «GET_ALL_ACTIONS» в этом плане — команды этого протокола, обрабатываемые arena.
- **Импорт пакета в формате Bil24** — разовый перенос данных сеанса из выгрузки в формате Bil24 в arena (`himports`, `cmd/arena-bil24-import`). После импорта данные живут в arena и с источником не синхронизируются, кроме явного повторного импорта.

## Зачем

Сейчас в arena два способа завести зал General Admission:

1. **GA-схема зала** (`seating_plans.plan_type = general_admission`, `hseating/bind.go` ~510). У схемы категории с количеством. При привязке к сеансу у каждой категории свои места `ga|c<index>|<n>` с жёсткой категорией, вместимость сеанса = сумма категорий. Это модель Bil24 Editor.
2. **Сеанс без схемы** (`hcatalog/sessions.go` ~599, `himports/import_arena.go` ~426). Одна общая пачка мест `ga|pool|<n>` с `tier_id NULL` на `sessions.capacity_total`. Лимит категории (`ticket_tiers.capacity`) проверяется при брони (`hcheckout/ga_units.go`, `CountGAUnitsHeldSoldByTier`). Бронь ставит на место категорию, освобождение её стирает.

Второй способ хранит правду о количестве в двух местах: вместимость сеанса и лимиты категорий. Их никто не держит равными, и каждый подсчёт приходится писать под пул. Живой пример — staging 2026-09-14: сеанс 60 мест (Standard 50, VIP 10), продано по 2. `GET_SEAT_LIST` и `GET_ALL_ACTIONS` отдали 0 по обеим категориям, сайт показал «Sold out». Временно исправлено в `60a2779` и следующем коммите; после миграции эти ветки удаляются.

**Решение владельца: единственная модель GA — квоты по категориям, как в Bil24.**

## Целевая модель

- Категория владеет своими местами: места создаются на её количество, у каждого места жёсткая категория.
- Вместимость GA-сеанса — всегда сумма количеств категорий. Отдельно не редактируется. У `hybrid`-сеанса вместимость = места схемы + сумма GA-категорий; план обязан покрывать `hybrid` наравне с `general_admission`.
- Количество меняется явно: увеличение добавляет места категории, уменьшение убирает только свободные и не опускается ниже проданных и забронированных.
- **Новую категорию можно добавить в любой момент, в том числе после начала продаж** (нашлась свободная вместимость зала, отдельная квота для партнёра). Добавление — та же операция «создать категорию с количеством»: появляются её места, вместимость сеанса растёт на её количество, продажи других категорий не затрагиваются. На сайт категория попадает после синхронизации каталога (`GET_ALL_ACTIONS`); для неё выдаётся новый `categoryPriceId` через `compatids.Ensure`.
- **На сеансе со схемой мест добавленная вручную категория — всегда GA** (билеты без места: допродажа стоячих, входных). Новых мест на схеме она не создаёт, только GA-юниты своей категории. У такого сеанса категории двух видов: **seated** — приходит из геометрии, количество = её места на схеме, вручную не меняется, `placement: true`; **GA** — добавлена вручную или из GA-части схемы, количество редактируется, `placement: false`. Сеанс `assigned_seats` при добавлении первой GA-категории становится `hybrid` (в той же транзакции); `hybrid` уже поддержан всеми входами продаж, `GET_SEAT_LIST` (GA-юниты как псевдо-места в секторе с именем категории) и виджетом. Обратно в `assigned_seats` сеанс не переводится.
- Цена меняется свободно (уже есть, включая цены по расписанию, миграция 0087).
- Категорию можно закрыть и открыть. Закрытая не принимает новых броней.
- Один механизм для сеансов со схемой и без неё. Кто владеет количеством у сеанса, привязанного к схеме, — решение 8.

## Решения, нужные от владельца до начала

| # | Вопрос | Рекомендация |
|---|---|---|
| 1 | Закрыли категорию внутри 20-минутного окна оплаты — можно ли оплатить уже оформленный заказ? | Да, места уже закреплены за покупателем |
| 2 | Удаление категории с продажами | Запретить, только закрыть. Удалять — только без проданных и забронированных |
| 3 | Категории без количества | Запретить для GA |
| 4 | Как сайт покажет закрытую категорию (в протоколе Bil24 нет признака «закрыта») | Как «Sold out» (availability 0), без доработки плагина |
| 5 | `ticket_tiers.sale_window_start/end` сейчас нигде не проверяется | Проверять при брони: это автоматическое закрытие по дате |
| 6 | Бесплатные билеты (`htickets/complimentary.go`) и внешние квоты (`hinventory/external_allocations.go`) списывают только ledger и не трогают места. С указанной категорией они зовут `ReserveCapacity(session, tier_id)`, а строку ledger с `tier_id IS NOT NULL` никто не создаёт (`InsertInventoryLedger` везде вызывается с `nil`), поэтому сейчас отвечают 409; без категории списывают строку сеанса, и места расходятся с ledger: места показывают «свободно», бронь падает на ledger | Перевести на места категории в этой же задаче. Строки ledger по категориям в целевой модели не нужны: удалить их в миграции и запретить `ReserveCapacity` с `tier_id` |
| 7 | Импорт пакета в формате Bil24 берёт `capacity` категории из `categoryList[].availability` (`himports/import_exec.go` ~546, спец. 18 п.5). В выгрузке Bil24 это **остаток на момент выгрузки**, а не количество категории; `availability: 0` сейчас даёт `capacity NULL`. Повторный импорт «через квоты» вычтет проданное в источнике поверх проданного в arena | Первый импорт: количество = `availability`. Повторный импорт количество **не меняет** — только цену, окно продаж и закрытие пропавших категорий; количество правится в админке. `availability: 0` при первом импорте — категория с количеством 0 запрещена (решение 3), значит создать закрытой с количеством 1 или пропустить с предупреждением `import.category_sold_out` |
| 8 | У сеанса, привязанного к схеме (`seating_plan_version_id IS NOT NULL`), количество категории живёт в геометрии заблокированной версии: `hseating/bind.go` ~530 создаёт места по `cat.Capacity`, `capacity_total` считается из `CapacityStanding` версии, `himports/seating.go` ~158 пересчитывает из версии; `ticket_tiers.capacity` — только копия. Если менять количество через квоты, повторная привязка/импорт схемы затрут правку | После первой привязки геометрия перестаёт быть источником количества: правда — места и `ticket_tiers.capacity`. Повторная привязка той же версии и повторный импорт схемы не пересоздают GA-места и не трогают количество. Смена версии схемы остаётся запрещённой при наличии броней/билетов (`seating.rebind_forbidden`). Добавленная вручную категория на таком сеансе получает места как любая другая |
| 9 | Добавление категории после начала продаж (см. «Целевая модель») | Разрешить без ограничений; сайт увидит её после синхронизации каталога |
| 10 | Добавленная вручную категория на сеансе со схемой мест — только GA, без мест на схеме. Сейчас `assigned_seats` отвергает GA-бронь в трёх местах (`hbil24/cmd_cart.go` ~188 «categoryList is not supported on assigned_seats», `hcheckout/hold_api.go` ~357 и `hold_mutation.go` ~302 `ErrHoldQuantityNotSupported`, `seat_reservations.go` ~339), а проверка «режим ↔ тип схемы» при привязке (`hseating/bind.go` `planTypesForAdmissionMode`) допускает `hybrid` только с планом `mixed` | Первая GA-категория переводит сеанс `assigned_seats` → `hybrid` в той же транзакции (CHECK `sessions_seated_requires_plan` это допускает: схема остаётся). `planTypesForAdmissionMode` принимает `hybrid` + план `assigned_seats`, когда у сеанса есть GA-категории. Обратный переход не делать, даже после удаления последней GA-категории. Seated-категории: количество только для чтения, закрытие/открытие работает так же, как у GA (закрытая — её места не бронируются) |

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

-- B. GA-сеансы без схемы и вообще без мест (импорт пакета в формате Bil24, arena-seed)
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

-- H. Строки ledger по категориям (решение 6: в целевой модели их нет)
SELECT il.session_id, il.tier_id, il.capacity_total, il.capacity_held, il.capacity_sold
FROM inventory_ledger il WHERE il.tier_id IS NOT NULL;

-- I. Свободные места, на которые ещё ссылается reservation_seats (конвертированные брони
--    после отмены билета): их нельзя удалять, пока не удалена строка join (FK без каскада)
SELECT ss.session_id, ss.seat_key, rs.reservation_id
FROM session_seats ss JOIN reservation_seats rs ON rs.session_seat_id=ss.id
WHERE ss.kind='ga_unit' AND ss.status='available';

-- J. Окна продаж, которые начнут срабатывать по решению 5
SELECT tt.session_id, tt.id, tt.name, tt.sale_window_start, tt.sale_window_end
FROM ticket_tiers tt WHERE tt.deleted_at IS NULL
  AND (tt.sale_window_end < now() OR tt.sale_window_start > now());

-- K. Hybrid-сеансы: места схемы, GA-места категорий и пул из backfill 0084
SELECT s.id, s.capacity_total,
       count(*) FILTER (WHERE ss.kind='seat') AS seats,
       count(*) FILTER (WHERE ss.kind='ga_unit' AND ss.seat_key LIKE 'ga|c%') AS cat_units,
       count(*) FILTER (WHERE ss.kind='ga_unit' AND ss.seat_key LIKE 'ga|pool|%') AS pool_units
FROM sessions s JOIN session_seats ss ON ss.session_id=s.id
WHERE s.deleted_at IS NULL AND s.admission_mode='hybrid' GROUP BY 1,2;
```

### Шаг 2. База данных

- Признак открытия у категории (по умолчанию «открыта»).
- Стабильный номер категории внутри сеанса для ключей новых мест (не переиспользуется после удаления). **Префикс ключа не `ga|c`**: он занят индексом категории геометрии (`ga|c<index>|<n>`, `hseating/bind.go` ~532, `himports/seating.go` ~415), и у сеанса со схемой номер добавленной вручную категории столкнётся с индексом по `UNIQUE (session_id, seat_key)`. Взять отдельный префикс, например `ga|t<номер>|<n>`. Код различает GA-места только по `kind='ga_unit'` и префиксу `ga|` (`htickets/cancel.go` ~414), поэтому третий префикс безопасен.
- Миграция перевода сеансов без схемы:
  - проданным и забронированным местам оставить их категорию;
  - свободные места раздать категориям по «количество − занято»;
  - лишние свободные удалить, недостающие добавить;
  - **удалять только места без строк `reservation_seats`**: `reservation_seats.session_seat_id` ссылается на `session_seats` без каскада (миграция 0058), при конвертации брони строки join остаются («kept on convert»), отмена билета их не чистит (`htickets/cancel.go`) — DELETE такого «свободного» места даёт 23503. Тот же дефект в `DeleteAvailableGAPoolUnits` (`queries/session_seats.sql` ~393), который план берёт за основу сжатия; починить там же (либо `NOT EXISTS (reservation_seats)`, либо чистить join первым);
  - hybrid-сеансам удалить пул `ga|pool|%` с `tier_id NULL` из backfill 0084 (продать его нельзя), с той же оговоркой про FK;
  - удалить строки `inventory_ledger` с `tier_id IS NOT NULL` (решение 6);
  - `capacity_total` и строка ledger сеанса = сумма (для hybrid — места схемы + сумма).
- Ключи существующих мест (`ga|pool|000003`) **не переименовывать**: на них ссылаются `tickets.seat_key`.
- Добавить миграцию — обновить пин головы миграций в тестах (AGENTS.md).

### Шаг 3. Единый механизм квот

Одно место в коде для операций с GA-категорией: создать с количеством (в том числе на сеансе с открытыми продажами — решение 9), изменить количество, закрыть или открыть, удалить. Каждая операция в одной транзакции пересчитывает вместимость сеанса и ledger и держит порядок блокировок: `sessions` (счётчик `seat_status_version`) → `inventory_ledger` → GA-места. Это порядок GA-пути (`createGAHoldTx`); seated-путь блокирует иначе — `sessions` → места `kind='seat'` FOR UPDATE → `inventory_ledger` (`createSeatedHoldTx`, `hold_api.go` ~207), поэтому на `hybrid`-сеансе механизм квот не должен трогать строки `kind='seat'` после ledger. Как и у всех hold-мутаций, тело транзакции — под `retryOnSerializationFailure` (AGENTS.md).

Механизм работает одинаково для `general_admission` и `hybrid`; для сеанса со схемой — по решению 8 (места и `ticket_tiers.capacity` — правда, геометрия не пересоздаёт места после первой привязки). По решению 10 механизм различает вид категории: у seated-категории (есть места `kind='seat'` с её `tier_id`) операции «изменить количество» и «удалить» отвечают 409 `tier.seated_category`, остаются цена и закрытие/открытие; «создать категорию» на сеансе `assigned_seats` создаёт GA-юниты и в той же транзакции переводит сеанс в `hybrid` (`UPDATE sessions SET admission_mode='hybrid'` под тем же row lock, что и счётчик версий). Вид категории хранить не нужно — он выводится из наличия мест `kind='seat'`; `hcatalog/ticket_tiers.go` `HandleListTiers` отдаёт его как `kind: seated|ga`.

Через него переводятся:
- `hcatalog/sessions.go` — создание сеанса и изменение вместимости (~501–618, ~950–1053, включая рост и сжатие пула); там же пересчёт вместимости при смене площадки (~951, `venueChanged` берёт `capacity_default` площадки) — для GA убрать, вместимость всегда сумма категорий;
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
  - Проверка закрытой категории покрывает и seated-категории (решение 10): `hcheckout/seat_reservations.go`, `hbil24/cmd_cart_reserve.go` (seatList) и виджет отказывают в брони места, чья категория закрыта.
- Ветки `admissionMode == "assigned_seats"`, отвергающие GA-бронь (решение 10), остаются: после добавления GA-категории сеанс уже `hybrid`, и эти ветки не срабатывают. Проверить, что `hbil24/cmd_cart.go` ~188 читает режим из базы на каждый запрос, а не из кэша каталога.
- Отмена билета (`htickets/cancel.go` `ReleaseCancelledTicketInventoryTx`) возвращает место в его категорию. Сейчас на пуле место остаётся «available» с категорией и не попадает в выборку `tier_id IS NULL`.
- **`hbil24/cmd_order_create.go:445-452` `orderIsSeated` — обязательная правка, не попутная.** Функция считает заказ «со схемой» по `tier_id != nil` у мест; после миграции категория стоит у каждого GA-места, поэтому сверка строк заказа (`orderReconcile`) для GA-корзин выключится полностью. Переписать на `kind == 'seat'`.
- По решению 6: бесплатные билеты и внешние квоты — через места категории; `ReserveCapacity`/`ConfirmCapacity` с `tier_id` не вызывать.

### Шаг 5. Показ остатка

- `hbil24/cmd_seat_list.go` `seatListAvailability`: остаток = свободные места категории, у закрытой 0. Удалить временную ветку пула.
- `hbil24/cmd_catalog_events.go` `gaCategoryAvailability`: то же. Удалить временную ветку `planLess`. Остаток сеанса (`sessionAvailability`) = сумма свободных мест открытых категорий.
- `hbil24/cmd_cart_view.go:520-549`: `bil24.category_sold_out` с реальным остатком вместо жёсткого 0.
- `placement` категории в `GET_ALL_ACTIONS` и `GET_SEAT_LIST` — по виду категории (решение 10): seated `true`, GA `false`, в том числе на `hybrid`-сеансе, куда GA-категория добавлена вручную. Сейчас `cmd_catalog_events.go` ~290 ставит `false` только для GA-части, проверить ветку `assigned_seats || hybrid` (~204). Сайт после синхронизации показывает такую категорию как «без выбора места» рядом со схемой.
- Публичный API виджета (`hfeed/public_feed.go`) отдаёт остаток категории. Виджет (`apps/widget/src/components/GaTierCard.svelte`) ограничивает выбор остатком, а не объявленным количеством.
- `hcatalog/ticket_tiers.go` `HandleListTiers`: счётчики «продано / в брони / свободно».

### Шаг 6. API и админка

- `openapi.yaml`:
  - `CreateTicketTierRequest` / `UpdateTicketTierRequest`: количество обязательно для GA;
  - признак открытия;
  - `TicketTierItem`: счётчики;
  - вместимость GA-сеанса только для чтения (`capacity_override` для GA убрать);
  - перегенерировать `types_gen.go` и TS-клиент.
- **Совместимость на время между волнами A и B** (стенд нужен Lampyris staging): бэкенд волны A принимает старые запросы — `capacity_override` у GA-сеанса игнорируется с предупреждением в ответе, а не 400; категория без количества на GA-сеансе отвечает 400 `tier.capacity_required` только после выкладки волны B, до неё — получает количество по умолчанию из `capacity_override`/старой вместимости. Виджет до волны B ограничивает выбор по `tier.capacity` (`GaTierCard.svelte` ~13) — это безопасно, бронь всё равно упрётся в остаток.
- Админка (`apps/admin-web/src/routes/events.tsx`):
  - таблица категорий как в Bil24 Editor — категория, количество, цена, продано, свободно, открыта;
  - кнопка «добавить категорию» доступна и у сеанса с продажами (решение 9); у сеанса со схемой мест она добавляет только GA-категорию с пометкой «без места» и предупреждением, что сеанс станет `hybrid` (решение 10);
  - колонка «вид» (seated / GA, поле `kind` из `HandleListTiers`); у seated-категорий количество и удаление недоступны;
  - минимальное количество = занято;
  - поле вместимости у GA-сеанса (`SessionEditor` ~3510) убрать; смена площадки вместимость не меняет.
- `queries/ticket_tiers.sql` `UpdateTicketTier`: сейчас `CASE WHEN $n IS NOT NULL` не даёт очистить поля, админка шлёт `null` и молча ничего не меняет. Исправить явным флагом, как `set_reservation_ttl_override` (AGENTS.md).

### Шаг 7. Импорты и привязка схемы

- Импорт пакета сеанса в формате Bil24 (`himports/import_exec.go` `resolveSession`) сейчас создаёт сеанс **без мест** — «распродан» с первой минуты. Создавать места по категориям.
- Количество при импорте — по решению 7: `availability` в пакете Bil24 — остаток, а не количество. Первый импорт: количество = `availability` (0 — см. решение 7). Повторный импорт (`upsertTiers`, ~546–571) количество **не трогает**: обновляет цену, `sale_window_end`, порядок; категорию, пропавшую из пакета, закрывает, а не удаляет; новую категорию из пакета добавляет с количеством = `availability`.
- `hseating/bind.go` ~358–423: первая привязка схемы к сеансу с пулом оставляет старые места рядом с новыми. Запретить или переводить.
- По решению 8: повторная привязка той же версии и повторный импорт схемы (`himports/seating.go` ~109–166, ~415) не пересоздают GA-места и не пересчитывают вместимость из `CapacityStanding`, если у сеанса уже есть GA-места категорий.

### Шаг 8. Попутные ошибки в том же коде (перепроверить перед исправлением)

- `orderIsSeated` перенесён в шаг 4 как обязательный.
- `hfeed/public_feed_checkout.go`: в ветке чистого GA `ReserveCapacity` (~752) идёт раньше счётчика версий (~806), вопреки порядку блокировок из AGENTS.md.
- Миграция 0084 создала hybrid-сеансам места пула с `tier_id NULL`: продать их нельзя (`AllocateGAUnitsForHold` с `planBound=true` фильтрует по категории), но они учитываются в подсчётах. Удаляются миграцией шага 2.
- Миграция 0084 брала проданное из ledger с `tier_id IS NOT NULL`, которых никто не создаёт (запрос H шага 1), — исторические продажи, вероятно, заведены как 0 проданных.
- `DeleteAvailableGAPoolUnits` не учитывает FK `reservation_seats` (шаг 2).

### Шаг 9. Тесты

Новые:
- после каждой операции квот сумма категорий = вместимость = ledger;
- количество не опускается ниже занятого;
- закрытая категория и окно продаж отказывают на всех входах из шага 4;
- параллельные брони одной категории не продают сверх количества;
- отмена возвращает место в категорию;
- миграция пула на данных как на стенде, включая свободное место со строкой `reservation_seats` от конвертированной брони (запрос I) — миграция и сжатие не падают на FK;
- новая категория на сеансе с проданными билетами: её места продаются, вместимость и ledger выросли ровно на её количество, чужие места не тронуты, `GET_ALL_ACTIONS` отдаёт её с новым `categoryPriceId`;
- `hybrid`-сеанс: вместимость = места схемы + сумма GA-категорий после каждой операции квот;
- сеанс со схемой: изменение количества через квоты переживает повторную привязку той же версии и повторный импорт схемы (решение 8);
- `orderIsSeated` для GA-корзины false, `orderReconcile` выполняется;
- GA-категория на сеансе `assigned_seats` с проданными местами: сеанс стал `hybrid`, её юниты продаются через RESERVATION с `categoryList`, виджет и REST; `GET_SEAT_LIST` отдаёт места схемы плюс псевдо-места категории с `placement:false`; изменение количества и удаление seated-категории отвечают 409; повторная привязка той же версии схемы принимает `hybrid` + план `assigned_seats`;
- закрытая seated-категория: бронь её места отказывает на всех входах;
- импорт пакета в формате Bil24 создаёт продаваемые места; повторный импорт с меньшим `availability` не меняет количество и не роняет его ниже занятого; `availability: 0` при первом импорте — по решению 7;
- бесплатный билет и внешняя квота с категорией занимают места категории, а не строку ledger.

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

- **Волна A — бэкенд:** шаги 1–5, 7, 8 и их тесты. Бэкенд волны A принимает запросы старой админки и старого виджета (шаг 6, «Совместимость»): стенд между волнами остаётся рабочим для Lampyris staging.
- **Волна B — API и админка:** шаг 6. Строгие проверки (`tier.capacity_required`, отказ `capacity_override`) включаются только здесь.
- **Волна C — проверка:** шаг 10.
