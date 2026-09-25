# Read-only аудит серверной инфраструктуры — 17.09.2026

Статус: **READ-ONLY аудит завершён. Ничего не изменялось, не перезапускалось, не удалялось.**
Основание: `HETZNER_WORDPRESS_INFRA_MASTER_BRIEF_2026-09-17.md`.
Метки по требованию брифа: **CONFIRMED CURRENT** / **HISTORICAL / NO LONGER PRESENT** / **UNKNOWN / NEEDS ACCESS** / **RECOMMENDATION**.
Секреты в отчёте не приводятся — только факт наличия и место.

---

## 0. Резюме в десяти пунктах

1. Реально существуют и доступны по SSH **четыре Hetzner-сервера в периметре**: `lead-parser` (CPX31 legacy, 8 GB), `arena-macs` (CX22 legacy, 4 GB — это и есть «Legacy CX22» из брифа, и он же MACS/сканирование), `minimaldeco` (CPX22, 4 GB, куплен 16.07.2026), `lampyrisevents` (CX33, 8 GB, куплен 18.07.2026). Плюс домашний `acer-server` (не Hetzner) и клиентский `nordicmart` (вне периметра по вашему указанию).
2. **Нет ни одного автоматического внешнего бэкапа, кроме UpdraftPlus → Google Drive у четырёх WordPress-сайтов.** Dokploy-бэкапы не настроены ни на одном из трёх Dokploy-хостов (0 записей `backup`). Единственное «расписание бэкапа» на lead-parser имеет пустую команду и последний раз запускалось 08.09.2025 — все запуски с ошибкой. Ни одного restore-теста не обнаружено.
3. **lead-parser находится в хроническом дефиците памяти**: 219 OOM-kill за 30 дней, из них 42 — `mysqld`. Сегодня трижды убит MySQL продакшн-магазина `vinoandco.events` (18 перезапусков), а сам Dokploy-стек (dokploy, postgres, redis, arena-admin) перезапускался 4 раза за двое суток (dokploy — exit 137). Swap 2.7 GB из 4 GB занят.
4. **Следы компрометации**: в PostgreSQL бота `mindsethappybot` на lead-parser есть база `readme_to_recover` с требованием выкупа в BTC, созданная 08.01.2026 (на следующий день после создания проекта). Пароль этой БД — значение по умолчанию из compose (`postgres`). Сейчас порт наружу не опубликован, данные (342 MB, 18 таблиц) на месте, но учётку надо считать скомпрометированной.
5. **Открытые наружу порты, подтверждённые извне**: на lead-parser — Dokploy UI `:3000`, Swarm `:2377`/`:7946`, админка бота `:18088`, webhook-приёмник `:9876`; на arena-macs — **Traefik dashboard `:8080` с `api.insecure=true` отдаёт всю конфигурацию маршрутов анонимно**; на minimaldeco и lampyrisevents — Dokploy UI `:3000`. Firewall на lead-parser и arena-macs **отсутствует** (INPUT ACCEPT, ufw не установлен, fail2ban нет) при ~20 000 неудачных SSH-попыток в неделю на каждом.
6. **Cloudflare можно обойти**: все домены за оранжевым облаком, но origin на всех четырёх серверах отвечает на прямой запрос по IP с нужным Host (проверено `curl --resolve`). WAF/rate-limit Cloudflare не защищают, пока 80/443 открыты для всего интернета.
7. На **arena-macs** диск заполнен на **90 % (4 GB свободно)** при 16.6 GB мусорных образов и 3.6 GB journald; uptime 471 день с ожидающим перезагрузки ядром; два процесса `claude` живут 460+ дней; **пароль MongoDB виден в командной строке 16 зомби-процессов `node mongodb ...` (через `ps`) и в plaintext в docker-compose.yml**.
8. Из исторического брифа **больше не существует / не работает**: Supabase, Postiz, LightRAG, Crawl4AI, Ollama/Open-WebUI (следов нет вообще), TgTicketAgent, WordPress `84umlx` и `61uqee`, dev-контур бота. Их Dokploy-записи, домены, тома и сети остались «идлом». Сервер `ubuntu-8gb-nbg1-1` не найден нигде — HISTORICAL. «Ava-dokploy CPX31» по характеристикам совпадает с lead-parser (нужно подтвердить в консоли Hetzner).
9. **Legacy CX22 как Dokploy control plane — да, подходит** (2 vCPU/4 GB достаточно для Dokploy+Postgres+Traefik ≈ 1.2 GB), но только после: переноса MACS+MongoDB, очистки диска, перезагрузки, удаления двух неатрибутированных root-ключей и закрытия `:8080`.
10. Цены Hetzner изменились 15.06.2026 для новых заказов: CX23 €5.49, CX33 €8.49, **CPX22 €19.49, CPX32 €35.49**. Поэтому главный ценный legacy-актив — не CX22 (новый CX23 всего на €1 дороже), а **lead-parser CPX31 (~€17.49 против €35.49 за новый CPX32)**. Ни один legacy-сервер нельзя rescale-ить: это переводит его на новые цены. Отдельная находка: `minimaldeco` (CPX22, куплен после 15.06) вероятно стоит €19.49/мес за 2 vCPU/4 GB, тогда как CX33 даёт 4 vCPU/8 GB за €8.49 — проверить по счёту.

---

## 1. Периметр и метод

- Источник алиасов: `~/.ssh/config`, `config.bak`, `known_hosts`. Опрошены все хосты, куда есть ключ.
- На каждом хосте выполнен единый read-only скрипт (система, пользователи/SSH, firewall, порты, cron/systemd, Docker/Swarm/Dokploy, Traefik, БД, WordPress, размеры). Значения `Env`, `.env`, ключи, `acme.json`, пароли **не выводились**.
- Снаружи: DNS (1.1.1.1), TCP-пробы портов, `curl --resolve` к origin.
- Артефакты аудита (локально, не в репо): `audit_*.txt`, `followup_*.txt`, `macs_extra.txt` в scratchpad-каталоге сессии.

Исключено по вашему указанию: `nordicmart` (89.167.108.178 — host key изменился, не подключался; 135.181.94.96 `ubuntu-16gb-hel1-1` — оказался тем же проектом, aaPanel/nginx/MariaDB, дальше не исследовался). Замечание для передачи проекта: ваш ключ `andreev@abhteam` до сих пор в `root/.ssh/authorized_keys` этого сервера, там `PermitRootLogin yes` + `PasswordAuthentication yes` и 242 000 неудачных SSH-попыток за неделю.

---

## 2. Инвентарь серверов

