-- 0152_event_watch_site_sync.sql — tell the operator when a selling site has
-- picked up an announced event change.
--
-- The event watcher (migration 0124, internal/platform/eventwatch, job
-- events.change_watch) announces a new or changed event about a minute after
-- the change in arena. A WordPress site sells from its OWN copy of the catalog,
-- which it refreshes through the Bil24-compatible gateway (GET_ALL_ACTIONS), so
-- for several minutes the site may still show the old price while Telegram
-- already says "price changed" (seen on lampyrisevents.com, 2026-10-14). Owner
-- decision 2026-10-14: the first message lists every storefront with its
-- state, a second short message follows when a site has fetched the catalog
-- after the change, and one warning goes out when a site has not done so for
-- 30 minutes.
--
--   channel_catalog_fetches — per sales channel, when its site last started a
--     GET_ALL_ACTIONS that answered successfully. Written by the gateway, best
--     effort and outside any transaction, at most once every 30 seconds per
--     channel. It records the START of the request, so a fetch that began
--     before a change can never count as having picked it up.
--   event_watch_site_sync — the watcher's own memory: per announced event and
--     site channel, the change it waits for (changed_at = when the watcher
--     first saw that version of the event), when it was announced, and when
--     the site confirmed or the warning went out. A newer announcement of the
--     same event replaces the event's rows.
--
-- No foreign keys: both tables are side records next to busy tables
-- (sales_channels, events) and must never hold a lock on them; a row of a
-- deleted channel or event is dropped by the watcher or simply never read.
-- No permissions: nothing here is reachable through the HTTP API.

-- +goose Up

CREATE TABLE channel_catalog_fetches (
    channel_id      uuid        PRIMARY KEY,
    last_fetched_at timestamptz NOT NULL
);

COMMENT ON TABLE channel_catalog_fetches IS
    'When the site behind a gateway sales channel last fetched the catalog (GET_ALL_ACTIONS answered resultCode 0). Start time of the request, written at most every 30 s.';

CREATE TABLE event_watch_site_sync (
    event_id     uuid        NOT NULL,
    channel_id   uuid        NOT NULL,
    changed_at   timestamptz NOT NULL,
    announced_at timestamptz NOT NULL,
    confirmed_at timestamptz,
    warned_at    timestamptz,
    PRIMARY KEY (event_id, channel_id)
);

COMMENT ON TABLE event_watch_site_sync IS
    'Event watcher: per announced event change and site channel, whether the site has fetched the catalog since the change (confirmed_at) or was reported as late (warned_at).';

COMMENT ON COLUMN event_watch_site_sync.changed_at IS
    'When the watcher first saw the announced version of the event; a catalog fetch started after it carries the change.';

CREATE INDEX event_watch_site_sync_pending ON event_watch_site_sync (changed_at)
    WHERE confirmed_at IS NULL;

-- +goose Down

DROP TABLE IF EXISTS event_watch_site_sync;
DROP TABLE IF EXISTS channel_catalog_fetches;
