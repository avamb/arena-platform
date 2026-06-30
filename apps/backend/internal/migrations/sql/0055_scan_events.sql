-- +goose Up
-- =====================================================================
-- arena_new — Scanner callback / scan_events (Wave S — feature #293, S-2)
--
-- Captures every scan attempt reported by external scanner devices via
-- POST /v1/scanner/scan-events.  The endpoint is authenticated using an
-- agent_feed_tokens bearer credential (see 0013_agent_feed_tokens.sql).
--
-- Idempotency: the (credential_code, scanned_at) pair is unique.  Replays
-- of the same scan event (e.g. retry after a network blip on the scanner
-- side) collapse to a no-op insert.  See ON CONFLICT DO NOTHING in the
-- scan_events.sql query file.
--
-- ticket.used_at semantics:
--
--   * NULL                — ticket has never been admitted by a scanner.
--   * non-NULL timestamp  — first admitted scan time.  Subsequent scans
--                           (admitted or denied) do not modify the column.
--
-- The column is added here rather than in 0026_tickets.sql so that the
-- ticket lifecycle migration stays focused on issuance.  Existing rows
-- get NULL via column default.
-- =====================================================================

ALTER TABLE tickets
    ADD COLUMN IF NOT EXISTS used_at timestamptz;

COMMENT ON COLUMN tickets.used_at IS
    'First admitted scan timestamp (set by POST /v1/scanner/scan-events). '
    'NULL when the ticket has never been admitted at a gate. Idempotent: '
    'subsequent scans do not overwrite the column.';

CREATE TABLE scan_events (
    id              uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    -- Tenancy: derived from the agent_feed_tokens -> sales_channels.org_id chain
    -- at insert time.  Indexed for per-org analytics.
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    -- Optional FKs: resolved from credential_code at insert time.  Left NULL
    -- when the credential cannot be matched to a known ticket (e.g. forged
    -- code, denied scan against an unknown barcode) so the audit trail is
    -- preserved even when the scanned credential is unrecognized.
    event_id        uuid        REFERENCES events(id),
    session_id      uuid        REFERENCES sessions(id),
    ticket_id       uuid        REFERENCES tickets(id),
    -- The bearer credential value presented by the scanner (the ticket QR
    -- payload or external barcode reference).  Always recorded verbatim.
    credential_code text        NOT NULL,
    -- Scanner-reported timestamp of when the scan physically happened at
    -- the gate.  May differ from received_at when the scanner was offline.
    scanned_at      timestamptz NOT NULL,
    -- Human-readable gate identifier (e.g. "North Entrance", "Gate 12").
    gate            text        NOT NULL DEFAULT '',
    -- Stable device identifier reported by the scanner (e.g. serial / MAC).
    device_id       text        NOT NULL DEFAULT '',
    -- Result of the scan as decided by the scanner device.
    result          text        NOT NULL,
    CONSTRAINT scan_events_result_check
        CHECK (result IN ('admitted', 'denied')),
    -- Server-side received-at timestamp (when the row was persisted).
    received_at     timestamptz NOT NULL DEFAULT now()
);

-- Idempotency: a given (credential_code, scanned_at) pair represents the
-- same physical scan.  Retries from flaky scanner networks collapse to a
-- no-op via ON CONFLICT DO NOTHING.
CREATE UNIQUE INDEX scan_events_credential_scanned_at_unique
    ON scan_events (credential_code, scanned_at);

-- Per-org analytics + admin views.
CREATE INDEX scan_events_org_id ON scan_events (org_id);

-- Per-session admission timeline (reporting, dashboards).
CREATE INDEX scan_events_session_id ON scan_events (session_id)
    WHERE session_id IS NOT NULL;

-- Per-ticket history (support console).
CREATE INDEX scan_events_ticket_id ON scan_events (ticket_id)
    WHERE ticket_id IS NOT NULL;

COMMENT ON TABLE scan_events IS
    'Append-only audit log of scan attempts from external scanner devices. '
    'Inserted by POST /v1/scanner/scan-events authenticated via an '
    'agent_feed_tokens bearer credential.  Idempotent on '
    '(credential_code, scanned_at) for safe retries.  Feature #293 (S-2).';

COMMENT ON COLUMN scan_events.credential_code IS
    'Bearer credential value presented at the gate.  Usually the static_qr '
    'ticket_credentials.payload or a federated barcodes.external_ref.';

COMMENT ON COLUMN scan_events.result IS
    'Scanner-reported decision: admitted | denied.  An admitted scan also '
    'triggers an idempotent UPDATE on tickets.used_at when the ticket is known.';

-- ── RBAC permission seed ─────────────────────────────────────────────────────
-- The scanner endpoint itself does NOT enforce a JWT permission (it is
-- authenticated by the agent_feed_tokens bearer).  We still seed a
-- conventional permission name so future admin tooling (e.g. "browse scan
-- events") can grant it via the existing RBAC engine.

INSERT INTO permissions (name, description) VALUES
    ('scan_event.read',  'Read scan_events rows from the support / reporting console (feature #293)'),
    ('scan_event.write', 'Internal: ingest scan events via the scanner callback (feature #293)')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('scan_event.read', 'scan_event.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('scan_event.read', 'scan_event.write')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'support'
  AND  p.name IN ('scan_event.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('scan_event.read', 'scan_event.write')
);

DELETE FROM permissions
WHERE name IN ('scan_event.read', 'scan_event.write');

DROP TABLE IF EXISTS scan_events;

ALTER TABLE tickets DROP COLUMN IF EXISTS used_at;
