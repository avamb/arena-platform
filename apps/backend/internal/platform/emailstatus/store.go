package emailstatus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the narrow query surface the package needs (a pool, a transaction
// or gen.Queries.DB()).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TxBeginner opens the transaction Apply runs in (a *pgxpool.Pool or the
// server's PoolDB).
type TxBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// ProviderBrevo is the only provider today (email_letters.provider).
const ProviderBrevo = "brevo"

// sendingKeepSQL is how long a "sending" row may live: a worker that died
// between Begin and Accepted/Abandon leaves one behind, and the next Begin
// removes it, so the recipient address does not linger.
const sendingKeepSQL = "1 day"

// NewLetter describes a letter about to be handed to the relay. Besides the
// routing fields it carries what proves later which letter was sent: the
// subject, the ticket numbers, a hash of the rendered body, the attached
// files' hashes and the template version (dispute evidence; MinRetention).
type NewLetter struct {
	OrgID     uuid.UUID
	OrderID   *uuid.UUID
	Kind      string
	Recipient string

	Subject         string
	TicketNumbers   []string
	BodySHA256      string
	Attachments     []AttachmentDigest
	TemplateVersion string
}

// AttachmentDigest is one attached file as sent.
type AttachmentDigest struct {
	Filename string `json:"filename"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256"`
}

// BodyDigest is the SHA-256 (hex) of a rendered letter: the HTML body, a NUL
// byte, the text body.
func BodyDigest(html, text string) string {
	h := sha256.New()
	h.Write([]byte(html))
	h.Write([]byte{0})
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// FileDigest is the SHA-256 (hex) of one attachment.
func FileDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// maxInteractionsPerLetter bounds the open/click log of one letter: a letter
// forwarded to a mailing list must not grow the table without limit.
const maxInteractionsPerLetter = 500

// Begin stores a letter in state "sending" and returns its id, which goes
// into the X-Mailin-custom header (TrackingID). It exists BEFORE the relay
// sees the message so a webhook event that outruns Accepted still finds it.
func Begin(ctx context.Context, db DB, l NewLetter) (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := db.Exec(ctx, `DELETE FROM email_letters WHERE status = 'sending' AND sent_at < now() - $1::interval`,
		sendingKeepSQL); err != nil {
		return uuid.Nil, fmt.Errorf("emailstatus: sweep stale letters: %w", err)
	}
	numbers := l.TicketNumbers
	if numbers == nil {
		numbers = []string{}
	}
	atts := l.Attachments
	if atts == nil {
		atts = []AttachmentDigest{}
	}
	attJSON, err := json.Marshal(atts)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := db.Exec(ctx, `INSERT INTO email_letters (id, org_id, order_id, kind, recipient, provider, status,
		    subject, ticket_numbers, body_sha256, attachments, template_version)
		VALUES ($1, $2, $3, $4, $5, $6, 'sending', $7, $8, $9, $10, $11)`,
		id, l.OrgID, l.OrderID, l.Kind, l.Recipient, ProviderBrevo,
		nonEmpty(l.Subject), numbers, nonEmpty(l.BodySHA256), attJSON, nonEmpty(l.TemplateVersion)); err != nil {
		return uuid.Nil, fmt.Errorf("emailstatus: begin letter: %w", err)
	}
	return id, nil
}

// Accepted marks the letter sent after the relay accepted it: stores the
// provider's message id and links every delivery_jobs row of the letter.
// Events that already arrived keep the status they produced.
func Accepted(ctx context.Context, db DB, letterID uuid.UUID, messageID string, deliveryJobIDs []uuid.UUID) error {
	var mid *string
	if m := NormalizeMessageID(messageID); m != "" {
		mid = &m
	}
	if _, err := db.Exec(ctx, `UPDATE email_letters
		SET provider_message_id = COALESCE($2, provider_message_id),
		    status     = CASE WHEN status = 'sending' THEN 'sent' ELSE status END,
		    sent_at    = now(),
		    updated_at = now()
		WHERE id = $1`, letterID, mid); err != nil {
		return fmt.Errorf("emailstatus: mark letter sent: %w", err)
	}
	if len(deliveryJobIDs) == 0 {
		return nil
	}
	if _, err := db.Exec(ctx, `UPDATE delivery_jobs SET email_letter_id = $1, provider_message_id = $2
		WHERE id = ANY($3)`, letterID, mid, deliveryJobIDs); err != nil {
		return fmt.Errorf("emailstatus: link delivery jobs: %w", err)
	}
	return nil
}

// Abandon removes a letter the relay refused: nothing was sent.
func Abandon(ctx context.Context, db DB, letterID uuid.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM email_letters WHERE id = $1 AND status = 'sending'`, letterID)
	return err
}

// Outcome is what Apply did with one event (also the metrics label).
type Outcome string

const (
	OutcomeApplied   Outcome = "applied"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeNotOurs   Outcome = "not_ours"
	OutcomeIgnored   Outcome = "ignored"
	OutcomeUnknown   Outcome = "unknown_event"
)

// Apply records one provider event on the letter it belongs to and
// recomputes the letter's status. An event for a message arena did not send
// (the Brevo account is shared with other sites) is OutcomeNotOurs and
// touches nothing.
func Apply(ctx context.Context, txb TxBeginner, ev ProviderEvent) (Outcome, error) {
	switch ev.Class {
	case ClassIgnored:
		return OutcomeIgnored, nil
	case ClassUnknown:
		return OutcomeUnknown, nil
	}
	if ev.MessageID == "" && ev.LetterID == uuid.Nil {
		return OutcomeNotOurs, nil
	}
	tx, err := txb.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	letterID, err := findLetter(ctx, tx, ev)
	if err != nil {
		return "", err
	}
	if letterID == uuid.Nil {
		return OutcomeNotOurs, nil
	}

	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return "", err
	}
	var reason *string
	if ev.Reason != "" {
		reason = &ev.Reason
	}
	var seen int
	if err := tx.QueryRow(ctx, `INSERT INTO email_letter_events (letter_id, event, first_at, last_at, reason, payload)
		VALUES ($1, $2, $3, $3, $4, $5)
		ON CONFLICT (letter_id, event) DO UPDATE SET
		    first_at   = LEAST(email_letter_events.first_at, EXCLUDED.first_at),
		    last_at    = GREATEST(email_letter_events.last_at, EXCLUDED.last_at),
		    seen_count = email_letter_events.seen_count + 1,
		    reason     = CASE WHEN EXCLUDED.last_at >= email_letter_events.last_at
		                      THEN COALESCE(EXCLUDED.reason, email_letter_events.reason)
		                      ELSE email_letter_events.reason END,
		    payload    = CASE WHEN EXCLUDED.last_at >= email_letter_events.last_at
		                      THEN EXCLUDED.payload ELSE email_letter_events.payload END
		RETURNING seen_count`,
		letterID, ev.Event, ev.At, reason, payload).Scan(&seen); err != nil {
		return "", fmt.Errorf("emailstatus: store event: %w", err)
	}

	if err := logInteraction(ctx, tx, letterID, ev); err != nil {
		return "", err
	}

	if ev.MessageID != "" {
		// A letter found by its tracking id learns its message id here when
		// the relay's reply did not name one.
		if _, err := tx.Exec(ctx, `UPDATE email_letters SET provider_message_id = $2
			WHERE id = $1 AND provider_message_id IS NULL
			  AND NOT EXISTS (SELECT 1 FROM email_letters o WHERE o.provider = $3 AND o.provider_message_id = $2)`,
			letterID, ev.MessageID, ProviderBrevo); err != nil {
			return "", fmt.Errorf("emailstatus: store message id: %w", err)
		}
	}

	if err := recompute(ctx, tx, letterID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if seen > 1 {
		return OutcomeDuplicate, nil
	}
	return OutcomeApplied, nil
}

// logInteraction keeps every open, proxy open and click with its own time
// (email_letter_events keeps only the first and last of each type). A
// redelivered event is the same row (unique on letter, event, time, link).
func logInteraction(ctx context.Context, tx pgx.Tx, letterID uuid.UUID, ev ProviderEvent) error {
	switch ev.Event {
	case EventOpened, EventProxyOpen, EventClick:
	default:
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO email_letter_interactions (letter_id, event, occurred_at, link)
		SELECT $1, $2, $3, $4
		WHERE (SELECT count(*) FROM email_letter_interactions WHERE letter_id = $1) < $5
		ON CONFLICT DO NOTHING`,
		letterID, ev.Event, ev.At, nonEmpty(ev.Link), maxInteractionsPerLetter)
	if err != nil {
		return fmt.Errorf("emailstatus: log interaction: %w", err)
	}
	return nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// findLetter locks the letter an event belongs to: by arena's tracking id
// first (it cannot belong to anyone else), then by the provider message id.
func findLetter(ctx context.Context, tx pgx.Tx, ev ProviderEvent) (uuid.UUID, error) {
	var id uuid.UUID
	if ev.LetterID != uuid.Nil {
		err := tx.QueryRow(ctx, `SELECT id FROM email_letters WHERE id = $1 FOR UPDATE`, ev.LetterID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, err
		}
	}
	if ev.MessageID == "" {
		return uuid.Nil, nil
	}
	err := tx.QueryRow(ctx, `SELECT id FROM email_letters WHERE provider = $1 AND provider_message_id = $2 FOR UPDATE`,
		ProviderBrevo, ev.MessageID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// recompute derives the letter's status from all its events.
func recompute(ctx context.Context, tx pgx.Tx, letterID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT event, first_at, last_at, COALESCE(reason, '')
		FROM email_letter_events WHERE letter_id = $1`, letterID)
	if err != nil {
		return err
	}
	var evs []EventTimes
	for rows.Next() {
		var e EventTimes
		if err := rows.Scan(&e.Event, &e.First, &e.Last, &e.Reason); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	d := Derive(evs)
	var failEvent, failReason *string
	if d.FailureEvent != "" {
		failEvent = &d.FailureEvent
	}
	if d.FailureReason != "" {
		failReason = &d.FailureReason
	}
	// A letter still "sending" (the event outran Accepted) moves on all the
	// same: the provider has it, so it was accepted.
	_, err = tx.Exec(ctx, `UPDATE email_letters SET
		    status          = $2,
		    delivered_at    = $3,
		    proxy_opened_at = $4,
		    opened_at       = $5,
		    failed_at       = $6,
		    failure_event   = $7,
		    failure_reason  = $8,
		    clicked_at      = $9,
		    updated_at      = now()
		WHERE id = $1`,
		letterID, d.Status, d.DeliveredAt, d.ProxyOpenedAt, d.OpenedAt, d.FailedAt, failEvent, failReason, d.ClickedAt)
	return err
}