| Alias | Hostname (Hetzner) | IPv4 / IPv6 | Тип (вывод по железу) | vCPU / RAM / Disk | OS / kernel | Uptime | Роль сейчас | Статус vs бриф |
|---|---|---|---|---|---|---|---|---|
| `lead-parser` | `ubuntu-8gb-fsn1-1`, fsn1-dc14, instance 64109406 | 78.46.176.249 / 2a01:4f8:c013:e221::1 | **CPX31 legacy** (AMD EPYC Rome, 160 GB) | 4 / 7.75 GB / 150 GB (41 % занято) | Ubuntu 24.04.2 / 6.8.0-107, reboot required, 80 обновлений | 157 д | Dokploy v0.29.13 (тег `latest`) + Traefik 3.1.2 + **вся продакшн-нагрузка**: 4 WP, arena-backend, n8n, бот, pr-top, umami | CONFIRMED CURRENT. Совпадает по характеристикам с «Ava-dokploy CPX31» (бриф B) — вероятно один и тот же сервер |
| `arena-macs` | `ubuntu-macs-4gb-fsn1-2`, fsn1-dc14, instance 64438901 | 91.99.84.144 / 2a01:4f8:c17:f3bc::1 | **CX22 legacy** (Intel Skylake, 40 GB) | 2 / 3.8 GB / 38 GB (**90 % занято**) | Ubuntu 24.04.2 / 6.8.0-58, reboot required, 80 обновлений | **471 д** | MACS: `arenasoldout-backend` (FastAPI), `arenasoldout-frontend` (Next.js), MongoDB 8.0.17, Traefik v3.0. Без Dokploy | CONFIRMED CURRENT. Бриф A (legacy CX22), E (MACS) и F (сканирование) — **один и тот же сервер** |
| `minimaldeco` | `debian-4gb-fsn1-1`, fsn1-dc8, instance 151637886 (создан 16.07.2026) | 167.233.210.168 / 2a01:4f8:c015:79c7::1 | **CPX22** (AMD Genoa, 80 GB) | 2 / 3.8 GB / 76 GB (28 %) | Debian 13 / 6.12.101 | 32 д | Dokploy v0.29.13 + Traefik 3.6.7; 1 WooCommerce-сайт `minimaldeco.es` | CONFIRMED CURRENT, Hetzner подтверждён |
| `lampyrisevents` | `debian-4gb-fsn1-1` (**тот же hostname, что у minimaldeco**), fsn1-dc14, instance 152439998 (создан 18.07.2026) | 167.233.208.166 / 2a01:4f8:c012:ac2b::1 | **CX33** (AMD Genoa, 80 GB) | 4 / 7.75 GB / 76 GB (40 %) | Debian 13 / 6.12.101 | 25 д | Dokploy v0.30.6 + Traefik 3.6.7; `lampyrisevents.com` prod + 2 staging-сайта вне Dokploy | Не был в брифе — **новый, CONFIRMED CURRENT** |
| `acer-server` | домашний, LAN 192.168.1.137, Tailscale 100.81.30.115 | — | не Hetzner | 4 / 7.8 GB / 405 GB home (/var 87 %) | Debian 13 | 231 д | PostgreSQL 17 local, Samba, Docker без контейнеров, Tailscale, fail2ban | Вне Hetzner-периметра; кандидат в offsite-приёмник бэкапов |
| `nordicmart` | `ubuntu-16gb-hel1-1`, hel1 | 135.181.94.96 | Hetzner (16 GB) | 4 / 15.6 GB / 150 GB + volume 400 GB (90 %) | Ubuntu 24.04 | 159 д | aaPanel, nginx, MariaDB, mail; WP nordicmart.com/.lt | Клиентский проект, вне периметра |
| — | `ubuntu-8gb-nbg1-1` (бриф D) | — | — | — | — | — | Ни в config, ни в known_hosts, ни в метаданных | **HISTORICAL / NO LONGER PRESENT** (подтвердить в консоли Hetzner) |

Типы серверов выведены из CPU/RAM/диска и линеек Hetzner (CX22: 2 Intel/4 GB/40 GB; CPX31: 4 AMD/8 GB/160 GB; CPX22: 2 AMD/4 GB/80 GB; CX33: 4/8 GB/80 GB). **Подтвердить в Hetzner Console — UNKNOWN / NEEDS ACCESS** (нет API-токена в сессии).

---

## 3. Карта: Cloudflare → сервер → Traefik → контейнер

Все перечисленные домены — **orange-cloud (проксируются Cloudflare)**, A-записи указывают на CF (188.114.x, 104.21.x, 172.67.x). Зоны на разных NS-парах Cloudflare (anita/arushi/grannbo/art) — вероятно разные CF-аккаунты или зоны, свести в один список.

| Домен | Сервер (origin) | Traefik-роутер / источник | Контейнер : порт | TLS на origin | Статус |
|---|---|---|---|---|---|
| arenasoldout.com | lead-parser | docker labels (Dokploy compose `arena-wordpress-hpl1ba`) | `…-wordpress-1:80` (WP 7.0.2, MySQL 8.4) | LE | работает |
| api.arenasoldout.com | lead-parser | labels `arena-backend-2lzrkj` | `…-api-1:8080` (ghcr arena-api:99c64f1) | LE | работает (404 на `/` — норма) |
| app.arenasoldout.com | lead-parser | file `arena-admin-ti1kei.yml` → swarm-сервис | `arena-admin-ti1kei:8080` (nginx) | LE | работает |
| macs.arenasoldout.com | **arena-macs** | labels в `/docker/arenasoldout/docker-compose.yml` | frontend `:3000`, `/api` → backend `:8000` | LE | работает |
| tgtest.arenasoldout.com | lead-parser | Dokploy domain → `tgticketagent-tgdev` | контейнера нет | LE-запись есть | **idle, 404** |
| vinoandco.events | lead-parser | labels `asoconnector-vinoandcotempvino-fsppdj` | `…-wordpress-1:80` (WP 7.1, Woo/WPML, кастомный образ) | LE | работает; **его MySQL OOM-killed трижды сегодня** |
| staging.vinoandco.events | lampyrisevents | labels в `/opt/vinoandco-staging/docker-compose.override.yml` (**вне Dokploy**) | `vinoandco-staging-wordpress-1:80` | LE | работает |
| lampyrisevents.com | lampyrisevents | labels (Dokploy compose `lampyrisevents-wordpress-ozgyu1`) | `…-wordpress-1:80` (WP 7.1, Woo, Stripe, Redis) | LE | работает |
| staging.lampyrisevents.com | lampyrisevents | labels `/opt/lampyrisevents-staging` (**вне Dokploy**) | `lampyrisevents-staging-wordpress-1:80` | LE | работает |
| minimaldeco.es, minimaldeco.andreevmaster.com | minimaldeco | labels (Dokploy compose `minimaldeco-wordpress-sa7hxl`) | `…-wordpress-1:80` (WP 7.1, Woo, WPML, JetEngine, B2BKing) | LE | работает; поддомен → 301 на .es |
| marinabakanova.com | lead-parser | labels `marinabakanovacom-wordpress-2h3b5p` | `…-wordpress-1:80` | LE | **403** и через CF, и напрямую (security-плагин/hide-login? проверить) |
| ndarchdesign.andreevmaster.com | lead-parser | labels `ndarchdesign-wordpress-wroe1g` | `…-wordpress-1:80` (WP 7.1, 4.5 MB БД) | CF Origin CA wildcard | работает (dev/витрина) |
| pr-top.com | lead-parser | labels `prtop-practice-ojpalv` | `…-frontend-1:80` (nginx) | LE | работает |
| app.pr-top.com | lead-parser | labels | `…-umami-1:3000` (Umami + pg15) | LE | работает |
| n8.andreevmaster.com | lead-parser | labels на контейнере `n8n` (**дублирующие роутеры** `n8n` и `lidgen-n8n-4v8qte-2-*`) | `n8n:5678` (n8n 2.25.7) | CF Origin CA | работает; в Dokploy `certificateType=none` |
| app.andreevmaster.com (Dokploy UI) | lead-parser | file `dokploy.yml` | `dokploy:3000` | CF Origin CA | работает; **тот же UI доступен напрямую по 78.46.176.249:3000** |
| 3hours.andreevmaster.com | lead-parser | labels `mindsethappybot-3hours` | `…-admin-1:8080` | CF Origin CA | работает; **и напрямую по :18088** |
| dev-3hours.andreevmaster.com | lead-parser | Dokploy domain → dev-стек | контейнера нет | — | idle |
| api./studio.andreevmaster.com (Supabase), post.andreevmaster.com (Postiz ×2), crawl4ai., lightrag., devnewvinoandco., vinoandco.andreevmaster.com | lead-parser | Dokploy domains → остановленные стеки | контейнеров нет | — | **idle, 404**; DNS-записи живы |
| deploy.andreevmaster.com | lead-parser | file `vino-webhook.yml` → `172.17.0.1:9876` (host-процесс python) | `vino-webhook.service` | — | **NXDOMAIN** в DNS; при этом порт 9876 открыт наружу напрямую |
| Dokploy UI minimaldeco / lampyrisevents | 167.233.210.168:3000, 167.233.208.166:3000 | — | `dokploy:3000` | нет (HTTP) | **открыт наружу без домена/TLS** |
| *.sslip.io / *.traefik.me | все Dokploy-хосты | автодомены Dokploy по IP | — | HTTP | побочные HTTP-входы к WP по IP |

