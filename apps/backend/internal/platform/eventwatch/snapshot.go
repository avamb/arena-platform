// Package eventwatch tells the operator when an event is published for the
// first time and when a published event changes what a buyer sees: its name,
// its poster, its dates and its prices (owner decision 2026-10-07, migration
// 0124). The message goes only to the operator's Telegram group, through the
// sales bot (salesnotify), never to an organization's own group.
//
// It compares instead of listening. The Telegram bot saves an event through
// the event-bundle import, which raises v1.event.published once and nothing
// for a later rename, a moved date, a new price or a new poster, and a price
// edit through the API raises nothing either, so the outbox cannot say "this
// changed". The job events.change_watch therefore builds a Snapshot of every
// published event on each run, compares it with the snapshot announced last
// (event_watch_snapshots) and announces the difference — whichever path wrote
// the change. A difference is announced only after it stayed the same for a
// while (StableFor): an event is saved date by date and a half-saved event
// must not be announced twice.
//
// It is strictly read-only on the business tables; it writes only its own
// two (event_watch_snapshots, event_watch_state). The first run records the
// events that already exist without announcing them.
package eventwatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// WindowSnap is one price window of a category (ticket_tier_prices).
type WindowSnap struct {
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	Price int64  `json:"price"`
}

// TierSnap is one ticket category of one date and what it costs.
type TierSnap struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Mode     string       `json:"mode"`
	Price    int64        `json:"price"`
	Currency string       `json:"currency"`
	Windows  []WindowSnap `json:"windows,omitempty"`
}

// SessionSnap is one date of an event.
type SessionSnap struct {
	ID        string `json:"id"`
	Start     string `json:"start"`
	Cancelled bool   `json:"cancelled,omitempty"`
	// TZ is the venue's zone, kept only to write the time the way the buyer
	// reads it. It is not part of the digest: a venue's zone changing is not
	// something to announce.
	TZ    string     `json:"tz,omitempty"`
	Tiers []TierSnap `json:"tiers"`
}

// Snapshot is everything about an event the operator is told about.
type Snapshot struct {
	Name     string        `json:"name"`
	Poster   string        `json:"poster,omitempty"`
	Sessions []SessionSnap `json:"sessions"`
}

// Event is a published event with the snapshot and the facts a message needs
// around it (organization name, the buyers' page).
type Event struct {
	ID       string
	OrgID    string
	OrgName  string
	Slug     string
	PageSlug string // the promoter's page when it has one, else the organization's
	Snapshot Snapshot
}

// Digest is a stable fingerprint of the tracked part of a snapshot.
func Digest(s Snapshot) string {
	c := s
	c.Sessions = make([]SessionSnap, len(s.Sessions))
	for i, ses := range s.Sessions {
		ses.TZ = ""
		c.Sessions[i] = ses
	}
	raw, _ := json.Marshal(c)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Querier is what Load reads through: a pool or a transaction.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	eventsSQL = `
		SELECT e.id::text, e.org_id::text, org.name, e.name,
		       COALESCE(e.poster_media_id::text, NULLIF(e.image_url, ''), ''),
		       COALESCE(e.slug, ''),
		       COALESCE(NULLIF(p.slug, ''), org.slug, '')
		  FROM events e
		  JOIN organizations org ON org.id = e.org_id AND org.deleted_at IS NULL
		  LEFT JOIN event_promoters ep ON ep.event_id = e.id
		  LEFT JOIN org_promoters p ON p.id = ep.promoter_id AND p.archived_at IS NULL
		 WHERE e.status = 'published' AND e.deleted_at IS NULL
		 ORDER BY e.created_at, e.id`

	sessionsSQL = `
		SELECT s.id::text, s.event_id::text, s.start_at, s.status, COALESCE(v.timezone, '')
		  FROM sessions s
		  JOIN events e ON e.id = s.event_id AND e.status = 'published' AND e.deleted_at IS NULL
		  LEFT JOIN venues v ON v.id = s.venue_id
		 WHERE s.deleted_at IS NULL
		 ORDER BY s.start_at, s.id`

	tiersSQL = `
		SELECT t.id::text, t.session_id::text, t.name, t.pricing_mode, t.price_amount, t.currency
		  FROM ticket_tiers t
		  JOIN sessions s ON s.id = t.session_id AND s.deleted_at IS NULL
		  JOIN events e ON e.id = s.event_id AND e.status = 'published' AND e.deleted_at IS NULL
		 WHERE t.deleted_at IS NULL
		 ORDER BY t.sort_order, t.name, t.id`

	windowsSQL = `
		SELECT w.tier_id::text, w.valid_from, w.valid_to, w.price_amount
		  FROM ticket_tier_prices w
		  JOIN ticket_tiers t ON t.id = w.tier_id AND t.deleted_at IS NULL
		  JOIN sessions s ON s.id = t.session_id AND s.deleted_at IS NULL
		  JOIN events e ON e.id = s.event_id AND e.status = 'published' AND e.deleted_at IS NULL
		 ORDER BY w.valid_from, w.id`
)

