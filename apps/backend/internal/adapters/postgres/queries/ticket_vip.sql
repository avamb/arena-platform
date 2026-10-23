-- ticket_vip.sql — the organizer's VIP mark of a ticket (task V1, migration
-- 0164). The mark is private: it is NOT part of GetTicketPresentationByID, so
-- the PDF e-ticket and the buyer's letters never carry it.

-- name: SetTicketVIP :one
-- Sets or clears the mark and stamps who changed it. Returns the id only when
-- the value really changed; a repeated call returns no row and writes nothing.
UPDATE tickets
SET    vip        = $2,
       vip_set_by = $3,
       vip_set_at = now()
WHERE  id = $1
  AND  vip IS DISTINCT FROM $2
RETURNING id;

-- name: SetTicketsVIPByIssuance :exec
-- Marks every ticket of one complimentary issuance as VIP (issued as VIP).
UPDATE tickets
SET    vip        = true,
       vip_set_by = $2,
       vip_set_at = now()
WHERE  complimentary_issuance_id = $1;

-- name: ListTicketVIPByIDs :many
-- The subset of the given tickets that carries the mark.
SELECT id FROM tickets WHERE id = ANY($1) AND vip;
