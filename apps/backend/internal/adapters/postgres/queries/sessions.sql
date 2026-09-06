-- sessions.sql — sqlc query definitions for the sessions table (feature #126).
-- Sessions are scoped to an event via the event_id foreign key.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.
--
-- Wave 4 (AB-36/AB-38): the session owns its venue (venue_id NOT NULL), an
-- optional capacity_override, and its currency (+ currency_source telling
-- whether the value was derived from the venue geography or set explicitly).
-- capacity_total is always a derived value (plan version -> capacity_override
-- -> venues.capacity_default), computed at the application layer.

-- name: InsertSession :one
-- InsertSession creates a new session for the given event at the given venue.
-- status defaults to 'scheduled' when empty. currency / currency_source are
-- resolved by the handler before the insert (AB-38).
-- Returns the created row including the uuidv7 PK assigned by the database.
INSERT INTO sessions (event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, currency, currency_source)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(NULLIF($7, ''), 'scheduled'), $8, $9)
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

-- name: GetSessionByID :one
-- GetSessionByID fetches an active session by its UUID primary key scoped to the event.
-- Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different event.
SELECT id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at
FROM   sessions
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL;

-- name: ListSessionsByEvent :many
-- ListSessionsByEvent returns all active sessions for the given event.
-- Ordered by start_at ASC so the earliest session is first.
SELECT id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at
FROM   sessions
WHERE  event_id = $1
  AND  deleted_at IS NULL
ORDER BY start_at ASC, id ASC;

-- name: UpdateSession :one
-- UpdateSession applies a partial update to an active session.
-- Scoped by event_id. NULL/zero optional fields keep the existing values.
-- capacity_total is the derived value computed by the handler; changing the
-- currency cascades to ticket_tiers via ticket_tiers_currency_matches_session
-- (ON UPDATE CASCADE), so a session can never end up with mixed-currency tiers.
UPDATE sessions
SET    venue_id          = CASE WHEN $3::uuid        IS NOT NULL THEN $3::uuid        ELSE venue_id END,
       start_at          = CASE WHEN $4::timestamptz IS NOT NULL THEN $4::timestamptz ELSE start_at END,
       end_at            = CASE WHEN $5::timestamptz IS NOT NULL THEN $5::timestamptz ELSE end_at END,
       capacity_total    = CASE WHEN $6::integer     IS NOT NULL THEN $6::integer     ELSE capacity_total END,
       capacity_override = CASE WHEN $7::integer     IS NOT NULL THEN $7::integer     ELSE capacity_override END,
       status            = COALESCE(NULLIF($8, ''), status),
       currency          = CASE WHEN $9::text        IS NOT NULL THEN $9::text        ELSE currency END,
       currency_source   = COALESCE(NULLIF($10, ''), currency_source),
       updated_at        = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

-- name: SoftDeleteSession :one
-- SoftDeleteSession marks a session as deleted by setting deleted_at.
-- Scoped by event_id to enforce owner-gated mutation policy.
-- The row is not physically removed.
UPDATE sessions
SET    deleted_at = now(),
       updated_at = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, venue_id, start_at, end_at, capacity_total, capacity_override, status, admission_mode, seating_plan_version_id, currency, currency_source, created_at, updated_at, deleted_at;

