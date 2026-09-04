# Перевод WordPress-сайтов (Vino&Co, Lampyris Events) с Bil24 на arena — оценка архитектуры и план волны 1

Дата: 2026-09-04. Статус: предложение владельцу, решения не приняты.
Источники: аудит `C:\Projects\lampyrisevents`, `C:\Projects\vinoandco-prod-rebuild`, `C:\Projects\macs-arenasoldout-com`
(Flutter-приложение + распакованные `backend-develop.zip`, `frontadmin-develop.zip`) и кода arena на `fda3b22`.
Связанные документы: `08_architecture/01_api_compatibility_gateway_ru.md`, `08_architecture/02_wordpress_integration_contract_ru.md`,
`08_architecture/17_macs_integration_contract.md`, `docs/competitive/arena_architecture_fit_2026-09-04.md` (волна A).

Размеры ниже: S — до 2 дней, M — 3–7 дней, L — 8–15 дней, XL — больше; один инженер или один проход AutoForge.

---

## 0. Краткий ответ

1. **Сайты ломать не придётся.** Оба сайта используют один и тот же код и говорят с Bil24 через один POST-эндпоинт
   (`<base>/json`, команда в теле, `fid`+`token` в теле), один GET SVG-схемы (`<base минус /json>/image?type=seatingPlan…`)
   и один входящий вебхук (`/wp-json/bil24/v1/notification`). Для переключения на arena на сайте меняется **одна опция**
   `bil24_acf_sync` (`base_url_prod`, `fid`, `token`) и регистрируется URL вебхука. Всё остальное — пикеры, корзина,
   модалка, Stripe/Allpay, PDF-рассылка, операторская консоль, мост в MACS — работает без правок, если arena честно
   воспроизводит 12 команд и три формата.
2. **Деньги в волне 1 остаются в WooCommerce.** Сайты сами принимают оплату (Stripe на Lampyris, Allpay на Vino) и
   сообщают бэкенду `PAY_ORDER`. Значит, платёжные адаптеры arena (Stripe/Allpay, сегодня не подключены в рантайме)
   для этой волны **не нужны**. Это снимает самый дорогой пункт волны A из предыдущего отчёта и откладывает его до
   этапа маркетплейса arenasoldout.com, где arena принимает деньги сама.
3. **Шлюз bil24compat в arena сделан наполовину и не под тот протокол.** Есть каталог, места, схема, холды, релиз
   холда, скан. Нет создания заказа (`CREATE_ORDER_EXT` → `-5`), нет `PAY_ORDER`, `CREATE_USER`, `GET_CART`,
   `GET_TICKETS_BY_ORDER`; все ID — UUID, а сайты приводят каждый ID к `int`; `GET_ALL_ACTIONS` отдаёт плоский
   список без сеансов и без фильтра по организации (кросс-тенантная утечка); даты RFC3339, а сайт парсит `DD.MM.YYYY`.
   Шлюз включается глобальным флагом, а не per-channel. Итог: **читающая половина есть, продающая — нет.**
4. **Нативный WP-плагин arena (`apps/wp-plugin/arena-events`) для этой задачи не подходит.** Он говорит на
   родном API arena, требует hosted checkout в arena (платежи), ломает WooCommerce-модель сайтов вместе с
   консолью оператора, и имеет четыре дефекта, ломающих прод в первый день. Июньское решение «нативный плагин —
   основной путь, compat — запасной» (`02_wordpress_integration_contract_ru.md`) для двух наших сайтов предлагаю
   **развернуть**: compat-шлюз — основной путь, нативный плагин — для этапа 2 (чужие сайты, не WordPress).
5. **MACS.** Ничего сам не тянет: билеты попадают либо файлом через админку (arena уже экспортирует нужный JSON), либо
   push-вебхуком на `/api/_wh/tickets` без авторизации. Диспетчер arena для `order.paid` шлёт неверную форму `data`
   (одиночный билет вместо заказа с `ticketList`), MACS отвечает HTTP 200 с `{"status":"Error"}`, outbox считает
   доставку успешной. Исправление на стороне arena — S. `ticket.refunded` уже совпадает с тем, что WP-сайты шлют
   в прод.
6. **Заведение мероприятий с WordPress-сайта — обязательная часть волны 1** (решение владельца 2026-09-04).
   Сегодня организатор только получает событие из менеджера Bil24; нужно, чтобы он создавал его на сайте, как без
   мест, так и с разметкой мест. Архитектурно это второй, «пишущий» канал WP → arena рядом с Bil24-шлюзом:
   сервисный ключ канала (ADR-029) + существующие нативные эндпоинты arena (события, сеансы, категории, схемы
   залов, версии плана из SVG или geometry) + вкладка «Мероприятия» в ивент-центре `lampyris-ops`. Для разметки
   мест у arena **нет своего модуля** (визуальный редактор был отложен, категории назначаются цветом в
   Inkscape). Решение владельца — развилка в волне 1: **мероприятия без мест заводятся сразу в админке сайта**
   и уходят в arena; **мероприятия с местами заводятся и размечаются в Bil24**, сайт их импортирует, и то же
   мероприятие синхронизируется в arena, которая дальше ведёт весь учёт мест. Чтобы продукт на сайте не
   задвоился, arena должна сохранять ID Bil24 (`actionId`, `actionEventId`, `categoryPriceId`, `seatId`) как
   свои целочисленные ID для импортированных сущностей. Собственный модуль разметки и заведение мероприятий с
   местами с сайта — следующий шаг (волна 1.1). Подробно в §4.5.
7. **AutoForge** уместен для самой ограниченной и самой тестируемой части — реализации команд шлюза под заранее
   написанными контрактными фикстурами (у нас есть 420 реальных заказов Bil24 в JSON и PHP-код сайтов как
   исполняемая спецификация). Подготовка фикстур, миграция данных, переключение стендов и продов — только
   интерактивно: это стоп-условия гардрейлов и именно там волна 4 потеряла четыре прохода.

---

## 1. Вводная и целевая модель

Модель «клиент работает только на своём сайте, arena — невидимый бэкенд»:

```
WordPress (Elementor + WooCommerce + WPML)            arena (Go)                       MACS (FastAPI + Mongo, Flutter)
  bil24-acf-sync ──GET_ALL_ACTIONS──────────────────▶ каталог (org, channel)
  seat-picker ────GET_SEAT_LIST / image?seatingPlan─▶ session_seats, layout.svg
  cart-bridge ────RESERVATION / GET_CART / promo────▶ reservations + промо
  vino modal ─────CREATE_ORDER_EXT──────────────────▶ заказ (ожидает внешнюю оплату)
  Stripe / Allpay (в WooCommerce) ──PAY_ORDER───────▶ выпуск билетов (worker) ──order.paid──▶ /api/_wh/tickets
  bil24-notification-receiver ◀──order.paid / ticket.refunded / event.*── outbox
  bil24-ticket-mailer (PDF + Brevo)                  ticket.refunded ─────────────────────────▶ /api/_wh/tickets
  lampyris-ops (консоль, возвраты) ──ticket.refunded (уже сегодня)─────────────────────────────▶ /api/_wh/tickets
```

