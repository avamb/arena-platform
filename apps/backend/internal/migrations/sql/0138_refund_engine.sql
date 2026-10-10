-- 0138_refund_engine.sql — the data the refund engine needs (spec
-- 08_architecture/36_payment_modules_refunds_acquiring_ru.md §7, PAY-03).
--
-- Until now an approved refund was only MARKED provider_pending ("simulating
-- provider submission") and the provider was never called, so the money of
-- every approved refund stayed with the organizer. The engine
-- (internal/platform/refunds) records a refund first, calls the provider
-- outside any transaction with the refund id as idempotency key, records the
-- answer, and only then cancels the ticket. This migration gives it:
--
--   * refund_batches — one row per "refund these tickets" operation; its
--     (org_id, idempotency_key) pair is where the Idempotency-Key of the
--     create call is kept, so a replay answers the first batch;
--   * refunds.provider / provider_status / failure_code — which module the
--     refund goes through and what it last said;
--   * refunds.origin — arena-initiated, or seen first in the provider's
--     dashboard (PAY-05 writes those);
--   * refunds.batch_id / cancel_ticket — the operation and whether the
--     ticket is cancelled once the provider accepts;
--   * refunds.provider_attempts / provider_attempted_at — the "a call is in
--     flight" marker refund.sweep respects (never re-call within a minute);
--   * refunds.first_attempted_at — when the provider was first asked: no
--     re-POST once it is 23 hours old (Stripe keeps an idempotency key for
--     24 hours from the first request);
--   * refunds.repair_attempts / repair_attempted_at — refund.sweep's bounded
--     retries of a ticket cancellation after an accepted refund;
--   * refunds.alert_due_at / review_alerted_at — an ops alert is owed
--     (set on every move into manual_review, and when a late provider
--     acceptance revives a failed refund) / when the last one was sent;
--     arena-worker's refund.sweep sends it, whichever process moved the row;
--   * refunds.alert_lease_until — the pass sending an owed alert holds it;
--     alert_due_at is cleared only after a confirmed delivery;
--   * refunds.alert_attempts — failed deliveries of the owed alert: the
--     ones with fewer failures go first, so a message Telegram keeps
--     refusing never blocks the others, and it is given up after a cap;
--   * refunds.refunded_published_at — v1.ticket.refunded was claimed for
--     publication, so it is published at most once;
--   * refunds.cancelled_ticket_id — the ticket POST /v1/tickets/{id}/cancel
--     cancelled before it wrote this ticket-less refund: such a refund
--     speaks for that one ticket only. requested_by is free text a client of
--     POST /v1/refunds sends, so it cannot carry that meaning (PAY-03 fifth
--     review, M-4); existing rows are backfilled where the ticket's own
--     refund link agrees with requested_by;
--   * refunds.settled_at — every step after the provider accepted (ticket
--     cancellations, the order projection, the publish once it succeeded)
--     is done; refund.sweep's repair finishes an accepted refund without it;
--   * a unique (provider, provider_refund_id) so one provider refund maps to
--     one row, and at most one live CANCELLING refund per ticket;
--   * usage_records.tickets_refunded — the counter PAY-11 bills from (only
--     the column here; counting and the tariff switch are PAY-11).
--
-- refunds_settlement_shape_check already lets a provider refund name its
-- order and ticket (it only demands payment_intent_id for 'provider'), so it
-- is left as is. Existing rows keep provider NULL: the engine and its sweep
-- only touch rows it created, and the unique index cannot trip over old data.
--
-- Rows the old approve left in provider_pending without a provider refund id
-- were never sent to any provider. Driving them now could return money an
-- organizer has since returned by hand in the provider's dashboard, so they
-- move to manual_review with an explanation for an operator to decide.

-- +goose Up
-- +goose StatementBegin

CREATE TABLE refund_batches (
    id                uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    org_id            uuid        NOT NULL REFERENCES organizations(id),
    order_id          uuid        NOT NULL REFERENCES orders(id),
    payment_intent_id uuid        NOT NULL REFERENCES payment_intents(id),
    idempotency_key   text        NOT NULL
                      CONSTRAINT refund_batches_idempotency_key_check
                      CHECK (length(btrim(idempotency_key)) BETWEEN 1 AND 255),
    reason            text        NOT NULL
                      CONSTRAINT refund_batches_reason_check CHECK (btrim(reason) <> ''),
    notify_buyer      boolean     NOT NULL DEFAULT true,
    cancel_tickets    boolean     NOT NULL DEFAULT true,
    created_by        text,
    via               text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT refund_batches_org_idempotency_key_uq UNIQUE (org_id, idempotency_key)
);

CREATE INDEX refund_batches_order_id_idx ON refund_batches (order_id);

ALTER TABLE refunds
    ADD COLUMN provider              text,
    ADD COLUMN provider_status       text,
    ADD COLUMN failure_code          text,
    ADD COLUMN origin                text    NOT NULL DEFAULT 'arena'
        CONSTRAINT refunds_origin_check CHECK (origin IN ('arena', 'provider_dashboard')),
    ADD COLUMN batch_id              uuid    REFERENCES refund_batches(id),
    ADD COLUMN cancel_ticket         boolean NOT NULL DEFAULT false,
    ADD COLUMN provider_attempts     integer NOT NULL DEFAULT 0,
    ADD COLUMN provider_attempted_at timestamptz,
    ADD COLUMN first_attempted_at    timestamptz,
    ADD COLUMN repair_attempts       integer NOT NULL DEFAULT 0,
    ADD COLUMN repair_attempted_at   timestamptz,
    ADD COLUMN review_alerted_at     timestamptz,
    ADD COLUMN alert_due_at          timestamptz,
    ADD COLUMN alert_lease_until     timestamptz,
    ADD COLUMN alert_attempts        integer NOT NULL DEFAULT 0,
    ADD COLUMN refunded_published_at timestamptz,
    ADD COLUMN settled_at            timestamptz,
    ADD COLUMN cancelled_ticket_id   uuid;

UPDATE refunds r
SET    cancelled_ticket_id = t.id
FROM   tickets t
WHERE  t.refund_id = r.id
  AND  r.ticket_id IS NULL
  AND  r.requested_by = 'ticket.cancel:' || t.id::text;

CREATE UNIQUE INDEX refunds_provider_refund_uq
    ON refunds (provider, provider_refund_id)
    WHERE provider IS NOT NULL AND provider_refund_id IS NOT NULL;

CREATE UNIQUE INDEX refunds_live_ticket_cancel_uq
    ON refunds (ticket_id)
    WHERE cancel_ticket AND state NOT IN ('failed', 'rejected');

CREATE INDEX refunds_batch_id_idx ON refunds (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX refunds_ticket_id_idx ON refunds (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX refunds_engine_pending_idx ON refunds (created_at)
    WHERE state = 'provider_pending' AND provider IS NOT NULL;
CREATE INDEX refunds_alert_due_idx ON refunds (alert_due_at)
    WHERE alert_due_at IS NOT NULL;
CREATE INDEX refunds_engine_unsettled_idx ON refunds (updated_at)
    WHERE provider IS NOT NULL AND settled_at IS NULL AND state IN ('succeeded', 'provider_pending');

UPDATE refunds
SET    state          = 'manual_review',
       failure_reason = COALESCE(failure_reason,
           'approved before the refund engine existed: the provider was never asked to return this money; check the provider dashboard before acting'),
       updated_at     = now()
WHERE  settlement = 'provider'
  AND  state = 'provider_pending'
  AND  provider_refund_id IS NULL;

ALTER TABLE usage_records
    ADD COLUMN tickets_refunded bigint NOT NULL DEFAULT 0;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- The manual_review rows of the Up step stay where they are: nothing tells
-- them apart from rows an operator parked, and putting them back into a
-- state that pretends a provider call is pending would be the old defect.
ALTER TABLE usage_records DROP COLUMN IF EXISTS tickets_refunded;

DROP INDEX IF EXISTS refunds_engine_unsettled_idx;
DROP INDEX IF EXISTS refunds_alert_due_idx;
DROP INDEX IF EXISTS refunds_engine_pending_idx;
DROP INDEX IF EXISTS refunds_ticket_id_idx;
DROP INDEX IF EXISTS refunds_batch_id_idx;
DROP INDEX IF EXISTS refunds_live_ticket_cancel_uq;
DROP INDEX IF EXISTS refunds_provider_refund_uq;

ALTER TABLE refunds
    DROP COLUMN IF EXISTS cancelled_ticket_id,
    DROP COLUMN IF EXISTS settled_at,
    DROP COLUMN IF EXISTS refunded_published_at,
    DROP COLUMN IF EXISTS alert_attempts,
    DROP COLUMN IF EXISTS alert_lease_until,
    DROP COLUMN IF EXISTS alert_due_at,
    DROP COLUMN IF EXISTS review_alerted_at,
    DROP COLUMN IF EXISTS repair_attempted_at,
    DROP COLUMN IF EXISTS repair_attempts,
    DROP COLUMN IF EXISTS first_attempted_at,
    DROP COLUMN IF EXISTS provider_attempted_at,
    DROP COLUMN IF EXISTS provider_attempts,
    DROP COLUMN IF EXISTS cancel_ticket,
    DROP COLUMN IF EXISTS batch_id,
    DROP COLUMN IF EXISTS origin,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS provider_status,
    DROP COLUMN IF EXISTS provider;

DROP TABLE IF EXISTS refund_batches;

-- +goose StatementEnd
