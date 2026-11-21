// Package ticketreissue replaces the tickets of a paid order whose letter went
// to a wrong address (spec 63, variant B).
//
// Every active ticket of the order is closed (status 'transferred',
// cancellation_reason 'reissued', barcodes and credentials revoked) and a new
// ticket takes the same seat in the same order: same checkout session,
// session, category, seat, VIP mark and holder name, a new system_ticket_id
// and a new EAN-13. The order line (order_items.ticket_id) moves to the new
// ticket; money, places and the inventory ledger are not touched. The purchase
// token changes so the links of the first letter stop working.
//
// The order's buyer e-mail changes only after the relay accepted the letter
// with the new tickets (AfterLetter, called by the delivery worker): until
// then the operation is 'pending'. The journal (ticket_reissues,
// ticket_reissue_items) keeps both addresses as evidence.
//
// The package never imports the delivery package: the caller queues the
// letter through Input.Enqueue, inside the reissue transaction.
package ticketreissue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reasons an operator states (decision R-10).
const (
	ReasonWrongAddress = "sent_to_wrong_address"
	ReasonForwarded    = "forwarded_to_stranger"
	ReasonOther        = "other"
)

// States of one operation.
const (
	StatePending    = "pending"
	StateApplied    = "applied"
	StateSendFailed = "send_failed"
)

// Names written elsewhere.
const (
	// CancellationReason is tickets.cancellation_reason of a replaced ticket.
	CancellationReason = "reissued"
	// EventOldTicket is the outbox event of a replaced (closed) ticket. MACS
	// and the selling sites read it as "this number no longer admits".
	EventOldTicket = "v1.ticket.reissued"
	// EventNewTicket is the outbox event of a ticket issued by a reissue.
	// MACS reads it as an order.paid of one ticket.
	EventNewTicket = "v1.ticket.reissue_issued"
	// OrderEventType is the order_events.type of one reissue.
	OrderEventType = "tickets_reissued"
	// AppliedOrderEventType is the order_events.type of the address change.
	AppliedOrderEventType = "reissue_applied"
	// AuditAction is the audit_events.action of one reissue.
	AuditAction = "v1.order.tickets_reissue"
	// MaxNote is the longest note an operator may write.
	MaxNote = 500
	// MaxEmail is the longest address.
	MaxEmail = 254
)

// Errors the routes map to answers (spec 63 section 4.3).
var (
	ErrOrderNotFound      = errors.New("ticketreissue: order not found")
	ErrNotPaid            = errors.New("ticketreissue: order is not paid")
	ErrSellerSite         = errors.New("ticketreissue: the order belongs to a live selling site")
	ErrNothingToReissue   = errors.New("ticketreissue: the order has no active tickets")
	ErrSessionClosed      = errors.New("ticketreissue: the session is cancelled or over")
	ErrInProgress         = errors.New("ticketreissue: an earlier reissue of this order has not been sent")
	ErrKeyReused          = errors.New("ticketreissue: the idempotency key was used for another request")
	ErrReissueNotFound    = errors.New("ticketreissue: reissue not found")
	ErrNotResendable      = errors.New("ticketreissue: the reissue was already applied")
	ErrInvalidReason      = errors.New("ticketreissue: unknown reason")
	ErrNoticeTooLong      = errors.New("ticketreissue: note is too long")
	ErrTicketNotReissable = errors.New("ticketreissue: a ticket cannot be reissued")
)

// TicketRefusal names the ticket that blocks the whole operation.
type TicketRefusal struct {
	TicketID uuid.UUID
	Number   int64
	// Reason: "review_hold" or "refund_in_progress".
	Reason string
}

func (e *TicketRefusal) Error() string {
	return fmt.Sprintf("ticketreissue: ticket %d cannot be reissued: %s", e.Number, e.Reason)
}

func (e *TicketRefusal) Unwrap() error { return ErrTicketNotReissable }

// ValidReason reports whether s is a reason an operator may state.
func ValidReason(s string) bool {
	switch s {
	case ReasonWrongAddress, ReasonForwarded, ReasonOther:
		return true
	}
	return false
}

// Querier is what the read functions need: a pool, a transaction or a
// gen.DBTX.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Beginner opens the reissue transaction.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Fingerprint is what a repeat of the same request must reproduce.
func Fingerprint(orderID uuid.UUID, email, reason, note string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		orderID.String(), strings.ToLower(strings.TrimSpace(email)), reason, strings.TrimSpace(note),
	}, "\x1f")))
	return hex.EncodeToString(h[:])
}

func mintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// MaskEmail hides all but the first character of the local part and of the
// domain's first label: "anna@example.com" -> "a***@e******.com".
func MaskEmail(s string) string {
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return "***"
	}
	local, domain := s[:at], s[at+1:]
	first, rest := domain, ""
	if dot := strings.Index(domain, "."); dot > 0 {
		first, rest = domain[:dot], domain[dot:]
	}
	return maskWord(local) + "@" + maskWord(first) + rest
}

func maskWord(w string) string {
	r := []rune(w)
	if len(r) == 0 {
		return ""
	}
	return string(r[:1]) + strings.Repeat("*", len(r)-1)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
