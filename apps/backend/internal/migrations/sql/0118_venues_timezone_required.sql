-- 0118_venues_timezone_required.sql — a venue always has a timezone.
--
-- Every clock time a buyer sees — the session chips of the widget, the
-- tickets page, the e-ticket PDF and e-mail, the bot's messages — is the
-- session's UTC instant rendered in the VENUE's zone (`venues.timezone`).
-- When that zone is missing the client falls back to the viewer's own
-- device zone, which is exactly the bug found on 2026-09-29: an organizer
-- whose phone lives in UTC+3 read her 11:00 and 12:30 Madrid shows as 13:00
-- and 14:30. Owner decision the same day: the zone must be a property of the
-- venue the database guarantees, not something each write path remembers.
--
-- The REST venue create (bug B-3) and the event-bundle import already refuse
-- a venue without one; this migration closes the remaining gaps (legacy rows,
-- the Bil24 catalog import, migration 0079's placeholder venues, fixtures) at
-- the column itself:
--
--   1. Backfill every NULL/blank timezone, in this order:
--        a. the zone the organization's other venues in the SAME country use
--           (most common one);
--        b. the zone the organization's other venues use, any country;
--        c. a built-in table for the single-zone countries arena sells in
--           (mirrors internal/platform/geotz — a unit test keeps them equal);
--        d. 'UTC' — never silently right, always visibly wrong, so an
--           operator notices and PATCHes the venue. On production every
--           venue already carried a zone when this shipped (28/28); this
--           branch exists for old test data.
--   2. SET NOT NULL plus a CHECK that the value is not blank.
--
-- Validation against the IANA database stays in the API (time.LoadLocation),
-- Postgres has no such list.

-- +goose Up
-- +goose StatementBegin
WITH country_zones(iso2, zone) AS (
    VALUES
        ('CZ', 'Europe/Prague'), ('SK', 'Europe/Bratislava'), ('HU', 'Europe/Budapest'),
        ('AT', 'Europe/Vienna'), ('DE', 'Europe/Berlin'), ('PL', 'Europe/Warsaw'),
        ('IT', 'Europe/Rome'), ('FR', 'Europe/Paris'), ('NL', 'Europe/Amsterdam'),
        ('BE', 'Europe/Brussels'), ('CH', 'Europe/Zurich'), ('GB', 'Europe/London'),
        ('IE', 'Europe/Dublin'), ('DK', 'Europe/Copenhagen'), ('SE', 'Europe/Stockholm'),
        ('NO', 'Europe/Oslo'), ('FI', 'Europe/Helsinki'), ('EE', 'Europe/Tallinn'),
        ('LV', 'Europe/Riga'), ('LT', 'Europe/Vilnius'), ('IL', 'Asia/Jerusalem'),
        ('CY', 'Asia/Nicosia'), ('GR', 'Europe/Athens'), ('BG', 'Europe/Sofia'),
        ('RO', 'Europe/Bucharest'), ('HR', 'Europe/Zagreb'), ('SI', 'Europe/Ljubljana'),
        ('RS', 'Europe/Belgrade'), ('TR', 'Europe/Istanbul'), ('GE', 'Asia/Tbilisi'),
        ('AM', 'Asia/Yerevan'), ('UA', 'Europe/Kyiv'), ('MD', 'Europe/Chisinau'),
        ('BY', 'Europe/Minsk'), ('LU', 'Europe/Luxembourg'), ('MT', 'Europe/Malta'),
        ('AE', 'Asia/Dubai'), ('ME', 'Europe/Podgorica'), ('AL', 'Europe/Tirane'),
        ('MK', 'Europe/Skopje'), ('BA', 'Europe/Sarajevo'), ('IS', 'Atlantic/Reykjavik')
),
missing AS (
    SELECT v.id, v.org_id,
           upper(coalesce(nullif(btrim(v.country), ''),
                          (SELECT c.iso2 FROM cities ci JOIN countries c ON c.id = ci.country_id
                            WHERE ci.id = v.city_id))) AS iso2
    FROM   venues v
    WHERE  v.timezone IS NULL OR btrim(v.timezone) = ''
),
resolved AS (
    SELECT m.id,
           coalesce(
               (SELECT o.timezone
                FROM   venues o
                WHERE  o.org_id = m.org_id AND o.id <> m.id
                  AND  o.timezone IS NOT NULL AND btrim(o.timezone) <> ''
                  AND  m.iso2 IS NOT NULL AND upper(o.country) = m.iso2
                GROUP BY o.timezone ORDER BY count(*) DESC, o.timezone LIMIT 1),
               (SELECT o.timezone
                FROM   venues o
                WHERE  o.org_id = m.org_id AND o.id <> m.id
                  AND  o.timezone IS NOT NULL AND btrim(o.timezone) <> ''
                GROUP BY o.timezone ORDER BY count(*) DESC, o.timezone LIMIT 1),
               (SELECT cz.zone FROM country_zones cz WHERE cz.iso2 = m.iso2),
               'UTC'
           ) AS zone
    FROM missing m
)
UPDATE venues v
SET    timezone = r.zone,
       updated_at = now()
FROM   resolved r
WHERE  v.id = r.id;
-- +goose StatementEnd

ALTER TABLE venues
    ALTER COLUMN timezone SET NOT NULL;

ALTER TABLE venues
    ADD CONSTRAINT venues_timezone_not_blank CHECK (btrim(timezone) <> '');

COMMENT ON COLUMN venues.timezone IS
    'IANA zone name of the venue (e.g. Europe/Madrid). NOT NULL and non-blank since 0118: every clock time a buyer sees is rendered in this zone, never in the viewing device''s. Validated against the IANA list in the API.';

-- +goose Down
ALTER TABLE venues DROP CONSTRAINT IF EXISTS venues_timezone_not_blank;
ALTER TABLE venues ALTER COLUMN timezone DROP NOT NULL;
COMMENT ON COLUMN venues.timezone IS
    'IANA timezone name (e.g. Europe/Prague). Free-form text; validated in the API.';
