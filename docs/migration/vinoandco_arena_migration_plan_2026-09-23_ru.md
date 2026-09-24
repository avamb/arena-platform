# Vino&Co → Arena: план переноса

Дата: 23.09.2026. Статус: **план, подготовка не начата; продакшн Vino не трогаем до команды владельца.**
Жёсткий срок: **Bil24 отключается 28.09.2026** (сервер 116.202.82.211; `api.bil24.pro` и `api.tixgear.com` — он же). Отката после отключения нет.

Решения владельца 23.09:
- Делаем **сразу оба направления**: переключение на Arena **и** доведение кода Vino до уровня Lampyris (подпись вебхуков, возвраты через Arena, ивент-центр Arena/промокоды, почтовик с переводами, окно оплаты под AllPay). Переключение прода — когда staging пройдёт сценарии.
- **Покупатели** (история Bil24) переносятся позже, **одним заходом вместе с Lampyris**, после доработки загрузки файла в Arena (см. §7).

Образец: перевод Lampyris 23.09 — `memory/lampyris-prod-cutover-2026-09-23.md`, план `docs/migration/bil24_shutdown_cutover_plan_2026-09-18_ru.md`.

## 1. Инфраструктура (проверено 23.09, только чтение)

| | Прод vinoandco.events | Staging staging.vinoandco.events |
|---|---|---|
| Хост | `lead-parser` (78.46.176.249), Dokploy | `lampyrisevents` (167.233.208.166), docker compose вне Dokploy |
| Каталог | `/etc/dokploy/compose/asoconnector-vinoandcotempvino-fsppdj/code` | `/opt/vinoandco-staging/code` |
| WP-контейнер | `asoconnector-vinoandcotempvino-fsppdj-wordpress-1` | `vinoandco-staging-wordpress-1` |
| БД | `…-wp_db-1`, mysql 8.4, префикс `x4gd_` | `vinoandco-staging-wp_db-1`, mysql 8.4, префикс `x4gd_` |
| Ветка / HEAD | `prod` f2ad9f9 (11.09) | `master` 3e237b6 (10.09) |
| `bil24_acf_sync` | fid **2580**, api.bil24.pro, cron 5 мин | fid **2623**, api.bil24.pro, cron 5 мин |
| `bil24_webhook_signing_secret`, `lops_arena` | нет | нет |
| Валюта / языки | ILS / en, he, ru (WPML) | то же |
| Оплата | только `allpay-payment-gateway` | то же |
| Системный cron wp-cron | есть (на lead-parser) | есть (на lampyrisevents) |

Репозиторий: `C:\Projects\vinoandco-prod-rebuild\repo\vinoandco` (CLAUDE.md: `dev` → `master` = staging → `prod` = прод, Dokploy через git push + merge). `start.sh` синхронизирует в том только `bil24-acf-sync, vino-purchase-flow-ux, wc-orders-columns-email-products, wc-orders-export-filtered-csv, lampyris-ops, bil24-ticket-mailer`; AllPay и mailin — вендорные, из образа не обновляются.

Прод, последние 30 дней: 267 заказов (completed 163, cancelled 101, refunded 3); AllPay 262, приглашения `lops_invite` 5. Нагрузка выше, чем у Lampyris, — окно переключения выбирать в тихое время по Израилю.

Предстоящие сеансы прода (7):

| Дата | Мероприятие | actionEventId |
|---|---|---|
| 01.10 | Винный квиз на крыше | 7040585 |
| 08.10 | Премьера вина в Хайфе | 7040589 |
| 17.10 | Эногастрономический ужин «Римская трапеза» | 7040556 |
| 29.10 | Море, опера, вино и джаз | 7040580 |
| 29.10 | Море, опера, вино и джаз (второй сеанс или дубль — проверить) | 7040584 |
| 21.11 | Премьера вина в Тель-Авиве | 7040596 |
| 26.11 | Премьера вина — впервые в Беэр-Шеве | 7040597 |

## 2. Совместимость с Arena

