-- getting_started.sql: the facts behind GET /v1/organizations/{org_id}/getting-started
-- (the four "first steps" of an organization). Read-only, one round trip.

-- name: GetGettingStartedFacts :one
-- payments_connected: an active, not deleted payment-provider config whose
-- credential the provider accepted (the bot's PaymentsConnected rule).
-- has_event: any not deleted event of the organization, a draft counts.
-- has_sales_link: any sales link of the organization, whatever its status.
SELECT EXISTS (
           SELECT 1 FROM payment_provider_configs
           WHERE  org_id = $1 AND deleted_at IS NULL AND is_active
             AND  verification_status = 'ok'
       ) AS payments_connected,
       EXISTS (
           SELECT 1 FROM events
           WHERE  org_id = $1 AND deleted_at IS NULL
       ) AS has_event,
       EXISTS (
           SELECT 1 FROM sales_links
           WHERE  org_id = $1
       ) AS has_sales_link;
