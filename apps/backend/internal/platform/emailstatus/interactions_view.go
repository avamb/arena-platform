package emailstatus

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// InteractionView is one open, proxy open or click of a letter, as the
// dispute evidence shows it. UserAgent is personal data (the reader's mail
// client or browser string): it is read only through the order's
// letter-interactions route, never logged.
type InteractionView struct {
	// Event is opened, proxy_open or click.
	Event string `json:"event"`
	// OccurredAt is when the provider says it happened (RFC 3339, UTC).
	OccurredAt string `json:"occurred_at"`
	// Link is the clicked link, host and path only (CleanLink); null for an
	// open.
	Link *string `json:"link"`
	// UserAgent is the reader's user agent as Brevo reported it; null when
	// the event carried none.
	UserAgent *string `json:"user_agent"`
}

// LetterInteractionsView is one letter of an order with every interaction
// the provider reported on it, oldest first.
type LetterInteractionsView struct {
	LetterID     string            `json:"letter_id"`
	Kind         string            `json:"kind"`
	SentAt       string            `json:"sent_at"`
	Interactions []InteractionView `json:"interactions"`
}

// orderLetterInteractionsSQL lists the interactions of the order's letters,
// scoped to the organization the order belongs to. A letter without any
// interaction is listed with none (LEFT JOIN), so the evidence shows which
// letters were never opened too.
const orderLetterInteractionsSQL = `
SELECT l.id, l.kind, l.sent_at, i.event, i.occurred_at, i.link, i.user_agent
FROM   email_letters l
LEFT JOIN email_letter_interactions i ON i.letter_id = l.id
WHERE  l.order_id = $1 AND l.org_id = $2 AND l.status <> 'sending'
ORDER  BY l.sent_at, l.id, i.occurred_at, i.id`

// OrderLetterInteractions reads every open, proxy open and click of the
// order's letters (ticket, change, cancel and refund letters), grouped by
// letter. orgID is the organization the order was loaded in: a letter of
// another organization is never returned.
func OrderLetterInteractions(ctx context.Context, db DB, orderID, orgID uuid.UUID) ([]LetterInteractionsView, error) {
	rows, err := db.Query(ctx, orderLetterInteractionsSQL, orderID, orgID)
	if err != nil {
		return nil, fmt.Errorf("emailstatus: letter interactions: %w", err)
	}
	defer rows.Close()
	out := []LetterInteractionsView{}
	idx := map[uuid.UUID]int{}
	for rows.Next() {
		var (
			id         uuid.UUID
			kind       string
			sentAt     time.Time
			event      *string
			occurredAt *time.Time
			link, ua   *string
		)
		if err := rows.Scan(&id, &kind, &sentAt, &event, &occurredAt, &link, &ua); err != nil {
			return nil, fmt.Errorf("emailstatus: scan letter interaction: %w", err)
		}
		i, ok := idx[id]
		if !ok {
			out = append(out, LetterInteractionsView{
				LetterID: id.String(), Kind: kind, SentAt: sentAt.UTC().Format(time.RFC3339),
				Interactions: []InteractionView{},
			})
			i = len(out) - 1
			idx[id] = i
		}
		if event != nil && occurredAt != nil {
			out[i].Interactions = append(out[i].Interactions, InteractionView{
				Event: *event, OccurredAt: occurredAt.UTC().Format(time.RFC3339), Link: link, UserAgent: ua,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("emailstatus: letter interactions: %w", err)
	}
	return out, nil
}
