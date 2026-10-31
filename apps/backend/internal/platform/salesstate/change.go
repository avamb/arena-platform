package salesstate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The catalog notification the WordPress sites react to. The strings are the
// contract of hscanner.PublishCatalogEvent and bil24wire (v1.session.updated
// becomes the sites' event.changed); they are repeated here because a
// platform package must not import the HTTP layer.
const (
	sessionUpdatedEventType = "v1.session.updated"
	eventAggregateType      = "event"
)

// Refusals of Change.
var (
	// ErrNotFound — no such session, a deleted one, or one of another
	// organization (the caller answers 404 for all three).
	ErrNotFound = errors.New("salesstate: session not found")
	// ErrCancelled — a cancelled session has no sales to open or close.
	ErrCancelled = errors.New("salesstate: session is cancelled")
	// ErrInvalidState — not open / soon / sold_out.
	ErrInvalidState = errors.New("salesstate: invalid state")
)

// Actor is who asks for the change, as the journal records it.
type Actor struct {
	Type string // "user", "api_key", "system"
	ID   string
	Name string
	Role string // membership role at the moment of the change
}

// ChangeRequest asks for one session's state.
type ChangeRequest struct {
	OrgID     uuid.UUID
	SessionID uuid.UUID
	State     string // open / soon / sold_out
	Source    string // the client: event_center_web, telegram_bot, site_plugin, api, ...
	Actor     Actor
}

// ChangeResult is what Change did. Changed is false when the session already
// had the asked state: nothing was written (a repeated press is harmless).
type ChangeResult struct {
	Changed       bool
	EventID       uuid.UUID
	OldState      string // open / soon / sold_out
	NewState      string
	ChangeID      uuid.UUID
	ChangedAt     time.Time
	OutboxEventID uuid.UUID
}

// Change sets or lifts the organizer's state of a session inside the
// caller's transaction (q must be bound to it). It locks the session row,
// writes the journal row and the v1.session.updated outbox row that tells
// the sites, and stores or deletes the state. It never touches places, sale
// windows or the sales end.
func Change(ctx context.Context, q *gen.Queries, req ChangeRequest) (ChangeResult, error) {
	if !ValidRequested(req.State) {
		return ChangeResult{}, ErrInvalidState
	}
	target, err := q.LockSessionForSalesState(ctx, req.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ChangeResult{}, ErrNotFound
		}
		return ChangeResult{}, fmt.Errorf("salesstate: lock session: %w", err)
	}
	if target.OrgID != req.OrgID {
		return ChangeResult{}, ErrNotFound
	}
	if target.Status == "cancelled" {
		return ChangeResult{}, ErrCancelled
	}
	old := target.Manual
	if old == "" {
		old = Open
	}
	res := ChangeResult{EventID: target.EventID, OldState: old, NewState: req.State}
	if old == req.State {
		return res, nil
	}

	outboxID, err := q.InsertSalesStateOutboxEvent(ctx, eventAggregateType, target.EventID.String(), sessionUpdatedEventType,
		map[string]any{
			"event_id":    target.EventID.String(),
			"org_id":      target.OrgID.String(),
			"session_ids": []string{target.SessionID.String()},
		})
	if err != nil {
		return ChangeResult{}, fmt.Errorf("salesstate: outbox: %w", err)
	}
	source := req.Source
	if source == "" {
		source = SourceAPI
	}
	actorType := req.Actor.Type
	if actorType == "" {
		actorType = "system"
	}
	changeID, at, err := q.InsertSalesStateChange(ctx, gen.InsertSalesStateChangeParams{
		SessionID: target.SessionID, EventID: target.EventID, OrgID: target.OrgID,
		OldState: old, NewState: req.State, Source: source,
		ActorType: actorType, ActorID: req.Actor.ID, ActorName: req.Actor.Name, ActorRole: req.Actor.Role,
		OutboxEventID: &outboxID,
	})
	if err != nil {
		return ChangeResult{}, fmt.Errorf("salesstate: journal: %w", err)
	}
	if req.State == Open {
		err = q.ClearSessionSalesState(ctx, target.SessionID)
	} else {
		err = q.SetSessionSalesState(ctx, target.SessionID, req.State, at, changeID)
	}
	if err != nil {
		return ChangeResult{}, fmt.Errorf("salesstate: store: %w", err)
	}
	res.Changed = true
	res.ChangeID = changeID
	res.ChangedAt = at
	res.OutboxEventID = outboxID
	return res, nil
}

// FactsFromRow converts the batched facts query row.
func FactsFromRow(r gen.SessionSalesFactsRow) Facts {
	return Facts{
		Status:      r.Status,
		SalesEnd:    r.SalesEndAt,
		Categories:  int(r.Categories),
		TotalPlaces: int(r.TotalPlaces),
		FreePlaces:  int(r.FreePlaces),
		Selling:     r.Selling,
		OpensLater:  r.OpensLater,
	}
}

// Site delivery of a change, as the event center shows it.
const (
	SiteNone      = "none"      // the event is sold through no WordPress site
	SitePending   = "pending"   // the sites have not been told yet
	SiteRetrying  = "retrying"  // a delivery failed and is being retried
	SiteDelivered = "delivered" // every site took the notification
	SiteFailed    = "failed"    // retries are exhausted
)

// SiteDelivery reads the delivery of a journal row's outbox event. sites is
// how many WordPress sites sell the event.
func SiteDelivery(r gen.SalesStateChangeRow, sites int) string {
	switch {
	case sites == 0:
		return SiteNone
	case r.OutboxEventID == nil:
		return SitePending
	case r.OutboxProcessed != nil:
		return SiteDelivered
	case r.OutboxDead != nil:
		return SiteFailed
	case r.OutboxAttempts > 0:
		return SiteRetrying
	default:
		return SitePending
	}
}