Что важно в этой модели:

- Источник истины по местам, заказам, билетам и штрих-кодам — arena. WooCommerce хранит зеркало заказа и деньги.
- Оплата — на сайте. Arena узнаёт о ней через `PAY_ORDER` (так Bil24 работает с фронтендом типа «Ticketing system»,
  и так помечены заказы Vino: `paymentBankMessage: "Paid per protocol"`).
- PDF билетов и письма делает сайт (`bil24-ticket-mailer` через Brevo). Arena может дублировать доставку, но не обязана.
- Сканер получает билеты от arena, а не от сайта. Сайт сегодня шлёт в MACS только `ticket.refunded`.

---

## 2. Факты по трём сторонам

### 2.1 WordPress-сайты

**Общий код.** Lampyris — порт vinoandco `prod@48f5a2d` с Stripe вместо Allpay, чешским вместо иврита и слоем
`lampyris-events` (CPT `past-event` поверх технического WC-продукта). Плагины `bil24-acf-sync`, `bil24-ticket-mailer`,
`lampyris-ops`, мосты `bil24-cart-bridge`, `bil24-notification-receiver` — по сути идентичны; `lampyris-ops/includes/class-lops-macs.php`
совпадает побайтно.

**Контракт с Bil24 — 12 команд на одном URL** (все `POST <base>/json`, JSON `{command, fid, token, locale, …}`,
успех — `resultCode: 0` внутри HTTP 200):

| Команда | Зачем сайту | Что читает из ответа |
|---|---|---|
| `GET_ALL_ACTIONS` | крон-синк каталога → WC-продукт на каждый сеанс | `cityList[].venueList[]`, `actionList[]{…, actionEventList[]{actionEventId, day, time, currency, sellEndTime, seatingPlanId, availability, chargePercent, categoryLimitList[].categoryList[]{categoryPriceId, price, availability, placement, tariffIdMap}, tariffPlanList[]}}` |
| `GET_SEAT_LIST` | проба «есть ли рассадка», цены/остатки категорий, список мест | `currency`, `categoryList[]{categoryPriceId, categoryPriceName, price, availability, placement, tariffIdMap}`, `seatList[]{seatId, categoryPriceId, tariffPlanId, price, available, location{sector,row,number}}` |
| `CREATE_USER` | сессия покупателя | `userId`, `sessionId` |
| `RESERVATION` (`RESERVE`/`UN_RESERVE` по `categoryList` или `seatList`, `UN_RESERVE_ALL`) | холд | `cartTimeout` (сек), `seatList[]` — **вся корзина сессии**, не дельта |
| `GET_CART` | авторитетная корзина и сумма | `totalSum`, `discountAmount`, `currency`, `actionEventList[]{chargePercent, seatList[]{seatId, categoryPriceId, tariffPlanId, price, discount}}` |
| `ADD_PROMO_CODES`, `CHECK_KDP` | промокоды (шлёт и `promoCodeList`, и `promoCodes`) | `newPromoCodeList`, `existPromoCodeList`, `errorPromoCodeList` |
| `CREATE_ORDER_EXT` | заказ; `orderId` = номер WC-заказа строкой, `fullName/phone/email`, `lines[]`, `chargePercent` | `orderId`, **`totalSum` → `$order->set_total()`**, `sum`, `charge` |
| `PAY_ORDER` | подтвердить внешнюю оплату | только `resultCode` |
| `GET_TICKETS_BY_ORDER` | билеты после оплаты | `ticketList[]{ticketId, pdfUrl?}` или `ticketIdList[]` |
| `SEND_TICKETS_TO_EMAIL` | письмо от Bil24 (подавляется, если включён свой мейлер) | только `resultCode` |
| `CANCEL_RESERVATION` | только для неоплаченных (в текущем коде фактически мёртв) | только `resultCode` |

Плюс `GET_ORDER_INFO` только у Vino (Google Sheets). Легаси-виджет Bil24 (`tickets/` в репо Lampyris, копия в
`vinoandco-prod-rebuild/docs/widget`) уже выведен из эксплуатации — выбор билетов и Stripe живут на сайте; его
19 команд не эмулируем, папку можно удалить из репо и из `start.sh`.

**Три формата, которые нужно воспроизвести дословно.**

- SVG-схема: `GET <host>/image?type=seatingPlan&actionEventId=<int>&userId=0&fid=<fid>&locale=<loc>`, где host —
  API-base без `/json`. JS сайта читает атрибуты с префиксом `sbt:` (fallback на namespace
  `http://www.w3.org/2015/sbt/1.0`): категории в `<metadata>` с `sbt:id/sbt:index/sbt:name/sbt:color/sbt:price`,
  места — `<circle sbt:id sbt:state sbt:cat sbt:seat>`, где `sbt:cat` — **индекс** категории, сектор — ближайший
  `<g sbt:sect>`; `sbt:state` 1 — свободно, 4 — занято; `viewBox` обязателен.
- Вебхуки в сайт: `POST /wp-json/bil24/v1/notification`, без подписи, `{type, data}`; типы `test`,
  `event.created|changed|deleted` (тело — сеансы; сайт запускает полный ресинк трижды), `order.paid`
  (`data{id, status, sum, totalSum, discount, charge, ticketList[]}`), `order.cancelled`, `ticket.refunded`
  (`data{id, orderId, barcode, refundPrice, refundDate, category}`; дедуп по `data.id`).
- Билет в `ticketList`: `id, barcode, barcodeFormat{id,name}, seatId, holderStatus, category` (**строка**),
  `tariff, price, totalPrice, seatLocation` (null | строка | `{sector,row,number}`), `actionEvent{id, actionId,
  actionName, venueName, cityName, actionLegalOwner, currency, showTime}`. Мейлер и MACS-мост требуют `showTime`
  (не `beginDate`) и `actionEvent.currency`; `category` объектом ломает PDF.

**Квирки, на которые сайт опирается.** Все ID — целые (`(int)` при чтении; UUID превращается в 0). Даты в
`GET_ALL_ACTIONS` — `DD.MM.YYYY` и `HH:MM`, разбираются вручную. `totalSum` из `CREATE_ORDER_EXT` переписывает
сумму WC-заказа до создания PaymentIntent. `resultCode: 1` — протухшая сессия, сайт пересоздаёт пользователя.
`description` показывается покупателю как есть. Корзина — плоский список мест даже для GA (количество
считается), GA на комбинированных сеансах — псевдо-места с `placement: false`; отсутствующий `placement` = рассадка.
`chargePercent` (сервисный сбор) сайт берёт из `GET_CART`. Штрих-код печатается в PDF как есть (EAN-13 с валидной
контрольной цифрой, иначе Code128).

**Конфиг-поверхность для переключения.** Опция `bil24_acf_sync` (`environment`, `fid`, `token`, `locale`,
`base_url_prod`, `base_url_test`); хардкоды хостов только в дефолтах и в легаси-виджете `tickets/`. Рецепт есть в
`docs/checklists/prod-rollout.md` Lampyris. Vino сидит на `api.tixgear.com`, Lampyris — на `api.bil24.pro`
(один и тот же API, два white-label хоста).

