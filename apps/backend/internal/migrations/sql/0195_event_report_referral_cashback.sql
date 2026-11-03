-- +goose Up
-- 0195_event_report_referral_cashback.sql — the post-event report (feature
-- #159) gets its own line for the "Refer a friend" cashback (spec 52): money
-- the organizer returned to referrers out of their own payments. It is not a
-- refund to a buyer, so the `refunds` line keeps leaving it out and this line
-- carries it.
ALTER TABLE event_report_lines DROP CONSTRAINT event_report_lines_category_check;
ALTER TABLE event_report_lines ADD CONSTRAINT event_report_lines_category_check CHECK (category IN (
    'sales',
    'refunds',
    'referral_cashback',
    'complimentary',
    'scans',
    'commissions',
    'payouts'
));

-- +goose Down
DELETE FROM event_report_lines WHERE category = 'referral_cashback';
ALTER TABLE event_report_lines DROP CONSTRAINT event_report_lines_category_check;
ALTER TABLE event_report_lines ADD CONSTRAINT event_report_lines_category_check CHECK (category IN (
    'sales',
    'refunds',
    'complimentary',
    'scans',
    'commissions',
    'payouts'
));