// Load reads every published event with its dates, categories and prices.
func Load(ctx context.Context, q Querier) ([]Event, error) {
	events, err := loadEvents(ctx, q)
	if err != nil {
		return nil, err
	}
	// sessions, then the tiers of each session, then the windows of each tier.
	sessionIndex := map[string][2]int{} // session id -> (event index, session index)
	eventIndex := map[string]int{}
	for i := range events {
		eventIndex[events[i].ID] = i
	}
	srows, err := q.Query(ctx, sessionsSQL)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: load sessions: %w", err)
	}
	for srows.Next() {
		var id, eventID, status, tz string
		var start time.Time
		if err := srows.Scan(&id, &eventID, &start, &status, &tz); err != nil {
			srows.Close()
			return nil, fmt.Errorf("eventwatch: scan session: %w", err)
		}
		ei, ok := eventIndex[eventID]
		if !ok {
			continue
		}
		ev := &events[ei]
		ev.Snapshot.Sessions = append(ev.Snapshot.Sessions, SessionSnap{
			ID: id, Start: start.UTC().Format(time.RFC3339), Cancelled: status == "cancelled", TZ: tz, Tiers: []TierSnap{},
		})
		sessionIndex[id] = [2]int{ei, len(ev.Snapshot.Sessions) - 1}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, fmt.Errorf("eventwatch: sessions: %w", err)
	}

	tierIndex := map[string][3]int{} // tier id -> (event, session, tier index)
	trows, err := q.Query(ctx, tiersSQL)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: load tiers: %w", err)
	}
	for trows.Next() {
		var t TierSnap
		var sessionID string
		if err := trows.Scan(&t.ID, &sessionID, &t.Name, &t.Mode, &t.Price, &t.Currency); err != nil {
			trows.Close()
			return nil, fmt.Errorf("eventwatch: scan tier: %w", err)
		}
		pos, ok := sessionIndex[sessionID]
		if !ok {
			continue
		}
		ses := &events[pos[0]].Snapshot.Sessions[pos[1]]
		ses.Tiers = append(ses.Tiers, t)
		tierIndex[t.ID] = [3]int{pos[0], pos[1], len(ses.Tiers) - 1}
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return nil, fmt.Errorf("eventwatch: tiers: %w", err)
	}

	wrows, err := q.Query(ctx, windowsSQL)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: load price windows: %w", err)
	}
	for wrows.Next() {
		var tierID string
		var from time.Time
		var to *time.Time
		var price int64
		if err := wrows.Scan(&tierID, &from, &to, &price); err != nil {
			wrows.Close()
			return nil, fmt.Errorf("eventwatch: scan price window: %w", err)
		}
		pos, ok := tierIndex[tierID]
		if !ok {
			continue
		}
		w := WindowSnap{From: from.UTC().Format(time.RFC3339), Price: price}
		if to != nil {
			w.To = to.UTC().Format(time.RFC3339)
		}
		tier := &events[pos[0]].Snapshot.Sessions[pos[1]].Tiers[pos[2]]
		tier.Windows = append(tier.Windows, w)
	}
	wrows.Close()
	if err := wrows.Err(); err != nil {
		return nil, fmt.Errorf("eventwatch: price windows: %w", err)
	}

	for i := range events {
		ss := events[i].Snapshot.Sessions
		if ss == nil {
			events[i].Snapshot.Sessions = []SessionSnap{}
		}
		sort.SliceStable(events[i].Snapshot.Sessions, func(a, b int) bool {
			x, y := events[i].Snapshot.Sessions[a], events[i].Snapshot.Sessions[b]
			if x.Start != y.Start {
				return x.Start < y.Start
			}
			return x.ID < y.ID
		})
	}
	return events, nil
}

func loadEvents(ctx context.Context, q Querier) ([]Event, error) {
	rows, err := q.Query(ctx, eventsSQL)
	if err != nil {
		return nil, fmt.Errorf("eventwatch: load events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.OrgID, &e.OrgName, &e.Snapshot.Name, &e.Snapshot.Poster, &e.Slug, &e.PageSlug); err != nil {
			return nil, fmt.Errorf("eventwatch: scan event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("eventwatch: events: %w", err)
	}
	return out, nil
}
