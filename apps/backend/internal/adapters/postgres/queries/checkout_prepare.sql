-- checkout_prepare.sql — the early (prepared) widget order of spec 65: the
-- order, the hold and the embedded Stripe session are created when the order
-- sheet opens, without the buyer; the buyer is attached later
-- (POST /v1/public/checkout/{checkout_token}/buyer).

-- name: GetCheckoutFeedContext :one
-- GetCheckoutFeedContext names an active feed token of the checkout's sales
-- channel that publishes the order's event, with the order's event id. The
-- attach route reads the channel's buyer-field flags and the consent texts
-- through it, exactly as checkout/start does through the token in its path.
-- Every token of one channel carries the same flags; the oldest one is taken
-- so the answer is stable.
SELECT ft.token, o.event_id
FROM   checkout_sessions cs
JOIN   orders o              ON o.checkout_session_id = cs.id
JOIN   agent_feed_tokens ft  ON ft.sales_channel_id = cs.channel_id
JOIN   event_publications ep ON ep.feed_token_id = ft.id AND ep.event_id = o.event_id
WHERE  cs.id = $1
  AND  ft.is_active = true
ORDER  BY ft.created_at, ft.id
LIMIT  1;

-- name: AttachOrderBuyer :execrows
-- AttachOrderBuyer writes the buyer onto a prepared order that has none yet.
-- A name or phone not given keeps what the order holds; the customer is set
-- only when one was resolved.
UPDATE orders
SET    buyer_email = $2,
       buyer_name  = COALESCE($3, buyer_name),
       buyer_phone = COALESCE($4, buyer_phone),
       customer_id = COALESCE($5, customer_id),
       updated_at  = now()
WHERE  id = $1
  AND  buyer_email IS NULL;

-- name: SetCheckoutBuyerLocale :exec
-- SetCheckoutBuyerLocale records the language the buyer attached in.
UPDATE checkout_sessions
SET    buyer_locale = $2
WHERE  id = $1;

-- name: FillOrderBuyerEmail :execrows
-- FillOrderBuyerEmail gives the order of a paid checkout the e-mail address
-- the payment provider collected, when the order has none (a prepared order
-- whose attach never ran). An order that has an address keeps it.
UPDATE orders
SET    buyer_email = $2,
       updated_at  = now()
WHERE  checkout_session_id = $1
  AND  buyer_email IS NULL;
