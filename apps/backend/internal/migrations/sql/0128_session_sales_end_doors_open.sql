-- +goose Up
-- =============================================================================
-- 0128 — a session's own "sales end" and "doors open" times.
--
-- sales_end_at: the moment ticket sales for the session close, on every
-- surface (gateway, widget, REST). Always set: it defaults to start_at, and
-- the organizer may move it (some events sell after the start, for those who
-- come late). It is a session-wide limit ON TOP of each category's own
-- sale_window_end, so a single category can still close earlier.
--
-- doors_open_at: when the venue lets people in. Display only (sales page,
-- e-ticket, letters), optional, never after start_at.
--
-- Until now only categories carried an end (ticket_tiers.sale_window_end) and
-- the Bil24 gateway derived a session's sellEndTime as their maximum. The
-- backfill below keeps exactly that value, so nothing that sells today stops.
--
-- A BEFORE INSERT trigger fills sales_end_at from start_at when an insert
-- leaves it out (every raw fixture insert, the seed), and a BEFORE UPDATE
-- trigger moves both times along with start_at when a move does not set
-- them itself, so a session moved by a week keeps "sales close at the start"
-- and "doors 30 minutes before".
-- =============================================================================

ALTER TABLE sessions
    ADD COLUMN sales_end_at  timestamptz NULL,
    ADD COLUMN doors_open_at timestamptz NULL;

UPDATE sessions s
SET    sales_end_at = COALESCE(
           (SELECT max(COALESCE(tt.sale_window_end, s.start_at))
              FROM ticket_tiers tt
             WHERE tt.session_id = s.id
               AND tt.deleted_at IS NULL),
           s.start_at);

-- A category whose sale window end IS the session's sales end only
-- inherited it (the bundle's actionEvent.sellEndTime was copied onto every
-- category). The session limit covers it now; leaving the copy would close
-- the category at the OLD time once the organizer extends the sale.
-- Categories with their own earlier end (price steps) keep it.
UPDATE ticket_tiers tt
SET    sale_window_end = NULL,
       updated_at      = now()
FROM   sessions s
WHERE  tt.session_id      = s.id
  AND  tt.deleted_at      IS NULL
  AND  tt.sale_window_end = s.sales_end_at;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sessions_sale_times_defaults()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.sales_end_at IS NULL THEN
            NEW.sales_end_at := NEW.start_at;
        END IF;
        RETURN NEW;
    END IF;
    -- UPDATE: a moved start carries both times along unless the same
    -- statement set them explicitly.
    IF NEW.start_at IS DISTINCT FROM OLD.start_at THEN
        IF NEW.sales_end_at IS NOT DISTINCT FROM OLD.sales_end_at THEN
            NEW.sales_end_at := OLD.sales_end_at + (NEW.start_at - OLD.start_at);
        END IF;
        IF NEW.doors_open_at IS NOT DISTINCT FROM OLD.doors_open_at
           AND OLD.doors_open_at IS NOT NULL THEN
            NEW.doors_open_at := OLD.doors_open_at + (NEW.start_at - OLD.start_at);
        END IF;
    END IF;
    IF NEW.sales_end_at IS NULL THEN
        NEW.sales_end_at := NEW.start_at;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sessions_sale_times_defaults
BEFORE INSERT OR UPDATE ON sessions
FOR EACH ROW
EXECUTE FUNCTION sessions_sale_times_defaults();

ALTER TABLE sessions
    ALTER COLUMN sales_end_at SET NOT NULL,
    ADD CONSTRAINT sessions_doors_before_start
        CHECK (doors_open_at IS NULL OR doors_open_at <= start_at);

COMMENT ON COLUMN sessions.sales_end_at IS
    'When ticket sales for the session close on every surface. Defaults to '
    'start_at; the organizer may set it later (sell after the start) or '
    'earlier. A per-category sale_window_end can only close a category '
    'sooner. Migration 0128.';

COMMENT ON COLUMN sessions.doors_open_at IS
    'When the venue opens its doors. Display only (sales page, e-ticket, '
    'letters), optional, never after start_at. Migration 0128.';

-- +goose Down
DROP TRIGGER IF EXISTS sessions_sale_times_defaults ON sessions;
DROP FUNCTION IF EXISTS sessions_sale_times_defaults();
ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_doors_before_start,
    DROP COLUMN IF EXISTS doors_open_at,
    DROP COLUMN IF EXISTS sales_end_at;