**Отличия Vino&Co.** Allpay (сервер-к-серверу `getpayment`, подпись sha256 по рекурсивному ksort, `paymentstatus`
крон каждые 15 минут, `refund` со статусами 3/4 и номерами налоговых квитанций), ILS, `billing_country = IL`,
иврит/RTL как первый класс (WPML ru/he/en, база RU), Google Sheets-реестр на 43 колонки (кормится мета-данными
WC-заказа из вебхука `order.paid`, не отдельным вызовом), бэкфилл-скрипты, Inkscape-схемы Escape Bar
(32 стола / 100 мест, загружались в редактор Bil24). За 6 месяцев — порядка 350 заказов, все GA, ни одного
билета с `seatLocation`. Реальные JSON-выгрузки заказов лежат в `vinoandco-prod-rebuild/docs/samples/`
(420 заказов, 85 возвращённых билетов) — это единственные настоящие сэмплы контракта на диске.

**Что из этого не касается arena.** Allpay, Stripe, GSheets, Brevo, PDF, консоль оператора, PWA — всё живёт на
сайте и продолжает работать, пока arena отдаёт те же вебхуки с той же мета-информацией.

### 2.2 arena

**Шлюз `bil24compat` + `hbil24`.** Один роут `POST /compat/bil24/json`, включается только `BIL24_COMPAT_ENABLED=true`
(глобально, не per-channel), не описан в `openapi.yaml`. `fid` = UUID `sales_channels.id`, `token` сверяется bcrypt
с `settings.gateway_token_hash`, для которого нет ни admin API, ни CLI (только ручной SQL). Токен проверяется лишь
на мутирующих командах; `GET_ALL_ACTIONS`, `GET_SEAT_LIST`, `GET_SCHEMA`, `GET_ORDER_INFO` — без авторизации и без
привязки к организации.

| Команда сайта | В arena | Разрыв |
|---|---|---|
| `GET_ALL_ACTIONS` | есть | плоский `actionList` без `actionEventList`, без городов/площадок, без `org_id`-фильтра (утечка между тенантами), фильтр по `visibility`, а не по `status=published`; даты RFC3339 |
| `GET_SEAT_LIST` | есть, два режима | `availableCount = capacity` (всего, не остаток); нет `placement`, `available`, `location{}`; статус — BSS-int |
| `GET_SCHEMA` | есть | не нужен сайту (он берёт SVG) |
| `CREATE_USER` | нет | нет понятия userId/sessionId |
| `RESERVATION` | есть RESERVE/UN_RESERVE | семантика «одна reservation = один чекаут», нет накопительной корзины сессии, нет `UN_RESERVE_ALL`, ответ — дельта, а не корзина |
| `GET_CART` | нет | — |
| `ADD_PROMO_CODES` / `CHECK_KDP` | `-5` / нет | нативный промо-движок есть |
| `CREATE_ORDER_EXT` | `-5` | нет агрегата заказа; buyer name/phone сегодня выбрасываются (см. волна A, Б1) |
| `PAY_ORDER` | нет | нет «внешняя оплата подтверждена»; в `payment_provider_configs` уже есть slug `manual` |
| `GET_TICKETS_BY_ORDER` | нет | публичный PDF по `checkout_token` есть |
| `SEND_TICKETS_TO_EMAIL` | нет | delivery_jobs есть |
| `CANCEL_RESERVATION` / `CANCEL_ORDER` | нет / `-5` | — |
| SVG `image?type=seatingPlan` | есть `GET /v1/event-sessions/{id}/layout.svg` с `sbt:`-атрибутами | другой URL и ключ (UUID сеанса вместо `actionEventId`+`fid`), namespace `http://bil24.pro/sbt`, категории как `<circle sbt:index…>`-свотчи, а не `<metadata>`-узлы; коды состояний 0–5 против 1/4 у сайта |
| Вебхуки в сайт | `webhook_subscribers`, HMAC, outbox, ретраи | нет «Bil24-flavour» подписчика для WP; форма билета уже есть в `macs/export.go` |
| Целочисленные ID | только `tickets.system_ticket_id`, `session_seats.system_seat_id` (0088) | нужны для событий, сеансов, категорий, заказов, пользователей шлюза |
| Штрих-код | `static_qr` = 64 hex | сайту и MACS нужен числовой EAN-13 |
| Коды результата | 0, −1…−5, −99 | у Bil24 −1 = «повторите», 1 = «сессия протухла», 101 = «покажи пользователю»; коллизия по −1 |

Тесты шлюза — только unit на фейках; обещанной в `doc.go` папки `tests/compat/bil24` с реальными фикстурами нет.

**Платежи.** `adapters/stripe` и `adapters/allpay` реализованы и покрыты тестами, но не импортируются ни одним
не-тестовым файлом; `CreateIntent` в проде не вызывается. Для волны 1 это не блокер (деньги на сайте), для
маркетплейса — блокер.

**Тенантность.** Модель подходит: две организации, по каналу с `provider='allpay'`/`'stripe'`, валюта на тарифе
(ILS/CZK), по feed-токену на сайт. Блокирует: нет API-ключей для сервер-к-серверу (ADR-029 proposed), `SCAN_TICKET`
ищет канал глобально, `GET_ALL_ACTIONS` кросс-тенантен, нет модели налогов/юрисдикции (ADR-030/031).

**WP-плагин `arena-events`.** v0.1.0, без PHP-тестов, три модели интеграции сразу; слушает несуществующие имена
вебхуков (`order_paid` вместо `v1.order.placed`), подделывает остаток (`capacity_available = capacity`),
редиректит на относительный `/checkout/{uuid}` на домене WP (404), Gutenberg-блок не получает feed-token.
Для наших сайтов не используется; для этапа 2 нуждается в переписывании по контракту `02_wordpress…`.

**Импорт из Bil24** (`cmd/arena-bil24-import`) переносит оболочки событий и географию; сеансы, категории, схемы,
заказы, билеты — нет.

### 2.3 MACS

FastAPI + MongoDB, `root_path=/api`. Авторизация — opaque bearer из коллекции `session`, пароли SHA-256 без соли.
`POST /api/_wh/tickets` — **без авторизации**; `POST /api/auth/register` открыт и принимает `roles` от клиента;
`POST /api/admin/init` открыт. Это не наша задача, но владельцу стоит знать.

Пути попадания билетов: (а) файловый импорт в админке (JSON в форме заказа Bil24, CSV TicketTailor, XLSX
TicketsCloud, голый список штрих-кодов); (б) push на `/_wh/tickets` — три принимаемые формы, для `order.paid`
нужны `data.ticketList` **и** `data.status == "PAID"`; (в) пула нет вообще — `ticket_systems` это ярлык, не
коннектор. Успех — только `{"status":"OK"}` в теле, HTTP всегда 200. Событие матчится по `actionEvent.id`
(`origin_id`), `showTime` не учитывается. Штрих-код — глобальный уникальный ключ на всю базу MACS. Статусы 0/1/2/3;
на импорте `holderStatus` игнорируется (все 0), т. е. повторный экспорт «воскрешает» отменённые билеты.
Сканирование — онлайн, без офлайн-кэша; результаты сканов никуда не экспортируются. Мобильное приложение видит
только 10 последних событий (не передаёт `limit`) и бьёт в несуществующий роут статистики.

