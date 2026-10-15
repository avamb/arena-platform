-- +goose Up
-- =====================================================================
-- arena_new — buyer consent at checkout (line P, task P2)
--
-- A sales channel may ask the buyer to accept the organizer's sale terms
-- and the platform policy before paying
-- (sales_channels.settings.hosted_page.consent_required, switched on only
-- through PATCH .../channels/{id}/seller-display and only while the seller
-- profile is complete). POST /v1/public/feeds/{token}/checkout/start then
-- refuses a cart without the tick (422 checkout.consent_required) and,
-- when the buyer ticked it, writes one row here in the SAME transaction
-- that creates the checkout session.
--
-- The row is evidence for a payment dispute (spec
-- 08_architecture/38_chargeback_evidence_ru.md, sections 5.1, 6 and 7):
--
--   * terms_version — what the buyer accepted. Until the organizer's
--     rendered terms exist (task P3, legal_renderings) it is the platform
--     terms version plus the seller organization id, e.g.
--     'platform-terms:2026-10-15;seller:<org uuid>'.
--   * locale         — the language the buyer read it in.
--   * accepted_at    — when.
--   * client_ip      — the buyer's REAL address at purchase (owner decision
--                      2026-10-15: Stripe's customer_purchase_ip needs the
--                      address itself, not a hash). The TCP peer as rewritten
--                      by the router's trusted-proxy handling.
--   * user_agent     — the browser string, at most 512 characters.
--   * withdrawal_notice_shown — whether this purchase showed the "no right
--     of withdrawal" notice (internal/platform/withdrawal for the seller
--     country AT THAT MOMENT), so the evidence reads the purchase, not
--     today's settings.
--
-- Access: only the order's organization (GET .../orders/{id}/consents,
-- order.read) and the platform operator. The IP address and the user agent
-- never go into logs, public answers, audit metadata or Telegram messages.
--
-- Retention: at least 24 and at most 36 months counted from the EVENT
-- date, like the other dispute evidence (spec 38 section 5.1 item 5; the
-- anchor is max(accepted_at, end of the session the checkout is for), the
-- same rule as emailstatus.RetentionAnchorSQL). There is deliberately NO
-- purge yet: the first rows reach 36 months in 2029-10 at the earliest. A
-- future purge must use that anchor, never accepted_at alone.
-- =====================================================================

SET LOCAL lock_timeout = '5s';

CREATE TABLE checkout_consents (
    id                      uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                  uuid        NOT NULL REFERENCES organizations(id),
    checkout_session_id     uuid        NOT NULL REFERENCES checkout_sessions(id),
    kind                    text        NOT NULL,
    terms_version           text        NOT NULL,
    locale                  text,
    accepted_at             timestamptz NOT NULL,
    client_ip               inet,
    user_agent              text,
    withdrawal_notice_shown boolean     NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT checkout_consents_kind_check
        CHECK (kind IN ('sale_terms')),
    CONSTRAINT checkout_consents_terms_version_length
        CHECK (char_length(terms_version) BETWEEN 1 AND 200),
    CONSTRAINT checkout_consents_locale_length
        CHECK (locale IS NULL OR char_length(locale) BETWEEN 1 AND 16),
    CONSTRAINT checkout_consents_user_agent_length
        CHECK (user_agent IS NULL OR char_length(user_agent) <= 512),
    CONSTRAINT checkout_consents_session_kind_unique
        UNIQUE (checkout_session_id, kind)
);

CREATE INDEX checkout_consents_org_session
    ON checkout_consents (org_id, checkout_session_id);

COMMENT ON TABLE checkout_consents IS
    'Buyer consent given at checkout (P2). Dispute evidence: kept at least 24 and at most 36 months from the event date; no purge yet. client_ip and user_agent are personal data: organization and operator only, never logged.';
COMMENT ON COLUMN checkout_consents.terms_version IS
    'What the buyer accepted: platform-terms:<version>;seller:<org uuid> until legal_renderings (P3) exist.';
COMMENT ON COLUMN checkout_consents.client_ip IS
    'The buyer''s real IP address at purchase (owner decision 2026-10-15), not a hash.';
COMMENT ON COLUMN checkout_consents.withdrawal_notice_shown IS
    'Whether the purchase showed the no-right-of-withdrawal notice for the seller country at that moment.';

-- +goose Down
DROP TABLE IF EXISTS checkout_consents;
