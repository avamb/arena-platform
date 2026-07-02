// event_reports.go — HTTP API for post-event report reads (feature #159).
//
// Endpoints:
//
//	GET  /v1/events/{event_id}/report   — return the latest report + lines (report.read)
//	POST /v1/events/{event_id}/report   — trigger on-demand report generation (report.generate)
//
// The GET endpoint returns the most-recently generated report for the event.
// If no report has been created yet, it returns 404 with code="report.not_found".
//
// The POST endpoint creates a new event_reports row in 'pending' state and
// returns 202 Accepted with the new report ID. The worker picks it up and runs
// the aggregation. Returns 409 Conflict if a report is already pending or
// generating.
//
// Both endpoints require a valid JWT. Auth enforcement is handled by route-level
// middleware (auth.Middleware + permissions.RequirePermission), not in the handler.
package hreports

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// EventReportLineResponse is the JSON representation of a single report line.
type EventReportLineResponse struct {
	ID          string    `json:"id"`
	Category    string    `json:"category"`
	Quantity    int64     `json:"quantity"`
	GrossAmount int64     `json:"gross_amount"`
	NetAmount   int64     `json:"net_amount"`
	Currency    string    `json:"currency"`
	CreatedAt   time.Time `json:"created_at"`
}

// EventReportResponse is the JSON representation of an event report with lines.
type EventReportResponse struct {
	ID                string                    `json:"id"`
	EventID           string                    `json:"event_id"`
	OrgID             string                    `json:"org_id"`
	State             string                    `json:"state"`
	ReportWindowStart *time.Time                `json:"report_window_start,omitempty"`
	ReportWindowEnd   *time.Time                `json:"report_window_end,omitempty"`
	ErrorMsg          *string                   `json:"error_msg,omitempty"`
	GeneratedAt       *time.Time                `json:"generated_at,omitempty"`
	CreatedAt         time.Time                 `json:"created_at"`
	UpdatedAt         time.Time                 `json:"updated_at"`
	Lines             []EventReportLineResponse `json:"lines"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/events/{event_id}/report
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetEventReport serves GET /v1/events/{event_id}/report.
// Returns the most recent report for an event with all aggregated lines.
// Returns 404 when no report exists yet.
// Auth + report.read permission enforced by route middleware.
func (h *Handler) HandleGetEventReport(w http.ResponseWriter, r *http.Request) {
	if h.reportQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "report service not available", r,
		))
		return
	}

	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	report, err := h.reportQueries.GetEventReportByEventID(r.Context(), eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"report.not_found", "no report found for this event", r,
			))
			return
		}
		slog.ErrorContext(r.Context(), "handleGetEventReport: query failed",
			"event_id", eventID, "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"report.get_failed", "failed to retrieve report", r,
		))
		return
	}

	lines, err := h.reportQueries.ListEventReportLinesByReport(r.Context(), report.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "handleGetEventReport: list lines failed",
			"report_id", report.ID, "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"report.lines_failed", "failed to retrieve report lines", r,
		))
		return
	}

	resp := BuildEventReportResponse(report, lines)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"report": resp})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/events/{event_id}/report
// ─────────────────────────────────────────────────────────────────────────────

// HandleTriggerEventReport serves POST /v1/events/{event_id}/report.
// Creates a new event_reports row in 'pending' state and returns 202 Accepted.
// Returns 409 Conflict if a report is already pending or generating.
// Auth + report.generate permission enforced by route middleware.
func (h *Handler) HandleTriggerEventReport(w http.ResponseWriter, r *http.Request) {
	if h.reportQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "report service not available", r,
		))
		return
	}

	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	// Check if a report already exists in a non-terminal state.
	existing, err := h.reportQueries.GetEventReportByEventID(r.Context(), eventID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.ErrorContext(r.Context(), "handleTriggerEventReport: check existing failed",
			"event_id", eventID, "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"report.check_failed", "failed to check existing report", r,
		))
		return
	}
	if err == nil && (existing.State == "pending" || existing.State == "generating") {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"report.already_pending", "a report is already being generated for this event", r,
		))
		return
	}

	// Create a new report row in 'pending' state.
	now := time.Now().UTC()
	report, err := h.reportQueries.InsertEventReport(r.Context(), eventID, eventID, &now, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "handleTriggerEventReport: insert failed",
			"event_id", eventID, "error", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"report.create_failed", "failed to create report", r,
		))
		return
	}

	slog.InfoContext(r.Context(), "event report triggered",
		"report_id", report.ID,
		"event_id", eventID,
	)

	// Enqueue a report.deliver worker job (feature #160).
	// The delivery handler retries until the report reaches 'ready' state.
	// Best-effort: errors are logged and swallowed inside enqueueReportDeliveryJob.
	h.enqueueReportDeliveryJob(r.Context(), report.ID)

	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{
		"report_id": report.ID.String(),
		"state":     report.State,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// BuildEventReportResponse converts gen.EventReportRow + lines into the API response.
func BuildEventReportResponse(r gen.EventReportRow, lines []gen.EventReportLineRow) EventReportResponse {
	lineResponses := make([]EventReportLineResponse, 0, len(lines))
	for _, l := range lines {
		lineResponses = append(lineResponses, EventReportLineResponse{
			ID:          l.ID.String(),
			Category:    l.Category,
			Quantity:    l.Quantity,
			GrossAmount: l.GrossAmount,
			NetAmount:   l.NetAmount,
			Currency:    l.Currency,
			CreatedAt:   l.CreatedAt,
		})
	}

	return EventReportResponse{
		ID:                r.ID.String(),
		EventID:           r.EventID.String(),
		OrgID:             r.OrgID.String(),
		State:             r.State,
		ReportWindowStart: r.ReportWindowStart,
		ReportWindowEnd:   r.ReportWindowEnd,
		ErrorMsg:          r.ErrorMsg,
		GeneratedAt:       r.GeneratedAt,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
		Lines:             lineResponses,
	}
}

// reportLineFromRow converts a gen.EventReportLineRow to an
// EventReportLineResponse. Used in tests to verify the mapping is correct.
//
//nolint:unused // test helper kept available for future event-report regression tests
func reportLineFromRow(l gen.EventReportLineRow) EventReportLineResponse {
	return EventReportLineResponse{
		ID:          l.ID.String(),
		Category:    l.Category,
		Quantity:    l.Quantity,
		GrossAmount: l.GrossAmount,
		NetAmount:   l.NetAmount,
		Currency:    l.Currency,
		CreatedAt:   l.CreatedAt,
	}
}

// encodeReportJSON is a convenience wrapper for handler tests that need to
// produce a JSON body without setting up a full HTTP stack.
//
//nolint:unused // test helper kept available for future event-report regression tests
func encodeReportJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