---

## 4. Инвентарь приложений

### 4.1 WordPress / WooCommerce

| Сайт | Сервер | Версия WP / образ | БД (размер) | Ключевые плагины | uploads / plugins | Бэкап |
|---|---|---|---|---|---|---|
| vinoandco.events (prod) | lead-parser | 7.1, кастомный образ (Dockerfile в Dokploy) | MySQL 8.4 `wordpress` 150 MB; volume `wp_data` 1.9 GB, `wp_app` 3.0 GB | WooCommerce, WPML, Elementor Pro, bil24-acf-sync, bil24-ticket-mailer, lampyris-ops, allpay, PayPal, wp-super-cache, UpdraftPlus, AI1WM | 542 MB / 670 MB | UpdraftPlus **без удалённого хранилища** (1.5 GB локальных архивов внутри тома); ручные дампы в `/opt/vino-snap` (749 MB) |
| lampyrisevents.com (prod) | lampyrisevents | 7.1, кастомный образ | MySQL 8.4 409 MB; volume 5.4 GB (**binlog-рост**) + Redis 256 MB | WooCommerce, Stripe, WPML, Elementor Pro, bil24-*, lampyris-ops, redis-cache, cache-enabler | 1.1 GB / 614 MB | UpdraftPlus weekly → Google Drive, retain 4 |
| minimaldeco.es (prod) | minimaldeco | 7.1, `wordpress:latest` | MySQL 8.4 77 MB; volume 4.3 GB (binlog) + Redis | WooCommerce, Stripe, WPML, JetEngine/Crocoblock, B2BKing, Baselinker, hide-my-wp | 334 MB / 585 MB | UpdraftPlus **daily** → Google Drive, retain 7/14 ✓; ручные SQL-дампы в `/root` (1.5 GB) |
| arenasoldout.com | lead-parser | 7.0.2, `wordpress:latest` | MySQL 8.4 **751 MB** | WPML, Elementor Pro, AIOS, wp-fastest-cache | 471 MB / 425 MB | UpdraftPlus weekly → Google Drive, retain 4 |
| marinabakanova.com | lead-parser | 7.0.2, `wordpress:latest` | MySQL 8.4 58 MB | Elementor Pro, Polylang, wps-hide-login, security-ninja | 236 MB / 249 MB | UpdraftPlus weekly → Google Drive; локальные `/root/mb_backups` 76 MB |
| ndarchdesign.andreevmaster.com | lead-parser | 7.1, `wordpress:latest` | MySQL 8.4 4.5 MB | Elementor Pro | 96 MB / 152 MB | **нет** |
| staging.lampyrisevents.com | lampyrisevents | 7.1, кастомный | MySQL 386 MB + Redis | как prod + Jetpack, WooPayments | 876 MB / 755 MB | нет (staging) |
| staging.vinoandco.events | lampyrisevents | 7.1, кастомный | MySQL 117 MB | как prod | 340 MB / 670 MB | нет (staging) |
| `84umlx`, `61uqee`, `vinoandco-ummdi1` | lead-parser | — | сети/каталоги остались, контейнеров и томов нет | — | — | HISTORICAL; root-cron всё ещё дёргает `wp cron` несуществующего контейнера `84umlx` каждую минуту |
| `/home/dev/wp-dev/vinoandco` | lead-parser | bind-mount из брифа | 5.1 GB на диске, **никем не смонтирован** | — | — | HISTORICAL (данные остались) |

Все WP — один контейнер Apache+PHP (образ `wordpress` или его наследник) + отдельный MySQL 8.4 на сайт; **memory/cpu-лимитов нет ни у одного WP/MySQL контейнера**. `wp-cron` вынесен в host-cron (правильно), но на lead-parser один из cron-джобов мёртвый.

### 4.2 n8n

- Один экземпляр: контейнер `n8n`, образ `docker.n8n.io/n8nio/n8n:2.25.7`, Dokploy-compose `lidgen-n8n-4v8qte`, домен `n8.andreevmaster.com`, `restart=always`, лимитов нет.
- **Хранилище — SQLite** (`DB_TYPE` не задан): `database.sqlite` **8.3 GB** + WAL 103 MB, том 8.6 GB — крупнейший том на lead-parser. `EXECUTIONS_DATA_PRUNE` не задан → история executions растёт бесконечно. Это главный кандидат на повторение «disk full».
- `N8N_PROTOCOL=http`, `WEBHOOK_URL=https://n8.andreevmaster.com/` — TLS терминируется в CF/Traefik, ок.
- Подключён к сети остановленного Supabase (`lidgen-supabase-nldtm3`) — след старой интеграции.
- Бэкапов нет. Дублирующиеся Traefik-роутеры (`n8n` + `lidgen-n8n-4v8qte-2`) на одном Host.

### 4.3 Базы данных

