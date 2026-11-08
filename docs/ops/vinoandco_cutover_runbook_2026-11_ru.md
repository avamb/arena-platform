# Переключение vinoandco.events с WordPress на новый сайт: ночь T0 по чек-листу

Спека: `08_architecture/62_vinoandco_site_off_wordpress_ru.md` (§4.1, §14, §15). Этот документ — исполняемая
версия §14: каждая команда и файл названы, ничего не придумывается в ночь переключения. Формат как у плана
переезда Lampyris: сначала **как есть сейчас** (факты, снятые на сервере 07.11.2026 только чтением), потом шаги,
в конце журнал, куда заносится **как получилось**.

Метки: **[ВЛАДЕЛЕЦ]** — только владелец (кнопки Dokploy, Cloudflare, Search Console, решения);
**[ОПЕРАТОР]** — команды (агент или оператор платформы). Команды на сервере выполняются в сессии
`ssh lead-parser`, локальные (с ноутбука, из корня репозитория) — в PowerShell. Секреты, токены и пароли в
этот документ и в репозиторий не записываются: названы только имена переменных.

## 0. Что готово в репозитории

| Файл | Для чего |
|---|---|
| `ops/vino-cutover/traefik-dynamic-vino-site.yml` | Файл маршрутов Traefik (§14.2 п. 4): сайт на `Host(vinoandco.events)`, WP-пути выше. Плейсхолдеры `__HOST__`, `__SITE_URL__`, `__WP_URL__`, `__CERT_RESOLVER__` |
| `ops/vino-cutover/traefik-dynamic-vino-rollback.yml` | Откат маршрута до T2 (§14.5): оставляет сайту только `/order-status/`, `/my-tickets/`, `/_site/` |
| `ops/vino-cutover/traefik-routing-check.sh` | 66 GET-проб по всем правилам: какой бэкенд ответил, SITE или WP |
| `ops/vino-cutover/wp-inventory.php` | Инвентаризация обязательств WP (§14.2 п. 3), только чтение |
| `ops/vino-cutover/wp-legacy-tickets-export.php` | Билеты эпохи Bil24 из меты заказов WP для `cmd/arena-legacy-tickets-import` (§3.5), только чтение |
| `ops/vino-cutover/wp-disable-sales.md` | Как остановить новые продажи на WP и вернуть (§14.2 п. 2) |
| `ops/vino-cutover/cutover-settings.php`, `gw-check-from-wp.mjs`, `run-export-from-wp.mjs`, `prod-facts.sql` | Настройки WP при переносе на Arena, проверка шлюза, выгрузка, факты прода (раньше, при переезде магазина) |

## 1. Как есть сейчас (факты на 07.11.2026)

**Traefik** — `dokploy-traefik`, образ `traefik:v3.1.2`; провайдеры `docker` (сеть `dokploy-network`) и
`file` (`/etc/dokploy/traefik/dynamic`, `watch: true`), синтаксис правил v3 (в `traefik.yml` нет
`defaultRuleSyntax`). Решение по сопоставителям: **`Query(...)` и `QueryRegexp(...)` v3** — правило по строке
запроса работает прямо в Traefik, обходной путь через nginx сайта не нужен. Проверено запуском настоящего
`traefik:v3.1.2` с двумя заглушками-бэкендами: все 66 проб `traefik-routing-check.sh` дали ожидаемый бэкенд
(28 SITE, 38 WP).

**Роутеры WP** (labels Dokploy, контейнер `asoconnector-vinoandcotempvino-*-wordpress-1`, порт 80):
`<appName>-<n>-web` (порт 80, `redirect-to-https@file`) и `<appName>-<n>-websecure` (certresolver `letsencrypt`), оба
`Host(`vinoandco.events`)` **без priority**, то есть приоритет = длина правила, 25. **Роутер стейджинг-сайта**
(`vinoandco-site-staging-*-web-1`): `Host(`new.vinoandco.events`)`, порт 80; контейнер публикует
`127.0.0.1:18090` и стоит в `dokploy-network`. Файл `vino-webhook.yml` (`deploy.andreevmaster.com`) в каталоге
dynamic остаётся как есть, наш файл его не касается.

**Cloudflare перед сайтом**: заголовок прода `cf-edge-cache: cache,platform=wordpress` — HTML WP кешируется на
краю (APO). Без сброса кеша после T0 часть посетителей ещё какое-то время получит старые страницы WP.

**WP**: WooCommerce 11.1.0, префикс таблиц `x4gd_`, Redis-объектный кеш включён, wp-cli в контейнере нет,
системный cron раз в минуту (`docker exec ... php wp-cron.php`), опция `vino_maintenance` не задана.
Старый канал платежей: `notifications_url` AllPay = `/?wc-api=allpay`, возврат с AllPay = `/?vino_pay_back=<id>&key=`.

**Базовая инвентаризация** (`wp-inventory.php --allpay`, 07.11.2026 ~01:10 по Мадриду, окно 7 дней):

