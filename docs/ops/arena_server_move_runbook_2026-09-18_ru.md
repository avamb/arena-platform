# Runbook: перенос Arena Platform на новый чистый сервер

Дата: 18.09.2026. Решение владельца: Arena уезжает с lead-parser на отдельный новый сервер **до** реальных тестов и до переключения сайтов. Собственные WordPress-сайты — на второй новый сервер. Vino&Co как инфраструктуру на этой неделе не трогаем.

## 0. Что уже сделано (18.09, 09:33 UTC)

- Страховочный дамп базы: `/root/arena-backups/arena_20260918-093317.dump` на lead-parser и копия `C:\Users\andre\arena-backups\` (sha256 начинается с `9b1b91f6f4373f15`, совпадает). Проверка `pg_restore -l`: 929 объектов, 88 таблиц с данными; в базе 2 организации, 9 заказов, 8 билетов, миграции до 101.
- Архив медиа-тома (548 КБ) там же.
- SSH-ключ для нового сервера: `~/.ssh/arena_prod_hetzner` (fingerprint `SHA256:/uNqMokkzsbLNzKCewRuj92TnjF/XjhygGge5tiCKw4`).
- Скрипт подготовки сервера: `ops/server-bootstrap/bootstrap.sh`.
- Состав стека зафиксирован: `db` (postgres:17-alpine, 1 ГБ), `redis` (7-alpine, AOF, 192 МБ), `migrate` (one-shot), `api` и `worker` (`ghcr.io/avamb/arena-api:99c64f1`, по 512 МБ), тома `arena-db-data`, `arena-redis-data`, `arena-media`; плюс приложение `admin` (сборка из Git). Секреты живут только в Raw-compose панели Dokploy и переносятся оттуда же, в репозиторий не попадают.

## 1. Заказ сервера (владелец, консоль Hetzner Cloud)

| Параметр | Значение |
|---|---|
| Тип | **CX33** (4 vCPU / 8 ГБ / 80 ГБ). Обязательно x86 (линейка CX или CPX), **не CAX**: образ arena-api собран только под amd64 |
| Локация | Falkenstein (fsn1) — там же, где серверы сайтов, минимальная задержка шлюза |
| Образ | Ubuntu 24.04 |
| Имя | `arena-prod-1` |
| SSH key | публичный ключ `arena-prod-hetzner` (ниже) |
| Backups | включить (+20 %, ≈ €1.70/мес) |
| Сеть | IPv4 + IPv6, без volume |

```
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDZMfDhrF9hkdkRN4CSttJB9YP6ek9ZGordcMQEyDJrA arena-prod-hetzner
```

**Hetzner Cloud Firewall `arena-prod`** (создать и привязать к серверу): входящие TCP 22 — с `78.46.176.249` (Dokploy на lead-parser) и с текущего IP владельца; TCP 80, TCP 443, UDP 443 — только с диапазонов Cloudflare (список: cloudflare.com/ips); всё остальное закрыто. Сайты клиентов ходят в API по доменному имени через Cloudflare, поэтому отдельных правил для них не нужно.

Второй такой же сервер `wp-own-1` (CX33, fsn1, Ubuntu 24.04, тот же ключ или отдельный) — под собственные сайты; заказывается сразу, настраивается после Arena.

## 2. Подготовка сервера (я, после получения IP)

1. Алиас `arena-prod` в `~/.ssh/config` с ключом `arena_prod_hetzner`.
2. `scp ops/server-bootstrap/bootstrap.sh` → `bash bootstrap.sh arena-prod-1`: ключи-only SSH, ufw, fail2ban, unattended-upgrades, swap 2 ГБ, Docker с лимитом логов, journald 300 МБ.
3. Проверки: `sshd -T` (password no), `ufw status`, `docker info`, исходящий SMTP `nc -vz smtp-relay.brevo.com 587`.

## 3. Подключение к Dokploy (владелец логинит меня в панель app.andreevmaster.com)

4. Settings → SSH Keys → создать ключ `arena-prod-1`; его публичную часть — в `/root/.ssh/authorized_keys` нового сервера.
5. Settings → Servers → Add Server (IP, root, порт 22, этот ключ) → Setup Server (ставит Traefik и сеть `dokploy-network`). Проверить, что Traefik слушает только 80/443 и что порт 3000 на этом сервере не открыт.

## 4. Развёртывание стека на новом сервере (старый продолжает работать)

6. В проекте `arena` создать новый Compose `backend-prod` с целевым сервером `arena-prod-1`, провайдер Raw; вставить текущий Raw-compose **без изменений секретов** (`JWT_SIGNING_SECRET`, `MEDIA_SIGNING_SECRET`, пароль БД остаются прежними, иначе сломаются выданные токены и подписанные ссылки на афиши). Добавить в `db` и `api` строку `oom_score_adj: -500`.
7. Домен `api.arenasoldout.com` → сервис `api:8080`, сертификат Let's Encrypt. До переключения DNS сертификат не выпустится — это ожидаемо.
8. Deploy. Убедиться: `db` healthy, `migrate` exit 0, `api`/`worker` healthy.
9. Приложение `admin-prod` на том же сервере: Git `avamb/arena-platform` master, `apps/admin-web/Dockerfile`, build-arg `VITE_API_BASE_URL=https://api.arenasoldout.com`, домен `app.arenasoldout.com:8080`.

