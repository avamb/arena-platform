-- =====================================================================
-- arena_new — session_seat_batches: every change of a session's seats is a
-- batch that can be listed, traced per seat and reverted (task S1c, spec
-- 08_architecture/26_seat_layout_management_plan_ru.md sections 3.2 item 3
-- and 3.3, plan docs/plans/event_center_unified_plan_2026-10-14_ru.md 8.3)
--
-- seatlayout.ApplyBatch is the one transactional operation that repaints,
-- closes and opens seats of a session (the per-seat category PATCH and the
-- block/unblock PATCH are thin wrappers over it). Each call writes ONE row
-- here, in the same transaction as the seat updates:
--
--   * changes — [{seat_key, from: {tier_id, status}, to: {tier_id, status}}]
--               for every seat that really changed;
--   * skipped — [{seat_key, reason, status}] for every seat the batch left
--               alone (sold, held, a GA place, an unknown key, ...). A sold
--               or held seat is frozen: the batch skips it and does not fail.
--
-- A revert is a NEW batch (kind 'revert', reverts_batch_id = the original)
-- carrying the inverse changes; the original gets reverted_at and
-- reverted_by_batch_id in the same transaction. A batch is reverted at most
-- once (partial unique index) and a revert batch is never reverted itself.
--
-- The seat history (GET .../seats/{seat_key}/history) is a containment query
-- `changes @> '[{"seat_key": ...}]'`, served by the GIN index below.
--
-- No permission is added here: the routes keep the
-- event_session.assign_seating_plan gate until task S1f introduces
-- session.seats.manage.
-- =====================================================================

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE session_seat_batches (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id               uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id           uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    actor_id             uuid        NULL,
    actor_label          text        NOT NULL DEFAULT '',
    kind                 text        NOT NULL,
    seat_status_version  bigint      NOT NULL,
    applied_at           timestamptz NOT NULL DEFAULT now(),
    reverted_at          timestamptz NULL,
    reverted_by_batch_id uuid        NULL REFERENCES session_seat_batches(id),
    reverts_batch_id     uuid        NULL REFERENCES session_seat_batches(id),
    changes              jsonb       NOT NULL DEFAULT '[]'::jsonb,
    skipped              jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at           timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_seat_batches_kind_check
        CHECK (kind IN ('paint', 'close', 'open', 'apply_layout', 'revert')),
    CONSTRAINT session_seat_batches_actor_label_check
        CHECK (char_length(actor_label) <= 200),
    CONSTRAINT session_seat_batches_changes_array_check
        CHECK (jsonb_typeof(changes) = 'array'),
    CONSTRAINT session_seat_batches_skipped_array_check
        CHECK (jsonb_typeof(skipped) = 'array'),
    CONSTRAINT session_seat_batches_revert_link_check
        CHECK ((kind = 'revert') = (reverts_batch_id IS NOT NULL)),
    CONSTRAINT session_seat_batches_reverted_pair_check
        CHECK ((reverted_at IS NULL) = (reverted_by_batch_id IS NULL)),
    CONSTRAINT session_seat_batches_revert_not_reverted_check
        CHECK (kind <> 'revert' OR reverted_at IS NULL)
);

CREATE INDEX session_seat_batches_session_applied_idx
    ON session_seat_batches (session_id, applied_at DESC, id DESC);

CREATE INDEX session_seat_batches_changes_gin
    ON session_seat_batches USING gin (changes jsonb_path_ops);

CREATE UNIQUE INDEX session_seat_batches_reverts_uq
    ON session_seat_batches (reverts_batch_id)
    WHERE reverts_batch_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS session_seat_batches;
