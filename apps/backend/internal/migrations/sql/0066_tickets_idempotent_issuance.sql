-- 0066_tickets_idempotent_issuance.sql — add ordinal column + unique constraint
-- for idempotent, concurrent-safe ticket issuance (feature #366, PR2-10).
--
-- Problem: the original idempotency check was "list-then-insert" with no
-- database-level lock or unique constraint. Two concurrent callers (e.g. a
-- payment.succeeded webhook replay racing the checkout-complete handler) both
-- saw 0 tickets and both started inserting → double-issuance. A mid-loop
-- failure (ticket 3 of 5 fails) left partial tickets; the next retry returned
-- the partial set as "already issued".
--
-- Fix:
--   1. Add `ordinal` (0-based ticket index within checkout session) so each
--      ticket has a deterministic identity.
--   2. Add UNIQUE (checkout_session_id, ordinal) so the DB rejects true
--      duplicate inserts even if the application advisory lock is bypassed.
--   3. Application logic (IssueTicketsForCheckout) now:
--        a. Acquires pg_advisory_xact_lock(low-64-bits-of-uuid) before counting.
--        b. Counts existing tickets vs. expected quantity.
--        c. Inserts ONLY missing ordinals on partial recovery.
--      This is idempotent and concurrent-safe.

-- +goose Up

-- Step 1: Add the ordinal column (nullable initially so the backfill can run
-- before we impose NOT NULL).
ALTER TABLE tickets ADD COLUMN IF NOT EXISTS ordinal int;

-- Step 2: Backfill — assign 0-based ordinals within each checkout session,
-- ordered by issuance time then UUID (deterministic ordering).
UPDATE tickets t
SET ordinal = numbered.rn - 1
FROM (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY checkout_session_id
               ORDER BY issued_at, id
           ) AS rn
    FROM tickets
) numbered
WHERE t.id = numbered.id;

-- Step 3: Enforce NOT NULL and add a DEFAULT so new single-ticket inserts
-- can omit the column and get ordinal=0.
ALTER TABLE tickets ALTER COLUMN ordinal SET NOT NULL;
ALTER TABLE tickets ALTER COLUMN ordinal SET DEFAULT 0;

-- Step 4: Add the UNIQUE constraint that prevents DB-level double-issuance.
-- The name matches the project convention for named constraints.
ALTER TABLE tickets
    ADD CONSTRAINT tickets_checkout_ordinal_uq
    UNIQUE (checkout_session_id, ordinal);

COMMENT ON COLUMN tickets.ordinal IS
    '0-based sequence number of this ticket within the checkout session. '
    'Combined with checkout_session_id the pair is unique (UNIQUE constraint), '
    'preventing concurrent double-issuance at the database level. '
    'IssueTicketsForCheckout assigns ordinal=i for the i-th ticket in the loop '
    'and uses pg_advisory_xact_lock + partial-recovery logic to complete '
    'a partial-issue set on retry instead of re-issuing already-existing tickets.';

-- +goose Down

ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_checkout_ordinal_uq;
ALTER TABLE tickets DROP COLUMN IF EXISTS ordinal;