| Показатель | Значение |
|---|---|
| Заказы WC `pending` / `on-hold` / `processing` | 0 / 0 / 4 |
| из них `processing` без отправленных билетов | 4 (все старые: 12.2025–04.2026, времён Bil24; один с оплатой при получении) |
| Отказы PAY_ORDER (`vino_apw_refusal`) | 1, уже возвращён автоматически (`refunded`) |
| Очередь возвратов (`refund_pending` / `refund_failed`) | 0 / 0 |
| Action Scheduler: `vino_apw_deadline`, `vino_apw_retry_pay`, `vino_apw_retry_refund`, `btm_process_order`, `lops_*` в состояниях pending / in-progress / failed | 0 |
| Action Scheduler: чужие `failed` (Google Listings, `fetch_patterns`) | 19 и 30, к продажам не относятся |
| Заказы AllPay за 7 дней / оплачено на стороне WC | 53 / 30 |
| оплачено без статуса IPN, без билетов, неоплаченных открытых | 0 / 0 / 0 |
| Сверка с AllPay `paymentstatus` по 30 оплаченным | 30 оплачено, расхождений 0 |

Четыре старых `processing` не относятся к платформе Arena (она учитывает заказы с 25.09). Решение владельца
08.11: это устаревшие заказы, остановку WP они не блокируют (исключение из условия §14.4, см. вопрос 3).

## 2. Готовность (go / no-go, §14.1): отметить до начала

| ☐ | Готовность | Условие |
|---|---|---|
| ☐ | Сайт | §13.2 пп. 1–2, 6, 8, 11–14, 17 пройдены (п. 4 — вторая волна) |
| ☐ | Платформа | П-2…П-11, П-13…П-23 выкачены (П-1 — вторая волна); §13.2 пп. 3, 5, 7, 9, 10, 11a, 11b, 15 пройдены |
| ☐ | Анна | EC-V1…EC-V6 выкачены; §13.2 п. 16 пройден самой Анной |
| ☐ | Репетиция | переключение и откат маршрута отрепетированы на тестовом домене (раздел 4 ниже), `traefik-routing-check.sh` без FAIL |
| ☐ | Подписчики | дозагрузка подписчиков (§10.1) готова |
| ☐ | Реклама | ни одной рекламной кампании в момент переключения не запускается |
| ☐ | Prod-приложение | `vinoandco_site_prod` развёрнуто, сборщик `ok: true` на `/healthz`, страницы отдаются изнутри сети (шаг 3.3) |
| ☐ | Сеть | веб-контейнер prod-сайта стоит в `dokploy-network` (шаг 3.2); ни один контейнер нового стека не получил адрес `172.26.0.3` (правило `DOCKER-USER` в `/etc/iptables/rules.v4` режет этому адресу SMTP и адрес Cloudflare) |
| ☐ | CORS и канал | `https://vinoandco.events` в CORS боевого API, канал сайта готов, feed-токен в окружении prod-compose, `vinoandco.events` в доменах Turnstile |
| ☐ | Cloudflare | токен очистки кеша для зоны (Zone.Cache Purge) положен в окружение сборщика; правила кеша зоны просмотрены, исключений нет |
| ☐ | Базовая инвентаризация | выполнена не раньше чем за сутки, JSON сохранён на сервере (шаг 3.1) |

## 3. Подготовка за дни до T0

### 3.1 Базовая инвентаризация и рабочая папка [ОПЕРАТОР]

```
ssh lead-parser 'mkdir -p /root/vino-cutover && chmod 700 /root/vino-cutover'
scp ops/vino-cutover/wp-inventory.php ops/vino-cutover/traefik-dynamic-vino-site.yml ops/vino-cutover/traefik-dynamic-vino-rollback.yml ops/vino-cutover/traefik-routing-check.sh lead-parser:/root/vino-cutover/
```

На сервере (файлы с номерами заказов лежат **только там**, в репозиторий не попадают):

```
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
docker cp /root/vino-cutover/wp-inventory.php $W:/tmp/wp-inventory.php
docker exec $W php /tmp/wp-inventory.php --allpay > /root/vino-cutover/inventory-baseline.json
docker exec $W rm /tmp/wp-inventory.php
python3 -c "import json;d=json.load(open('/root/vino-cutover/inventory-baseline.json'));print(json.dumps(d['stop_condition_14_4'],indent=1))"
```

Без `--allpay` скрипт не ходит наружу; с ним он делает те же запросы `paymentstatus`, что WP-Cron каждые 15 минут,
по одному на оплаченный или ожидающий заказ окна.

### 3.2 Приложение сайта `vinoandco_site_prod` [ВЛАДЕЛЕЦ нажимает Create / Deploy]

Копия настроек стейджинга (`vinoandco-site-staging`), второй compose в том же Dokploy:

| Настройка | Значение |
|---|---|
| Тип, источник | Docker Compose, репозиторий `avamb/vinoandco-site`, ветка `main`, файл `./docker-compose.yml` (как на стейджинге) |
| Имя | `vinoandco_site_prod` (имя контейнеров Dokploy сгенерирует само: `<appName>-web-1`, `<appName>-builder-1`) |
| **Домен в Dokploy** | технический, **не** `vinoandco.events`: host `vinoandco-site-prod.invalid`, сервис `web`, порт 80, **https выключен**. Маршрут настоящего хоста задаёт динамический файл (раздел 5). Проверено 07.11 на стейджинге по сгенерированному compose: Dokploy добавляет `dokploy-network` только сервису, у которого есть домен (у `builder` её нет), поэтому без домена `web` для Traefik невидим. Технический домен даёт лишь http-роутер `Host(vinoandco-site-prod.invalid)`, который никому не отвечает; ACME для него не запрашивается, потому что https выключен |
| `SITE_URL` | `https://vinoandco.events` |
| `SITE_INDEXING` | `true` (на стейджинге `false`) |
| `PUBLIC_ARENA_API` | `https://api.arenasoldout.com` |
| `PUBLIC_FEED_TOKEN` | feed-токен **боевого** канала сайта (значение из платформы, в репозиторий не пишется) |
| `PUBLIC_META_PIXEL_ID`, `PUBLIC_GA4_ID` | боевые идентификаторы (значения из конфигурации владельца) |
| `PUBLIC_TURNSTILE_SITE_KEY` | боевой ключ сайта Turnstile; на стейджинге в окружении этой переменной нет |
| `COMPOSE_PROFILES` | `builder` (как на стейджинге: иначе сборщик не запустится) |
| `BUILDER_POLL_SECONDS` | как на стейджинге |
| `WEB_PORT` | `127.0.0.1:18091` (на стейджинге `127.0.0.1:18090`; порт занят — compose всегда публикует; свободен 18091) |
| `CF_API_TOKEN`, `CF_ZONE_ID` | токен (Zone.Cache Purge) и id зоны `vinoandco.events`; значения только в окружении сборщика, без них сборщик пропускает очистку кеша |

Проверка сети после Deploy [ОПЕРАТОР]:

```
docker ps --format '{{.Names}}' | grep vinoandco-site-prod
docker inspect <prod web контейнер> --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'
```

В ответе должна быть `dokploy-network`. Если её нет, Traefik не достанет контейнер по имени. Варианты в порядке
предпочтения: (1) добавить в `docker-compose.yml` репозитория сайта внешнюю сеть `dokploy-network` для сервиса
`web` и перевыкатить (правка в репозитории сайта, решается там); (2) временно `docker network connect
dokploy-network <web контейнер>`, но после каждого пересоздания контейнера это нужно повторять, то есть годится
только для репетиции; (3) завести в Dokploy технический домен для prod-compose, но не `vinoandco.events`
(иначе его роутер с приоритетом 25 встанет рядом с роутерами WP).

### 3.3 Проверка prod-сайта до маршрута [ОПЕРАТОР]

Страница изнутри сети Traefik (без маршрута, без Cloudflare):

```
docker exec dokploy-traefik wget -S -q -O /dev/null --header 'Host: vinoandco.events' http://<prod web контейнер>:80/ 2>&1 | head -n 12
docker exec <prod builder контейнер> wget -qO- http://127.0.0.1:8081/healthz
```

Ждём `HTTP/1.1 200 OK` и `"ok": true`, `published_events` как в платформе, `last_error: null`. Заголовок
`x-robots-tag` у prod (`SITE_INDEXING=true`) отсутствует, в `robots.txt` нет `Disallow: /`.

### 3.4 Данные, которые есть только в WordPress: статусы писем и фотоотчёты [ОПЕРАТОР]

Две вещи живут только в WP и до переключения должны оказаться в платформе. Оба экспорта только читают, ничего не пишут.
Файлы с адресами покупателей лежат **только на сервере и ноутбуке оператора**, в репозиторий не попадают; после импорта удалить.

**А. Статусы писем с билетами.** WP слал письма сам, через свой Brevo (плагин `bil24-ticket-mailer`). На заказе WooCommerce
лежат `_btm_sent_at`, `_btm_brevo_message_id`, `lops_email_status` (последний статус), `lops_email_status_times`
(отправлено, доставлено, прочитано) и `lops_email_delivery_failed`. Номер заказа платформы это `bil24_external_order_id`,
то есть `orders.system_id`. Импорт кладёт письма в `email_letters` с `provider = wp_brevo`; в карточке заказа они видны как
обычные письма с билетами.

```
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
docker cp /root/vino-cutover/wp-letters-export.php $W:/tmp/x.php
docker exec $W php /tmp/x.php > /root/vino-cutover/wp-letters.json
docker exec $W rm /tmp/x.php
```

Файл перенести на машину, где есть доступ к базе платформы, и из корня репозитория:

```
DATABASE_URL=$ARENA_DATABASE_URL JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-wp-letters-import --org $ARENA_ORG_ID --file wp-letters.json
DATABASE_URL=$ARENA_DATABASE_URL JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-wp-letters-import --org $ARENA_ORG_ID --file wp-letters.json --apply
```