Сверка с arena: `ticket.refunded` совместим (та же форма, что шлёт `class-lops-macs.php`); `order.paid` — нет
(одиночный билет в `data`, нет `status`); стаб-приёмник в тестах arena принимает то, что прод отвергает;
`actionEvent.id` привязан к событию, а не к сеансу; `barcodeFormat` объявлен EAN-13 при 64-hex токене.

---

## 3. Варианты реализации

| | A. Drop-in: достроить Bil24-совместимый шлюз | B. Нативный WP-плагин arena | C. Гибрид: A сейчас, B для этапа 2 |
|---|---|---|---|
| Изменения на сайтах | опция `bil24_acf_sync` + URL вебхука; в ивент-центре: один вызов `REFUND_TICKET` вместо ручного шага в менеджере Bil24 и новая вкладка «Мероприятия» | замена всего покупочного слоя (пикеры, корзина, модалка, мейлер, консоль, MACS-мост), WooCommerce уходит | как A |
| Объём в arena | L–XL (команды, корзина сессии, заказ, int-ID, SVG-роут, вебхуки, EAN-13, tenant-scoping) | XL (платежи в рантайме + hosted checkout + переписать плагин + организаторский shell) | как A, B позже |
| Платежи в arena | не нужны | нужны (Stripe и Allpay) | не нужны сейчас |
| Риск для прода | низкий: откат = вернуть URL; параллельный прогон на staging обоих сайтов | высокий: миграция живых WC-заказов, консоль оператора теряет источник данных | как A |
| Что теряем | шлюз «замораживает» Bil24-протокол как внешний контракт arena (адаптер, не ядро — так и задумано в `01_api_compatibility…`) | легаси-совместимость для партнёров и отката | ничего |
| Соответствие вводной «не ломать сайты» | да | нет | да |

**Рекомендация — C, то есть A как волна 1.** Это ровно тот путь, который описан в `01_api_compatibility_gateway_ru.md`
(«домен и креды меняются, поток команд и структуры ответов — нет»), а не в `02_wordpress…`. Требует зафиксировать
разворот ADR-011/012/013 для наших двух сайтов: WooCommerce остаётся, checkout остаётся на сайте, `arena_event`-CPT
не вводится (у Lampyris уже есть свой `past-event`, у Vino событие = продукт).

---

## 4. Что должна сделать arena в варианте A

### 4.1 Каркас шлюза (обязательно, до команд)

| # | Работа | Размер |
|---|---|---|
| К1 | Per-channel включение шлюза вместо глобального флага; `fid` → канал → организация на **всех** командах, включая читающие; `SCAN_TICKET` — только в своей организации | S |
| К2 | Провижининг `gateway_token_hash`: admin-эндпоинт `PUT /v1/organizations/{org}/channels/{id}/gateway-credential` (секрет возвращается один раз, как у MACS-вебхука) | S |
| К3 | Целочисленные системные ID: bigint-последовательности для событий, сеансов, категорий (tiers), заказов, пользователей шлюза — по образцу 0088; таблица соответствия для импортированных Bil24-ID (`compatibility_id_map`, давно TODO) | S–M |
| К4 | Сессия шлюза: таблица `gateway_users` (`userId`, `sessionId`, org, channel, TTL) и накопительная корзина сессии поверх `reservations` (RESERVE/UN_RESERVE дельтами по местам и категориям, `UN_RESERVE_ALL`, ответ = вся корзина, `cartTimeout`) | M |
| К5 | Коды результата и тексты: `1` для протухшей сессии, `101` с локализованным `description` для «место занято / нет мест», `-1` только для системных ошибок; `description` на языке `locale` | S |
| К6 | Контрактные фикстуры: `tests/compat/bil24/` с реальными телами из `docs/samples/*.json` Vino и с запросами, снятыми с PHP; golden-тесты на каждую команду | M (интерактивно, до AutoForge) |

### 4.2 Команды

| Команда | Работа | Размер |
|---|---|---|
| `GET_ALL_ACTIONS` | полная вложенная форма: `countryList/cityList/venueList`, `actionList[].actionEventList[]` с `day` `DD.MM.YYYY`, `time` `HH:MM` в TZ площадки, `currency`, `sellEndTime`, `seatingPlanId`, `availability` (остаток), `chargePercent`, `categoryLimitList[].categoryList[]` с `placement` и `tariffIdMap`; только `published` события своей организации | M |
| `GET_SEAT_LIST` | `placement` (тристейт как у Bil24), `available` bool, `location{sector,row,number}`, остаток вместо capacity, псевдо-места для GA на комбинированных сеансах, `tariffIdMap` | M |
| `CREATE_USER` | из К4 | S |
| `RESERVATION` | переложить на корзину сессии из К4; сохранить существующие проверки конфликтов | M (в К4) |
| `GET_CART` | плоский `seatList` (GA как псевдо-места), `totalSum/discountAmount/currency`, `chargePercent` по сеансу | S |
| `ADD_PROMO_CODES`, `CHECK_KDP` | обёртка над нативным промо-движком; принимать оба ключа; классификация new/exist/error | S |
| `CREATE_ORDER_EXT` | агрегат заказа (волна A, A1): `orderId` int, `external_ref` = номер WC-заказа, buyer `fullName/phone/email`, строки, `sum/discount/charge/totalSum` из `ComputePricingLines` с `chargePercent`; статус «ожидает внешней оплаты» | M (плюс A1 — L) |
| `PAY_ORDER` | подтверждение внешней оплаты: payment_intent провайдера `manual/external` с `method` строкой, переход в paid, постановка существующего `checkout.issue_tickets` job; идемпотентно | S–M |
| `GET_TICKETS_BY_ORDER` | `ticketList[]{ticketId, pdfUrl}` (pdfUrl → существующий публичный PDF), принимать `orderId` и строкой, и числом | S |
| `SEND_TICKETS_TO_EMAIL` | `resultCode 0` + опционально delivery_job | S |
| `CANCEL_RESERVATION`, `CANCEL_ORDER` | релиз холда неоплаченного заказа | S |
| `REFUND_TICKET` (расширение arena, обязательно) | возврат с сайта уже реализован в ивент-центре (`lampyris-ops`: деньги через WooCommerce, MACS уведомляется, статус «ожидает отмены в Bil24»); сегодня отмену билета оператор делает руками в менеджере Bil24, а сайт сверяется по вебхуку `ticket.refunded`. В arena этот ручной шаг становится командой: обёртка над AB-49 `POST /v1/tickets/{id}/cancel` с `refund_mode=manual`, ответ — `resultCode`, затем штатный вебхук `ticket.refunded` в сайт и в MACS. На сайте — один вызов из `Lops_Refunds` вместо заметки «отмените в Bil24» | S + S (WP) |

### 4.3 Форматы

