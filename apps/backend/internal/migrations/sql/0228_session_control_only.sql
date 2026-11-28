-- 0228_session_control_only.sql — a session for entry control only (MA-40).
--
-- sessions.sales_mode gets a third value:
--   control_only — the tickets of the session are not sold on this platform
--                  at all; the session exists only so that tickets sold
--                  elsewhere can be checked at the door (scanner app).
--
-- A control_only session carries none of the four partner fields of 0221
-- (external_ticket_url, external_seller_name, display_price_from_minor,
-- display_currency), like a platform session.
--
-- The database keeps the invariant of a control event:
--   * an event that has a live (deleted_at IS NULL) control_only session has
--     ONLY live control_only sessions — a control session never shares an
--     event with a platform or external one, and an existing session never
--     turns into a control session (or back) while the event holds live
--     sessions of the other kind;
--   * such an event is never 'published' (it is refused both when the event
--     is published and when a control session is added to a published event).
-- Both checks raise check_violation (SQLSTATE 23514). The handlers keep the
-- same rules and answer 409 before the database has to.
--
-- The checks read other rows without locking the event, so two concurrent
-- transactions could in theory each add a session of a different kind to
-- the same event; the handlers create a control event in one request, so
-- this window is not used by any write path.
--
-- Down restores the 0221 constraints and so refuses while any
-- control_only session exists (delete or convert them first).
--
-- No permission is seeded, so the superadmin parity guard (rule 532) has
-- nothing to check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE sessions
    DROP CONSTRAINT sessions_sales_mode_check,
    DROP CONSTRAINT sessions_sales_mode_fields;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_sales_mode_check
        CHECK (sales_mode IN ('platform', 'external', 'control_only')),
    ADD CONSTRAINT sessions_sales_mode_fields
        CHECK (
            (sales_mode = 'external'
             AND external_ticket_url IS NOT NULL
             AND external_seller_name IS NOT NULL
             AND external_ticket_url LIKE 'https://%')
            OR
            (sales_mode IN ('platform', 'control_only')
             AND external_ticket_url IS NULL
             AND external_seller_name IS NULL
             AND display_price_from_minor IS NULL
             AND display_currency IS NULL)
        );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sessions_control_only_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    -- A soft-deleted row never blocks and is never blocked.
    IF NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- An UPDATE that lists the columns but changes none of them has nothing
    -- new to check.
    IF TG_OP = 'UPDATE'
       AND NEW.sales_mode = OLD.sales_mode
       AND NEW.event_id = OLD.event_id
       AND OLD.deleted_at IS NULL THEN
        RETURN NEW;
    END IF;

    IF NEW.sales_mode = 'control_only' THEN
        IF EXISTS (SELECT 1 FROM sessions s
                   WHERE  s.event_id = NEW.event_id
                     AND  s.id <> NEW.id
                     AND  s.deleted_at IS NULL
                     AND  s.sales_mode <> 'control_only') THEN
            RAISE EXCEPTION 'a control_only session cannot share event % with sessions that are sold (platform or external)', NEW.event_id
                USING ERRCODE = '23514';
        END IF;
        IF EXISTS (SELECT 1 FROM events e
                   WHERE  e.id = NEW.event_id
                     AND  e.status = 'published') THEN
            RAISE EXCEPTION 'a control_only session cannot belong to the published event %', NEW.event_id
                USING ERRCODE = '23514';
        END IF;
    ELSE
        IF EXISTS (SELECT 1 FROM sessions s
                   WHERE  s.event_id = NEW.event_id
                     AND  s.id <> NEW.id
                     AND  s.deleted_at IS NULL
                     AND  s.sales_mode = 'control_only') THEN
            RAISE EXCEPTION 'event % holds control_only sessions, a % session cannot be added to it', NEW.event_id, NEW.sales_mode
                USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sessions_control_only_guard
    BEFORE INSERT OR UPDATE OF sales_mode, event_id, deleted_at ON sessions
    FOR EACH ROW EXECUTE FUNCTION sessions_control_only_guard();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION events_control_only_publish_guard()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'published'
       AND OLD.status IS DISTINCT FROM NEW.status
       AND EXISTS (SELECT 1 FROM sessions s
                   WHERE  s.event_id = NEW.id
                     AND  s.deleted_at IS NULL
                     AND  s.sales_mode = 'control_only') THEN
        RAISE EXCEPTION 'event % holds control_only sessions and cannot be published', NEW.id
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER events_control_only_publish_guard
    BEFORE UPDATE OF status ON events
    FOR EACH ROW EXECUTE FUNCTION events_control_only_publish_guard();

COMMENT ON COLUMN sessions.sales_mode IS
    'Who sells the tickets of the session: platform (arena), external (a '
    'partner box office) or control_only (nobody here: the session exists '
    'for entry control only). Only platform sessions are held or sold here. '
    'A control_only session lives only in an event whose live sessions are '
    'all control_only, and such an event is never published (triggers '
    'sessions_control_only_guard and events_control_only_publish_guard). '
    'Migrations 0221, 0228.';
COMMENT ON FUNCTION sessions_control_only_guard() IS
    'Refuses a live session that mixes control_only with sold sessions in '
    'one event, or a control_only session in a published event (23514). '
    'Migration 0228.';
COMMENT ON FUNCTION events_control_only_publish_guard() IS
    'Refuses publishing an event that holds a live control_only session '
    '(23514). Migration 0228.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TRIGGER IF EXISTS events_control_only_publish_guard ON events;
DROP TRIGGER IF EXISTS sessions_control_only_guard ON sessions;
DROP FUNCTION IF EXISTS events_control_only_publish_guard();
DROP FUNCTION IF EXISTS sessions_control_only_guard();

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_sales_mode_fields,
    DROP CONSTRAINT IF EXISTS sessions_sales_mode_check;

ALTER TABLE sessions
    ADD CONSTRAINT sessions_sales_mode_check
        CHECK (sales_mode IN ('platform', 'external')),
    ADD CONSTRAINT sessions_sales_mode_fields
        CHECK (
            (sales_mode = 'external'
             AND external_ticket_url IS NOT NULL
             AND external_seller_name IS NOT NULL
             AND external_ticket_url LIKE 'https://%')
            OR
            (sales_mode = 'platform'
             AND external_ticket_url IS NULL
             AND external_seller_name IS NULL
             AND display_price_from_minor IS NULL
             AND display_currency IS NULL)
        );

COMMENT ON COLUMN sessions.sales_mode IS
    'Who sells the tickets of the session: platform (arena) or external (a '
    'partner box office). An external session is never held or sold here. '
    'Migration 0221.';