| Экземпляр | Сервер | Движок / версия | Приложение | Размер | Том / путь | Сеть | Пиннинг | Бэкап |
|---|---|---|---|---|---|---|---|---|
| dokploy-postgres | lead-parser / minimaldeco / lampyrisevents | PostgreSQL 16.9 / 16.14 / 16.14 (`postgres:16`) | Dokploy | 12 / 11 / 11 MB | `dokploy-postgres-database`, `dokploy-postgres` | overlay `dokploy-network`, не опубликован | major-тег | **нет** (на lead-parser это уже падало в 10.2025) |
| dokploy-redis | lead-parser | Redis 7.4.3 | Dokploy | 1 MB | без persistence (rdb ok) | внутр. | `redis:7` | не нужен |
| arena-backend db | lead-parser | PostgreSQL 17.10 (`postgres:17-alpine`), лимит 1 GB | arena API/worker | `arena` 20 MB | `arena-backend-2lzrkj_arena-db-data` 75 MB | внутр. | major | **нет** |
| arena-backend redis | lead-parser | Redis 7.4.10, AOF on, лимит 192 MB | arena | <1 MB | том | внутр. | | — |
| mindsethappybot postgres | lead-parser | pgvector pg16 (16.11) | Telegram-бот | `mindsethappybot` **342 MB** + **`readme_to_recover` (ransom-note)** | том 453 MB | внутр. (сейчас) | `pg16` | **нет** |
| umami-db | lead-parser | PostgreSQL 15.17 | Umami | 12 MB | том 67 MB | внутр. | | нет |
| 4× wp_db | lead-parser | MySQL 8.4.8 | WP | 751 / 150 / 58 / 4.5 MB | тома | внутр. | `mysql:8.4` | через UpdraftPlus (3 из 4) |
| 3× wp_db | lampyrisevents | MySQL 8.4.10 | WP prod + 2 staging | 409 / 386 / 117 MB | тома 5.4 / 1.4 / 1.6 GB | внутр. | | Updraft (prod) |
| wp_db + redis | minimaldeco | MySQL 8.4.10 / Redis 7.4.9 (256 MB) | WP | 77 MB | том 4.3 GB | внутр. | | Updraft daily |
| **MongoDB** | arena-macs | **8.0.17 (пиннинг ✓)**, `--auth` | MACS backend | `arenasoldout` **4 MB** | bind `/docker/mongodb/data` (457 MB), configdb — безымянный том | **127.0.0.1:27017 ✓** (снаружи закрыт — проверено) | ✓ | `/docker/mongodb/backup` — ручной, 01.2026; автоматики нет |
| dev-тома | lead-parser | — | `mindsethappybot-dev…postgres_data_dev` 87 MB, `tgticketagent…` 47 MB, postiz/temporal/es ~250 MB, `redis-data-volume`, `lidgen-supabase…db-config` | — | 15 dangling-томов | — | — | HISTORICAL-данные без владельца |

Ни одна БД не опубликована наружу (кроме исторического инцидента с MongoDB и, по косвенным признакам, Postgres бота в 01.2026). Хост-уровневых MySQL/Postgres на Hetzner-серверах нет.

### 4.4 AI, боты, парсеры, прочие приложения

| Приложение | Сервер | Состояние | Примечание |
|---|---|---|---|
| `mindsethappybot-3hours` (bot + admin + pgvector) | lead-parser | работает 5 мес. | admin опубликован на `0.0.0.0:18088` **в обход Traefik/CF**; БД с ransom-note |
| `mindsethappybot-dev3hours` | lead-parser | остановлен | HISTORICAL (том 87 MB остался) |
| `prtop-practice` (frontend, backend node:3001, bot, umami) | lead-parser | работает | лимитов нет; том `backend-backups` 138 MB — локальные бэкапы приложения |
| `tgticketagent-tgdev` | lead-parser | остановлен | HISTORICAL; домен tgtest.arenasoldout.com жив |
| Supabase (`lidgen-supabase`), LightRAG, Crawl4AI (swarm 0/0), Postiz ×2 | lead-parser | остановлены, каталоги 265 / 26 / 17 / 33 MB | HISTORICAL; домены в CF живы → 404 |
| Ollama / Open-WebUI | — | **не найдено нигде** | HISTORICAL / NO LONGER PRESENT |
| `vino-webhook.service` (python, :9876) | lead-parser | работает на хосте | GitHub-webhook с HMAC-проверкой → `deploy-staging.sh`/`deploy-prod.sh` из `/opt/vinoandco-deploy` (git-репо). Порт открыт наружу, домен deploy.andreevmaster.com не резолвится |
| `nodered` | nordicmart | остановлен 9 мес. | вне периметра |

### 4.5 ArenaSoldOut / MACS / сканирование

- **arena (платформа)**: `arena-backend` (api + worker, `ghcr.io/avamb/arena-api:99c64f1`, лимиты 512 MB, non-root, healthcheck ✓) + `arena-admin` (swarm-app, nginx) + `arenasoldout.com` (WP) — всё на **lead-parser**. 10 старых тегов `arena-api` в образах.
- **MACS** (`macs.arenasoldout.com`): **arena-macs**. Backend — `uvicorn main:app --reload` (dev-режим в проде), frontend Next.js, MongoDB. Образы собраны 15 месяцев назад из `registry.gitlab.com/skripov.com/arenasoldout-group/*` — код прежней команды разработки; ACME-e-mail в Traefik — e-mail третьего лица. Данные MongoDB всего 4 MB.
- Бриф F («отдельный сервер сканирования, 2–3 дня в месяц») = **arena-macs**. Отдельного scanning-хоста нет. Бизнес-требование «snapshot → delete → restore перед событием» пока не реализовано: сервер работает постоянно.

### 4.6 Бэкапы — сводка

| Что | Есть? | Где | Retention | Вне VM? | Restore-тест |
|---|---|---|---|---|---|
| Dokploy DB / конфиги (3 хоста) | **нет** | — | — | — | — |
| Dokploy destination GCS `dokploy-backups-ava` (europe-west3) | destination есть, **backup-джобов 0** | GCS | — | — | — |
| Dokploy schedule «S3 dokploy-backups-ava» 04:15 | enabled, **команда пустая**, последние 5 запусков 08.09.2025 — `error` | — | — | — | — |
| Dokploy volume-backups | 2 лога от 15.02.2026, больше ничего | — | — | — | — |
| WP UpdraftPlus → Google Drive | 4 сайта (arenasoldout weekly, marinabakanova weekly, lampyris weekly, minimaldeco daily) | Google Drive | 4 / 4 / 4 / 7–14 | да | не подтверждён |
| vinoandco.events | только локальные архивы Updraft (1.5 GB в томе) + ручные дампы `/opt/vino-snap` | тот же VM | — | **нет** | — |
| arena Postgres, mindsethappybot, n8n SQLite (8.3 GB), umami, MACS MongoDB | **нет автоматики** | — | — | — | — |
| Hetzner Snapshots / Backups (опция сервера) | UNKNOWN / NEEDS ACCESS (консоль) | — | — | — | — |
| rclone/aws/mc на хостах | rclone+aws есть на lead-parser (конфиг только у заблокированного `dev`), `mc` на arena-macs — **не используются по cron** | — | — | — | — |

**Вывод: ни один компонент, кроме четырёх WP через Updraft, не восстанавливаем из внешнего источника. Рабочих restore-проверенных бэкапов — ноль.**

### 4.7 Мониторинг

Отсутствует полностью: нет Netdata/Beszel/Uptime Kuma/node_exporter ни на одном хосте, Dokploy-notifications не настроены (0 записей), внешних проверок не обнаружено. Единственный «мониторинг» — Dokploy healthcheck самого себя.

### 4.8 DEV / staging

