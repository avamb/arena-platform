package ticketreissue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/mint"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/outbox"
)

// Pair is one replaced ticket.
type Pair struct {
	OldTicketID  uuid.UUID
	NewTicketID  uuid.UUID
	OldNumber    int64
	NewNumber    int64
	OldBarcode   string
	NewBarcode   string
	Seat         string
	Category     string
	OldScannedAt *time.Time
}

// Input is one reissue request.
type Input struct {
	OrgID   uuid.UUID
	OrderID uuid.UUID
	// IssuanceID names an invitation without an order (spec 63 R-5) instead
	// of OrderID. Exactly one of the two is set.
	IssuanceID     uuid.UUID
	Email          string
	Reason         string
	Note           string
	IdempotencyKey string
	// ActorType, ActorID and Via are written to the journal.
	ActorType string
	ActorID   string
	Via       string
	// ActorLabel is order_events.actor ("user:<uuid>").
	ActorLabel string

	// Enqueue queues the letter with the new tickets on the reissue
	// transaction: one delivery job per new ticket, all to recipient.
	Enqueue func(ctx context.Context, tx pgx.Tx, pairs []Pair, recipient string) error
	// Audit writes the audit event on the reissue transaction. meta carries
	// no address.
	Audit func(ctx context.Context, tx pgx.Tx, meta map[string]any) error
	// Outbox appends the events MACS and the sites read. Nil skips them
	// (tests that do not wire the outbox).
	Outbox outbox.Writer
}

// Result is the answer of a reissue.
type Result struct {
	ReissueID uuid.UUID
	State     string
	NewEmail  string
	Pairs     []Pair
	Replayed  bool
	CreatedAt time.Time
}

// Reissue runs the whole operation in one transaction (spec 63 section 4.4).
// A repeat with the same idempotency key answers the first result and writes
// nothing; the same key with another request is ErrKeyReused.
func Reissue(ctx context.Context, db Beginner, in Input) (Result, error) {
	in.Email = strings.TrimSpace(in.Email)
	in.Note = strings.TrimSpace(in.Note)
	if !ValidReason(in.Reason) {
		return Result{}, ErrInvalidReason
	}
	if len([]rune(in.Note)) > MaxNote {
		return Result{}, ErrNoticeTooLong
	}
	if (in.OrderID == uuid.Nil) == (in.IssuanceID == uuid.Nil) {
		return Result{}, ErrOrderNotFound
	}
	fp := Fingerprint(in.subjectID(), in.Email, in.Reason, in.Note)

	run := reissueTx
	if in.IssuanceID != uuid.Nil {
		run = reissueInvitationTx
	}
	res, err := run(ctx, db, in, fp)
	if err == nil {
		return res, nil
	}
	// Two identical requests at once: the loser hits the key's unique
	// constraint and answers what the winner wrote.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "ticket_reissues_org_key_uq" {
		return replayOutsideTx(ctx, db, in, fp)
	}
	// Two different requests at once: the second finds the first still open.
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(pgErr.ConstraintName == "ticket_reissues_one_open_per_order_uq" ||
			pgErr.ConstraintName == "ticket_reissues_one_open_per_invitation_uq") {
		return Result{}, ErrInProgress
	}
	return Result{}, err
}

func replayOutsideTx(ctx context.Context, db Beginner, in Input, fp string) (Result, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, found, err := replay(ctx, tx, in, fp)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, ErrInProgress
	}
	return res, nil
}

