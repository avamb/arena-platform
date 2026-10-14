package emailstatus

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RetentionAnchorSQL is the moment the retention period of a letter row "l"
// starts counting (owner decision 2026-10-14): the end of the latest session
// the letter belongs to when that is later than the letter, otherwise the
// time the letter was sent. A ticket bought a year before the show can be
// disputed months after it, so counting from the letter alone would delete
// the evidence before the dispute window closes.
//
// The letter's sessions are found through its order (ticket, change, cancel
// and refund letters), its change notice (change and cancel letters) and
// the ticket numbers its passport records (ticket and invitation letters;
// an invitation has no order). A letter tied to none of them counts from
// sent_at. GREATEST ignores the NULL of a letter without a session.
//
// MinRetention and any future purge count from this value, never from
// sent_at (runbook docs/ops/brevo_delivery_status_webhook_ru.md §5).
const RetentionAnchorSQL = `GREATEST(l.sent_at, (
	SELECT max(s.end_at) FROM sessions s
	WHERE  s.id IN (
	         SELECT o.session_id FROM orders o WHERE o.id = l.order_id
	         UNION ALL
	         SELECT c.session_id FROM session_change_notices n
	         JOIN   session_changes c ON c.id = n.change_id
	         WHERE  n.email_letter_id = l.id
	         UNION ALL
	         SELECT t.session_id FROM tickets t
	         WHERE  t.system_ticket_id IN (
	                  SELECT num::bigint FROM unnest(l.ticket_numbers) AS num
	                  WHERE  num ~ '^[0-9]{1,18}$'))))`

// RetentionStart returns the moment the retention period of a letter starts
// counting (RetentionAnchorSQL). It is kept until at least
// RetentionStart + MinRetention.
func RetentionStart(ctx context.Context, db DB, letterID uuid.UUID) (time.Time, error) {
	var at time.Time
	err := db.QueryRow(ctx, `SELECT `+RetentionAnchorSQL+` FROM email_letters l WHERE l.id = $1`, letterID).Scan(&at)
	return at, err
}
