# Нагрузочный прогон на сервере — runbook (шаг 2)

Дата: 2026-09-22. Решение владельца: прод (`arena-prod-1`, 178.105.217.195)
нагрузкой **не трогаем**. Прогон идёт на одноразовой копии стенда на
отдельном сервере того же класса; после отчёта копия удаляется.

Локальный шаг 1 и его находки: `2026-09-13_local_step1_ru.md`. Набор
сценариев и ручки: `ops/loadtest/README.md`.

## 0. Почему не прод

- У обоих первых клиентов идут живые продажи (мастер-класс 30.09, фестиваль
  16–18.10) с боевыми ключами Stripe. Платный чекаут виджета ходит в живой
  Stripe клиента — его нельзя ни заглушить, ни «отыграть» на проде.
- Любой write-сценарий создаёт сотни заказов в общей базе: сторож заваливает
  Telegram сообщениями «продажа», воркер шлёт письма через Brevo и вебхуки в
  MACS. Дамп откатит базу, письма и вебхуки не отзовёшь.
- Единственное, что допустимо на проде, — читающая нагрузка в тихое окно
  (§8), и только со страховкой из §7.

## 1. Что поднимаем

Два сервера Hetzner в одном регионе (fsn1). Прод `arena-platform-prod-1`
проверен 22.09: CX33, `fsn1-dc14`, 4 vCPU, 7.7 ГБ, Ubuntu 24.04, образ
`ghcr.io/avamb/arena-api:d84a4de`, и он уже работает с `APP_ENV=staging`.

| Роль | Тип | Что на нём |
|------|-----|------------|
| копия стенда `arena-loadtest-1` | CX33 (как прод: 4 vCPU, 8 ГБ) | postgres, redis, arena-api, arena-worker, stripe-stub, Traefik |
| генератор `arena-loadgen-1` | CX22 | k6 (docker), Prometheus + Grafana из `ops/`, psql |

Стоимость: CX33 около €0.014/час, CX22 около €0.007/час — день прогонов
дешевле одного билета. Серверы создаются с SSH-ключом владельца (на копии
он нужен: `docker stats`, дампы, psql).

Создание — в Hetzner Cloud Console (проект, где живёт
`arena-platform-prod-1`) или `hcloud` с API-токеном проекта (на рабочей
машине токена нет и `hcloud` не установлен — сессия агента без них
создать серверы не может):

```bash
hcloud server create --name arena-loadtest-1 --type cx33 --location fsn1 --image ubuntu-24.04 --ssh-key arena-platform
hcloud server create --name arena-loadgen-1  --type cx22 --location fsn1 --image ubuntu-24.04 --ssh-key arena-platform
```

Ключ `arena-platform` = `~/.ssh/arena-platform.pub`; если в проекте его
нет, добавить перед созданием (`hcloud ssh-key create --name arena-platform
--public-key-from-file ~/.ssh/arena-platform.pub`). После создания —
записи в `~/.ssh/config`: `Host arena-loadtest` и `Host arena-loadgen`,
`User root`, `IdentityFile ~/.ssh/arena-platform`, `IdentitiesOnly yes`.

Первичная настройка обоих: `bash ops/server-bootstrap/bootstrap.sh <hostname>`
(ключи вместо паролей, ufw, fail2ban, swap 2 ГБ, Docker). Скрипт НЕ ставит
Dokploy.

### 1.1. Стек на копии

Два пути, выбираем первый:

1. **Подключить копию к существующей панели Dokploy** (Settings → Servers →
   Add) и создать там compose `backend-loadtest` из готового
   `ops/loadtest/server/docker-compose.copy.yml` (Raw compose) с
   переменными из `ops/loadtest/server/.env.example` во вкладке
   Environment. Файл выведен из Raw-compose `backend-prod` (снят по SSH
   22.09) с отличиями из таблицы ниже, лимиты памяти те же, что на проде
   (db 1 ГБ, api/worker по 512 МБ). Так копия проходит через тот же
   Traefik, что и прод, и `TRUSTED_PROXY_COUNT=1` проверяется в
   настоящих условиях.
2. Голый `docker compose` на сервере без Traefik — проще, но без прокси и
   без TLS; допустимо только как запасной вариант.

Отличия окружения копии от прода (ставятся в Raw-compose копии, остальное
как на проде):