// replay answers an earlier operation under the same key.
func replay(ctx context.Context, q Querier, in Input, fp string) (Result, bool, error) {
	var (
		id                 uuid.UUID
		subjectID          uuid.UUID
		storedFP, st, mail string
		created            time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT id, COALESCE(order_id, complimentary_issuance_id), request_fingerprint, state, new_email, created_at
		FROM   ticket_reissues WHERE org_id = $1 AND idempotency_key = $2`,
		in.OrgID, in.IdempotencyKey).Scan(&id, &subjectID, &storedFP, &st, &mail, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("ticketreissue: look up key: %w", err)
	}
	if storedFP != fp || subjectID != in.subjectID() {
		return Result{}, true, ErrKeyReused
	}
	pairs, err := loadPairs(ctx, q, id)
	if err != nil {
		return Result{}, true, err
	}
	return Result{ReissueID: id, State: st, NewEmail: mail, Pairs: pairs, Replayed: true, CreatedAt: created}, true, nil
}

func reissueTx(ctx context.Context, db Beginner, in Input, fp string) (Result, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("ticketreissue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if res, found, err := replay(ctx, tx, in, fp); found || err != nil {
		return res, err
	}

	// Lock order: the order row, then its tickets by id (spec 63 section 4.4).
	o, err := loadOrder(ctx, tx, in.OrgID, in.OrderID, true)
	if err != nil {
		return Result{}, err
	}
	if o.Status != "paid" && o.Status != "partially_refunded" {
		return Result{}, ErrNotPaid
	}
	site, err := siteRoute(ctx, tx, o.ChannelID)
	if err != nil {
		return Result{}, err
	}
	if site {
		return Result{}, ErrSellerSite
	}
	if o.SessionClosed {
		return Result{}, ErrSessionClosed
	}
	if open, err := loadOpen(ctx, tx, o.ID); err != nil {
		return Result{}, err
	} else if open != nil {
		return Result{}, ErrInProgress
	}
	tickets, err := loadTickets(ctx, tx, o, true)
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
		OrderID: o.ID, OrderNumber: o.Number, OrgID: o.OrgID, SessionID: o.SessionID,
		CheckoutSessionID: o.CheckoutSessionID, OldEmail: o.BuyerEmail,
	}, active)
}

// subject is what one reissue replaces: an order, or an invitation without
// an order (OrderID uuid.Nil, IssuanceID set).
type subject struct {
	OrderID           uuid.UUID
	OrderNumber       int64
	IssuanceID        uuid.UUID
	OrgID             uuid.UUID
	SessionID         uuid.UUID
	CheckoutSessionID uuid.UUID
	OldEmail          string
}

// finishTx replaces the checked tickets and writes the journal, the history,
// the outbox, the letter and the audit, then commits.
func finishTx(ctx context.Context, tx pgx.Tx, in Input, fp string, subj subject, active []ticketFacts) (Result, error) {
	txQ := gen.New(tx)
	authority, err := txQ.GetBarcodeAuthorityByType(ctx, "platform")
	if err != nil {
		return Result{}, fmt.Errorf("ticketreissue: platform barcode authority: %w", err)
	}

	pairs := make([]Pair, 0, len(active))
	var firstScan *time.Time
	for _, t := range active {
		p, err := replaceOne(ctx, tx, txQ, authority.ID, subj.OrderID, t, in.Email)
		if err != nil {
			return Result{}, err
		}
		if t.ScannedAt != nil && (firstScan == nil || t.ScannedAt.Before(*firstScan)) {
			firstScan = t.ScannedAt
		}
		pairs = append(pairs, p)
	}

	// The purchase token of the first letter stops working (decision R-12).
	// An invitation without an order has none.
	if subj.CheckoutSessionID != uuid.Nil {
		token, err := mintToken()
		if err != nil {
			return Result{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE checkout_sessions SET checkout_token = $2, updated_at = now() WHERE id = $1`,
			subj.CheckoutSessionID, token); err != nil {
			return Result{}, fmt.Errorf("ticketreissue: rotate purchase token: %w", err)
		}
	}

	var (
		reissueID uuid.UUID
		created   time.Time
	)
	if err := tx.QueryRow(ctx, `
		INSERT INTO ticket_reissues
		    (org_id, order_id, complimentary_issuance_id, old_email, new_email, reason, note, old_code_scanned_at,
		     actor_type, actor_id, via, idempotency_key, request_fingerprint)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''), $8, $9, NULLIF($10, ''), NULLIF($11, ''), $12, $13)
		RETURNING id, created_at`,
		subj.OrgID, nilIfZero(subj.OrderID), nilIfZero(subj.IssuanceID), subj.OldEmail, in.Email, in.Reason, in.Note, firstScan,
		in.ActorType, in.ActorID, in.Via, in.IdempotencyKey, fp).Scan(&reissueID, &created); err != nil {
		return Result{}, err
	}
	for _, p := range pairs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ticket_reissue_items (reissue_id, old_ticket_id, new_ticket_id, old_barcode, new_barcode, old_used_at)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6)`,
			reissueID, p.OldTicketID, p.NewTicketID, p.OldBarcode, p.NewBarcode, p.OldScannedAt); err != nil {
			return Result{}, fmt.Errorf("ticketreissue: journal item: %w", err)
		}
	}

	// Order history: numbers and masked addresses, read by every member.
	numbers := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		numbers = append(numbers, map[string]any{"old": p.OldNumber, "new": p.NewNumber})
	}
	hist, _ := json.Marshal(map[string]any{
		"reissue_id":       reissueID.String(),
		"tickets":          numbers,
		"reason":           in.Reason,
		"old_email":        maskOrEmpty(subj.OldEmail),
		"new_email":        MaskEmail(in.Email),
		"old_code_scanned": firstScan != nil,
	})
	actorLabel := in.ActorLabel
	if actorLabel == "" {
		actorLabel = "system"
	}
	if subj.OrderID != uuid.Nil {
		if _, err := txQ.InsertOrderEvent(ctx, subj.OrderID, OrderEventType, actorLabel, hist); err != nil {
			return Result{}, fmt.Errorf("ticketreissue: order event: %w", err)
		}
	}

	if in.Outbox != nil {
		for _, p := range pairs {
			if err := in.Outbox.Append(ctx, tx, outbox.Event{
				AggregateType: "ticket",
				AggregateID:   p.OldTicketID.String(),
				EventType:     EventOldTicket,
				Payload: subj.tag(map[string]any{
					"ticket_id":     p.OldTicketID.String(),
					"new_ticket_id": p.NewTicketID.String(),
					"reason":        CancellationReason,
				}),
			}); err != nil {
				return Result{}, fmt.Errorf("ticketreissue: outbox old ticket: %w", err)
			}
		}
		for _, p := range pairs {
			if err := in.Outbox.Append(ctx, tx, outbox.Event{
				AggregateType: "ticket",
				AggregateID:   p.NewTicketID.String(),
				EventType:     EventNewTicket,
				Payload: subj.tag(map[string]any{
					"ticket_id":          p.NewTicketID.String(),
					"replaces_ticket_id": p.OldTicketID.String(),
				}),
			}); err != nil {
				return Result{}, fmt.Errorf("ticketreissue: outbox new ticket: %w", err)
			}
		}
	}

	if in.Enqueue != nil {
		if err := in.Enqueue(ctx, tx, pairs, in.Email); err != nil {
			return Result{}, fmt.Errorf("ticketreissue: queue the letter: %w", err)
		}
	}
	if in.Audit != nil {
		if err := in.Audit(ctx, tx, map[string]any{
			"org_id":           subj.OrgID.String(),
			"reissue_id":       reissueID.String(),
			"tickets":          len(pairs),
			"reason":           in.Reason,
			"old_code_scanned": firstScan != nil,
		}); err != nil {
			return Result{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{ReissueID: reissueID, State: StatePending, NewEmail: in.Email, Pairs: pairs, CreatedAt: created}, nil
}

const insertReplacementSQL = `
INSERT INTO tickets (
    checkout_session_id, session_id, tier_id, holder_email,
    seat_key, seat_sector, seat_row, seat_number, ordinal,
    order_id, holder_name, vip, vip_set_by, vip_set_at, complimentary_issuance_id)