| # | Работа | Размер |
|---|---|---|
| Ф1 | Роут `GET /compat/bil24/image?type=seatingPlan&actionEventId&fid&locale` поверх `RenderBSSLayoutSVG`: ключ по int-ID сеанса, namespace `http://www.w3.org/2015/sbt/1.0`, категории как `<metadata>`-узлы с `sbt:index`, `sbt:cat` = индекс, состояния 1/4, `viewBox` | S–M |
| Ф2 | Штрих-коды EAN-13: новый тип credential (числовой, валидная контрольная цифра, свой namespace в `barcodes`), используемый в PDF сайта и в MACS; `static_qr` остаётся для виджета arena | S |
| Ф3 | Вебхуки в сайт: подписчик `kind='bil24_wp'` на канал, envelope `{type, data}`, `order.paid` с `ticketList` в форме Bil24 (вынести маппер билета из `macs/export.go` в `bil24compat`, MACS его переиспользует), `order.cancelled`, `ticket.refunded` с полями `{id, orderId, barcode, refundPrice, refundDate, category}`, `event.created/changed/deleted`, `test`; HMAC-подпись опционально (сайт её не проверяет) | M |
| Ф4 | Поля билета: `category` строкой, `showTime` в локальном времени площадки, `actionEvent.currency`, `holderStatus` `NEVER_USE`/`REFUND` как в реальных сэмплах (не `REFUNDED`) | S (в Ф3) |

### 4.4 MACS (arena-сторона, без изменений в MACS)

| # | Работа | Размер |
|---|---|---|
| М1 | `order.paid`: `data` = заказ `{id, status:"PAID", ticketList[]}`; `ticket.refunded` не трогать | S |
| М2 | Успех только при `{"status":"OK"}` в теле; иначе ошибка → ретрай outbox | S |
| М3 | `actionEvent.id` = int-ID **сеанса**, не события (одна ночь на воротах = одно событие MACS); `actionId` int | S |
| М4 | `barcodeFormat` = реальный формат из Ф2; URL подписчика заканчивается на `/api/_wh/tickets`; slug системы в реестре MACS — `arena` | S |
| М5 | Стаб-приёмник в тестах привести к реальному поведению (отвергать без `ticketList`/`status`, 200 с `Error`) | S |

### 4.5 Заведение мероприятий с сайта (поток «W1-C», обязательный в волне 1)

Сегодня: событие создаётся в менеджере Bil24, там же размечается схема, сайт тянет результат и дописывает контент.
Цель: организатор делает то же самое в ивент-центре своего сайта, а arena — источник истины; продукт на сайте
появляется штатным синком через `GET_ALL_ACTIONS` и вебхук `event.created`.

**Архитектура — два слоя рядом с Bil24-шлюзом.**

1. **Пишущий канал WP → arena.** Сервисный ключ канала (ADR-029): principal с правами
   `event.write`, `session.write`, `tier.write`, `venue.read`, `seating_plan.write`, `media.write`, только своя
   организация, выдача и ротация в admin-web (по образцу секрета MACS-вебхука). Эндпоинты записи уже существуют
   под JWT: события, сеансы, категории и цены (AB-48), площадки, планы залов и их версии
   (`POST /v1/seating-plans/{id}/versions` принимает `svg` или `geometry`), привязка плана к сеансу с картой
   категория → тариф, публикация. Новое — только принципал ключа и middleware.
2. **UI на сайте.** Вкладка «Мероприятия» в `lampyris-ops` (у него есть тесты, роль оператора, PWA, три
   языка): событие → сеансы (дата, площадка, валюта, окно продаж) → категории с ценами и вместимостью →
   публикация. Для Vino и Lampyris это единая форма, как сегодня у `lampyris-events`.

**Развилка волны 1 (решение владельца 2026-09-04).**

```
Мероприятие БЕЗ мест:   админка сайта ──(ключ канала)──▶ arena ──GET_ALL_ACTIONS / event.created──▶ продукт на сайте
Мероприятие С местами:  менеджер Bil24 (сеанс + разметка) ──▶ сайт импортирует ──▶ arena регистрирует событие,
                        сеанс, категории, схему и места с ТЕМИ ЖЕ ID ──▶ дальше все холды/продажи/остатки ведёт arena
```

Условие, без которого вторая ветка не работает: **arena сохраняет ID Bil24 как свои целочисленные ID** для
импортированных сущностей (`actionId`, `actionEventId`, `categoryPriceId`, `seatId` → `system_*_id`;
таблица `compatibility_id_map` из К3), а для событий, созданных в arena, выдаёт ID из непересекающегося
диапазона. Тогда продукт на сайте, созданный по `bil24_action_event_id` при импорте из Bil24, — тот же самый,
который потом видит штатный синк из arena; дублей нет, пикер мест получает от arena SVG с теми же `sbt:id`,
что были в Bil24, и все `bil24_*`-меты на сайте остаются валидными.

**Транспорт второй ветки — сайт-ретранслятор (решение владельца 2026-09-04):** arena подвязана только к сайту
и с Bil24 не общается. `lampyris-ops` держит второй, «исходный» набор кред Bil24 (тестовый агент/фронтенд на
staging, свой на проде), вызывает у Bil24 `GET_ALL_ACTIONS`, `GET_SEAT_LIST` и `image?type=seatingPlan`,
собирает событие/сеанс/категории/sbt-SVG и отправляет в arena по ключу канала одним вызовом
`POST /v1/organizations/{org}/imports/bil24-session` (новый эндпоинт, идемпотентный по `actionEventId`).
Существующий синк `bil24-acf-sync` при этом смотрит уже на arena; путь к Bil24 нужен только модулю импорта.
Размер: M (WP) + M (arena: приём sbt-SVG, регистрация внешних ID, публикация).

**Правило ID (фиксирую как архитектурное).** Все целочисленные ID, которые видит сайт, живут в
`compatibility_id_map (kind, system_id, platform_uuid, source)`, `kind ∈ {action, action_event, category_price,
seat, order, user}`. Для сущностей, импортированных из Bil24, `system_id` = ID Bil24 (`source = bil24`), поэтому
продукт и меты на сайте не меняются. Для сущностей, созданных в arena, `system_id` выдаётся из последовательности,
начинающейся с 1 000 000 000 (`source = arena`); диапазоны не пересекаются, коллизия с Bil24 невозможна
(его ID на порядок меньше). Существующие `system_ticket_id`/`system_seat_id` (0088) остаются и получают
`source`. Один UUID ↔ один `system_id` навсегда; повторный импорт того же `actionEventId` — обновление, не дубль.

После импорта Bil24 больше не участвует: `RESERVATION`, заказы, билеты, остатки — только arena. Сеанс в Bil24
остаётся «пустым» (продажи там не открываются), это надо зафиксировать в инструкции оператора.

**Разметка мест — три пути, от переходного к целевому.** Волна 1 идёт по Р0 (редактор Bil24 + импорт через
В1/В2), Р1 остаётся как запасной для площадок, схемы которых есть в Inkscape; Р2 — волна 1.1 вместе с заведением
мероприятий с местами прямо с сайта. Пока идёт Р0, аккаунт Bil24 нужен только ради редактора схем.

