# Бэкап и восстановление Arena и MACS: как устроено на 2026-10-02

Эта инструкция описывает то, что **реально работает на боевом сервере**, и пошагово говорит, что делать,
когда что-то сломалось. Общий, более ранний документ `deploy/BACKUP_RESTORE_RUNBOOK.md` написан в июне
под гипотетическую схему (другие имена скриптов, WAL-архив, который не включён). Для реальной аварии
пользуйтесь этим файлом, а тот оставьте как справочник по WAL/PITR.

- Боевой сервер: `arena-prod`, Hetzner `arena-platform-prod-1`, IP `178.105.217.195`, подключение `ssh arena-prod`.
- Время в расписании указано по **UTC**. Мадрид сейчас UTC+2, с 25 октября 2026 года UTC+1.
- Все команды ниже выполняются **на сервере** по SSH, если не сказано иное.
- В файле нет ни одного секрета. Где лежат ключи, сказано в разделе 3.

## 1. Коротко

| Что | Когда (UTC) | Куда | Сколько хранится |
|---|---|---|---|
| Дамп Postgres Arena | каждые 3 часа, в :05 | `/var/backups/arena-3h` и сразу в R2 `arena-postgres-3h/` | 3 суток |
| Ночной дамп Postgres и архив медиа | 02:15 | `/var/backups/arena` | 14 дней |
| Дамп MongoDB (MACS) | 03:30 | `/opt/macs/backup` | 14 дней |
| Копия ночных дампов и MACS в R2 | 03:45 | R2 `arena-postgres/`, `arena-media/`, `macs-mongo/` | 30 дней |
| Суточная проверка всего | 04:15 | тревога в чат поддержки Telegram | |
| Почасовая проверка 3-часового дампа | каждый час в :35 | тревога в чат поддержки Telegram | |
| Зашифрованный архив секретов (compose и `.env`) | 03:50 | `/var/backups/secrets` и R2 `secrets-encrypted/` | 14 дней на сервере, 90 дней в R2 |
| Образы и исходники MACS | один раз 02.10.2026 | R2 `macs-images/`, `macs-sources/` | без срока |

- **Максимальная потеря данных (RPO):** до 3 часов при потере сервера или базы.
- **Время восстановления (RTO), оценка, не измерена:** база на живом сервере 15–30 минут, сервер целиком 2–4 часа.
  Измерьте на первых учениях (раздел 11) и поправьте цифры здесь.
- Redis бэкапить не нужно (раздел 9).
- Хранилище вне сервера: Cloudflare R2, бакет `arena-platform`, юрисдикция EU, класс Standard.

Что делать, если случилось:

| Ситуация | Раздел |
|---|---|
| Пришла тревога «BACKUP PROBLEM» | 4 |
| База испорчена или удалены данные, сервер жив | 5 |
| Сервер потерян или не загружается | 6 |
| Пропали афиши и логотипы | 7 |
| Сломался MACS | 8 |
| Нужно понять, какие продажи потерялись | 10 |

## 2. Что где лежит

| Набор | На сервере | В R2 (`r2:arena-platform/…`) |
|---|---|---|
| Postgres каждые 3 часа | `/var/backups/arena-3h/arena_3h_ГГГГММДД-ЧЧММ.dump` | `arena-postgres-3h/` |
| Postgres ночной | `/var/backups/arena/arena_ГГГГММДД-ЧЧММСС.dump` | `arena-postgres/` |
| Медиа (афиши, логотипы) | `/var/backups/arena/media_ГГГГММДД-ЧЧММСС.tgz` | `arena-media/` |
| MongoDB MACS | `/opt/macs/backup/mongo_daily_ГГГГММДД_ЧЧММ.archive.gz` | `macs-mongo/` |
| Секреты (compose и `.env` Arena, `.env` MACS, настройки Traefik), зашифрованы | `/var/backups/secrets/arena_secrets_ГГГГММДД-ЧЧММ.tar.gz.age` | `secrets-encrypted/` |
| Образы Docker MACS | только `docker images` на сервере | `macs-images/macs_images_20261002.tar.gz` |
| Исходники MACS | были на старом сервере `arena-macs` в `/docker/arenasoldout` | `macs-sources/macs_sources_20261002.tar.gz` |

Дампы Postgres сняты командой `pg_dump -Fc` (сжатый формат), восстанавливаются через `pg_restore`.
Дамп MongoDB: `mongodump --archive --gzip` базы `arenasoldout`.

Сами скрипты и задания cron лежат в репозитории в `ops/backup/` (раздел 13), compose MACS в `ops/macs/`.

## 3. Доступы и где что хранится

