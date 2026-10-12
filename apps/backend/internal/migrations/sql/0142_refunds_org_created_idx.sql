-- 0142_refunds_org_created_idx.sql — the organizer's refund queue
-- (GET /v1/organizations/{org_id}/refunds) pages one organization's refunds
-- newest first. Without this index the page and its count scan every
-- organization's refunds.

-- +goose Up
CREATE INDEX IF NOT EXISTS refunds_org_created_idx ON refunds (org_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS refunds_org_created_idx;
