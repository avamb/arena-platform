package disputes

import (
	"context"

	"github.com/google/uuid"
)

// Summary is what a list row and a card show next to the dispute: the order
// number, the event and the buyer.
type Summary struct {
	OrderNumber int64
	EventName   string
	SessionAt   string
	Timezone    string
	BuyerName   string
	BuyerEmail  string
}

// Summaries reads the summaries of disputes of one organization in one query.
func (s *Service) Summaries(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]Summary, error) {
	out := map[uuid.UUID]Summary{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT d.id, COALESCE(o.system_id, 0), COALESCE(e.name, ''),
			COALESCE(to_char(s.start_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
			COALESCE(v.timezone, ''), COALESCE(o.buyer_name, ''), COALESCE(o.buyer_email, '')
		FROM payment_disputes d
		LEFT JOIN orders o ON o.id = d.order_id
		LEFT JOIN sessions s ON s.id = o.session_id
		LEFT JOIN events e ON e.id = s.event_id
		LEFT JOIN venues v ON v.id = s.venue_id
		WHERE d.org_id = $1 AND d.id = ANY($2)`, orgID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var sm Summary
		if err := rows.Scan(&id, &sm.OrderNumber, &sm.EventName, &sm.SessionAt, &sm.Timezone, &sm.BuyerName, &sm.BuyerEmail); err != nil {
			return nil, err
		}
		out[id] = sm
	}
	return out, rows.Err()
}