**Cloudflare R2 (копия вне сервера).**
- Бакет `arena-platform`, EU. Адрес подключения: `https://<номер-аккаунта>.eu.r2.cloudflarestorage.com`
  (именно с `.eu.`, адрес без него для этого бакета не работает).
- Ключ: токен `arena-backups` в Cloudflare → R2 Object Storage → Manage API Tokens. Права `Object Read & Write`,
  только на бакет `arena-platform`. Если ключ потерян или скомпрометирован, создайте новый токен с теми же
  параметрами, обновите `rclone.conf` на сервере и удалите старый токен.
- На сервере: `/root/.config/rclone/rclone.conf` (права 600, только root). Шаблон:

```ini
[r2]
type = s3
provider = Cloudflare
access_key_id = <Access Key ID>
secret_access_key = <Secret Access Key>
endpoint = https://<номер-аккаунта>.eu.r2.cloudflarestorage.com
acl = private
no_check_bucket = true
```

- У владельца копия значений лежит в файле `api-token-r2-backup.txt` в рабочем каталоге проекта (он в `.gitignore`).
  Держите значения также в менеджере паролей.
- Проверка, что ключ работает: `rclone lsl r2:arena-platform/arena-postgres-3h | tail -3`.
- Старая версия rclone (1.60) при первой записи в R2 пишет «501 NotImplemented» и повторяет запись сама. Это норма.

**Боевые секреты Arena** (ключ подписи JWT, секрет вебхука Stripe, пароль почты, токены Telegram, ключ подписи медиа и т.д.)
лежат в `/etc/dokploy/compose/arena-backend-prod-xy5zqo/code/docker-compose.yml` и `.env` рядом, а также в панели Dokploy.
Копии хранятся **в двух местах** (решение владельца 02.10.2026):

1. **Менеджер паролей владельца**: защищённая заметка с содержимым compose и `.env` проекта (из панели Dokploy, вкладка окружения проекта
   `arena-backend-prod`) и `.env` MACS. Обновлять при каждом изменении секретов.
2. **Зашифрованный архив в R2** (`secrets-encrypted/`), обновляется каждую ночь в 03:50 UTC скриптом `secrets-backup.sh`. Шифрование открытым ключом
   `age`, поэтому сервер и R2 прочитать архив не могут. **Закрытый ключ** хранится только в менеджере паролей владельца (запись «Arena backup age private key»), на сервере его быть не должно.

Открытый ключ (секретом не является, лежит на сервере в `/etc/backup-age-recipient.txt`):

```
age14meanax6yr474lnvapldtq2jcgm2w7ytgg952v4ypzzrg6g0gatqhnnmpf
```

