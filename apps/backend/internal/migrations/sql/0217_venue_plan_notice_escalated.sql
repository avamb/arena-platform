-- 0217_venue_plan_notice_escalated.sql — the bot tells the organizer that the
-- generator could not build a draft (spec 08_architecture/64 wave 2).
--
-- venue_plan_notices.kind gets the value 'escalated': the worker job
-- venue_plan.generate writes it when an intake goes to an administrator
-- (generator_failed, draft_invalid, too_many_questions, unsupported_format,
-- no_generator), so the organizer knows a person builds the plan now.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE venue_plan_notices DROP CONSTRAINT venue_plan_notices_kind_check;
ALTER TABLE venue_plan_notices ADD CONSTRAINT venue_plan_notices_kind_check
    CHECK (kind IN ('draft_ready', 'returned', 'rejected', 'approved', 'escalated'));

COMMENT ON TABLE venue_plan_notices IS
    'What the bot must tell the creator of an intake (draft_ready, returned, rejected, approved, escalated). POST /v1/bot/venue-plan-notices/claim sets claimed_at; a notice is told at most once.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- An escalated notice has no value in the old CHECK: drop those rows first.
DELETE FROM venue_plan_notices WHERE kind = 'escalated';

ALTER TABLE venue_plan_notices DROP CONSTRAINT venue_plan_notices_kind_check;
ALTER TABLE venue_plan_notices ADD CONSTRAINT venue_plan_notices_kind_check
    CHECK (kind IN ('draft_ready', 'returned', 'rejected', 'approved'));

COMMENT ON TABLE venue_plan_notices IS
    'What the bot must tell the creator of an intake (draft_ready, returned, rejected, approved). POST /v1/bot/venue-plan-notices/claim sets claimed_at; a notice is told at most once.';
