-- =====================================================================
-- arena_new — VIP flag of a ticket (task V1, spec 40 section 5.V,
-- plan section 8.12)
--
-- An organizer marks a ticket (bought or an invitation) as VIP so that the
-- staff at the door and the organizer's own screens can tell it apart. The
-- flag is the organizer's private mark: it is NOT printed on the PDF
-- e-ticket and NOT written in any letter to the buyer.
--
--   * vip        — the mark itself, default false.
--   * vip_set_by — who last changed it: the user id, or the organization
--                  API key id. Plain uuid, no foreign key (the actor may
--                  be either kind of principal). NULL until first change.
--   * vip_set_at — when it was last changed.
--
-- The partial index serves "list the VIP tickets of a session" (the door
-- list, task V3) and stays tiny because almost every ticket is not VIP.
-- =====================================================================

-- +goose Up
-- tickets is the busiest table of the platform: fail fast rather than queue
-- every sale behind a long transaction during a deploy. Adding a column with
-- a constant default is a catalog-only change on PostgreSQL 11 and later.
SET LOCAL lock_timeout = '5s';

ALTER TABLE tickets
    ADD COLUMN vip        boolean     NOT NULL DEFAULT false,
    ADD COLUMN vip_set_by uuid,
    ADD COLUMN vip_set_at timestamptz;

CREATE INDEX tickets_vip_session_idx ON tickets (session_id) WHERE vip;

-- +goose Down
DROP INDEX IF EXISTS tickets_vip_session_idx;
ALTER TABLE tickets
    DROP COLUMN IF EXISTS vip_set_at,
    DROP COLUMN IF EXISTS vip_set_by,
    DROP COLUMN IF EXISTS vip;
