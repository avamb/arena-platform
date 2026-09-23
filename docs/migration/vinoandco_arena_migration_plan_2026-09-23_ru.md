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

## 5. Открытые вопросы к владельцу
1. Разрешение создать `vinoandco-staging` на боевой Arena.
2. Тестовый режим AllPay на staging: есть ли, кто платит.
3. Сканирует ли Vino через MACS (`macs.arenasoldout.com`).
4. 7040580 и 7040584 — два сеанса или дубль.
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
