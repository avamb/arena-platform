-- =====================================================================
-- arena_new — the reader's browser string on every e-mail open and click
-- (task U-7, owner decision 2026-10-15, spec
-- 08_architecture/38_chargeback_evidence_ru.md sections 6 and 7)
--
-- email_letter_interactions (migration 0147) logs every open, proxy open
-- and click Brevo reports. This migration adds the one reader-side field
-- Brevo's transactional webhook carries for those events:
--
--   * user_agent — the "user_agent" field of the event (the reader's mail
--                  client or browser string, at most 512 characters).
--
-- Brevo does NOT send the reader's IP address. The documented event
-- payloads (developers.brevo.com/docs/transactional-webhooks, checked
-- 2026-10-16) have `sending_ip` (the address arena's mail was sent FROM,
-- the relay's, not the reader's) and no field for the address that loaded
-- the image or followed the link. So there is deliberately NO client_ip
-- column here: a column that could never be filled would only look like
-- evidence. The reader's address needs a pixel or link redirect arena
-- serves itself (an owner decision, see the runbook section 6).
--
-- The user agent is evidence for a payment dispute, personal data. It is
-- read only through GET /v1/organizations/{org_id}/orders/{order_id}/
-- letter-interactions (order.read, the order's organization and the
-- platform operator; never an API key). It never goes into logs, public
-- answers, audit metadata or Telegram messages, and email_letter_events
-- keeps its minimal payload (event name and ts_epoch) unchanged.
--
-- Retention: that of the letters (emailstatus.MinRetention), at least 24
-- and at most 36 months counted from the EVENT date
-- (emailstatus.RetentionAnchorSQL). There is deliberately NO purge.
-- =====================================================================

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE email_letter_interactions
    ADD COLUMN user_agent text,
    ADD CONSTRAINT email_letter_interactions_user_agent_length
        CHECK (user_agent IS NULL OR char_length(user_agent) <= 512);

COMMENT ON COLUMN email_letter_interactions.user_agent IS
    'The reader''s mail client or browser string as Brevo reported it (at most 512 characters). Personal data and dispute evidence: organization and operator only, never logged. Kept with the letter, 24 to 36 months from the event date.';

-- +goose Down
ALTER TABLE email_letter_interactions
    DROP CONSTRAINT IF EXISTS email_letter_interactions_user_agent_length,
    DROP COLUMN IF EXISTS user_agent;
