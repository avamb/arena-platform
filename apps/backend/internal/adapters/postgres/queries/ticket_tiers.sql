-- ticket_tiers.sql — sqlc query definitions for the ticket_tiers table (feature #127).
-- Tiers are scoped to a session via the session_id foreign key.
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertTicketTier :one
-- InsertTicketTier creates a new pricing tier for the given session.
-- price_amount is in smallest currency units (cents). Defaults to 0 for free tiers.
-- Returns the created row including the uuidv7 PK assigned by the database.
INSERT INTO ticket_tiers (
    session_id, name, pricing_mode, price_amount, currency,
    pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end, sort_order,
    is_open
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
        COALESCE($12::boolean, true))
RETURNING id, session_id, name, pricing_mode, price_amount, currency,
          pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
          sort_order, created_at, updated_at, deleted_at, is_open, unit_seq;

-- name: GetTicketTierByID :one
-- GetTicketTierByID fetches an active tier by its UUID primary key scoped to the session.
-- Returns pgx.ErrNoRows when not found, already deleted, or belongs to a different session.
SELECT id, session_id, name, pricing_mode, price_amount, currency,
       pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
       sort_order, created_at, updated_at, deleted_at, is_open, unit_seq
FROM   ticket_tiers
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL;

-- name: GetTicketTierByIDGlobal :one
-- GetTicketTierByIDGlobal fetches an active tier by its UUID primary key without
-- requiring a session_id. Used by the checkout/quote endpoint where only the
-- tier_id is known. Returns pgx.ErrNoRows when not found or soft-deleted.
SELECT id, session_id, name, pricing_mode, price_amount, currency,
       pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
       sort_order, created_at, updated_at, deleted_at, is_open, unit_seq
FROM   ticket_tiers
WHERE  id         = $1
  AND  deleted_at IS NULL;

-- name: ListTicketTiersBySession :many
-- ListTicketTiersBySession returns all active tiers for the given session.
-- Ordered by sort_order ASC then id ASC so the display order is stable.
SELECT id, session_id, name, pricing_mode, price_amount, currency,
       pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
       sort_order, created_at, updated_at, deleted_at, is_open, unit_seq
FROM   ticket_tiers
WHERE  session_id = $1
  AND  deleted_at IS NULL
ORDER BY sort_order ASC, id ASC;

-- name: UpdateTicketTier :one
-- UpdateTicketTier applies a partial update to an active tier scoped by session_id.
-- Empty string fields leave the existing string values unchanged.
-- NULL price_amount / sort_order / is_open keep the existing column values.
--
-- The five NULLABLE columns pwyw_min, pwyw_max, capacity, sale_window_start and
-- sale_window_end carry an explicit SET FLAG each ($14..$18) instead of
-- overloading NULL, because NULL is itself a meaningful stored value for them:
-- with `CASE WHEN $n IS NOT NULL` an operator could never CLEAR a bound or a
-- sale window — the admin sent `null` and the old value silently survived.
-- Flag false = leave the column untouched; flag true = write the value given,
-- NULL included. Same idiom as set_reservation_ttl_override on sales_channels.
UPDATE ticket_tiers
SET    name              = COALESCE(NULLIF($3, ''), name),
       pricing_mode      = COALESCE(NULLIF($4, ''), pricing_mode),
       price_amount      = CASE WHEN $5::bigint   IS NOT NULL THEN $5::bigint   ELSE price_amount     END,
       currency          = COALESCE(NULLIF($6, ''), currency),
       pwyw_min          = CASE WHEN $14::boolean THEN $7::bigint  ELSE pwyw_min END,
       pwyw_max          = CASE WHEN $15::boolean THEN $8::bigint  ELSE pwyw_max END,
       capacity          = CASE WHEN $16::boolean THEN $9::integer ELSE capacity END,
       sale_window_start = CASE WHEN $17::boolean THEN $10::timestamptz ELSE sale_window_start END,
       sale_window_end   = CASE WHEN $18::boolean THEN $11::timestamptz ELSE sale_window_end   END,
       sort_order        = CASE WHEN $12::integer IS NOT NULL THEN $12::integer ELSE sort_order       END,
       is_open           = CASE WHEN $13::boolean IS NOT NULL THEN $13::boolean ELSE is_open          END,
       updated_at        = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING id, session_id, name, pricing_mode, price_amount, currency,
          pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
          sort_order, created_at, updated_at, deleted_at, is_open, unit_seq;

-- name: SoftDeleteTicketTier :one
-- SoftDeleteTicketTier marks a tier as deleted by setting deleted_at.
-- Scoped by session_id to enforce owner-gated mutation policy.
UPDATE ticket_tiers
SET    deleted_at = now(),
       updated_at = now()
WHERE  id         = $1
  AND  session_id = $2
  AND  deleted_at IS NULL
RETURNING id, session_id, name, pricing_mode, price_amount, currency,
          pwyw_min, pwyw_max, capacity, sale_window_start, sale_window_end,
          sort_order, created_at, updated_at, deleted_at, is_open, unit_seq;
