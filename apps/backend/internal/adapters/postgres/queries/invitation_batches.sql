-- Bulk staff invitations from a CSV file (spec 46, task INV-06). The batch
-- row and every order of the file are written by ONE transaction.

-- name: ListInvitationPlanSeats :many
-- Every assigned seat of the session (General Admission places excluded:
-- they are addressed by category) with the names a CSV file uses and the
-- status the dry run and the pre-check read. Not locked: the issue
-- transaction locks and re-checks.
SELECT id, seat_key, sector_name, row_name, seat_number, tier_id, status
FROM   session_seats
WHERE  session_id = $1
  AND  kind <> 'ga_unit';

-- name: ListInvitationGACounts :many
-- Per General Admission category of the session, how many places an
-- invitation may take: free ones and the organizer's reserve.
SELECT tier_id,
       count(*) FILTER (WHERE status = 'available')   AS available,
       count(*) FILTER (WHERE status = 'unavailable') AS reserve
FROM   session_seats
WHERE  session_id = $1
  AND  kind = 'ga_unit'
  AND  tier_id IS NOT NULL
GROUP  BY tier_id;

-- name: InsertInvitationBatch :one
INSERT INTO invitation_batches (org_id, session_id, created_by, file_sha256, request_id, rows, seats)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, org_id, session_id, created_by, file_sha256, request_id, rows, seats,
          from_reserve, status, error_report, created_at;

-- name: GetInvitationBatchByRequest :one
SELECT id, org_id, session_id, created_by, file_sha256, request_id, rows, seats,
       from_reserve, status, error_report, created_at
FROM   invitation_batches
WHERE  org_id = $1
  AND  request_id = $2;

-- name: GetInvitationBatch :one
SELECT id, org_id, session_id, created_by, file_sha256, request_id, rows, seats,
       from_reserve, status, error_report, created_at
FROM   invitation_batches
WHERE  id = $1
  AND  org_id = $2;

-- name: SetInvitationBatchFromReserve :exec
UPDATE invitation_batches SET from_reserve = $2 WHERE id = $1;

-- name: SetOrderInvitationBatch :exec
-- Stamps the bulk issuance: staff_bulk, the issuer and the batch.
UPDATE orders
SET    issued_via          = $2,
       issued_by           = $3,
       invitation_batch_id = $4,
       updated_at          = now()
WHERE  id = $1;

-- name: ListInvitationBatchOrders :many
-- The orders of a batch in file order (the guest's position in the file is
-- kept in orders.metadata.invitation.batch_seq).
SELECT o.id, o.system_id, o.status, o.buyer_name, o.buyer_email, o.metadata
FROM   orders o
WHERE  o.invitation_batch_id = $1
  AND  o.org_id = $2
ORDER  BY COALESCE((o.metadata->'invitation'->>'batch_seq')::int, 0), o.created_at, o.id;

-- name: HoldSessionSeatsForInvitation :many
-- The set-based twin of HoldSessionSeatForInvitation: every named seat of
-- one invitation in a single statement. A seat that is neither available nor
-- unavailable is simply not returned; the caller compares the counts.
UPDATE session_seats
SET    status         = 'held',
       reservation_id = $2,
       status_version = $3,
       updated_at     = now()
WHERE  id = ANY($1::uuid[])
  AND  status IN ('available', 'unavailable')
RETURNING id, session_id, seat_key, sector_name, row_name, seat_number,
          tier_id, status, reservation_id, status_version, updated_at, system_seat_id, kind;

-- name: InsertReservationSeats :exec
-- Links every held seat of an invitation to its reservation in one statement.
INSERT INTO reservation_seats (reservation_id, session_seat_id)
SELECT $1, unnest($2::uuid[]);

-- name: GetInvitationSessionCapacity :one
-- The session-level ledger row an invitation reserves against (tier_id NULL):
-- the dry run reads it to say what ReserveCapacity would answer.
SELECT capacity_total, capacity_held + capacity_sold AS used
FROM   inventory_ledger
WHERE  session_id = $1
  AND  tier_id IS NULL;
