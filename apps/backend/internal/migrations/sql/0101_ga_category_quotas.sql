-- +goose Up
-- =====================================================================
-- arena_new — GA category quotas (plan 08_architecture/23, step 2)
--
-- Target model: a General Admission category OWNS its places. Its
-- quantity is ticket_tiers.capacity, the session capacity is always the
-- sum of the category quantities, and every ga_unit row carries a hard
-- category (session_seats.tier_id). The pre-0101 "shared pool" shape —
-- one fungible batch of 'ga|pool|<n>' units with tier_id NULL, stamped
-- on hold and reset on release — held the truth about quantity in two
-- unsynchronised places (sessions.capacity_total and the per-category
-- ticket_tiers.capacity) and made every per-category availability count
-- a special case. This migration converts the existing pool sessions to
-- the quota shape.
--
-- Schema added here:
--
--   ticket_tiers.is_open  — a closed category takes no NEW holds. The
--                           sales gates themselves land in step 4; the
--                           column and its default ship here so the
--                           quota mechanism (step 3) can write it.
--   ticket_tiers.unit_seq — stable per-session category number used to
--                           build the seat_key prefix of the category's
--                           own units: 'ga|t<unit_seq>|<n>'.
--
-- Key-prefix note: the prefix is deliberately NOT 'ga|c'. 'ga|c<index>'
-- is the seating-geometry category index (hseating/bind.go,
-- himports/seating.go); on a session bound to a plan a hand-added
-- category's number would collide with a geometry index under
-- UNIQUE (session_id, seat_key). Code distinguishes GA places only by
-- kind='ga_unit' and the 'ga|' prefix, so a third prefix is safe.
-- Existing keys ('ga|pool|000003', 'ga|c1|000007') are NEVER renamed —
-- tickets.seat_key references them verbatim.
-- =====================================================================

ALTER TABLE ticket_tiers
    ADD COLUMN is_open  boolean NOT NULL DEFAULT true,
    ADD COLUMN unit_seq integer NULL;

COMMENT ON COLUMN ticket_tiers.is_open IS
    'Open/closed flag for a ticket category. A closed category (false) '
    'accepts no NEW holds; places already held or sold are unaffected '
    'and an order already placed can still be paid. Defaults to true. '
    'GA category quotas — plan 08_architecture/23 step 2.';

COMMENT ON COLUMN ticket_tiers.unit_seq IS
    'Stable per-session category number used to build the seat_key '
    'prefix of this category''s own GA places: ga|t<unit_seq>|<n> with '
    'n zero-padded to 6. Assigned once and never reused, including '
    'after a soft delete, so a deleted category''s keys can never be '
    'minted again. Unique per session where NOT NULL. Deliberately '
    'distinct from the geometry category index prefix ga|c<index>.';

-- Backfill unit_seq for every existing category, soft-deleted rows
-- INCLUDED: a number that belonged to a deleted category must stay
-- retired so its old seat keys can never be re-minted.
WITH numbered AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY session_id
               ORDER BY sort_order, created_at, id
           ) AS seq
    FROM   ticket_tiers
)
UPDATE ticket_tiers tt
SET    unit_seq = n.seq
FROM   numbered n
WHERE  n.id = tt.id;

CREATE UNIQUE INDEX ticket_tiers_unit_seq_uq
    ON ticket_tiers (session_id, unit_seq)
    WHERE unit_seq IS NOT NULL;

-- ---------------------------------------------------------------------
-- Data conversion: pool -> per-category quotas.
--
-- Scope: plan-less General Admission sessions that actually carry pool
-- units. Deterministic and re-runnable: a second run over an already
-- converted session recomputes the same targets, re-picks the same
-- units (already-stamped units are preferred first) and therefore
-- deletes and inserts nothing.
--
-- Referenced-unit rule: reservation_seats.session_seat_id and
-- order_items.session_seat_id both reference session_seats WITHOUT a
-- cascade, and a converted reservation keeps its join rows, so an
-- AVAILABLE unit may still be referenced. Such a unit can never be
-- deleted (23503); it is assigned to a category BEFORE anything else,
-- and if that pushes the category past its stated quantity, the
-- quantity is raised to the number of units it actually owns.
-- ---------------------------------------------------------------------

