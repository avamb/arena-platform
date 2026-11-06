-- 0204_site_feed_removals_my_tickets.sql - platform items P-6 and P-7 of the
-- Vino&Co static site (spec 62).
--
-- P-6  event_publication_removals remembers WHEN an event left a feed token's
--      list. Unpublishing deletes the event_publications row (a hard delete),
--      so without a trace the site builder only sees an event vanish and
--      cannot tell "removed" from "the answer was incomplete". A trigger on
--      event_publications keeps one row per (feed token, event); publishing
--      again removes it. The feed reads it for 30 days, together with events
--      that stay published to the token but were cancelled, archived,
--      returned to draft or soft-deleted.
--
-- P-7  "My tickets by link": my_tickets_link_requests holds the e-mailed
--      one-time link (only the SHA-256 of the token, 30 minutes) and the
--      counters the rate limits read; my_tickets_sessions holds the 4-hour
--      session the buyer exchanges the link for (only the token hash).
--      No foreign key from the removals table to the token or the event: the
--      trigger also fires for a cascade, and a tombstone must survive it.
-- =====================================================================

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE event_publication_removals (
    feed_token_id uuid        NOT NULL,
    event_id      uuid        NOT NULL,
    removed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (feed_token_id, event_id)
);

CREATE INDEX event_publication_removals_removed_at_idx
    ON event_publication_removals (removed_at);

COMMENT ON TABLE event_publication_removals IS
    'Tombstone of a deleted event_publications row, kept by trigger, read by the '
    'public feed for 30 days (spec 62 item P-6). Rows older than 90 days are '
    'pruned by the trigger itself.';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION event_publications_track_removal()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        INSERT INTO event_publication_removals (feed_token_id, event_id, removed_at)
        VALUES (OLD.feed_token_id, OLD.event_id, now())
        ON CONFLICT (feed_token_id, event_id) DO UPDATE SET removed_at = EXCLUDED.removed_at;
        DELETE FROM event_publication_removals
        WHERE removed_at < now() - interval '90 days';
        RETURN OLD;
    END IF;
    -- INSERT: published again, the tombstone is obsolete.
    DELETE FROM event_publication_removals
    WHERE feed_token_id = NEW.feed_token_id AND event_id = NEW.event_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER event_publications_track_removal_del
    AFTER DELETE ON event_publications
    FOR EACH ROW EXECUTE FUNCTION event_publications_track_removal();

CREATE TRIGGER event_publications_track_removal_ins
    AFTER INSERT ON event_publications
    FOR EACH ROW EXECUTE FUNCTION event_publications_track_removal();

CREATE TABLE my_tickets_link_requests (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    channel_id       uuid        REFERENCES sales_channels(id) ON DELETE SET NULL,
    email_normalized text        NOT NULL,
    locale           text,
    page_url         text,
    ip_hash          text,
    token_hash       text,
    token_expires_at timestamptz,
    sent_at          timestamptz,
    used_at          timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX my_tickets_link_requests_token_uq
    ON my_tickets_link_requests (token_hash) WHERE token_hash IS NOT NULL;
CREATE INDEX my_tickets_link_requests_email_idx
    ON my_tickets_link_requests (email_normalized, created_at);
CREATE INDEX my_tickets_link_requests_ip_idx
    ON my_tickets_link_requests (ip_hash, created_at) WHERE ip_hash IS NOT NULL;
CREATE INDEX my_tickets_link_requests_created_idx
    ON my_tickets_link_requests (created_at);

COMMENT ON TABLE my_tickets_link_requests IS
    'One request for a "my tickets" link (spec 62 §7). Every request counts for '
    'the rate limits, whether or not the address has orders; the token is minted '
    'by the worker at send time and only its SHA-256 is stored.';

CREATE TABLE my_tickets_sessions (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    email_normalized text        NOT NULL,
    token_hash       text        NOT NULL UNIQUE,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    revoked_at       timestamptz
);

CREATE INDEX my_tickets_sessions_expires_idx ON my_tickets_sessions (expires_at);

COMMENT ON TABLE my_tickets_sessions IS
    'The 4-hour session a buyer gets by exchanging the e-mailed link; sent as a '
    'bearer token, only its SHA-256 is stored (spec 62 §7).';

-- +goose Down

SET LOCAL lock_timeout = '5s';

DROP TABLE IF EXISTS my_tickets_sessions;
DROP TABLE IF EXISTS my_tickets_link_requests;
DROP TRIGGER IF EXISTS event_publications_track_removal_ins ON event_publications;
DROP TRIGGER IF EXISTS event_publications_track_removal_del ON event_publications;
DROP FUNCTION IF EXISTS event_publications_track_removal();
DROP TABLE IF EXISTS event_publication_removals;
