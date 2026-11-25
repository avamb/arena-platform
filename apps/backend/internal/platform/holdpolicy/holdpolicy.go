// Package holdpolicy reads the seat hold times and limits (spec
// 08_architecture/67, section 7.1, migration 0225).
//
// A profile lives in hold_policy_profiles; an event names its own profile in
// events.hold_profile (NULL = standard). The profile is read when a hold is
// created, on the pool before the transaction, and copied into
// reservations.hold_policy as the JSON of Policy, so an edit of a profile
// moves only new holds.
package holdpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// Profile names seeded by migration 0225.
const (
	ProfileStandard   = "standard"
	ProfileHighDemand = "high_demand"
)

// Policy is the hold times and limits of one profile. The JSON form (the
// reservations.hold_policy snapshot) keeps the column names and units of
// hold_policy_profiles: seconds, and milliseconds for the poll interval.
type Policy struct {
	Profile            string
	Select             time.Duration
	Payment            time.Duration
	Extend             time.Duration
	ExtendCount        int
	Paying             time.Duration
	MaxSeatsPerHold    int
	MaxActivePerDevice int
	MaxActivePerIP     int
	ReholdPause        time.Duration
	PollInterval       time.Duration
}

type policyJSON struct {
	Profile            string `json:"profile"`
	SelectSeconds      int64  `json:"select_seconds"`
	PaymentSeconds     int64  `json:"payment_seconds"`
	ExtendSeconds      int64  `json:"extend_seconds"`
	ExtendCount        int    `json:"extend_count"`
	PayingSeconds      int64  `json:"paying_seconds"`
	MaxSeatsPerHold    int    `json:"max_seats_per_hold"`
	MaxActivePerDevice int    `json:"max_active_per_device"`
	MaxActivePerIP     int    `json:"max_active_per_ip"`
	ReholdPauseSeconds int64  `json:"rehold_pause_seconds"`
	PollIntervalMs     int64  `json:"poll_interval_ms"`
}

// MarshalJSON writes the snapshot form of the policy.
func (p Policy) MarshalJSON() ([]byte, error) {
	return json.Marshal(policyJSON{
		Profile:            p.Profile,
		SelectSeconds:      int64(p.Select / time.Second),
		PaymentSeconds:     int64(p.Payment / time.Second),
		ExtendSeconds:      int64(p.Extend / time.Second),
		ExtendCount:        p.ExtendCount,
		PayingSeconds:      int64(p.Paying / time.Second),
		MaxSeatsPerHold:    p.MaxSeatsPerHold,
		MaxActivePerDevice: p.MaxActivePerDevice,
		MaxActivePerIP:     p.MaxActivePerIP,
		ReholdPauseSeconds: int64(p.ReholdPause / time.Second),
		PollIntervalMs:     int64(p.PollInterval / time.Millisecond),
	})
}

// UnmarshalJSON reads the snapshot form of the policy.
func (p *Policy) UnmarshalJSON(b []byte) error {
	var j policyJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*p = Policy{
		Profile:            j.Profile,
		Select:             time.Duration(j.SelectSeconds) * time.Second,
		Payment:            time.Duration(j.PaymentSeconds) * time.Second,
		Extend:             time.Duration(j.ExtendSeconds) * time.Second,
		ExtendCount:        j.ExtendCount,
		Paying:             time.Duration(j.PayingSeconds) * time.Second,
		MaxSeatsPerHold:    j.MaxSeatsPerHold,
		MaxActivePerDevice: j.MaxActivePerDevice,
		MaxActivePerIP:     j.MaxActivePerIP,
		ReholdPause:        time.Duration(j.ReholdPauseSeconds) * time.Second,
		PollInterval:       time.Duration(j.PollIntervalMs) * time.Millisecond,
	}
	return nil
}

// Default is the standard profile as seeded by migration 0225. It is used
// when the profile table cannot be read.
func Default() Policy {
	return Policy{
		Profile:            ProfileStandard,
		Select:             600 * time.Second,
		Payment:            900 * time.Second,
		Extend:             300 * time.Second,
		ExtendCount:        1,
		Paying:             600 * time.Second,
		MaxSeatsPerHold:    10,
		MaxActivePerDevice: 2,
		MaxActivePerIP:     6,
		ReholdPause:        300 * time.Second,
		PollInterval:       3000 * time.Millisecond,
	}
}