-- +goose StatementBegin
DO $$
DECLARE
    v_sess        uuid;
    v_tier        record;
    v_tier_ids    uuid[];
    v_targets     int[];
    v_i           int;
    v_n           int;
    v_null_count  int;
    v_avail       int;
    v_claimed     int;
    v_remaining   int;
    v_share       int;
    v_used        int;
    v_pre         int;
    v_target      int;
    v_need        int;
    v_max_idx     int;
    v_prefix      text;
    v_first_tier  uuid;
BEGIN
    -- Planned (unit -> category) assignment for the session in hand.
    CREATE TEMP TABLE ga101_plan (
        unit_id uuid PRIMARY KEY,
        tier_id uuid NOT NULL
    ) ON COMMIT DROP;

    -- Every session whose capacity_total / ledger must be recomputed.
    CREATE TEMP TABLE ga101_touched (
        session_id uuid PRIMARY KEY
    ) ON COMMIT DROP;

    FOR v_sess IN
        SELECT s.id
        FROM   sessions s
        WHERE  s.deleted_at              IS NULL
          AND  s.admission_mode           = 'general_admission'
          AND  s.seating_plan_version_id IS NULL
          AND  EXISTS (SELECT 1
                         FROM   session_seats ss
                         WHERE  ss.session_id = s.id
                           AND  ss.kind       = 'ga_unit'
                           AND  ss.seat_key LIKE 'ga|pool|%')
        ORDER BY s.id
    LOOP
        -- Active categories in deterministic display order.
        SELECT array_agg(tt.id ORDER BY tt.sort_order, tt.created_at, tt.id)
          INTO v_tier_ids
          FROM ticket_tiers tt
         WHERE tt.session_id = v_sess
           AND tt.deleted_at IS NULL;

        IF v_tier_ids IS NULL THEN
            -- No active category: nothing can be sold on this session at
            -- all. Leave its units and capacity exactly as they are; the
            -- step-1 report lists these sessions for a human to fix.
            CONTINUE;
        END IF;

        v_n          := array_length(v_tier_ids, 1);
        v_first_tier := v_tier_ids[1];

        -- Every category needs its number before its key prefix can be
        -- built. The backfill above already covers everything that exists
        -- when the migration runs; this keeps the block self-sufficient if
        -- it is ever replayed against a category created later. One at a
        -- time, so two categories cannot pick the same number.
        FOR v_i IN 1 .. v_n LOOP
            UPDATE ticket_tiers tt
               SET unit_seq = (SELECT COALESCE(MAX(x.unit_seq), 0) + 1
                                 FROM ticket_tiers x
                                WHERE x.session_id = v_sess),
                   updated_at = now()
             WHERE tt.id = v_tier_ids[v_i]
               AND tt.unit_seq IS NULL;
        END LOOP;

        TRUNCATE ga101_plan;

        -- A non-available unit with no active category of its own cannot
        -- be released back into any quota, so it is folded into the first
        -- category, which then owns (and pays for) it.
        UPDATE session_seats ss
           SET tier_id    = v_first_tier,
               updated_at = now()
         WHERE ss.session_id = v_sess
           AND ss.kind       = 'ga_unit'
           AND ss.status    <> 'available'
           AND NOT EXISTS (SELECT 1
                             FROM   ticket_tiers tt
                            WHERE  tt.id         = ss.tier_id
                              AND  tt.deleted_at IS NULL);

        -- Phase 1 — AVAILABLE units that are still referenced by a
        -- reservation_seats / order_items join row. They can never be
        -- deleted, so they are placed first: onto their own category
        -- when it is still active, otherwise onto the first one.
        INSERT INTO ga101_plan (unit_id, tier_id)
        SELECT ss.id,
               COALESCE(
                   (SELECT tt.id
                      FROM   ticket_tiers tt
                     WHERE  tt.id         = ss.tier_id
                       AND  tt.deleted_at IS NULL),
                   v_first_tier)
          FROM session_seats ss
         WHERE ss.session_id = v_sess
           AND ss.kind       = 'ga_unit'
           AND ss.status     = 'available'
           AND (EXISTS (SELECT 1 FROM reservation_seats rs
                         WHERE rs.session_seat_id = ss.id)
             OR EXISTS (SELECT 1 FROM order_items oi
                         WHERE oi.session_seat_id = ss.id));

        -- Free units left for the quota split.
        SELECT count(*)
          INTO v_avail
          FROM session_seats ss
         WHERE ss.session_id = v_sess
           AND ss.kind       = 'ga_unit'
           AND ss.status     = 'available'
           AND NOT EXISTS (SELECT 1 FROM ga101_plan g WHERE g.unit_id = ss.id);

        -- Target quantity per category. A stated capacity wins; a
        -- category without one shares what the stated ones leave over.
        v_targets   := ARRAY[]::int[];
        v_claimed   := 0;
        v_null_count := 0;

        FOR v_i IN 1 .. v_n LOOP
            SELECT tt.capacity INTO v_target
              FROM ticket_tiers tt WHERE tt.id = v_tier_ids[v_i];

            SELECT count(*) INTO v_used
              FROM session_seats ss
             WHERE ss.session_id = v_sess
               AND ss.kind       = 'ga_unit'
               AND ss.status    <> 'available'
               AND ss.tier_id    = v_tier_ids[v_i];

            SELECT count(*) INTO v_pre
              FROM ga101_plan g WHERE g.tier_id = v_tier_ids[v_i];

            IF v_target IS NULL THEN
                v_null_count := v_null_count + 1;
                v_targets := array_append(v_targets, -1);   -- decided below
            ELSE
                v_target := GREATEST(v_target, v_used + v_pre);
                v_targets := array_append(v_targets, v_target);
                v_claimed := v_claimed + GREATEST(v_target - v_used - v_pre, 0);
            END IF;
        END LOOP;

        v_remaining := GREATEST(v_avail - v_claimed, 0);

        IF v_null_count > 0 THEN
            FOR v_i IN 1 .. v_n LOOP
                CONTINUE WHEN v_targets[v_i] <> -1;

                -- Even split, remainder to the earlier category.
                v_share := v_remaining / v_null_count;
                IF (v_remaining % v_null_count) > 0 THEN
                    v_share := v_share + 1;
                END IF;

                SELECT count(*) INTO v_used
                  FROM session_seats ss
                 WHERE ss.session_id = v_sess
                   AND ss.kind       = 'ga_unit'
                   AND ss.status    <> 'available'
                   AND ss.tier_id    = v_tier_ids[v_i];

                SELECT count(*) INTO v_pre
                  FROM ga101_plan g WHERE g.tier_id = v_tier_ids[v_i];

                v_target := v_used + v_pre + v_share;
                -- ticket_tiers_capacity_positive: a quota of 0 is illegal.
                IF v_target < 1 THEN
                    v_target := 1;
                END IF;

                v_targets[v_i] := v_target;
                v_remaining    := v_remaining - v_share;
                v_null_count   := v_null_count - 1;
            END LOOP;
        END IF;

        -- Phase 2 — hand each category the free units it still needs.
        -- Preference: units already stamped with this category, then
        -- unstamped ones, then ones stamped with another category; by
        -- seat_key inside each group, so the pass is deterministic and
        -- re-running it changes nothing.
        FOR v_i IN 1 .. v_n LOOP
            SELECT count(*) INTO v_used
              FROM session_seats ss
             WHERE ss.session_id = v_sess
               AND ss.kind       = 'ga_unit'
               AND ss.status    <> 'available'
               AND ss.tier_id    = v_tier_ids[v_i];

            SELECT count(*) INTO v_pre
              FROM ga101_plan g WHERE g.tier_id = v_tier_ids[v_i];

            v_need := v_targets[v_i] - v_used - v_pre;

            IF v_need > 0 THEN
                INSERT INTO ga101_plan (unit_id, tier_id)
                SELECT p.id, v_tier_ids[v_i]
                  FROM (
                        SELECT ss.id
                          FROM session_seats ss
                         WHERE ss.session_id = v_sess
                           AND ss.kind       = 'ga_unit'
                           AND ss.status     = 'available'
                           AND NOT EXISTS (SELECT 1 FROM ga101_plan g
                                            WHERE g.unit_id = ss.id)
                         ORDER BY CASE
                                    WHEN ss.tier_id = v_tier_ids[v_i] THEN 0
                                    WHEN ss.tier_id IS NULL           THEN 1
                                    ELSE 2
                                  END,
                                  ss.seat_key
                         LIMIT v_need
                       ) p;
            END IF;
        END LOOP;

        -- Apply the plan.
        UPDATE session_seats ss
           SET tier_id    = g.tier_id,
               updated_at = now()
          FROM ga101_plan g
         WHERE ss.id      = g.unit_id
           AND ss.tier_id IS DISTINCT FROM g.tier_id;

        -- Surplus free units that nothing references: remove them.
        DELETE FROM session_seats ss
         WHERE ss.session_id = v_sess
           AND ss.kind       = 'ga_unit'
           AND ss.status     = 'available'
           AND NOT EXISTS (SELECT 1 FROM ga101_plan g
                            WHERE g.unit_id = ss.id)
           AND NOT EXISTS (SELECT 1 FROM reservation_seats rs
                            WHERE rs.session_seat_id = ss.id)
           AND NOT EXISTS (SELECT 1 FROM order_items oi
                            WHERE oi.session_seat_id = ss.id);

        -- Deficit: mint the missing places under the category's own key
        -- prefix, continuing from the highest index already used there.
        FOR v_i IN 1 .. v_n LOOP
            SELECT count(*) INTO v_used
              FROM session_seats ss
             WHERE ss.session_id = v_sess
               AND ss.kind       = 'ga_unit'
               AND ss.tier_id    = v_tier_ids[v_i];

            v_need := v_targets[v_i] - v_used;
            IF v_need > 0 THEN
                SELECT 'ga|t' || tt.unit_seq INTO v_prefix
                  FROM ticket_tiers tt WHERE tt.id = v_tier_ids[v_i];

                SELECT COALESCE(MAX(split_part(ss.seat_key, '|', 3)::int), 0)
                  INTO v_max_idx
                  FROM session_seats ss
                 WHERE ss.session_id = v_sess
                   AND ss.kind       = 'ga_unit'
                   AND ss.seat_key LIKE v_prefix || '|%';

                INSERT INTO session_seats
                    (session_id, seat_key, sector_name, row_name, seat_number,
                     tier_id, status, kind)
                SELECT v_sess,
                       v_prefix || '|' || lpad((gs + v_max_idx)::text, 6, '0'),
                       '', '', '',
                       v_tier_ids[v_i],
                       'available',
                       'ga_unit'
                  FROM generate_series(1, v_need) gs;
            END IF;

            -- The quota is now exactly the number of places owned.
            UPDATE ticket_tiers
               SET capacity   = v_targets[v_i],
                   updated_at = now()
             WHERE id = v_tier_ids[v_i]
               AND capacity IS DISTINCT FROM v_targets[v_i];
        END LOOP;

        INSERT INTO ga101_touched (session_id) VALUES (v_sess)
        ON CONFLICT (session_id) DO NOTHING;
    END LOOP;

    -- Hybrid sessions: migration 0084 gave them a 'ga|pool|%' batch with
    -- tier_id NULL for the plan's standing capacity. Those places are
    -- unsellable (AllocateGAUnitsForHold filters plan-bound holds by
    -- category) yet still counted, so they go — again only when nothing
    -- references them. Only a hybrid session that actually loses places
    -- is recomputed; the others are left exactly as they are.
    INSERT INTO ga101_touched (session_id)
    SELECT DISTINCT ss.session_id
      FROM session_seats ss
      JOIN sessions s ON s.id = ss.session_id
     WHERE s.deleted_at     IS NULL
       AND s.admission_mode = 'hybrid'
       AND ss.kind          = 'ga_unit'
       AND ss.seat_key LIKE 'ga|pool|%'
       AND ss.tier_id       IS NULL
       AND ss.status        = 'available'
       AND NOT EXISTS (SELECT 1 FROM reservation_seats rs
                        WHERE rs.session_seat_id = ss.id)
       AND NOT EXISTS (SELECT 1 FROM order_items oi
                        WHERE oi.session_seat_id = ss.id)
    ON CONFLICT (session_id) DO NOTHING;

    DELETE FROM session_seats ss
     USING sessions s
     WHERE s.id             = ss.session_id
       AND s.deleted_at     IS NULL
       AND s.admission_mode = 'hybrid'
       AND ss.kind          = 'ga_unit'
       AND ss.seat_key LIKE 'ga|pool|%'
       AND ss.tier_id       IS NULL
       AND ss.status        = 'available'
       AND NOT EXISTS (SELECT 1 FROM reservation_seats rs
                        WHERE rs.session_seat_id = ss.id)
       AND NOT EXISTS (SELECT 1 FROM order_items oi
                        WHERE oi.session_seat_id = ss.id);

    -- Capacity and ledger, once per touched session.
    -- GA session: capacity = ga_unit rows. Hybrid: seats + ga_unit rows.
    FOR v_sess IN SELECT session_id FROM ga101_touched ORDER BY session_id
    LOOP
        SELECT count(*) INTO v_avail
          FROM session_seats ss
         WHERE ss.session_id = v_sess
           AND ss.kind IN ('seat', 'ga_unit');

        CONTINUE WHEN v_avail <= 0;   -- sessions_capacity_total_check

        UPDATE sessions
           SET capacity_total = v_avail,
               updated_at     = now()
         WHERE id             = v_sess
           AND capacity_total IS DISTINCT FROM v_avail;

        UPDATE inventory_ledger il
           SET capacity_total = GREATEST(v_avail,
                                         il.capacity_held + il.capacity_sold),
               version        = il.version + 1,
               updated_at     = now()
         WHERE il.session_id = v_sess
           AND il.tier_id    IS NULL;

        IF NOT FOUND THEN
            INSERT INTO inventory_ledger (session_id, tier_id, capacity_total)
            VALUES (v_sess, NULL, v_avail);
        END IF;
    END LOOP;
END $$;
-- +goose StatementEnd

-- Per-category ledger rows are not part of the quota model: places are
-- the per-category truth and nothing in the code base ever creates one
-- (every InsertInventoryLedger call site passes a nil tier). The few
-- historical rows only ever made free tickets and external allocations
-- answer 409.
DELETE FROM inventory_ledger WHERE tier_id IS NOT NULL;

-- +goose Down
-- Only the schema is reverted. The unit / capacity / ledger conversion
-- above is NOT undone: the pre-0101 pool shape cannot be reconstructed
-- (surplus places were deleted, deficits minted, and the fungible
-- NULL-category pool no longer exists). A rollback therefore leaves the
-- data in the quota shape. Pre-0101 code cannot SELL those places: its
-- plan-less path allocates only tier_id IS NULL units, and none remain.
DROP INDEX IF EXISTS ticket_tiers_unit_seq_uq;

ALTER TABLE ticket_tiers
    DROP COLUMN IF EXISTS unit_seq,
    DROP COLUMN IF EXISTS is_open;
