-- 0210_ticket_reissues.sql — spec 63, stage R-2: the journal of ticket reissues.
--
-- A reissue replaces every active ticket of a paid order: the old ticket
-- becomes 'transferred' (cancellation_reason 'reissued') with its barcodes
-- and credentials revoked, and a new ticket takes the same seat in the same
-- order. One ticket_reissues row per operation, one ticket_reissue_items row
-- per replaced ticket.
--
-- The rows are evidence of where the old barcode went (spec 63 section 4.9),
-- so they keep both addresses and are never deleted outside this file's Down
-- section (tests/staticanalysis guards it). Only the letter-state columns
-- (and, on an item, the old-code attempt and notice columns) change after
-- the insert.
--
-- state: 'pending' until the relay accepts the letter with the new tickets,
-- then 'applied' (the order's buyer e-mail moves to the new address in the
-- same statement, the order keeps its customer), or 'send_failed' while
-- the letter has not gone out. At most one operation per order is open ('pending' or
-- 'send_failed') at a time.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE ticket_reissues (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id                uuid        NOT NULL REFERENCES organizations(id),
    -- The subject: an order, or an invitation without an order (stage
    -- R-5, issued before invitations became orders). Exactly one is set.
    order_id              uuid        REFERENCES orders(id),
    complimentary_issuance_id uuid    REFERENCES complimentary_issuances(id),
    old_email             text,
    new_email             text        NOT NULL CHECK (length(new_email) BETWEEN 3 AND 254),
    state                 text        NOT NULL DEFAULT 'pending'
                                      CHECK (state IN ('pending', 'applied', 'send_failed')),
    applied_at            timestamptz,
    letter_failed_at      timestamptz,
    reason                text        NOT NULL
                                      CHECK (reason IN ('sent_to_wrong_address', 'forwarded_to_stranger', 'other')),
    note                  text        CHECK (note IS NULL OR length(note) <= 500),
    old_code_scanned_at   timestamptz,
    actor_type            text        NOT NULL,
    actor_id              text,
    via                   text,
    idempotency_key       text        NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    request_fingerprint   text        NOT NULL,
    -- provider_email_state: what happened to the buyer's address at the
    -- payment provider (spec 63 section 4.10, decision R-17). NULL until the
    -- order's address changed.
    provider_email_state  text        CHECK (provider_email_state IS NULL OR provider_email_state IN
                                      ('not_applicable', 'queued', 'updated', 'failed', 'manual')),
    provider_email_error  text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ticket_reissues_org_key_uq UNIQUE (org_id, idempotency_key),
    CONSTRAINT ticket_reissues_applied_check CHECK ((state = 'applied') = (applied_at IS NOT NULL)),
    CONSTRAINT ticket_reissues_one_subject_check CHECK (num_nonnulls(order_id, complimentary_issuance_id) = 1)
);

CREATE INDEX ticket_reissues_order_idx ON ticket_reissues (order_id, created_at);
CREATE UNIQUE INDEX ticket_reissues_one_open_per_order_uq
    ON ticket_reissues (order_id) WHERE state IN ('pending', 'send_failed');
CREATE INDEX ticket_reissues_invitation_idx
    ON ticket_reissues (complimentary_issuance_id, created_at) WHERE complimentary_issuance_id IS NOT NULL;
CREATE UNIQUE INDEX ticket_reissues_one_open_per_invitation_uq
    ON ticket_reissues (complimentary_issuance_id)
    WHERE complimentary_issuance_id IS NOT NULL AND state IN ('pending', 'send_failed');

CREATE TABLE ticket_reissue_items (
    reissue_id     uuid    NOT NULL REFERENCES ticket_reissues(id),
    old_ticket_id  uuid    NOT NULL REFERENCES tickets(id),
    new_ticket_id  uuid    NOT NULL REFERENCES tickets(id),
    old_barcode    text,
    new_barcode    text    NOT NULL,
    old_used_at    timestamptz,
    -- The first try to enter with the old code AFTER the reissue (spec 63
    -- section 4.8, decision of 2026-11-08): when, which scanner, what it
    -- answered. Written once by the arena scanner or the MACS scan intake.
    old_code_attempt_at     timestamptz,
    old_code_attempt_source text CHECK (old_code_attempt_source IS NULL
                                        OR old_code_attempt_source IN ('arena_scanner', 'macs')),
    old_code_attempt_result text CHECK (old_code_attempt_result IS NULL
                                        OR old_code_attempt_result IN ('admitted', 'denied')),
    old_code_attempt_gate   text,
    -- When the organizer was told (order card, Telegram of the organization,
    -- e-mail to the owners without a chat): an entry before the reissue, and
    -- the first try after it. Each is told once.
    entry_notified_at       timestamptz,
    attempt_notified_at     timestamptz,
    PRIMARY KEY (reissue_id, old_ticket_id),
    CONSTRAINT ticket_reissue_items_attempt_check CHECK (
        (old_code_attempt_at IS NULL) = (old_code_attempt_source IS NULL)),
    CONSTRAINT ticket_reissue_items_old_uq UNIQUE (old_ticket_id),
    CONSTRAINT ticket_reissue_items_new_uq UNIQUE (new_ticket_id)
);

CREATE INDEX ticket_reissue_items_entry_untold_idx
    ON ticket_reissue_items (reissue_id) WHERE old_used_at IS NOT NULL AND entry_notified_at IS NULL;
CREATE INDEX ticket_reissue_items_attempt_untold_idx
    ON ticket_reissue_items (reissue_id) WHERE old_code_attempt_at IS NOT NULL AND attempt_notified_at IS NULL;

COMMENT ON TABLE ticket_reissues IS
    'Spec 63: one row per reissue of an order''s tickets. Evidence, never deleted.';
COMMENT ON TABLE ticket_reissue_items IS
    'Spec 63: one row per ticket a reissue replaced (old ticket -> new ticket).';

-- +goose Down
DROP TABLE IF EXISTS ticket_reissue_items;
DROP TABLE IF EXISTS ticket_reissues;