SELECT src.checkout_session_id, src.session_id, src.tier_id, $2,
       src.seat_key, src.seat_sector, src.seat_row, src.seat_number,
       (SELECT COALESCE(max(o2.ordinal), -1) + 1 FROM tickets o2
        WHERE o2.checkout_session_id = src.checkout_session_id),
       src.order_id, src.holder_name, src.vip, src.vip_set_by, src.vip_set_at, src.complimentary_issuance_id
FROM   tickets src
WHERE  src.id = $1
RETURNING id, system_ticket_id`

// replaceOne closes one old ticket and issues its replacement on tx.
func replaceOne(ctx context.Context, tx pgx.Tx, txQ *gen.Queries, authorityID uuid.UUID,
	orderID uuid.UUID, t ticketFacts, email string) (Pair, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE tickets
		SET    status = 'transferred', cancelled_at = now(), cancellation_reason = $2, updated_at = now()
		WHERE  id = $1 AND status = 'active'`, t.ID, CancellationReason)
	if err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: close ticket %d: %w", t.Number, err)
	}
	if tag.RowsAffected() != 1 {
		return Pair{}, &TicketRefusal{TicketID: t.ID, Number: t.Number, Reason: "not_active"}
	}
	// Unlike a cancellation, a code left open here defeats the operation:
	// every error is returned, never logged and swallowed.
	if _, err := tx.Exec(ctx, `UPDATE barcodes SET status = 'revoked', updated_at = now()
		WHERE ticket_id = $1 AND status <> 'revoked'`, t.ID); err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: revoke barcodes of %d: %w", t.Number, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_credentials SET revoked_at = now()
		WHERE ticket_id = $1 AND revoked_at IS NULL`, t.ID); err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: revoke credentials of %d: %w", t.Number, err)
	}

	var (
		newID     uuid.UUID
		newNumber int64
	)
	if err := tx.QueryRow(ctx, insertReplacementSQL, t.ID, email).Scan(&newID, &newNumber); err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: issue replacement of %d: %w", t.Number, err)
	}
	code, err := mint.EAN13(ctx, txQ, authorityID, &newID, nil)
	if err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: mint barcode for %d: %w", newNumber, err)
	}
	if _, err := txQ.InsertTicketCredential(ctx, newID, "ean13", code); err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: store barcode for %d: %w", newNumber, err)
	}
	// The order line moves; its money does not change.
	if _, err := tx.Exec(ctx, `UPDATE order_items SET ticket_id = $2 WHERE order_id = $3 AND ticket_id = $1`,
		t.ID, newID, orderID); err != nil {
		return Pair{}, fmt.Errorf("ticketreissue: move order line of %d: %w", t.Number, err)
	}
	return Pair{
		OldTicketID: t.ID, NewTicketID: newID,
		OldNumber: t.Number, NewNumber: newNumber,
		OldBarcode: t.Barcode, NewBarcode: code,
		Seat: t.SeatLabel(), Category: t.Category,
		OldScannedAt: t.ScannedAt,
	}, nil
}

func loadPairs(ctx context.Context, q Querier, reissueID uuid.UUID) ([]Pair, error) {
	rows, err := q.Query(ctx, `
		SELECT i.old_ticket_id, i.new_ticket_id, ot.system_ticket_id, nt.system_ticket_id,
		       COALESCE(i.old_barcode, ''), i.new_barcode, i.old_used_at,
		       concat_ws(' · ', NULLIF(nt.seat_sector, ''), NULLIF(nt.seat_row, ''), NULLIF(nt.seat_number, '')),
		       COALESCE(tt.name, '')
		FROM   ticket_reissue_items i
		JOIN   tickets ot ON ot.id = i.old_ticket_id
		JOIN   tickets nt ON nt.id = i.new_ticket_id
		LEFT   JOIN ticket_tiers tt ON tt.id = nt.tier_id
		WHERE  i.reissue_id = $1
		ORDER  BY ot.id`, reissueID)
	if err != nil {
		return nil, fmt.Errorf("ticketreissue: load items: %w", err)
	}
	defer rows.Close()
	var out []Pair
	for rows.Next() {
		var p Pair
		if err := rows.Scan(&p.OldTicketID, &p.NewTicketID, &p.OldNumber, &p.NewNumber,
			&p.OldBarcode, &p.NewBarcode, &p.OldScannedAt, &p.Seat, &p.Category); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func maskOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return MaskEmail(s)
}

func (in Input) subjectID() uuid.UUID {
	if in.IssuanceID != uuid.Nil {
		return in.IssuanceID
	}
	return in.OrderID
}

func nilIfZero(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// tag adds the subject's ids to an outbox payload: order_id for an order,
// invitation_id for an invitation without one.
func (s subject) tag(m map[string]any) map[string]any {
	m["org_id"] = s.OrgID.String()
	m["session_id"] = s.SessionID.String()
	if s.OrderID != uuid.Nil {
		m["order_id"] = s.OrderID.String()
	}
	if s.IssuanceID != uuid.Nil {
		m["invitation_id"] = s.IssuanceID.String()
	}
	return m
}
