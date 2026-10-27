-- 0172_venue_sharing.sql — a verified venue is open to every organization
-- (spec 08_architecture/24, owner decision 2026-10-27).
--
-- A venue belongs to the organization that made it (venues.org_id stays the
-- owner and the only editor). The platform operator can VERIFY a venue: from
-- then on every other organization may pick it for its own dates, so a new
-- organizer finds "Palác Akropolis" in the list instead of making a copy.
-- Using a venue is all that other organizations get: they cannot edit or
-- delete it, and a hall plan stays with the organization that drew it.
--
-- venues.review_status: 'unverified' (default) or 'verified'. Venues that
-- came from the Bil24 directory (external_bil24_id) are verified here: they
-- are one shared directory already. Everything else waits for the operator
-- (PATCH /v1/admin/venues/{id}/review, permission venue.curate).

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE venues
    ADD COLUMN review_status text NOT NULL DEFAULT 'unverified',
    ADD COLUMN verified_at   timestamptz NULL;

ALTER TABLE venues
    ADD CONSTRAINT venues_review_status_check CHECK (review_status IN ('unverified', 'verified'));

COMMENT ON COLUMN venues.review_status IS
    'unverified: only the owning organization may use the venue. verified: every organization may pick it for its dates (the owner stays its only editor).';

UPDATE venues
SET    review_status = 'verified', verified_at = now()
WHERE  external_bil24_id IS NOT NULL
  AND  deleted_at IS NULL;

CREATE INDEX venues_verified_city_idx
    ON venues (city_id, name)
    WHERE review_status = 'verified' AND deleted_at IS NULL;

INSERT INTO permissions (name, description) VALUES
    ('venue.curate', 'Verify a venue so every organization can use it, or take the verification back')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name = 'venue.curate'
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'venue.curate');
DELETE FROM permissions WHERE name = 'venue.curate';

DROP INDEX IF EXISTS venues_verified_city_idx;
ALTER TABLE venues DROP CONSTRAINT IF EXISTS venues_review_status_check;
ALTER TABLE venues DROP COLUMN IF EXISTS verified_at;
ALTER TABLE venues DROP COLUMN IF EXISTS review_status;