// MaxCartSeats is the platform limit of assigned seats in one public cart
// (hfeed checkCartCaps uses it); a hold can never be larger than a cart.
const MaxCartSeats = 100

// ErrInvalid reports a policy that breaks a CHECK of hold_policy_profiles.
var ErrInvalid = errors.New("holdpolicy: invalid policy")

// Validate mirrors the CHECK constraints of hold_policy_profiles (migration
// 0225). Seconds must be whole, the poll interval whole milliseconds.
func (p Policy) Validate() error {
	bad := func(field string, v any) error {
		return fmt.Errorf("%w: %s = %v", ErrInvalid, field, v)
	}
	whole := func(d, unit time.Duration) bool { return d%unit == 0 }
	switch {
	case p.Select < 120*time.Second || p.Select > 1800*time.Second || !whole(p.Select, time.Second):
		return bad("select", p.Select)
	case p.Payment < 300*time.Second || p.Payment > 1800*time.Second || !whole(p.Payment, time.Second):
		return bad("payment", p.Payment)
	case p.Extend < 0 || !whole(p.Extend, time.Second):
		return bad("extend", p.Extend)
	case p.ExtendCount < 0 || p.ExtendCount > 3:
		return bad("extend_count", p.ExtendCount)
	case p.Paying < 0 || !whole(p.Paying, time.Second):
		return bad("paying", p.Paying)
	case p.MaxSeatsPerHold < 1 || p.MaxSeatsPerHold > MaxCartSeats:
		return bad("max_seats_per_hold", p.MaxSeatsPerHold)
	case p.MaxActivePerDevice < 1:
		return bad("max_active_per_device", p.MaxActivePerDevice)
	case p.MaxActivePerIP < 1:
		return bad("max_active_per_ip", p.MaxActivePerIP)
	case p.ReholdPause < 0 || !whole(p.ReholdPause, time.Second):
		return bad("rehold_pause", p.ReholdPause)
	case p.PollInterval < 1000*time.Millisecond || p.PollInterval > 10000*time.Millisecond || !whole(p.PollInterval, time.Millisecond):
		return bad("poll_interval", p.PollInterval)
	}
	return nil
}

// FromRow converts a hold_policy_profiles row into a Policy.
func FromRow(r gen.HoldPolicyProfile) Policy {
	return Policy{
		Profile:            r.Name,
		Select:             time.Duration(r.SelectSeconds) * time.Second,
		Payment:            time.Duration(r.PaymentSeconds) * time.Second,
		Extend:             time.Duration(r.ExtendSeconds) * time.Second,
		ExtendCount:        int(r.ExtendCount),
		Paying:             time.Duration(r.PayingSeconds) * time.Second,
		MaxSeatsPerHold:    int(r.MaxSeatsPerHold),
		MaxActivePerDevice: int(r.MaxActivePerDevice),
		MaxActivePerIP:     int(r.MaxActivePerIP),
		ReholdPause:        time.Duration(r.ReholdPauseSeconds) * time.Second,
		PollInterval:       time.Duration(r.PollIntervalMs) * time.Millisecond,
	}
}

// Querier is the one query ForSession needs; *gen.Queries satisfies it.
type Querier interface {
	GetSessionHoldPolicyProfile(ctx context.Context, sessionID uuid.UUID) (gen.HoldPolicyProfile, error)
}

// ForSession returns the policy of the session's event: the event's own
// profile, or standard when the event names none. On any error it returns
// Default() together with the error; pgx.ErrNoRows means the session does
// not exist (or the named profile is missing), and the caller decides
// whether to go on with Default() or refuse. A stored row that breaks
// Validate is reported with ErrInvalid, also with Default().
func ForSession(ctx context.Context, q Querier, sessionID uuid.UUID) (Policy, error) {
	row, err := q.GetSessionHoldPolicyProfile(ctx, sessionID)
	if err != nil {
		return Default(), err
	}
	p := FromRow(row)
	if err := p.Validate(); err != nil {
		return Default(), err
	}
	return p, nil
}
