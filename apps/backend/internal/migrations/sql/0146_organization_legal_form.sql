-- 0146_organization_legal_form.sql — the legal form of the seller (HP-4a,
-- plan docs/plans/hosted_page_legal_web_center_wave_plan_2026-10-13_ru.md).
--
-- The organizer application has asked for it since migration 0125
-- (answers.legal_form) but the organization had nowhere to keep it, so
-- approval dropped it. The owner now enters it in the Telegram bot under
-- "Organization data", next to the legal name, and the hosted page prints it
-- in the seller block when the channel shows that block.
--
-- Free text ("S.L.", "s.r.o.", "autónomo", "Ltd"), at most 80 characters as
-- in the application form. NULL = not given.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
-- organizations is read on every request: fail fast rather than queue behind
-- a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    ADD COLUMN legal_form text,
    ADD CONSTRAINT organizations_legal_form_check
        CHECK (legal_form IS NULL OR char_length(legal_form) BETWEEN 1 AND 80);

-- +goose Down
ALTER TABLE organizations
    DROP CONSTRAINT IF EXISTS organizations_legal_form_check,
    DROP COLUMN IF EXISTS legal_form;