Первый запуск по умолчанию пробный: печатает `matched`, `unmatched`, `already_present`, `would insert`. Смотреть на `unmatched`
(заказ с номером, которого нет у организации) и `no_order_number` (старые заказы без номера платформы: письма им не нужны).
Запуск безопасно повторять: письмо с уже записанным message id пропускается, а у записанного ранее статус только продвигается
вперёд (отправлено, доставлено, прочитано). Поэтому команду запускают дважды: до переключения и в переходный период, когда
Brevo ещё досылает события «доставлено» и «прочитано» в WP.

**Б. Галерея, видео и фотоотчёт мероприятий.** В Vino&Co мероприятие это товар WooCommerce (`_bil24_action_id` равен
`events.external_bil24_id`); `lampyris-ops` хранит по языкам ru/en/he галерею, видео и отчёт (текст, фото, видео).
Платформа хранит один набор материалов на мероприятие, без языков: переносится набор ru (его же показывают en и he, пока для них
не включён «свой набор»); свой набор en/he и текст отчёта скрипт только перечисляет (поля для текста в платформе нет, его можно
сохранить флагом `--texts-out`). Лимиты платформы: 6 фото галереи, 30 фото отчёта, 4 видео, 3 партнёрские ссылки.

```
docker cp /root/vino-cutover/wp-event-media-export.php $W:/tmp/x.php
docker exec $W php /tmp/x.php > /root/vino-cutover/wp-event-media.json
docker exec $W rm /tmp/x.php
```

Соответствие номера акции мероприятию платформы (в публичном API `external_bil24_id` не отдаётся), из базы платформы:

```
psql "$ARENA_DATABASE_URL" -c "\copy (SELECT external_bil24_id, id FROM events WHERE org_id = '$ARENA_ORG_ID' AND external_bil24_id IS NOT NULL AND deleted_at IS NULL) TO 'events.csv' CSV"
```

Импорт идёт через REST API платформы, с ноутбука; нужен токен владельца или менеджера организации. Без `--apply` это пробный
запуск: он скачивает картинки с сайта WP и читает платформу, но ничего не пишет.

```
export ARENA_API_URL=<адрес arena-api> ARENA_TOKEN=<токен> ARENA_ORG_ID=<uuid организации>
node ops/vino-cutover/wp-event-media-import.mjs --file wp-event-media.json --map events.csv --texts-out report-texts.json
node ops/vino-cutover/wp-event-media-import.mjs --file wp-event-media.json --map events.csv --apply
```

Повтор безопасен: фото сравнивается по SHA-256 с уже лежащими в секции, видео и ссылки по адресу. В отчёте смотреть `UNMAPPED`
(акция без мероприятия в платформе), `over_limit`, `failed` (код выхода 1 при ошибках). Картинки качаются с сайта WP, поэтому
импорт выполнить, пока WP ещё отвечает (до T2).

### 3.5 Билеты эпохи Bil24 на ближайшие мероприятия [ОПЕРАТОР]

Билеты, проданные через Bil24 до 25.09.2026, есть только в мете заказов WooCommerce (`bil24_tickets`, номер заказа
Bil24 из 7 цифр в `bil24_external_order_id`). Чтобы покупатели видели их в «Моих билетах», а они попадали в список гостей
(Excel и автоматическая рассылка за 24 ч и 3 ч) и в проданные места сеанса, их переносят в платформу. Сейчас это две акции:
267465 «Премьера вина в Тель-Авиве» (сеанс 21.11) и 267466 Beer Sheva (сеанс 26.11).

Что делает импорт. Каждый заказ WP становится заказом платформы с `source = legacy_import`, статусом `paid`, датой оплаты
из WP и номером заказа Bil24 как номером заказа платформы. Места не берутся из свободных: в категорию ДОБАВЛЯЮТСЯ проданные
места (как «продано вне системы»), ёмкость категории и сеанса и счётчики `inventory_ledger` растут на то же число. Штрихкод
каждого билета сохраняется как был, в центре штрихкодов `legacy_bil24`. Импорт ничего не отправляет: ни вебхуков, ни MACS
(MACS эти штрихкоды знает с эпохи Bil24), ни писем, ни уведомлений в Telegram, и не создаёт биллинга. Переносятся только
живые билеты (`lops_bil24_live_ids`), только заказы `completed` и `processing`.

Экспорт из WP (только чтение; в файле полные имена, e-mail и телефоны покупателей, держать только на сервере и ноутбуке
оператора, после импорта удалить):

```
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
docker cp /root/vino-cutover/wp-legacy-tickets-export.php $W:/tmp/x.php
docker exec $W php /tmp/x.php --actions=267465,267466 > /root/vino-cutover/legacy-tickets.json
docker exec $W rm /tmp/x.php
```

Импорт, из корня репозитория, на машине с доступом к базе платформы. Первый запуск по умолчанию пробный: ничего не пишет и
по каждому сеансу печатает число заказов, билетов, сумму и места до и после; отдельно все отказы.

```
DATABASE_URL=$ARENA_DATABASE_URL JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-legacy-tickets-import --org-id $ARENA_ORG_ID --file legacy-tickets.json
DATABASE_URL=$ARENA_DATABASE_URL JWT_SIGNING_SECRET=x go run ./apps/backend/cmd/arena-legacy-tickets-import --org-id $ARENA_ORG_ID --file legacy-tickets.json --apply
```

