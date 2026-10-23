-- Buyer consent at checkout (migration 0155, task P2). Hand-maintained
-- mirror of gen/checkout_consents.sql.go.

-- name: InsertCheckoutConsent :one
-- InsertCheckoutConsent writes one consent row on the checkout transaction
-- that creates the checkout session.
INSERT INTO checkout_consents (
    org_id, checkout_session_id, kind, terms_version, locale,
    accepted_at, client_ip, user_agent, withdrawal_notice_shown,
    origin_host, seller_legal_name, terms_url, privacy_url,
    legal_rendering_id, terms_sha256
) VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING id, org_id, checkout_session_id, kind, terms_version, locale,
          accepted_at, host(client_ip), user_agent, withdrawal_notice_shown,
          origin_host, seller_legal_name, terms_url, privacy_url, created_at,
          legal_rendering_id, terms_sha256;

-- name: ListCheckoutConsentsBySession :many
-- ListCheckoutConsentsBySession lists the consents of one checkout session of
-- the organization, oldest first.
SELECT id, org_id, checkout_session_id, kind, terms_version, locale,
       accepted_at, host(client_ip), user_agent, withdrawal_notice_shown,
       origin_host, seller_legal_name, terms_url, privacy_url, created_at,
       legal_rendering_id, terms_sha256
FROM checkout_consents
WHERE org_id = $1
  AND checkout_session_id = $2
ORDER BY accepted_at, id;
