-- +goose Up
-- =====================================================================
-- arena_new — Event dates derived from sessions (Wave 4, AB-37)
--
-- Bil24: only the session (ActionEvent) carries date/time; the event
-- (Action) does not. events.start_at/end_at were independently editable
-- and drifted from sessions (live proof: "Next session" rendered for an
-- event with zero sessions). This migration drops them and replaces them
-- with a trigger-maintained cache:
--
--   events.first_session_at — MIN(start_at) over active, non-cancelled
--                             sessions; NULL when the event has none.
--   events.last_session_at  — MAX(end_at) over the same set.
--
-- Cache rather than a view because the events list filters and sorts on
-- these columns and must not pay a correlated subquery per row.
--
-- The trigger fires on INSERT / UPDATE / DELETE of sessions. Soft-delete
-- and cancellation are UPDATEs, so they are covered; hard DELETE is
-- covered for completeness (test teardown paths).
-- =====================================================================

ALTER TABLE events
    ADD COLUMN first_session_at timestamptz NULL,
    ADD COLUMN last_session_at  timestamptz NULL;

COMMENT ON COLUMN events.first_session_at IS
    'Trigger-maintained cache: MIN(sessions.start_at) over active '
    '(deleted_at IS NULL), non-cancelled sessions of this event. NULL '
    'when the event has no such sessions. Never written by handlers. '
    'Wave 4 — AB-37.';

COMMENT ON COLUMN events.last_session_at IS
    'Trigger-maintained cache: MAX(sessions.end_at) over active, '
    'non-cancelled sessions of this event. NULL when the event has no '
    'such sessions. Never written by handlers. Wave 4 — AB-37.';

-- ---------------------------------------------------------------------
-- Trigger machinery: recompute the cached window for the affected event.
-- ---------------------------------------------------------------------

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION events_refresh_session_window(p_event_id uuid)
RETURNS void
LANGUAGE sql
AS $$
    UPDATE events e
    SET    first_session_at = agg.min_start,
           last_session_at  = agg.max_end
    FROM   (
        SELECT MIN(s.start_at) AS min_start,
               MAX(s.end_at)   AS max_end
        FROM   sessions s
        WHERE  s.event_id   = p_event_id
          AND  s.deleted_at IS NULL
          AND  s.status    <> 'cancelled'
    ) agg
    WHERE  e.id = p_event_id;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION sessions_touch_event_window()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM events_refresh_session_window(NEW.event_id);
    END IF;
    IF TG_OP IN ('DELETE', 'UPDATE') AND
       (TG_OP = 'DELETE' OR OLD.event_id IS DISTINCT FROM NEW.event_id) THEN
        PERFORM events_refresh_session_window(OLD.event_id);
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sessions_refresh_event_window
AFTER INSERT OR UPDATE OR DELETE ON sessions
FOR EACH ROW
EXECUTE FUNCTION sessions_touch_event_window();

-- Backfill the cache for existing events.
UPDATE events e
SET    first_session_at = agg.min_start,
       last_session_at  = agg.max_end
FROM   (
    SELECT s.event_id,
           MIN(s.start_at) AS min_start,
           MAX(s.end_at)   AS max_end
    FROM   sessions s
    WHERE  s.deleted_at IS NULL
      AND  s.status    <> 'cancelled'
    GROUP BY s.event_id
) agg
WHERE  agg.event_id = e.id;

-- List ordering / filtering happens on the cached column now.
CREATE INDEX events_first_session_at_active
    ON events (first_session_at)
    WHERE deleted_at IS NULL;

-- The event no longer carries its own dates.
ALTER TABLE events
    DROP CONSTRAINT IF EXISTS events_date_order;

ALTER TABLE events
    DROP COLUMN start_at,
    DROP COLUMN end_at;

-- +goose Down
ALTER TABLE events
    ADD COLUMN start_at timestamptz NULL,
    ADD COLUMN end_at   timestamptz NULL;

-- Best-effort reverse backfill from the cache (epoch fallback keeps the
-- NOT NULL + date-order constraints satisfiable for date-less events).
UPDATE events
SET    start_at = COALESCE(first_session_at, created_at),
       end_at   = COALESCE(last_session_at,
                           COALESCE(first_session_at, created_at) + interval '1 hour');

-- Guarantee the historical end_at > start_at invariant.
UPDATE events
SET    end_at = start_at + interval '1 hour'
WHERE  end_at <= start_at;

ALTER TABLE events
    ALTER COLUMN start_at SET NOT NULL,
    ALTER COLUMN end_at   SET NOT NULL;

ALTER TABLE events
    ADD CONSTRAINT events_date_order CHECK (end_at > start_at);

DROP INDEX IF EXISTS events_first_session_at_active;

DROP TRIGGER IF EXISTS sessions_refresh_event_window ON sessions;
DROP FUNCTION IF EXISTS sessions_touch_event_window();
DROP FUNCTION IF EXISTS events_refresh_session_window(uuid);

ALTER TABLE events
    DROP COLUMN IF EXISTS last_session_at,
    DROP COLUMN IF EXISTS first_session_at;