- `staging.lampyrisevents.com`, `staging.vinoandco.events` — на lampyrisevents, **вне Dokploy** (plain compose в `/opt`), делят RAM с продом.
- `ndarchdesign.andreevmaster.com` — витрина на общем проде.
- `dev-3hours`, `tgtest`, `devnewvinoandco`, `vinoandco.andreevmaster.com` — мёртвые записи.
- Разделения PROD/DEV по хостам нет; все staging живут рядом с продом того же клиента.

---

## 5. Ресурсы, здоровье, причины повторения аварий

| Метрика | lead-parser | arena-macs | minimaldeco | lampyrisevents |
|---|---|---|---|---|
| RAM used / total | 5.1 / 7.75 GB, **swap 2.7/4 GB** | 2.5 / 3.8 GB, swap нет | 2.5 / 3.8 GB, swap 0.75/2 GB | 5.2 / 7.75 GB, swap 0.77/2 GB |
| OOM-kill за 30 дн. | **219** (42× mysqld, 27× systemd, 4× node) | 0 | 48 (mysqld ×5 за 08.09) | 3 |
| Disk `/` | 59/150 GB (41 %) | **32/38 GB (90 %)** | 20/76 GB | 29/76 GB |
| Docker overlay2 / volumes / images reclaimable | 34 GB / 21 GB / **7.7 GB** (+15 dangling volumes, 269 build-cache) | 26 GB / 0.2 GB / **16.6 GB (89 %)** + 3.2 GB build cache | 11 / 7.4 GB / 0 | 16 / 16 GB / 0.8 GB |
| journald | 854 MB | **3.6 GB** | 697 MB | 843 MB |
| Контейнерные json-логи | ограничены 20 MB×3 (`daemon.json`) ✓ | **не ограничены** (mongodb 235 MB) | не ограничены | не ограничены (стеки в /opt — ограничены) |
| Контейнеры с memory-limit | только arena-backend (api/worker/db/redis) | **ни одного** | только dokploy (1.25 GB) | **ни одного** |
| Перезапуски за 48 ч | dokploy ×4 (exit 137), postgres ×4, redis ×4, arena-admin ×5, vino wp_db ×3 | 0 | 0 | dokploy-postgres ×2 |
| Пакеты / reboot | 80 обновл., **reboot required** | 80, **reboot required** | 7 | 7 |

**Почему lead-parser упал по диску в 11.2025 и может ли повториться.** Драйверы роста тогда и сейчас одни и те же: (1) n8n SQLite без prune — уже 8.3 GB и растёт; (2) каждый rebuild кастомного WP-образа vinoandco оставляет «безымянный» слой ~791 MB — их сейчас 20 штук (7.7 GB); (3) 15 dangling-томов и остатки Supabase/Postiz/ES; (4) `/home/dev` 7.1 GB и `/root` 2.2 GB (кэши Claude/Cursor/npm); (5) локальные Updraft-архивы внутри томов. Логи контейнеров с тех пор ограничены — этот вектор закрыт. Автоочистки образов не найдено. **Повторение — вопрос времени, если не включить prune n8n и cleanup образов.**

**Почему падает память на lead-parser.** 4 WP + 6 MySQL + n8n + бот + Dokploy + arena на 7.75 GB без лимитов: MySQL-инстансы (по 230–470 MB), WP vinoandco 1 GB, Dokploy 700 MB, n8n. Ядро убивает то, что крупнее — чаще всего `mysqld` магазина vinoandco. Это уже прямое влияние на продажи, а не риск.

---

## 6. Безопасность

### 6.1 SSH / пользователи

| | lead-parser | arena-macs | minimaldeco | lampyrisevents |
|---|---|---|---|---|
| PasswordAuthentication | **no** ✓ | **no** ✓ | **yes** ✗ | **yes** ✗ |
| PermitRootLogin | without-password | without-password | without-password | without-password |
| Пользователи с shell | root, `andrei` (sudo NOPASSWD, docker), **`codex`** (пустые ключи, создан 02.2026) | root, andrei (sudo) | root | root |
| Заблокированные | `dev` (nologin, ключи пусты; но в `/home/dev` остались `cert_key.pem`, `.aws/config`, `.claude`, `.cursor-server`, 7.1 GB) | — | — | — |
| root authorized_keys | 1 (`ava@geekom`) | **3**: `andreev@abhteam` + **2 без комментария (RSA 3072, ED25519)** — атрибуция неизвестна | 1 | 1 |
| fail2ban / ufw | **нет / нет** | **нет / нет** | да / да | да / да |
| Неудачных SSH за 7 дн. | 19 902 | 17 513 | 23 226 (забанено 8 396) | 27 554 (5 497) |
| Последние входы | ваши IP (79.117.x, 86.127.229.6); `dev` последний раз 13.04.2026 с 161.35.87.209 (инцидент) | ваши; в 06.2025 — 103.137.248.186 (не ваш диапазон, момент установки MACS) | ваши | ваши |

### 6.2 Публичные порты (подтверждено снаружи)

| Сервер | Открыто | Оценка |
|---|---|---|
| lead-parser | 22, 80, 443, **3000 (Dokploy UI без CF)**, **2377, 7946 (Docker Swarm manager/gossip)**, 4789/udp, **18088 (админка бота)**, **9876 (webhook)** | Swarm-порты и UI управления наружу — критично |
| arena-macs | 22, 80, 443, **8080 (Traefik dashboard, `api.insecure=true`, отдаёт `/api/http/routers` анонимно)** | критично |
| minimaldeco | 22, 80, 443, **3000 (Dokploy UI)** | высоко |
| lampyrisevents | 22, 80, 443, **3000 (Dokploy UI)** | высоко |

Origin всех сайтов отвечает напрямую по IP (Cloudflare обходится). Hetzner Cloud Firewall не применён ни к одному серверу (метаданные пусты).

### 6.3 Docker

- `docker.sock` смонтирован в Dokploy (ожидаемо) и в Traefik на arena-macs (`:ro`). Docker API по TCP не открыт нигде ✓.
- Группа `docker`: `andrei` на lead-parser (эквивалент root).
- Образы `wordpress:latest`, `dokploy:latest` (lead-parser), `umami:postgresql-latest` — плавающие теги в проде.
- `uvicorn --reload` в проде MACS.

### 6.4 Секреты (места, без значений)

- **Пароль MongoDB в plaintext** в `/docker/arenasoldout/docker-compose.yml` и **в argv 16 процессов `node /usr/bin/mongodb mongodb://user:pass@91.99.84.144:27017/...`** (виден любому через `ps`) + `npm exec mcp-mongo-server` — rotate.
- Пароль Postgres бота `mindsethappybot` — дефолт `postgres` (из `${POSTGRES_PASSWORD:-postgres}`), БД уже носит ransom-note — rotate.
- `.env` в `/etc/dokploy/compose/*/code/.env` (16 файлов, 0644 root) и `/opt/*/.env`; Dokploy хранит env в своей Postgres.
- `/home/dev/cert_key.pem`, `/home/dev/.aws/config` — учётные данные заблокированного пользователя после инцидента 04.2026 не удалены.
- ACME e-mail стороннего лица в Traefik на arena-macs.
- `acme.json` 0600 ✓; CF Origin CA private key в `/etc/dokploy/traefik/dynamic/certificates/.../privkey.key` с правами **0644** ✗.

---

## 7. Реестр рисков

