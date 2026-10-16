// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: ticket_vip.sql

package gen

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const setTicketVIP = `-- name: SetTicketVIP :one
UPDATE tickets
SET    vip        = $2,
       vip_set_by = $3,
       vip_set_at = now()
WHERE  id = $1
  AND  vip IS DISTINCT FROM $2
RETURNING id`

// SetTicketVIP sets or clears the organizer's VIP mark of one ticket and
// stamps who changed it and when (V1, migration 0160). It reports whether the
// row changed: false means the ticket already had that value (or does not
// exist), so a repeated call writes nothing and the caller writes no audit
// row either. The caller proves the ticket belongs to the organization first
// (GetTicketOrgID). tickets.updated_at is left alone on purpose: the mark is
// private to the organizer and must not look like a change to anything that
// follows ticket updates.
func (q *Queries) SetTicketVIP(ctx context.Context, ticketID uuid.UUID, vip bool, by *uuid.UUID) (bool, error) {
	var id uuid.UUID
	err := q.db.QueryRow(ctx, setTicketVIP, ticketID, vip, by).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

const setTicketsVIPByIssuance = `-- name: SetTicketsVIPByIssuance :exec
UPDATE tickets
SET    vip        = true,
       vip_set_by = $2,
       vip_set_at = now()
WHERE  complimentary_issuance_id = $1`

// SetTicketsVIPByIssuance marks every ticket of one complimentary issuance as
// VIP, in the issuing transaction (V1: an invitation issued as VIP uses the
// same column as a bought ticket).
func (q *Queries) SetTicketsVIPByIssuance(ctx context.Context, issuanceID uuid.UUID, by *uuid.UUID) error {
	_, err := q.db.Exec(ctx, setTicketsVIPByIssuance, issuanceID, by)
	return err
}

const listTicketVIPByIDs = `-- name: ListTicketVIPByIDs :many
SELECT id FROM tickets WHERE id = ANY($1) AND vip`

// ListTicketVIPByIDs returns the set of ticket ids, out of the given ones,
// that carry the VIP mark.
func (q *Queries) ListTicketVIPByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listTicketVIPByIDs, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