Канал продаж берётся тот, через который шли заказы шлюза этого сеанса (Vino&Co WP); другой задают флагом
`--channel-id $ARENA_CHANNEL_ID`. Прошедшие сеансы пропускаются (`--only-future`, включён по умолчанию). Отказы, которые
нужно разобрать до `--apply`: `barcode_taken` (такой штрихкод уже есть в платформе, дубль не создаётся),
`ambiguous_session` (у мероприятия несколько живых сеансов, а сеанс Bil24 не зарегистрирован), `unknown_action`,
`order_number_taken`, `ticket_number_taken`, `missing_data`, `money_mismatch`. Повтор безопасен: уже перенесённый заказ печатается как `unchanged`,
а `already_imported_differs` значит, что в WP с тех пор вернули билет: такой заказ правят в платформе руками.

После переноса запустить импорт писем из §3.4 ещё раз: письма заказов эпохи Bil24 найдут свой заказ через `wp_order_links`
(в выводе `matched_bil24_era`). Вернуть деньги за такой заказ через платформу нельзя (платёж шёл не через неё, ответ
`refund.nothing_to_refund`); возврат, сделанный в кабинете платёжной системы, отмечают как «возврат вне платформы».

Билет сохраняет свой номер Bil24 (`tickets.system_ticket_id`): под этим номером его знает MACS и с ним присылает сканы
на `/v1/macs/scan-events`. Сами билеты в MACS уже есть со времён Bil24, импорт в MACS ничего не отправляет.

Репетиция на копии боевой базы 08.11.2026 (временный контейнер на arena-prod, удалён после): 58 заказов, 112 билетов
(Тель-Авив 8/16, 4 640 ILS; Беэр-Шева 50/96, 24 000 ILS), отказов 0, свободные места не изменились (317 и 66), повтор —
58 `unchanged`, все 112 номеров найдены в MACS как `origin_id`, письма WP — `matched_bil24_era=58`.

## 4. Репетиция переключения и отката на тестовом домене [ОПЕРАТОР]

Без DNS и без сертификата: выдуманное имя `vino-rehearsal.example.test`, запросы идут с `--resolve` прямо в
Traefik сервера. Боевой `vinoandco.events` репетиция не затрагивает: правила привязаны к другому `Host`.
Для репетиции годится и стейджинг-контейнер: `__SITE_URL__` = `http://$STG:80`.

```
cd /root/vino-cutover
IP=$(hostname -I | cut -d' ' -f1)
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
STG=$(docker ps --format '{{.Names}}' | grep -E '^vinoandco-site-staging-.*-web-1$')
sed -e 's|__HOST__|vino-rehearsal.example.test|' \
    -e "s|__SITE_URL__|http://$STG:80|" \
    -e "s|__WP_URL__|http://$W:80|" \
    -e 's|__CERT_RESOLVER__||' traefik-dynamic-vino-site.yml > /root/vino-cutover/rehearsal.yml
python3 -c "import yaml;yaml.safe_load(open('/root/vino-cutover/rehearsal.yml'));print('yaml ok')"
install -m 0644 rehearsal.yml /etc/dokploy/traefik/dynamic/vino-rehearsal.yml.new
mv /etc/dokploy/traefik/dynamic/vino-rehearsal.yml.new /etc/dokploy/traefik/dynamic/vino-rehearsal.yml
sleep 3; docker logs dokploy-traefik --since 30s 2>&1 | grep -iE 'vino|error' | head
bash traefik-routing-check.sh vino-rehearsal.example.test "$IP" 443 -k
```

Ожидание: `66 probes, 0 failed`. Откат репетиции: `rm /etc/dokploy/traefik/dynamic/vino-rehearsal.yml`, через
секунды те же пробы возвращают 404 от Traefik (имени больше нет ни у одного роутера). Для репетиции откатного
файла: отрендерить `traefik-dynamic-vino-rollback.yml` с теми же подстановками, положить поверх
`vino-rehearsal.yml` и убедиться, что `/order-status/` и `/my-tickets/` отвечает сайт, остальное — нет роутера (404).
Полная репетиция по §14.5 (оплаченный заказ нового канала, незавершённая оплата, «Мои билеты» с очисткой адреса,
переход из письма и подвала на трёх языках) делается на стейджинге по приёмке §13.

## 5. Ночь T0: последовательность

Время относительное. Окно «продажи на WP остановлены» должно быть как можно короче.

### T − 60 мин: последние проверки [ОПЕРАТОР]

- Раздел 2 отмечен, ни одна рекламная кампания не идёт.
- API платформы доступен (`curl -s -o /dev/null -w '%{http_code}' https://api.arenasoldout.com/` отвечает), сборщик prod-сайта: `ok: true`.
- Рендер боевого файла и проверка синтаксиса (файл пока **не** в каталоге dynamic):

