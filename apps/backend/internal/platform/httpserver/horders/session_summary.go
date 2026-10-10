package horders

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// The session summary is the one-screen answer to "how is this session
// doing": places by status, categories with what was paid for them, orders,
// tickets, refunds, promo codes, invitations and the money per currency. It
// is read-only, scoped to one organization, and carries no buyer data —
// counts, amounts and ids only. The aggregates are shared with the event
// summary (event_summary.go) through summary_core.go.
//
//	GET /v1/organizations/{org_id}/sessions/{session_id}/summary   (order.read)

type summarySession struct {
	ID             string  `json:"id"`
	EventID        string  `json:"event_id"`
	OrgID          string  `json:"org_id"`
	EventName      string  `json:"event_name"`
	StartAt        string  `json:"start_at"`
	Status         string  `json:"status"`
	CapacityTotal  int32   `json:"capacity_total"`
	HasSeatingPlan bool    `json:"has_seating_plan"`
	VenueName      *string `json:"venue_name"`
	VenueTimezone  *string `json:"venue_timezone"`
}

type sessionSummary struct {
	Session summarySession `json:"session"`
	summaryCore
}

// buildSessionSummary folds the raw aggregates of one session into the
// response. Pure, so the arithmetic is unit-tested without a database.
func buildSessionSummary(header gen.SessionSummaryHeaderRow, rows summaryRows) sessionSummary {
	return sessionSummary{
		Session: summarySession{
			ID:             header.ID.String(),
			EventID:        header.EventID.String(),
			OrgID:          header.OrgID.String(),
			EventName:      header.EventName,
			StartAt:        header.StartAt.UTC().Format(time.RFC3339),
			Status:         header.Status,
			CapacityTotal:  header.CapacityTotal,
			HasSeatingPlan: header.SeatingPlanVersionID != nil,
			VenueName:      header.VenueName,
			VenueTimezone:  header.VenueTimezone,
		},
		summaryCore: buildSummaryCore(rows),
	}
}

// summaryFailure answers 500 and logs which part of the assembly failed.
func (h *Handler) summaryFailure(w http.ResponseWriter, r *http.Request, screen, part string, err error) {
	h.logger.Error("horders: "+screen+" summary failed",
		slog.String("part", part), slog.Any("error", err))
	httputil.WriteJSON(w, http.StatusInternalServerError,
		httputil.ErrorEnvelope("orders.internal", "failed to build the "+screen+" summary", r))
}

// HandleSessionSummary serves
// GET /v1/organizations/{org_id}/sessions/{session_id}/summary. A session of
// another organization is indistinguishable from a missing one (404).
func (h *Handler) HandleSessionSummary(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable,
			httputil.ErrorEnvelope("dependency.database_unavailable", "orders store not configured", r))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	ctx := r.Context()

	header, err := h.queries.GetSessionSummaryHeader(ctx, sessionID, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound,
			httputil.ErrorEnvelope("session.not_found", "session not found", r))
		return
	}
	if err != nil {
		h.summaryFailure(w, r, "session", "header", err)
		return
	}
	rows, part, err := h.loadSummaryRows(ctx, []uuid.UUID{sessionID})
	if err != nil {
		h.summaryFailure(w, r, "session", part, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, buildSessionSummary(header, rows))
}
