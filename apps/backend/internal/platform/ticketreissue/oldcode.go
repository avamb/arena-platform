package ticketreissue

// oldcode.go — a try to enter with the code of a replaced ticket (spec 63
// section 4.8, decision of 2026-11-08, О-5). The arena scanner and the MACS
// scan intake call RecordOldCodeAttempt; the sales alert watch
// (salesalert/reissue_alerts.go) tells the organizer once per old ticket.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Attempt sources (ticket_reissue_items.old_code_attempt_source).
const (
	AttemptSourceArena = "arena_scanner"
	AttemptSourceMACS  = "macs"
)

// Attempt results (ticket_reissue_items.old_code_attempt_result).
const (
	AttemptAdmitted = "admitted"
	AttemptDenied   = "denied"
)

// Execer is a pool or a transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// maxGate bounds the scanner's gate label kept on the item.
const maxGate = 120

// RecordOldCodeAttempt stores a scan of oldTicketID's code. A scan made at or
// after the reissue is the first try after it and is kept once (the first one
// wins). A scan made before the reissue that arrives late (an offline MACS
// batch) is an entry before the reissue: an admission fills old_used_at when
// it was empty, so the organizer still hears about it. It reports whether a
// row changed; a ticket that was never replaced changes nothing.
func RecordOldCodeAttempt(ctx context.Context, db Execer, oldTicketID uuid.UUID, at time.Time, source, result, gate string) (bool, error) {
	if source != AttemptSourceArena && source != AttemptSourceMACS {
		return false, fmt.Errorf("ticketreissue: attempt source %q", source)
	}
	if result != AttemptAdmitted && result != AttemptDenied {
		return false, fmt.Errorf("ticketreissue: attempt result %q", result)
	}
	gate = strings.TrimSpace(gate)
	if r := []rune(gate); len(r) > maxGate {
		gate = string(r[:maxGate])
	}
	tag, err := db.Exec(ctx, `
		UPDATE ticket_reissue_items i
		SET    old_code_attempt_at     = CASE WHEN $2 >= r.created_at AND i.old_code_attempt_at IS NULL THEN $2 ELSE i.old_code_attempt_at END,
		       old_code_attempt_source = CASE WHEN $2 >= r.created_at AND i.old_code_attempt_at IS NULL THEN $3 ELSE i.old_code_attempt_source END,
		       old_code_attempt_result = CASE WHEN $2 >= r.created_at AND i.old_code_attempt_at IS NULL THEN $4 ELSE i.old_code_attempt_result END,
		       old_code_attempt_gate   = CASE WHEN $2 >= r.created_at AND i.old_code_attempt_at IS NULL THEN NULLIF($5, '') ELSE i.old_code_attempt_gate END,
		       old_used_at             = CASE WHEN $2 < r.created_at AND $4 = 'admitted' AND i.old_used_at IS NULL THEN $2 ELSE i.old_used_at END
		FROM   ticket_reissues r
		WHERE  r.id = i.reissue_id AND i.old_ticket_id = $1
		  AND  ((i.old_code_attempt_at IS NULL AND $2 >= r.created_at)
		        OR (i.old_used_at IS NULL AND $2 < r.created_at AND $4 = 'admitted'))`,
		oldTicketID, at.UTC(), source, result, gate)
	if err != nil {
		return false, fmt.Errorf("ticketreissue: record old-code attempt: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
