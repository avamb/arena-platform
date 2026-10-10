// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: complimentary_issuances.sql (ListComplimentaryIssuancesPage and
// friends - the paged, enriched list behind the Telegram bot's "Invitations"
// screen, spec 08_architecture/35 §6.3, EC-12).

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ComplimentaryStateFilter values of ListComplimentaryIssuancesPage: "" is
// every state.
const (
	ComplimentaryStateValid   = "valid"
	ComplimentaryStateRevoked = "revoked"
	ComplimentaryStateUsed    = "used"
)

// ComplimentaryListRow is one issuance of the list with what a person needs to
// recognise it: the event and date it is for, the category, the number of
// tickets and the derived state.
//
// State: "revoked" when the issuance was annulled, "used" when at least one
// of its tickets has been scanned at the door (or the issuance is parked for
// manual review because of that), "valid" otherwise.
type ComplimentaryListRow struct {
	ComplimentaryIssuanceRow
	EventID        *uuid.UUID
	EventName      *string
	SessionStartAt *time.Time
	VenueTimezone  *string
	TierName       *string
	TicketCount    int64
	State          string
}

// ComplimentaryListTicket is one ticket of a listed issuance.
type ComplimentaryListTicket struct {
	IssuanceID     uuid.UUID
	ID             uuid.UUID
	SystemTicketID int64
	HolderEmail    *string
	HolderName     *string
	Status         string
	Used           bool
}

// complimentaryListBase is the filtered, state-annotated set of an
// organization's issuances. $1 org, $2 session (NULL = any), $3 state (” =
// any). Pending and failed issuances are not shown: they never reached the
// guest.
const complimentaryListBase = `
WITH base AS (
    SELECT ci.*,
           (ci.status = 'manual_review' OR EXISTS (
               SELECT 1 FROM tickets t
               WHERE  t.complimentary_issuance_id = ci.id
                 AND (t.used_at IS NOT NULL OR EXISTS (
                         SELECT 1 FROM barcodes b WHERE b.ticket_id = t.id AND b.status = 'scanned'))
           )) AS used
    FROM   complimentary_issuances ci
    WHERE  ci.org_id = $1
      AND  ci.status IN ('issued', 'revoked', 'manual_review')
      AND  ($2::uuid IS NULL OR ci.session_id = $2)
), st AS (
    SELECT base.*,
           CASE WHEN base.status = 'revoked' THEN 'revoked'
                WHEN base.used THEN 'used'
                ELSE 'valid' END AS state
    FROM   base
)`

// CountComplimentaryIssuances counts the issuances the list would hold.
func (q *Queries) CountComplimentaryIssuances(ctx context.Context, orgID uuid.UUID, sessionID *uuid.UUID, state string) (int64, error) {
	const sql = complimentaryListBase + `
SELECT count(*) FROM st WHERE ($3::text = '' OR st.state = $3)`
	var n int64
	err := q.db.QueryRow(ctx, sql, orgID, sessionID, state).Scan(&n)
	return n, err
}

// ListComplimentaryIssuancesPage returns one page of the organization's
// issuances, newest first.
func (q *Queries) ListComplimentaryIssuancesPage(
	ctx context.Context, orgID uuid.UUID, sessionID *uuid.UUID, state string, limit, offset int32,
) ([]ComplimentaryListRow, error) {
	const sql = complimentaryListBase + `
SELECT st.id, st.org_id, st.session_id, st.tier_id, st.qty, st.recipients, st.batch_id, st.status,
       st.issued_by, st.notes, st.created_at, st.updated_at,
       e.id, e.name, s.start_at, v.timezone, tt.name,
       (SELECT count(*) FROM tickets t WHERE t.complimentary_issuance_id = st.id),
       st.state
FROM   st
LEFT JOIN sessions     s  ON s.id  = st.session_id
LEFT JOIN events       e  ON e.id  = s.event_id
LEFT JOIN venues       v  ON v.id  = s.venue_id
LEFT JOIN ticket_tiers tt ON tt.id = st.tier_id
WHERE  ($3::text = '' OR st.state = $3)
ORDER BY st.created_at DESC, st.id DESC
LIMIT $4 OFFSET $5`
	rows, err := q.db.Query(ctx, sql, orgID, sessionID, state, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComplimentaryListRow
	for rows.Next() {
		var r ComplimentaryListRow
		if err := rows.Scan(
			&r.ID, &r.OrgID, &r.SessionID, &r.TierID, &r.Qty, &r.Recipients, &r.BatchID, &r.Status,
			&r.IssuedBy, &r.Notes, &r.CreatedAt, &r.UpdatedAt,
			&r.EventID, &r.EventName, &r.SessionStartAt, &r.VenueTimezone, &r.TierName,
			&r.TicketCount, &r.State,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListComplimentaryTicketsForIssuances returns the tickets of the given
// issuances, oldest first.
func (q *Queries) ListComplimentaryTicketsForIssuances(ctx context.Context, issuanceIDs []uuid.UUID) ([]ComplimentaryListTicket, error) {
	const sql = `
SELECT t.complimentary_issuance_id, t.id, t.system_ticket_id, t.holder_email, t.holder_name, t.status,
       (t.used_at IS NOT NULL OR EXISTS (
            SELECT 1 FROM barcodes b WHERE b.ticket_id = t.id AND b.status = 'scanned')) AS used
FROM   tickets t
WHERE  t.complimentary_issuance_id = ANY($1)
ORDER BY t.issued_at ASC, t.id ASC`
	rows, err := q.db.Query(ctx, sql, issuanceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComplimentaryListTicket
	for rows.Next() {
		var t ComplimentaryListTicket
		if err := rows.Scan(&t.IssuanceID, &t.ID, &t.SystemTicketID, &t.HolderEmail, &t.HolderName, &t.Status, &t.Used); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
