// session_sales_state.go — the organizer's sales state of a session
// (migration 0182, package salesstate): one state for EVERY sales channel
// (owner decision 2026-10-31).
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/sales-state          (session.read)
//	PUT /v1/organizations/{org_id}/sessions/{session_id}/sales-state          (session.update)
//	GET /v1/organizations/{org_id}/sessions/{session_id}/sales-state/history  (session.read)
//
// PUT {"state": "open" | "soon" | "sold_out"} sets or lifts the state. A
// repeat of the current state answers 200 with changed=false and writes
// nothing. A change writes the journal row and a v1.session.updated outbox
// row in one transaction; the WordPress sites react to it by re-reading the
// session (salesState in GET_ALL_ACTIONS). A session of another organization
// is 404, like a missing one.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

// maxChangedByRunes bounds the name a site may give for the person who
// switched the state on its own screen.
const maxChangedByRunes = 100

type salesStateActorOut struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Source string `json:"source"`
}

type salesStateSiteSyncOut struct {
	Status    string  `json:"status"`
	Sites     int     `json:"sites"`
	Attempts  int     `json:"attempts"`
	LastError *string `json:"last_error"`
}

type sessionSalesStateResponse struct {
	SessionID           string                `json:"session_id"`
	EventID             string                `json:"event_id"`
	Manual              *string               `json:"manual"`
	Effective           string                `json:"effective"`
	TotalPlaces         int                   `json:"total_places"`
	FreePlaces          int                   `json:"free_places"`
	HeldPlaces          int                   `json:"held_places"`
	SalesEndAt          string                `json:"sales_end_at"`
	ChangedAt           *string               `json:"changed_at"`
	ChangedBy           *salesStateActorOut   `json:"changed_by"`
	SiteSync            salesStateSiteSyncOut `json:"site_sync"`
	ExternalAllocations int                   `json:"external_allocations"`
	Changed             *bool                 `json:"changed,omitempty"`
}

type salesStateChangeOut struct {
	ID        string             `json:"id"`
	OldState  string             `json:"old_state"`
	NewState  string             `json:"new_state"`
	ChangedBy salesStateActorOut `json:"changed_by"`
	CreatedAt string             `json:"created_at"`
	SiteSync  string             `json:"site_sync"`
}

// salesStatePath parses org and session and checks the caller's membership.
func (h *Handler) salesStatePath(w http.ResponseWriter, r *http.Request) (orgID, sessionID uuid.UUID, ok bool) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return uuid.Nil, uuid.Nil, false
	}
	if orgID, ok = httputil.UUIDPathParam(w, r, "org_id"); !ok {
		return
	}
	if sessionID, ok = httputil.UUIDPathParam(w, r, "session_id"); !ok {
		return
	}
	ok = h.requireOrgMembership(w, r, h.sessionQueries, orgID)
	return orgID, sessionID, ok
}

// HandleGetSessionSalesState serves GET .../sessions/{session_id}/sales-state.
func (h *Handler) HandleGetSessionSalesState(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.salesStatePath(w, r)
	if !ok {
		return
	}
	out, err := h.loadSessionSalesState(r.Context(), h.sessionQueries, orgID, sessionID)
	if err != nil {
		h.writeSalesStateLoadError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, out)
}