| # | Риск | Severity | Вероятность | Затронуто | Доказательство | Remediation |
|---|---|---|---|---|---|---|
| R1 | Нет restore-ready бэкапов ни для одного нестатичного компонента (Dokploy, arena PG, n8n, бот, MongoDB, vinoandco.events) | **CRITICAL** | высокая (уже был crash Dokploy PG в 10.2025) | всё | §4.6: `backup` 0 строк на 3 хостах; schedule с пустой командой, error с 09.2025; vinoandco Updraft без remote | Сначала ручные дампы всех БД + тома → внешнее хранилище; затем Dokploy Backups (S3-совместимое: Hetzner Object Storage/Storage Box) + restic для томов; restore-тест на scratch-VM |
| R2 | Хронический OOM на lead-parser убивает MySQL магазина vinoandco.events и сам Dokploy | **CRITICAL** | происходит ежедневно | продажи Vino&Co, управление всеми стеками | §5: 219 OOM/30 дн., 3 kill mysqld сегодня, dokploy exit 137 | Краткосрочно: разгрузить хост (остановить/перенести ndarchdesign, pr-top, бот), задать лимиты; долгосрочно: разделение по Option 2 |
| R3 | Следы компрометации Postgres бота (ransom-note 08.01.2026), дефолтный пароль | **HIGH** | реализован | mindsethappybot, потенциально соседние стеки | §4.3 | Форензика (как был опубликован порт), смена пароля, проверка целостности данных, удалить БД `readme_to_recover` после снятия копии |
| R4 | Swarm-порты 2377/7946/4789 и Dokploy UI :3000 открыты в интернет; на arena-macs — анонимный Traefik dashboard | **HIGH** | средняя | lead-parser, arena-macs, minimaldeco, lampyrisevents | §6.2 | Hetzner Cloud Firewall (22 по allowlist/Tailscale, 80/443 только с CF-диапазонов, остальное deny); убрать публикацию 8080/3000/18088/9876 |
| R5 | Нет firewall/fail2ban на lead-parser и arena-macs при 20k brute-force/нед.; на двух других — PasswordAuthentication yes | HIGH | средняя | все | §6.1 | ufw/Hetzner FW + fail2ban; `PasswordAuthentication no`; `PermitRootLogin prohibit-password`; убрать неатрибутированные ключи |
| R6 | arena-macs: диск 90 %, uptime 471 д, kernel не обновлён, зомби-процессы с паролем Mongo в argv | HIGH | высокая (диск) | MACS/сканирование | §2, §5, §6.4 | Плановое окно: снять дамп Mongo → prune образов/журнала → перезагрузка; rotate пароля Mongo; убить stale-процессы |
| R7 | Cloudflare обходится по IP → WAF/rate-limit бесполезны, origin-IP утекает через sslip.io-домены | MEDIUM | средняя | все сайты | §3, §6.2 | Allowlist CF-диапазонов на 80/443; Authenticated Origin Pulls; отключить sslip/traefik.me домены |
| R8 | Повторение disk-full на lead-parser (n8n SQLite 8.3 GB без prune, 7.7 GB мусорных образов, 15 dangling-томов) | MEDIUM | высокая в горизонте месяцев | lead-parser целиком | §5 | `EXECUTIONS_DATA_PRUNE=true` + `EXECUTIONS_DATA_MAX_AGE`, перевод n8n на Postgres, Dokploy docker cleanup, алерт на disk |
| R9 | Один хост = control plane + все клиентские сайты + n8n + arena API (blast radius) | HIGH | — | всё на lead-parser | §2 | Option 2 |
| R10 | Плавающие теги (`wordpress:latest`, `dokploy:latest`, `umami:*-latest`), `uvicorn --reload` в проде | MEDIUM | средняя | WP, Dokploy, MACS | §6.3 | Пиннинг версий; отдельный prod-Dockerfile MACS |
| R11 | Нет мониторинга и алертов вообще | HIGH | — | всё | §4.7 | Uptime Kuma + Beszel (или Netdata) + Telegram; алерты на disk/RAM/OOM/backup |
| R12 | Мёртвые записи Dokploy/DNS (Supabase, Postiz, TgTicketAgent, dev-боты, WP 84umlx/61uqee) и мёртвый cron | LOW | — | чистота, риск случайного деплоя | §3, §4.4 | Инвентаризовать → удалить после approval |
| R13 | Одинаковый hostname `debian-4gb-fsn1-1` у двух серверов | LOW | — | логи/алерты | §2 | Переименовать при миграции |
| R14 | Staging-сайты и prod того же клиента на одном хосте вне Dokploy | MEDIUM | средняя | lampyrisevents | §4.8 | Отдельный staging-хост или жёсткие лимиты; завести staging в Dokploy |
| R15 | Учётные данные заблокированного `dev` остались на диске; пользователь `codex` с shell | MEDIUM | низкая | lead-parser | §6.1 | Удалить/архивировать `/home/dev`, отозвать AWS-ключ, удалить `codex` если не нужен |
| R16 | Legacy-цены теряются при rescale; новые CPX в 2 раза дороже | FINANCIAL | — | lead-parser, arena-macs | §8 | Не rescale-ить; переносы делать через новые CX-серверы |

---

## 8. Что из брифа больше не существует, и что является SPOF

**HISTORICAL / NO LONGER PRESENT:** сервер `ubuntu-8gb-nbg1-1`; Ollama/Open-WebUI; Supabase, Postiz, LightRAG, Crawl4AI как работающие сервисы (записи остались); WordPress `asoconnector-wordpress-84umlx`/`61uqee` и bind-mount `/home/dev/wp-dev/vinoandco` как рабочая схема; dev-контур бота; TgTicketAgent; отдельный сервер сканирования (его никогда не было — это arena-macs); публично открытый MongoDB `:27017` (закрыт — CONFIRMED FIXED); IP `89.167.108.178` для nordicmart (host key сменился, текущий сервер — 135.181.94.96).

**UNKNOWN / NEEDS ACCESS:** точные типы и цены серверов (консоль Hetzner/счета); включены ли Hetzner Backups/Snapshots; содержимое GCS-бакета `dokploy-backups-ava`; настройки Cloudflare (SSL mode Full (Strict)?, кэш-правила для `/wp-admin`, `/cart`, `/checkout`, webhook-путей); что именно и с каких IP публиковало Postgres бота в 01.2026.

**Single points of failure сегодня:**
1. **lead-parser** — control plane Dokploy + 4 сайта + arena API + n8n + бот + pr-top. Падение = всё, включая возможность что-либо задеплоить.
2. **Dokploy Postgres на lead-parser** без бэкапа — потеря = потеря всех env/доменов/конфигов 17 стеков (уже случалось).
3. **arena-macs** — единственный экземпляр MACS и его MongoDB, 4 GB свободного диска.
4. **Google Drive** одного аккаунта — единственный внешний бэкап WP.
5. **Один SSH-ключ на человека без второго канала** (нет Tailscale/console-доступа на Hetzner-хостах, кроме веб-консоли).

---

## 9. Legacy CX22 как Dokploy control plane — вердикт