| Переменная | Значение на копии | Зачем |
|------------|-------------------|-------|
| `APP_ENV` | `staging` | `config.Validate` отказывается принимать `STRIPE_API_BASE_URL` под `production` |
| `ENABLE_DEV_AUTH` | `true` | `provision.mjs` минтит dev-JWT через `POST /v1/dev/auth/token` |
| `STRIPE_API_BASE_URL` | `http://stripe_stub:12111/v1` | платный чекаут виджета создаёт hosted-сессию на заглушке; сегмент `/v1` обязателен |
| `PUBLIC_TICKETS_BASE_URL` | `https://loadtest-tickets.arenasoldout.com` (или любой origin) | запасной `return_url`; `native.js` шлёт `RETURN_URL` с тем же origin |
| `CORS_ALLOWED_ORIGINS` | тот же origin | allow-list для `return_url` |
| `TRUSTED_PROXY_COUNT` | `1` | Traefik добавляет один hop; иначе все посетители = IP прокси и per-IP лимит режет весь тест |
| `BIL24_COMPAT_ENABLED` | `true` | сценарии шлюза |
| `METRICS_BEARER_TOKEN` | случайная строка | `/metrics` наружу только с токеном; тот же токен в `ops/prometheus/prometheus.yml` |
| `OPS_TELEGRAM_BOT_TOKEN`, `OPS_TELEGRAM_CHAT_ID` | пусто | сторож пишет только в лог, иначе спамит «продажами» в группу |
| Brevo / почтовый бэкенд | ключ убрать, логирующий бэкенд | копия не должна ничего слать наружу |
| `APP_COMMIT` | тот же sha, что тег образа | иначе логи врут про версию |

Сервис заглушки в том же compose (по образцу
`ops/loadtest/bil24/docker-compose.loadtest.yml`):

```yaml
  stripe_stub:
    image: node:20-alpine
    command: ["node", "/stub/stripe-stub.cjs"]
    volumes:
      - /opt/arena-loadtest/stripe-stub.cjs:/stub/stripe-stub.cjs:ro
    environment:
      PORT: "12111"
      HOST: "0.0.0.0"
      PUBLIC_URL: "http://stripe-stub.invalid"
```

Файл `apps/widget/scripts/stripe-stub.cjs` копируется на сервер руками
(`scp`). Заглушка отвечает только на `POST /v1/checkout/sessions` и
`GET /v1/balance` — этого достаточно и для чекаута, и для зелёного бейджа
проверки ключа.

### 1.2. База копии

По умолчанию — **свежая база**: миграции накатывает сервис `migrate` из
compose при первом запуске, а `arena-seed` в образ не входит (Dockerfile
собирает только api/worker/migrate/healthcheck), поэтому seed запускается с
рабочей машины через SSH-туннель к loopback-порту базы копии
(`ports: 127.0.0.1:5432` в compose):

```powershell
ssh -N -L 55433:127.0.0.1:5432 arena-loadtest
```

```powershell
$env:DATABASE_URL = "postgres://arena:<POSTGRES_PASSWORD>@localhost:55433/arena?sslmode=disable"; $env:JWT_SIGNING_SECRET = "x"; $env:APP_ENV = "development"; go run ./apps/backend/cmd/arena-seed
```

Тот же туннель обслуживает SQL-снимок из §2 и `audit.sql` из §3.
`provision.mjs` заводит всё остальное сам (канал, шлюзовый токен, платёжную
конфигурацию, события, фид-токен) в сидированной org `OrgA`.

Дамп прода на копию заливать **не нужно** и по умолчанию нельзя: в нём
персональные данные покупателей, боевые ключи Stripe в
`payment_provider_configs.secrets`, адреса вебхуков MACS и живые
`delivery_jobs`. Если владелец всё же захочет объём «как на проде», перед
запуском воркера на копии обязательно:

```sql
UPDATE payment_provider_configs SET secrets = '{}'::jsonb, is_active = false;
UPDATE sales_channels SET settings = settings - 'webhooks';   -- проверить реальный ключ в settings
UPDATE delivery_jobs SET status = 'skipped' WHERE status IN ('pending','processing');
UPDATE worker_jobs SET status = 'failed' WHERE status = 'pending';
UPDATE outbox_events SET processed_at = now() WHERE processed_at IS NULL;
UPDATE customers SET email = 'lt+' || id || '@example.test', phone = NULL;
```

и только после этого `docker compose up worker`.

### 1.3. DNS и файрвол

- Cloudflare: `loadtest-api.arenasoldout.com` → IP копии. Первые прогоны —
  **серое облако** (DNS only), чтобы мерить origin; один отдельный короткий
  прогон — через оранжевое облако (§5).
- Файрвол Hetzner копии: 443/80 с IP генератора и с адресов Cloudflare;
  22 — с IP владельца. На проде 443 открыт только для Cloudflare — на копии
  так же плюс генератор.
- `provision.mjs` отказывается работать с `api.arenasoldout.com`,
  `tickets.arenasoldout.com`, `app.arenasoldout.com` даже при
  `ALLOW_REMOTE=1` — защита от перепутанного `BASE_URL`.

## 2. Мониторинг во время прогона