| Путь | Что это | Статус в arena | Размер |
|---|---|---|---|
| Р0 переходный | разметка в редакторе Bil24 (сеанс заводится там только ради разметки), arena импортирует **sbt-SVG**, который Bil24 отдаёт по `image?type=seatingPlan` (категории в `<metadata>`, `sbt:cat` на местах) | парсера sbt-формата нет; есть парсер Inkscape-конвенций | S |
| Р1 есть сегодня | Inkscape-SVG, категории заданы цветом заливки и свотчами `PriceCategory`, сектора `#Сектор N` | `svg_import.go`, загрузчик в admin-web; проверить на файлах Escape Bar (`#Стол N`, круг = место, 15 групп категорий) | S |
| **Р2 целевой** | **собственный модуль разметки arena**: справа схема, слева таблица секторов/рядов/мест; категория назначается кликом, лассо, по ряду или сектору на схеме **и** из таблицы, синхронно; легенда категорий с ценами и цветами; GA-зоны с вместимостью; блокировка мест | нет. Есть заготовки: рендер geometry → SVG с кликом и множественным выбором по сектору/ряду в `sessionSeats.tsx` (SEAT-E3), модель `categoryIndex` на каждом месте, версии плана из `geometry` | M–L |

**Где живёт Р2.** Как встраиваемый компонент arena (web component на стеке виджета: Svelte 5, Shadow DOM,
те же CI-ворота по размеру и доступности), а не как PHP-код сайта. Он работает с канонической геометрией и
сохраняет новую версию плана через существующий API; в ивент-центр WP встраивается по короткоживущему токену,
который сайт получает по ключу канала; тот же компонент потом ставится в admin-web и на маркетплейс. Границы
модуля в волне 1: назначение категорий, цен и статусов на готовой геометрии; рисование и перемещение мест
(«редактор зала») — не входит, геометрия по-прежнему приходит из SVG.

| # | Работа | Размер |
|---|---|---|
| C1 | Сервисный ключ канала (ADR-029) и принципал с org-scoped правами; выдача/ротация в admin-web | M |
| C2 | Вкладка «Мероприятия» в `lampyris-ops` для мероприятий **без мест**: событие, сеансы, категории/цены/вместимость, публикация; вызовы REST arena по ключу | M (WP) |
| C3 | Ветка «с местами»: модуль импорта в `lampyris-ops` (второй набор кред Bil24, pull события/сеанса/категорий/`GET_SEAT_LIST`/sbt-SVG, отправка в arena) + эндпоинт приёма в arena с регистрацией ID Bil24 и публикацией; Р1: проверка Inkscape-файлов Escape Bar на существующем импортёре | M (WP) + M (arena) + S |
| C6 | Покупатель как сущность платформы (§4.6): `customers`, `customer_identities`, `customer_org_links`, `customer_attributes`, резолюция на `CREATE_USER`/`CREATE_ORDER_EXT`/`PAY_ORDER`, `userId` шлюза = ID покупателя, один открытый заказ на покупателя и сеанс | M |
| C7 | Импорт баз покупателей супероператором (§4.6): `customer_imports` с маппингом на организации, dry-run, идемпотентность, правовое основание; загрузка накопленных баз Bil24/WooCommerce/GSheets/Brevo | S–M (интерактивно) |
| C4 | Р2: модуль разметки как embeddable-компонент и заведение мероприятий с местами с сайта — **волна 1.1** | M–L |
| C5 | Выдача плана в Bil24-контракт: `seatingPlanId/seatingPlanName` в `GET_ALL_ACTIONS`, `placement` по категориям, SVG по Ф1 | в §4.2/§4.3 |

---

### 4.6 Покупатель как сущность платформы

**Как сейчас.** В arena покупателя нет. Есть `users` — это сотрудники и зарегистрированные аккаунты с ролями,
согласиями GDPR и анонимизацией; `checkout_sessions.user_id` и `reservations.user_id` ссылаются на них и при
публичной покупке пусты. От покупателя остаётся только `tickets.holder_email`; имя и телефон из формы
проверяются и выбрасываются (`hfeed/public_feed_checkout.go:308-337`). Два заказа одного человека ничем не
связаны; один и тот же e-mail на двух сайтах — два ничем не связанных `holder_email`. На сайтах: WooCommerce
хранит своего покупателя (billing e-mail/телефон, необязательный WP-аккаунт), Bil24 выдаёт `userId` через
`CREATE_USER` (e-mail/телефон в нём обычно пустые), и сайт кладёт этот `userId` в мету WP-пользователя, если тот
залогинен, иначе в WC-сессию (куку). В выгрузках Bil24 у заказов `user.email` пустой — там идентификации тоже
фактически нет. Неудавшаяся оплата на сайте оставляет `pending`-заказ WooCommerce и холд, который истекает по TTL;
следующая попытка создаёт новый заказ и новый холд.

**Предложение — `Customer` как платформенная, а не организационная сущность.**

```
customers            id (uuid), system_id (int, = userId шлюза), display_name, locale, created_at, merged_into
customer_identities  customer_id, kind ∈ {email, phone, telegram, device, wc_customer, bil24_user},
                     value_normalized (e-mail в нижнем регистре, телефон в E.164, telegram user id),
                     channel_id (для device/wc_customer — привязка к сайту), verified_at, first_seen, last_seen
customer_consents    customer_id, org_id, kind ∈ {terms, marketing}, given_at, source
```

- **Один покупатель на всю платформу**, заказы и билеты — по организациям. Организация видит только тех
  покупателей, у которых есть её заказы; сквозной профиль (все сайты, все организации) — только у платформы.
  Маркетинговое согласие — на организацию, не глобальное: это требование GDPR и разумное ожидание клиента.
- **Сильные ключи**: подтверждённый e-mail, подтверждённый телефон, Telegram user id. **Слабые**: cookie/device
  (это `sessionId`/`userId` шлюза в WC-сессии), WC customer id сайта, имя. Совпадение по любому сильному ключу →
  тот же покупатель. Конфликт сильных ключей (e-mail принадлежит A, телефон — B) → не сливать автоматически,
  создать кандидата на объединение для оператора платформы; иначе семья с одним телефоном превращается в одного
  человека. Слияние — через `merged_into`, история заказов переезжает, обратимо.
- **Где происходит резолюция.** В Bil24-шлюзе: `CREATE_USER` с e-mail/телефоном → найти или создать покупателя и
  вернуть его `system_id` как `userId` (сайт уже сохраняет `bil24_user_id` в мету WP-пользователя и в мету
  заказа — правок на сайте нет); `CREATE_ORDER_EXT` с `fullName/phone/email` → дорезолвить и привязать заказ;
  `PAY_ORDER` → пометить e-mail/телефон подтверждёнными фактом оплаты. В нативном API: `checkout/start` с
  `buyer{}` — то же. Позже: регистрация/вход по OTP на e-mail или телефон, Telegram Login и Mini App
  (телефон через `request_contact`) — те же таблицы, только `verified_at`.
