package wpletters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the part of a pgx pool or transaction Import needs.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Report counts what a run found. In a dry run Inserted and Updated are the
// numbers WOULD be written.
type Report struct {
	Records       int // records in the file
	Skipped       int // no recipient or no usable time
	NoOrderNumber int // the WooCommerce order has no platform order number
	Unmatched     int // numbered, but no such order in this organization
	Matched       int
	AlreadyThere  int // a row with this message id exists and is not behind
	Inserted      int
	Updated       int // an existing wp_brevo row that the file is further along than
	UnmatchedList []string
}

// Import files the records for one organization. dry=true writes nothing.
// Idempotent: a letter whose (provider, message id) exists is left alone,
// unless it is one of ours and the file knows more (delivered, opened,
// failed), in which case only the status columns move forward.
func Import(ctx context.Context, db DB, orgID uuid.UUID, recs []Record, dry bool) (Report, error) {
	var rep Report
	rep.Records = len(recs)
	for _, rec := range recs {
		letter, ok := Map(rec)
		if !ok {
			rep.Skipped++
			continue
		}
		sid := rec.SystemID()
		if sid == 0 {
			rep.NoOrderNumber++
			continue
		}
		var orderID uuid.UUID
		err := db.QueryRow(ctx, `SELECT id FROM orders WHERE org_id = $1 AND system_id = $2`, orgID, sid).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			rep.Unmatched++
			rep.UnmatchedList = append(rep.UnmatchedList, fmt.Sprintf("wc_order=%s system_id=%d", rec.WCOrderID, sid))
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("find order %d: %w", sid, err)
		}
		rep.Matched++

		existing, found, err := findExisting(ctx, db, orgID, orderID, letter)
		if err != nil {
			return rep, err
		}
		if found {
			if Rank(letter.Status) > Rank(existing.status) {
				rep.Updated++
				if !dry {
					if err := advance(ctx, db, existing.id, letter); err != nil {
						return rep, err
					}
				}
			} else {
				rep.AlreadyThere++
			}
			continue
		}
		rep.Inserted++
		if dry {
			continue
		}
		numbers, err := ticketNumbers(ctx, db, orderID)
		if err != nil {
			return rep, err
		}
		if err := insert(ctx, db, orgID, orderID, letter, numbers); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

type existingLetter struct {
	id     uuid.UUID
	status string
}

// findExisting looks a letter up by (provider, message id). A record without a
// message id is matched on the order and recipient instead, so it cannot be
// filed twice either.
func findExisting(ctx context.Context, db DB, orgID, orderID uuid.UUID, l Letter) (existingLetter, bool, error) {
	var e existingLetter
	var err error
	if l.MessageID != "" {
		err = db.QueryRow(ctx,
			`SELECT id, status FROM email_letters WHERE provider = $1 AND provider_message_id = $2`,
			Provider, l.MessageID).Scan(&e.id, &e.status)
	} else {
		err = db.QueryRow(ctx,
			`SELECT id, status FROM email_letters
			  WHERE org_id = $1 AND order_id = $2 AND provider = $3 AND provider_message_id IS NULL AND recipient = $4
			  ORDER BY sent_at LIMIT 1`,
			orgID, orderID, Provider, l.Recipient).Scan(&e.id, &e.status)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return e, false, nil
	}
	if err != nil {
		return e, false, fmt.Errorf("look up letter: %w", err)
	}
	return e, true, nil
}

// ticketNumbers are the display numbers of the order's tickets
// (tickets.system_ticket_id, what the buyer sees), in ticket order.
func ticketNumbers(ctx context.Context, db DB, orderID uuid.UUID) ([]string, error) {
	rows, err := db.Query(ctx,
		`SELECT t.system_ticket_id::text
		   FROM order_items oi JOIN tickets t ON t.id = oi.ticket_id
		  WHERE oi.order_id = $1 AND t.system_ticket_id IS NOT NULL
		  ORDER BY oi.ordinal`, orderID)
	if err != nil {
		return nil, fmt.Errorf("ticket numbers: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func insert(ctx context.Context, db DB, orgID, orderID uuid.UUID, l Letter, numbers []string) error {
	var msg *string
	if l.MessageID != "" {
		msg = &l.MessageID
	}
	_, err := db.Exec(ctx, `
INSERT INTO email_letters
    (id, org_id, order_id, kind, recipient, provider, provider_message_id, status,
     sent_at, delivered_at, proxy_opened_at, opened_at, failed_at, failure_event, failure_reason,
     ticket_numbers, template_version, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, now())
ON CONFLICT (provider, provider_message_id) WHERE provider_message_id IS NOT NULL DO NOTHING`,
		uuid.New(), orgID, orderID, KindTicket, l.Recipient, Provider, msg, l.Status,
		l.SentAt, l.DeliveredAt, l.ProxyOpenedAt, l.OpenedAt, l.FailedAt, l.FailureEvent, l.FailureReason,
		numbers, TemplateVersion)
	if err != nil {
		return fmt.Errorf("insert letter: %w", err)
	}
	return nil
}

// advance moves an existing imported row forward: the status and the times the
// new copy knows, never a time already stored.
func advance(ctx context.Context, db DB, id uuid.UUID, l Letter) error {
	_, err := db.Exec(ctx, `
UPDATE email_letters SET
    status          = $2,
    delivered_at    = COALESCE(delivered_at, $3),
    proxy_opened_at = COALESCE(proxy_opened_at, $4),
    opened_at       = COALESCE(opened_at, $5),
    failed_at       = COALESCE(failed_at, $6),
    failure_event   = COALESCE(failure_event, $7),
    failure_reason  = COALESCE(failure_reason, $8),
    updated_at      = now()
WHERE id = $1 AND provider = $9`,
		id, l.Status, l.DeliveredAt, l.ProxyOpenedAt, l.OpenedAt, l.FailedAt, l.FailureEvent, l.FailureReason, Provider)
	if err != nil {
		return fmt.Errorf("advance letter: %w", err)
	}
	return nil
}
