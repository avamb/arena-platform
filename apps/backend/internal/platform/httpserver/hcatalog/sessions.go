// sessions.go implements the session CRUD API endpoints (feature #126,
// reshaped in Wave 4 by AB-36/AB-38).
//
// A Session is a specific time slot of an Event at a specific Venue. The
// session — not the event — owns the venue, the seating bind, and the
// currency (Bil24 model: only the ActionEvent carries date/venue/currency).
//
// Capacity is DERIVED, in this order (AB-36):
//
//	bound seating plan version (assigned_seats / hybrid)
//	  -> capacity_override (operator input)
//	  -> venues.capacity_default
//
// A general-admission session that resolves to none of these is rejected
// with 422 session.capacity_unresolvable.
//
// Currency resolution (AB-38): venue -> city.currency_override ??
// country.currency, recorded as currency_source='derived'; an explicit
// request value wins and is recorded as 'override'. A session whose venue
// geography resolves to nothing MUST carry an explicit currency.
//
// Status lifecycle: draft → scheduled → completed|cancelled.
// Date invariant: end_at must be strictly after start_at (table CHECK + handler).
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/events/{event_id}/sessions        — create (session.create)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions        — list   (session.read)
//	GET    /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — get    (session.read)
//	PATCH  /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — update (session.update)
//	DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}   — delete (session.delete)
//
// Seating at create (AB-36 step 3): the create request may carry
// admission_mode=assigned_seats|hybrid plus seating_plan_version_id; the
// handler then runs the SEAT-B2 bind (auto-creating tiers from the SVG
// category legend) through the injected SeatingBinder callback, so a seated
// session is fully materialized in one call instead of requiring a separate
// edit-mode bind.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	catalogdomain "github.com/abhteam/arena_new/apps/backend/internal/domain/catalog"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Valid session statuses and transitions (forwarders to internal/domain/catalog).
//
// The state-machine has moved to the pure-domain layer (feature #183). The
// local names are preserved as thin forwarders so the handlers below and the
// in-package tests (sessions_test.go via catalog_shims.go) compile unchanged.
// ─────────────────────────────────────────────────────────────────────────────

// validSessionStatuses lists the allowed status values for sessions. Backed
// by the catalog domain layer.
var validSessionStatuses = map[string]bool{
	string(catalogdomain.SessionStatusDraft):     true,
	string(catalogdomain.SessionStatusScheduled): true,
	string(catalogdomain.SessionStatusCancelled): true,
	string(catalogdomain.SessionStatusCompleted): true,
}

// validCreateAdmissionModes lists the admission modes accepted at session
// create. general_admission is the default when the field is omitted.
var validCreateAdmissionModes = map[string]bool{
	"general_admission": true,
	"assigned_seats":    true,
	"hybrid":            true,
}

// currencyPattern is the ISO-4217 shape check mirrored from the DB CHECK
// constraints added in migration 0081.
var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// IsValidSessionTransition returns true when the transition from → to is
// allowed by the Session state machine. Forwards to internal/domain/catalog
// so the rule lives in exactly one place.
func IsValidSessionTransition(from, to string) bool {
	return catalogdomain.IsValidSessionTransition(from, to)
}

// ─────────────────────────────────────────────────────────────────────────────
// Seating bind callback (AB-36 step 3)
// ─────────────────────────────────────────────────────────────────────────────

// SeatingBindError is the transport shape a SeatingBinder uses to report a
// bind failure back into the session-create path. It mirrors the error
// envelope the standalone bind endpoint would have written, so the create
// endpoint surfaces identical codes whether the bind runs standalone or
// inline.
type SeatingBindError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

// SeatingBinder runs the SEAT-B2 seating bind for a freshly created session:
// materializes session_seats from the plan version geometry, auto-creates
// one tier per SVG price category, locks the version, and recomputes
// capacity_total. The canonical implementation lives in the hseating
// sub-package; catalog_shims.go injects a forwarder so hcatalog never
// imports hseating (same pattern as SessionCancelledPublisher).
type SeatingBinder func(ctx context.Context, r *http.Request, eventID, sessionID, planVersionID uuid.UUID, admissionMode string) *SeatingBindError