- **Повторные заказы одного человека.** Правило «один открытый заказ на покупателя и сеанс»: при
  `CREATE_ORDER_EXT` от того же покупателя на тот же сеанс, пока предыдущий заказ не оплачен и холд жив, arena
  возвращает тот же `orderId` с обновлёнными строками, а не создаёт новый; истёкший — закрывается как
  `abandoned` с причиной. Все попытки видны в карточке покупателя. На сайте WooCommerce по-прежнему может
  создавать свой `pending`-заказ на каждую попытку — это его зеркало, и ссылка на один `bil24_external_order_id`
  делает эти попытки узнаваемыми в консоли.
- **Что это даёт сразу**: карточка покупателя с историей по всем сайтам, поиск заказов по e-mail/телефону
  (P0 из TT-отчёта), возвраты «этому человеку», честная воронка «попытки → оплаты», одна GDPR-точка для экспорта
  и удаления. **Чего не даёт и не должно**: сквозной рекламы между организациями без согласия.

**Решения владельца 2026-09-04:** один покупатель на платформу; согласия — по организациям; один покупатель
может быть привязан к нескольким организациям; супероператор платформы должен уметь загрузить базы покупателей
с разметкой по организаторам и с метаданными, накопленными за время работы на Bil24.

Что это добавляет к схеме:

```
customer_org_links   customer_id, org_id, first_order_at, last_order_at, orders_count, tickets_count,
                     source ∈ {order, import}, attributes jsonb        -- связь «покупатель ↔ организация», N:M
customer_attributes  customer_id, org_id (nullable = платформенные), key, value, source, imported_at
                     -- метаданные: сегменты, теги, интересы, город, язык, «VIP», история промокодов и т. п.
customer_imports     id, org_id (nullable = мультиорг-файл), source_label, file_media_id, mapping jsonb,
                     status, dry_run_report jsonb, created_by (superadmin), legal_basis, created_at
customer_import_rows raw jsonb, resolved_customer_id, action ∈ {created, matched, merged_candidate, skipped}, reason
```

Импорт баз (только `platform.superadmin`, с `X-Admin-Reason`, через `worker_jobs`):

- **Форматы и источники**, которые у нас реально есть: выгрузки заказов Bil24 в JSON (`fullName`, `phone`,
  `email`, `agent`/`frontend` → организация, `actionEvent` → интересы, `discountReason` → промокоды), экспорт
  покупателей и заказов WooCommerce обоих сайтов (CSV), Google Sheets-реестр Vino (43 колонки), списки контактов
  Brevo с тегами. Один файл может содержать покупателей нескольких организаторов — колонка или правило
  сопоставления `agent/frontend/site → org_id` в `mapping`.
- **Проход через ту же резолюцию идентичности**, что и живые заказы: нормализация e-mail/телефона, сильные ключи,
  конфликт → `merged_candidate`, а не автослияние. Сначала `dry_run` с отчётом «создано / сопоставлено /
  кандидаты / пропущено», потом применение. Повторная загрузка того же файла идемпотентна (хэш строки).
- **Правовое основание** фиксируется на импорт (`legal_basis`: договор с организатором, legitimate interest,
  явное согласие); импортированные согласия помечаются `source = import:<label>` и не считаются подтверждёнными
  маркетинговыми согласиями, пока покупатель их не подтвердит на сайте организации. Это и защита по GDPR, и то,
  что позволит потом честно делать рассылки.
- **Историю заказов** импорт не создаёт (решение 4 в §7): он создаёт покупателя, его связи с организациями и
  агрегаты (`first/last_order_at`, `orders_count`) как атрибуты, чтобы карточка покупателя не была пустой.

Размер в волне 1 — M (таблицы, нормализация, резолюция в трёх командах, правило одного открытого заказа,
связи с организациями) плюс S–M на инструмент импорта с dry-run (интерактивно: миграция данных — стоп-условие
для AutoForge). Аккаунт покупателя, OTP, Telegram и UI слияния — следующие волны.

---

## 5. Состав волны 1 и порядок

```
W1-0  Контракт и фикстуры (К6) + решения владельца (§7)                      интерактивно   M
W1-A  Каркас шлюза (К1–К5) + агрегат заказа (A1 из волны A) + покупатель (C6) AutoForge*     L
W1-B  Команды §4.2 + форматы §4.3 под golden-тестами                        AutoForge*     L
W1-M  MACS-исправления М1–М5                                                интерактивно   S
W1-S  Staging Lampyris (staging.lampyrisevents.com) → arena-стенд:
      импорт событий/схемы Palac Akropolis, тестовые покупки GA и с местами,
      возврат, PDF, MACS-импорт и вебхук                                    интерактивно   M
W1-P  Прод Lampyris: переключение опции, окно отката = вернуть URL          интерактивно   S
W1-V  Vino&Co: ILS, иврит в description, проверка GSheets и Allpay-цепочки
      на staging.vinoandco.events, затем прод                                интерактивно   M
W1-C  Ключ канала + вкладка «Мероприятия» (без мест) + импорт из Bil24
      для мероприятий с местами с сохранением ID (C1–C3)                    AutoForge* + WP M+M+M
---- волна 1.1, после прода обоих сайтов ----
W1.1  Модуль разметки Р2 + заведение мероприятий с местами с сайта (C4)    AutoForge*     M–L
```

`*` — при условиях из §6. W1-C нужен до переключения прода Lampyris: после переключения организатор заводит
мероприятия без мест в админке сайта, а с местами — в Bil24 с импортом в arena; продажа и учёт мест в обоих
случаях идут через arena. Модуль разметки и заведение мероприятий с местами с сайта — волна 1.1, стартует,
когда продажа и заведение работают на проде; это UI-работа, к которой промпт AutoForge подходит лучше всего.
Суммарно волна 1 — L–XL: порядка полутора–двух месяцев календарного времени при темпе волны 4, с продажей на
Lampyris как первым видимым результатом; волна 1.1 добавляет ещё M–L.

**Стенды (решение владельца 2026-09-04).** Вся отработка идёт на staging-сайтах, прод не трогаем до приёмки:

| Контур | Что | Примечание |
|---|---|---|
| WP staging | `staging.lampyrisevents.com` (`LAMPYRIS_ENV=staging`) и `staging.vinoandco.events` (`VINO_ENV=staging`, живёт на хосте Lampyris) | у обоих есть staging-lock: письма блокируются, исходящие запросы на прод-домены запрещены; Bil24, Brevo и MACS остаются доступными |
| arena staging | стенд Dokploy (сейчас head `4d2492f`, миграции 89) | отдельные организации `lampyris-staging` и `vino-staging`, свои каналы, `gateway_token_hash`, ключи канала |
| Bil24 для тестовых мероприятий с местами | **отдельный организатор, агент и фронтенд (свои `fid`/`token`)** — создаёт владелец | прод-аккаунты Bil24 (агенты 682/675, фронтенды 2580/2570 у Vino; Lampyris — свои) не используются; тестовые сеансы и схемы живут только под новым агентом |
| MACS | прод `macs.arenasoldout.com` — единственный экземпляр | **предостережение:** staging-локи сайтов пропускают запросы в MACS, и `lampyris-ops` при тестовом возврате отправит `ticket.refunded` в прод-MACS. На staging задать `LOPS_MACS_WEBHOOK_URL` на стаб-приёмник arena (`macs/stub`) или на отдельное тестовое событие в MACS под системой `arena-staging`; то же для MACS-подписчика staging-организаций arena |
| Stripe / Allpay | тестовые ключи Stripe на staging Lampyris есть; у Vino отдельного sandbox Allpay нет — «тест» = подмена кред в ночное окно (как в текущем rollout-плане) | для проверки `PAY_ORDER` достаточно Stripe test-mode на Lampyris; цепочку Allpay прогоняем на staging Vino с тестовыми кредами Allpay в согласованное окно |