Все команды, которые шлёт код Vino, Arena поддерживает (`hbil24/bil24_compat.go`): GET_ALL_ACTIONS, GET_SEAT_LIST, CREATE_USER, RESERVATION, CANCEL_RESERVATION, GET_CART, GET_ORDER_INFO, CREATE_ORDER_EXT, PAY_ORDER, ADD_PROMO_CODES, CHECK_KDP, GET_TICKETS_BY_ORDER, SEND_TICKETS_TO_EMAIL. Значит, базовая продажа должна работать сменой настроек — **проверить на staging** (формат уведомления order.paid для старого приёмника Vino не проверялся).

## 3. Пробелы кода Vino (старая версия того же набора плагинов)

| # | Что | Где | Решение |
|---|---|---|---|
| 1 | Приёмник уведомлений не проверяет подпись `X-Arena-Signature`, опции `bil24_webhook_signing_secret` нет | `wp-content/mu-plugins/bil24-notification-receiver.php` | перенести версию Lampyris |
| 2 | Нет REFUND_TICKET: возврат только деньгами в AllPay, билет и место в Arena остаются | `lampyris-ops/includes/class-lops-refunds.php` (копия Vino) | перенести возвраты Lampyris |
| 3 | Нет окна оплаты; AllPay **списывает сразу**, авторизации нет (`allpay-payment-gateway/classes/class-allpay.php:164`) | — | **новая схема**: списание → PAY_ORDER → при отказе автоматический возврат через уже существующий `allpay-refunds.php` (`process_refund`, api12). Плюс политика Lampyris: отказ карты не снимает бронь до конца окна |
| 4 | В ивент-центре нет вкладок Arena и «Промокоды», нет промокодов в пикере, нет туров/мастера | `lampyris-ops` (нет `class-lops-arena.php`, `class-lops-promo.php`, `class-lops-promoters.php`, `class-lops-refund-email.php`, `class-lops-event-wizard.php`, `class-lops-i18n.php`, `class-lops-letter.php`, `class-lops-tours.php`) | перенести; туры/мастер — по желанию владельца |
| 5 | Почтовик билетов без переводов и без «одного письма со сводкой» | `bil24-ticket-mailer` (нет `class-btm-i18n.php`, `class-btm-wc-emails.php`) | перенести; **проверить иврит/RTL в письме и PDF** |
| 6 | Язык покупателя не передаётся в шлюз | `mu-plugins/bil24-cart-bridge.php` (Lampyris: `'locale' => $lang`) | перенести |
| 7 | Правки Lampyris 23.09, которых нет нигде у Vino: снятие брони целиком (UN_RESERVE_ALL для мест), `promoCodeList` в CREATE_ORDER_EXT, одно письмо о возврате, история возвратов в карточке, «вебхук с опозданием» | Lampyris коммиты 401aeef, b266afb, 6cd8038, 51368ac, d325686 | перенести вместе с плагинами |

Подход: **переносить** из Lampyris (`C:\Projects\lampyris-stage-wt`, ветка `staging/arena-on-main`) приёмник, `lampyris-ops`, `bil24-ticket-mailer`; **править на месте** у Vino `bil24-cart-bridge.php`, AllPay и его возвраты — модель оплаты другая. Vino-специфику (иврит, ILS, AllPay, приглашения, XLSX-сводки) не терять: перед переносом каждого файла — diff с версией Vino.

## 4. Шаги (всё на staging; прод Vino — только по команде)

