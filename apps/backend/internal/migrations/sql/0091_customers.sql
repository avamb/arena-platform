-- +goose Up
-- W1-A4a (feature #479): Customers, identities, consents, org-links,
-- attributes, merge-candidates and gateway_sessions — Bil24-compat wave 1,
-- spec 08_architecture/18_bil24_compat_wave1_specification_ru.md section 3.2.
--
-- Model overview (spec §12.1): the platform holds a single global "customer"
-- entity; identities (email/phone/telegram/device/wc_customer/bil24_user)
-- attach to it and are unique per-platform (strong identities) or per-channel
-- (weak identities). Consents, order-facing counters and free-form
-- attributes are keyed per organization. gateway_sessions is the persistent
-- Bil24 session cookie: WordPress sites keep a base64url token for a
-- customer within one org/channel; the Bil24 gateway resolves it back to a
-- customer on every wire call.
--
-- All ids use uuidv7() (RFC 9562) so they are chronologically sortable and
-- follow the platform-wide identifier convention (0001_init.sql).
--
-- customers.system_id draws from compatibility_system_id_seq (0090) so
-- gateway responses can expose a bigint userId that plays nice with the
-- Bil24 wire schema while never colliding with locally-minted arena ids
-- (all sequence values are >= 1e9 by construction).

CREATE TABLE customers (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    system_id     bigint NOT NULL UNIQUE DEFAULT nextval('compatibility_system_id_seq'),
    display_name  text,
    locale        text,
    merged_into   uuid REFERENCES customers(id),
    anonymized_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE customers IS
    'W1-A4a: platform-wide buyer entity (spec §3.2 / §12.1). system_id is the '
    'bigint id emitted to Bil24 wire responses; it draws from '
    'compatibility_system_id_seq and is therefore always >= 1e9 for locally-'
    'minted rows. merged_into points at the winning row when two customers '
    'are consolidated (see customer_merge_candidates); anonymized_at flags '
    'GDPR-erased rows whose PII columns have been redacted.';

COMMENT ON COLUMN customers.system_id IS
    'W1-A4a: bigint identifier surfaced to Bil24 clients as userId. Draws '
    'from compatibility_system_id_seq (>= 1e9) unless the row was created by '
    'the §13.2 importer with an externally-assigned id (< 1e9).';

CREATE TABLE customer_identities (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id      uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    kind             text NOT NULL CHECK (kind IN
                       ('email','phone','telegram','device','wc_customer','bil24_user')),
    value_normalized text NOT NULL,
    channel_id       uuid REFERENCES sales_channels(id) ON DELETE SET NULL,
    verified_at      timestamptz,
    first_seen_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    source           text NOT NULL DEFAULT 'live'
);

-- Strong identities (email/phone/telegram) are unique across the whole
-- platform: sharing an email with another customer means "same person".
CREATE UNIQUE INDEX customer_identities_strong_uq
    ON customer_identities (kind, value_normalized)
    WHERE kind IN ('email','phone','telegram');

-- Weak identities (device cookie, WooCommerce user id, Bil24 user id) are
-- only unique within a single sales channel — the same device cookie may
-- legitimately reappear against a different site.
CREATE UNIQUE INDEX customer_identities_weak_uq
    ON customer_identities (kind, value_normalized, channel_id)
    WHERE kind IN ('device','wc_customer','bil24_user');

CREATE INDEX customer_identities_customer_idx ON customer_identities (customer_id);

COMMENT ON TABLE customer_identities IS
    'W1-A4a: attachable identity records for a customer (spec §3.2). '
    'value_normalized is the canonical form (lower/trim for email, E.164 for '
    'phone via github.com/nyaruka/phonenumbers). Strong-identity uniqueness '
    'is enforced platform-wide; weak-identity uniqueness is per sales channel.';

CREATE TABLE customer_consents (
    customer_id  uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('terms','marketing')),
    given_at     timestamptz NOT NULL,
    withdrawn_at timestamptz,
    source       text NOT NULL,
    PRIMARY KEY (customer_id, org_id, kind)
);

COMMENT ON TABLE customer_consents IS
    'W1-A4a: per-org consent records (terms / marketing). Consents are '
    'scoped to the organization that collected them; withdrawing consent '
    'sets withdrawn_at rather than deleting the row (audit trail).';

CREATE TABLE customer_org_links (
    customer_id    uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    first_order_at timestamptz,
    last_order_at  timestamptz,
    orders_count   int NOT NULL DEFAULT 0,
    tickets_count  int NOT NULL DEFAULT 0,
    source         text NOT NULL CHECK (source IN ('order','import')),
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (customer_id, org_id)
);

COMMENT ON TABLE customer_org_links IS
    'W1-A4a: denormalised per-org rollup of a customer''s order activity. '
    'Counters are maintained by the orders write path; source records '
    'whether the link was materialised by a live purchase or the §13.2 '
    'import path.';

CREATE TABLE customer_attributes (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    org_id      uuid REFERENCES organizations(id) ON DELETE CASCADE,
    key         text NOT NULL,
    value       jsonb NOT NULL,
    source      text NOT NULL,
    imported_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, org_id, key)
);

