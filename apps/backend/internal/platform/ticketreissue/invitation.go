// invitation.go — replacing the tickets of an invitation that has no order
// (spec 63, stage R-5).
//
// Invitations issued before spec 46 are complimentary_issuances rows whose
// tickets have no order and no checkout session. They are replaced exactly like
// an order (old tickets transferred, new numbers and barcodes, one letter, the
// address moves after the letter) with three differences: there is no
// purchase token to rotate, no order history and no payment provider, and the
// address that moves is the issuance's recipient list.
package ticketreissue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrInvitationNotFound: no such invitation in the organization.
	ErrInvitationNotFound = errors.New("ticketreissue: invitation not found")
	// ErrInvitationNotIssued: the invitation was revoked, failed or is still
	// waiting for its tickets.
	ErrInvitationNotIssued = errors.New("ticketreissue: invitation is not issued")
)

type invitationFacts struct {
	ID            uuid.UUID
	OrgID         uuid.UUID
	Status        string
	SessionID     uuid.UUID
	Recipient     string
	SessionClosed bool
	VenueTimezone string
}

const loadInvitationSQL = `
SELECT ci.id, ci.org_id, ci.status, ci.session_id, COALESCE(ci.recipients[1], ''),
       (s.status = 'cancelled' OR s.deleted_at IS NOT NULL
        OR COALESCE(s.end_at, s.start_at + interval '6 hours') < now()) AS session_closed,
       COALESCE((SELECT v.timezone FROM venues v WHERE v.id = s.venue_id), '') AS venue_timezone
FROM   complimentary_issuances ci
JOIN   sessions s ON s.id = ci.session_id
WHERE  ci.id = $1 AND ci.org_id = $2`

func loadInvitation(ctx context.Context, q Querier, orgID, issuanceID uuid.UUID, lock bool) (invitationFacts, error) {
	sql := loadInvitationSQL
	if lock {
		sql += "\nFOR UPDATE OF ci"
	}
	var f invitationFacts
	err := q.QueryRow(ctx, sql, issuanceID, orgID).Scan(&f.ID, &f.OrgID, &f.Status, &f.SessionID, &f.Recipient, &f.SessionClosed, &f.VenueTimezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrInvitationNotFound
	}
	if err != nil {
		return f, fmt.Errorf("ticketreissue: load invitation: %w", err)
	}
	return f, nil
}

// loadInvitationTicketsSQL mirrors loadTicketsSQL for the tickets of an
// invitation: no refund can be open on a ticket nobody paid for.
const loadInvitationTicketsSQL = `
SELECT t.id, t.system_ticket_id, t.status, t.review_hold,
       COALESCE(t.seat_sector, ''), COALESCE(t.seat_row, ''), COALESCE(t.seat_number, ''),
       COALESCE(tt.name, ''),
       false AS refund_open,
       COALESCE(t.used_at,
                (SELECT min(se.scanned_at) FROM scan_events se
                 WHERE se.ticket_id = t.id AND se.result = 'admitted'),
                (SELECT min(b.scanned_at) FROM barcodes b
                 WHERE b.ticket_id = t.id AND b.scanned_at IS NOT NULL)) AS scanned_at,
       COALESCE((SELECT c.payload FROM ticket_credentials c
                 WHERE c.ticket_id = t.id AND c.type = 'ean13'), '') AS barcode
FROM   tickets t
LEFT   JOIN ticket_tiers tt ON tt.id = t.tier_id
WHERE  t.complimentary_issuance_id = $1 AND t.order_id IS NULL
ORDER  BY t.id`

