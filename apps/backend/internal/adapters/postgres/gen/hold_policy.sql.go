// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: hold_policy.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const holdPolicyProfileColumns = `name, select_seconds, payment_seconds, extend_seconds, extend_count, paying_seconds,
       max_seats_per_hold, max_active_per_device, max_active_per_ip, rehold_pause_seconds,
       poll_interval_ms, updated_at, updated_by`

// HoldPolicyProfile is one row of hold_policy_profiles (migration 0225,
// spec 67 section 7.1): the seat hold times and limits of one profile.
type HoldPolicyProfile struct {
	Name               string     `json:"name"`
	SelectSeconds      int32      `json:"select_seconds"`
	PaymentSeconds     int32      `json:"payment_seconds"`
	ExtendSeconds      int32      `json:"extend_seconds"`
	ExtendCount        int32      `json:"extend_count"`
	PayingSeconds      int32      `json:"paying_seconds"`
	MaxSeatsPerHold    int32      `json:"max_seats_per_hold"`
	MaxActivePerDevice int32      `json:"max_active_per_device"`
	MaxActivePerIP     int32      `json:"max_active_per_ip"`
	ReholdPauseSeconds int32      `json:"rehold_pause_seconds"`
	PollIntervalMs     int32      `json:"poll_interval_ms"`
	UpdatedAt          time.Time  `json:"updated_at"`
	UpdatedBy          *uuid.UUID `json:"updated_by"`
}

type holdPolicyRowScanner interface {
	Scan(dest ...any) error
}

func scanHoldPolicyProfile(r holdPolicyRowScanner) (HoldPolicyProfile, error) {
	var p HoldPolicyProfile
	err := r.Scan(&p.Name, &p.SelectSeconds, &p.PaymentSeconds, &p.ExtendSeconds, &p.ExtendCount,
		&p.PayingSeconds, &p.MaxSeatsPerHold, &p.MaxActivePerDevice, &p.MaxActivePerIP,
		&p.ReholdPauseSeconds, &p.PollIntervalMs, &p.UpdatedAt, &p.UpdatedBy)
	return p, err
}

const listHoldPolicyProfiles = `-- name: ListHoldPolicyProfiles :many
SELECT ` + holdPolicyProfileColumns + `
FROM   hold_policy_profiles
ORDER  BY name`

// ListHoldPolicyProfiles returns every hold policy profile ordered by name.
func (q *Queries) ListHoldPolicyProfiles(ctx context.Context) ([]HoldPolicyProfile, error) {
	rows, err := q.db.Query(ctx, listHoldPolicyProfiles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HoldPolicyProfile
	for rows.Next() {
		p, err := scanHoldPolicyProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const getHoldPolicyProfile = `-- name: GetHoldPolicyProfile :one
SELECT ` + holdPolicyProfileColumns + `
FROM   hold_policy_profiles
WHERE  name = $1`

// GetHoldPolicyProfile returns one profile by name; pgx.ErrNoRows when absent.
func (q *Queries) GetHoldPolicyProfile(ctx context.Context, name string) (HoldPolicyProfile, error) {
	return scanHoldPolicyProfile(q.db.QueryRow(ctx, getHoldPolicyProfile, name))
}

const getSessionHoldPolicyProfile = `-- name: GetSessionHoldPolicyProfile :one
SELECT p.name, p.select_seconds, p.payment_seconds, p.extend_seconds, p.extend_count, p.paying_seconds,
       p.max_seats_per_hold, p.max_active_per_device, p.max_active_per_ip, p.rehold_pause_seconds,
       p.poll_interval_ms, p.updated_at, p.updated_by
FROM   sessions s
JOIN   events e ON e.id = s.event_id
JOIN   hold_policy_profiles p ON p.name = COALESCE(e.hold_profile, 'standard')
WHERE  s.id = $1`

// GetSessionHoldPolicyProfile returns the profile of the session's event in
// one round trip (an event without its own profile reads standard);
// pgx.ErrNoRows when the session does not exist.
func (q *Queries) GetSessionHoldPolicyProfile(ctx context.Context, sessionID uuid.UUID) (HoldPolicyProfile, error) {
	return scanHoldPolicyProfile(q.db.QueryRow(ctx, getSessionHoldPolicyProfile, sessionID))
}

const updateHoldPolicyProfile = `-- name: UpdateHoldPolicyProfile :one
UPDATE hold_policy_profiles
SET    select_seconds        = $2,
       payment_seconds       = $3,
       extend_seconds        = $4,
       extend_count          = $5,
       paying_seconds        = $6,
       max_seats_per_hold    = $7,
       max_active_per_device = $8,
       max_active_per_ip     = $9,
       rehold_pause_seconds  = $10,
       poll_interval_ms      = $11,
       updated_by            = $12,
       updated_at            = now()
WHERE  name = $1
RETURNING ` + holdPolicyProfileColumns

// UpdateHoldPolicyProfileParams are the new values of one profile.
type UpdateHoldPolicyProfileParams struct {
	Name               string
	SelectSeconds      int32
	PaymentSeconds     int32
	ExtendSeconds      int32
	ExtendCount        int32
	PayingSeconds      int32
	MaxSeatsPerHold    int32
	MaxActivePerDevice int32
	MaxActivePerIP     int32
	ReholdPauseSeconds int32
	PollIntervalMs     int32
	UpdatedBy          *uuid.UUID
}

// UpdateHoldPolicyProfile replaces the values of one profile and returns the
// new row; pgx.ErrNoRows when the profile does not exist, a 23514 when a
// value breaks a CHECK of migration 0225.
func (q *Queries) UpdateHoldPolicyProfile(ctx context.Context, arg UpdateHoldPolicyProfileParams) (HoldPolicyProfile, error) {
	return scanHoldPolicyProfile(q.db.QueryRow(ctx, updateHoldPolicyProfile,
		arg.Name, arg.SelectSeconds, arg.PaymentSeconds, arg.ExtendSeconds, arg.ExtendCount,
		arg.PayingSeconds, arg.MaxSeatsPerHold, arg.MaxActivePerDevice, arg.MaxActivePerIP,
		arg.ReholdPauseSeconds, arg.PollIntervalMs, arg.UpdatedBy))
}

const getEventHoldProfile = `-- name: GetEventHoldProfile :one
SELECT hold_profile FROM events WHERE id = $1`

// GetEventHoldProfile returns the event's own profile name, nil when the
// event uses standard; pgx.ErrNoRows when the event does not exist.
func (q *Queries) GetEventHoldProfile(ctx context.Context, eventID uuid.UUID) (*string, error) {
	var name *string
	err := q.db.QueryRow(ctx, getEventHoldProfile, eventID).Scan(&name)
	return name, err
}

const setEventHoldProfile = `-- name: SetEventHoldProfile :execrows
UPDATE events SET hold_profile = $2 WHERE id = $1`

// SetEventHoldProfile sets the event's profile (nil returns it to standard)
// and reports the rows changed: 0 means no such event. updated_at is left
// alone, the hold profile is not event content.
func (q *Queries) SetEventHoldProfile(ctx context.Context, eventID uuid.UUID, profile *string) (int64, error) {
	ct, err := q.db.Exec(ctx, setEventHoldProfile, eventID, profile)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