## 5. Окно переключения (≈ 15 минут; реальных продаж ещё нет)

10. На lead-parser остановить `api` и `worker` старого стека (запись в базу прекращается). `db` оставить.
11. Финальный дамп старой базы (`pg_dump -Fc`) + архив медиа → копия на новый сервер.
12. На новом сервере: остановить `api`/`worker`, `dropdb arena` → `createdb arena` → `pg_restore -d arena`, распаковать медиа в том `arena-media`, запустить `api`/`worker`.
13. Проверка в обход DNS (с моей машины): `curl -k --resolve api.arenasoldout.com:443:<новый IP> https://api.arenasoldout.com/readyz`; команда шлюза `GET_ALL_ACTIONS` для fid 2 — число мероприятий и цены совпадают со старым сервером; счётчики `organizations/orders/tickets/schema_migrations` совпадают с дампом.
14. **Brevo**: владелец добавляет новый IP в Authorized IPs (иначе письма перестанут уходить).
15. **Cloudflare**: A-записи `api.arenasoldout.com` и `app.arenasoldout.com` → новый IP, proxy включён. Traefik выпускает Let's Encrypt за 10–30 секунд; в этот промежуток при режиме Full (strict) возможен ответ 526 — укладывается в окно. Вариант без промежутка: заранее выпустить Cloudflare Origin CA для `*.arenasoldout.com` и поставить в Dokploy как custom-сертификат.
16. Публичные проверки: `/healthz`, `/readyz`, вход в админку, письмо сброса пароля доходит, повторный event-bundle под API-ключом → 200, вебхук `event.created` доходит до staging Lampyris, тестовая покупка со staging Lampyris, доставка в MACS (нет dead-letter в `outbox_events`).

## 6. Сразу после переключения

17. Ночной cron на новом сервере: `pg_dump -Fc` + архив медиа в `/var/backups/arena`, хранить 14 дней; до настройки Google Drive — ежедневно забирать копию на внешнюю машину. Первый restore-тест — в течение недели.
18. Старый стек на lead-parser: `db` и `redis` остановить, **тома не удалять 14 дней**. Старые Dokploy-сервисы `backend` и `admin` не удалять, только остановить.
19. Обновить `AGENTS.md`/память: стенд = `arena-prod-1`, рецепт деплоя тот же (Raw compose → тег ×4 → Save → Deploy), сервис `backend-prod`.

## 7. Откат

До появления реальных продаж: вернуть A-записи в Cloudflare на `78.46.176.249`, запустить старые `api`/`worker`. Данные, записанные на новом сервере после переключения, при откате теряются — поэтому реальные тесты и импорт мероприятий начинаются только после п. 16.

## 8. Известные подводные камни

- Brevo пускает SMTP только с разрешённых IP (п. 14).
- Сборка админки требует ~1.5 ГБ RAM — на CX33 проходит, swap 2 ГБ добавлен скриптом.
- Порты, опубликованные контейнерами, обходят ufw. Наружу публикует только Traefik (80/443); внешний слой — Hetzner Cloud Firewall.
- `TRUSTED_PROXY_COUNT=1` сохраняется; Traefik без `forwardedHeaders.trustedIPs` видит IP края Cloudflare, а не покупателя — лимит по IP проверить на реальных тестах (план недели, шаг 13).
- In-process кэш прав API: после ручных правок `role_permissions` нужен рестарт `api`.