func loadInvitationTickets(ctx context.Context, q Querier, issuanceID uuid.UUID, lock bool) ([]ticketFacts, error) {
	sql := loadInvitationTicketsSQL
	if lock {
		sql += "\nFOR UPDATE OF t"
	}
	rows, err := q.Query(ctx, sql, issuanceID)
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: load invitation tickets: %w", err)
	}
	defer rows.Close()
	var out []ticketFacts
	for rows.Next() {
		var t ticketFacts
		if err := rows.Scan(&t.ID, &t.Number, &t.Status, &t.ReviewHold, &t.Sector, &t.Row, &t.Seat,
			&t.Category, &t.RefundOpen, &t.ScannedAt, &t.Barcode); err != nil {
			return nil, fmt.Errorf("ticketreissue: scan invitation ticket: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func loadOpenInvitation(ctx context.Context, q Querier, issuanceID uuid.UUID) (*Open, error) {
	var op Open
	err := q.QueryRow(ctx, `
		SELECT id, state, new_email, created_at FROM ticket_reissues
		WHERE  complimentary_issuance_id = $1 AND state IN ('pending', 'send_failed')
		ORDER  BY created_at DESC LIMIT 1`, issuanceID).Scan(&op.ID, &op.State, &op.NewEmail, &op.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: load open invitation reissue: %w", err)
	}
	return &op, nil
}

func reissueInvitationTx(ctx context.Context, db Beginner, in Input, fp string) (Result, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("ticketreissue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if res, found, err := replay(ctx, tx, in, fp); found || err != nil {
		return res, err
	}
	// Lock order: the invitation row, then its tickets by id.
	inv, err := loadInvitation(ctx, tx, in.OrgID, in.IssuanceID, true)
	if err != nil {
		return Result{}, err
	}
	if inv.Status != "issued" {
		return Result{}, ErrInvitationNotIssued
	}
	if inv.SessionClosed {
		return Result{}, ErrSessionClosed
	}
	if open, err := loadOpenInvitation(ctx, tx, inv.ID); err != nil {
		return Result{}, err
	} else if open != nil {
		return Result{}, ErrInProgress
	}
	tickets, err := loadInvitationTickets(ctx, tx, inv.ID, true)
	if err != nil {
		return Result{}, err
	}
	var active []ticketFacts
	for _, t := range tickets {
		if t.Status != "active" {
			continue
		}
		if r := t.refusal(); r != "" {
			return Result{}, &TicketRefusal{TicketID: t.ID, Number: t.Number, Reason: r}
		}
		active = append(active, t)
	}
	if len(active) == 0 {
		return Result{}, ErrNothingToReissue
	}
	return finishTx(ctx, tx, in, fp, subject{
		IssuanceID: inv.ID, OrgID: inv.OrgID, SessionID: inv.SessionID, OldEmail: inv.Recipient,
	}, active)
}

// InvitationOptions is the check screen of an invitation without an order.
type InvitationOptions struct {
	IssuanceID uuid.UUID
	Status     string
	Email      string
	// VenueTimezone is the IANA zone to print scanned_at in.
	VenueTimezone string
	Tickets       []TicketOption
	Blocked       string
	Open          *Open
}

// LoadInvitationOptions reads the invitation and its tickets without
// locking anything.
func LoadInvitationOptions(ctx context.Context, q Querier, orgID, issuanceID uuid.UUID) (InvitationOptions, error) {
	inv, err := loadInvitation(ctx, q, orgID, issuanceID, false)
	if err != nil {
		return InvitationOptions{}, err
	}
	tickets, err := loadInvitationTickets(ctx, q, inv.ID, false)
	if err != nil {
		return InvitationOptions{}, err
	}
	open, err := loadOpenInvitation(ctx, q, inv.ID)
	if err != nil {
		return InvitationOptions{}, err
	}
	out := InvitationOptions{IssuanceID: inv.ID, Status: inv.Status, Email: inv.Recipient, VenueTimezone: inv.VenueTimezone, Open: open}
	active := 0
	refusal := ""
	for _, t := range tickets {
		opt := TicketOption{TicketID: t.ID, Number: t.Number, Seat: t.SeatLabel(), Category: t.Category,
			Status: t.Status, ScannedAt: t.ScannedAt}
		switch {
		case t.Status != "active":
			opt.Reason = "not_active"
		case t.refusal() != "":
			opt.Reason = t.refusal()
			refusal = opt.Reason
		default:
			opt.Reissuable = true
			active++
		}
		out.Tickets = append(out.Tickets, opt)
	}
	switch {
	case inv.Status != "issued":
		out.Blocked = "reissue.invitation_not_issued"
	case inv.SessionClosed:
		out.Blocked = "reissue.session_closed"
	case refusal != "":
		out.Blocked = "reissue.ticket_not_reissuable"
	case open != nil:
		out.Blocked = "reissue.in_progress"
	case active == 0:
		out.Blocked = "reissue.nothing_to_reissue"
	}
	return out, nil
}

// InvitationHistory lists the reissues of an invitation, newest first.
func InvitationHistory(ctx context.Context, q Querier, orgID, issuanceID uuid.UUID) ([]Entry, error) {
	return history(ctx, q, orgID, "complimentary_issuance_id", issuanceID)
}

// GetInvitation reads one reissue of an invitation.
func GetInvitation(ctx context.Context, q Querier, orgID, issuanceID, reissueID uuid.UUID) (Entry, error) {
	all, err := InvitationHistory(ctx, q, orgID, issuanceID)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range all {
		if e.ID == reissueID {
			return e, nil
		}
	}
	return Entry{}, ErrReissueNotFound
}

// applyInvitation moves an invitation to the new address on tx once the
// relay accepted its letter.
func applyInvitation(ctx context.Context, tx pgx.Tx, reissueID, issuanceID uuid.UUID, oldEmail, email string) error {
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM ticket_reissues WHERE id = $1 FOR UPDATE`, reissueID).Scan(&state); err != nil {
		return fmt.Errorf("ticketreissue: lock reissue: %w", err)
	}
	if state == StateApplied {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE complimentary_issuances
		SET    recipients = CASE WHEN cardinality(recipients) <= 1 THEN ARRAY[$2::text]
		                         ELSE array_replace(recipients, $3::text, $2::text) END,
		       updated_at = now()
		WHERE  id = $1`, issuanceID, email, oldEmail); err != nil {
		return fmt.Errorf("ticketreissue: change invitation address: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_reissues
		SET state = 'applied', applied_at = $2, provider_email_state = 'not_applicable', updated_at = now()
		WHERE id = $1`, reissueID, time.Now().UTC()); err != nil {
		return fmt.Errorf("ticketreissue: mark applied: %w", err)
	}
	return nil
}
