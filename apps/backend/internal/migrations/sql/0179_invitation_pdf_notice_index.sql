-- 0179_invitation_pdf_notice_index.sql — an index for the bot's PDF notice poll.
--
-- ClaimInvitationPDFNotices (INV-09) runs every few seconds and picks the
-- staff invitations issued in the last day, oldest first. Its filter is
-- issued_via IS NOT NULL AND issued_by IS NOT NULL AND created_at > now() -
-- 1 day, ordered by (created_at, id). The only index that fits
-- orders_session_issued_via_idx starts with session_id, so every poll walked
-- the whole orders table. This partial index holds invitation orders only
-- (a tiny fraction of orders) and is already in the poll's order, so the poll
-- reads the last day's tail and stops at its LIMIT.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS orders_invitation_recent_idx
    ON orders (created_at, id)
    WHERE issued_via IS NOT NULL AND issued_by IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS orders_invitation_recent_idx;
