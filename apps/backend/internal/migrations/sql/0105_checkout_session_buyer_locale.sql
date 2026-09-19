-- +goose Up
-- =====================================================================
-- arena_new — remember which language the buyer actually used
--
-- delivery.Payload.Locale is a real field the e-mail/PDF renderer honours,
-- but nothing has ever set it: every ticket and invitation e-mail arena has
-- ever sent rendered in English, no matter which language the buyer chose in
-- the widget. The widget tracked en/ru/cs/he purely client-side for its own
-- UI strings and never told the backend.
--
-- The locale is a property of the PURCHASE, not of the customer: the same
-- person may buy in Czech on one site and in English on another, and a
-- customer row is shared across organizations. It therefore lives on the
-- checkout session, which is also the row ticket issuance already reads.
--
-- NULL means "not stated" — tickets sold through the Bil24 gateway carry no
-- buyer locale and keep falling back to templates.DefaultLocale. The value is
-- validated against templates.SupportedLocales before it is stored, so an
-- unknown or hostile string never reaches the renderer.
-- =====================================================================

ALTER TABLE checkout_sessions
    ADD COLUMN buyer_locale text;

COMMENT ON COLUMN checkout_sessions.buyer_locale IS
    'Language the buyer used at checkout, validated against the shipped template locales. NULL means not stated - delivery falls back to English.';

-- +goose Down
ALTER TABLE checkout_sessions
    DROP COLUMN IF EXISTS buyer_locale;
