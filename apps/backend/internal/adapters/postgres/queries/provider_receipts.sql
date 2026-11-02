-- provider_receipts.sql — the payment provider's own receipt for the buyer
-- (spec 51 section 2, migration 0184): the organization's setting, the
-- archive of a paid payment's receipt, and the provider a ticket was paid
-- through (the letters name it).

-- name: GetOrgProviderReceiptSettings :one
-- The owner's choice and the platform operator's "always send" flag.
-- pgx.ErrNoRows when the organization does not exist or is deleted.
SELECT provider_receipt_enabled, provider_receipt_forced
FROM organizations
WHERE id = $1 AND deleted_at IS NULL;

-- name: SetOrgProviderReceiptEnabled :one
-- Sets the owner's choice; answers both flags as stored.
UPDATE organizations
SET provider_receipt_enabled = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING provider_receipt_enabled, provider_receipt_forced;

-- name: SetOrgProviderReceiptForced :one
-- Sets the platform operator's flag (platform superadmin only, checked by
-- the handler); answers both flags as stored.
UPDATE organizations
SET provider_receipt_forced = $2, updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING provider_receipt_enabled, provider_receipt_forced;

-- name: GetPaymentIntentReceipt :one
-- What the archive job needs of one payment intent.
SELECT id, org_id, provider, provider_payment_id, provider_charge_ref, state,
       provider_receipt_url, provider_receipt_number,
       provider_receipt_media_id, provider_receipt_archived_at
FROM payment_intents
WHERE id = $1;

-- name: SetPaymentIntentReceipt :exec
-- Stores what the provider answered. A NULL argument keeps the stored value;
-- a media id also stamps provider_receipt_archived_at.
UPDATE payment_intents
SET provider_receipt_url         = COALESCE($2, provider_receipt_url),
    provider_receipt_number      = COALESCE($3, provider_receipt_number),
    provider_receipt_media_id    = COALESCE($4, provider_receipt_media_id),
    provider_receipt_archived_at = CASE WHEN $4::uuid IS NOT NULL THEN now()
                                        ELSE provider_receipt_archived_at END,
    updated_at = now()
WHERE id = $1;

-- name: GetTicketPaymentProvider :one
-- The provider of the succeeded payment of the ticket's checkout ('' when
-- none: an invitation, a free order, an order paid on a selling site).
SELECT COALESCE((
    SELECT pi.provider
    FROM tickets t
    JOIN payment_intents pi ON pi.checkout_session_id = t.checkout_session_id
    WHERE t.id = $1 AND pi.state = 'succeeded'
    ORDER BY pi.created_at DESC
    LIMIT 1
), '');

-- name: GetOrderPaymentProvider :one
-- The provider of the succeeded payment of the order's checkout ('' when none).
SELECT COALESCE((
    SELECT pi.provider
    FROM orders o
    JOIN payment_intents pi ON pi.checkout_session_id = o.checkout_session_id
    WHERE o.id = $1 AND pi.state = 'succeeded'
    ORDER BY pi.created_at DESC
    LIMIT 1
), '');

-- name: GetPaymentReceiptMediaID :one
-- The live copy an earlier run of the archive job already saved for this
-- payment intent, so a retry reuses it instead of saving a second one.
-- pgx.ErrNoRows when there is none.
SELECT id
FROM media_objects
WHERE owner_type = 'payment_receipt' AND owner_id = $1 AND org_id = $2
  AND deleted_at IS NULL
ORDER BY created_at DESC
LIMIT 1;
