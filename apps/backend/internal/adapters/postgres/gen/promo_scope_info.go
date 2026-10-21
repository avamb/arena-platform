// Hand-maintained typed query wrapper (promo scope clarity).
// source: promo_scope_info.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PromoScopeSessionRow is what an organizer needs to read a promo code's
// scope: the session's event name, dates, sale end and venue zone.
type PromoScopeSessionRow struct {
	SessionID     uuid.UUID
	EventID       uuid.UUID
	EventName     string
	EventStatus   string
	EventDeleted  bool
	StartAt       time.Time
	SalesEndAt    time.Time
	SessionStatus string
	Deleted       bool
	Timezone      string
	// SalesStartAt is the earliest sale_window_start of the session's open
	// categories; nil when none of them has one.
	SalesStartAt *time.Time
}

const listPromoScopeSessions = `-- name: ListPromoScopeSessions :many
SELECT s.id, e.id, COALESCE(e.name, ''), e.status, e.deleted_at IS NOT NULL,
       s.start_at, s.sales_end_at, s.status, s.deleted_at IS NOT NULL,
       COALESCE(NULLIF(v.timezone, ''), 'UTC'),
       (SELECT min(t.sale_window_start) FROM ticket_tiers t WHERE t.session_id = s.id AND t.is_open)
FROM sessions s
JOIN events e ON e.id = s.event_id
LEFT JOIN venues v ON v.id = s.venue_id
WHERE e.org_id = $1 AND s.id::text = ANY($2::text[])
ORDER BY s.start_at, s.id`

// ListPromoScopeSessions reads the named sessions of ONE organization (a
// session of another organization is simply absent), deleted and cancelled
// ones included, so the caller can warn about them.
func (q *Queries) ListPromoScopeSessions(ctx context.Context, orgID uuid.UUID, sessionIDs []string) ([]PromoScopeSessionRow, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := q.db.Query(ctx, listPromoScopeSessions, orgID, sessionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PromoScopeSessionRow
	for rows.Next() {
		var r PromoScopeSessionRow
		if err := rows.Scan(&r.SessionID, &r.EventID, &r.EventName, &r.EventStatus, &r.EventDeleted,
			&r.StartAt, &r.SalesEndAt, &r.SessionStatus, &r.Deleted, &r.Timezone, &r.SalesStartAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
