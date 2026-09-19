# Ops watchdog и мониторинг — рабочий журнал

Первые реальные продажи стартуют без дежурного оператора. `ops.watchdog`
(`internal/platform/opswatchdog`, задача в `arena-worker`, цикл 60 секунд)
читает бизнес-таблицы READ-ONLY и шлёт алерты в Telegram через
`internal/platform/opsalert`. Ничего не пишет в бизнес-таблицы, не стоит на
денежном пути. Собственные таблицы — `ops_watchdog_state` (курсоры) и
`ops_alerts` (дедуп/жизненный цикл алертов), миграция `0104`.

## 1. Создать Telegram-бота и получить chat_id

1. В Telegram написать `@BotFather` → `/newbot`, задать имя и username.
   BotFather выдаст токен вида `123456789:AAExampleTokenValue` — это
   `OPS_TELEGRAM_BOT_TOKEN`.
2. Написать боту любое сообщение (или добавить его в группу и написать там).
3. Получить `chat_id`:
   `curl "https://api.telegram.org/bot<TOKEN>/getUpdates"` — в ответе поле
   `message.chat.id` (для личной переписки — положительное число, для
   группы — отрицательное). Это `OPS_TELEGRAM_CHAT_ID`.
4. Если бот в группе — отключить Privacy Mode (`@BotFather` →
   `/mybots` → выбрать бота → `Bot Settings` → `Group Privacy` → `Turn off`),
   иначе `getUpdates` не увидит сообщения в группе.

## 2. Переменные окружения (Dokploy: api и worker)

Задать ОДИНАКОВО на обоих сервисах (`arena-api` и `arena-worker`) —
watchdog живёт в воркере, но токен `/metrics` защищает оба процесса:

| Переменная | Обязательна | Назначение |
|---|---|---|
| `OPS_TELEGRAM_BOT_TOKEN` | нет (пусто = алерты только в лог) | токен бота |
| `OPS_TELEGRAM_CHAT_ID` | нет | куда слать |
| `OPS_ALERT_ENV_LABEL` | нет, по умолчанию `dev` | префикс `[prod]`/`[staging]` в каждом сообщении — различать окружения в одном чате |
| `OPS_WATCHDOG_HEARTBEAT_HOUR_UTC` | нет, по умолчанию `7` | час UTC для ежедневного «watchdog alive» |
| `METRICS_BEARER_TOKEN` | нет (пусто = `/metrics` без авторизации, как раньше) | защищает `GET /metrics` на api и на воркере (`WORKER_METRICS_ADDR`, по умолчанию `:9091`) |

Пустые `OPS_TELEGRAM_*` — безопасный дефолт: watchdog продолжает работать,
просто пишет каждое сообщение в структурированный лог вместо Telegram
(`opsalert: notify (no-op, Telegram not configured)`).

После задания `METRICS_BEARER_TOKEN` не забыть прописать тот же токен в
`ops/prometheus/prometheus.yml` (`authorization.credentials`), иначе
Prometheus получит 401.

## 3. Каталог алертов

Каждый алерт — Telegram-сообщение с эмодзи-меткой severity. У
«стоячих» условий (b/c/e) есть дедуп: первое срабатывание уведомляет сразу,
повтор — раз в 30 минут, пока условие держится, и отдельное «✅ resolved»
сообщение, когда условие исчезает. У «потоковых» проверок (a/d/f) —
курсор: одно сообщение на новую строку (или один дайджест, если строк
больше 5 за проход), без повторов.

### a. 🎟 Продажа (INFO, поток)

Каждый заказ, ставший `paid` с прошлого прохода (`orders.paid_at`).
Покрывает оба пути продажи (виджет и Bil24-шлюз) — опрашивает `orders`
напрямую, независимо от источника. Действие: не требуется, это
подтверждение, что продажа состоялась.

### b. 🚨 Оплачен, но билеты не выпущены (CRITICAL, стоячее)

`orders.status='paid'`, `paid_at` старше 3 минут, и в `tickets` нет активной
строки с этим `order_id`. Значит `checkout.issue_tickets` не отработал.

Проверить вручную:

```sql
SELECT id, system_id, status, paid_at FROM orders WHERE id = '<order_id>';
SELECT * FROM tickets WHERE order_id = '<order_id>';
SELECT * FROM worker_jobs
 WHERE job_type = 'checkout.issue_tickets'
   AND payload->>'checkout_session_id' = (
       SELECT checkout_session_id::text FROM orders WHERE id = '<order_id>');
```

Если `worker_jobs` строка `status='failed'` — смотреть `last_error`,
перезапустить job'у вручную (обновить `status='pending'`,
`scheduled_at=now()`) после устранения причины. Если строки нет вовсе —
завести её вручную по образцу `IssueTicketsForCheckout` (см.
`internal/platform/issuejob`) или эскалировать разработчику — авто-выпуск
не сработал в принципе.

### c. 🚨 Оплата прошла, но checkout не завершён / manual_review (CRITICAL, стоячее)

Два случая, один алерт-класс:

- `payment_intents.state='succeeded'` старше 3 минут, а связанный
  `checkout_sessions.state <> 'completed'` — веб-хук провайдера не довёл
  чекаут до конца.
- Любая строка `checkout_sessions`/`orders`/`payment_intents` со
  статусом `manual_review` — просроченная оплата, для которой нужен ручной
  возврат.

Проверить:

```sql
SELECT id, state, checkout_session_id, amount, currency, succeeded_at
  FROM payment_intents WHERE id = '<payment_intent_id>';
SELECT id, state, total, currency FROM checkout_sessions WHERE id = '<checkout_session_id>';
```

Действие для `manual_review`: деньги списаны у покупателя, но заказ не
проведён — оформить возврат вручную в дашборде Stripe организатора
(Payments → найти платёж по сумме/времени → Refund), затем закрыть
заказ/чекаут в БД (`status='cancelled'`) и коротко зафиксировать причину в
`order_events`/`checkout_sessions` (произвольным SQL, это не денежная
транзакция).

### d. 🔶 Dead letter (HIGH, поток)

Новые строки `worker_dead_letter`, `outbox_events.dead_lettered_at`,
`delivery_jobs.status='failed'`. `last_error` в сообщении уже усечён до
200 символов и очищен от email-адресов.

- **worker_dead_letter / outbox_events** — SQL-реплей, инструкция целиком
  в `docs/ops/bil24_gateway.md` §4 («Dead-lettered outbox rows»): найти
  причину в `last_error`, исправить (обычно — недоступный вебхук WP-сайта),
  снять `dead_lettered_at`/`attempts`/`next_attempt_at` у конкретной строки.
  Не реплеить пачкой, пока причина не устранена — забьёте очередь снова.
- **delivery_jobs** (email с билетом не доставлен) — обычно SMTP-ошибка
  или неверный адрес. `UPDATE delivery_jobs SET status='pending', attempts=0
  WHERE id='<id>'` после исправления причины (или письмо не критично —
  билет всё равно доступен по прямой ссылке/PDF).

### e. ⚠️ Задержка очереди / бэклог (WARN, стоячее)

- Самая старая `pending` строка `worker_jobs` старше 5 минут — воркер не
  успевает или не запущен. Проверить `docker compose ps` /
  `docker ps -f name=arena_worker`, посмотреть логи воркера.
- Бэклог `outbox_events` (`processed_at IS NULL AND dead_lettered_at IS
  NULL`) ≥ 100 — диспетчер не успевает или целевой вебхук недоступен.

```sql
SELECT min(scheduled_at) FROM worker_jobs WHERE status='pending';
SELECT count(*) FROM outbox_events WHERE processed_at IS NULL AND dead_lettered_at IS NULL;
```

### f. 💸 Возврат (INFO, поток)

Новая строка `refunds` (любой `settlement`). Информационное — подтверждает,
что возврат зафиксирован в реестре.

### g. 📋 Heartbeat (INFO, раз в сутки)

В `OPS_WATCHDOG_HEARTBEAT_HOUR_UTC` (по умолчанию 07:00 UTC) — сводка за
последние 24 часа: количество и сумма продаж по валютам, число открытых
алертов. Если сообщение не пришло — watchdog не работает, смотреть логи
`arena-worker` на предмет `ops.watchdog` (job type) и падений при старте.
Плюс одно сообщение при каждом старте воркера («watchdog started»).

### Служебный алерт: сам watchdog сломался (WARN)

Если одна и та же проверка падает 3 прохода подряд (60с × 3 = 3 минуты) —
приходит `watchdog check "<name>" failing`. Смотреть логи `arena-worker` —
там будет полная ошибка (в Telegram — только последние 200 символов,
экранированные от email).

## 4. Внешний аптайм-мониторинг (вне этого сервера)

Watchdog видит только то, что происходит ВНУТРИ БД — если сам `arena-api`
не отвечает или упал контейнер целиком, читать ему будет нечего.
Обязательно завести внешний пробер (UptimeRobot / Better Uptime / Pingdom —
любой, не хостящийся на том же сервере) с проверкой раз в минуту:

- `https://api.arenasoldout.com/readyz` — готовность API (БД, миграции).
- Страница с билетами на сайте (например конкретное событие на
  arenasoldout.com) — что сайт вообще открывается.
- Бандл виджета (`https://api.arenasoldout.com/...widget.js` — уточнить
  актуальный путь в `apps/widget`) — что покупка технически возможна.

Важно: Hetzner-файрвол на прод-сервере пропускает только Cloudflare (см.
`docs/ops/infra_audit_2026-09-17_ru.md`), поэтому пробер должен ходить
через публичный хостнейм (`api.arenasoldout.com`), а не напрямую по IP —
иначе он всегда будет видеть таймаут независимо от реального состояния
сервиса.

## 5. `/metrics` и токен

`METRICS_BEARER_TOKEN` защищает `GET /metrics` на обоих процессах:
без него — как раньше, без авторизации (локальный compose/приватная сеть).
С ним — запрос без `Authorization: Bearer <token>` получает 401 (сравнение
константного времени, токен никогда не логируется). Сгенерировать значение
можно любым способом (например `openssl rand -hex 32`), поместить в Dokploy
переменные `arena-api` и `arena-worker`, и туда же в
`ops/prometheus/prometheus.yml` (раздел `authorization` в каждом
`scrape_config`, закомментированный пример уже в файле).

## Связанное чтение

- `docs/ops/bil24_gateway.md` §4 — реплей dead-letter строк outbox.
- `docs/ops/infra_audit_2026-09-17_ru.md` — топология серверов и файрвол.
- `AGENTS.md` — готча про watchdog (read-only, курсоры с `now()`, дедуп по
  fingerprint, без PII).
