package marketing

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Subscriber is one row of the organizer's list.
type Subscriber struct {
	ID               uuid.UUID
	Email            string
	Name             string
	Locale           string
	Status           string
	GrantedAt        time.Time
	ConfirmedAt      *time.Time
	WithdrawnAt      *time.Time
	UnsubscribeToken string
}

// ListFilter narrows the organizer's list. Status "" means confirmed only:
// an address that never confirmed is not the organizer's to write to.
type ListFilter struct {
	Status string // "", "confirmed", "pending", "withdrawn", "all"
	Limit  int
	Offset int
}

// List answers one page of the organization's subscribers, newest grant
// first, and how many there are in total.
func List(ctx context.Context, db DB, orgID uuid.UUID, f ListFilter) ([]Subscriber, int, error) {
	status := f.Status
	if status == "" {
		status = StatusConfirmed
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	var total int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM marketing_subscriptions
		WHERE  org_id = $1 AND ($2 = 'all' OR status = $2)`, orgID, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.Query(ctx, `
		SELECT id, email, COALESCE(buyer_name, ''), locale, status, granted_at, confirmed_at, withdrawn_at, unsubscribe_token
		FROM   marketing_subscriptions
		WHERE  org_id = $1 AND ($2 = 'all' OR status = $2)
		ORDER  BY granted_at DESC, id DESC
		LIMIT  $3 OFFSET $4`, orgID, status, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.ID, &s.Email, &s.Name, &s.Locale, &s.Status, &s.GrantedAt,
			&s.ConfirmedAt, &s.WithdrawnAt, &s.UnsubscribeToken); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// ValidStatusFilter reports whether a status filter is one List knows.
func ValidStatusFilter(s string) bool {
	switch s {
	case "", StatusConfirmed, StatusPending, StatusWithdrawn, "all":
		return true
	}
	return false
}

// WithdrawPath is the API path of the withdraw-only link.
func WithdrawPath(token string) string {
	return "/v1/public/marketing/withdraw?token=" + token
}

// ConfirmPath is the API path of the confirmation link.
func ConfirmPath(token string) string {
	return "/v1/public/marketing/confirm?token=" + token
}