```
cd /root/vino-cutover
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
SITE=$(docker ps --format '{{.Names}}' | grep '^vinoandco-site-prod.*-web-1$')
test -n "$SITE" && echo "$SITE"
sed -e 's|__HOST__|vinoandco.events|' \
    -e "s|__SITE_URL__|http://$SITE:80|" \
    -e "s|__WP_URL__|http://$W:80|" \
    -e 's|__CERT_RESOLVER__|letsencrypt|' traefik-dynamic-vino-site.yml > vino-site.rendered.yml
grep -cE '^[^#]*__[A-Z_]+__' vino-site.rendered.yml     # 0: плейсхолдеров вне комментариев не осталось
python3 -c "import yaml;yaml.safe_load(open('vino-site.rendered.yml'));print('yaml ok')"
sed -e 's|__HOST__|vinoandco.events|' -e "s|__SITE_URL__|http://$SITE:80|" -e 's|__CERT_RESOLVER__|letsencrypt|' traefik-dynamic-vino-rollback.yml > vino-rollback.rendered.yml
python3 -c "import yaml;yaml.safe_load(open('vino-rollback.rendered.yml'));print('yaml ok')"
```

### T − 30 мин: остановить НОВЫЕ продажи на WP [ОПЕРАТОР] (`ops/vino-cutover/wp-disable-sales.md`)

```
docker exec $W php -r 'require "/var/www/html/wp-load.php"; update_option("vino_maintenance", 1, false); echo "vino_maintenance=", get_option("vino_maintenance"), "\n";'
curl -s -o /dev/null -D - https://vinoandco.events/ | head -n 8     # ждём 503 и retry-after
```

Сразу сбросить кеш Cloudflare (команда в «T0 + 1 мин», п. 3): иначе краевой кеш ещё отдаёт страницы с рабочей кнопкой.

### T − 25 мин: инвентаризация [ОПЕРАТОР]

```
W=$(docker ps --format '{{.Names}}' | grep -E '^asoconnector-vinoandcotempvino-.*-wordpress-1$')
docker cp /root/vino-cutover/wp-inventory.php $W:/tmp/wp-inventory.php
docker exec $W php /tmp/wp-inventory.php --allpay > /root/vino-cutover/inventory-T0-before.json
docker exec $W rm /tmp/wp-inventory.php
python3 -c "import json;d=json.load(open('/root/vino-cutover/inventory-T0-before.json'));print(json.dumps({k:d[k]['counts'] if 'counts' in d[k] else d[k] for k in ('open_orders','pay_order_refusals','recent_allpay_payments','stop_condition_14_4')},indent=1,default=str))"
```

Сравнить с базовой (раздел 1): новые `pending`/`on-hold`/`processing`, новые отказы, очередь возвратов.
Любой **новый** заказ в `pending` или `processing` без билетов — разобрать до маршрута, а не после.
Список (`inventory-T0-before.json`) — основа сверки §14.3 и §14.4.

### T0: переключение маршрута [ОПЕРАТОР]

```
install -m 0644 /root/vino-cutover/vino-site.rendered.yml /etc/dokploy/traefik/dynamic/vino-site.yml.new
mv /etc/dokploy/traefik/dynamic/vino-site.yml.new /etc/dokploy/traefik/dynamic/vino-site.yml
sleep 3
docker logs dokploy-traefik --since 1m 2>&1 | grep -iE 'vino|error|level=err' | head
ls /etc/dokploy/traefik/dynamic/vino-webhook.yml                      # на месте и не тронут
```

Каталог `dynamic` отслеживается, Traefik подхватывает файл за секунды; передеплоя WP-compose нет, DNS не меняется.
Файл копируется через `.new` и `mv`: частично записанный файл Traefik не прочитает. До появления файла весь хост у WP, после — по правилам файла (приоритеты описаны в его шапке).

### T0 + 1 мин: проверки [ОПЕРАТОР]

1. Маршруты, с сервера мимо Cloudflare (в этом режиме `-k` не нужен: сертификат боевой):

```
IP=$(hostname -I | cut -d' ' -f1)
bash /root/vino-cutover/traefik-routing-check.sh vinoandco.events "$IP"
```

Ждём `66 probes, 0 failed`. Две пробы WP-стороны (`?wc-api=allpay`, `?vino_pay_back=0`) настоящие запросы к WP:
IPN для заказа 0 ничего не меняет, но записывает строку в лог; запускать проверку **один раз**, не в цикле.
Подтверждение, что IPN дошёл до WP:

```
docker exec $W sh -c 'tail -n 3 "$(ls -t /var/www/html/wp-content/uploads/wc-logs/allpay-* | head -1)"'
```

Должна быть свежая строка `[Allpay MU] hit {"order_id":0,...}`.

2. Снаружи, через Cloudflare, после сброса кеша (п. 3 ниже): главная, событие, `/en/`, `/he/`, `/sitemap.xml`,
   `/robots.txt` (без `Disallow: /`), `/wp-login.php` (форма входа WP), `/payment-successful/` → 301 на
   `/order-status/`.

3. Сброс кеша Cloudflare [ОПЕРАТОР или ВЛАДЕЛЕЦ]. Из контейнера сборщика, токен не печатается:

