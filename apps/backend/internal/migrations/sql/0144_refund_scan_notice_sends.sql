-- 0144_refund_scan_notice_sends.sql — which owner addresses already received
-- one notice about a refund blocked by a scan (refund.scanned_alert_email).
--
-- The job retries when one address fails; without this table every retry
-- mailed again the owners who had already received the notice. One row per
-- notice (the job's notice_id) and address, written after the provider
-- accepted the letter.
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up
CREATE TABLE refund_scan_notice_sends (
    notice_id uuid        NOT NULL,
    email     text        NOT NULL,
    sent_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (notice_id, email)
);

COMMENT ON TABLE refund_scan_notice_sends IS
    'One row per notice about a scan-blocked refund and per address it reached, so a retried job mails only the addresses that failed.';

-- +goose Down
DROP TABLE IF EXISTS refund_scan_notice_sends;
