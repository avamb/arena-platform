# Outbox, worker jobs and operator notifications

Outbox tables and dead letters, worker_jobs retention, per-binary config, ops watchdog, Telegram sales and change notifications. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **Two outbox tables exist and only one is dispatched.** Legacy `outbox`
  (migration 0002: `aggregate_id uuid`, `dispatched_at`) is what
  `outbox.PGWriter` writes to and what the backlog/lag monitors count.
  `outbox_events` (migration 0001: `aggregate_id text`, `processed_at`,
  `last_error`) is what EVERY dispatcher reads — `PGOutboxEventStore`,
  `macs.Dispatcher`, `bil24wire.Dispatcher`, `cmd/arena-worker`. Events
  appended through `PGWriter` are therefore never delivered, silently and
  without an error log. Feature #509 added `outbox.PGEventsWriter` and made it
  the default in `httpserver/wire.go` and `cmd/arena-worker/main.go`; use it
  for any new wiring. Tests asserting that an event was published must query
  `outbox_events` and cast the id (`aggregate_id = $1::text`).

- **`outbox_events` dead-letter replay is SQL-only, no admin endpoint.**
  `internal/platform/outbox` stops retrying a row once `dead_lettered_at` is
  set; find candidates with
  `SELECT * FROM outbox_events WHERE dead_lettered_at IS NOT NULL`, fix the
  root cause named in `last_error` (usually a stale/unregistered WP webhook
  URL), then clear `dead_lettered_at`/`next_attempt_at`/`attempts` on that
  one row to requeue it. Do not bulk-requeue against a still-broken
  receiver — it just refills the dead-letter queue. Full runbook:
  `docs/ops/bil24_gateway.md` §4.

- **`ops.watchdog` (`internal/platform/opswatchdog`, registered in
  `cmd/arena-worker/main.go` next to `order.expire_sweep`/
  `reservation.expire_sweep`, migration 0104) is READ-ONLY on every business
  table and must stay that way** — it only ever writes its own two tables,
  `ops_watchdog_state` (per-check cursors) and `ops_alerts` (dedup/lifecycle
  for standing-condition alerts). Cursor-based checks (sales feed, dead
  letters, refunds) seed their cursor to `now()` on first use, never
  replaying pre-existing history — a fixture that inserts a row and THEN
  calls the handler for the very first time in that process sees its OWN
  row treated as "history" and skipped (bit the dead-letters integration
  test; fixed by priming the handler once before seeding, same pattern the
  sales-feed test already used). Standing-condition checks (paid-no-
  tickets, payment-succeeded-not-completed, manual_review, lag) dedup by
  `ops_alerts.fingerprint` via `AlertEngine.Sync` — notify once, re-notify
  every 30 minutes while it recurs, one "resolved" message when a later run
  no longer finds it. Alert/sale messages must never carry buyer email,
  name or phone — only order numbers (`orders.system_id`), amounts,
  counts, ids; a job's free-form `last_error` is scrubbed
  (`opsalert.ScrubEmails`) and truncated to 200 chars before it can reach a
  message. `internal/platform/opsalert.New` degrades to a logging no-op
  when `OPS_TELEGRAM_BOT_TOKEN`/`OPS_TELEGRAM_CHAT_ID` are empty — never an
  error, never blocks worker startup. Running the watchdog against the
  shared local dev-stand surfaces REAL pre-existing `payment_not_completed`/
  `manual_review` rows from old load-test data (dated 2026-09-13) — that is
  correct behaviour, not a bug in the check; do not "fix" the query to hide
  them.

- **Organizer Telegram sales notifications are the FIRST leg of the outbox
  fan-out (`internal/platform/salesnotify`), not the ops watchdog.** Rows of
  `sales_notification_subscriptions` (migration 0111) map a chat to an org
  (`org_id NULL` = operator, every org) with `on_order_paid` /
  `on_ticket_refunded` / `allowed`, like Bil24's Notifications tab. The leg
  reacts to `v1.order.paid`, `v1.ticket.refunded`, `v1.ticket.cancelled`,
  only QUEUES and always returns nil, so Telegram can never delay or
  re-trigger the WordPress/MACS webhooks; because `multiDispatcher` still
  re-runs every leg when ANY other leg fails, each announcement is claimed
  once in `sales_notification_deliveries` (`paid:<order>` /
  `refund:<ticket>` — the latter also collapses the refunded+cancelled pair
  of one provider refund). Own bot `SALES_TELEGRAM_BOT_TOKEN` (the ops bot
  also sends operator alerts organizers must not see), English text. Since
  2026-09-25 (owner decision) sale and refund messages DO carry the buyer's
  name, e-mail and phone — `orders.buyer_*`, falling back to the customer's
  `display_name` / latest `customer_identities` value — so the organizer can
  reach the buyer; the ops watchdog alerts still never do. A supergroup upgrade (`migrate_to_chat_id`) is
  followed automatically; a chat the bot is not in lands in `last_error`.
  The bot must be a member of the group first (`getChat` answers `chat not
  found` otherwise). No admin screen yet: subscriptions are SQL rows.