// WithSeatingBinder attaches the seating bind callback used by the session
// create path. Production wiring calls this in the shim layer; tests that
// omit it will get 503 on seated session creation.
func (h *Handler) WithSeatingBinder(b SeatingBinder) *Handler {
	h.bindSeating = b
	return h
}

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// SessionResponse is the JSON representation of a single session.
type SessionResponse struct {
	ID                     string  `json:"id"`
	EventID                string  `json:"event_id"`
	VenueID                string  `json:"venue_id"`
	StartAt                string  `json:"start_at"`
	EndAt                  string  `json:"end_at"`
	CapacityTotal          int32   `json:"capacity_total"`
	CapacityOverride       *int32  `json:"capacity_override"`
	Status                 string  `json:"status"`
	AdmissionMode          string  `json:"admission_mode"`
	SeatingPlanVersionID   *string `json:"seating_plan_version_id"`
	Currency               string  `json:"currency"`
	CurrencySource         string  `json:"currency_source"`
	CreatedAt              string  `json:"created_at"`
	UpdatedAt              string  `json:"updated_at"`
	HasOverlappingSessions bool    `json:"has_overlapping_sessions"`
}

// SessionFromRow converts a SessionRow to a SessionResponse.
// hasOverlap is the result of the overlap detection check.
func SessionFromRow(s gen.SessionRow, hasOverlap bool) SessionResponse {
	resp := SessionResponse{
		ID:                     s.ID.String(),
		EventID:                s.EventID.String(),
		VenueID:                s.VenueID.String(),
		StartAt:                s.StartAt.UTC().Format(time.RFC3339),
		EndAt:                  s.EndAt.UTC().Format(time.RFC3339),
		CapacityTotal:          s.CapacityTotal,
		CapacityOverride:       s.CapacityOverride,
		Status:                 s.Status,
		AdmissionMode:          s.AdmissionMode,
		Currency:               strings.TrimSpace(s.Currency),
		CurrencySource:         s.CurrencySource,
		CreatedAt:              s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              s.UpdatedAt.UTC().Format(time.RFC3339),
		HasOverlappingSessions: hasOverlap,
	}
	if s.SeatingPlanVersionID != nil {
		v := s.SeatingPlanVersionID.String()
		resp.SeatingPlanVersionID = &v
	}
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Overlap detection helpers
// ─────────────────────────────────────────────────────────────────────────────

// DetectSessionOverlaps returns true when any two sessions in the list overlap.
// Two sessions overlap when a.start_at < b.end_at AND a.end_at > b.start_at.
// This is an O(n²) check applied at the application layer per the feature spec.
//
// The pure-domain implementation lives in internal/domain/catalog as
// DetectOverlaps over the adapter-free SessionInterval value type; this
// forwarder projects the gen.SessionRow slice into that value type so the
// domain layer never imports the adapters/postgres/gen package (feature #183).
func DetectSessionOverlaps(sessions []gen.SessionRow) bool {
	intervals := make([]catalogdomain.SessionInterval, len(sessions))
	for i, s := range sessions {
		intervals[i] = catalogdomain.SessionInterval{StartAt: s.StartAt, EndAt: s.EndAt}
	}
	return catalogdomain.DetectOverlaps(intervals)
}

// ─────────────────────────────────────────────────────────────────────────────
// Capacity propagation hook (foundation placeholder)
// ─────────────────────────────────────────────────────────────────────────────

// OnCapacityChange is called whenever a session's capacity_total is updated.
// It propagates the new capacity to the inventory ledger (feature #130).
// Exported so catalog_shims.go can keep the original *Server onCapacityChange
// delegate alive for sessions_test.go.
func (h *Handler) OnCapacityChange(ctx context.Context, sessionID uuid.UUID, oldCapacity, newCapacity int32) {
	h.logger.Info("session: capacity changed — propagating to inventory ledger",
		slog.String("session_id", sessionID.String()),
		slog.Int("old_capacity", int(oldCapacity)),
		slog.Int("new_capacity", int(newCapacity)),
	)
	if h.inventoryQueries != nil {
		newTotalPtr := newCapacity
		if _, err := h.inventoryQueries.UpdateCapacityTotal(ctx, sessionID, nil, &newTotalPtr); err != nil {
			h.logger.Error("session: inventory capacity propagation failed",
				slog.String("session_id", sessionID.String()),
				slog.String("error", err.Error()),
			)
			// Non-fatal: the inventory ledger may not exist yet (if it hasn't been initialized).
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Venue context resolution (shared by create + update)
// ─────────────────────────────────────────────────────────────────────────────

// resolveVenueContext loads the venue's org / capacity / derived-currency
// context and enforces that the venue belongs to the event's org (superadmin
// bypass per AB-12/AB-28). Writes the error envelope and returns ok=false on
// any failure.
func (h *Handler) resolveVenueContext(
	w http.ResponseWriter, r *http.Request, orgID, venueID uuid.UUID,
) (gen.VenueSessionContextRow, bool) {
	venueCtx, err := h.sessionQueries.GetVenueSessionContext(r.Context(), venueID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"session.venue_not_found", "venue_id does not reference an existing venue", r,
				map[string]any{"field": "venue_id"},
			))
			return gen.VenueSessionContextRow{}, false
		}
		h.logger.Error("session: venue context lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.venue_lookup_failed", "failed to resolve venue", r,
		))
		return gen.VenueSessionContextRow{}, false
	}
	if venueCtx.OrgID != orgID && !auth.HasSuperadminOrgAccess(r.Context()) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"session.venue_org_mismatch",
			"venue does not belong to the event's organization", r,
			map[string]any{"field": "venue_id"},
		))
		return gen.VenueSessionContextRow{}, false
	}
	return venueCtx, true
}

