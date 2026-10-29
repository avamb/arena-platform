-- Staff invitations with seats on the plan (spec 46, task INV-02): the
-- issue transaction, the per-session list and the cancellation. Every
-- statement here runs on the caller's transaction where it writes.

-- name: GetInvitationSessionTarget :one
-- The session an invitation is issued for, with its organization (through
-- the event) and the two facts the issue transaction gates on: the status
-- (cancelled refuses) and the soft delete (deleted reads as missing).
SELECT s.id, s.event_id, e.org_id, s.status, (s.deleted_at IS NOT NULL OR e.deleted_at IS NOT NULL) AS deleted
FROM   sessions s
JOIN   events e ON e.id = s.event_id
WHERE  s.id = $1;

-- name: LockInvitationGAUnits :many
-- Up to $3 places of one General Admission category for a staff invitation:
-- free places ('available') first, then the organizer's reserve
-- ('unavailable'). SKIP LOCKED like AllocateGAUnitsForHold, so a public
-- on-sale burst is never waited on; a short answer means the category has
-- no more places the invitation may take. The rows come back with their
-- CURRENT status, which is the order line's seat_origin.
SELECT id, session_id, seat_key, sector_name, row_name, seat_number,
       tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind
FROM   session_seats
WHERE  session_id = $1
  AND  kind       = 'ga_unit'
  AND  tier_id    = $2::uuid
  AND  status IN ('available', 'unavailable')
ORDER  BY (status = 'unavailable'), seat_key
LIMIT  $3
FOR UPDATE SKIP LOCKED;

-- name: HoldSessionSeatForInvitation :one
-- The invitation twin of HoldSessionSeat: 'available' OR 'unavailable' ->
-- 'held'. Only the issue transaction calls it, and it sells the seat a few
-- statements later in the SAME transaction, so a seat of the reserve is
-- never visible as anything but unavailable or sold (spec 46 section 2).
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id     = $1
  AND  status IN ('available', 'unavailable')
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind;

-- name: SetOrderStaffIssuance :exec
UPDATE orders
SET    issued_via = $2,
       issued_by  = $3,
       updated_at = now()
WHERE  id = $1;

-- name: SetInvitationCancelledBy :exec
-- Records who cancelled a staff invitation in orders.metadata.invitation
-- (spec 46 section 3.7). $2 is {"user_id": ...} or {"api_key": name}; the
-- sales bot reads it. The last cancellation wins.
UPDATE orders
SET    metadata   = COALESCE(metadata, '{}'::jsonb)
                    || jsonb_build_object('invitation',
                         COALESCE(metadata->'invitation', '{}'::jsonb)
                         || jsonb_build_object('cancelled_by', $2::jsonb)),
       updated_at = now()
WHERE  id = $1
  AND  issued_via IS NOT NULL;

-- name: SetInvitationOrderCustomer :exec
-- The guest's customer row, linked only once the order is paid: a pending
-- link would collide with orders_one_pending_per_customer_session_uq when
-- the guest has an open purchase on the same session.
UPDATE orders
SET    customer_id = $2,
       updated_at  = now()
WHERE  id = $1
  AND  status = 'paid'
  AND  issued_via IS NOT NULL;

-- name: SetOrderItemsSeatOrigin :exec
-- Where each line's seat came from: available (sale) or unavailable
-- (reserve). A cancelled ticket returns its seat there (INV-01).
UPDATE order_items oi
SET    seat_origin = v.origin
FROM   unnest($2::uuid[], $3::text[]) AS v(seat_id, origin)
WHERE  oi.order_id = $1
  AND  oi.session_seat_id = v.seat_id;

-- name: GetInvitationOrderByRequest :one
-- The staff invitation an earlier request with the same request_id wrote
-- (orders.external_ref = 'inv:' || request_id on the internal channel,
-- unique per channel and so per organization).
SELECT id, session_id, metadata->'invitation'->>'fingerprint' AS fingerprint
FROM   orders
WHERE  org_id = $1
  AND  external_ref = $2
  AND  issued_via IS NOT NULL;

-- name: GetInvitationOrder :one
-- One staff invitation of the organization; any other order is not found.
SELECT o.id, o.system_id, o.session_id, o.checkout_session_id, o.status,
       o.buyer_name, o.metadata, o.issued_via, o.issued_by, o.created_at
FROM   orders o
WHERE  o.id = $1
  AND  o.org_id = $2
  AND  o.source = 'complimentary'
  AND  o.issued_via IS NOT NULL;

-- name: ListSessionInvitations :many
-- Every staff invitation of the session, whoever issued it (spec 46 R-5),
-- newest first, with the issuer's e-mail and name and the ticket counts.
SELECT o.id, o.system_id, o.status, o.buyer_name, o.metadata, o.issued_via,
       o.issued_by, u.email, u.first_name, u.last_name, o.created_at,
       (SELECT count(*) FROM tickets t WHERE t.checkout_session_id = o.checkout_session_id) AS tickets_total,
       (SELECT count(*) FROM tickets t WHERE t.checkout_session_id = o.checkout_session_id AND t.status = 'active') AS tickets_active
FROM   orders o
LEFT JOIN users u ON u.id = o.issued_by
WHERE  o.org_id = $1
  AND  o.session_id = $2
  AND  o.source = 'complimentary'
  AND  o.issued_via IS NOT NULL
ORDER  BY o.created_at DESC, o.id DESC
LIMIT  $3;

-- name: ListInvitationOrderLines :many
-- The places of the given invitation orders, in line order.
SELECT oi.order_id, oi.ordinal, oi.ticket_id, oi.unit_price, oi.seat_origin,
       ss.seat_key, ss.sector_name, ss.row_name, ss.seat_number, ss.kind,
       tt.id AS tier_id, tt.name AS tier_name
FROM   order_items oi
JOIN   ticket_tiers tt ON tt.id = oi.tier_id
LEFT JOIN session_seats ss ON ss.id = oi.session_seat_id
WHERE  oi.order_id = ANY($1::uuid[])
ORDER  BY oi.order_id, oi.ordinal;
