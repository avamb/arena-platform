package delivery

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// Every letter a buyer receives tells them not to reply and names the address
// to write to (owner request 2026-11-08, Vino&Co). The address belongs to the
// ORGANIZATION: the event's promoter e-mail when the event has a promoter with
// one, else the organization's contact_email - exactly sessionchange.
// ResolveContact, the rule the session-change letters already follow. The same
// address is the letter's Reply-To, so a reply that is sent anyway still
// reaches the organizer. No address, no block and no Reply-To.

// buyerContactEmail resolves the organizer's buyer-facing address of an event.
// eventID may be uuid.Nil for a letter that belongs to no event (then only the
// organization's address counts). Empty when nobody has one, or on a failed
// lookup (logged): a letter is never held back for its contact block.
func buyerContactEmail(ctx context.Context, db sessionchange.DB, eventID, orgID uuid.UUID, logger *slog.Logger) string {
	if db == nil || orgID == uuid.Nil {
		return ""
	}
	c, err := sessionchange.ResolveContact(ctx, db, eventID, orgID)
	if err != nil {
		if logger != nil {
			logger.Warn("delivery: buyer contact lookup failed; the letter goes without the contact block",
				slog.String("org_id", orgID.String()), slog.String("error", err.Error()))
		}
		return ""
	}
	if !c.Complete() {
		return ""
	}
	return strings.TrimSpace(c.Email)
}

// ticketEventOrg is the event and the organization a ticket belongs to.
func ticketEventOrg(ctx context.Context, db sessionchange.DB, ticketID uuid.UUID) (eventID, orgID uuid.UUID, err error) {
	err = db.QueryRow(ctx, `
		SELECT e.id, e.org_id
		  FROM tickets t
		  JOIN sessions s ON s.id = t.session_id
		  JOIN events   e ON e.id = s.event_id
		 WHERE t.id = $1`, ticketID).Scan(&eventID, &orgID)
	return eventID, orgID, err
}

// applyBuyerContact writes the organizer's address onto the payload of a
// ticket letter: it becomes OrgContactEmail (printed in the footer and the
// contact block, and the Reply-To) and replaces the contact-form link, which
// is only the stand-in for a letter without an address. When no address
// resolves the payload keeps whatever the older ContactRoute rule gave it.
func applyBuyerContact(ctx context.Context, db sessionchange.DB, ticketID uuid.UUID, p *Payload, logger *slog.Logger) {
	if db == nil || ticketID == uuid.Nil {
		return
	}
	eventID, orgID, err := ticketEventOrg(ctx, db, ticketID)
	if err != nil {
		if logger != nil && !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("delivery: ticket event lookup for the buyer contact failed",
				slog.String("ticket_id", ticketID.String()), slog.String("error", err.Error()))
		}
		return
	}
	if email := buyerContactEmail(ctx, db, eventID, orgID, logger); email != "" {
		p.OrgContactEmail = email
		p.ContactFormURL = ""
	}
}

// orderSystemID is the buyer-facing number of an order (orders.system_id), ""
// when it cannot be read.
func orderSystemID(ctx context.Context, db sessionchange.DB, orderID uuid.UUID) string {
	if db == nil || orderID == uuid.Nil {
		return ""
	}
	var n int64
	if err := db.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, orderID).Scan(&n); err != nil || n <= 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

// eventOfSession is the event a session belongs to, uuid.Nil when unknown.
func eventOfSession(ctx context.Context, db sessionchange.DB, sessionID uuid.UUID) uuid.UUID {
	if db == nil || sessionID == uuid.Nil {
		return uuid.Nil
	}
	var id uuid.UUID
	if err := db.QueryRow(ctx, `SELECT event_id FROM sessions WHERE id = $1`, sessionID).Scan(&id); err != nil {
		return uuid.Nil
	}
	return id
}

// sessionContactEmail is the buyer contact of a letter about a session of an
// organization (the event's promoter, else the organization).
func sessionContactEmail(ctx context.Context, db sessionchange.DB, orgID, sessionID uuid.UUID, logger *slog.Logger) string {
	return buyerContactEmail(ctx, db, eventOfSession(ctx, db, sessionID), orgID, logger)
}

// withReplyTo makes replies to the letter reach the organizer. An empty
// address leaves the message without a Reply-To.
func withReplyTo(msg *email.Message, address string) {
	msg.ReplyTo = strings.TrimSpace(address)
}