// HandleSetSessionSalesState serves PUT .../sessions/{session_id}/sales-state.
func (h *Handler) HandleSetSessionSalesState(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.salesStatePath(w, r)
	if !ok {
		return
	}
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	var req struct {
		State     string `json:"state"`
		ChangedBy string `json:"changed_by"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024))
	if err != nil || json.Unmarshal(body, &req) != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"session.invalid_json", "request body is not valid JSON", r,
		))
		return
	}
	req.State = strings.TrimSpace(req.State)
	if !salesstate.ValidRequested(req.State) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_sales_state", "state must be one of open, soon, sold_out", r,
			map[string]any{"field": "state"},
		))
		return
	}
	changedBy := strings.TrimSpace(req.ChangedBy)
	if utf8.RuneCountInString(changedBy) > maxChangedByRunes {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_changed_by", "changed_by is longer than 100 characters", r,
			map[string]any{"field": "changed_by"},
		))
		return
	}

	ctx := r.Context()
	actor := h.salesStateActor(ctx, orgID, changedBy)

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.sessionQueries.WithTx(tx)

	res, err := salesstate.Change(ctx, qtx, salesstate.ChangeRequest{
		OrgID: orgID, SessionID: sessionID, State: req.State,
		Source: salesStateSource(ctx), Actor: actor,
	})
	switch {
	case errors.Is(err, salesstate.ErrNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	case errors.Is(err, salesstate.ErrCancelled):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"session.cancelled", "this session has been cancelled: there are no sales to open or close", r,
		))
		return
	case err != nil:
		h.logger.Error("session: sales state change failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.sales_state_failed", "failed to change the sales state", r,
		))
		return
	}

	if res.Changed && h.audit != nil {
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    actor.Type,
			ActorID:      actorAuditID(ctx),
			Action:       "v1.session.sales_state",
			ResourceType: "session",
			ResourceID:   sessionID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"event_id":  res.EventID.String(),
				"old_state": res.OldState,
				"new_state": res.NewState,
				"change_id": res.ChangeID.String(),
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			h.logger.Error("session: sales state audit failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"session.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	out, err := h.loadSessionSalesState(ctx, h.sessionQueries, orgID, sessionID)
	if err != nil {
		h.writeSalesStateLoadError(w, r, err)
		return
	}
	changed := res.Changed
	out.Changed = &changed
	httputil.WriteJSON(w, http.StatusOK, out)
}

// HandleListSessionSalesStateHistory serves GET .../sales-state/history.
// Query: limit (1..100, default 20).
func (h *Handler) HandleListSessionSalesStateHistory(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.salesStatePath(w, r)
	if !ok {
		return
	}
	limit := int32(20)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 || n > 100 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"session.invalid_limit", "limit must be an integer from 1 to 100", r,
				map[string]any{"field": "limit"},
			))
			return
		}
		limit = int32(n)
	}
	ctx := r.Context()
	_, sites, err := h.salesStateSession(ctx, h.sessionQueries, orgID, sessionID)
	if err != nil {
		h.writeSalesStateLoadError(w, r, err)
		return
	}
	rows, err := h.sessionQueries.ListSalesStateChanges(ctx, sessionID, limit)
	if err != nil {
		h.writeSalesStateLoadError(w, r, err)
		return
	}
	items := make([]salesStateChangeOut, 0, len(rows))
	for _, c := range rows {
		items = append(items, salesStateChangeOut{
			ID:        c.ID.String(),
			OldState:  c.OldState,
			NewState:  c.NewState,
			ChangedBy: salesStateActorOut{Type: c.ActorType, Name: c.ActorName, Role: c.ActorRole, Source: c.Source},
			CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
			SiteSync:  salesstate.SiteDelivery(c, sites),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// errSalesStateNotFound marks a session that is missing, deleted or of
// another organization.
var errSalesStateNotFound = errors.New("hcatalog: session not found")

func (h *Handler) writeSalesStateLoadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errSalesStateNotFound) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	h.logger.Error("session: sales state load failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"session.sales_state_failed", "failed to load the sales state", r,
	))
}

// salesStateSession resolves the session's event inside the organization and
// how many WordPress sites sell that event.
func (h *Handler) salesStateSession(ctx context.Context, q *gen.Queries, orgID, sessionID uuid.UUID) (uuid.UUID, int, error) {
	eventID, sessionOrg, err := q.GetSalesStateSessionScope(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, 0, errSalesStateNotFound
		}
		return uuid.Nil, 0, err
	}
	if sessionOrg != orgID {
		return uuid.Nil, 0, errSalesStateNotFound
	}
	subs, err := q.ListWPSubscribersForEvent(ctx, eventID)
	if err != nil {
		return uuid.Nil, 0, err
	}
	return eventID, len(subs), nil
}

// loadSessionSalesState builds the full answer for one session.
func (h *Handler) loadSessionSalesState(ctx context.Context, q *gen.Queries, orgID, sessionID uuid.UUID) (sessionSalesStateResponse, error) {
	eventID, sites, err := h.salesStateSession(ctx, q, orgID, sessionID)
	if err != nil {
		return sessionSalesStateResponse{}, err
	}
	now := time.Now().UTC()
	facts, err := q.ListSessionSalesFacts(ctx, []uuid.UUID{sessionID}, now)
	if err != nil {
		return sessionSalesStateResponse{}, err
	}
	f, found := facts[sessionID]
	if !found {
		return sessionSalesStateResponse{}, errSalesStateNotFound
	}
	out := sessionSalesStateResponse{
		SessionID:   sessionID.String(),
		EventID:     eventID.String(),
		Effective:   salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now),
		TotalPlaces: int(f.TotalPlaces),
		FreePlaces:  int(f.FreePlaces),
		HeldPlaces:  int(f.HeldPlaces),
		SalesEndAt:  f.SalesEndAt.UTC().Format(time.RFC3339),
		SiteSync:    salesStateSiteSyncOut{Status: salesstate.SiteNone, Sites: sites},
	}
	if f.Manual != "" {
		m := f.Manual
		out.Manual = &m
	}
	changes, err := q.ListSalesStateChanges(ctx, sessionID, 1)
	if err != nil {
		return sessionSalesStateResponse{}, err
	}
	if len(changes) > 0 {
		c := changes[0]
		at := c.CreatedAt.UTC().Format(time.RFC3339)
		out.ChangedAt = &at
		out.ChangedBy = &salesStateActorOut{Type: c.ActorType, Name: c.ActorName, Role: c.ActorRole, Source: c.Source}
		out.SiteSync = salesStateSiteSyncOut{
			Status:    salesstate.SiteDelivery(c, sites),
			Sites:     sites,
			Attempts:  int(c.OutboxAttempts),
			LastError: c.OutboxLastError,
		}
		if out.SiteSync.Status == salesstate.SiteNone || out.SiteSync.Status == salesstate.SiteDelivered {
			out.SiteSync.LastError = nil
		}
	}
	n, err := q.CountLiveExternalAllocations(ctx, sessionID)
	if err != nil {
		return sessionSalesStateResponse{}, err
	}
	out.ExternalAllocations = int(n)
	return out, nil
}

// salesStateActor describes the caller for the journal: a user by their own
// name and membership role, an organization API key (a site) by the name
// the site gave for the person who switched, else "API key".
func (h *Handler) salesStateActor(ctx context.Context, orgID uuid.UUID, changedBy string) salesstate.Actor {
	actor, _ := auth.ActorFromContext(ctx)
	switch actor.Type {
	case auth.ActorTypeUser, auth.ActorTypeStubUser:
		out := salesstate.Actor{Type: "user", ID: actor.ID}
		if uid, err := uuid.Parse(actor.ID); err == nil {
			if name, role, err := h.sessionQueries.GetSalesStateActor(ctx, uid, orgID); err == nil {
				out.Name, out.Role = name, role
			}
		}
		return out
	case auth.ActorTypeService:
		name := changedBy
		if name == "" {
			name = "API key"
		}
		return salesstate.Actor{Type: "api_key", ID: actor.ID, Name: name}
	default:
		return salesstate.Actor{Type: "system", Name: changedBy}
	}
}

// actorAuditID is the caller's id for audit_events.actor_id, which accepts
// only a bare uuid (or "").
func actorAuditID(ctx context.Context) string {
	actor, _ := auth.ActorFromContext(ctx)
	if _, err := uuid.Parse(actor.ID); err != nil {
		return ""
	}
	return actor.ID
}

// salesStateSource is the client the request declared (X-Client-Channel),
// else "api".
func salesStateSource(ctx context.Context) string {
	if v, ok := audit.ClientChannelFromContext(ctx); ok {
		return v
	}
	return salesstate.SourceAPI
}

// hydrateSessionSalesStates fills SalesState and ManualSalesState from one
// batched facts read. A failed read logs and leaves them empty: the list is
// still served, and an empty state is visibly "unknown", never a wrong one.
func (h *Handler) hydrateSessionSalesStates(ctx context.Context, sessions []SessionResponse) {
	if h.sessionQueries == nil || len(sessions) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(sessions))
	for _, s := range sessions {
		if id, err := uuid.Parse(s.ID); err == nil {
			ids = append(ids, id)
		}
	}
	now := time.Now().UTC()
	facts, err := h.sessionQueries.ListSessionSalesFacts(ctx, ids, now)
	if err != nil {
		h.logger.Warn("session: sales-state hydration failed", slog.String("error", err.Error()))
		return
	}
	for i := range sessions {
		id, err := uuid.Parse(sessions[i].ID)
		if err != nil {
			continue
		}
		if f, ok := facts[id]; ok {
			sessions[i].SalesState = salesstate.Effective(f.Manual, salesstate.FactsFromRow(f), now)
			sessions[i].ManualSalesState = f.Manual
		}
	}
}
