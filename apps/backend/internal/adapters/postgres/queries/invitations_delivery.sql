-- name: GetInvitationDeliveryByCheckoutSession :one
-- The delivery facts of the order a checkout session produced (spec 46,
-- section 3.4): its source, the guest's address, the delivery mode stored by
-- the invitation issue and the issuer's e-mail.
SELECT o.id, o.source,
       COALESCE(btrim(o.buyer_email), ''),
       COALESCE(o.metadata->'invitation'->>'delivery', ''),
       COALESCE(btrim(u.email), '')
FROM   orders o
LEFT   JOIN users u ON u.id = o.issued_by
WHERE  o.checkout_session_id = $1;
