-- +goose Up
SET LOCAL lock_timeout = '5s';

-- =====================================================================
-- 0160 billing invoices and payments (billing wave B-B/B-C: tasks B3-3,
-- B4-1, B4-2; 41 sections 6.6, 6.11 and 8.1 B-14)
-- =====================================================================
-- * invoices: drop UNIQUE (org_id, billing_period) (an account bills per
--   currency, a pack purchase is its own invoice); add the billing account,
--   the issuer, the kind (usage / credit_pack / credit_note / subscription /
--   manual), the gapless number (issuer, year, seq: given at issue, never
--   changed; a draft has none), the payment reference a bank transfer must
--   carry, the payment method, the money parts (subtotal, card fee, tax,
--   total), what was paid and how, the Stripe ids and an idempotency key
--   (account, kind, basis, sequence).
-- * invoice_number_series: the gapless counter per issuer and year.
-- * invoice_lines: the kind of a line (usage, pack, card_fee, credit_note,
--   manual) and the rule it bills.
-- * stripe_customers: one Stripe customer per BILLING ACCOUNT (an account is
--   one currency), no longer per organization.
-- * billing_stripe_events: idempotency of the platform Stripe webhook by the
--   Stripe event id (41 section 6.6).
-- * billing_payment_fees: the average fee of a payment method, set by a
--   superadmin, versioned by effective_from. A card payment through Stripe
--   adds a "payment system fee" line computed from it so the platform nets
--   the invoice total (owner decision B-14: the payer bears the Stripe fee);
--   a bank transfer is a net payment and gets no such line. No number is
--   seeded (stop condition ST-2): until a superadmin enters one, a card
--   payment is refused.
-- * billing_bank_transactions: incoming bank payments from a bank webhook,
--   a bank API pull or an operator import, matched to an issued invoice by
--   its payment reference and exact amount; whatever does not match waits
--   for an operator.
--
-- Money is bigint MINOR units next to its currency, never a float.
-- =====================================================================

-- ── invoices ─────────────────────────────────────────────────────────────
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_org_id_billing_period_key;

ALTER TABLE invoices
    ADD COLUMN account_id                 uuid        NULL REFERENCES billing_accounts(id),
    ADD COLUMN issuer_id                  uuid        NULL REFERENCES billing_issuers(id),
    ADD COLUMN kind                       text        NOT NULL DEFAULT 'usage'
               CONSTRAINT invoices_kind_check CHECK (kind IN (
                   'usage', 'credit_pack', 'credit_note', 'subscription', 'manual')),
    ADD COLUMN number                     text        NULL,
    ADD COLUMN number_year                int         NULL,
    ADD COLUMN number_seq                 int         NULL,
    ADD COLUMN related_invoice_id         uuid        NULL REFERENCES invoices(id),
    ADD COLUMN idempotency_key            text        NULL,
    ADD COLUMN payment_method             text        NULL
               CONSTRAINT invoices_payment_method_check CHECK (payment_method IS NULL OR payment_method IN (
                   'bank_transfer', 'stripe_card')),
    ADD COLUMN payment_reference          text        NULL,
    ADD COLUMN period_from                timestamptz NULL,
    ADD COLUMN period_to                  timestamptz NULL,
    ADD COLUMN pack_id                    uuid        NULL REFERENCES price_list_packs(id),
    ADD COLUMN subtotal_minor             bigint      NOT NULL DEFAULT 0
               CONSTRAINT invoices_subtotal_check CHECK (subtotal_minor >= 0),
    ADD COLUMN card_fee_minor             bigint      NOT NULL DEFAULT 0
               CONSTRAINT invoices_card_fee_check CHECK (card_fee_minor >= 0),
    ADD COLUMN tax_status                 text        NULL,
    ADD COLUMN tax_minor                  bigint      NOT NULL DEFAULT 0
               CONSTRAINT invoices_tax_check CHECK (tax_minor >= 0),
    ADD COLUMN due_at                     timestamptz NULL,
    ADD COLUMN paid_amount_minor          bigint      NULL
               CONSTRAINT invoices_paid_amount_check CHECK (paid_amount_minor IS NULL OR paid_amount_minor >= 0),
    ADD COLUMN paid_via                   text        NULL
               CONSTRAINT invoices_paid_via_check CHECK (paid_via IS NULL OR paid_via IN (
                   'bank_transfer', 'stripe_card', 'stripe_invoice', 'manual', 'credits')),
    ADD COLUMN paid_reference             text        NULL,
    ADD COLUMN stripe_checkout_session_id text        NULL,
    ADD COLUMN stripe_payment_intent_id   text        NULL,
    ADD COLUMN hosted_payment_url         text        NULL,
    ADD COLUMN created_by                 text        NULL,
    ADD COLUMN void_reason                text        NULL;