// normalizeCurrency trims + uppercases a caller-supplied currency and
// validates the ISO-4217 shape. Returns ("", false) after writing a 422
// envelope when the value is malformed.
func normalizeCurrency(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	cur := strings.ToUpper(strings.TrimSpace(raw))
	if !currencyPattern.MatchString(cur) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_currency",
			"currency must be a three-letter ISO 4217 code", r,
			map[string]any{"field": "currency"},
		))
		return "", false
	}
	return cur, true
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/events/{event_id}/sessions
// ─────────────────────────────────────────────────────────────────────────────

// createSessionRequest is the request body for POST .../sessions.
// capacity_total is deliberately absent: capacity is derived (plan ->
// capacity_override -> venue default). currency is an optional explicit
// override of the geography-derived value.
type createSessionRequest struct {
	VenueID              string `json:"venue_id"`
	StartAt              string `json:"start_at"`
	EndAt                string `json:"end_at"`
	CapacityOverride     *int32 `json:"capacity_override"`
	Status               string `json:"status"`
	AdmissionMode        string `json:"admission_mode"`
	SeatingPlanVersionID string `json:"seating_plan_version_id"`
	Currency             string `json:"currency"`
}

// HandleCreateSession serves POST /v1/organizations/{org_id}/events/{event_id}/sessions.
// Requires JWT + "session.create" permission.
func (h *Handler) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req createSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)
	req.AdmissionMode = strings.TrimSpace(req.AdmissionMode)
	req.SeatingPlanVersionID = strings.TrimSpace(req.SeatingPlanVersionID)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_status", "status must be one of: draft, scheduled, cancelled, completed", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Parse start_at.
	if req.StartAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.missing_start_at", "start_at is required", r,
			map[string]any{"field": "start_at"},
		))
		return
	}
	startAt, parseErr := time.Parse(time.RFC3339, req.StartAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "start_at"},
		))
		return
	}

	// Parse end_at.
	if req.EndAt == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.missing_end_at", "end_at is required", r,
			map[string]any{"field": "end_at"},
		))
		return
	}
	endAt, parseErr := time.Parse(time.RFC3339, req.EndAt)
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Date invariant: end_at must be after start_at.
	if !endAt.After(startAt) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Venue: required, must exist, must belong to the event's org (AB-36).
	if strings.TrimSpace(req.VenueID) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.missing_venue_id", "venue_id is required", r,
			map[string]any{"field": "venue_id"},
		))
		return
	}
	venueID, parseErr := uuid.Parse(strings.TrimSpace(req.VenueID))
	if parseErr != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_venue_id", "venue_id must be a valid UUID", r,
			map[string]any{"field": "venue_id"},
		))
		return
	}

	// Admission mode + seating plan version (AB-36 step 3).
	mode := req.AdmissionMode
	if mode == "" {
		mode = "general_admission"
	}
	if !validCreateAdmissionModes[mode] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_admission_mode",
			"admission_mode must be one of general_admission|assigned_seats|hybrid", r,
			map[string]any{"field": "admission_mode"},
		))
		return
	}
	seated := mode != "general_admission"
	var planVersionID uuid.UUID
	if seated {
		if req.SeatingPlanVersionID == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"session.missing_seating_plan_version",
				"seating_plan_version_id is required for assigned_seats/hybrid sessions", r,
				map[string]any{"field": "seating_plan_version_id"},
			))
			return
		}
		planVersionID, parseErr = uuid.Parse(req.SeatingPlanVersionID)
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"session.invalid_seating_plan_version",
				"seating_plan_version_id must be a valid UUID", r,
				map[string]any{"field": "seating_plan_version_id"},
			))
			return
		}
	} else if req.SeatingPlanVersionID != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.seating_plan_not_applicable",
			"seating_plan_version_id requires admission_mode assigned_seats or hybrid", r,
			map[string]any{"field": "seating_plan_version_id"},
		))
		return
	}

	// Explicit currency override, if any (AB-38): validated before any DB
	// round trip so malformed codes fail fast.
	currency := ""
	currencySource := "derived"
	if strings.TrimSpace(req.Currency) != "" {
		currency, ok = normalizeCurrency(w, r, req.Currency)
		if !ok {
			return
		}
		currencySource = "override"
	}

	// capacity_override sanity — also a pure syntax check.
	if req.CapacityOverride != nil && *req.CapacityOverride <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_capacity_override", "capacity_override must be greater than 0", r,
			map[string]any{"field": "capacity_override"},
		))
		return
	}

	// Syntax is clean — now hit the DB: venue existence / org ownership /
	// capacity default / geography-derived currency in one round trip.
	venueCtx, ok := h.resolveVenueContext(w, r, orgID, venueID)
	if !ok {
		return
	}

	// Currency derivation (AB-38): a venue with no resolvable geography
	// demands an explicit value.
	if currency == "" {
		if venueCtx.DerivedCurrency != nil && strings.TrimSpace(*venueCtx.DerivedCurrency) != "" {
			currency = strings.TrimSpace(*venueCtx.DerivedCurrency)
		} else {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"session.currency_unresolvable",
				"the venue's geography does not resolve to a currency; supply currency explicitly", r,
				map[string]any{"field": "currency"},
			))
			return
		}
	}

	// Capacity resolution (AB-36 step 4).
	var capacityTotal int32
	switch {
	case seated:
		// Placeholder satisfying the capacity_total > 0 CHECK; the bind
		// below recomputes the real value from the plan version.
		capacityTotal = 1
		if req.CapacityOverride != nil {
			capacityTotal = *req.CapacityOverride
		}
	case req.CapacityOverride != nil:
		capacityTotal = *req.CapacityOverride
	case venueCtx.CapacityDefault != nil && *venueCtx.CapacityDefault > 0:
		capacityTotal = *venueCtx.CapacityDefault
	default:
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"session.capacity_unresolvable",
			"capacity could not be derived: supply capacity_override or set the venue's capacity_default", r,
			map[string]any{"field": "capacity_override"},
		))
		return
	}

	sess, err := h.sessionQueries.InsertSession(ctx, eventID, venueID, startAt, endAt, capacityTotal, req.CapacityOverride, req.Status, currency, currencySource)
	if err != nil {
		h.logger.Error("session: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.insert_failed", "failed to create session", r,
		))
		return
	}

	// Seated create: run the SEAT-B2 bind inline (auto-creating tiers from
	// the SVG category legend). On bind failure the freshly created session
	// is soft-deleted so a failed create leaves no half-bound session
	// behind, and the bind's own error envelope is surfaced verbatim.
	if seated {
		if h.bindSeating == nil {
			_, _ = h.sessionQueries.SoftDeleteSession(ctx, sess.ID, eventID)
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"session.seating_bind_unavailable", "seating bind is not available", r,
			))
			return
		}
		if bindErr := h.bindSeating(ctx, r, eventID, sess.ID, planVersionID, mode); bindErr != nil {
			if _, delErr := h.sessionQueries.SoftDeleteSession(ctx, sess.ID, eventID); delErr != nil {
				h.logger.Error("session: cleanup after failed bind failed",
					slog.String("session_id", sess.ID.String()),
					slog.String("error", delErr.Error()),
				)
			}
			details := bindErr.Details
			if details == nil {
				httputil.WriteJSON(w, bindErr.Status, httputil.ErrorEnvelope(bindErr.Code, bindErr.Message, r))
			} else {
				httputil.WriteJSON(w, bindErr.Status, httputil.ErrorEnvelopeWithDetails(bindErr.Code, bindErr.Message, r, details))
			}
			return
		}
		// Re-read the session: the bind rewired admission_mode,
		// seating_plan_version_id and capacity_total.
		sess, err = h.sessionQueries.GetSessionByID(ctx, sess.ID, eventID)
		if err != nil {
			h.logger.Error("session: reload after bind failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"session.insert_failed", "failed to load session after seating bind", r,
			))
			return
		}
	}

	// Initialize the inventory ledger for the new session (feature #130,
	// non-fatal) with the final derived capacity.
	if h.inventoryQueries != nil {
		capTotal := sess.CapacityTotal
		if _, err := h.inventoryQueries.InsertInventoryLedger(ctx, sess.ID, nil, &capTotal); err != nil {
			h.logger.Warn("session: inventory initialization failed (non-fatal)",
				slog.String("session_id", sess.ID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	// Check for overlapping sessions (allowed but flagged).
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, uuid.Nil, startAt, endAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	if hasOverlap {
		h.logger.Warn("session: overlapping sessions detected",
			slog.String("session_id", sess.ID.String()),
			slog.String("event_id", eventID.String()),
			slog.Int("overlap_count", int(overlapCount)),
		)
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"session": SessionFromRow(sess, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions
// ─────────────────────────────────────────────────────────────────────────────

// HandleListSessions serves GET /v1/organizations/{org_id}/events/{event_id}/sessions.
// Returns all active sessions for the specified event.
// Requires JWT + "session.read" permission.
func (h *Handler) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	rows, err := h.sessionQueries.ListSessionsByEvent(ctx, eventID)
	if err != nil {
		h.logger.Error("session: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.list_failed", "failed to list sessions", r,
		))
		return
	}

	// Detect overlaps across the returned list at the application layer.
	hasOverlap := DetectSessionOverlaps(rows)

	result := make([]SessionResponse, 0, len(rows))
	for _, sess := range rows {
		result = append(result, SessionFromRow(sess, hasOverlap))
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"sessions":                 result,
		"has_overlapping_sessions": hasOverlap,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetSession serves GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.read" permission.
func (h *Handler) HandleGetSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	sess, err := h.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	// Check overlap with siblings.
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, sess.StartAt, sess.EndAt)
	hasOverlap := overlapErr == nil && overlapCount > 0

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(sess, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateSessionRequest is the request body for PATCH .../sessions/{id}.
// All fields are optional; nil/empty values leave the existing value unchanged.
// capacity_total is not an input — it is re-derived when venue_id or
// capacity_override change (GA sessions only; plan-bound capacity is owned
// by the bind path). Setting currency records an explicit override and
// cascades to the session's tiers.
type updateSessionRequest struct {
	VenueID          *string `json:"venue_id"`
	StartAt          *string `json:"start_at"`
	EndAt            *string `json:"end_at"`
	CapacityOverride *int32  `json:"capacity_override"`
	Status           string  `json:"status"`
	Currency         *string `json:"currency"`
}

// HandleUpdateSession serves PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Requires JWT + "session.update" permission.
// Triggers the capacity propagation hook when capacity_total changes.
func (h *Handler) HandleUpdateSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.empty_body", "request body is required", r))
		return
	}

	var req updateSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Status = strings.TrimSpace(req.Status)

	// Validate status if provided.
	if req.Status != "" && !validSessionStatuses[req.Status] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_status", "status must be one of: draft, scheduled, cancelled, completed", r,
			map[string]any{"field": "status"},
		))
		return
	}

	// Parse optional start_at.
	var startAt *time.Time
	if req.StartAt != nil {
		trimmed := strings.TrimSpace(*req.StartAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "start_at"},
				))
				return
			}
			startAt = &t
		}
	}

	// Parse optional end_at.
	var endAt *time.Time
	if req.EndAt != nil {
		trimmed := strings.TrimSpace(*req.EndAt)
		if trimmed != "" {
			t, parseErr := time.Parse(time.RFC3339, trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", r,
					map[string]any{"field": "end_at"},
				))
				return
			}
			endAt = &t
		}
	}

	// Date invariant: if both are provided, end_at must be after start_at.
	if startAt != nil && endAt != nil && !endAt.After(*startAt) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_date_range", "end_at must be after start_at", r,
			map[string]any{"field": "end_at"},
		))
		return
	}

	// Validate capacity_override if provided.
	if req.CapacityOverride != nil && *req.CapacityOverride <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"session.invalid_capacity_override", "capacity_override must be greater than 0", r,
			map[string]any{"field": "capacity_override"},
		))
		return
	}

	// Fetch the current session to detect capacity changes and validate status transitions.
	current, err := h.sessionQueries.GetSessionByID(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: get for update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.get_failed", "failed to get session", r,
		))
		return
	}

	seated := current.AdmissionMode != "general_admission"

	// capacity_override is a GA knob only: plan-bound sessions derive their
	// capacity from the bound version and must not be silently overridden.
	if req.CapacityOverride != nil && seated {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"session.capacity_override_not_applicable",
			"capacity_override does not apply to plan-bound sessions; the bound seating plan owns the capacity", r,
			map[string]any{"field": "capacity_override"},
		))
		return
	}

	// Optional venue change (AB-36): validate ownership and re-derive the
	// currency when the current value was itself derived.
	var venueID *uuid.UUID
	var venueCtx gen.VenueSessionContextRow
	venueChanged := false
	if req.VenueID != nil {
		trimmed := strings.TrimSpace(*req.VenueID)
		if trimmed != "" {
			parsed, parseErr := uuid.Parse(trimmed)
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"session.invalid_venue_id", "venue_id must be a valid UUID", r,
					map[string]any{"field": "venue_id"},
				))
				return
			}
			if parsed != current.VenueID {
				venueCtx, ok = h.resolveVenueContext(w, r, orgID, parsed)
				if !ok {
					return
				}
				venueID = &parsed
				venueChanged = true
			}
		}
	}

	// Currency (AB-38): explicit value = deliberate override; a venue change
	// re-derives only sessions whose currency was derived in the first place.
	var newCurrency *string
	currencySource := ""
	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		cur, ok := normalizeCurrency(w, r, *req.Currency)
		if !ok {
			return
		}
		if cur != strings.TrimSpace(current.Currency) {
			newCurrency = &cur
		}
		currencySource = "override"
	} else if venueChanged && current.CurrencySource == "derived" &&
		venueCtx.DerivedCurrency != nil && strings.TrimSpace(*venueCtx.DerivedCurrency) != "" {
		derived := strings.TrimSpace(*venueCtx.DerivedCurrency)
		if derived != strings.TrimSpace(current.Currency) {
			newCurrency = &derived
		}
		currencySource = "derived"
	}

	// Re-derive capacity_total for GA sessions when the operator knob or the
	// venue changed (AB-36 resolution chain: override -> venue default).
	var newCapacityTotal *int32
	if !seated && (req.CapacityOverride != nil || venueChanged) {
		effectiveOverride := current.CapacityOverride
		if req.CapacityOverride != nil {
			effectiveOverride = req.CapacityOverride
		}
		switch {
		case effectiveOverride != nil:
			newCapacityTotal = effectiveOverride
		case venueChanged && venueCtx.CapacityDefault != nil && *venueCtx.CapacityDefault > 0:
			newCapacityTotal = venueCtx.CapacityDefault
		case venueChanged:
			// Venue changed, no override anywhere, new venue has no default:
			// keep the current capacity rather than failing the whole PATCH.
			newCapacityTotal = nil
		}
	}

	// Validate status transition when status is being changed.
	if req.Status != "" && req.Status != current.Status {
		if !IsValidSessionTransition(current.Status, req.Status) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"session.invalid_transition",
				"status transition from '"+current.Status+"' to '"+req.Status+"' is not allowed",
				r,
				map[string]any{
					"current_status": current.Status,
					"target_status":  req.Status,
				},
			))
			return
		}
	}

	updated, err := h.sessionQueries.UpdateSession(ctx, sessionID, eventID, venueID, startAt, endAt, newCapacityTotal, req.CapacityOverride, req.Status, newCurrency, currencySource)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.update_failed", "failed to update session", r,
		))
		return
	}

	// Capacity propagation hook: fire when capacity_total changed.
	if updated.CapacityTotal != current.CapacityTotal {
		h.OnCapacityChange(ctx, updated.ID, current.CapacityTotal, updated.CapacityTotal)
	}

	// Webhook event catalog (feature S-1): emit v1.session.cancelled exactly
	// once when the status transitions into "cancelled".  Best-effort: errors
	// are logged inside publishScannerEvent. The publisher is injected as a
	// callback by catalog_shims.go because its implementation lives in the
	// hscanner sub-package.
	if req.Status == "cancelled" && current.Status != "cancelled" && updated.Status == "cancelled" {
		if h.publishSessionCancelled != nil {
			h.publishSessionCancelled(ctx, updated.ID.String(), eventID.String(), current.Status)
		}
	}

	// Check overlap with siblings (excluding this session itself).
	effectiveStart := updated.StartAt
	effectiveEnd := updated.EndAt
	overlapCount, overlapErr := h.sessionQueries.CountOverlappingSessions(ctx, eventID, sessionID, effectiveStart, effectiveEnd)
	hasOverlap := overlapErr == nil && overlapCount > 0

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(updated, hasOverlap),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteSession serves DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "session.delete" permission.
func (h *Handler) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	// Open transaction: soft-delete + audit in one atomic write.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.sessionQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteSession(ctx, sessionID, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("session: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.delete_failed", "failed to delete session", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.session.delete",
			ResourceType: "session",
			ResourceID:   sessionID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"event_id": eventID.String(),
				"status":   deleted.Status,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("session: audit write failed", slog.String("error", err.Error()))
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"session": SessionFromRow(deleted, false),
		"deleted": true,
	})
}