```
docker exec <prod builder контейнер> node -e "fetch('https://api.cloudflare.com/client/v4/zones/'+process.env.CF_ZONE_ID+'/purge_cache',{method:'POST',headers:{Authorization:'Bearer '+process.env.CF_API_TOKEN,'Content-Type':'application/json'},body:JSON.stringify({purge_everything:true})}).then(async r=>{const j=await r.json();console.log(r.status,j.success,JSON.stringify(j.errors))})"
```

Ждём `200 true []`. Без токена в окружении сборщика: **[ВЛАДЕЛЕЦ]** Cloudflare → Caching → Configuration →
Purge Everything. Сделать дважды: на T − 30 (после заглушки) и сейчас.

4. Тестовая покупка в **тестовом канале** на тестовом событии, оплата тестовым способом: письмо с билетом,
   «Мои билеты» по ссылке из письма (`/my-tickets/?t=…`), «Спасибо» (`/order-status/`) на трёх языках, серверная
   конверсия (CAPI/GA4 MP) видна в журнале платформы.

5. **[ВЛАДЕЛЕЦ]** Search Console: отправить новый `https://vinoandco.events/sitemap.xml`, проверить главную
   инструментом проверки URL.

### Заглушка WP остаётся

Заглушка `vino_maintenance` остаётся включённой весь переходный период для тех, кто ещё попадёт на WP
(wp-admin и вебхуки её обходят). Если в 14.5 откат — её снимают (`wp-disable-sales.md`, раздел «Откат»).

## 6. Переходный период (до 7 дней, §14.3) [ОПЕРАТОР]

- Новые продажи только через сайт и новый канал. WP обрабатывает старые обязательства: AllPay IPN, `vino_pay_back`,
  страницы старых заказов с параметрами, Brevo, WP-Cron (сверка AllPay каждые 15 минут, повторы возвратов, письма).
- **Ежедневно** (в то же время): повторить инвентаризацию (шаг 3.1, файл `inventory-day-N.json`) и сравнить с
  `inventory-T0-before.json`: заказы из списка должны либо исчезать из `pending`/`processing`, либо иметь причину.
- Прочие ежедневные проверки: Traefik не пишет ошибок по файлу (`docker logs dokploy-traefik --since 24h | grep -i vino`),
  `GET /healthz` сборщика ok, ops-бот молчит (раздел 9).
- **Тест позднего IPN**: платёжный повтор старого платежа должен обработаться ровно один раз. На проде проверка
  косвенная, по журналу: для любого заказа строка `payment_complete OK for #<id>` в `allpay-*.log`
  встречается не более одного раза (`grep -c`), второе событие того же заказа заканчивается без этой строки.
  Прямой повтор IPN на боевых данных не делается.

## 7. Остановка WP, T2 (§14.4): по сверке, а не по таймеру

Условие, по `inventory-dayN.json` (`stop_condition_14_4` в нём должна быть вся `true`):

| Ключ в JSON | Смысл |
|---|---|
| `no_open_orders` | в WP нет заказов `pending`/`on-hold`/`processing` |
| `no_processing_without_tickets` | нет `processing` без билетов |
| `refund_queue_empty` | очередь повторов возврата (`vino_apw_retry_refund`, `refund_pending`, `refund_failed`) пуста |
| `no_pending_apw_or_ticket_actions` | задачи Action Scheduler выполнены или сняты |
| `payments_confirmed_by_allpay` | все оплаты последних 7 дней сверены с AllPay (запуск с `--allpay`, окно `--days=7`) |

Два ключа красные из-за четырёх старых `processing` 12.2025–04.2026. Решение владельца 08.11: это устаревшие
заказы, они в условие не входят. Ключи считаются зелёными, если красными их делают только эти четыре номера
(сверить с `inventory-baseline.json`). Затем [ОПЕРАТОР]/[ВЛАДЕЛЕЦ]:

1. Выгрузка соответствия «заказ WC ↔ AllPay ↔ заказ платформы ↔ билеты» и загрузка на платформу (П-14).
2. У старого канала `settings.gateway.enabled = false` (канал остаётся ради истории) — действие платформы.
3. Снять маршруты WP: переписать `/etc/dokploy/traefik/dynamic/vino-site.yml` копией, где вырезаны все роутеры
   `vino-wp-*` и сервис `vino-wp-svc`, остались `vino-site-always`, `vino-site` и `vino-site-svc` (тот же приём
   `.new` и `mv`). Пробы WP-стороны в `traefik-routing-check.sh` после этого ожидаемо FAIL, остальные ok.
   Затем **[ВЛАДЕЛЕЦ]** в Dokploy останавливает compose WP Vino. **Сертификат**: после остановки WP его labels
   пропадают, и роутер нашего файла с `certResolver: letsencrypt` остаётся единственным, кто продлевает сертификат
   `vinoandco.events`, поэтому `__CERT_RESOLVER__` всегда `letsencrypt`, а файл не удаляется.
4. Данные и бэкапы WP хранятся 4–6 недель. После срока: контейнеры снимаются, сайт убирается из
   `wp-sites-backup.sh` и проверки свежести бэкапов, правила Cloudflare против сканеров WordPress пересматриваются.