1. **Страховка данных.** Сырая выгрузка Bil24 Vino: прод fid 2580 (7 сеансов, места, SVG, афиши) и staging fid 2623 — `ops/bil24-export/export.mjs` с `BIL24_ENV_FILE`. Креды кладёт владелец: `C:\Users\andre\arena-backups\bil24-vino-prod.env`, `…\bil24-vino-staging.env` (формат `BIL24_FID=` / `BIL24_TOKEN=`; при необходимости `BIL24_URL=https://api.bil24.pro/json`).
2. **Arena для staging.** На боевой Arena (как было с `lampyris-staging`): организация `vinoandco-staging` (country IL, locale he или ru — уточнить), канал `staging.vinoandco.events`, выпуск fid/токена шлюза, WordPress webhook `https://staging.vinoandco.events/wp-json/bil24/v1/notification`, MACS-подписчик (если Vino сканирует через MACS), API-ключ с каналом и всеми правами, включая `promo.*`. Секреты вписывает владелец (см. §6).
3. **Импорт тестовых сеансов staging** в `vinoandco-staging`: `to-bundle.mjs <dump> --publish --venue-offset 900000000`, отправка из вкладки админки (JWT суперадмина, см. §6). Смещение площадок обязательно — иначе прод-импорт упрётся в `import.venue_owned_by_other_org` (так вышло с Lucerna у Lampyris).
4. **Переключение staging-сайта**: `bil24_acf_sync` → `https://api.arenasoldout.com/compat/bil24/json` + fid/токен канала; синк каталога; сверка «товаров столько же, дублей нет».
5. **Сценарии на staging** (AllPay в тестовом режиме):
   - GA и место, иврит и русский;
   - уведомление order.paid дошло, письмо с PDF, цена;
   - отказ карты → бронь держится → оплата второй картой;
   - истечение окна → отмена и снятие брони;
   - возврат билета и заказа целиком → REFUND_TICKET → место вернулось → одно письмо;
   - промокод;
   - скан в MACS.

   Сначала на **текущем** коде Vino (что работает «как есть»), затем на перенесённом.
6. **Код** в ветке `dev` Vino: пункты §3 с тестами (харнесс `php wp-content/plugins/lampyris-ops/tests/run.php`, `mu-plugins/tests/*`), затем `master` → staging, повтор шага 5.
7. **Сценарий переключения прода** (по образцу Lampyris, выполняется только по команде):
   - бэкапы БД Arena и сайта;
   - организация `vinoandco` и её канал;
   - свежая выгрузка и импорт 7 сеансов **без** смещения площадок;
   - владелец закрывает продажи в Bil24 (включая другие фронтенды Bil24);
   - Deploy ветки `prod` с кодом этапа 2;
   - смена настроек, синк, контрольная покупка;
   - открыть продажи.

   Срок — до 28.09 с запасом (цель 26–27.09).

## 4а. Выполнено 23.09

- **Шаг 1 (страховка) сделан.** Креды взяты из `bil24_acf_sync` сайтов в памяти, с разрешения владельца, на диск не писались (fid 2580, отпечаток токена sha256 `9d298f8a60c6`; fid 2623 — `479bc7380dbc`). Выгрузки:
  - прод: `C:\Users\andre\arena-backups\bil24-export-2580-2026-09-23T15-04-47` — 6 мероприятий/6 сеансов, 54 файла, 0 ошибок + вручную дописан `sessions/7040580/` (GET_SEAT_LIST, GET_SCHEMA): распроданный сеанс Bil24 в каталоге не отдаёт;
  - staging: `…\bil24-export-2623-2026-09-23T15-06-01` — 2 тестовых сеанса (7040592, 7040593, сентябрь 2027), 26 файлов, 0 ошибок.
  - Иврита у Bil24 нет (`he-IL` отдаёт то же, что `en-GB`); сеансы 7040585 — с местами (схема зала), остальные GA.

- **Шаг 2 (Arena для staging) — сделан без секретов.** Прод-Arena, от имени суперадмина из вкладки админки:
  - org `vinoandco-staging` «Vino&Co (staging)» `01a0ced8-7ba6-767e-9e27-976ceb0c2862` (номер 8), country IL, locale en;
  - канал «Vino&Co WP staging» `01a0ced9-4e04-7bc1-a78c-f88eeec51433`, provider `allpay`/direct_merchant (деньги берёт сайт), шлюз включён, **fid 8**, токен ещё не выпущен;
  - MACS-подписчик `https://macs.arenasoldout.com/api/_wh/tickets` без подписи (как у lampyris-staging).
  - Осталось владельцу: выпустить токен шлюза, зарегистрировать WordPress webhook, выпустить API-ключ (с каналом и `promo.*`) — последний нужен только после переноса ивент-центра.