ALTER TABLE invoices
    ADD CONSTRAINT invoices_number_shape_check CHECK (
        (number IS NULL AND number_year IS NULL AND number_seq IS NULL)
        OR (number IS NOT NULL AND number_year IS NOT NULL AND number_seq IS NOT NULL)),
    ADD CONSTRAINT invoices_draft_unnumbered_check CHECK (state <> 'draft' OR number IS NULL),
    ADD CONSTRAINT invoices_pack_shape_check CHECK (kind <> 'credit_pack' OR (pack_id IS NOT NULL AND account_id IS NOT NULL)),
    ADD CONSTRAINT invoices_note_shape_check CHECK (kind <> 'credit_note' OR related_invoice_id IS NOT NULL);

CREATE UNIQUE INDEX invoices_issuer_number_uq ON invoices (issuer_id, number_year, number_seq)
    WHERE number_seq IS NOT NULL;
CREATE UNIQUE INDEX invoices_idempotency_key_uq ON invoices (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX invoices_payment_reference_uq ON invoices (payment_reference)
    WHERE payment_reference IS NOT NULL;
CREATE UNIQUE INDEX invoices_stripe_checkout_session_uq ON invoices (stripe_checkout_session_id)
    WHERE stripe_checkout_session_id IS NOT NULL;
CREATE INDEX invoices_account_idx ON invoices (account_id, created_at DESC) WHERE account_id IS NOT NULL;
CREATE INDEX invoices_open_idx ON invoices (state, due_at) WHERE state = 'issued';

COMMENT ON COLUMN invoices.number IS
    'Our legal invoice number, gapless per issuer and year, given when the invoice is issued and never changed. A Stripe invoice carries it only as a reference (41 section 6.6).';
COMMENT ON COLUMN invoices.payment_reference IS
    'The code a bank transfer must carry; the bank intake matches incoming payments by it and the exact amount.';

-- ── billing_account_terms.fee_mode ───────────────────────────────────────
-- organizer_pays (default): the platform charges the price-list rate.
-- buyer_pays: the organizer wants the net amount and the BUYER pays the
-- platform's commission as the service charge; the charge of a sold ticket is
-- then the service charge the buyer actually paid for it (recorded on the
-- billable event), with the price-list rate kept next to it for comparison.
ALTER TABLE billing_account_terms
    ADD COLUMN fee_mode text NOT NULL DEFAULT 'organizer_pays'
        CONSTRAINT billing_account_terms_fee_mode_check CHECK (fee_mode IN ('organizer_pays', 'buyer_pays'));

-- ── invoice_number_series ────────────────────────────────────────────────
CREATE TABLE invoice_number_series (
    issuer_id   uuid        NOT NULL REFERENCES billing_issuers(id),
    year        int         NOT NULL CONSTRAINT invoice_number_series_year_check CHECK (year BETWEEN 2000 AND 9999),
    last_seq    int         NOT NULL DEFAULT 0 CONSTRAINT invoice_number_series_seq_check CHECK (last_seq >= 0),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (issuer_id, year)
);

COMMENT ON TABLE invoice_number_series IS
    'Gapless invoice numbering per issuer and year: the number is taken in the transaction that issues the invoice, so a rolled-back issue leaves no hole.';

-- ── invoice_lines ────────────────────────────────────────────────────────
ALTER TABLE invoice_lines
    ADD COLUMN kind      text NOT NULL DEFAULT 'usage'
               CONSTRAINT invoice_lines_kind_check CHECK (kind IN (
                   'usage', 'pack', 'card_fee', 'credit_note', 'manual')),
    ADD COLUMN rule_type text NULL;

-- ── stripe_customers: per billing account ────────────────────────────────
ALTER TABLE stripe_customers DROP CONSTRAINT IF EXISTS stripe_customers_org_id_key;
ALTER TABLE stripe_customers ADD COLUMN account_id uuid NULL REFERENCES billing_accounts(id);
CREATE UNIQUE INDEX stripe_customers_account_uq ON stripe_customers (account_id) WHERE account_id IS NOT NULL;
CREATE INDEX stripe_customers_org_idx ON stripe_customers (org_id);

-- ── billing_stripe_events ────────────────────────────────────────────────
CREATE TABLE billing_stripe_events (
    event_id      text        PRIMARY KEY,
    event_type    text        NOT NULL,
    invoice_id    uuid        NULL REFERENCES invoices(id),
    outcome       text        NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE billing_stripe_events IS
    'Platform Stripe webhook events already applied, by Stripe event id: written in the transaction that applies the event, so a redelivery is a no-op and a failure leaves nothing behind.';

-- ── billing_payment_fees ─────────────────────────────────────────────────
CREATE TABLE billing_payment_fees (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    method          text        NOT NULL
                    CONSTRAINT billing_payment_fees_method_check CHECK (method IN ('stripe_card')),
    currency        char(3)     NOT NULL
                    CONSTRAINT billing_payment_fees_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    pct_bp          int         NOT NULL
                    CONSTRAINT billing_payment_fees_pct_check CHECK (pct_bp >= 0 AND pct_bp < 10000),
    fixed_minor     bigint      NOT NULL
                    CONSTRAINT billing_payment_fees_fixed_check CHECK (fixed_minor >= 0 AND fixed_minor <= 1000000),
    effective_from  timestamptz NOT NULL,
    created_by      text        NULL,
    reason          text        NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT billing_payment_fees_series_uq UNIQUE (method, currency, effective_from)
);

COMMENT ON TABLE billing_payment_fees IS
    'Average fee of a payment method per currency, set by a superadmin; a card payment adds a fee line so the platform nets the invoice (owner decision B-14). No seed: card payment is refused until one exists.';

-- ── billing_bank_transactions ────────────────────────────────────────────
CREATE TABLE billing_bank_transactions (
    id                 uuid        PRIMARY KEY DEFAULT uuidv7(),
    -- Where it came from: a bank code (webhook or API pull) or 'manual' for
    -- an operator import. With external_id the idempotency key.
    provider           text        NOT NULL
                       CONSTRAINT billing_bank_transactions_provider_check CHECK (provider ~ '^[a-z][a-z0-9_]{0,31}$'),
    external_id        text        NOT NULL
                       CONSTRAINT billing_bank_transactions_external_check CHECK (length(external_id) BETWEEN 1 AND 200),
    booked_at          timestamptz NOT NULL,
    amount_minor       bigint      NOT NULL,
    currency           char(3)     NOT NULL
                       CONSTRAINT billing_bank_transactions_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    -- The remittance information as the bank gave it, cut to 500 characters.
    reference          text        NULL,
    -- The payer's account holder name, cut to 200 characters (a B2B payment;
    -- no account number is stored).
    counterparty_name  text        NULL,
    status             text        NOT NULL DEFAULT 'unmatched'
                       CONSTRAINT billing_bank_transactions_status_check CHECK (status IN (
                           'matched', 'unmatched', 'ignored')),
    unmatched_reason   text        NULL,
    invoice_id         uuid        NULL REFERENCES invoices(id),
    matched_at         timestamptz NULL,
    matched_by         text        NULL,
    received_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT billing_bank_transactions_provider_external_uq UNIQUE (provider, external_id),
    CONSTRAINT billing_bank_transactions_matched_check CHECK (
        status <> 'matched' OR (invoice_id IS NOT NULL AND matched_at IS NOT NULL))
);

CREATE INDEX billing_bank_transactions_status_idx ON billing_bank_transactions (status, booked_at DESC);
CREATE UNIQUE INDEX billing_bank_transactions_invoice_uq ON billing_bank_transactions (invoice_id)
    WHERE status = 'matched';

COMMENT ON TABLE billing_bank_transactions IS
    'Incoming bank payments (webhook, API pull or operator import), matched to an issued invoice by payment reference and exact amount; the rest waits for an operator.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP TABLE IF EXISTS billing_bank_transactions;
DROP TABLE IF EXISTS billing_payment_fees;
DROP TABLE IF EXISTS billing_stripe_events;

DROP INDEX IF EXISTS stripe_customers_org_idx;
DROP INDEX IF EXISTS stripe_customers_account_uq;
ALTER TABLE stripe_customers DROP COLUMN IF EXISTS account_id;
ALTER TABLE stripe_customers ADD CONSTRAINT stripe_customers_org_id_key UNIQUE (org_id);

ALTER TABLE invoice_lines DROP COLUMN IF EXISTS rule_type, DROP COLUMN IF EXISTS kind;
ALTER TABLE billing_account_terms DROP COLUMN IF EXISTS fee_mode;

DROP TABLE IF EXISTS invoice_number_series;

DROP INDEX IF EXISTS invoices_open_idx;
DROP INDEX IF EXISTS invoices_account_idx;
DROP INDEX IF EXISTS invoices_stripe_checkout_session_uq;
DROP INDEX IF EXISTS invoices_payment_reference_uq;
DROP INDEX IF EXISTS invoices_idempotency_key_uq;
DROP INDEX IF EXISTS invoices_issuer_number_uq;
ALTER TABLE invoices
    DROP CONSTRAINT IF EXISTS invoices_note_shape_check,
    DROP CONSTRAINT IF EXISTS invoices_pack_shape_check,
    DROP CONSTRAINT IF EXISTS invoices_draft_unnumbered_check,
    DROP CONSTRAINT IF EXISTS invoices_number_shape_check;
ALTER TABLE invoices
    DROP COLUMN IF EXISTS void_reason, DROP COLUMN IF EXISTS created_by,
    DROP COLUMN IF EXISTS hosted_payment_url, DROP COLUMN IF EXISTS stripe_payment_intent_id,
    DROP COLUMN IF EXISTS stripe_checkout_session_id, DROP COLUMN IF EXISTS paid_reference,
    DROP COLUMN IF EXISTS paid_via, DROP COLUMN IF EXISTS paid_amount_minor, DROP COLUMN IF EXISTS due_at,
    DROP COLUMN IF EXISTS tax_minor, DROP COLUMN IF EXISTS tax_status, DROP COLUMN IF EXISTS card_fee_minor,
    DROP COLUMN IF EXISTS subtotal_minor, DROP COLUMN IF EXISTS pack_id, DROP COLUMN IF EXISTS period_to,
    DROP COLUMN IF EXISTS period_from, DROP COLUMN IF EXISTS payment_reference,
    DROP COLUMN IF EXISTS payment_method, DROP COLUMN IF EXISTS idempotency_key,
    DROP COLUMN IF EXISTS related_invoice_id, DROP COLUMN IF EXISTS number_seq,
    DROP COLUMN IF EXISTS number_year, DROP COLUMN IF EXISTS number,
    DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS issuer_id, DROP COLUMN IF EXISTS account_id;
-- The old UNIQUE (org_id, billing_period) is NOT restored: rows written since
-- may already break it.
