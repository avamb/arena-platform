-- +goose Up
SET LOCAL lock_timeout = '5s';

-- =====================================================================
-- 0159 billing credits (billing wave B-B, task B3-1)
-- =====================================================================
-- 41 sections 5.2, 6.4, 6.5 and 8.1 (B-6, B-9, B-11, B-18):
--
--   * credit_lots   — a balance in the ACCOUNT currency (B-9: one balance,
--                     bonus is a discount in the same currency). A purchased
--                     lot carries the tier rates of the pack it was bought
--                     as (snapshot), a bonus lot has no tier. A lot is
--                     available to a charge only when purchased_at <=
--                     occurred_at and it has not expired at occurred_at.
--   * credit_ledger — append-only journal of every change of a lot's
--                     remaining_minor (purchase, bonus_grant, consume,
--                     reversal, reversal_dropped, expire, hold, release,
--                     adjustment_credit, adjustment_debit). remaining_minor
--                     of a lot always equals the signed sum of its journal
--                     (billing.reconcile checks it), and remaining_after is
--                     the lot's balance right after the entry (a snapshot).
--   * charge_allocations.lot_id gets its foreign key, and a new allocation
--     kind pack_rate_discount: the part of a pay-as-you-go charge a pack
--     tier makes cheaper (0.59 by fact, 0.43 from a tier-5 pack: 0.16 is the
--     pack discount). An invoice bills charges minus allocations, so the
--     discount is never invoiced.
--
-- Every write of a lot or the journal happens in internal/platform/billing
-- under SELECT ... FOR UPDATE of the billing_accounts row (spec 42 rule 10).
-- Money is bigint MINOR units of the account currency, never a float.
--
-- The expiry of a bonus lot born from a cancelled session (B-6: "срок как
-- у офферов") has no number yet: such lots are written with expires_at
-- NULL until the owner names it (stop condition ST-2).
-- =====================================================================

-- ── credit_lots ──────────────────────────────────────────────────────────
CREATE TABLE credit_lots (
    id                      uuid        PRIMARY KEY DEFAULT uuidv7(),
    account_id              uuid        NOT NULL REFERENCES billing_accounts(id),
    org_id                  uuid        NOT NULL REFERENCES organizations(id),
    currency                char(3)     NOT NULL
                            CONSTRAINT credit_lots_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    kind                    text        NOT NULL
                            CONSTRAINT credit_lots_kind_check CHECK (kind IN ('purchased', 'bonus')),
    -- invoice: a paid credit_pack invoice; cancellation: the charges of a
    -- cancelled session (B-6); campaign: an offer (block B5); adjustment: an
    -- operator, with a reason in the journal.
    source                  text        NOT NULL
                            CONSTRAINT credit_lots_source_check CHECK (source IN (
                                'invoice', 'cancellation', 'campaign', 'adjustment')),
    -- The invoice of a purchase, the session_cancelled billable event of a
    -- cancellation bonus. With (account_id, source) the idempotency key.
    source_ref              uuid        NULL,
    -- The event (events.id) whose cancelled session gave the bonus: a bonus
    -- pays only for the NEXT events (B-6, B-19), never for this one.
    source_event_id         uuid        NULL,
    -- Purchased lots only: the pack tier and its rates at the purchase.
    tier                    smallint    NULL
                            CONSTRAINT credit_lots_tier_check CHECK (tier IS NULL OR tier > 0),
    unseated_rate_minor     bigint      NULL
                            CONSTRAINT credit_lots_unseated_check CHECK (unseated_rate_minor IS NULL OR unseated_rate_minor >= 0),
    seated_surcharge_minor  bigint      NULL
                            CONSTRAINT credit_lots_surcharge_check CHECK (seated_surcharge_minor IS NULL OR seated_surcharge_minor >= 0),
    price_list_id           uuid        NULL REFERENCES price_lists(id),
    pack_id                 uuid        NULL REFERENCES price_list_packs(id),
    -- When the lot becomes available (the payment of a pack, the grant of a
    -- bonus).
    purchased_at            timestamptz NOT NULL,
    face_minor              bigint      NOT NULL
                            CONSTRAINT credit_lots_face_check CHECK (face_minor > 0),
    remaining_minor         bigint      NOT NULL
                            CONSTRAINT credit_lots_remaining_check CHECK (remaining_minor >= 0),
    expires_at              timestamptz NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT credit_lots_remaining_le_face_check CHECK (remaining_minor <= face_minor),
    CONSTRAINT credit_lots_purchased_shape_check CHECK (
        kind <> 'purchased'
        OR (tier IS NOT NULL AND unseated_rate_minor IS NOT NULL
            AND seated_surcharge_minor IS NOT NULL AND expires_at IS NULL)),
    CONSTRAINT credit_lots_bonus_shape_check CHECK (
        kind <> 'bonus'
        OR (tier IS NULL AND unseated_rate_minor IS NULL AND seated_surcharge_minor IS NULL)),
    CONSTRAINT credit_lots_expiry_check CHECK (expires_at IS NULL OR expires_at > purchased_at)
);

CREATE UNIQUE INDEX credit_lots_source_uq ON credit_lots (account_id, source, source_ref)
    WHERE source_ref IS NOT NULL;
-- The consume order: bonus lots nearest to expiry first, purchased FIFO.
CREATE INDEX credit_lots_account_open_idx ON credit_lots (account_id, kind, expires_at, purchased_at, id)
    WHERE remaining_minor > 0;
CREATE INDEX credit_lots_expiry_idx ON credit_lots (expires_at)
    WHERE expires_at IS NOT NULL AND remaining_minor > 0;
CREATE INDEX credit_lots_org_idx ON credit_lots (org_id);

COMMENT ON TABLE credit_lots IS
    'Credit balance of a billing account in its currency (41 section 6.5). remaining_minor equals the signed sum of the lot''s credit_ledger entries. Written only under FOR UPDATE of billing_accounts.';

-- ── credit_ledger ────────────────────────────────────────────────────────
CREATE TABLE credit_ledger (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    seq                 bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    account_id          uuid        NOT NULL REFERENCES billing_accounts(id),
    lot_id              uuid        NOT NULL REFERENCES credit_lots(id),
    entry_type          text        NOT NULL
                        CONSTRAINT credit_ledger_type_check CHECK (entry_type IN (
                            'purchase', 'bonus_grant', 'consume', 'reversal', 'reversal_dropped',
                            'expire', 'hold', 'release', 'adjustment_credit', 'adjustment_debit')),
    -- Always positive; the sign follows entry_type (credit_ledger_delta in
    -- the queries): purchase, bonus_grant, reversal, release,
    -- adjustment_credit add; consume, expire, hold, adjustment_debit take;
    -- reversal_dropped moves nothing (a bonus that expired before its money
    -- came back, 41 section 6.5).
    amount_minor        bigint      NOT NULL
                        CONSTRAINT credit_ledger_amount_check CHECK (amount_minor > 0),
    currency            char(3)     NOT NULL
                        CONSTRAINT credit_ledger_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    remaining_after     bigint      NOT NULL
                        CONSTRAINT credit_ledger_remaining_after_check CHECK (remaining_after >= 0),
    charge_id           uuid        NULL REFERENCES charges(id),
    allocation_id       uuid        NULL REFERENCES charge_allocations(id),
    invoice_id          uuid        NULL REFERENCES invoices(id),
    cause_event_id      uuid        NULL REFERENCES billable_events(id),
    actor_type          text        NOT NULL DEFAULT 'system',
    actor_id            text        NOT NULL DEFAULT '',
    reason              text        NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT credit_ledger_adjustment_reason_check CHECK (
        entry_type NOT IN ('adjustment_credit', 'adjustment_debit')
        OR (reason IS NOT NULL AND btrim(reason) <> ''))
);

CREATE INDEX credit_ledger_lot_idx ON credit_ledger (lot_id, seq);
CREATE INDEX credit_ledger_account_idx ON credit_ledger (account_id, seq);
CREATE INDEX credit_ledger_charge_idx ON credit_ledger (charge_id) WHERE charge_id IS NOT NULL;

COMMENT ON TABLE credit_ledger IS
    'Append-only journal of credit lots (41 section 6.5). Never UPDATE or DELETE: a correction is an adjustment entry with a reason.';

-- ── charge_allocations: lot foreign key, pack discount ───────────────────
ALTER TABLE charge_allocations
    ADD CONSTRAINT charge_allocations_lot_fk FOREIGN KEY (lot_id) REFERENCES credit_lots(id);

ALTER TABLE charge_allocations DROP CONSTRAINT charge_allocations_kind_check;
ALTER TABLE charge_allocations ADD CONSTRAINT charge_allocations_kind_check CHECK (kind IN (
    'purchased_lot', 'bonus_lot', 'invoice_line', 'agreement_discount', 'pack_rate_discount'));

ALTER TABLE charge_allocations DROP CONSTRAINT charge_allocations_target_check;
ALTER TABLE charge_allocations ADD CONSTRAINT charge_allocations_target_check CHECK (
    (kind IN ('purchased_lot', 'bonus_lot') AND lot_id IS NOT NULL)
    OR (kind = 'invoice_line' AND invoice_line_id IS NOT NULL)
    OR (kind IN ('agreement_discount', 'pack_rate_discount') AND lot_id IS NULL));

CREATE INDEX charge_allocations_lot_idx ON charge_allocations (lot_id) WHERE lot_id IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS charge_allocations_lot_idx;
DELETE FROM charge_allocations WHERE kind = 'pack_rate_discount' OR lot_id IS NOT NULL;
ALTER TABLE charge_allocations DROP CONSTRAINT IF EXISTS charge_allocations_target_check;
ALTER TABLE charge_allocations ADD CONSTRAINT charge_allocations_target_check CHECK (
    (kind IN ('purchased_lot', 'bonus_lot') AND lot_id IS NOT NULL)
    OR (kind = 'invoice_line' AND invoice_line_id IS NOT NULL)
    OR kind = 'agreement_discount');
ALTER TABLE charge_allocations DROP CONSTRAINT IF EXISTS charge_allocations_kind_check;
ALTER TABLE charge_allocations ADD CONSTRAINT charge_allocations_kind_check CHECK (kind IN (
    'purchased_lot', 'bonus_lot', 'invoice_line', 'agreement_discount'));
ALTER TABLE charge_allocations DROP CONSTRAINT IF EXISTS charge_allocations_lot_fk;

DROP TABLE IF EXISTS credit_ledger;
DROP TABLE IF EXISTS credit_lots;
