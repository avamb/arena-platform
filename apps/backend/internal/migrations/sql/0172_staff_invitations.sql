-- =====================================================================
-- arena_new — staff invitations with seats on the plan (tasks INV-01 and
-- INV-02, spec 08_architecture/46_staff_invitations_seated_ru.md, section 3.3)
--
-- The owner and the manager of an organization issue INVITATIONS the way a
-- buyer buys a ticket: they pick seats on the plan (closed seats too), fill
-- in the guest and get tickets. An invitation is an ORDER worth 0 with
-- orders.source = 'complimentary' that goes through the same path as a
-- purchase (owner decision R-1). This migration is the only one of the run:
--
--   * order_items.seat_origin — the status the seat had BEFORE the
--     invitation took it ('available' = from sale, 'unavailable' = from the
--     organizer's reserve). A cancelled ticket returns its seat there
--     (owner decision R-2). NULL for every item that is not an invitation.
--
--   * orders.issued_via / issued_by / invitation_batch_id — who issued the
--     invitation and how: 'staff' (one by one) or 'staff_bulk' (a CSV file,
--     task INV-06). NULL for every purchase.
--
--   * invitation_batches — one row per CSV file issued (INV-06). The table
--     exists now so the order can point at it; nothing writes it yet.
--
--   * billing_account_terms.complimentary_billing — the superadmin's switch
--     "free / standard rate" for invitations (owner decision R-7). Column
--     only: the rating rule is task INV-07.
--
--   * sales_channels.kind — 'sales' for every channel an organizer can see,
--     'internal' for the one hidden channel "Invitations" that an
--     invitation order is written on (orders.channel_id is NOT NULL). It is
--     created lazily by the first invitation, at most one per organization
--     (partial unique index), has no provider, no webhooks, no hosted page
--     and no feed token, and every channel read an organizer, a site or a
--     buyer can reach filters it out (gen/channels.sql.go).
--
--   * role_permissions: organizer <- complimentary.revoke (owner decision
--     R-5: the manager cancels any invitation of the organization). No new
--     permission is seeded, so the 532 parity guard has nothing to check.
-- =====================================================================

-- +goose Up
-- orders, order_items and sales_channels sit on the sales path: fail fast
-- rather than queue every sale behind a long transaction during a deploy.
-- Every column below is nullable or has a constant default, which is a
-- catalog-only change.
SET LOCAL lock_timeout = '5s';

ALTER TABLE order_items
    ADD COLUMN seat_origin text NULL
        CONSTRAINT order_items_seat_origin_check
        CHECK (seat_origin IN ('available', 'unavailable'));

COMMENT ON COLUMN order_items.seat_origin IS
    'Status the seat had before a staff invitation took it: available (from '
    'sale) or unavailable (from the organizer''s reserve). A cancelled '
    'ticket returns its seat there. NULL for purchases (spec 46, INV-01).';

CREATE TABLE invitation_batches (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id       uuid        NOT NULL REFERENCES organizations(id),
    session_id   uuid        NOT NULL REFERENCES sessions(id),
    created_by   uuid        REFERENCES users(id),
    file_sha256  text        NOT NULL,
    request_id   uuid        NOT NULL,
    rows         integer     NOT NULL DEFAULT 0 CHECK (rows >= 0),
    seats        integer     NOT NULL DEFAULT 0 CHECK (seats >= 0),
    from_reserve integer     NOT NULL DEFAULT 0 CHECK (from_reserve >= 0),
    status       text        NOT NULL DEFAULT 'issued'
                 CONSTRAINT invitation_batches_status_check
                 CHECK (status IN ('issued', 'failed')),
    error_report jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT invitation_batches_org_request_uq UNIQUE (org_id, request_id)
);

CREATE INDEX invitation_batches_session_idx ON invitation_batches (session_id, created_at DESC);

COMMENT ON TABLE invitation_batches IS
    'One CSV file of staff invitations issued in one transaction (spec 46, '
    'task INV-06). request_id makes a repeated upload return the first batch.';

ALTER TABLE orders
    ADD COLUMN issued_via text NULL
        CONSTRAINT orders_issued_via_check
        CHECK (issued_via IN ('staff', 'staff_bulk')),
    ADD COLUMN issued_by uuid NULL REFERENCES users(id),
    ADD COLUMN invitation_batch_id uuid NULL REFERENCES invitation_batches(id);

CREATE INDEX orders_session_issued_via_idx ON orders (session_id, created_at DESC)
    WHERE issued_via IS NOT NULL;

COMMENT ON COLUMN orders.issued_via IS
    'How a staff invitation was issued: staff (one by one) or staff_bulk '
    '(a CSV file). NULL for every purchase (spec 46).';
COMMENT ON COLUMN orders.issued_by IS
    'The user who issued a staff invitation. NULL for purchases and for an '
    'invitation issued with an organization API key.';

ALTER TABLE billing_account_terms
    ADD COLUMN complimentary_billing text NOT NULL DEFAULT 'free'
        CONSTRAINT billing_account_terms_complimentary_billing_check
        CHECK (complimentary_billing IN ('free', 'standard'));

COMMENT ON COLUMN billing_account_terms.complimentary_billing IS
    'Invitations of this account: free (up to the yearly free limit) or '
    'standard (the fixed per-ticket rate). Set by the platform superadmin; '
    'the rating rule is task INV-07 (spec 46, section 3.6).';

ALTER TABLE sales_channels
    ADD COLUMN kind text NOT NULL DEFAULT 'sales'
        CONSTRAINT sales_channels_kind_check
        CHECK (kind IN ('sales', 'internal'));

CREATE UNIQUE INDEX sales_channels_one_internal_per_org_uq
    ON sales_channels (org_id) WHERE kind = 'internal';

COMMENT ON COLUMN sales_channels.kind IS
    'sales = a channel the organizer sees and sells through; internal = the '
    'hidden channel "Invitations" that staff invitation orders are written '
    'on (spec 46, section 3.3). Every organizer-, site- and buyer-facing '
    'channel read filters internal out.';

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'complimentary.revoke'
WHERE  r.name = 'organizer'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM role_permissions rp
USING  roles r, permissions p
WHERE  rp.role_id = r.id
  AND  rp.permission_id = p.id
  AND  r.name = 'organizer'
  AND  r.org_id IS NULL
  AND  p.name = 'complimentary.revoke';

DROP INDEX IF EXISTS sales_channels_one_internal_per_org_uq;
ALTER TABLE sales_channels DROP COLUMN IF EXISTS kind;

ALTER TABLE billing_account_terms DROP COLUMN IF EXISTS complimentary_billing;

DROP INDEX IF EXISTS orders_session_issued_via_idx;
ALTER TABLE orders
    DROP COLUMN IF EXISTS invitation_batch_id,
    DROP COLUMN IF EXISTS issued_by,
    DROP COLUMN IF EXISTS issued_via;

DROP TABLE IF EXISTS invitation_batches;

ALTER TABLE order_items DROP COLUMN IF EXISTS seat_origin;
