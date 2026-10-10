-- 0136_ticket_holder_name.sql — a ticket may carry the NAME of the person it
-- was issued to (Telegram event-center bot, spec 08_architecture/35 §6.3,
-- EC-12). An invitation issued from the bot is "Name, e-mail": the e-mail was
-- always stored (tickets.holder_email), the name had nowhere to live, so the
-- printed invitation could never say who it was for. A paid ticket's holder
-- name still comes from its order (orders.buyer_name) at render time; this
-- column is the name typed by the operator on an invitation, and when it is
-- set it wins over the order's.
--
-- Nullable, 1-200 characters when present. No permission is seeded, so the
-- superadmin parity guard has nothing to check.

-- +goose Up

ALTER TABLE tickets
    ADD COLUMN holder_name text NULL;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_holder_name_len_check
    CHECK (holder_name IS NULL OR (char_length(btrim(holder_name)) BETWEEN 1 AND 200));

COMMENT ON COLUMN tickets.holder_name IS
    'Name of the person the ticket was issued to, typed by the operator on a '
    'complimentary invitation (migration 0136). NULL for a paid ticket, whose '
    'holder name is resolved from the order at render time.';

-- +goose Down

ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_holder_name_len_check;
ALTER TABLE tickets DROP COLUMN IF EXISTS holder_name;