**RECOMMENDATION: подходит, при условиях.** Dokploy (≈700–900 MB) + его Postgres (≈50 MB) + Traefik (≈60 MB) укладываются в 2 vCPU/4 GB с запасом; трафик клиентских сайтов через control plane не идёт (workers держат свои Traefik). Экономически выигрыш невелик (CX22 legacy ≈ €4.49 против нового CX23 €5.49), но и потерять нечего.

Обязательные предпосылки (все — изменения, требуют отдельного approval):
1. Перенести MACS + MongoDB (см. план) и снять полный дамп Mongo до любых действий.
2. Освободить диск: 16.6 GB мусорных образов, 3.2 GB build cache, 3.6 GB journald, `/root/.npm` 1.1 GB, `.vscode-server` 1 GB — сейчас 4 GB свободно, Dokploy-образ один занимает 3.1 GB.
3. Плановая перезагрузка (471 день, старое ядро).
4. Удалить два неатрибутированных root-ключа, завершить stale-процессы `claude`/`node mongodb`, rotate пароля Mongo.
5. Закрыть `:8080`, поставить Hetzner Firewall; **не делать rescale** (потеря legacy-цены).
6. Dokploy control plane сам по себе тоже нуждается в бэкапе своей Postgres наружу — это условие вне зависимости от хоста.

Альтернатива, если MACS решено оставить на CX22 навсегда: control plane ставить на новый CX23 (€5.49) — разница €1/мес, но MACS не трогаем.

---

## 10. Целевая архитектура — два варианта

Цены Hetzner (net, EUR/мес, для **новых заказов с 15.06.2026**; существующие серверы сохраняют старую цену, пока их не rescale-ить): CX23 (2/4/40) €5.49; CX33 (4/8/80) €8.49; CX43 (8/16/160) ≈ €16 (точную цифру страница не показала — **уточнить**); CPX22 (2/4/80) €19.49; CPX32 (4/8/160) €35.49. На момент проверки страница Hetzner помечала CX23–CX53 как «not available» (нет в наличии) — **наличие надо проверить в консоли перед планированием**; альтернатива с той же ценой — ARM CAX (CAX21 4/8/80, CAX31 8/16/160), но тогда образы должны быть multi-arch (Dokploy, WP, MySQL, n8n — есть; кастомные образы MACS/бота — проверить).

Текущая оценка расходов (уточнить по счёту): lead-parser CPX31 legacy ≈ €17.49 + arena-macs CX22 legacy ≈ €4.49 + minimaldeco CPX22 ≈ €19.49 + lampyrisevents CX33 ≈ €8.49 ≈ **€50/мес**, без бэкапов и мониторинга.

### Option 1 — минимальная стоимость, максимум существующих серверов

| Сервер | Тип / цена | Роль |
|---|---|---|
| arena-macs (CX22 legacy) | €4.49 | **Dokploy control plane** (после переноса MACS) |
| lead-parser (CPX31 legacy, 8 GB) | €17.49 | **Apps + n8n worker**: arena-backend, n8n (на Postgres), mindsethappybot, pr-top + umami, **MACS (с лимитами)**; WordPress убрать |
| lampyrisevents (CX33, 8 GB) | €8.49 | **WordPress worker**: lampyrisevents, vinoandco.events, arenasoldout.com, marinabakanova, ndarchdesign — с одним общим MariaDB (отдельные БД/пользователи) вместо 5 MySQL |
| minimaldeco (CPX22, 4 GB) | €19.49 | остаётся как есть (изолированный тяжёлый Woo) **или** заменяется на CX33 €8.49 с бо́льшими ресурсами (−€11) |
| Hetzner Storage Box BX11 (1 TB) | ≈ €3.81 | restic/rclone-бэкапы + Dokploy backups (через S3-совместимый Object Storage ≈ €5 или SFTP на Storage Box) |
| **Итого** | **≈ €54–58/мес** (≈ €43–47 при замене minimaldeco на CX33) | |

Плюсы: 0–1 новый сервер, control plane отделён, WP отделён от n8n/AI. Минусы: MACS и n8n делят хост с arena API; 5 WP на 8 GB — впритык (сейчас 3 WP на этом хосте уже дают 5.2 GB + swap), обязательны лимиты и общий MariaDB; staging для vinoandco/lampyris придётся выключать вне событий или держать на том же WP-хосте. Сложность миграции: средняя. Надёжность: средняя (SPOF по-прежнему lead-parser для всех apps).

### Option 2 — рекомендуемая: control / WordPress / Apps+n8n / scanning

| Сервер | Тип / цена | Роль |
|---|---|---|
| arena-macs (CX22 legacy) | €4.49 | **Control plane**: Dokploy UI/API, Dokploy Postgres с бэкапом, Uptime Kuma + Beszel hub. Никаких клиентских нагрузок |
| **Новый CX43** (8/16/160) | ≈ €16 (уточнить) | **WP worker «heavy»**: minimaldeco, vinoandco.events, lampyrisevents (три WooCommerce/WPML) — MariaDB общий с лимитами, Redis, лимиты на каждый WP |
| lampyrisevents (CX33, 8 GB) | €8.49 | **WP worker «light» + staging**: arenasoldout.com, marinabakanova, ndarchdesign, staging.lampyris, staging.vino (staging с лимитами и остановкой по расписанию) |
| lead-parser (CPX31 legacy, 8 GB) | €17.49 | **Apps + n8n worker**: arena-backend (api/worker/pg/redis), n8n на Postgres + Redis (queue mode при росте), mindsethappybot, pr-top, umami, будущие боты/парсеры. Переустановка ОС на месте после выноса WP (без rescale) |
| MACS / сканирование | вариант A: остаётся на Apps-worker с лимитами (€0); вариант B: **CX23 €5.49 «событийный»**: snapshot (≈ €0.5/мес за 40 GB) → удалить → воссоздать перед событием, MongoDB восстанавливать из бэкапа; DNS macs.* через CF переключается за минуты | €0–5.49 |
| minimaldeco (CPX22) | **удалить после миграции** | −€19.49 |
| Hetzner Object Storage (S3) 1 TB | ≈ €5 | Dokploy backups (все Postgres/MySQL), restic-снимки томов, wp-content |
| **Итого** | **≈ €52–57/мес** | практически то же, что сейчас, но с изоляцией, бэкапами и мониторингом |

Плюсы: падение любого worker не трогает остальные и не лишает управления; WP не конкурируют с n8n/AI; клиентский сайт переносится независимо; новые WP добавляются как compose-шаблон на нужный worker. Минусы: 1 новый сервер, миграция в 4 этапа, нужен S3. Надёжность: высокая для этой цены. Сложность: средняя-высокая, но каждый шаг откатываем.

Общие требования к обоим вариантам: Hetzner Cloud Firewall на каждом сервере (22 только с ваших IP/Tailscale, 80/443 только с диапазонов Cloudflare, 3000 нигде); Dokploy подключает workers как remote servers по SSH; `PasswordAuthentication no` везде; лимиты memory/cpu у каждого контейнера; `daemon.json` с log-opts; pin версий; Cloudflare Full (Strict) + Authenticated Origin Pulls; n8n → Postgres + prune; мониторинг disk/RAM/HTTP/TLS/backup с Telegram-алертами.

---

## 11. Последовательный план миграции (каждый шаг — только после approval)