Три уровня, все — ДО первого запуска k6, чтобы иметь базовую линию.

**Сервер.** Графики Hetzner Cloud Console (CPU, сеть, диск) — ничего
ставить не надо. На копии в `tmux`:

```bash
docker stats --format 'table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}'
```

Лимиты из прод-compose (api/worker по 512 МБ, db 1 ГБ) — стоп-сигнал, когда
`MemUsage` подходит к лимиту: OOM-перезапуск api под нагрузкой — это находка,
а не помеха.

**Приложение.** На генераторе — Prometheus + Grafana из репозитория
(`docker compose --profile observability up -d prometheus grafana` из
корня, с правленым `ops/prometheus/prometheus.yml`: targets
`loadtest-api.arenasoldout.com:443`, `scheme: https`,
`authorization.credentials: <METRICS_BEARER_TOKEN>`). k6 пишет свои метрики
туда же: `--out experimental-prometheus-rw` с
`K6_PROMETHEUS_RW_SERVER_URL=http://localhost:9090/api/v1/write`; дашборд
`arena_platform_overview` показывает их в строке Load Test Results рядом с
серверными.

**База.** С генератора раз в 10 секунд (`watch -n 10`), DSN копии через
SSH-туннель:

```sql
SELECT (SELECT count(*) FROM pg_stat_activity WHERE datname = 'arena')                       AS conns,
       (SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock')                AS waiting_on_lock,
       (SELECT max(now() - xact_start) FROM pg_stat_activity WHERE state <> 'idle')          AS longest_tx,
       (SELECT deadlocks FROM pg_stat_database WHERE datname = 'arena')                      AS deadlocks_total,
       (SELECT count(*) FROM outbox_events WHERE processed_at IS NULL)                        AS outbox_backlog,
       (SELECT count(*) FROM worker_jobs WHERE status = 'pending')                            AS jobs_pending,
       (SELECT count(*) FROM outbox_events WHERE dead_lettered_at IS NOT NULL)                AS dead_letters;
```

`DB_POOL_MAX_CONNS` api (20 на локальном compose) — стоп-сигнал, когда
`conns` упирается в пул: латентность растёт не из-за CPU, а из-за очереди
за соединением.

**Сторож** на копии выключен (§1.1); его проверки заменяет `audit.sql` после
каждого сценария.

## 3. Порядок прогона

С рабочей машины владельца (PowerShell) — фикстуры:

```powershell
$env:BASE_URL = "https://loadtest-api.arenasoldout.com"; $env:ALLOW_REMOTE = "1"; $env:RETURN_URL = "https://loadtest-tickets.arenasoldout.com/"; node ops/loadtest/provision.mjs
```

`provision.mjs` падает, если проверка платёжной конфигурации не `ok` — это
значит, что api копии не видит заглушку (`STRIPE_API_BASE_URL`). Файл
`ops/loadtest/results/fixtures.local.json` копируется на генератор
(`scp` в `~/lt/results/`), вместе со всей папкой `ops/loadtest`.

На генераторе, всегда с `ABORT_ON_FAIL=1`:

```bash
run() { docker run --rm -v "$HOME/lt:/lt" -e BASE_URL=https://loadtest-api.arenasoldout.com -e ABORT_ON_FAIL=1 "$@" grafana/k6:0.54.0; }
# ступень 1: четверть цели
run -e SCENARIO=flow -e BROWSERS=50  -e ORDERS_PER_MIN=12 -e DURATION=5m  run /lt/gateway.js
run -e SCENARIO=flow -e BROWSERS=50  -e ORDERS_PER_MIN=12 -e DURATION=5m  run /lt/native.js
# ступень 2: половина
run -e SCENARIO=flow -e BROWSERS=100 -e ORDERS_PER_MIN=25 -e DURATION=5m  run /lt/gateway.js
run -e SCENARIO=flow -e BROWSERS=100 -e ORDERS_PER_MIN=25 -e DURATION=5m  run /lt/native.js
# ступень 3: цель (200 посетителей, 50 заказов/мин)
run -e SCENARIO=flow -e DURATION=5m run /lt/gateway.js
run -e SCENARIO=flow -e DURATION=5m run /lt/native.js
# гонки и истечения — перед каждым заново node provision.mjs (пулы расходуются)
run -e SCENARIO=race      run /lt/gateway.js
run -e SCENARIO=race      run /lt/native.js
run -e SCENARIO=expiry    run /lt/gateway.js
run -e SCENARIO=paywindow run /lt/gateway.js
# оба входа одновременно + оператор крутит квоты
run -e SCENARIO=flow -e DURATION=5m run /lt/gateway.js &  run -e SCENARIO=flow -e DURATION=5m run /lt/native.js &  run -e DURATION=5m run /lt/quota_admin.js; wait
# soak: час на половине цели, оба входа
run -e SCENARIO=flow -e BROWSERS=100 -e ORDERS_PER_MIN=25 -e DURATION=60m run /lt/gateway.js &  run -e SCENARIO=flow -e BROWSERS=100 -e ORDERS_PER_MIN=25 -e DURATION=60m run /lt/native.js; wait
```

