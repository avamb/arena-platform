# Deploying arena_new to Dokploy

> **Milestone scope:** This guide covers the **Backend Foundation Milestone** and
> the **PR-07 admin-web production artifact** (§15). Business-logic modules
> (identity, catalog, payments, etc.) are added in subsequent milestones;
> deployment steps will grow accordingly.

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Production Runtime Topology (api, worker, migrate)](#2-production-runtime-topology-api-worker-migrate)
3. [Create the Application in Dokploy](#3-create-the-application-in-dokploy)
4. [Attach a Managed PostgreSQL Service](#4-attach-a-managed-postgresql-service)
5. [Configure Environment Variables](#5-configure-environment-variables)
6. [Run Database Migrations](#6-run-database-migrations)
7. [Healthcheck & Port Configuration](#7-healthcheck--port-configuration)
8. [Deploy](#8-deploy)
9. [Post-Deploy Smoke Checks](#9-post-deploy-smoke-checks)
10. [Production Environment Variable Checklist](#10-production-environment-variable-checklist)
11. [Secret Rotation](#11-secret-rotation)
12. [Troubleshooting](#12-troubleshooting)
13. [Release Branch & Image Tag Strategy](#13-release-branch--image-tag-strategy)
14. [Initial Production Server Requirements](#14-initial-production-server-requirements)
15. [Admin Web Service (PR-07)](#15-admin-web-service-pr-07)

---

## 1. Prerequisites

| Requirement | Version |
|---|---|
| Dokploy | ≥ 0.6 (self-hosted) |
| Docker registry | Any OCI-compatible registry (GHCR, Docker Hub, etc.) |
| PostgreSQL | 17 (managed or Dokploy service) |
| Redis | 7 (optional for this milestone; required for locks in later milestones) |

---

## 2. Production Runtime Topology (api, worker, migrate)

A production deployment of arena_new consists of **three distinct runtime roles**,
all built from the same Docker image but invoked with different entrypoints. The
diagram below summarises the topology Dokploy should reproduce:

```
                  ┌────────────────────────┐
                  │  arena-migrate         │  (one-shot, runs first)
                  │  /app/arena-migrate up │
                  └───────────┬────────────┘
                              │ goose migrations applied
                              ▼
   ┌──────────────────────────┐      ┌──────────────────────────┐
   │  arena-api               │      │  arena-worker            │
   │  /app/arena-api          │      │  /app/arena-worker       │
   │  Public port: 8080       │      │  Internal metrics: 9091  │
   │  GET /healthz, /readyz   │      │  GET /healthz, /metrics  │
   │  GET /metrics            │      │  Polls worker_jobs,      │
   │                          │      │  dispatches outbox       │
   └──────────────┬───────────┘      └──────────────┬───────────┘
                  │                                 │
                  └─────────────► PostgreSQL ◄──────┘
                              (single source of truth)
```

### 2.1 `arena-api` service (public HTTP)

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running) |
| Docker image | `your-registry/arena-api:<tag>` (built from repo root `Dockerfile`) |
| Entrypoint | `/app/arena-api` (default `CMD` of the image) |
| Listen port | `8080` (`HTTP_LISTEN_ADDR=:8080`) |
| Liveness probe | `GET /healthz` → `200 OK` |
| Readiness probe | `GET /readyz` → `200 OK` once the pgx pool can `Ping` the DB |
| Metrics scrape | `GET /metrics` (Prometheus exposition on `:8080`) |
| Start policy | `Always` |
| Replicas | ≥ 1 (horizontally scalable behind Dokploy's Traefik router) |

Dokploy must expose `8080` as the public HTTP port and route the application
domain to it. Healthcheck is wired by the image's `HEALTHCHECK` directive (see
§7).

### 2.2 `arena-worker` service (background jobs + outbox)

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running, internal only) |
| Docker image | **Same image** as `arena-api` |
| Entrypoint | `/app/arena-worker` (override the image `CMD`) |
| Internal listen | `WORKER_METRICS_ADDR=:9091` (sidecar HTTP for `/healthz` and `/metrics`) |
| Public port | **None** — do not expose `:9091` to the public router |
| Liveness probe | `GET http://<container>:9091/healthz` → `200 OK` |
| Metrics scrape | `GET http://<container>:9091/metrics` (Prometheus) |
| Start policy | `Always` |
| Replicas | 1 by default; the queue uses `FOR UPDATE SKIP LOCKED`, so additional replicas scale safely |

The worker is **mandatory** in production: it drives the platform's job queue
(`worker_jobs`) and dispatches domain events from the `outbox` table. Without a
running worker, side effects (delivery, billing notifications, reconciliation,
scheduled reports, retried webhooks, etc.) accumulate in PostgreSQL and never
fire. Treat it as a first-class production component, not an optional sidecar.

### 2.3 `arena-migrate` one-shot (pre-deploy)

| Property | Value |
|---|---|
| Dokploy service type | **Application** with one-shot / "Run on deploy" start policy |
| Docker image | **Same image** as `arena-api` |
| Entrypoint | `/app/arena-migrate up` |
| Listen port | None (the binary exits when migrations are applied) |
| Start policy | `On Deploy` / `One-shot` (must **not** be `Always`) |
| Deploy order | Runs **before** `arena-api` and `arena-worker` on every deploy |

`arena-migrate` runs the embedded `goose` migrations (`embed.FS`) against
`DATABASE_URL` and exits `0` on success. If it fails, the deploy must abort —
the API and worker rely on the new schema being in place before they boot.

### 2.5 `arena-bot` service (Telegram event-center bot)

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running, internal only) |
| Docker image | **Same image** as `arena-api` |
| Entrypoint | `/app/arena-bot` (override the image `CMD`) |
| Internal listen | `BOT_METRICS_ADDR=:9092` (sidecar HTTP for `/healthz` and `/metrics`) |
| Public port | **None** — the bot long-polls Telegram, nothing connects to it |
| Liveness probe | `GET http://<container>:9092/healthz` → `200 OK` |
| Start policy | `Always` |
| Replicas | **Exactly 1** — Telegram refuses a second poller of the same token (409) |

Spec: `08_architecture/28_telegram_event_center_bot_ru.md`. The bot owns only
the `bot_*` tables and calls `arena-api` over REST (`BOT_ARENA_API_URL`, the
api service's internal address) as the linked user, so it needs the API's
`JWT_SIGNING_SECRET` to mint those tokens. Variables:

| Variable | Service | Value |
|---|---|---|
| `EVENTS_TELEGRAM_BOT_TOKEN` | bot | the bot's token (secret) |
| `EVENTS_TELEGRAM_BOT_USERNAME` | api, worker, bot | `ArenaEventsCentrBot` — the deep links in invitation e-mails |
| `BOT_SERVICE_TOKEN` | api, bot | shared random secret of `POST /v1/bot/invitations/accept`; the route answers 503 while unset |
| `TELEGRAM_MINIAPP_SECRET_KEY` | api | the DERIVED key Telegram signs Mini App launch data with (64 hex characters, never the bot token): `POST /v1/auth/telegram-miniapp` answers 503 while unset. How to make it: below the table |
| `BOT_ARENA_API_URL` | bot | `http://<api service>:8080` |
| `BOT_METRICS_ADDR` | bot | `:9092` |
| `JWT_SIGNING_SECRET` | api, bot | the same value on both |

**`TELEGRAM_MINIAPP_SECRET_KEY` (Telegram Mini App sign-in).** Telegram signs the launch data of a Mini App with a key derived from the bot token: `HMAC-SHA256(key = "WebAppData", message = bot token)`. arena-api gets only that derived value, so a leak of it lets nobody speak as the bot (it still lets a forger sign in as ANY linked person until it is replaced: treat it as a secret). Make it once in PowerShell on your own machine; the token is typed in, not saved, and the result is the value to paste into the api service's environment (api only, not the bot):

```powershell
$token = [Net.NetworkCredential]::new("", (Read-Host "Bot token (EVENTS_TELEGRAM_BOT_TOKEN)" -AsSecureString)).Password
$hmac = [System.Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes("WebAppData"))
-join ($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($token)) | ForEach-Object { $_.ToString("x2") })
```

A new bot token (BotFather revoke) needs a new key. The route also needs the Mini App page to be served under the bot, `frame-ancestors` for `/tg` is the web side's (nginx of the event center).

**Poster reading (since 2026-10-14) lives on `arena-api`, not on the bot.**
The model key `POSTER_LLM_API_KEY` (and `POSTER_LLM_MODEL`,
`POSTER_LLM_BASE_URL`) belongs to the api service only: the bot sends the
poster it uploaded to `POST /v1/organizations/{org_id}/media/{media_id}/poster-facts`
and the api calls the model, counting at most `POSTER_READS_PER_ORG_PER_DAY`
(default 50, `0` = no cap) reads per organization per sliding day. A bot that
still holds the key logs a "set but unused" warning (the value is never
printed); an api without the key answers 503
`media.poster_reader_not_configured` and the wizard asks every question by
hand. **Roll out in this order:** (1) deploy `arena-api` of this release and
add `POSTER_LLM_API_KEY` to its environment (check:
`docker inspect` of the api container shows the variable, and the route
answers 422/404, not 503, for a real media id); (2) deploy `arena-bot` of the
same release, which calls the route; (3) only then delete `POSTER_LLM_API_KEY`
from the bot's environment. Doing (3) before (2) switches the old bot's
poster hints off, doing (2) before (1) makes every poster answer "nothing
found".

### 2.4 Env passthrough checklist (all services)

Every binary loads its configuration with `config.LoadFor(<its role>)`, and
production validation checks only what that binary reads. A variable used by
several services **must have the same value** on each of them, but no service
needs another service's secrets: section 2.6 lists which service gets which
variable. The production rules below apply to the services named in that
table; the database, log-format and debug-flag rules apply to all of them.

> **PR-00 production safety contract**: `APP_ENV=production` enforces the
> following hard rejections at boot time — the process will not start if any
> of these are violated:
> - `JWT_SIGNING_SECRET` must be ≥ 32 bytes and not a known dev placeholder.
> - `ENABLE_DEV_AUTH` must be `false`.
> - `CORS_ALLOWED_ORIGINS` must not contain `*`.
> - `DB_LOG_QUERIES` must be `false`.
> - `DATABASE_URL` must use `sslmode=require` or `sslmode=verify-full`, unless
>   the narrowly-scoped private Docker-network override below is explicitly used.
> - Any `MEDIA_BACKEND` (`local` or `s3`) requires a strong `MEDIA_SIGNING_SECRET`
>   (≥ 32 bytes), the same value on api and worker: the worker signs the media
>   links the api verifies, and production never falls back to the JWT secret.
> - `EMAIL_MODE` must be `smtp` (never `log`).
> - `OUTBOX_MODE` must be `webhook` or `disabled` (never `noop` or empty).
> - `LOG_FORMAT` must be `json`.
> - `BIL24_COMPAT_ENABLED=true` requires `BIL24_REQUIRE_TOKEN=true` (the
>   gateway never mounts without fid/token enforcement in production;
>   feature #390 / PR2-32).
> - `BIL24_COMPAT_ENABLED=true` also requires a non-empty `https://`
>   `API_PUBLIC_URL` (or, on a single-host deployment, `APP_PUBLIC_URL` as its
>   fallback) — that value IS the Bil24 spec's `PUBLIC_BASE_URL`.
>   The gateway's `GET_TICKETS_BY_ORDER` answers absolute ticket links
>   (`API_PUBLIC_URL` + `/v1/public/checkout/<token>/tickets/<uuid>/pdf`) and
>   `GET_ALL_ACTIONS` answers absolute signed poster URLs on the same origin;
>   an empty base yields host-less `pdfUrl`/`downloadUrl` values no buyer
>   can open (feature #495 / W1-B2b, spec §7.10/§16; feature #535).

#### Mandatory shared variables

| Variable | Required by | Production value | Notes |
|---|---|---|---|
| `APP_ENV` | api / worker / migrate | `production` | Enables all production safety checks |
| `DATABASE_URL` | api / worker / migrate | `postgres://user:pw@host:5432/db?sslmode=require` | **sslmode=require** or verify-full is mandatory |
| `LOG_LEVEL` | api / worker / migrate | `info` | |
| `LOG_FORMAT` | api / worker / migrate | `json` | **Must be json** in production |
| `APP_NAME` | api / worker | `arena-api` / `arena-worker` | |
| `APP_VERSION` | api / worker / migrate | *(injected by CI)* | |
| `APP_COMMIT` | api / worker / migrate | *(injected by CI)* | |
| `JWT_SIGNING_SECRET` | api / bot | *(strong random, ≥ 32 bytes)* | Shared symmetric key; must not be a dev placeholder |
| `ENABLE_DEV_AUTH` | all (or unset) | `false` | **Must be false**; refused in production by every service |
| `APP_PUBLIC_URL` | api / worker | `https://app.example.com` | Canonical **SPA origin** for emails and webhooks; distinct from the API base URL and never derived from request headers. Also the single-host fallback for `API_PUBLIC_URL` |
| `API_PUBLIC_URL` | api / worker | `https://api.example.com` | Canonical **API origin**. Every link a third-party site must FETCH is built on it: Bil24 `base_url`/`image_url`, `GET_ALL_ACTIONS` signed poster URLs, `GET_TICKETS_BY_ORDER` `pdfUrl`. Empty falls back to `APP_PUBLIC_URL`; the effective value is mandatory and `https://` when `BIL24_COMPAT_ENABLED=true` (feature #535) |
| `OUTBOX_MODE` | worker | `webhook` or `disabled` | `noop` and empty are forbidden in production |
| `EMAIL_MODE` | worker | `smtp` | `log` is forbidden in production |
| `ALLOW_PRIVATE_DB_PLAINTEXT` | api / worker / migrate | `false` | Set `true` only for an unqualified Docker service host or private IP when in-network PostgreSQL has no TLS. Never use for a managed/external DB. |

#### Email delivery (when EMAIL_MODE=smtp)

| Variable | Required by | Notes |
|---|---|---|
| `SMTP_HOST` | worker | SMTP server hostname |
| `SMTP_PORT` | worker | Default: `587` (STARTTLS) |
| `SMTP_USERNAME` | worker | SMTP auth username |
| `SMTP_PASSWORD` | worker | SMTP auth password |
| `SMTP_FROM` | worker | Envelope from address, e.g. `tickets@arena.example.com` |
| `SMTP_USE_TLS` | worker | `true` recommended |

#### Brevo SMTP production values

Create a Brevo SMTP key in the Brevo console and store it as a Dokploy secret;
do not put it in Git or a shared non-secret environment group. Set these on
**arena-worker** (the only process that sends mail):

| Variable | Brevo value |
|---|---|
| `EMAIL_MODE` | `smtp` |
| `SMTP_HOST` | `smtp-relay.brevo.com` |
| `SMTP_PORT` | `587` |
| `SMTP_USERNAME` | Brevo SMTP login (usually the account email) |
| `SMTP_PASSWORD` | Brevo SMTP key (Dokploy secret) |
| `SMTP_FROM` | A Brevo-verified sender address |
| `SMTP_USE_TLS` | `true` |

For per-organizer sender identities, also set `BREVO_API_KEY` as a Dokploy
secret on **arena-api** and **arena-worker**. This API key only reads Brevo
sender/DNS verification state; organizers never supply SMTP credentials.

`EMAIL_MODE` and the SMTP variables belong to the worker only: API, bot and
migrate never instantiate an SMTP sender and do not validate them.

#### Outbox webhook (when OUTBOX_MODE=webhook)

| Variable | Required by | Notes |
|---|---|---|
| `OUTBOX_WEBHOOK_URL` | worker | HTTP endpoint for outbox event delivery |
| `OUTBOX_SIGNING_SECRET` | worker | HMAC-SHA256 key for `X-Arena-Signature` header |

#### Service-specific variables

| Variable | Required by | Default | Notes |
|---|---|---|---|
| `HTTP_LISTEN_ADDR` | api only | `:8080` | |
| `WORKER_METRICS_ADDR` | worker only | `:9091` | Do **not** expose publicly |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api / worker | *(empty)* | OTLP gRPC collector |
| `OTEL_SERVICE_NAME` | api / worker | | `arena-api` / `arena-worker` |
| `REDIS_URL` | api / worker | | Required once lock/hot-cache features land |
| `DB_POOL_MIN_CONNS` / `DB_POOL_MAX_CONNS` | api / worker | 2 / 20 | Tune independently |
| `SHUTDOWN_TIMEOUT` | api / worker | `20s` | |
| `DB_LOG_QUERIES` | api / worker | `false` | **Must be false** in production |

> Tip: in Dokploy ≥ 0.6 you can attach a **Shared Environment** group to all
> three services and only override the service-specific entries (`APP_NAME`,
> `HTTP_LISTEN_ADDR`, `OTEL_SERVICE_NAME`, `WORKER_METRICS_ADDR`).

### 2.6 Which service gets which variable

Production validation is per binary (`config.LoadFor(config.RoleAPI)` in
`cmd/arena-api`, `RoleWorker`, `RoleBot`, `RoleMigrate`; arena-seed uses
`RoleMigrate`). A service starts with only the variables marked for it below,
and a secret of another service in its environment is NOT an error: the
binary logs one `config warning` naming the unused secrets (names only, never
values) so it can be removed. The secret list lives in
`apps/backend/internal/platform/config/roles.go` (`roleSecrets`); keep it and
this table in step. `tests/staticanalysis/dokploy_env_table_test.go` fails
when a variable `config.go` reads is missing from this table.

✅ = required in production, ● = read when set (optional or with a default),
— = never read, do not give it.

**`APP_ENV` is required on all four services.** Without it a container runs
as `development`: every production check is off and `ENABLE_DEV_AUTH`
defaults to `true` (dev tokens accepted by the api). Each binary then logs a
`config warning` "APP_ENV is not set" at start.

| Variable | api | worker | bot | migrate |
|---|---|---|---|---|
| `APP_ENV` (`production` / `staging`) | ✅ | ✅ | ✅ | ✅ |
| `DATABASE_URL` (`sslmode=require`, or `ALLOW_PRIVATE_DB_PLAINTEXT`) | ✅ | ✅ | ✅ | ✅ |
| `ALLOW_PRIVATE_DB_PLAINTEXT` | ● | ● | ● | ● |
| `EGRESS_ALLOW_PRIVATE_TARGETS` (local stand only, refused in production) | ● | ● | — | — |
| `APP_NAME`, `APP_VERSION`, `APP_COMMIT`, `LOG_LEVEL` | ● | ● | ● | ● |
| `LOG_FORMAT` (`json` in production) | ● | ● | ● | ● |
| `DB_POOL_MIN_CONNS`, `DB_POOL_MAX_CONNS`, `DB_POOL_MAX_CONN_LIFETIME`, `DB_POOL_MAX_CONN_IDLE_TIME` | ● | ● | ● | ● |
| `DEFAULT_LOCALE`, `ACTIVE_LOCALES` | ● | ● | — | — |
| `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_INSECURE`, `OTEL_SERVICE_NAME`, `OTEL_TRACES_SAMPLER_ARG` | ● | ● | — | — |
| `SHUTDOWN_TIMEOUT` | ● | ● | — | — |
| `HEALTH_ADDR` (read by `arena-healthcheck` inside the container) | ● | ● | ● | — |
| `JWT_SIGNING_SECRET` | ✅ | — | ✅ | — |
| `JWT_ISSUER`, `JWT_AUDIENCE` (same values on api and bot) | ● | — | ● | — |
| `JWT_DEFAULT_TTL` | ● | — | — | — |
| `WEB_SESSION_IDLE_HOURS` (idle window of a web event center session, 0..720, default 72) | ● | — | — | — |
| `HTTP_LISTEN_ADDR`, `BODY_LIMIT_BYTES`, `REQUEST_TIMEOUT_SECONDS` | ● | — | — | — |
| `CORS_ALLOWED_ORIGINS` (no `*`) | ✅ | — | — | — |
| `TRUSTED_PROXY_COUNT` | ● | — | — | — |
| `PUBLIC_FEED_TOKEN_RATE_LIMIT`, `PUBLIC_CHECKOUT_TOKEN_RATE_LIMIT`, `PUBLIC_API_IP_RATE_LIMIT`, `PUBLIC_PROMO_FAIL_TOKEN_RATE_LIMIT`, `PUBLIC_PROMO_FAIL_IP_RATE_LIMIT`, `PUBLIC_PAGE_TRACK_EVENT_RATE_LIMIT`, `PUBLIC_PAGE_TRACK_IP_RATE_LIMIT` | ● | — | — | — |
| `WIDGET_PAYMENT_WINDOW_SECONDS`, `WIDGET_PAYMENT_GRACE_SECONDS` | ● | — | — | — |
| `STRIPE_WEBHOOK_SECRET` | ✅ | — | — | — |
| `ALLPAY_WEBHOOK_SECRET` (deprecated, verifies nothing) | ● | — | — | — |
| `BIL24_COMPAT_ENABLED`, `BIL24_REQUIRE_TOKEN`, `BIL24_TOKEN_CACHE_TTL` | ● | — | — | — |
| `BREVO_WEBHOOK_TOKEN`, `BREVO_WEBHOOK_TOKEN_PREVIOUS` (≥ 32 random chars) | ● | — | — | — |
| `BREVO_API_KEY` | ● | ● | — | — |
| `BREVO_API_BASE_URL` (production: unset or `https://api.brevo.com`) | ● | ● | — | — |
| `APP_PUBLIC_URL` (`https://`) | ✅ | ✅ | — | — |
| `API_PUBLIC_URL` (`https://` when set; required with the Bil24 gateway) | ● | ● | — | — |
| `EVENT_CENTER_PUBLIC_URL` (`https://` when set) | ● | ● | ● | — |
| `BOT_SCHEME_BUTTON` (`off` default, `url` or `web_app`: the «Схема» button on the card of a seated date; needs `EVENT_CENTER_PUBLIC_URL` on the bot; turn it on only after the `/tg` screen of the event center is live; `web_app` opens `<EVENT_CENTER_PUBLIC_URL>/tg?next=<seats path>` and also needs `POST /v1/auth/telegram-miniapp` with `TELEGRAM_MINIAPP_SECRET_KEY` on the API) | — | — | ● | — |
| `PUBLIC_TICKETS_BASE_URL` | ● | ● | ● | — |
| `SHOP_PUBLIC_BASE_URL` (`https://` when set, the platform storefront origin; the shop order e-mail links `<this>/<org slug>/checkout/<token>` for an organization without its own `shop_domain`; empty = no status link) | — | ● | — | — |
| `SHORT_LINK_BASE_URL` (`https://`, host only, default `https://go.arenasoldout.com`, the host printed under a QR code; setup: `docs/ops/sales_links_ru.md`) | ● | — | — | — |
| `SHORT_LINK_FALLBACK_URL` (`https://` when set; where `GET /go/{code}` sends an unknown code; empty = `PUBLIC_TICKETS_BASE_URL`, else `APP_PUBLIC_URL`) | ● | — | — | — |
| `ONBOARDING_ENABLED`, `ONBOARDING_TERMS_URL`, `ONBOARDING_PRIVACY_URL` | ● | — | — | — |
| `PLATFORM_TERMS_URL` (buyer checkout consent; empty = `<ONBOARDING_SITE_URL>/legal/terms`) | ● | — | — | — |
| `ONBOARDING_SITE_URL` | ● | ● | — | — |
| `ONBOARDING_TURNSTILE_SECRET`, `CONTACT_FORM_TURNSTILE_SECRET`, `CONTACT_FORM_TURNSTILE_SITE_KEY` | ● | ● | — | — |
| `MEDIA_BACKEND`, `MEDIA_LOCAL_ROOT`, `MEDIA_S3_ENDPOINT`, `MEDIA_S3_REGION`, `MEDIA_S3_BUCKET`, `MEDIA_S3_ACCESS_KEY_ID`, `MEDIA_S3_SECRET_ACCESS_KEY`, `MEDIA_S3_USE_PATH_STYLE` | ● | ● | — | — |
| `MEDIA_SIGNING_SECRET` (same value on api and worker) | ✅ with a media backend | ✅ with a media backend | — | — |
| `BOT_SERVICE_TOKEN` | ● | — | ● | — |
| `TELEGRAM_MINIAPP_SECRET_KEY` | ● | — | — | — |
| `EVENTS_TELEGRAM_BOT_USERNAME` | ● | ● | — | — |
| `METRICS_BEARER_TOKEN` | ● | ● | ● | — |
| `EMAIL_MODE` (`smtp`), `SMTP_HOST`, `SMTP_FROM` | — | ✅ | — | — |
| `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_USE_TLS` | — | ● | — | — |
| `OUTBOX_MODE` (`webhook`/`disabled`) | — | ✅ | — | — |
| `OUTBOX_WEBHOOK_URL`, `OUTBOX_SIGNING_SECRET` | — | ✅ with `OUTBOX_MODE=webhook` | — | — |
| `OUTBOX_POLL_INTERVAL`, `WORKER_POLL_INTERVAL`, `WORKER_METRICS_ADDR`, `SENDER_VERIFICATION_POLL_INTERVAL` | — | ● | — | — |
| `OPS_TELEGRAM_BOT_TOKEN`, `OPS_TELEGRAM_CHAT_ID`, `OPS_ALERT_ENV_LABEL`, `OPS_WATCHDOG_HEARTBEAT_HOUR_UTC` | — | ● | — | — |
| `SALES_TELEGRAM_BOT_TOKEN` | — | ● | — | — |
| `BILLING_COLLECT_SINCE` (RFC 3339, empty = billing collection off; the owner names the date X) | — | ● | — | — |
| `BILLING_STRIPE_SECRET_KEY`, `BILLING_STRIPE_WEBHOOK_SECRET` (the PLATFORM's Stripe account for organizer invoices and credit packs; empty = card payment off) | ● | — | — | — |
| `BILLING_BANK_WEBHOOK_SECRET` (HMAC key of bank payment notifications; empty = the bank webhook answers 503) | ● | — | — | — |
| `BILLING_ORGANIZER_ENABLED` (`true` opens credit packs and platform invoices to organizers; default off = those routes answer 404 `billing.organizer_disabled`, the operator console is unaffected) | ● | — | — | — |
| `EVENTS_TELEGRAM_BOT_TOKEN` | — | — | ✅ | — |
| `BOT_ARENA_API_URL`, `BOT_METRICS_ADDR`, `BOT_SELF_ONBOARDING_ENABLED`, `BOT_ONBOARDING_SITE_BUTTON` | — | — | ● | — |
| `BOT_MINIAPP_EDITOR_ENABLED` (`true` shows the «Править в редакторе» web_app button in the bot, spec 44 RT-12a; default off. Needs `EVENT_CENTER_PUBLIC_URL` with https on the API and the `/tg/` nginx location of the event center. Until RT-12b the page is a placeholder, so leave it off in production. To roll back, turn it off and redeploy the bot FIRST, then roll back the API image, the migration last: docs/ops/event_center_rollout_ru.md section 4) | — | — | ● | — |
| `POSTER_LLM_API_KEY` (secret), `POSTER_LLM_MODEL` | ● | — | — | — |
| `POSTER_LLM_BASE_URL` (production: unset or `https://api.anthropic.com`) | ● | — | — | — |
| `POSTER_LLM_PROVIDER` (`anthropic` default or `gemini`; the vision provider that reads posters), `POSTER_LLM_FALLBACK_PROVIDER` (empty = none, else the other provider, tried once when the first fails or times out) | ● | — | — | — |
| `GEMINI_API_KEY` (secret; the key of the `gemini` provider), `POSTER_GEMINI_MODEL` (default `gemini-3.8-flash`) | ● | — | — | — |
| `POSTER_GEMINI_BASE_URL` (test seam; production: unset or `https://generativelanguage.googleapis.com`) | ● | — | — | — |
| `POSTER_READS_PER_ORG_PER_DAY` (default 50, `0` = no cap) | ● | — | — | — |
| `TEXT_FORMAT_LLM_MODEL` (default `claude-haiku-5-5`; the key and the base URL are the poster ones: `POSTER_LLM_API_KEY`, `POSTER_LLM_BASE_URL`) | ● | — | — | — |
| `TEXT_FORMATS_PER_USER_PER_DAY` (default 100, `0` = no cap), `TEXT_FORMATS_PER_ORG_PER_DAY` (default 500, `0` = no cap) | ● | — | — | — |
| `ENABLE_DEV_AUTH`, `DB_LOG_QUERIES`, `DEBUG_ROUTES_ENABLED`, `FAULT_INJECT_OUTBOX_AFTER_AUDIT` (leave unset; `true` is refused in production) | — | — | — | — |
| `STRIPE_API_BASE_URL`, `FLITT_API_BASE_URL`, `ALLPAY_API_BASE_URL` (test seams; refused in production) | — | — | — | — |
| `REDIS_URL`, `IDEMPOTENCY_TTL`, `IDEMPOTENCY_KEY_MAX_LENGTH`, `WORKER_CONCURRENCY`, `WORKER_JOB_TIMEOUT`, `WORKER_RETRY_BACKOFF_BASE`, `WORKER_RETRY_BACKOFF_MAX`, `OUTBOX_BATCH_SIZE` (parsed, but no binary uses them today) | — | — | — | — |

Never set in production, on any service: `ENABLE_DEV_AUTH=true`,
`DB_LOG_QUERIES=true`, `DEBUG_ROUTES_ENABLED=true`,
`FAULT_INJECT_OUTBOX_AFTER_AUDIT=true`, `STRIPE_API_BASE_URL`,
`FLITT_API_BASE_URL`, `ALLPAY_API_BASE_URL`, and `BREVO_API_BASE_URL` /
`POSTER_LLM_BASE_URL` / `POSTER_GEMINI_BASE_URL` with anything but their default (every role refuses
to start: a changed host would receive the API key).

**Staging** follows the same split. With `APP_ENV=staging` the production
checks are off, but `MEDIA_SIGNING_SECRET` is still required on api and
worker for any `MEDIA_BACKEND`: the JWT-secret fallback exists only in
development, and a worker without the media key would put unsigned links in
letters (logos and posters vanish).

Before removing a variable from a container, deploy an image that contains
`config.LoadFor` (2026-10-14 or later): an older image validates every
service's variables in every container and will not start without them.

---

## 3. Create the Application in Dokploy

1. Log in to your Dokploy dashboard.
2. Navigate to **Projects** → **New Project** (or open an existing project).
3. Click **+ Add Service** → **Application**.
4. Set the **Application Name** to `arena-api` (or your preferred name).
5. Under **Source**, select **Git Repository** and enter your repository URL.
   - Branch: `master` (the single production release branch — see §13).
6. Under **Build**, select **Dockerfile** and set the **Dockerfile path** to:
   ```
   Dockerfile
   ```
   *(The `Dockerfile` is at the repository root — no subdirectory needed.)*
7. Leave **Build context** as the repository root (`.`).
8. Click **Save**.

---

## 4. Attach a Managed PostgreSQL Service

### Option A — Dokploy Managed Database (recommended)

1. In your Dokploy project, click **+ Add Service** → **Database** → **PostgreSQL 17**.
2. Set a strong password. Copy the generated **Internal Connection String** — you
   will need it for `DATABASE_URL` in §4.
3. Once the database service is `Running`, connect it to your `arena-api`
   application via **Service Links** (or set `DATABASE_URL` manually).

#### Private Docker-network PostgreSQL without TLS

Prefer enabling TLS on PostgreSQL or using a managed TLS endpoint. If the
Dokploy PostgreSQL 17 service is reachable only through its private Docker
network and does not support TLS, this release supports an audited temporary
exception. Set the following on API, worker, and migrate:

```text
DATABASE_URL=postgres://USER:PASSWORD@postgres:5432/DB?sslmode=disable
ALLOW_PRIVATE_DB_PLAINTEXT=true
```

The application rejects this flag for dotted/public hostnames; it only accepts
loopback, private/link-local IPs, or an unqualified Docker service name. Do not
publish port 5432 and remove the override once PostgreSQL TLS is enabled.

### Option B — External PostgreSQL

Set `DATABASE_URL` directly to your external DSN (e.g., a managed cloud database).

---

## 5. Configure Environment Variables

In Dokploy, open your `arena-api` application → **Environment Variables** tab.
Set each variable below.  A full list of *optional* tuning variables is in
[`.env.example`](../.env.example) at the repository root.

### Mandatory Production Variables (enforced by config.LoadFor at boot)

> `APP_ENV=production` causes the process to exit at boot if any of these
> constraints are violated. See `apps/backend/internal/platform/config/config.go`
> for the full `validateProduction()` implementation. Each row applies only
> to the services that read the variable (section 2.6).

| Variable | Production value | Constraint |
|---|---|---|
| `APP_ENV` | `production` | Required; enables all safety checks |
| `DATABASE_URL` | `postgres://user:pass@host:5432/db?sslmode=require` | **sslmode=disable / allow are rejected** |
| `JWT_SIGNING_SECRET` | *(random, ≥ 32 bytes)* | **Empty, short, or known dev placeholder are rejected** |
| `ENABLE_DEV_AUTH` | `false` | **`true` is rejected** |
| `LOG_FORMAT` | `json` | **`text` is rejected** |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `CORS_ALLOWED_ORIGINS` | `https://your-frontend.example.com` | **Wildcard `*` is rejected** |
| `DB_LOG_QUERIES` | `false` | **`true` is rejected** |
| `EMAIL_MODE` | `smtp` | **`log` and empty are rejected** |
| `OUTBOX_MODE` | `webhook` or `disabled` | **`noop` and empty are rejected** |
| `APP_PUBLIC_URL` | `https://app.example.com` | Required on api and worker; the SPA origin used for canonical email links, separate from the API base URL |
| `API_PUBLIC_URL` | `https://api.example.com` | The API origin third parties fetch from; required (directly or via the `APP_PUBLIC_URL` fallback) when `BIL24_COMPAT_ENABLED=true` |
| `SMTP_HOST` | SMTP server hostname | Required when EMAIL_MODE=smtp |
| `SMTP_FROM` | `tickets@arena.example.com` | Required when EMAIL_MODE=smtp |

### Recommended Tuning Variables

| Variable | Recommended value | Notes |
|---|---|---|
| `APP_NAME` | `arena-api` | Appears in logs and traces |
| `APP_VERSION` | *(set by CI, e.g. `1.0.0`)* | Semver for observability dashboards |
| `APP_COMMIT` | *(set by CI, e.g. `abc1234`)* | Git SHA for alerting correlation |
| `HTTP_LISTEN_ADDR` | `:8080` | Keep default; Dokploy routes to this port |
| `DB_POOL_MIN_CONNS` | `2` | Minimum idle connections in pgx pool |
| `DB_POOL_MAX_CONNS` | `20` | Maximum connections; tune to DB tier limits |
| `REDIS_URL` | `redis://redis:6379/0` | Required when lock / hot-cache features ship |
| `SHUTDOWN_TIMEOUT` | `20s` | Graceful shutdown window |
| `OTEL_SERVICE_NAME` | `arena-api` | Service name in traces |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `host:4317` | OTLP gRPC endpoint for traces; leave empty to disable |
| `OTEL_TRACES_SAMPLER_ARG` | `0.1` | 10 % sampling; increase for debugging |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` | Set `true` only for localhost/sidecar collectors; a startup warning is emitted when `true` with a non-localhost endpoint |
| `JWT_ISSUER` | `arena-api` | JWT issuer claim (default: arena-dev) |
| `JWT_AUDIENCE` | `arena-api` | JWT audience claim |
| `JWT_DEFAULT_TTL` | `1h` | Access token lifetime |
| `WORKER_CONCURRENCY` | `4` | Parallel job dispatch threads |
| `WORKER_POLL_INTERVAL` | `1s` | Queue polling cadence |
| `OUTBOX_WEBHOOK_URL` | `https://receiver.example.com/events` | Required when OUTBOX_MODE=webhook |
| `OUTBOX_SIGNING_SECRET` | *(random, ≥ 32 bytes)* | HMAC key for X-Arena-Signature; required with webhook mode |

---

## 6. Run Database Migrations

**Migrations must run before `arena-api` starts.**  The `arena-migrate` binary
is embedded in the same Docker image as `arena-api`, but it must be invoked
separately — it is a one-shot command, not a daemon.

### Recommended: Pre-deploy Job in Dokploy

1. In your Dokploy project, create a second **Application** named `arena-migrate`.
2. Use the **same Docker image** (same repository, same `Dockerfile`).
3. Override the **Entrypoint** to `/app/arena-migrate up`.
4. Set **the same environment variables** as `arena-api` (at minimum `DATABASE_URL`
   and `APP_ENV`).
5. Set the **Start Policy** to `On Deploy` / one-shot (not `Always`).
6. In **Deploy Order**, place `arena-migrate` **before** `arena-api`.

Dokploy will then run migrations automatically before the API starts on every
deployment.

### Alternative: init-container pattern

If your Dokploy version or hosting setup supports init-containers, you can add
`arena-migrate up` as an init step that blocks the `arena-api` container from
starting until migrations succeed.

### Manual one-off execution

```bash
# From within the Dokploy host (or via `dokploy exec`):
docker run --rm \
  --env DATABASE_URL="postgres://user:pass@host:5432/dbname?sslmode=require" \
  --env APP_ENV=production \
  --env ENABLE_DEV_AUTH=false \
  --env LOG_LEVEL=info \
  --entrypoint /app/arena-migrate \
  your-registry/arena-api:latest up
```

**Rollback:**

```bash
# Roll back the last migration:
docker run --rm ... --entrypoint /app/arena-migrate your-registry/arena-api:latest down 1
```

---

## 7. Healthcheck & Port Configuration

The shared image contains a `HEALTHCHECK` directive that Dokploy honours
automatically:

```dockerfile
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/app/arena-healthcheck"]
```

`arena-healthcheck` is a tiny static Go binary that resolves the probe URL
from environment variables (deterministic resolution order) and performs
`GET <resolved-url>/healthz`, exiting 0 on HTTP 200.

### 7.1 Healthcheck resolution order

The probe URL is determined in this order — the first matching rule wins:

| Priority | Rule | Example result |
|---|---|---|
| 1 | `HEALTH_ADDR` is set | `http://localhost:8080` (use as-is) |
| 2 | `APP_NAME` contains `worker` | derive from `WORKER_METRICS_ADDR` (default `:9091`) → `http://localhost:9091` |
| 3 | Default (API role) | derive from `HTTP_LISTEN_ADDR` (default `:8080`) → `http://localhost:8080` |

**Always set `HEALTH_ADDR` explicitly** in Dokploy and docker-compose to avoid
relying on the `APP_NAME` heuristic at runtime. See the compose file for the
canonical values:

```yaml
# arena-api service
HEALTH_ADDR: "http://localhost:8080"

# arena-worker service
HEALTH_ADDR: "http://localhost:9091"
```

### 7.2 `arena-api` healthcheck configuration

| Property | Value |
|---|---|
| Probe URL | `http://localhost:8080/healthz` |
| `HEALTH_ADDR` env | `http://localhost:8080` |
| Expose port | `8080` (`EXPOSE 8080` in Dockerfile) |
| Dokploy healthcheck path | `/healthz` |
| Protocol | HTTP |

| Endpoint | Purpose | Expected response |
|---|---|---|
| `GET /healthz` | Liveness — process alive | `200 OK {"status":"ok"}` |
| `GET /readyz`  | Readiness — DB reachable | `200 OK` (or `503` during startup) |

Dokploy will forward traffic to port 8080 and mark the container unhealthy if
`/healthz` fails three consecutive checks.

### 7.3 `arena-worker` healthcheck configuration

| Property | Value |
|---|---|
| Probe URL | `http://localhost:9091/healthz` |
| `HEALTH_ADDR` env | `http://localhost:9091` |
| `WORKER_METRICS_ADDR` env | `:9091` |
| Public port | **None** — do not expose `:9091` to the public Traefik router |

The worker exposes a lightweight sidecar HTTP server on `WORKER_METRICS_ADDR`
(default `:9091`) with two endpoints:

| Endpoint | Purpose | Expected response |
|---|---|---|
| `GET /healthz` | Liveness — worker process alive | `200 OK {"status":"ok"}` |
| `GET /metrics` | Prometheus scrape | Prometheus exposition format |

**Important:** the sidecar port `:9091` must never be exposed as a public
Dokploy port. It is internal-only (Prometheus scraping, container health
checks). Only `arena-api`'s port `8080` routes public traffic.

---

## 8. Deploy

1. In the Dokploy dashboard, open the `arena-api` application.
2. Click **Deploy** (or push to `master` if auto-deploy is configured — see §13).
3. Monitor the **Logs** tab — look for:
   ```
   {"level":"INFO","msg":"server started","addr":":8080"}
   ```
4. Once the container is `Healthy`, verify:
   ```bash
   curl https://your-domain.example.com/healthz
   # → 200 OK  {"status":"ok"}

   curl https://your-domain.example.com/readyz
   # → 200 OK  {"status":"ok"}
   ```

---

## 9. Post-Deploy Smoke Checks

Run these checks **immediately after every production deploy**, before
announcing the deploy as successful. They cover all three runtime roles.

### 9.1 `arena-migrate` finished cleanly

```bash
# Dokploy → arena-migrate → Logs (last run):
# Expect a final line similar to:
#   {"level":"INFO","msg":"migrations applied","applied":N}
# Container exit code must be 0; if it is non-zero, abort the deploy and
# investigate before bringing arena-api up against an inconsistent schema.
```

### 9.2 `arena-api` liveness, readiness, and version

```bash
# Liveness
curl -fsS https://your-domain.example.com/healthz
# → 200 {"status":"ok"}

# Readiness (also asserts pgx can reach PostgreSQL)
curl -fsS https://your-domain.example.com/readyz
# → 200 {"status":"ok"}

# Confirm the deployed build matches CI (APP_VERSION / APP_COMMIT)
curl -fsS https://your-domain.example.com/healthz | jq .
```

### 9.3 `arena-worker` liveness and metrics

The worker has no public route; check it from the Dokploy host or via
`dokploy exec` into any container on the same internal network:

```bash
# Liveness (sidecar HTTP on WORKER_METRICS_ADDR, default :9091)
curl -fsS http://arena-worker:9091/healthz
# → 200 {"status":"ok"}

# Confirm Prometheus exposition is live
curl -fsS http://arena-worker:9091/metrics | head -n 20
# → expect arena_outbox_backlog, arena_worker_jobs_*, process_* gauges
```

### 9.4 Outbox / job queue is draining

```bash
# From a DB shell or psql connected via DATABASE_URL:
SELECT count(*) FROM outbox WHERE dispatched_at IS NULL;
-- → expect a small, decreasing number; a steadily growing value means
--    arena-worker is not running or is stuck.

SELECT status, count(*) FROM worker_jobs GROUP BY status;
-- → expect mostly 'done'; investigate any 'failed' rows.
```

### 9.5 Stripe webhook registration

After deploying, register the platform's payment webhook in your Stripe Dashboard
(**Developers → Webhooks → Add endpoint**):

1. **Endpoint URL:** `https://api.arenasoldout.com/v1/payment-intents/webhook`
   *(or your deployment's API base URL — visible in Admin → Payment Configs → "Stripe webhook setup").*
2. **Events to listen to:** select all of the following:
   - `payment_intent.succeeded`
   - `payment_intent.payment_failed`
   - `payment_intent.requires_action`
   - `payment_intent.processing`
   - `payment_intent.amount_capturable`
   - `payment_intent.manual_review`
3. Save the endpoint, then click **Reveal signing secret** to get the `whsec_…` value.
4. In Admin → Payment Configs, open (or create) the Stripe config for the org and
   paste the signing secret into the **webhook_secret** field. The status badge
   changes from "missing required fields" to "configured" once both `api_key`
   and `webhook_secret` are stored.

> **Test vs. live:** Stripe issues separate signing secrets for test mode and live
> mode. Create a separate Payment Config for each mode.

### 9.6 Logs sanity

In Dokploy **Logs** for both `arena-api` and `arena-worker`, verify:

- Log lines are valid JSON (`LOG_FORMAT=json`).
- No repeated `ERROR` entries.
- `arena-api` shows `{"msg":"server started","addr":":8080"}`.
- `arena-worker` shows `{"msg":"arena-worker metrics server listening","addr":":9091"}` and periodic `{"msg":"outbox backlog","count":...}` ticks.

If any of the above fails, mark the deploy as failed in Dokploy and roll back
to the previous successful revision before debugging.

---

## 10. Production Environment Variable Checklist

Use this checklist before every first deploy and whenever environment variables
change.  See [`.env.example`](../.env.example) for full documentation of every
variable.

> **Long-form authoritative version:** see
> [`../00_project_control/PRODUCTION_HARDENING_CHECKLIST.md`](../00_project_control/PRODUCTION_HARDENING_CHECKLIST.md)
> (feature #193). That document covers Grafana credentials, internal-port
> exposure (`55432`, `56379`, `9090`, `9091`, `3000`), post-deploy
> in-container `env` verification, and `DB_LOG_QUERIES`. The short list
> below is the quick operator reference; the long-form is the gate.

### ✅ Security-Critical (must not use dev defaults)

- [ ] `APP_ENV=production`
- [ ] `ENABLE_DEV_AUTH=false` — dev JWT stub **must** be disabled
- [ ] `JWT_SIGNING_SECRET` — set to a strong random secret (≥ 32 bytes, not the
  dev placeholder `dev-only-do-not-use-in-prod`)
- [ ] `DATABASE_URL` — uses `sslmode=require` or `sslmode=verify-full`
- [ ] `CORS_ALLOWED_ORIGINS` — restricted to your frontend domain(s), not `*`

### ✅ Observability

- [ ] `LOG_LEVEL=info` (or `warn` in high-traffic production)
- [ ] `LOG_FORMAT=json`
- [ ] `OTEL_EXPORTER_OTLP_ENDPOINT` — set if using distributed tracing
- [ ] `OTEL_SERVICE_NAME=arena-api`
- [ ] `OTEL_EXPORTER_OTLP_INSECURE=false` — required when collector is remote (non-localhost); `true` only for sidecar/localhost
- [ ] `APP_VERSION` — set by CI to the deployed semver
- [ ] `APP_COMMIT` — set by CI to the deployed git SHA

### ✅ Database

- [ ] `DATABASE_URL` — correct host, port, database name, credentials
- [ ] `DB_POOL_MAX_CONNS` — tuned to PostgreSQL connection limit
- [ ] Migrations have been applied via `arena-migrate up` before the first start

### ✅ Optional / Future Milestones

- [ ] `REDIS_URL` — required once lock / hot-cache features land (next milestone)
- [ ] `OTEL_TRACES_SAMPLER_ARG` — tune sampling rate for your traffic volume

---

## 11. Secret Rotation

> **Note:** A formal secret-rotation procedure is **out of scope for this
> (Backend Foundation) milestone.**  The following section documents *where*
> rotation is expected to be implemented in subsequent milestones.

### Current state (milestone 1)

- `JWT_SIGNING_SECRET` is used by the dev-stub HS256 identity provider.
  It is a single symmetric secret; rotation requires a restart.
- Database credentials are embedded in `DATABASE_URL`.
- No other secrets are used in this milestone.

### Expected in subsequent milestones

| Secret | Expected rotation mechanism | Milestone |
|---|---|---|
| JWT signing key | Replace HS256 stub with RS256 asymmetric keys; support key-id rotation without restart (JWKS endpoint) | Identity module milestone |
| Database password | Dokploy / Vault dynamic credentials; zero-downtime via pgBouncer connection pooling | Infrastructure hardening milestone |
| Redis AUTH token | Dokploy secret injection; no application restart required (reconnect on auth failure) | Infrastructure hardening milestone |
| OTLP / observability API keys | Injected via Dokploy secrets; rotated out-of-band | Infrastructure hardening milestone |

Until those milestones ship, rotate secrets by:

1. Generating the new secret value.
2. Updating the Dokploy **Environment Variables** for the application.
3. Triggering a redeploy (new containers pick up the new secret on start).

---

## 12. Troubleshooting

### Container exits immediately after start

Check the Dokploy **Logs** tab for a config-validation error.  Common causes:

- `DATABASE_URL` is empty or malformed.
- `ENABLE_DEV_AUTH=true` but `JWT_SIGNING_SECRET` is not set.
- `APP_ENV` is not one of `development`, `staging`, `production`.

### `/readyz` returns 503

The API is alive but cannot reach PostgreSQL.  Verify:

- `DATABASE_URL` is correct (host, port, credentials, database name).
- The PostgreSQL service is running and accepting connections.
- Network policies / firewall rules allow the container to reach the DB port.

### Migrations fail to run

- Ensure `DATABASE_URL` in the `arena-migrate` job matches the production DB.
- Check that the DB user has `CREATE TABLE`, `ALTER TABLE`, and `CREATE INDEX`
  privileges (needed by goose).
- Check for migration version conflicts: run
  `arena-migrate status` to see which migrations have been applied.

### `arena-worker` is running but jobs / outbox are not draining

- Check that `arena-worker` is actually started in Dokploy (not just defined).
  Its sidecar `:9091/healthz` must return `200`.
- Confirm `DATABASE_URL` points to the **same** PostgreSQL instance as
  `arena-api`. A mismatched DSN means the worker is polling an empty queue.
- Grep `arena-worker` logs for `handler not registered` — unknown job types
  are parked as `failed` until a handler ships.
- `SELECT count(*) FROM outbox WHERE dispatched_at IS NULL;` should trend
  toward zero. A monotonically increasing value indicates the worker process
  is crashed, stuck, or pointing at a different DB.

### High DB connection count

Reduce `DB_POOL_MAX_CONNS` or add a PgBouncer connection pooler in front of
PostgreSQL.  The current default of `20` connections assumes a single replica.

---

## 13. Release Branch & Image Tag Strategy

### 13.1 Release branch

`arena_new` uses a **single production release branch: `master`**.

| Concern | Configured location | Value |
|---|---|---|
| Local default branch | `git branch --show-current` on a fresh clone | `master` |
| GitHub Actions trigger | `.github/workflows/ci.yml` (`on.push.branches`) | `master` |
| GitHub Actions image publish gate | `.github/workflows/ci.yml` (`build-and-push.if`) | `github.ref == 'refs/heads/master'` |
| Accessibility workflow trigger | `.github/workflows/accessibility.yml` | `master` |
| Dokploy application source branch | Dokploy → application → **Source** → Branch | `master` |

All four locations must stay in sync. If the release branch is ever renamed
(e.g., `master` → `main`), update **all five rows** above in a single PR and
update both Dokploy applications' Source → Branch in the Dokploy UI.

### 13.2 GitHub registry secrets (optional — GHCR fallback built in)

When none of the secrets below are configured, `build-and-push` falls back
to **GitHub Container Registry** automatically: it logs in to `ghcr.io`
with the built-in `GITHUB_TOKEN` and publishes
`ghcr.io/<repo-owner>/arena-api:<short-sha>` + `:latest`. The resulting
GHCR package is **private by default** — either make it public (GitHub →
Packages → arena-api → Package settings → Change visibility) or configure
registry credentials in Dokploy so it can pull the private image.

To publish to a different registry instead, configure the following
repository (or organisation) secrets; they take precedence over the GHCR
fallback:

| Secret | Purpose | Example |
|---|---|---|
| `REGISTRY_URL` | OCI registry hostname (no scheme, no trailing slash) | `ghcr.io/your-org` or `registry.example.com` |
| `REGISTRY_USERNAME` | Username for `docker login` | `ci-publisher` |
| `REGISTRY_PASSWORD` | Password or token for `docker login` (use a registry-scoped token, **not** a personal password) | `ghp_***` / robot-account token |

Verification (one-off, before the first deploy):

1. GitHub → **Settings** → **Secrets and variables** → **Actions** — confirm
   all three secrets exist and are non-empty.
2. Push a small commit to `master`. The `build-and-push` job must reach
   "Log in to container registry" and succeed (`Login Succeeded`).
3. In your registry UI, confirm two new tags appear on the `arena-api`
   repository: one short-SHA tag and `latest` (see §13.3).

### 13.3 Image tag strategy

Every successful push to `master` publishes the image at
`${REGISTRY_URL}/arena-api` with the following tags
(generated by `docker/metadata-action@v5` — see `ci.yml` job `build-and-push`):

| Tag | Mutability | Source | Intended use |
|---|---|---|---|
| `<short-sha>` (e.g., `a1b2c3d`) | Immutable | `type=sha,prefix=,format=short` | **Pinned production deploys.** Dokploy should reference this exact tag for reproducible rollbacks. |
| `latest` | Mutable, always tip of `master` | `type=raw,value=latest` | Local development, smoke tests, and "deploy current tip" buttons. **Do not** pin production to `latest` — it makes rollbacks ambiguous. |

Environment-specific tags (e.g., `staging`, `production`) are intentionally
**not** part of this milestone. Promotion to staging or production is performed
by Dokploy re-pulling a specific `<short-sha>` tag, not by re-tagging the
image in the registry.

### 13.4 Dokploy deploy flow

1. Developer pushes to `master`.
2. GitHub Actions runs `lint`, `test`, `openapi-check` and (on success)
   `build-and-push`, which logs in to `${REGISTRY_URL}` and publishes
   `arena-api:<short-sha>` + `arena-api:latest`.
3. Dokploy auto-deploy (if enabled on the application's `master` branch
   webhook) pulls the new image and restarts `arena-migrate`, then
   `arena-api`, then `arena-worker` per §2.
4. To roll back, edit each Dokploy application's image tag to the previous
   `<short-sha>` and redeploy — do **not** rely on `latest`.

---

## 14. Initial Production Server Requirements

This section captures the recommended host sizing and operational guardrails
for the **first** production / pilot deploy of arena_new. It exists so that
infrastructure procurement does not block the milestone-1 launch, and so that
the same host can be re-used (or replaced with a documented upgrade path) as
load grows.

All three services from §2 (`arena-api`, `arena-worker`, `arena-migrate`)
co-locate on a single Docker host unless explicitly stated. PostgreSQL 17 is
assumed to run on the same host for staging and pilot, and to be split out
to a managed / dedicated tier as soon as workload pressure justifies it
(see §14.4).

### 14.1 Staging — minimum sizing

| Resource | Minimum | Notes |
|---|---|---|
| vCPU | **2** | Sufficient for `arena-api` + `arena-worker` + PostgreSQL 17 under synthetic load. |
| RAM | **4 GB** | ~1 GB for the Go services, ~1.5 GB for PostgreSQL `shared_buffers` / cache, ~1.5 GB headroom for OS + Dokploy. |
| Disk | **40 GB SSD** | Holds OS, Docker images, PostgreSQL data, and a few days of logs. SATA SSD is acceptable for staging. |
| Network | 1 Gbps shared | No public traffic guarantees required. |

Intended use: ephemeral pre-release validation, demo environments, CI smoke
targets. **Not** intended to absorb production traffic.

### 14.2 Production pilot — initial single-host sizing

| Resource | Pilot target | Notes |
|---|---|---|
| vCPU | **4** | Headroom for `arena-api` request bursts + `arena-worker` outbox drain + PostgreSQL background workers (autovacuum, checkpointer). |
| RAM | **8 GB** | `shared_buffers` ~2 GB, `effective_cache_size` ~4 GB, Go services + OS ~2 GB. |
| Disk | **80–100 GB NVMe** | NVMe (not SATA SSD) is required so PostgreSQL fsync latency stays in a healthy band under the pilot load profile. |
| Network | 1 Gbps with public IP | TLS termination handled by Dokploy/Traefik. |

Intended use: first paying customers / pilot tenants on a single colocated
host. Grafana/Prometheus and backups are external (e.g., scraped from a
separate observability host) at this tier.

### 14.3 Production with Grafana / Prometheus / backups on the same host

If observability and on-host backup retention must run on the same VM as
the application (typical for the first self-hosted production deploy), bump
the host to:

| Resource | Co-located target | Notes |
|---|---|---|
| vCPU | **4** | Same as pilot; observability stack is mostly RAM/disk bound, not CPU bound. |
| RAM | **12–16 GB** | Adds ~3 GB for Prometheus TSDB + ~1 GB for Grafana + ~1 GB headroom for backup tooling on top of §14.2. |
| Disk | **120–160 GB NVMe** | Adds ~30 GB Prometheus retention (15 days @ 1s scrape) + ~30 GB for 7-day rolling pg_dump archives on top of §14.2. |
| Network | 1 Gbps with public IP | Unchanged from §14.2. |

This is the recommended **default** for milestone-1 production unless a
separate observability host is already available.

### 14.4 When to split PostgreSQL onto a dedicated tier

The single-host topology in §14.2 / §14.3 is acceptable for the pilot, but
PostgreSQL must be moved to a dedicated tier (managed service or separate
VM) once **any** of the following thresholds is breached for more than
**15 minutes** in a rolling 24-hour window:

| Signal | Threshold | Source |
|---|---|---|
| Sustained CPU on the host | ≥ 70 % | host metrics |
| PostgreSQL `shared_buffers` cache hit ratio | < 95 % | `pg_stat_database` |
| Disk write latency (p99) | > 10 ms | node-exporter `node_disk_write_time_seconds` |
| `arena_outbox_backlog` (from `arena-worker`) | growing trend > 1 h | Prometheus |
| Active DB connections | > 80 % of `DB_POOL_MAX_CONNS` | `pg_stat_activity` |

Target dedicated-tier sizing for the first split (sized to ~3× pilot
workload): **4 vCPU, 16 GB RAM, 200 GB NVMe** managed PostgreSQL 17,
reachable only from the application host's private network (see §14.5).
PgBouncer in transaction-pooling mode is recommended once the active
connection count approaches `DB_POOL_MAX_CONNS`.

### 14.5 Firewall

Inbound rules (default-deny, allow-list only):

| Port | Protocol | Source | Purpose |
|---|---|---|---|
| 22 | TCP | Operator bastion IPs only | SSH administration |
| 80 | TCP | `0.0.0.0/0` | HTTP → HTTPS redirect (Traefik / Dokploy) |
| 443 | TCP | `0.0.0.0/0` | Public API traffic (TLS) |
| 5432 | TCP | App host private IP only | PostgreSQL (when split per §14.4) |
| 9090, 9091, 3000 | TCP | Observability host private IP only | Prometheus scrape + Grafana UI |

Outbound: allow `443/TCP` to the container registry and to OpenTelemetry /
log-sink endpoints; deny everything else by default. The Docker host must
**not** expose `arena-api:8080`, `arena-worker:9091`, or PostgreSQL `5432`
on the public interface — only Traefik listens publicly.

### 14.6 Backup

| Target | Frequency | Retention | Tooling |
|---|---|---|---|
| PostgreSQL logical dump (`pg_dump --format=custom`) | every 6 h | 7 days on-host + 30 days off-host | `deploy/backup.sh` |
| PostgreSQL WAL archive (when on a dedicated tier per §14.4) | continuous | 7 days | `archive_command` to off-host object storage |
| Dokploy configuration export | daily | 30 days off-host | Dokploy native export |
| Application secrets (`.env`) | on every rotation (§11) | indefinite, encrypted | password manager / sealed-secrets store |

Off-host destinations (S3-compatible bucket or equivalent) must be in a
**different failure domain** from the application host. A restore drill
following `deploy/BACKUP_RESTORE_RUNBOOK.md` must be exercised **at least
quarterly** and on every PostgreSQL major-version upgrade.

### 14.7 Monitoring

Minimum monitored signals (alerting required, not just dashboards):

| Signal | Alert threshold | Source |
|---|---|---|
| `arena-api` `/readyz` failing | > 1 min | blackbox-exporter |
| `arena-api` HTTP 5xx rate | > 1 % over 5 min | Prometheus (`arena_http_requests_total`) |
| `arena-api` request latency p99 | > 1 s over 10 min | Prometheus histogram |
| `arena_outbox_backlog` | > 1000 or growing > 15 min | `arena-worker` `/metrics` |
| Host CPU | > 85 % for 10 min | node-exporter |
| Host RAM available | < 10 % for 5 min | node-exporter |
| Host disk free | < 20 % | node-exporter |
| PostgreSQL connection saturation | > 90 % of `max_connections` | postgres-exporter |
| Backup job last success | > 12 h ago | backup script heartbeat |

Alerts must page (not just email) for `/readyz` failure, host disk free,
and backup-last-success staleness.

### 14.8 Disk retention

| Stream | On-host retention | Off-host retention | Action when exceeded |
|---|---|---|---|
| Container `stdout` / `stderr` JSON logs | 7 days (Docker `max-size=100m`, `max-file=10` per container) | 30 days in log sink | Rotate / drop oldest |
| PostgreSQL log files | 14 days | 30 days off-host | `log_rotation_age=1d`, `log_rotation_size=100MB` |
| PostgreSQL data directory | n/a (live) | n/a | Disk-free alert per §14.7 |
| Prometheus TSDB | 15 days | n/a (downsampled archive optional) | `--storage.tsdb.retention.time=15d` |
| `pg_dump` archive snapshots | 7 days on-host | 30 days off-host | `deploy/backup.sh` prunes oldest |

Disk-free monitoring (§14.7) must alert **before** any of these retention
policies is exceeded so that no stream is forced to drop unrotated data.

---

## 15. Admin Web Service (PR-07)

The admin-web service serves the SuperAdmin React SPA as a static nginx artifact.
It is built from `apps/admin-web/Dockerfile` (separate from the Go image).

### 15.1 Image overview

| Property | Value |
|---|---|
| Source Dockerfile | `apps/admin-web/Dockerfile` |
| Build stage | `node:20-alpine` — runs `npm ci && npm run build` |
| Runtime stage | `nginx:1.27-alpine` — serves `/usr/share/nginx/html` |
| Listen port | `8080` (non-root; no CAP_NET_BIND_SERVICE) |
| Runtime user | `nginx` (uid=101) — fully non-root |
| Health endpoint | `GET /health` → `200 OK text/plain "ok\n"` |
| Healthcheck | `HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3` |
| SPA routing | `try_files $uri /index.html` — all unknown paths return the SPA |
| Asset caching | Hashed `.js`/`.css` files: `Cache-Control: public, immutable, max-age=31536000` |
| `index.html` | `Cache-Control: no-store, no-cache` — always re-fetched on deploy |
| Source maps | Disabled by default; set `VITE_ENABLE_SOURCEMAPS=true` at build time for CI artifacts only |

### 15.2 Dokploy service configuration

| Property | Value |
|---|---|
| Dokploy service type | **Application** (long-running) |
| Docker image | `your-registry/arena-admin-web:<tag>` |
| Listen port | `8080` |
| Liveness / readiness probe | `GET /health` → `200 OK` |
| Start policy | `Always` |
| Replicas | ≥ 1 (stateless; can scale freely) |
| Domain | Assign your admin subdomain (e.g. `admin.yourdomain.com`) via Traefik |

### 15.3 Build arguments (set at image build time, NOT runtime)

`VITE_*` variables are inlined by Vite at build time and become part of the
JavaScript bundle. They are **not** read at nginx runtime. Never put secrets
in `VITE_*` — they appear in browser-readable JS.

| Build ARG | Required | Example value | Notes |
|---|---|---|---|
| `VITE_API_BASE_URL` | **Yes** | `https://api.yourdomain.com` | No trailing slash. Must be the public-facing URL of arena-api. |
| `VITE_ENABLE_SOURCEMAPS` | No | `false` | Set to `true` in CI to generate `.map` files for upload to an error tracker. Do not serve them publicly. |

**Build example (from repo root):**

```bash
docker build \
  --file apps/admin-web/Dockerfile \
  --build-arg VITE_API_BASE_URL=https://api.yourdomain.com \
  --tag your-registry/arena-admin-web:v0.1.0 \
  .
```

### 15.4 Environment variables (nginx runtime — none required)

The nginx container needs **no** runtime environment variables. Configuration is
fully baked into the bundle at build time.

If you need to change `VITE_API_BASE_URL` after deploy, you must **rebuild**
the image and redeploy. There is no runtime injection for this value.

### 15.5 Rollback procedure

1. Identify the last known-good image tag in your registry (e.g. `v0.1.0`).
2. In Dokploy, change the image tag for the `admin-web` service to the rollback tag.
3. Trigger a re-deploy. The old static bundle will be served immediately.
4. Verify by loading the admin UI and checking the version badge in the sidebar footer.

Because the admin-web image is stateless (no database, no persistent storage),
rollback is instantaneous and risk-free. The API it points to is determined at
build time; ensure the rollback image's baked `VITE_API_BASE_URL` matches your
current API endpoint.

### 15.6 Browser smoke test (manual, run after each production deploy)

The automated smoke tests in `apps/admin-web/src/smoke/pr07_smoke.test.ts`
verify logic at build time. After deploying the container, perform these
manual checks:

1. Open `https://admin.yourdomain.com` in a browser — the login page must load.
2. Log in with a valid superadmin account.
3. Open the browser's Network tab and confirm `GET /v1/me` returns `200 OK`.
4. Confirm the navigation sidebar does NOT show Reports, Notifications & Content,
   or POS Mode (these are hidden in production until implemented).
5. Open `https://admin.yourdomain.com/health` directly — must return `200 ok`.
6. Check that `.js` asset responses include `Cache-Control: public, immutable`.
7. Check that `index.html` response includes `Cache-Control: no-store`.

### 15.7 CORS and security notes

- The nginx container serves only static files; it makes no outbound requests.
- Cross-origin requests go from the **browser** to `arena-api`; ensure
  `CORS_ALLOWED_ORIGINS` on arena-api includes the admin domain.
- Do not use `*` for `CORS_ALLOWED_ORIGINS` in production (rejected by
  `APP_ENV=production` validation).
- The nginx config includes `X-Content-Type-Options: nosniff`,
  `X-Frame-Options: DENY`, `X-XSS-Protection: 1; mode=block`, and
  `Referrer-Policy: strict-origin-when-cross-origin` on all responses.

### 15.8 Bundle size budget

| Chunk | Budget (gzip) |
|---|---|
| `vendor-react` | ≤ 200 kB |
| `vendor-router` | ≤ 150 kB |
| `vendor-query` | ≤ 80 kB |
| `vendor-table` | ≤ 80 kB |
| `vendor-forms` | ≤ 50 kB |
| `vendor-misc` | ≤ 50 kB |
| `index` (app code) | ≤ 150 kB |
| **Total** | **≤ 760 kB** |

Monitor bundle size in CI with:

```bash
npm --prefix apps/admin-web run build 2>&1 | grep gzip
```

If any chunk exceeds its budget, investigate with `vite-bundle-analyzer` and
split the offending dependency or introduce lazy route loading.
