-- 0189_analytics.sql — schema `analytics` of the platform's own product
-- analytics (spec 08_architecture/50_product_analytics_ru.md, stage A1).
--
-- The schema lives in the same database as the business tables so goose,
-- the backups (ops/backup) and a restore work unchanged, and it shares no
-- foreign key with the money tables: analytics.people.user_id is the ONLY
-- reference from this schema into a business table, and no business table
-- ever references the schema (spec 50 section 6).
--
--   events         raw behaviour events, range-partitioned by month on `at`
--                  (the partition is dropped whole after 13 months by the
--                  analytics.partitions worker job, which also keeps three
--                  future months ready). No e-mail, phone, name, message
--                  text, field value or entity id ever goes into `props`
--                  (spec 50 section 11): the dictionary in package
--                  internal/platform/analytics decides what may be stored.
--   people         one row per person, built by the rollup (stage A5).
--   identities     (kind, value) -> person, so merging two people is one
--                  UPDATE and never rewrites events.
--   contact_codes  the `cid` carried by links in e-mails to CRM contacts.
--   sessions, profiles, flow_daily, state  rollup output and cursors (A5).
--
-- Deviations from the SQL block of the spec: none in the columns. Only
-- indexes are added where a foreign key cascade or the rollup needs them
-- (events by identifier, identities and sessions by person). There are no
-- CHECK constraints on surface / id_kind on purpose: the Go dictionary is
-- the one allowlist, and a CHECK mirror that drifts fails a whole COPY batch
-- (AGENTS.md). The partitioned parent uses an identity column and a primary
-- key that contains the partition key, both of which PostgreSQL 17 (the
-- version of the stand, CI and production) supports.
--
-- Partitions are named events_yYYYYmMM. This migration creates the PREVIOUS
-- month (a batch sent just after midnight on the 1st carries events a little
-- older than the month), the current one and the next three (UTC month
-- boundaries); the worker job analytics.partitions keeps the previous month
-- and three months ahead from then on.
--
-- events_default is a safety net only: a row whose `at` falls outside every
-- monthly partition would otherwise make the whole COPY batch fail and lose
-- up to 500 events. The receivers clamp `at` into [now - 24 h, now], so
-- nothing should ever land there. The maintenance job moves nothing out of it
-- and does not drop it. Attention: PostgreSQL refuses to create a monthly
-- partition while the default one holds rows of that month, so if rows ever
-- do land there, move them (INSERT ... SELECT into the parent after
-- detaching, or DELETE them) before the missing partition is created. The
-- writer counts a failed flush in arena_analytics_flush_failed_total.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE SCHEMA IF NOT EXISTS analytics;

CREATE TABLE analytics.events (
    id          bigint GENERATED ALWAYS AS IDENTITY,
    at          timestamptz NOT NULL,
    surface     text NOT NULL,
    name        text NOT NULL,
    id_kind     text NOT NULL,
    id_value    text NOT NULL,
    org_id      uuid,
    props       jsonb NOT NULL DEFAULT '{}',
    suspect     boolean NOT NULL DEFAULT false,
    PRIMARY KEY (at, id)
) PARTITION BY RANGE (at);

COMMENT ON TABLE analytics.events IS
    'Raw behaviour events, one partition per UTC month. surface: site | event_center | bot. id_kind: aid | cid | user | tg | anon. No personal data in props.';

CREATE INDEX analytics_events_id_idx ON analytics.events (id_kind, id_value, at);

-- +goose StatementBegin
DO $$
DECLARE
    m     date;
    first date := date_trunc('month', now() AT TIME ZONE 'UTC')::date;
    i     int;
BEGIN
    FOR i IN -1..3 LOOP
        m := (first + make_interval(months => i))::date;
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS analytics.%I PARTITION OF analytics.events FOR VALUES FROM (%L) TO (%L)',
            'events_y' || to_char(m, 'YYYY') || 'm' || to_char(m, 'MM'),
            m::text || ' 00:00:00+00',
            (m + interval '1 month')::date::text || ' 00:00:00+00');
    END LOOP;
END
$$;
-- +goose StatementEnd

CREATE TABLE analytics.events_default PARTITION OF analytics.events DEFAULT;

CREATE TABLE analytics.people (
    id               uuid PRIMARY KEY,
    twenty_person_id text UNIQUE,
    user_id          uuid UNIQUE REFERENCES public.users(id) ON DELETE SET NULL,
    first_touch      jsonb NOT NULL DEFAULT '{}',
    last_touch       jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE analytics.people IS
    'One row per person, built by analytics.rollup. user_id is the only reference from the analytics schema into a business table.';

CREATE TABLE analytics.identities (
    kind      text NOT NULL,
    value     text NOT NULL,
    person_id uuid NOT NULL REFERENCES analytics.people(id) ON DELETE CASCADE,
    seen_at   timestamptz NOT NULL,
    PRIMARY KEY (kind, value)
);

CREATE INDEX analytics_identities_person_idx ON analytics.identities (person_id);

CREATE TABLE analytics.contact_codes (
    code             text PRIMARY KEY,
    twenty_person_id text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX contact_codes_person_idx ON analytics.contact_codes (twenty_person_id);

CREATE TABLE analytics.sessions (
    id              uuid PRIMARY KEY,
    person_id       uuid REFERENCES analytics.people(id) ON DELETE CASCADE,
    surface         text NOT NULL,
    started_at      timestamptz NOT NULL,
    ended_at        timestamptz NOT NULL,
    pages           int NOT NULL,
    engaged_seconds int NOT NULL,
    entry           jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX analytics_sessions_person_idx ON analytics.sessions (person_id, started_at);

CREATE TABLE analytics.profiles (
    person_id           uuid PRIMARY KEY REFERENCES analytics.people(id) ON DELETE CASCADE,
    stage               text NOT NULL,
    last_seen_at        timestamptz,
    visits_30d          int NOT NULL DEFAULT 0,
    engaged_minutes_30d int NOT NULL DEFAULT 0,
    articles            jsonb NOT NULL DEFAULT '[]',
    milestones          jsonb NOT NULL DEFAULT '{}',
    activity_score      int NOT NULL DEFAULT 0,
    changed_at          timestamptz NOT NULL
);

CREATE INDEX profiles_changed_idx ON analytics.profiles (changed_at);

CREATE TABLE analytics.flow_daily (
    day            date NOT NULL,
    surface        text NOT NULL,
    flow           text NOT NULL,
    step           text NOT NULL,
    entered        int NOT NULL,
    left_here      int NOT NULL,
    errors         int NOT NULL,
    median_seconds int,
    PRIMARY KEY (day, surface, flow, step)
);

CREATE TABLE analytics.state (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL
);

-- +goose Down

DROP SCHEMA IF EXISTS analytics CASCADE;