## 8. Откат (§14.5)

**До T2** (WP не остановлен, подписчик активен). Время на всё около 5 минут.

1. Новый канал: остановить начало новых продаж (снять публикацию событий в новом канале) — действие платформы
   [ОПЕРАТОР платформы]; уже начатые оплаты идут как обычно.
2. Маршрут. **Заменить** боевой файл откатным (одним файлом, Traefik перезагружается один раз):

```
cd /root/vino-cutover
install -m 0644 vino-rollback.rendered.yml /etc/dokploy/traefik/dynamic/vino-site.yml.new
mv /etc/dokploy/traefik/dynamic/vino-site.yml.new /etc/dokploy/traefik/dynamic/vino-site.yml
sleep 3
docker logs dokploy-traefik --since 1m 2>&1 | grep -iE 'vino|error' | head
```

   Остаются у сайта `/order-status/`, `/my-tickets/` (все три языка) и `/_site/` — по пути, не по токену.
   Старые пути WP (`/payment-successful/`, `/my-account/`) целиком у WP. Полный откат репетиции (когда
   нужно убрать совсем всё) — удалить файл: `rm /etc/dokploy/traefik/dynamic/vino-site.yml`; в боевом откате
   этого **не делать**, пока в новом канале есть покупатели.
3. Снять заглушку на WP и сбросить кеш (`wp-disable-sales.md`, «Откат»), синк каталога WP (15 минут) запустить
   вручную: `docker exec --user www-data $W php -r 'require "/var/www/html/wp-load.php"; do_action("bil24_acf_sync_cron");'`.
4. Включить продажи на WP (они включены самим снятием заглушки), сверить оплаты обоих каналов за период работы
   сайта (инвентаризация + платформа).

**После T2**: то же плюс поднять контейнеры WP из сохранённых томов, включить `gateway.enabled` старого канала,
восстановить маршруты колбэков. Проверка по `traefik-routing-check.sh`.

## 9. Наблюдение после переключения (§14.7)

Ответственный — оператор платформы; первые 7 дней — ежедневный просмотр сводки.

| Что | Порог | Куда | Действие |
|---|---|---|---|
| Сайт недоступен | 2 проверки подряд | ops-бот | откат или перезапуск контейнера |
| Нет удачной сверки сборщика с API | > 10 мин | ops-бот | смотреть журнал сборщика, доступность API |
| Необработанное изменение данных | старше 15 мин | ops-бот | смотреть журнал сборки |
| Ошибки `checkout/start` | > 5 % за 15 мин | ops-бот | разбор, при необходимости откат |
| Оплачен, но без билетов | любой (уже есть в `ops.watchdog`) | ops-бот | разбор вручную |
| Недоставленные письма с билетами | любой | очередь возвратов (EC-V5) | повторная отправка |
| Неудачная доставка CAPI / GA4 MP | > 10 % за час | ops-бот | проверить токены |
| Неудачные возвраты | любой | EC-V5 | повтор |

Что смотреть руками в первый час: `docker logs dokploy-traefik --since 1h | grep -iE 'vino|error'`,
`GET /healthz` сборщика, журнал `allpay-*.log` WP на `hit`/`payment_complete`, счётчик заказов платформы.

## 10. Журнал ночи (заполнить по факту)

| Время | Шаг | Результат | Кто |
|---|---|---|---|
|  | Заглушка WP включена, кеш сброшен |  |  |
|  | Инвентаризация `T0-before` |  |  |
|  | Файл `vino-site.yml` установлен |  |  |
|  | `traefik-routing-check.sh`, 66 проб |  |  |
|  | Кеш Cloudflare сброшен |  |  |
|  | Тестовая покупка и «Мои билеты» |  |  |
|  | Search Console |  |  |

## 11. Открытые вопросы к владельцу

1. ~~Сеть prod-compose без домена~~ — закрыто 07.11: без домена Dokploy сеть не подключает (проверено по compose стейджинга), заводится технический домен без https (шаг 3.2).
2. **Вход в wp-admin на переходный период** (спека оставляет «решить при репетиции»): в файле заложен тот же хост,
   `/wp-admin/` и `/wp-login.php` открыты всем, как сегодня, защищают плагины WP. Нужна ли дополнительная защита
   правилом Cloudflare на эти два пути.
3. ~~Четыре старых заказа `processing`~~: закрыто 08.11, владелец считает их устаревшими, в условие §14.4 они
   не входят.
4. **Заглушка на ≤ 30 минут вместо мягкого отключения кнопки**: возвратившийся с AllPay покупатель увидит заглушку,
   а не «Спасибо» (см. `wp-disable-sales.md`). Устраивает или нужен mu-plugin с фильтром на закрытие продаж.
5. **Токен очистки кеша Cloudflare** для зоны `vinoandco.events` в окружение сборщика (или чистить кеш руками).
6. **`PUBLIC_TURNSTILE_SITE_KEY`** отсутствует в окружении стейджинга: формы на стейджинге проверялись без капчи?
   В prod обязателен, домен в списке Turnstile.