**Фаза 0 — стабилизация без миграции (1–2 дня).**
1. Ручные дампы **всего** прямо сейчас: Dokploy PG ×3, arena PG, mindsethappybot PG, umami PG, 7 MySQL, MongoDB (`mongodump`), n8n `database.sqlite` (стоп-копия или `.backup`), `/etc/dokploy` ×3, `/docker` на arena-macs, `/opt/*` на lampyrisevents/lead-parser → на acer-server через Tailscale (405 GB свободно) и/или Storage Box. Проверить, что Updraft-архивы в Google Drive реально существуют и открываются.
2. На lead-parser снять давление: остановить мёртвый cron `84umlx`, включить `EXECUTIONS_DATA_PRUNE` у n8n, задать лимиты MySQL (`innodb_buffer_pool_size`) и memory-limit контейнерам — по одному, с наблюдением.
3. Hetzner Cloud Firewall на 4 серверах (сначала в режиме «allow all + логи», затем deny) — закрыть 2377/7946/3000/8080/18088/9876.
4. Rotate: пароль Mongo, пароль Postgres бота; убрать неатрибутированные ключи на arena-macs; `PasswordAuthentication no` на minimaldeco/lampyrisevents.

**Фаза 1 — бэкап-контур и restore-тест (2–3 дня).**
5. Заказать Object Storage/Storage Box; настроить Dokploy Backups для всех БД (cron, retention 14/30); restic для томов (`wp_app`, uploads, n8n, mongo) с ежедневным cron и алертом при ошибке.
6. **Restore-тест**: поднять временный CX23, восстановить из бэкапа один WP (ndarchdesign) и Dokploy PG, убедиться, что сайт открывается по sslip-домену. Зафиксировать процедуру в runbook. Удалить временный сервер.

**Фаза 2 — подготовка целевых хостов (1–2 дня).**
7. Заказать CX43 (или CAX31), базовый hardening (ключи, ufw/Hetzner FW, fail2ban, unattended-upgrades, Tailscale), Docker с `daemon.json`, подключить как remote server к текущему Dokploy на lead-parser (control plane переезжает последним).
8. Cloudflare: проверить SSL mode Full (Strict), cache-rules bypass для `/wp-admin`, `/wp-login.php`, `/cart`, `/checkout`, `/my-account`, `/wp-json`, `/?wc-ajax`, webhook-пути n8n и `api.arenasoldout.com`; включить Authenticated Origin Pulls после переноса.

**Фаза 3 — пилот на низкорисковом сайте.**
9. ndarchdesign (витрина, 4.5 MB БД) → light-WP worker: dump+rsync томов → поднять → проверить по sslip → переключить origin в CF (proxy on, TTL не важен) → 24 ч наблюдения. Rollback = вернуть origin-IP в CF.

**Фаза 4 — WordPress по одному (по 1 сайту в день, в окно низкого трафика).**
10. Порядок: marinabakanova → arenasoldout.com → lampyrisevents (уже на своём хосте — только перевод в общий MariaDB и лимиты) → vinoandco.events (заморозить заказы на 15 мин, финальный dump, rsync uploads, переключить CF, проверить checkout/PayPal/AllPay/bil24-синк) → minimaldeco (аналогично, Stripe/Baselinker). Для каждого: pre-dump, post-verify чек-лист (главная, категория, товар, корзина, checkout test-mode, wp-admin, cron, письма), rollback через CF origin.

**Фаза 5 — Apps/n8n.**
11. n8n: экспорт workflows/credentials, миграция SQLite → Postgres (официальная процедура), проверка Telegram-триггеров и webhook URL (не меняются). Бот, pr-top, umami — как compose-переносы. arena-backend: окно 10–15 мин, `pg_dump`/restore, проверка `/health`, MACS-хуков и Bil24-шлюза.

**Фаза 6 — MACS.**
12. Согласовать с календарём событий (2–3 дня в месяц). `mongodump` → перенос на Apps-worker или на «событийный» CX23 → переключить `macs.arenasoldout.com` → тест сканеров/offline-режима до события. Только после этого CX22 освобождается.

**Фаза 7 — control plane.**
13. Dokploy на CX22: чистая установка, восстановление Dokploy PG из бэкапа (или re-регистрация remote servers), перевод `app.andreevmaster.com`. Старый Dokploy на lead-parser останавливается только после проверки деплоя на каждом worker.

**Фаза 8 — decommission.**
14. Удалить minimaldeco CPX22 (после 7 дней стабильной работы на новом месте и финального снапшота), почистить мёртвые Dokploy-записи/DNS/тома, `/home/dev`, stale-образы. Переустановить ОС на lead-parser без rescale (сохранить цену) и отдать под Apps-worker, если решено делать чистую базу.

Rollback на каждом шаге: origin-IP в Cloudflare возвращается на старый хост; старые контейнеры не удаляются до конца фазы 8; дампы «до» хранятся минимум 30 дней.

---

## 12. «Не менять до отдельного approval»

- Legacy-серверы lead-parser (CPX31) и arena-macs (CX22): **не rescale, не удалять, не менять тип** — потеря legacy-цены.
- MACS/MongoDB на arena-macs — не трогать до согласования с календарём событий и дампа.
- vinoandco.events, lampyrisevents.com, minimaldeco.es — продакшн-магазины: любые изменения только в окно и после дампа.
- Cloudflare DNS/SSL-режимы, Traefik-конфиги, `acme.json` — только по плану миграции.
- `readme_to_recover` — не удалять до форензики и копии.
- `/home/dev`, `/opt/vino-snap`, `/root/mb_backups`, локальные SQL-дампы в `/root` на minimaldeco — не удалять, пока не подтверждён внешний бэкап.
- Docker prune / удаление образов, томов, dangling — только после фазы 1.
- Ключи в `authorized_keys` — не удалять до подтверждения владельцев (два ключа на arena-macs).

---

## 13. Открытые вопросы к владельцу

1. Подтвердить в консоли Hetzner типы/цены всех четырёх серверов и наличие Backups/Snapshots; какой сервер в консоли называется «Ava-dokploy».
2. Чьи два безымянных root-ключа на arena-macs (RSA 3072 и ED25519) и ключ `ava@geekom` на lead-parser (это ваш geekom?).
3. Нужны ли ещё: Supabase, Postiz, LightRAG, Crawl4AI, TgTicketAgent, dev-контур бота, ndarchdesign, pr-top (не обновлялся с июля).
4. MACS: кто владелец кода (образы из GitLab третьей стороны), есть ли исходники/CI, можно ли пересобрать без `--reload`.
5. Google-аккаунт для UpdraftPlus и GCS-бакет `dokploy-backups-ava` — доступ есть? Что в бакете?
6. Cloudflare: сколько аккаунтов/зон, включён ли Full (Strict), есть ли page rules для WooCommerce.
7. Согласие на Hetzner Object Storage/Storage Box (≈ €4–5/мес) как единое место бэкапов.

---

Источники по ценам: [Hetzner Docs — Price Adjustment 15 June 2026](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/), [Hetzner Cloud — Cost-Optimized plans](https://www.hetzner.com/cloud/cost-optimized/), [Hetzner Cloud — Regular Performance plans](https://www.hetzner.com/cloud/regular-performance/).
