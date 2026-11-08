// Package salesalert tells an organizer what happened to the sales of a
// session (owner decisions 2026-10-31, migration 0183):
//
//   - sold out: the organizer set "sold out" by hand, or the last place sold
//     and no unpaid hold is left (a hold may still lapse and put the place
//     back, so the storefront shows "sold out" at once but the organizer is
//     told only when it is final). A session that has no places is never
//     sold out by itself. The organization's own sales chats get a message
//     and the owners and managers an e-mail.
//   - the site has not updated: a state change the WordPress sites must hear
//     about was still not delivered after StaleAfter. The organization's chat
//     gets one message; without a chat the owners get an e-mail.
//
// The job is sales_state.watch (job.go). The e-mail is the worker job
// sales_state.alert_email, sent by delivery.NewSalesAlertHandler.
package salesalert

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Alert kinds (AlertEmailPayload.Kind, the e-mail template's variants).
const (
	KindSoldOut   = "sold_out"
	KindSiteStale = "site_stale"
)

// JobTypeAlertEmail mails the owners (and for a sell-out the managers).
const JobTypeAlertEmail = "sales_state.alert_email"

// AlertEmailPayload is the payload of JobTypeAlertEmail.
type AlertEmailPayload struct {
	Kind      string `json:"kind"`
	OrgID     string `json:"org_id"`
	SessionID string `json:"session_id"`
	// Manual: the sell-out was set by hand; Actor names who did it.
	Manual bool   `json:"manual,omitempty"`
	Actor  string `json:"actor,omitempty"`
	// State is the new state of a change the sites did not take; Sites how
	// many sites sell the event.
	State string `json:"state,omitempty"`
	Sites int    `json:"sites,omitempty"`
	// The old code of a replaced ticket (KindReissueOldCode): an entry
	// before the replacement, or a try after it (Attempt). At is RFC 3339.
	Attempt     bool   `json:"attempt,omitempty"`
	OrderNumber int64  `json:"order_number,omitempty"`
	OldNumber   int64  `json:"old_number,omitempty"`
	NewNumber   int64  `json:"new_number,omitempty"`
	At          string `json:"at,omitempty"`
	Source      string `json:"source,omitempty"`
	Result      string `json:"result,omitempty"`
	Gate        string `json:"gate,omitempty"`
}

// Querier is what the loaders read through (a pool or a transaction).
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SessionInfo is what a message says about the session.
type SessionInfo struct {
	OrgID     uuid.UUID
	OrgName   string
	OrgLocale string
	EventName string
	Start     time.Time
	Venue     string
	Timezone  string
	// TotalPlaces / FreePlaces count the places of the live main categories
	// (every category when the session has no main one, migration 0197);
	// AddonFree the free places of its add-on categories.
	TotalPlaces int64
	FreePlaces  int64
	AddonFree   int64
	// AllTotal / AllFree count every live category, add-ons included.
	AllTotal int64
	AllFree  int64
}

// ErrSessionGone: the session was deleted, or is not the organization's.
var ErrSessionGone = errors.New("salesalert: session gone")

// LoadSessionInfo reads one session of the organization.
func LoadSessionInfo(ctx context.Context, q Querier, orgID, sessionID uuid.UUID) (SessionInfo, error) {
	var in SessionInfo
	err := q.QueryRow(ctx, `
		SELECT o.id, o.name, COALESCE(o.default_locale, 'en'), e.name, s.start_at,
		       COALESCE(v.name, ''), COALESCE(v.timezone, ''),
		       COALESCE(p.main_total, 0), COALESCE(p.main_free, 0), COALESCE(p.addon_free, 0),
		       COALESCE(p.total, 0), COALESCE(p.free, 0)
		FROM   sessions s
		JOIN   events e        ON e.id = s.event_id
		JOIN   organizations o ON o.id = e.org_id
		LEFT   JOIN venues v   ON v.id = s.venue_id
		LEFT   JOIN LATERAL (
		    SELECT count(*) AS total,
		           count(*) FILTER (WHERE ss.status = 'available') AS free,
		           count(*) FILTER (WHERE NOT tt.is_addon OR NOT x.has_main) AS main_total,
		           count(*) FILTER (WHERE (NOT tt.is_addon OR NOT x.has_main) AND ss.status = 'available') AS main_free,
		           count(*) FILTER (WHERE tt.is_addon AND x.has_main AND ss.status = 'available') AS addon_free
		    FROM   session_seats ss
		    JOIN   ticket_tiers tt ON tt.id = ss.tier_id
		    CROSS  JOIN (SELECT EXISTS (SELECT 1 FROM ticket_tiers m
		                  WHERE m.session_id = s.id AND m.deleted_at IS NULL AND NOT m.is_addon) AS has_main) x
		    WHERE  ss.session_id = s.id AND tt.deleted_at IS NULL
		) p ON true
		WHERE  s.id = $1 AND e.org_id = $2 AND s.deleted_at IS NULL`, sessionID, orgID).Scan(
		&in.OrgID, &in.OrgName, &in.OrgLocale, &in.EventName, &in.Start,
		&in.Venue, &in.Timezone, &in.TotalPlaces, &in.FreePlaces, &in.AddonFree,
		&in.AllTotal, &in.AllFree)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionInfo{}, ErrSessionGone
	}
	return in, err
}

// When is the session start on the venue's clock, Madrid when the venue has
// no zone (the organizations sell in Europe), never bare UTC.
func (in SessionInfo) When() string {
	loc, err := time.LoadLocation(in.Timezone)
	if in.Timezone == "" || err != nil {
		if loc, err = time.LoadLocation("Europe/Madrid"); err != nil {
			return in.Start.UTC().Format("02.01.2006 15:04") + " UTC" // allow:timeformat
		}
	}
	return in.Start.In(loc).Format("02.01.2006 15:04") // allow:timeformat
}

// Sold is how many places are gone: every place that is not free.
func (in SessionInfo) Sold() int64 { return in.TotalPlaces - in.FreePlaces }