После КАЖДОГО сценария — аудит сессии (id в `fixtures.local.json`):

```bash
psql "$DSN" -v session_id=<uuid> -f ~/lt/sql/audit.sql
```

Каждая колонка `violations` должна быть 0. Между ступенями — пауза 2–3
минуты, чтобы увидеть, возвращаются ли CPU, память и число соединений к
базовой линии (утечка соединений/горутин видна именно здесь).

## 4. Стоп-критерии и кнопка «стоп»

Останавливаемся (k6 сам, если `ABORT_ON_FAIL=1`, или рукой), когда:

- ошибок (5xx, transport) больше 0.5 % за 30 секунд;
- p95 `checkout/start` или CREATE_ORDER_EXT выше 2 секунд;
- `docker stats`: память api/worker в 10 % от лимита или перезапуск контейнера;
- `conns` в пуле, `waiting_on_lock` растёт, `deadlocks_total` увеличился;
- Traefik отвечает 502/504.

Кнопки, от быстрой к тяжёлой:

1. `Ctrl+C` в k6 / `docker stop <k6-контейнер>` — нагрузка исчезает
   мгновенно, стенд сам догонит очереди.
2. `docker compose stop api` на копии — если нужно заморозить и посмотреть
   базу «как есть».
3. Hetzner Console → Power cycle — если сервер завис целиком. Dokploy живёт
   на другом сервере и не страдает.

Копия одноразовая, восстанавливать её не нужно: если состояние испорчено,
`dropdb` + миграции + seed + `provision.mjs` — десять минут.

## 5. Отдельный прогон через Cloudflare

k6 с одного IP похож на атаку. Один короткий прогон `flow` на цели (5 минут)
через оранжевое облако показывает, что сделает Cloudflare с реальным пиком:
429/1015, challenge, Under Attack. Это отдельная находка про настройки
Cloudflare (WAF-исключение для `/v1/public/*` и `/compat/bil24/json` по
rate limit, Bot Fight Mode), а не про arena; не крутить настройки
Cloudflare прода по результатам одного прогона — записать и обсудить.

## 6. Отчёт

`docs/loadtest/<дата>_server_step2_ru.md`, по образцу отчёта шага 1:
конфигурация серверов и sha образа, таблица ступеней (посетители,
заказы/мин, p50/p95/p99 по шагам пути, ошибки, throttled, purchases
ok/sold_out/failed), графики Grafana (скриншоты в `docs/loadtest/img/`),
базовая линия и «после» по `docker stats` и SQL-снимку, результаты
`audit.sql`, находки с оценкой (critical/high/low) и предложением. JSON-
сводки k6 из `results/` — приложить к отчёту (папка gitignored, копировать
руками).

## 7. Уборка

1. Отчёт закоммичен.
2. Hetzner: удалить оба сервера и их снапшоты, если делались.
3. Cloudflare: удалить записи `loadtest-*`.
4. Dokploy: удалить сервер и compose `backend-loadtest` (Raw-compose с
   токенами копии больше нигде не нужен).
5. Локально: `ops/loadtest/results/fixtures.local.json` от копии удалить —
   в нём шлюзовый токен и webhook secret копии.

## 8. Если всё же нужен читающий прогон на проде

Только по отдельному решению владельца, только `feed.js` / GET-сценарии,
никаких заказов. Страховка ДО первого запроса:

1. `pg_dump -Fc` базы прода + архив тома `arena-media`, `pg_restore -l`
   для проверки, копия вне сервера (рабочая машина или `acer-server`).
   Бэкапов Dokploy по-прежнему нет.
2. Снапшот `arena-prod-1` в Hetzner (с работающего сервера, пара минут,
   около €0.012/ГБ в месяц) — откат всей машины одной кнопкой. Удалить
   после прогона.
3. Записать текущий тег образа из Raw-compose `backend-prod` — откат кода
   это четыре строки и Deploy (`docs/ops/arena_server_move_runbook_2026-09-18_ru.md`).
4. Тихое окно: ночь по Мадриду, не в день события клиента, не в первые сутки
   после открытия продаж.
5. Сторож Telegram включён — это и есть сигнал тревоги. Стоп-критерии из §4,
   `ABORT_ON_FAIL=1`, старт с 25 посетителей и рост в четыре ступени.
6. Генератор — не с домашнего IP: одна ступень нагрузки через Cloudflare с
   домашнего адреса может отправить его в challenge для самого владельца.