Порядок на стендах: W1-S (Lampyris staging ↔ arena staging) → приёмка владельцем → W1-P (прод Lampyris) → W1-V
(Vino staging, затем прод). Переключение прода — только сменой опции `bil24_acf_sync`; откат — вернуть URL.

**Почему Lampyris первым.** Stripe уже в WooCommerce, arena-работы под платежи нет; код новее (кэш SVG 90 с,
`showLeft`, флаги `fullNameRequired`); есть staging-процесс и тег отката `pre-bil24-wc`; в arena уже лежит
фикстура Palac Akropolis. Vino вторым: там Allpay-цепочка тоже не касается arena, но есть GSheets, иврит и
живые продажи на 10 площадках.

**Что из волны A предыдущего отчёта входит сюда, что откладывается.** Входит: A1 (агрегат заказа, buyer-поля,
номер заказа, поиск), часть A2 (org-scoping шлюза и каналов). Откладывается до маркетплейса: A3 (платёжные
адаптеры в рантайме, абсолютный redirect), A4 (org_settings), A5 (`app/ordering`), волна B (organizer shell).
Организаторский shell при варианте A не критичен: оператор продолжает работать в консоли `lampyris-ops`.

---

## 6. AutoForge: где уместен, где нет

**Отдать AutoForge** (W1-A, W1-B, C1; в волне 1.1 — C4): команды с точной wire-спецификацией и golden-тестами;
PHP сайтов — исполняемая спецификация, реальные тела — в `docs/samples`. Модуль разметки C4 — отдельный бэклог
в формате `widget_backlog.md` с Playwright-сценариями. Условия, без которых не запускать:

1. Фикстуры и контрактный харнесс (К6) написаны интерактивно **до** первой фичи; каждая фича формулируется как
   «golden-тест X зелёный», а не «реализовать команду».
2. В `description` каждой фичи повторён полный набор ворот из `WAVE4_RUNBOOK.md` и требование пуша — `coding_prompt.md`
   этого не делает, и именно так волна 4 потеряла проходы 2, 4, 6, 7.
3. Нумерация с id 450 / priority 1007; бэклог-файл `09_autoforge/wp_bil24_compat_backlog.md` в принятом формате
   (Category / Problem с file:line / Steps / Done when).
4. Ревизия по протоколу из памяти: сверка диффов на фабрикацию, `gh run view` на коммит волны, полный `go test ./...`
   без пайплайна.

**Только интерактивно:** снятие фикстур, миграция данных (стоп-условие гардрейлов: legacy Vino&Co data), любые
изменения на сайтах и в их опциях, переключения стендов и продов, MACS (чужой прод без staging), решения §7.

**Экономика.** Волна 4 — 9 проходов вместо 6, из них четыре не запушены или провалены. Для контрактной волны с
golden-тестами ожидаю меньше дрейфа, потому что «зелёный тест = точное совпадение байт», но подготовка К6 — это
реальные дни интерактивной работы, которые нельзя экономить.

---

## 7. Решения владельца (2026-09-04) и что осталось открытым

**Принято.**

1. Для двух собственных сайтов compat-шлюз — основной путь; ADR-011/012/013 к ним не применяются. Нативный
   плагин — этап 2.
2. Легаси-виджет Bil24 уже выведен из эксплуатации; выбор билетов и оплата — на сайте. Не эмулируем.
3. Штрих-коды — EAN-13, как сегодня из Bil24; сайты уже печатают билеты с ними (Ф2).
4. История заказов остаётся на сайтах в WooCommerce; в arena не мигрируется.
5. Возврат билета с сайта уже реализован в ивент-центре и сохраняется; arena добавляет `REFUND_TICKET`, чтобы
   ручной шаг «отменить в менеджере Bil24» стал вызовом (§4.2).
6. Заведение мероприятий — развилка волны 1 (§4.5): без мест — сразу из админки сайта в arena; с местами —
   в Bil24 с разметкой, сайт импортирует, то же мероприятие синхронизируется в arena с сохранением ID Bil24,
   дальше учёт мест ведёт arena. Свой модуль разметки и заведение мероприятий с местами с сайта — волна 1.1,
   после того как всё заработает.
7. MACS: безопасность и модернизация — отдельно и позже; в волне 1 только исправления на стороне arena (§4.4).
   Реальный вебхук `order.paid` от Bil24 не нужен: после отключения от Bil24 arena сама единственный источник.
8. Налоги остаются в WooCommerce, как реализовано на сайтах; arena их не моделирует.
9. Вся отработка — на staging-сайтах обоих клиентов и arena-стенде; для тестовых мероприятий с местами в
   Bil24 владелец создаёт отдельного организатора, агента и фронтенд, прод-аккаунты Bil24 не трогаем (§5).
10. Транспорт ветки «с местами» — сайт-ретранслятор: arena подвязана только к сайту, с Bil24 не общается.
    Правило целочисленных ID — по §4.5 (ID Bil24 сохраняются, свои — из диапазона от миллиарда).
11. Покупатель — один на платформу, согласия по организациям, покупатель может быть привязан к нескольким
    организациям; супероператор загружает базы покупателей с разметкой по организаторам и метаданными из
    эпохи Bil24 (§4.6, C6–C7).

**Открыто.**

1. Модуль разметки (волна 1.1): как встраиваемый компонент arena (рекомендация, §4.5) или как экран admin-web
   со ссылкой с сайта. Первое даёт один код для WP, admin-web и маркетплейса. Решение нужно к старту волны 1.1.
2. Промокоды: становятся нативными скидками arena; `discountReason` «Промокод <code>» сохраняем ради GSheets.
3. Проверить, читает ли приложение MACS 1D-штрихкод в режиме `ScanMode.QR`; если нет, на PDF печатать QR с тем
   же числом EAN-13 рядом с полосой.

---

## 8. Связь с этапами дальше

- **Этап «маркетплейс arenasoldout.com»** переиспользует всё из волны 1, кроме одного: arena должна принимать
  деньги сама. Это A3 из волны A (Stripe/Allpay в рантайме, hosted checkout с абсолютным redirect, refund через
  провайдера) плюс витрина (волна E из `arena_architecture_fit`). Организаторы без сайта заводят события в
  admin-web — тут понадобится organizer shell (волна B).
- **Этап «чужие сайты, не WordPress»** — нативный виджет arena + переписанный плагин по `02_wordpress…` для тех,
  у кого WordPress, и REST/webhook-контракт для остальных. Compat-шлюз при этом остаётся как адаптер для
  партнёров на Bil24-протоколе.
