package refunds

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UnattributedForTicket is the money already returned on the payment the
// ticket was sold with by live refunds that name no ticket (a partial refund
// made in the provider dashboard, a flat refund of the payment). It is what a
// review hold was put there for: lifting the hold without deciding which
// ticket it covers lets the ticket be refunded in full on top of it.
func UnattributedForTicket(ctx context.Context, q pgx.Tx, ticketID uuid.UUID) (int64, string, error) {
	var (
		amount   int64
		currency string
	)
	err := q.QueryRow(ctx, `SELECT COALESCE(SUM(r.amount), 0)::bigint, COALESCE(MAX(pi.currency), '')
		FROM tickets t
		JOIN payment_intents pi ON pi.checkout_session_id = t.checkout_session_id
		LEFT JOIN refunds r ON r.payment_intent_id = pi.id AND r.ticket_id IS NULL AND r.cancelled_ticket_id IS NULL
		     AND r.state NOT IN ('failed', 'rejected')
		     AND NOT (r.state = 'manual_review' AND r.provider_attempts = 0 AND r.provider_refund_id IS NULL AND r.provider IS NOT NULL)
		WHERE t.id = $1`, ticketID).Scan(&amount, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", nil
	}
	return amount, currency, err
}
