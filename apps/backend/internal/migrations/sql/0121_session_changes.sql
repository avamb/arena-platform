-- 0121_session_changes.sql — moving or cancelling a session tells its buyers.
--
-- Until now a session's date, time or venue could be rewritten (PATCH, the
-- event-bundle import, the Bil24-format import) or the session cancelled with
-- nobody told: the buyer kept an e-mail and a PDF with the old date.
-- 08_architecture/30_session_change_notifications_ru.md fixes that with ONE
-- function, internal/platform/sessionchange, which every write path calls in
-- the same transaction. These tables are its journal:
--
--   session_changes         one row per saved change (kinds, before/after,
--                           the organizer's message, who did it, how many
--                           orders and tickets it touched);
--   session_change_notices  one row per affected order: which route tells
--                           the buyer (the selling site or Arena itself) and
--                           how that went.
--
-- Both cascade from the session / order, so a fixture or an operator that
-- hard-deletes either one is never blocked by the journal.
--
-- The buyer must be able to answer the organizer, so the organizer's contact
-- is part of the change. organizations.contact_email/contact_phone and
-- org_promoters.email/phone already exist; the two new flags let the
-- organizer keep a phone number out of buyers' e-mails.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE organizations
    ADD COLUMN contact_phone_hidden boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN organizations.contact_phone_hidden IS
    'When true the organization''s contact_phone is never printed in a letter to a buyer.';

ALTER TABLE org_promoters
    ADD COLUMN phone_hidden boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN org_promoters.phone_hidden IS
    'When true the promoter''s phone is never printed in a letter to a buyer.';

CREATE TABLE session_changes (
    id            uuid        PRIMARY KEY DEFAULT uuidv7(),
    session_id    uuid        NOT NULL REFERENCES sessions(id)      ON DELETE CASCADE,
    event_id      uuid        NOT NULL REFERENCES events(id)        ON DELETE CASCADE,
    org_id        uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kinds         text[]      NOT NULL CHECK (cardinality(kinds) >= 1),
    old_state     jsonb       NOT NULL,
    new_state     jsonb       NOT NULL,
    message       text        NOT NULL DEFAULT '',
    actor_type    text        NOT NULL DEFAULT 'system',
    actor_id      text        NOT NULL DEFAULT '',
    orders_total  integer     NOT NULL DEFAULT 0,
    tickets_total integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (kinds <@ ARRAY['date','time','venue','cancelled']::text[])
);

CREATE INDEX session_changes_session_idx ON session_changes (session_id, created_at DESC);

COMMENT ON TABLE session_changes IS
    'Journal of buyer-visible session changes (date, time, venue, cancellation), one row per save.';
COMMENT ON COLUMN session_changes.actor_id IS
    'Free text on purpose (a user id, an api key id, or empty): never cast to uuid, see audit_events.actor_id.';

CREATE TABLE session_change_notices (
    change_id  uuid        NOT NULL REFERENCES session_changes(id) ON DELETE CASCADE,
    order_id   uuid        NOT NULL REFERENCES orders(id)          ON DELETE CASCADE,
    route      text        NOT NULL CHECK (route IN ('site', 'arena')),
    state      text        NOT NULL DEFAULT 'queued'
                           CHECK (state IN ('queued', 'delivered_to_site', 'sent', 'failed', 'skipped')),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (change_id, order_id)
);

CREATE INDEX session_change_notices_order_idx ON session_change_notices (order_id);

COMMENT ON TABLE session_change_notices IS
    'One row per order a session change touched: the route that tells the buyer and the outcome.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS session_change_notices;
DROP TABLE IF EXISTS session_changes;
ALTER TABLE org_promoters   DROP COLUMN IF EXISTS phone_hidden;
ALTER TABLE organizations   DROP COLUMN IF EXISTS contact_phone_hidden;

-- +goose StatementEnd