- **Шаг 3 (импорт staging) — сделан.** `to-bundle.mjs --venue-offset 900000000 --tz Asia/Jerusalem`, импорт от суперадмина (publication=null — товары на сайте уже есть): 7040592 «Test event Ashdod» (GA, 4 категории 10/149/149/150, venue 900009585) и 7040593 «Test event Escape» (места + GA, 230 мест, 4 проданы в Bil24 → sold upstream, venue 900010559). Названия короткие, PATCH не нужен.
- `to-bundle.mjs` доработан: сеанс из `sessions/<id>/` без записи в каталоге собирается по `sessions/<id>/actionEvent.json` (сделан для 7040580 из метаданных товара 2892); если у такого GA-сеанса остаток 0, объявляется 1 место и в сводке пишется «закрыть категорию после импорта» — Arena отклоняет новый GA-сеанс с нулевой суммарной квотой (`import_exec.go`), а категорию с 0 создаёт закрытой с 1 местом (`import_categories.go`).

- **Шаг 4 (переключение staging-сайта) — сделан 23.09 ~18:15.** Владелец выпустил токен шлюза (rotated_at 16:05Z), вписал на сайте fid 8 + токен (64 симв.) + Base URL (prod) `https://api.arenasoldout.com/compat/bil24/json`; Environment остаётся `prod` (`test` = песочница Bil24 :1240). WordPress webhook зарегистрирован на `https://staging.vinoandco.events/wp-json/bil24/v1/notification` (приёмник жив, только POST). Бэкап до переключения: `lampyrisevents:/root/vino-staging-cutover-20260923/wp_staging_before.sql.gz`. Проверка GET_ALL_ACTIONS кредами сайта → resultCode 0, 2 сеанса. Синк (`do_action('bil24_acf_sync_cron')` через `php -r` в контейнере, wp-cli нет): товаров 84 → 84 (66 publish / 18 draft), вариаций 44 → 44, по одному товару на 7040592/7040593, дублей нет; venue_id товаров стали смещёнными (9000xxxxx), category_price_id вариаций совпадают с Arena.
- AllPay: в плагине нет переключателя test/sandbox — тестовый режим задаётся на стороне AllPay (тестовый терминал/ключи). Какие ключи стоят на staging — уточнить у владельца до сценариев с оплатой.

- **Шаг 5, текущий код Vino — сценарий «GA, русский» ПРОЙДЕН 23.09 18:24–18:30.** WC 3202 = Arena 1000154726, «Test event Ashdod» / «Старт» 99 ₪, AllPay тестовый режим (карта вводилась владельцем — агент номера карт не вводит; тестовые карты: Visa 4557430402053431, отказ 4000000000000002). Цепочка: RESERVATION (бронь 19 мин) → CREATE_ORDER_EXT (charge 0) → оплата AllPay → PAY_ORDER resultCode 0 → вебхук order.paid дошёл (старый приёмник без подписи) → заказ completed → BTM-письмо с PDF через Brevo на andreev@abhteam.com. Билет № 67, EAN-13 `2108595670283` (выпущен Arena, совпадает в PDF). Замечание к переносу почтовика: в PDF строка «Заказ 3202 · Bil24 1000154726» — в версии Lampyris подпись должна быть без «Bil24».

- **Сценарий «отказ карты → вторая карта», места — ПРОЙДЕН.** WC 3207 = Arena 1000155528, «Test event Escape», стол 11 (First, 100 ₪). Отказ `4000000000000002` в 18:33:29 → на сайте только заметка «Оплата не прошла», статус остаётся pending (AllPay даёт повторить на своей странице), `maybe_release` не срабатывает → бронь цела → MasterCard в 18:34:51 → PAY_ORDER 0 → билет № 68 «Стол 11, место 1». В отличие от Lampyris/Stripe, исправление «бронь до конца окна» здесь не требуется — при переносе не сломать.
  - Билет стола печатает «место 1»: у Vino есть флажок товара «Стол целиком» (`vino_hide_seat_number`, `mu-plugins/vino-ops-bridge.php`), на проде стоит на квизе 7040585 (товар 3027), на staging — на старом 3151, не на 3175. **В чек-лист переключения прода: после импорта поставить флажок на товары, продающиеся столами.**
  - «Bil24» в подвале билета → «Arena»: коммит Vino `d4fc936` на `dev` (не запушен), как у Lampyris.
