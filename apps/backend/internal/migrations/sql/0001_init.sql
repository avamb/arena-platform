-- +goose Up
-- =====================================================================
-- arena_new — Baseline migration (Wave 3, feature #2)
--
-- Creates the cross-cutting platform tables required by the foundation
-- milestone. All business-domain tables (organizations, users, events,
-- orders, tickets, etc.) are DEFERRED to subsequent milestones.
--
-- Tables created here:
--   * idempotency_keys    — middleware key/value cache for replayed POSTs
--   * audit_events        — append-only audit log
--   * outbox_events       — transactional outbox for domain events
--   * worker_jobs         — background job queue (FOR UPDATE SKIP LOCKED)
--   * worker_dead_letter  — exhausted-retry sink for jobs
--   * i18n_text           — user-content translations
--
-- The schema_migrations table is managed by goose itself.
-- =====================================================================

-- pgcrypto provides gen_random_bytes(), used by our uuidv7() function.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------
-- uuidv7() — RFC 9562 UUIDv7 generator implemented in plpgsql.
--
-- PostgreSQL 17 does NOT ship a native uuidv7() function (that lands
-- in PostgreSQL 18). This function provides the same surface so column
-- defaults can be written today; when we move to PG18 we can drop this
-- function and the defaults will resolve to the built-in.
--
-- Layout (per RFC 9562 §5.7):
--   bytes 0..5  : 48-bit Unix timestamp in milliseconds (big-endian)
--   byte 6      : 4-bit version (0x7) | 4 random bits
--   byte 7      : 8 random bits
--   byte 8      : 2-bit variant (0b10) | 6 random bits
--   bytes 9..15 : 56 random bits
-- ---------------------------------------------------------------------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION uuidv7() RETURNS uuid
LANGUAGE plpgsql
VOLATILE
AS $$
DECLARE
    unix_ts_ms bytea;
    uuid_bytes bytea;
BEGIN
    -- int8send produces 8 big-endian bytes; we keep the low 6 (=48 bits).
    unix_ts_ms := substring(
        int8send((extract(epoch from clock_timestamp()) * 1000)::bigint)
        from 3
    );
    -- 6 timestamp bytes || 10 random bytes = 16 bytes total.
    uuid_bytes := unix_ts_ms || gen_random_bytes(10);
    -- Set version nibble (high 4 bits of byte 6) to 0x7.
    uuid_bytes := set_byte(
        uuid_bytes, 6,
        (get_byte(uuid_bytes, 6) & 15) | 112
    );
    -- Set variant bits (high 2 bits of byte 8) to 0b10.
    uuid_bytes := set_byte(
        uuid_bytes, 8,
        (get_byte(uuid_bytes, 8) & 63) | 128
    );
    RETURN encode(uuid_bytes, 'hex')::uuid;
END;
$$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------
-- idempotency_keys
-- ---------------------------------------------------------------------
CREATE TABLE idempotency_keys (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    key             text        NOT NULL,
    scope           text        NOT NULL,
    actor_id        uuid,
    request_hash    text        NOT NULL,
    response_status integer     NOT NULL,
    response_body   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL
);

CREATE UNIQUE INDEX idempotency_keys_key_scope_uniq
    ON idempotency_keys (key, scope);

CREATE INDEX idempotency_keys_expires_at_idx
    ON idempotency_keys (expires_at);

-- ---------------------------------------------------------------------
-- audit_events
-- ---------------------------------------------------------------------
CREATE TABLE audit_events (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text        NOT NULL,
    actor_id      uuid,
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL,
    request_id    text        NOT NULL DEFAULT '',
    trace_id      text        NOT NULL DEFAULT '',
    ip            inet,
    metadata      jsonb       NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX audit_events_resource_idx
    ON audit_events (resource_type, resource_id, occurred_at DESC);

CREATE INDEX audit_events_actor_idx
    ON audit_events (actor_id);

-- ---------------------------------------------------------------------
-- outbox_events
-- ---------------------------------------------------------------------
CREATE TABLE outbox_events (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    aggregate_type text        NOT NULL,
    aggregate_id   text        NOT NULL,
    event_type     text        NOT NULL,
    payload        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    processed_at   timestamptz,
    attempts       integer     NOT NULL DEFAULT 0,
    last_error     text
);

-- Partial index dramatically reduces scan cost for the dispatcher: it
-- only sees the small set of unprocessed rows even when the table grows
-- into the millions.
CREATE INDEX outbox_events_unprocessed_idx
    ON outbox_events (occurred_at)
    WHERE processed_at IS NULL;

-- ---------------------------------------------------------------------
-- worker_jobs
-- ---------------------------------------------------------------------
CREATE TABLE worker_jobs (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    job_type        text        NOT NULL,
    payload         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    idempotency_key text,
    scheduled_at    timestamptz NOT NULL DEFAULT now(),
    claimed_at      timestamptz,
    claimed_by      text,
    attempts        integer     NOT NULL DEFAULT 0,
    max_attempts    integer     NOT NULL DEFAULT 10,
    status          text        NOT NULL DEFAULT 'pending',
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT worker_jobs_status_chk
        CHECK (status IN ('pending', 'claimed', 'done', 'failed'))
);

-- Primary queue scan: SELECT ... WHERE status='pending' AND scheduled_at <= now()
CREATE INDEX worker_jobs_status_scheduled_idx
    ON worker_jobs (status, scheduled_at);

-- Partial uniqueness: enforce one row per (job_type, idempotency_key)
-- only when an idempotency key is supplied.
CREATE UNIQUE INDEX worker_jobs_idem_uniq
    ON worker_jobs (job_type, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- ---------------------------------------------------------------------
-- worker_dead_letter
-- ---------------------------------------------------------------------
CREATE TABLE worker_dead_letter (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    original_job_id     uuid        NOT NULL,
    job_type            text        NOT NULL,
    payload             jsonb       NOT NULL DEFAULT '{}'::jsonb,
    attempts            integer     NOT NULL,
    last_error          text,
    failed_at           timestamptz NOT NULL DEFAULT now(),
    original_created_at timestamptz NOT NULL
);

CREATE INDEX worker_dead_letter_job_type_idx
    ON worker_dead_letter (job_type, failed_at DESC);

-- ---------------------------------------------------------------------
-- i18n_text
-- ---------------------------------------------------------------------
CREATE TABLE i18n_text (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    namespace  text        NOT NULL,
    key        text        NOT NULL,
    locale     text        NOT NULL,
    value      text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX i18n_text_ns_key_locale_uniq
    ON i18n_text (namespace, key, locale);

CREATE INDEX i18n_text_ns_key_idx
    ON i18n_text (namespace, key);


-- +goose Down
-- =====================================================================
-- Reverse order: drop tables that depend on uuidv7() first, then the
-- function and extension. Each DROP is idempotent (IF EXISTS) so the
-- migration is replayable in any partial state.
-- =====================================================================

DROP TABLE IF EXISTS i18n_text;
DROP TABLE IF EXISTS worker_dead_letter;
DROP TABLE IF EXISTS worker_jobs;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS audit_events;
DROP TABLE IF EXISTS idempotency_keys;

DROP FUNCTION IF EXISTS uuidv7();

-- pgcrypto is left in place: other future migrations may depend on it,
-- and it is safe to keep enabled. Drop it manually if you need a
-- truly clean rollback.
