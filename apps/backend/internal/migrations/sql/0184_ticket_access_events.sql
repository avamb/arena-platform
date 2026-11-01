-- 0184_ticket_access_events.sql — every time a buyer downloads or views their
-- tickets (spec 51, decision R-13).
--
-- A chargeback answer needs proof that the buyer actually received the
-- tickets: Stripe's dispute evidence carries access_activity_log (who fetched
-- the product, from which IP, when) and customer_purchase_ip. The e-mail's
-- PDF attachment is invisible to arena and to Brevo (which also never reports
-- the reader's IP), so the public ticket routes write a row here instead:
--
--   * action 'pdf_download' — GET /v1/public/checkout/{token}/tickets/{id}/pdf,
--     one row per request;
--   * action 'tickets_view' — the order-status answer that listed the paid
--     tickets (thank-you page, widget); ticket_id is NULL because the whole
--     order was shown, and the writer skips a repeat from the same IP within
--     ten minutes so the widget's polling does not multiply rows.
--
-- source says where the request came from: 'email' when the link in the
-- ticket e-mail was followed (?src=email), otherwise 'web'.
--
-- The IP is stored in the clear on purpose (decision R-10, 2026-11-01): the
-- dispute evidence must name the real address, and payment providers keep it
-- too. Retention follows the other dispute evidence (counted from the event
-- date, decision 2026-10-14); the purge job is a follow-up.
--
-- Rows go with their checkout and ticket (ON DELETE CASCADE), so a fixture
-- that deletes a checkout never trips over this table.

-- +goose Up

-- The foreign keys lock tickets and checkout_sessions; a deploy that meets a
-- long transaction fails fast instead of queueing every sale behind it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS ticket_access_events (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    checkout_session_id uuid        NOT NULL REFERENCES checkout_sessions(id) ON DELETE CASCADE,
    ticket_id           uuid        REFERENCES tickets(id) ON DELETE CASCADE,
    org_id              uuid        NOT NULL,
    source              text        NOT NULL,
    action              text        NOT NULL,
    ip                  inet,
    user_agent          text,
    occurred_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ticket_access_events_source_check
        CHECK (source IN ('web', 'email')),
    CONSTRAINT ticket_access_events_action_check
        CHECK (action IN ('pdf_download', 'tickets_view')),
    CONSTRAINT ticket_access_events_user_agent_len
        CHECK (user_agent IS NULL OR char_length(user_agent) <= 512)
);

CREATE INDEX IF NOT EXISTS ticket_access_events_checkout_idx
    ON ticket_access_events (checkout_session_id, occurred_at);

CREATE INDEX IF NOT EXISTS ticket_access_events_ticket_idx
    ON ticket_access_events (ticket_id)
    WHERE ticket_id IS NOT NULL;

-- +goose Down

DROP TABLE IF EXISTS ticket_access_events;