COMMENT ON TABLE customer_attributes IS
    'W1-A4a: free-form attribute bag keyed by (customer_id, org_id, key). '
    'org_id NULL means the attribute is platform-scoped rather than owned by '
    'one organisation (e.g. an unresolved raw phone number kept as data).';

CREATE TABLE customer_merge_candidates (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    customer_a  uuid NOT NULL REFERENCES customers(id),
    customer_b  uuid NOT NULL REFERENCES customers(id),
    reason      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolution  text CHECK (resolution IN ('merged','kept_separate'))
);

COMMENT ON TABLE customer_merge_candidates IS
    'W1-A4a: queue of automatically-detected suspected duplicates. The '
    'gateway never merges strong identities on its own (spec §12.2 + '
    'ADR-036); operator resolves each candidate via the admin UI.';

CREATE TABLE gateway_sessions (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    session_token text NOT NULL UNIQUE,
    customer_id   uuid NOT NULL REFERENCES customers(id),
    org_id        uuid NOT NULL REFERENCES organizations(id),
    channel_id    uuid NOT NULL REFERENCES sales_channels(id),
    locale        text NOT NULL DEFAULT 'en',
    promo_codes   text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL
);
CREATE INDEX gateway_sessions_customer_idx ON gateway_sessions (customer_id);

COMMENT ON TABLE gateway_sessions IS
    'W1-A4a: persistent Bil24 session cookie for a customer within one '
    'org/channel. session_token is 43 base64url chars derived from 32 bytes '
    'crypto/rand (spec §5). WordPress sites store the token and pass it as '
    'Bil24 sessionId on every wire call; expiry beyond expires_at yields '
    'resultCode=1 (session expired) so the site re-creates the session.';

COMMENT ON COLUMN gateway_sessions.session_token IS
    'W1-A4a: 43-character base64url representation of 32 random bytes '
    '(crypto/rand). Bil24 clients see this as sessionId on the wire.';

ALTER TABLE reservations
    ADD COLUMN gateway_session_id uuid REFERENCES gateway_sessions(id) ON DELETE SET NULL,
    ADD COLUMN customer_id        uuid REFERENCES customers(id);

CREATE INDEX reservations_gateway_session_active_idx
    ON reservations (gateway_session_id) WHERE state = 'active';

COMMENT ON COLUMN reservations.gateway_session_id IS
    'W1-A4a: the Bil24 gateway session that produced this reservation (spec '
    '§3.2). SET NULL on session deletion so hold expiry from garbage-'
    'collecting sessions never cascades onto reservations.';

COMMENT ON COLUMN reservations.customer_id IS
    'W1-A4a: buyer entity attached to the reservation via customers.Resolve. '
    'NULL until the site provides at least one identity for the session.';

-- permissions.name (not "code") is the canonical column — see 0008_rbac.sql;
-- the spec uses "code" prose but the schema settled on "name" long ago.
INSERT INTO permissions (name, description) VALUES
    ('customer.read',   'Read customers linked to the organization'),
    ('customer.import', 'Platform-level customer database import')
ON CONFLICT DO NOTHING;

-- +goose Down
DELETE FROM permissions WHERE name IN ('customer.read','customer.import');

DROP INDEX IF EXISTS reservations_gateway_session_active_idx;
ALTER TABLE reservations DROP COLUMN IF EXISTS customer_id;
ALTER TABLE reservations DROP COLUMN IF EXISTS gateway_session_id;

DROP INDEX IF EXISTS gateway_sessions_customer_idx;
DROP TABLE IF EXISTS gateway_sessions;

DROP TABLE IF EXISTS customer_merge_candidates;
DROP TABLE IF EXISTS customer_attributes;
DROP TABLE IF EXISTS customer_org_links;
DROP TABLE IF EXISTS customer_consents;

DROP INDEX IF EXISTS customer_identities_customer_idx;
DROP INDEX IF EXISTS customer_identities_weak_uq;
DROP INDEX IF EXISTS customer_identities_strong_uq;
DROP TABLE IF EXISTS customer_identities;

DROP TABLE IF EXISTS customers;