- **"A new event / an event changed" reaches the operator by COMPARING, not by listening (migration 0124, `internal/platform/eventwatch`, job `events.change_watch`, every 30 s).** The Telegram bot saves an event through the event-bundle import, which raises `v1.event.published` once (`result.PublishedNow`) and NOTHING for a later rename, moved date, new price or new poster; a price edit through the API raises nothing either. So an outbox leg would miss exactly the bot's edits. The watcher builds a `Snapshot` (name, poster, dates, categories with prices and price windows) of every published event, compares it with `event_watch_snapshots.announced` and posts the difference through the SALES bot (`salesnotify.Dispatcher.AnnounceEventChange`, English, HTML) to subscriptions with `org_id IS NULL AND on_event_changes` only — an organization's own group never gets it (owner decision 2026-10-07); the migration switches the operator row on. Tracked: name, poster, dates (added/moved/cancelled/removed), prices (price, windows, free/pwyw, category added/removed/renamed). NOT tracked: venue, description, age, capacity, sale windows. A difference is announced only after `DefaultStableFor` (60 s) of staying the same, because an event is saved date by date and may be published after the first one. The very first pass records the existing events silently (`event_watch_state.seeded_at`); a draft is silent until published, and republishing an unchanged event is silent. Row is recorded BEFORE the send, so a crash loses one message rather than repeating it. Add a tracked field in `snapshot.go` (SQL + struct), `Diff` and `message_test.go`; `TestWatcher_Integration` resets the watch tables, so run it on a scratch database.

- **`worker_jobs` rows are not kept forever since 2026-10-14:** the hourly `worker_jobs.cleanup` job (`internal/platform/jobretention`) deletes `done` rows 30 days and `failed` rows 90 days after `scheduled_at`, in batches of 5000, and never touches `pending`/`claimed`; a check that needs older job history must read `worker_dead_letter` or its own table instead.

- **Configuration is validated PER BINARY: every `cmd/*/main.go` calls `config.LoadFor(config.Role<X>)`, never `config.Load()` (2026-10-14).** `RoleAPI`, `RoleWorker`, `RoleBot`, `RoleMigrate` (also arena-seed); production checks only what that role reads (`config/roles.go`: `roleCapabilities` decides the rule groups, `roleSecrets` the "set but unused" warning, which names secrets and never prints values). Shared rules (DATABASE_URL + sslmode, LOG_FORMAT=json, dev-auth / query-log / debug / fault-injection bans, `*_API_BASE_URL` bans, `BREVO_API_BASE_URL` / `POSTER_LLM_BASE_URL` only at their default) apply to every role. A container without `APP_ENV` runs as development and logs a loud "APP_ENV is not set" warning: set it on all four. `config.Load()` is `RoleAll`, the union of api + worker + migrate rules, kept for tests only; `tests/staticanalysis/cmd_config_role_test.go` fails on a binary that loads without its role or with the wrong one, and a new binary that imports config needs a line in its `cmdConfigRole`. In staging and production `MediaSigningKey()` no longer falls back to the JWT secret and ANY `MEDIA_BACKEND` needs `MEDIA_SIGNING_SECRET` on api and worker (the worker holds no JWT secret any more; the fallback is for development only). A new setting goes into the role table of `deploy/DOKPLOY.md` §2.6 (`tests/staticanalysis/dokploy_env_table_test.go` fails otherwise) and, if secret, into `roleSecrets`; a new production rule uses `c.uses(cap...)` unless every binary really reads it. Tests: `config/roles_test.go` (each role starts with only its minimum and fails without each variable of it; each conditional rule refuses exactly the roles that read its setting).

- **The event watcher's message tells the operator which storefronts already show a change, and a second message follows when a site catches up (migration 0152, owner decision 2026-10-14).** A WordPress site sells from its own copy of the catalog, so Telegram said "price changed" minutes before lampyrisevents.com showed it. Now every announcement (`eventwatch`, `events.change_watch`) ends with a `🛒 Storefronts` block: a channel without a gateway credential that the event is published to (`event_publications` -> `agent_feed_tokens.sales_channel_id`) reads arena live and is `updated`; a channel WITH one (`settings.gateway.token_hash` or legacy `gateway_token_hash`) is a site, and since GET_ALL_ACTIONS lists every published event of the organization regardless of publications, every site of the organization that fetched within 7 days counts (a published-to site silent longer is listed `no catalog fetch for over 7 days` and not tracked). A site is `updated` when its last fetch STARTED after the watcher first saw the announced version (`observed_at`), else `waiting for sync` and tracked in `event_watch_site_sync`; later passes send ONE `✅ … picked up the change (after N min)` (duration from that first sighting to the fetch) and, after `SiteSyncWarnAfter` (30 min) without a fetch, ONE `⚠️ … has not picked up the change for 30 min`, and still the confirmation later. A newer announcement of the same event deletes and rewrites its rows in the same transaction as the snapshot, so an older change is never confirmed; rows of an unpublished event or deleted channel are dropped. The only write outside the watcher is `hbil24.recordCatalogFetch`: after a GET_ALL_ACTIONS answer was built without an error for an authenticated channel, on the pool, never in a transaction, best effort (a failure only logs), it upserts the request's START time into `channel_catalog_fetches` only when the stored one is more than 30 s older — throttling can delay a confirmation, never fake one. Tests: `eventwatch/sitesync_integration_test.go` (resets the watcher tables: scratch database only), `tests/compat/bil24/catalog_fetch_integration_test.go`.