В архиве: `docker-compose.yml` и `.env` Arena, `traefik.yml` и `middlewares.yml`, `.env`, `docker-compose.yml` и `init-user.js` MACS. Не входят: ключ R2
(он нужен, чтобы вообще достать архив, храните его отдельно в менеджере паролей) и `acme.json` Traefik (сертификаты Let's Encrypt выдаются заново).

**Как расшифровать архив.** Нужны программа `age` (на Windows: `winget install FiloSottile.age`, на Ubuntu: `apt install age`) и закрытый ключ из менеджера
паролей, сохранённый во временный файл `key.txt` (удалите его после работы).

```bash
rclone lsl r2:arena-platform/secrets-encrypted | tail -3
rclone copyto r2:arena-platform/secrets-encrypted/arena_secrets_ГГГГММДД-ЧЧММ.tar.gz.age secrets.tar.gz.age
mkdir -p secrets-restore && age -d -i key.txt secrets.tar.gz.age | tar xz -C secrets-restore
```

В каталоге `secrets-restore` пути повторяют серверные (`etc/dokploy/...`, `opt/macs/...`). Проверка без показа секретов: `age -d -i key.txt secrets.tar.gz.age | tar tz`.

**Если закрытый ключ потерян**, архив прочитать нельзя. Тогда создайте новую пару на сервере (`age-keygen -o /root/backup-age-private.txt`, открытую часть
запишите командой `age-keygen -y /root/backup-age-private.txt > /etc/backup-age-recipient.txt`), **сразу заберите закрытый файл в менеджер паролей и удалите его с сервера**,
запустите `/usr/local/bin/secrets-backup.sh`. Старые архивы останутся нечитаемыми.

**MACS:** пароли MongoDB только в `/opt/macs/.env` (права 600). Compose в репозитории их не содержит.

**Telegram-тревоги:** скрипты берут токен служебного бота и номер чата поддержки из окружения контейнера
`arena-backend-prod-xy5zqo-worker-1` (переменные `OPS_TELEGRAM_*`) в момент отправки. Продажный бот не используется.

## 4. Как понять, что бэкапы живы

Раз в неделю можно просто посмотреть, что в понедельник пришла сводка «Weekly backup summary» в чат поддержки.
Если не пришла, проверка сама не работает: смотрите журналы ниже.

```bash
tail -3 /var/log/arena-pg-3h.log          # 3-часовой дамп
tail -3 /var/log/arena-backup.log         # ночной (пустой журнал = нет ошибок)
tail -3 /var/log/macs-backup.log          # MACS
tail -8 /var/log/offsite-backup.log       # копия в R2
tail -5 /var/log/backup-check.log         # проверки
ls -lh /var/backups/arena-3h | tail -4
rclone lsl r2:arena-platform/arena-postgres-3h | tail -3
```

Что означает тревога и что делать:

| Текст тревоги | Что делать |
|---|---|
| `no fresh 3-hourly Postgres dump on the server` | `tail /var/log/arena-pg-3h.log`. Запустить вручную `/usr/local/bin/arena-pg-3h.sh`. Проверить контейнер `docker ps \| grep db-1` и свободное место `df -h /` |
| `no fresh 3-hourly Postgres dump in R2` | Дамп есть, но не загрузился. `rclone lsl r2:arena-platform/arena-postgres-3h` покажет, работает ли ключ. Если нет: раздел 3, перевыпуск ключа |
| `Arena Postgres dump is older than 26h` / `media archive` | `/usr/local/bin/arena-backup.sh` вручную, смотреть вывод, затем `/var/log/arena-backup.log` |
| `MACS MongoDB dump is older than 26h` | `/opt/macs/backup.sh` вручную. Проверить `docker ps \| grep macs-mongodb` |
| `R2 has no fresh copy in …` | `/usr/local/bin/offsite-backup.sh` вручную, затем `tail /var/log/offsite-backup.log` |
| `the last off-site run reported FAILED` | то же, смотреть `/var/log/offsite-backup.log.rclone` |
| `disk is …% full` | `docker system df`, `docker image prune`, `docker builder prune`. Старые дампы чистятся сами |
| `Backup recovered` | Всё в порядке, ничего делать не нужно |

Если тревог нет совсем, а вы сомневаетесь, что бэкап живой: пройдите учения (раздел 11).

## 5. Сценарий A: база Postgres испорчена или удалены данные, сервер жив

Самый вероятный случай: неудачная миграция, ошибочное удаление, повреждение данных. **Не торопитесь с шагами 1–3:
пока вы не остановили приложение, оно продолжает писать в испорченную базу.**

Контейнеры: `arena-backend-prod-xy5zqo-db-1` (база `arena`, пользователь `arena`), `…-api-1`, `…-worker-1`, `…-bot-1`,
`…-redis-1`. Во время восстановления **не нажимайте Deploy в панели Dokploy**: он перезапустит остановленные контейнеры.

**Шаг 1. Выбрать дамп.** Чем свежее, тем меньше потери, но дамп должен быть **до** порчи.

```bash
ls -lh /var/backups/arena-3h | tail -10          # каждые 3 часа, 3 суток
ls -lh /var/backups/arena | tail -8              # ночные, 14 дней
rclone lsl r2:arena-platform/arena-postgres-3h   # то же в R2 (если сервер без дисков)
rclone lsl r2:arena-platform/arena-postgres      # ночные в R2, 30 дней
```

Время в имени файла по UTC. Запишите время выбранного дампа: оно понадобится в разделе 10.

Если нужного файла нет на диске, достаньте его из R2:

```bash
mkdir -p /var/backups/restore && chmod 700 /var/backups/restore
rclone copyto r2:arena-platform/arena-postgres-3h/arena_3h_ГГГГММДД-ЧЧММ.dump /var/backups/restore/restore.dump
```

Если файл на диске, просто скопируйте: `cp /var/backups/arena-3h/arena_3h_….dump /var/backups/restore/restore.dump`.

**Шаг 2. Остановить всех, кто пишет в базу.**

```bash
docker stop arena-backend-prod-xy5zqo-api-1 arena-backend-prod-xy5zqo-worker-1 arena-backend-prod-xy5zqo-bot-1
```

С этого момента продажи на сайтах не проходят и бот молчит. Запишите время остановки.

**Шаг 3. Страховочный дамп текущего (испорченного) состояния.** Из него можно будет достать то, что ещё цело.

```bash
mkdir -p /var/backups/restore && chmod 700 /var/backups/restore
docker exec arena-backend-prod-xy5zqo-db-1 sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' \
  > /var/backups/restore/before_restore_$(date -u +%Y%m%d-%H%M).dump
ls -lh /var/backups/restore
```

**Шаг 4. Восстановить в новую, пустую базу рядом, не поверх.**

```bash
DB=arena-backend-prod-xy5zqo-db-1
docker cp /var/backups/restore/restore.dump $DB:/tmp/restore.dump
docker exec $DB psql -U arena -d postgres -c "CREATE DATABASE arena_restore OWNER arena"
docker exec $DB pg_restore -U arena -d arena_restore --no-owner --no-privileges --exit-on-error /tmp/restore.dump
echo "pg_restore завершился с кодом $?"
```

Код должен быть 0 и ошибок в выводе быть не должно. Если ошибка: `docker exec $DB psql -U arena -d postgres -c "DROP DATABASE arena_restore"`,
проверьте дамп (`docker run --rm -v /var/backups/restore:/b:ro postgres:17-alpine pg_restore --list /b/restore.dump | head`) и попробуйте другой.

**Шаг 5. Проверить содержимое новой базы до подмены.**

```bash
for t in organizations events sessions orders tickets customers payment_intents; do
  printf "%-18s" $t
  docker exec $DB psql -U arena -d arena_restore -At -c "select count(*) from $t"
done
docker exec $DB psql -U arena -d arena_restore -At -c "select max(created_at) from orders"
docker exec $DB psql -U arena -d arena_restore -At -c "select max(version_id) from schema_migrations where is_applied"
```

Сравните с ожиданием: последний заказ должен быть незадолго до времени дампа, число миграций не больше, чем в запущенном образе
(на 02.10.2026 версия 120).

**Шаг 6. Подменить базу.**

```bash
docker exec $DB psql -U arena -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname IN ('arena','arena_restore') AND pid <> pg_backend_pid()"
docker exec $DB psql -U arena -d postgres -c "ALTER DATABASE arena RENAME TO arena_broken_$(date -u +%Y%m%d)"
docker exec $DB psql -U arena -d postgres -c "ALTER DATABASE arena_restore RENAME TO arena"
docker exec $DB rm -f /tmp/restore.dump
```

Испорченная база остаётся рядом под именем `arena_broken_ГГГГММДД`. Удалите её (`DROP DATABASE`) только когда убедитесь, что всё работает.

**Шаг 7. Сдвинуть счётчики номеров вперёд.** Это важно. Дамп старый, а номера заказов и билетов, выданные после него, уже
знают сайты, покупатели и MACS. Если счётчики откатятся вместе с базой, **новые заказы получат уже занятые номера**.

```bash
docker exec $DB psql -U arena -d arena -At -c "select sequencename, last_value from pg_sequences where schemaname = 'public' order by 1"
```

На 02.10.2026 есть: `compatibility_system_id_seq` (номер заказа, `orders.system_id`), `tickets_system_id_seq` (номер билета),
`session_seats_system_id_seq`, а также `events_`, `organizations_`, `sales_channels_`, `users_`, `venues_display_number_seq`
(номера, которые видят люди). Сдвиньте каждый на запас, который заведомо больше числа потерянных заказов (раздел 10), не меньше 1000:

```bash
docker exec $DB psql -U arena -d arena -c "SELECT setval('compatibility_system_id_seq', (SELECT last_value FROM compatibility_system_id_seq) + 1000)"
docker exec $DB psql -U arena -d arena -c "SELECT setval('tickets_system_id_seq', (SELECT last_value FROM tickets_system_id_seq) + 1000)"
docker exec $DB psql -U arena -d arena -c "SELECT setval('session_seats_system_id_seq', (SELECT last_value FROM session_seats_system_id_seq) + 1000)"
```

Номера для людей (`*_display_number_seq`) двигайте так же, если за потерянный период создавались организации, события или площадки.
Для `schema_migrations_id_seq` ничего не делайте.

**Шаг 8. Догнать схему, если образ новее дампа.**

```bash
cd /etc/dokploy/compose/arena-backend-prod-xy5zqo/code
docker compose -p arena-backend-prod-xy5zqo run --rm migrate
```

Сервис `migrate` запускает `arena-migrate up`. Если схема уже актуальна, он напишет, что новых миграций нет. Образ **не может быть старше**
дампа: если дамп содержит более новую версию схемы, чем код (например, после отката образа), поднимите нужный образ.

**Шаг 9. Сбросить Redis и запустить приложение.**

```bash
docker exec arena-backend-prod-xy5zqo-redis-1 redis-cli FLUSHALL
docker start arena-backend-prod-xy5zqo-api-1 arena-backend-prod-xy5zqo-worker-1 arena-backend-prod-xy5zqo-bot-1
sleep 20
docker ps --format '{{.Names}}\t{{.Status}}' | grep arena-backend
```

Все три должны стать `healthy`.

**Шаг 10. Проверить снаружи.**

```bash
curl -s -o /dev/null -w "healthz %{http_code}\n" https://api.arenasoldout.com/healthz
curl -s -o /dev/null -w "readyz  %{http_code}\n" https://api.arenasoldout.com/readyz
curl -s https://api.arenasoldout.com/v1/info
```

Затем войдите в админку `app.arenasoldout.com`, откройте список заказов и убедитесь, что он соответствует времени дампа.
Тестовая покупка бесплатного билета на `arenasoldout.com` (например, категория «Guest list» на событии Midnight Jazz) проверяет всю цепочку, включая запись в MACS.

**Шаг 11. Разобрать потерянное окно** (раздел 10) и сообщить организаторам.

## 6. Сценарий B: сервер потерян целиком

Нужен новый сервер того же класса (Hetzner cpx32: 4 vCPU, 8 ГБ, Ubuntu). Подробно про заказ сервера, подключение к Dokploy
и устройство Traefik написано в `docs/ops/arena_server_move_runbook_2026-09-18_ru.md` и `deploy/DOKPLOY.md`. Ниже порядок именно для аварии.

1. **Сервер.** Закажите, поставьте Docker, подключите к панели Dokploy как удалённый сервер (панель живёт отдельно, на `lead-parser`, см. раздел 12, пункт 2).
2. **Доступ к образам.** Образ `ghcr.io/avamb/arena-api:<тег>` приватный. На новом сервере выполните `docker login ghcr.io` (токен GitHub с правом `read:packages`).
3. **rclone.** `apt install rclone`, затем создайте `/root/.config/rclone/rclone.conf` по шаблону из раздела 3 (ключ R2 из менеджера паролей или файла
   владельца, а если потерян, перевыпустите). Проверка: `rclone lsl r2:arena-platform/arena-postgres-3h | tail -3`.
4. **Compose с пустой базой.** В панели Dokploy возьмите compose проекта `arena-backend-prod` (он хранится в панели вместе с переменными окружения, а если панели нет, возьмите файлы из зашифрованного архива секретов, раздел 3)
   и разверните на новом сервере. Поднимутся `db`, `migrate` (создаст пустую схему), `api`, `worker`, `bot`. Сразу остановите приложение:
   `docker stop <проект>-api-1 <проект>-worker-1 <проект>-bot-1`, где `<проект>` это имя compose-проекта на новом сервере.
5. **Загрузить последний дамп из R2** и восстановить. База после `migrate` не пустая (в ней схема), поэтому пересоздаём её:

```bash
mkdir -p /var/backups/restore && chmod 700 /var/backups/restore
L=$(rclone lsf r2:arena-platform/arena-postgres-3h --include 'arena_3h_*.dump' | sort | tail -1)
rclone copyto "r2:arena-platform/arena-postgres-3h/$L" /var/backups/restore/restore.dump
DB=<проект>-db-1
docker cp /var/backups/restore/restore.dump $DB:/tmp/restore.dump
docker exec $DB psql -U arena -d postgres -c "DROP DATABASE arena"
docker exec $DB psql -U arena -d postgres -c "CREATE DATABASE arena OWNER arena"
docker exec $DB pg_restore -U arena -d arena --no-owner --no-privileges --exit-on-error /tmp/restore.dump
```

6. **Медиа** (раздел 7), затем **счётчики номеров** (раздел 5, шаг 7), **миграции** (шаг 8), **Redis** (шаг 9).
7. **DNS.** В Cloudflare поменяйте записи A у `api.`, `app.`, `tickets.` и `macs.arenasoldout.com` на IP нового сервера (оставьте оранжевое облако).
   Домены не меняются, поэтому адреса вебхуков Stripe, Flitt и MACS, настройки сайтов и бот остаются как были.
8. **Ловушка Cloudflare «526».** Зона работает в режиме Full (strict): без настоящего сертификата на сервере будет ошибка 526. Сертификат Let's Encrypt
   Traefik выдаёт **только после смены DNS**. Сразу после смены пересоздайте контейнеры (`docker compose up -d --force-recreate api`), и сертификат выдастся за секунды.
9. **Traefik на новом сервере** должен иметь список адресов Cloudflare в `forwardedHeaders.trustedIPs`, а в окружении Arena стоит `TRUSTED_PROXY_COUNT=2`.
   Иначе все посетители выглядят одним адресом и срабатывает ограничение по IP. Образец: `/etc/dokploy/traefik/traefik.yml` на старом сервере.
10. **Бэкапы на новом сервере**: раздел 13.
11. **MACS**: раздел 8, подраздел «MACS на новом сервере».
12. Разберите потерянное окно (раздел 10).

## 7. Сценарий C: потеряны афиши и логотипы (медиа)

Медиа лежит в томе Docker `arena-backend-prod-xy5zqo_arena-media` и архивируется каждую ночь. Если том пуст или повреждён:

```bash
docker stop arena-backend-prod-xy5zqo-api-1 arena-backend-prod-xy5zqo-worker-1
ls -lh /var/backups/arena/media_*.tgz | tail -3        # или: rclone lsl r2:arena-platform/arena-media
docker run --rm -v arena-backend-prod-xy5zqo_arena-media:/dst -v /var/backups/arena:/src:ro alpine \
  sh -c "cd /dst && tar xzf /src/media_ГГГГММДД-ЧЧММСС.tgz"
docker start arena-backend-prod-xy5zqo-api-1 arena-backend-prod-xy5zqo-worker-1
```

Если архив из R2, скачайте его в `/var/backups/restore` и подставьте этот путь вместо `/var/backups/arena`.
Афиши, загруженные после ночного архива, пропадут: организаторам придётся загрузить их заново.

## 8. Сценарий D: MACS

MACS (`macs.arenasoldout.com`) это FastAPI, Next.js и MongoDB 8.0.17, стек `/opt/macs` на боевом сервере (обычный `docker compose`, **не Dokploy**).
Контейнеры `macs-mongodb`, `macs-backend`, `macs-frontend`. Посетители идут через Cloudflare.

**База испорчена, сервер жив.**

```bash
ls -lh /opt/macs/backup | tail -5                 # или: rclone lsl r2:arena-platform/macs-mongo
cd /opt/macs
docker cp /opt/macs/backup/mongo_daily_ГГГГММДД_ЧЧММ.archive.gz macs-mongodb:/tmp/r.gz
docker exec macs-mongodb sh -c 'mongorestore -u admin -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --gzip --archive=/tmp/r.gz --nsInclude "arenasoldout.*" --drop; rm -f /tmp/r.gz'
```

Проверка количества записей (должно быть похоже на то, что было: примерно 680 билетов, 7400 записей вебхуков на 02.10.2026):

```bash
cat > /tmp/count.js <<'EOF'
print(db.getCollectionNames().sort().map(c => c + "=" + db.getCollection(c).countDocuments()).join(" "));
EOF
docker cp /tmp/count.js macs-mongodb:/tmp/count.js
docker exec macs-mongodb sh -c 'mongosh --quiet -u admin -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin arenasoldout --file /tmp/count.js'
```

Затем зайдите в панель MACS и отсканируйте тестовый билет. Билеты, проданные после дампа, дойдут повторно только если Arena
повторит вебхуки: сверьтесь по разделу 10.

**MACS на новом сервере** (образы и исходники лежат в R2, их больше нигде нет, кроме самого сервера).

```bash
mkdir -p /opt/macs && cd /opt/macs
# compose и init-user.js возьмите из репозитория: ops/macs/docker-compose.yml и ops/macs/init-user.js
umask 077
printf "MONGO_ROOT_PASSWORD=%s\nMONGO_APP_PASSWORD=%s\n" "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > .env
rclone cat r2:arena-platform/macs-images/macs_images_20261002.tar.gz | gunzip | docker load
docker pull mongo:8.0.17
docker compose up -d mongodb          # дождитесь healthy, пользователь приложения создаётся скриптом при первом запуске
L=$(rclone lsf r2:arena-platform/macs-mongo | sort | tail -1)
mkdir -p backup && rclone copyto "r2:arena-platform/macs-mongo/$L" "backup/$L"
docker cp "backup/$L" macs-mongodb:/tmp/r.gz
docker exec macs-mongodb sh -c 'mongorestore -u admin -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin --gzip --archive=/tmp/r.gz --nsInclude "arenasoldout.*" --drop; rm -f /tmp/r.gz'
docker compose up -d backend frontend
```

Нужна внешняя сеть `dokploy-network` (есть на сервере, подключённом к Dokploy). Затем смените в Cloudflare запись A `macs.arenasoldout.com`
и пересоздайте контейнеры после смены DNS (ловушка 526 из раздела 6, пункт 8).

**Важно про MACS.**
- Панель MACS **вшивает адрес API `https://macs.arenasoldout.com/api` в образ при сборке**. Поэтому на временном имени (например `macs2.…`) панель не проверить,
  а домен менять нельзя без пересборки образа. Пересобирать образы, которым больше года, не стоит: свежие зависимости их ломают.
- Исходники лежат в `macs-sources/macs_sources_20261002.tar.gz` (каталоги `backend` и `frontadmin`). В архив **нарочно не включён** старый `docker-compose.yml`,
  в нём открытый пароль базы со старого сервера.
- Бэкенд работает в режиме разработки (`uvicorn --reload`), пароль базы новый, но код чужой и старый.

## 9. Сценарий E: потерян Redis

Ничего не восстанавливать. Redis хранит только временное состояние (блокировки, кэш), всё перестраивается из Postgres. После любого восстановления базы
сбрасывайте его (`redis-cli FLUSHALL`), чтобы старые блокировки не противоречили восстановленным данным. Подробности: `deploy/BACKUP_RESTORE_RUNBOOK.md`, §8.

## 10. Потерянное окно продаж

Между временем дампа и временем аварии продажи в Arena пропали, хотя покупатели заплатили, сайты показали «оплата прошла» и MACS мог получить билеты.
**Автоматического способа вернуть такие заказы нет:** платёж в Arena уже не найти, поэтому повторная отправка вебхуков Stripe или Flitt их не создаст
(платёж чужой для восстановленной базы, и Arena ответит «не наш платёж»). Решение по каждому заказу принимает человек: оформить заново вручную или вернуть деньги.

Где взять список потерянных:
1. **Чаты продаж в Telegram.** Продажный бот пишет сообщение о каждой оплате с номером заказа, суммой и контактами покупателя. Всё, что после времени дампа, потеряно.
2. **Кабинет Stripe** (платежи и события за период) и **кабинет Flitt**, отфильтруйте по времени.
3. **Сайты-продавцы** (WooCommerce на Lampyris, Vino&Co, arenasoldout.com): заказы за период со статусом «оплачен». В заказе есть номер заказа Arena.
4. **MACS:** билеты в коллекции `ticket`, проданные после времени дампа (`sold_at`), сверьте со списком.

Для каждого потерянного заказа: найдите платёж, свяжитесь с покупателем (организатор сделает это сам, адрес есть в сообщении бота), затем либо оформите билет вручную,
либо верните деньги в кабинете платёжной системы. Число потерянных заказов нужно и для запаса счётчиков номеров (раздел 5, шаг 7).

## 11. Учения (проверка, что бэкап восстанавливается)

Делайте раз в квартал и после любых изменений в бэкап-скриптах. Боевую базу учения **не затрагивают**.

```bash
mkdir -p /tmp/pgtest && cd /tmp/pgtest
L=$(rclone lsf r2:arena-platform/arena-postgres-3h --include 'arena_3h_*.dump' | sort | tail -1)
rclone copyto "r2:arena-platform/arena-postgres-3h/$L" "/tmp/pgtest/$L"
docker run -d --name pgrestoretest --memory 512m -e POSTGRES_PASSWORD=throwaway -e POSTGRES_DB=restoretest -v /tmp/pgtest:/b:ro postgres:17-alpine
sleep 15
docker exec pgrestoretest pg_restore -U postgres -d restoretest --no-owner --no-privileges "/b/$L"; echo "код $?"
docker exec pgrestoretest psql -U postgres -d restoretest -At -c "select count(*) from orders"
docker rm -f pgrestoretest; rm -rf /tmp/pgtest
```

Код должен быть 0, число заказов близким к боевому (отстаёт на продажи после дампа). Для MACS то же самое: восстановите дамп во временную базу
(`--nsFrom "arenasoldout.*" --nsTo "restoretest.*"`) и удалите её.

Результаты учений:

| Дата | Что проверено | Результат |
|---|---|---|
| 2026-10-02 | дамп Postgres из R2 → одноразовый postgres:17 → `pg_restore` | без ошибок; организации, события, сеансы совпали с боевой базой, заказы и билеты отставали на продажи после 02:15 |
| 2026-10-02 | дамп MongoDB MACS → временная база | 8474 документа, 0 ошибок |
| 2026-10-02 | образ MACS из R2 | архив читается (`manifest.json` на месте) |

Чего учения пока не проверяли: сценарий B целиком (новый сервер), подмену базы в боевом контейнере (шаги 6–9 сценария A), ручной запуск миграций
(шаг 8: проверено только, что compose под именем проекта `arena-backend-prod-xy5zqo` находит сервис `migrate`, но не сам запуск) и скорость. Проведите их при первой возможности вне рабочего времени и запишите реальное время в раздел 1.

## 12. Известные дыры (решения владельца)

1. **Секреты Arena: закрыто 02.10.2026.** Копии в менеджере паролей и в зашифрованном архиве R2 (раздел 3). Остаточный риск: **закрытый ключ `age` должен быть
   сохранён в менеджере паролей**, без него архив бесполезен. Файл `/root/backup-age-private.txt` на сервере после этого удаляют. Пока он лежит на сервере рядом с архивом,
   шифрование защищает только от утечки самого R2, но не от взлома сервера. Менеджер паролей придётся обновлять вручную при смене секретов.
2. **Бэкапа самой панели Dokploy (`lead-parser`) нет.** В её базе лежат все проекты, compose и переменные окружения Arena и сайтов. После переноса панели на
   освободившийся сервер добавьте R2 как место назначения (Settings → S3 Destinations) и включите бэкап базы панели. Эти бэкапы будут содержать секреты из пункта 1:
   папка в R2 только с этой целью, ключ с правами на один бакет.
3. **Потеря данных до 3 часов.** WAL-архив и восстановление на любую секунду (PITR) не включены, описание в `deploy/BACKUP_RESTORE_RUNBOOK.md`, §5 и §7.
4. **Копии не шифруются нашим ключом** (только шифрование на стороне Cloudflare). Бакет закрыт, ключ ограничен одним бакетом.
5. **Ключ R2 с правом удаления файлов** лежит на сервере. Тот, кто получит ключ, может стереть и копии. Усиление: версионирование бакета.
6. **Один боевой сервер.** Потеря сервера означает простой, пока идёт сценарий B (оценка 2–4 часа).
7. **Токены в `api-token-*.txt`** (Cloudflare, Hetzner, R2) лежат открытым текстом в рабочем каталоге проекта (в `.gitignore`). Перевыпустите их по окончании работ.
8. **Старый сервер `arena-macs`** ещё работает как резерв MACS с данными на 02.10.2026, 14:29 UTC. Не останавливайте, пока не пройдёт реальное событие со сканированием.

## 13. Скрипты и их установка на новом сервере

В репозитории: `ops/backup/` (скрипты и `cron.d/`), `ops/macs/` (compose и init-скрипт MACS). На сервере сверены побайтно 02.10.2026.

| Файл в репозитории | Куда на сервере | Задание cron |
|---|---|---|
| `ops/backup/arena-backup.sh` | `/usr/local/bin/arena-backup.sh` | `/etc/cron.d/arena-backup`, 02:15 |
| `ops/backup/arena-pg-3h.sh` | `/usr/local/bin/arena-pg-3h.sh` | `/etc/cron.d/arena-pg-3h`, каждые 3 часа |
| `ops/backup/macs-backup.sh` | `/opt/macs/backup.sh` | `/etc/cron.d/macs-backup`, 03:30 |
| `ops/backup/offsite-backup.sh` | `/usr/local/bin/offsite-backup.sh` | `/etc/cron.d/offsite-backup`, 03:45 |
| `ops/backup/secrets-backup.sh` | `/usr/local/bin/secrets-backup.sh` | `/etc/cron.d/secrets-backup`, 03:50 |
| `ops/backup/backup-check.sh` | `/usr/local/bin/backup-check.sh` | `/etc/cron.d/backup-check` и `--hourly` в `arena-pg-3h` |

Имена контейнеров в скриптах прописаны под проект `arena-backend-prod-xy5zqo` (`arena-backend-prod-xy5zqo-db-1`, `…-worker-1`) и том
`arena-backend-prod-xy5zqo_arena-media`. На новом сервере Dokploy создаст проект с другим суффиксом: **замените имена в `arena-backup.sh`, `arena-pg-3h.sh` и `backup-check.sh`**.

```bash
install -m 700 ops/backup/arena-backup.sh ops/backup/arena-pg-3h.sh ops/backup/offsite-backup.sh ops/backup/backup-check.sh ops/backup/secrets-backup.sh /usr/local/bin/
echo 'age14meanax6yr474lnvapldtq2jcgm2w7ytgg952v4ypzzrg6g0gatqhnnmpf' > /etc/backup-age-recipient.txt      # открытый ключ, нужен secrets-backup.sh
install -m 700 ops/backup/macs-backup.sh /opt/macs/backup.sh
install -m 644 ops/backup/cron.d/* /etc/cron.d/
mkdir -p /var/backups/arena /var/backups/arena-3h && chmod 700 /var/backups/arena /var/backups/arena-3h
/usr/local/bin/arena-pg-3h.sh && /usr/local/bin/backup-check.sh --test      # дамп и тестовое сообщение в чат поддержки
```

Файлы cron лежат в `ops/backup/cron.d/` без выполняемых прав, это обычные текстовые файлы с расписанием. Права на выполнение нужны только скриптам.
