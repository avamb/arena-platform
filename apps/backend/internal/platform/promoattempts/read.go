package promoattempts

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Filter narrows a listing. OrgID is always required; the rest is optional.
type Filter struct {
	OrgID       uuid.UUID
	PromoCodeID uuid.UUID // uuid.Nil = any
	Code        string    // case-insensitive exact match of what was typed
	Result      string    // "" = any
	Reason      string    // "" = any
	From, To    *time.Time
}

// Item is one stored attempt as the API shows it.
type Item struct {
	ID               uuid.UUID
	CreatedAt        time.Time
	ChannelID        *uuid.UUID
	Surface          string
	CodeEntered      string
	PromoCodeID      *uuid.UUID
	SessionIDs       []uuid.UUID
	Result           string
	Reason           *string
	CustomerID       *uuid.UUID
	GatewaySessionID *uuid.UUID
	// ClientRef is the first 12 characters of the salted address hash: it
	// groups attempts of one client without revealing the address.
	ClientRef *string
}

const whereSQL = `
 WHERE org_id = $1
   AND ($2::uuid IS NULL OR promo_code_id = $2)
   AND ($3::text = '' OR lower(code_entered) = lower($3))
   AND ($4::text = '' OR result = $4)
   AND ($5::text = '' OR reason = $5)
   AND ($6::timestamptz IS NULL OR created_at >= $6)
   AND ($7::timestamptz IS NULL OR created_at < $7)`

func (f Filter) args() []any {
	var pc *uuid.UUID
	if f.PromoCodeID != uuid.Nil {
		pc = &f.PromoCodeID
	}
	return []any{f.OrgID, pc, f.Code, f.Result, f.Reason, f.From, f.To}
}

// List returns one page (newest first) and the total number of matches.
func List(ctx context.Context, pool *pgxpool.Pool, f Filter, limit, offset int) ([]Item, int64, error) {
	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM promo_code_attempts`+whereSQL, f.args()...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args := append(f.args(), limit, offset)
	rows, err := pool.Query(ctx, `
SELECT id, created_at, channel_id, surface, code_entered, promo_code_id, session_ids,
       result, reason, customer_id, gateway_session_id, left(ip_hash, 12)
  FROM promo_code_attempts`+whereSQL+`
 ORDER BY created_at DESC, id DESC LIMIT $8 OFFSET $9`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.CreatedAt, &it.ChannelID, &it.Surface, &it.CodeEntered,
			&it.PromoCodeID, &it.SessionIDs, &it.Result, &it.Reason, &it.CustomerID,
			&it.GatewaySessionID, &it.ClientRef); err != nil {
			return nil, 0, err
		}
		if it.SessionIDs == nil {
			it.SessionIDs = []uuid.UUID{}
		}
		items = append(items, it)
	}
	return items, total, rows.Err()
}

// ReasonCount is one line of the top-reasons list.
type ReasonCount struct {
	Reason string
	Count  int64
}

// Summary is the roll-up of a filter.
type Summary struct {
	Total, Applied, Rejected int64
	TopReasons               []ReasonCount
	FirstAttemptAt           *time.Time
	LastAttemptAt            *time.Time
}

// Summarize counts the attempts that match f.
func Summarize(ctx context.Context, pool *pgxpool.Pool, f Filter) (Summary, error) {
	var s Summary
	err := pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE result = 'applied'),
       count(*) FILTER (WHERE result = 'rejected'), min(created_at), max(created_at)
  FROM promo_code_attempts`+whereSQL, f.args()...).
		Scan(&s.Total, &s.Applied, &s.Rejected, &s.FirstAttemptAt, &s.LastAttemptAt)
	if err != nil {
		return s, err
	}
	rows, err := pool.Query(ctx, `
SELECT reason, count(*) AS n FROM promo_code_attempts`+whereSQL+` AND reason IS NOT NULL
 GROUP BY reason ORDER BY n DESC, reason LIMIT 5`, f.args()...)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	s.TopReasons = []ReasonCount{}
	for rows.Next() {
		var rc ReasonCount
		if err := rows.Scan(&rc.Reason, &rc.Count); err != nil {
			return s, err
		}
		s.TopReasons = append(s.TopReasons, rc)
	}
	return s, rows.Err()
}