- **Сценарий «истечение окна» — Arena ПРОЙДЕН, сайт — ПРОБЕЛ.** WC 3210 = Arena 1000156444 (2 × «Для друзей», 298 ₪), не оплачен. Arena перевела заказ в `expired` в 17:02:45Z (через 4 с после `expires_at`), 2 места вернулись (остаток 455 → 457). Сайт об этом не узнал: заказ WC остался `pending` без заметок, страница AllPay оставалась открытой с кнопкой «Оплатить». **Если покупатель заплатит после окна, AllPay спишет деньги (списание сразу), PAY_ORDER ответит 101 `bil24.order_expired`, билета не будет** — ровно пробел §3 п.3; нужен автовозврат через `allpay-refunds.php` и окно оплаты на стороне сайта. **Проверено 19:20 (оплата после окна на текущем коде):** AllPay списал 298 ₪, заказ WC → `processing`, PAY_ORDER → 101 «payment time for this order has expired», `bil24_ext_status=pay_failed`, в заметках дважды «Bil24 PAY_ORDER failed»; билета нет, возврата нет, покупателю ушло письмо «Новый заказ»; в Arena заказ остался `expired`, мест не занял. То есть на проде Vino после переключения такой покупатель платит и остаётся без билета, а оператор видит только заметку. Требование к коду этапа 2: при 101 после списания — автоматический `process_refund` AllPay + отмена заказа WC + письмо покупателю; и не пускать на оплату после окна (таймер/перепроверка перед редиректом на AllPay).
- **AllPay умеет то же, что Stripe (документация https://www.allpay.co.il/en/api-reference, проверено 23.09):**
  - `expire` (Unix timestamp) в запросе оплаты `show=getpayment&mode=api11` — после этого момента ссылка на оплату недействительна (по умолчанию неделя). Ставить = `orders.expires_at` Arena (paymentDeadline из CREATE_ORDER_EXT) → оплата после окна становится невозможной, как `expires_at` Checkout Session у Stripe.
  - `preauthorize: true` — J5, холдирование до 168 ч без списания; списание `show=runauthorizedpayment&mode=api11` (order_id, amount), отмена — `show=refund` без `amount`, неиспользованный холд сам снимается через 7 дней. Схема «холд → PAY_ORDER → списание, при 101 — отмена холда» убирает «деньги без билета» полностью, без возвратов. **Уточнить у AllPay/банка, что J5 включён для терминала Vino** (в Израиле J5 разрешается эквайером отдельно).
  - `show=paymentstatus&mode=api11` (статус 0/1/3/4) — уже использует `mu-plugins/allpay-status-sync.php`; возвраты — `mu-plugins/allpay-refunds.php` (`show=refund&mode=api12`).
  - Точка внедрения: вендорный `allpay-payment-gateway` не обновляется из образа и фильтров для запроса не имеет, но `allpay-refunds.php` уже подменяет класс шлюза через `woocommerce_payment_gateways` — там же переопределить `process_payment` (добавить `expire`, опционально `preauthorize`).
  - Предлагаемый порядок: этап 2 минимум — `expire` + автовозврат при 101 (страховка); J5 — после подтверждения AllPay, отдельным шагом.
- **Реализовано 23.09 (Vino `dev`, не запушено; решение владельца «вариант 1»):**
  - `d4fc936` — подвал билета: «Arena» вместо «Bil24».
  - `4093727` — `mu-plugins/vino-allpay-payment-window.php`: ссылка AllPay с `expire` = `paymentDeadline` Arena, после окна ссылка не создаётся («Время на оплату истекло…»); PAY_ORDER: -1/сбой — повтор до deadline+120 с, отказ по оплаченному заказу → полный возврат через AllPay (`wc_create_refund`, повторы 5 мин/15 мин/1 ч/6 ч до 7 дней) + одно письмо покупателю (ru/he/en; стандартное письмо WC о возврате подавлено); после deadline+120 с неоплаченный заказ сверяется с AllPay `paymentstatus` (потерянный вебхук → обработать оплату) и отменяется (бронь снимается). Точки: `allpay-discount-fix.php` (`vino_allpay_before_payment`, фильтр `vino_allpay_payment_request` до подписи), `Bil24_Orders::maybe_pay_order` шлёт `bil24_pay_order_result` (как у Lampyris). Тест `php wp-content/mu-plugins/tests/test-vino-allpay-payment-window.php` (67), `.gitignore`/`.dockerignore` обновлены.
  - `25999b3` — ивент-центр (`class-lops-ticket-fallback.php`): отказ PAY_ORDER больше не выдаётся за «вебхук не пришёл»; карточка пишет «оплатил после окончания времени на оплату…» + состояние возврата (возвращено / повторяется / вернуть вручную); тексты про вебхук — «Arena». Тесты lampyris-ops 2568/2568.
  - **Не проверено вживую:** принимает ли AllPay `expire` в режиме `getpayment&mode=api8` (так шлёт `allpay-discount-fix.php`; в документации — api11) и не ломает ли он подпись. Проверить на staging ПЕРВОЙ покупкой после деплоя; если AllPay отвергнет — перевести запрос на api11.
  - При переносе `lampyris-ops` из Lampyris не потерять правку `card_hint`/`pay_refused` (у Lampyris её нет).
  - Язык писем: заказы Vino все помечены `wpml_language=ru` (937/937 на проде) — письма и билеты уходят по-русски; язык покупателя — пробел §3 п.6.
- **Staging задеплоен агентом 23.09 ~20:32** (владелец разрешил деплой staging): локально `master` перемотан на `dev` (`25999b3`), бандл → `lampyrisevents:/opt/vinoandco-staging/vino.bundle` (старый — `vino.bundle.20260910.bak`), в `code` `git fetch origin && git merge --ff-only origin/master`, тег отката `staging-before-20260923` (= 3e237b6), `docker compose -f docker-compose.yml -f ../docker-compose.override.yml build wordpress && … up -d wordpress` (перед этим `export $(grep -v "^#" .env | xargs)` в `code`). Проверено: модуль окна загружен, `/wp-content/mu-plugins/tests/…` → 404, ошибок PHP нет.
  - **`expire` в `getpayment&mode=api8` AllPay принимает** — заказ 3258 (Arena 1000160795) открыл страницу оплаты; `bil24_payment_deadline` = 18:54:16Z, задача `vino_apw_deadline` на 18:57:16Z. Отмена по окну и отказ AllPay принять оплату после `expire` — проверяются.
  - Возврат 3229 (−100 ₪) сделан по заказу 3207, а не 3210: 298 ₪ по 3210 всё ещё списаны.
  - Там же задеплоены `a7b49ef` (поиск ивент-центра понимает «#3210»/«№3210»), `935d126` + `74bf941` (кнопка «Вернуть N ILS покупателю» в карточке заказа с отказом PAY_ORDER: тот же полный возврат через AllPay + письмо, заметка «Возврат запущен из ивент-центра: <логин>», права `lops_manage_sales` + nonce, AJAX `vino_apw_refund`; кнопку рисует mu-plugin через фильтр `lops_pay_refused_actions` — при переносе lampyris-ops из Lampyris сохранить этот фильтр). Staging HEAD = `74bf941`.
- Staging-код отстаёт от прода: staging `3e237b6` (10.09), прод и `dev` — `f2ad9f9` (11.09). Перед сценариями на перенесённом коде обновить staging целиком (git-бандл `/opt/vinoandco-staging/vino.bundle`, `restore-app.sh`). Агенту запрещена прямая запись в контейнер/сервер staging (классификатор «Remote Shell Writes») — деплой staging согласовать с владельцем.

## 5. Ответы владельца 23.09

1. `vinoandco-staging` на боевой Arena — **да**.
2. AllPay: тестовый режим на staging есть; тестовые покупки — владелец или агент проходит все сценарии сам.
3. MACS — да (см. ниже, подтверждено кодом).
4. 7040580 и 7040584 — **оба актуальны** (две ценовые волны одного вечера; по первой продажи закрыты, по второй идут); на входе сканирование объединяют в один сеанс. **Переносить оба.**
5. Язык организации — **en**.
6. Туры не нужны; **мастер мероприятий и ивент-центр — перенести целиком как у Lampyris**, со всеми новыми функциями.

Как переносим 7040580: Bil24 его в каталоге не отдаёт, поэтому `to-bundle.mjs` его не видит. Места сняты отдельно (100 мест категории 93924605 «Входной билет» 99 ₪, все недоступны). Доработать `to-bundle.mjs`: сеанс из `sessions/` без записи в каталоге собирать по карточке мероприятия 267451 + атрибутам сеанса из метаданных товара WP 2892 (29.10 20:00, Ashdod Yam 9585, схема 43168 «Раннее бронирование», продажи до 18:00). GA-остаток 0 → категория с нулевой квотой, сеанс в Arena «распродан», продаж нет; товар 2892 на сайте продолжает узнавать свой actionEventId. Проданные в Bil24 билеты в Arena не появятся (у импорта GA нет билетов) — на входе их сканирует MACS, куда их уже передал Bil24; до 28.09 проверить в MACS, что билеты 7040580 там есть. Объединение двух сеансов при сканировании — на стороне MACS.

## 5а. Исходные вопросы к владельцу (закрыты)
1. Разрешение создать `vinoandco-staging` на боевой Arena.
2. Тестовый режим AllPay на staging: есть ли, кто платит.
3. Сканирует ли Vino через MACS (`macs.arenasoldout.com`). *Проверено 23.09 (чтение): почти наверняка да — `lampyris-ops/includes/class-lops-macs.php` у Vino шлёт `ticket.refunded` на `https://macs.arenasoldout.com/api/_wh/tickets` (значение по умолчанию, опций `%macs%` в БД прода нет, т.е. переопределения нет). Значит, в Arena нужен MACS-подписчик для канала Vino. Подтвердить у владельца.*
4. 7040580 и 7040584 — два сеанса или дубль. *Проверено 23.09 (чтение БД прода): не дубль и не второй сеанс, а две ценовые волны одного вечера — одно мероприятие (action 267451), 29.10 20:00, Ashdod Yam, продажи до 18:00, но разные схемы зала: 7040580 — схема 43168 «Раннее бронирование», «Входной билет — 99 ₪», остаток 0 (распродан); 7040584 — схема 43172 «Early birds», «Лучшая вечеринка — 199 ₪», 124 места (87 в категории). В Arena это два сеанса с разными квотами; при импорте проверить, что проданные в 7040580 не «воскреснут» свободными. Решение владельцу: переносить 7040580 как закрытый сеанс (ради сканирования проданных билетов) или не переносить.*
5. Язык организации в Arena (he/ru/en) — влияет только на тексты Arena, письма шлёт сайт.
6. Нужны ли Vino туры/мастер мероприятий из Lampyris.

## 6. Уроки переключения Lampyris (23.09), которые применить

- **Доступ к API Arena из сессии:**
  - из вкладки админки `app.arenasoldout.com` через `/v1/auth/refresh`: refresh-токен в `sessionStorage['arena.admin.refresh_token']`, новый записывать обратно;
  - все вызовы — в JS вкладки;
  - сохранять токены/ключи в файлы классификатор запрещает;
  - большие пакеты импорта — gzip+base64 кусками по 2 КБ с sha256-проверкой каждого;
  - из вкладки не достучаться до `localhost`.
- **Секреты вписывает владелец.** Сверять их по sha256-отпечатку с обеих сторон. У Lampyris первый секрет вебхука был вставлен не целиком (56 символов вместо 64) → 401 на order.paid. Лечение:
  - Re-register;
  - вставить заново;
  - `update outbox_events set next_attempt_at=now() where id=…` — переотправить событие.
- **Название мероприятия при импорте** берётся из `fullActionName` (с датой) — после импорта вернуть короткое `PATCH …/events/{id} {"name": …}`.
- **Импорт от имени суперадмина** не публикует мероприятие в канал: вебхук `event.created` сайту не уходит. Для уже существующих товаров это правильно — дублей не будет.
- **Повторный импорт source=bil24 не меняет количество GA** — импортировать один раз, после остановки продаж в Bil24.
- **Deploy.** Lampyris деплоит владелец в **своём** Dokploy. Vino-прод — в Dokploy на lead-parser (`app.andreevmaster.com`), staging — docker compose на lampyrisevents.
- **wp-cli.** Рецепт с `WORDPRESS_CONFIG_EXTRA=define("WP_REDIS_HOST","wp_redis")`, если есть object-cache. Для Vino проверить: у Vino отдельного redis нет.
- **Резервная доставка билетов сайта (GET_TICKETS_BY_ORDER)** с Arena пока не работает: нет `statusExtStr` — задача «Add statusExtStr to Arena GET_TICKETS_BY_ORDER». Основной канал — вебхук.

## 7. Покупатели (позже, вместе с Lampyris)

В Arena нельзя загрузить файл импорта покупателей: `mediastore.AllowedOwnerTypes` — только картинки/SVG, у экрана Customer Imports нет кнопки загрузки. Нужна доработка:
- новый тип файла + миграция CHECK;
- приём JSON/CSV;
- кнопка «Загрузить файл» на экране импорта;
- деплой бэкенда и админки.

Источник — `C:\Users\andre\OneDrive\AA_Actual_c\BIL24 Europe\All_orders` (все организаторы; Lampyris уже выделен в `arena-backups/lampyris_bil24_orders_for_customer_import.json`, 775 заказов), выделить Vino по agent/frontend. Импорт с `org_id` организации (single-org), сначала dry-run.

## 8. Дополнения 24.09 к переключению прода

Код (ветка `dev` Vino, на staging): `6988f5a` перенос кода Lampyris (ивент-центр, REFUND_TICKET, почтовик, подпись вебхука), `5b87445` карточка отказа при ручном возврате, `79f7815` неверный промокод не блокирует оплату + иврит в выборе мест, `7408eab` причины отказа промокода по-русски и на иврите. Не перенесены туры, промоутеры и мастер `past-event` (модель сайта Lampyris).

При переключении прода, помимо шагов §4.7:

- вкладка «Arena» ивент-центра: адрес API `https://api.arenasoldout.com`, ID организации `vinoandco`, имя организатора `Vino&Co` (по умолчанию стоит «Lampyris»), API-ключ организации с каналом и правами `promo.*` — вписывает владелец;
- секрет вебхука: канал → WordPress webhook → Re-register, секрет в «Настройки → Arena webhook» — владелец;
- «Стол целиком» — проверить на тех товарах, что продаются столами (на staging флажок по ошибке встал на старый черновик с тем же названием);
- главная (RU, страница 495): сетке событий `82139f0` добавить «Скрыть на планшете» — иначе на планшете видны сетка и карусель сразу; это правка контента Elementor на проде, по команде владельца (на staging сделано);
- очередь «Возвраты» покажет старые заказы Bil24 с деньгами, возвращёнными без отмены билета: закрывать вручную, Arena о них не знает.
- WPML (по команде владельца, на проде так же пусто): товары, вариации, категории и метки товаров — «Переводимые — показывать перевод или оригинал» (`custom_posts_sync_option` product/product_variation и `taxonomies_sync_option` product_cat/product_tag = 2). Без этого страницы событий на иврите и английском пусты: события 2026 года существуют только на русском. На staging включено 24.09, старые значения в опции `vino_backup_icl_sync_20260924`;
- код карточек `42e3086` (вся карточка — ссылка, кнопка «Билеты») уходит с деплоем, правок контента не требует.