-- name: GetSessionSeatingBinding :one
-- GetSessionSeatingBinding fetches the seating-related columns for a session
-- scoped by event. Returns pgx.ErrNoRows when not found, already soft-deleted,
-- or belongs to a different event. Used by the seating-binding endpoint
-- (feature #306, Wave SEAT-B2) to decide first-bind vs rebind and to know
-- whether a rebind is safe.
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL;

-- name: GetSessionSeatingBindingForUpdate :one
-- GetSessionSeatingBindingForUpdate is the row-locking variant of
-- GetSessionSeatingBinding. Taken at the top of the seating-bind transaction
-- (feature #306, Wave SEAT-B2) so binds serialize against any concurrent
-- transaction that mutates the session's seat inventory — the seated
-- reservation path locks the same sessions row via
-- IncrementSessionSeatStatusVersion, which closes the TOCTOU window between
-- the rebind zero-reservations check and the session_seats wipe. MUST be
-- called inside a transaction; the lock releases on commit / rollback.
SELECT id, event_id, admission_mode, seating_plan_version_id,
       seat_status_version, capacity_total
FROM   sessions
WHERE  id         = $1
  AND  event_id   = $2
  AND  deleted_at IS NULL
FOR UPDATE;

-- name: BindSessionSeatingPlan :one
-- BindSessionSeatingPlan flips a session onto the (admission_mode,
-- seating_plan_version_id) tuple and recomputes capacity_total from the
-- materialized-seat count computed by the caller. seat_status_version is left
-- untouched — bind is a metadata change, not a seat-status transition. The
-- SET is guarded by the same event_id + soft-delete filter as the plain
-- sessions CRUD to keep the mutation policy consistent across the domain.
UPDATE sessions
SET    admission_mode          = $3,
       seating_plan_version_id = $4,
       capacity_total          = $5,
       updated_at              = now()
WHERE  id       = $1
  AND  event_id = $2
  AND  deleted_at IS NULL
RETURNING id, event_id, admission_mode, seating_plan_version_id,
          seat_status_version, capacity_total;

-- name: CountOverlappingSessions :one
-- CountOverlappingSessions counts active sessions for the given event whose
-- time range overlaps with [start_at, end_at). The session with id=exclude_id
-- is excluded from the count so update operations can check against their
-- siblings without counting themselves.
-- Overlap condition: a.start_at < end_at AND a.end_at > start_at.
SELECT COUNT(*)::int
FROM   sessions
WHERE  event_id    = $1
  AND  id         <> $2
  AND  deleted_at  IS NULL
  AND  start_at    < $4
  AND  end_at      > $3;

-- name: GetSessionOrgContext :one
-- GetSessionOrgContext resolves the owning organization of a session by
-- walking the sessions → events join. Used by the Bil24 compatibility
-- gateway (RESERVATION, feature #312 wiring) to anchor a reservation to
-- the correct tenant without requiring the caller to know the event_id.
-- Returns pgx.ErrNoRows when the session or its event does not exist or
-- has been soft-deleted.
SELECT s.id AS session_id, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.id         = $1
  AND  s.deleted_at IS NULL
  AND  e.deleted_at IS NULL;

-- name: GetSessionCurrency :one
-- GetSessionCurrency returns the ISO 4217 currency of an active session.
-- Used by the seating bind's tier auto-creation so every minted tier is
-- denominated in the session currency (AB-38 — one currency per session).
SELECT trim(currency)::text AS currency
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetSessionEventID :one
-- GetSessionEventID returns the owning event of a session. Order creation
-- needs orders.event_id, but reservations only carry session_id, so the
-- checkout confirm and public-feed paths resolve the event through this
-- one-column lookup rather than a full session SELECT (W1-A6c, #488).
SELECT event_id
FROM   sessions
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: ListActionEventsByOrg :many
-- ListActionEventsByOrg returns every sellable session of the given
-- organization's published events, shaped for the Bil24-compat
-- GET_ALL_ACTIONS actionEventList block (feature #497, spec §7.1).
--
-- Filter semantics come straight from spec §7.1: events must be
-- status='published' (visibility is NOT a filter — the site decides what to
-- show), sessions must be status='scheduled' and start no earlier than six
-- hours ago (a session that started within the last six hours is still being
-- sold at the door), and soft-deleted rows on either side are out.
--
-- The aggregate sub-selects keep the projection at ONE query for the whole
-- catalog instead of three per session:
--   * sell_end_at — min(sale_window_end) over the session's live tiers; NULL
--     when no tier carries a window, and the handler then falls back to
--     start_at as the spec requires.
--   * seats_total / seats_available — the materialised session_seats pool
--     (assigned seats AND ga_units, AB-51). seats_total = 0 means the session
--     has no unit rows at all, which is the signal to price availability off
--     the ledger instead.
--   * ledger_available — capacity_total − sold − held on the session-level
--     (tier_id IS NULL) inventory_ledger row. A NULL capacity_total means
--     "unlimited" and COALESCEs to 0 here: a session that is both unit-less
--     and uncapped has no finite remaining count to report.
--
-- timezone is venues.timezone and MAY be NULL. Spec §7.1 requires the session
-- to be dropped from the response with a warn log in that case, so the column
-- is projected rather than filtered in SQL — the handler needs the venue name
-- for the log line.
--
-- poster_media_id / event_poster_media_id / event_image_url (feature #498,
-- spec §7.1 "Постеры — публичный URL media_objects постера сеанса с fallback
-- на events.image_url"): the three-tier cover resolution the handler applies
-- is session override > event poster > legacy free-form URL, projected here
-- so buildActionEntry needs no extra round-trip.
SELECT s.id                                        AS session_id,
       s.event_id,
       s.venue_id,
       v.city_id,
       v.name                                      AS venue_name,
       v.timezone,
       s.start_at,
       trim(s.currency)::text                      AS currency,
       s.admission_mode,
       sp.name                                     AS seating_plan_name,
       s.poster_media_id,
       e.poster_media_id                           AS event_poster_media_id,
       e.image_url                                 AS event_image_url,
       (SELECT min(tt.sale_window_end)
          FROM   ticket_tiers tt
          WHERE  tt.session_id = s.id
            AND  tt.deleted_at IS NULL
            AND  tt.sale_window_end IS NOT NULL)   AS sell_end_at,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = s.id)::int        AS seats_total,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = s.id
            AND  ss.status = 'available')::int     AS seats_available,
       COALESCE((SELECT il.capacity_total - il.capacity_sold - il.capacity_held
                   FROM   inventory_ledger il
                   WHERE  il.session_id = s.id
                     AND  il.tier_id IS NULL), 0)::int AS ledger_available
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
JOIN   venues   v ON v.id = s.venue_id
LEFT JOIN seating_plan_versions spv ON spv.id = s.seating_plan_version_id
LEFT JOIN seating_plans         sp  ON sp.id  = spv.seating_plan_id
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  s.status     = 'scheduled'
  AND  s.start_at   > now() - interval '6 hours'
ORDER BY s.start_at ASC, s.id ASC;

-- name: ListActionEventTiersByOrg :many
-- ListActionEventTiersByOrg returns every live ticket tier of the same session
-- set as ListActionEventsByOrg, for the spec §7.1 categoryLimitList block and
-- the minPrice / maxPrice aggregates (feature #497).
--
-- The GA/seated split is PROJECTED (is_ga) rather than filtered, because the
-- two consumers want different subsets out of the same single round-trip:
--   * categoryLimitList takes is_ga = true only. "GA tier" per spec §7.1 = a
--     tier that sells a place without a seat: every tier of a
--     general_admission session, plus the tiers stamped on kind='ga_unit' rows
--     of a hybrid session. Seated tiers are deliberately excluded — their
--     absence is how the WP plugin tells "pure seating" (empty
--     categoryLimitList) from "combined" (bil24-acf-sync.php:434-446).
--   * minPrice / maxPrice span ALL live tiers, so a pure-seating action still
--     advertises a "from" price even though it exposes no categories here.
--
-- ga_units_available counts the tier's own free GA units; ga_units_total tells
-- "this tier has no unit rows at all" (a GA pool with NULL tier_id, the common
-- shape) apart from "genuinely sold out", so the handler knows when to fall
-- back to the session-level remaining count.
--
-- The row carries the full ticket_tiers column list so the handler can reuse
-- the shared TicketTierRow scanner and feed priceresolve.ForTiers in ONE
-- round-trip for the whole catalog.
SELECT tt.id, tt.session_id, tt.name, tt.pricing_mode, tt.price_amount,
       tt.currency, tt.pwyw_min, tt.pwyw_max, tt.capacity,
       tt.sale_window_start, tt.sale_window_end, tt.sort_order,
       tt.created_at, tt.updated_at, tt.deleted_at,
       (s.admission_mode = 'general_admission'
        OR (s.admission_mode = 'hybrid'
            AND EXISTS (SELECT 1
                          FROM   session_seats ss3
                          WHERE  ss3.session_id = tt.session_id
                            AND  ss3.tier_id    = tt.id
                            AND  ss3.kind       = 'ga_unit'))) AS is_ga,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = tt.session_id
            AND  ss.tier_id    = tt.id
            AND  ss.kind       = 'ga_unit')::int      AS ga_units_total,
       (SELECT count(*)
          FROM   session_seats ss
          WHERE  ss.session_id = tt.session_id
            AND  ss.tier_id    = tt.id
            AND  ss.kind       = 'ga_unit'
            AND  ss.status     = 'available')::int    AS ga_units_available
FROM   ticket_tiers tt
JOIN   sessions s ON s.id = tt.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  e.org_id     = $1
  AND  e.status     = 'published'
  AND  e.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  s.status     = 'scheduled'
  AND  s.start_at   > now() - interval '6 hours'
  AND  tt.deleted_at IS NULL
ORDER BY tt.session_id, tt.sort_order ASC, tt.id ASC;
