// control_sessions.go — the session for entry control only (migration 0228,
// spec 69 MA-40).
//
// A control session exists so that tickets sold ELSEWHERE can be checked at
// the door by the scanner app; the platform sells nothing for it. It lives in
// its own container event: a draft, private event named by the organizer, whose
// live sessions are all control_only and which is never published (the
// triggers of migration 0228 keep both facts, the handlers below answer 409
// before the database has to).
//
//	POST /v1/organizations/{org_id}/control-sessions — create the event and its
//	     session in ONE request (session.create)
//
// A control session owns no tiers, no seats and no inventory ledger row, and
// it is never announced: no catalog outbox row, no site notification.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/richtext"
)

const (
	// ControlSessionTitleMaxRunes is the longest title (the container event's
	// name) accepted, counted in characters after trimming.
	ControlSessionTitleMaxRunes = 200
	// controlSessionDefaultLength is the session length when end_at is omitted.
	controlSessionDefaultLength = 3 * time.Hour
	// maxControlSessionCapacity bounds capacity to what int32 holds.
	maxControlSessionCapacity = int64(1<<31 - 1)
	// CodeEventControlOnly is the 409 code of an event that only controls the
	// entrance when somebody tries to publish it or to add a sold session.
	CodeEventControlOnly = "event.control_only"
)

// createControlSessionRequest is the body of POST .../control-sessions.
type createControlSessionRequest struct {
	Title    string  `json:"title"`
	VenueID  string  `json:"venue_id"`
	StartAt  string  `json:"start_at"`
	EndAt    *string `json:"end_at"`
	Capacity *int64  `json:"capacity"`
	Currency *string `json:"currency"`
}

// controlSessionInput is the checked body.
type controlSessionInput struct {
	Title   string
	VenueID uuid.UUID
	StartAt time.Time
	EndAt   time.Time
	// Capacity is the provisional capacity_total: the request's capacity, else 1.
	Capacity int32
	// CapacityOverride is set exactly when the request carried a capacity.
	CapacityOverride *int32
	// Currency is the explicit currency, upper-cased, "" when the venue's
	// geography has to derive it.
	Currency string
}

// controlFieldError is a refusal of the body: the HTTP status, the error code,
// the message and the offending field.
type controlFieldError struct {
	Status  int
	Code    string
	Message string
	Field   string
}

// parseControlSessionRequest reads and checks the body of POST
// .../control-sessions. It touches no database: the venue and the currency
// derivation are resolved by the handler afterwards.
func parseControlSessionRequest(body []byte) (controlSessionInput, *controlFieldError) {
	var out controlSessionInput
	bad := func(code, msg, field string) (controlSessionInput, *controlFieldError) {
		return controlSessionInput{}, &controlFieldError{Status: http.StatusBadRequest, Code: code, Message: msg, Field: field}
	}
	if len(body) == 0 {
		return bad("session.empty_body", "request body is required", "")
	}
	var req createControlSessionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return bad("session.invalid_json", "request body is not valid JSON", "")
	}

	out.Title = strings.TrimSpace(req.Title)
	if out.Title == "" {
		return bad("session.missing_title", "title is required", "title")
	}
	if utf8.RuneCountInString(out.Title) > ControlSessionTitleMaxRunes {
		return bad("session.title_too_long", "title must not be longer than 200 characters", "title")
	}

	venue := strings.TrimSpace(req.VenueID)
	if venue == "" {
		return bad("session.missing_venue_id", "venue_id is required", "venue_id")
	}
	venueID, err := uuid.Parse(venue)
	if err != nil {
		return bad("session.invalid_venue_id", "venue_id must be a valid UUID", "venue_id")
	}
	out.VenueID = venueID

	if strings.TrimSpace(req.StartAt) == "" {
		return bad("session.missing_start_at", "start_at is required", "start_at")
	}
	startAt, err := time.Parse(time.RFC3339, strings.TrimSpace(req.StartAt))
	if err != nil {
		return bad("session.invalid_start_at", "start_at must be a valid RFC3339 timestamp", "start_at")
	}
	out.StartAt = startAt

	out.EndAt = startAt.Add(controlSessionDefaultLength)
	if req.EndAt != nil && strings.TrimSpace(*req.EndAt) != "" {
		endAt, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.EndAt))
		if err != nil {
			return bad("session.invalid_end_at", "end_at must be a valid RFC3339 timestamp", "end_at")
		}
		if !endAt.After(startAt) {
			return bad("session.invalid_date_range", "end_at must be after start_at", "end_at")
		}
		out.EndAt = endAt
	}

	out.Capacity = 1
	if req.Capacity != nil {
		if *req.Capacity <= 0 || *req.Capacity > maxControlSessionCapacity {
			return bad("session.invalid_capacity", "capacity must be greater than 0", "capacity")
		}
		c := int32(*req.Capacity)
		out.Capacity = c
		out.CapacityOverride = &c
	}

	if req.Currency != nil && strings.TrimSpace(*req.Currency) != "" {
		cur := strings.ToUpper(strings.TrimSpace(*req.Currency))
		if !currencyPattern.MatchString(cur) {
			return controlSessionInput{}, &controlFieldError{
				Status: http.StatusUnprocessableEntity, Code: "session.invalid_currency",
				Message: "currency must be a three-letter ISO 4217 code", Field: "currency",
			}
		}
		out.Currency = cur
	}
	return out, nil
}

// writeControlOnlyEvent answers 409 event.control_only: the event only
// controls the entrance, so it is never published and takes no session that is
// sold here.
func writeControlOnlyEvent(w http.ResponseWriter, r *http.Request, eventID uuid.UUID, why string) {
	httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
		CodeEventControlOnly, why, r, map[string]any{"event_id": eventID.String()},
	))
}

// isControlOnlyViolation reports whether err is the check_violation (23514)
// the triggers of migration 0228 raise when a sold session meets a control
// event, or a control event is published. The handlers check first; this is
// the answer for a race that slips between the check and the write.
func isControlOnlyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && strings.Contains(pgErr.Message, "control_only")
}

// writeControlOnlyViolation answers 409 event.control_only for a trigger
// refusal; the caller has seen isControlOnlyViolation.
func writeControlOnlyViolation(w http.ResponseWriter, r *http.Request, eventID uuid.UUID) {
	writeControlOnlyEvent(w, r, eventID, "this event only controls the entrance: it takes no session that is sold here and is never published")
}

// eventIsControlOnly reports whether the event holds a live control_only
// session. q must not be inside an open transaction.
func eventIsControlOnly(ctx context.Context, q *gen.Queries, eventID uuid.UUID) (bool, error) {
	ids, err := q.ListControlOnlyEventIDs(ctx, []uuid.UUID{eventID})
	if err != nil {
		return false, err
	}
	return len(ids) > 0, nil
}

// hydrateControlOnly fills ControlOnly on the given responses from one
// ListControlOnlyEventIDs round trip. A failed lookup logs and leaves false:
// the flag is presentational, the list must not fail because of it.
func (h *Handler) hydrateControlOnly(ctx context.Context, responses []eventResponse) {
	if h.eventQueries == nil || len(responses) == 0 {
		return
	}
	ids := make([]uuid.UUID, 0, len(responses))
	for _, r := range responses {
		if id, err := uuid.Parse(r.ID); err == nil {
			ids = append(ids, id)
		}
	}
	found, err := h.eventQueries.ListControlOnlyEventIDs(ctx, ids)
	if err != nil {
		h.logger.Warn("event: control-only hydration failed", slog.String("error", err.Error()))
		return
	}
	control := make(map[uuid.UUID]struct{}, len(found))
	for _, id := range found {
		control[id] = struct{}{}
	}
	for i := range responses {
		if id, err := uuid.Parse(responses[i].ID); err == nil {
			_, responses[i].ControlOnly = control[id]
		}
	}
}

// HandleCreateControlSession serves POST /v1/organizations/{org_id}/control-sessions.
// Requires JWT + "session.create" permission.
//
// The container event (draft, private, named by the title) and its session
// (scheduled, general admission, no plan, sales_mode control_only) are written
// in ONE transaction. No tier, no inventory ledger row, no outbox row.
func (h *Handler) HandleCreateControlSession(w http.ResponseWriter, r *http.Request) {
	if h.eventQueries == nil || h.sessionQueries == nil || h.pool == nil {
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
	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("session.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	in, ferr := parseControlSessionRequest(body)
	if ferr != nil {
		if ferr.Field == "" {
			httputil.WriteJSON(w, ferr.Status, httputil.ErrorEnvelope(ferr.Code, ferr.Message, r))
		} else {
			httputil.WriteJSON(w, ferr.Status, httputil.ErrorEnvelopeWithDetails(
				ferr.Code, ferr.Message, r, map[string]any{"field": ferr.Field},
			))
		}
		return
	}

	// The venue belongs to this organization or is a verified shared one,
	// exactly as for POST .../sessions.
	venueCtx, ok := h.resolveVenueContext(w, r, orgID, in.VenueID)
	if !ok {
		return
	}
	currency, currencySource := in.Currency, "override"
	if currency == "" {
		currencySource = "derived"
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

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	fail := func(step string, err error) {
		h.logger.Error("control session: "+step+" failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.insert_failed", "failed to create the control session", r,
		))
	}

	ev, err := h.eventQueries.WithTx(tx).InsertEvent(ctx, orgID, in.Title, nil, "draft", "private", nil, richtext.FormatART)
	if err != nil {
		fail("insert event", err)
		return
	}
	qs := h.sessionQueries.WithTx(tx)
	sess, err := qs.InsertSession(ctx, ev.ID, in.VenueID, in.StartAt, in.EndAt, in.Capacity, in.CapacityOverride, "scheduled", nil, currency, currencySource)
	if err != nil {
		fail("insert session", err)
		return
	}
	// None of the four partner fields is set: the table CHECK demands it.
	sess, err = qs.SetSessionSalesMode(ctx, sess.ID, ev.ID, gen.SessionSalesModeControlOnly, nil, nil, nil, nil)
	if err != nil {
		fail("set sales mode", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	// The cache columns (first_session_at ...) are trigger-maintained: re-read
	// the event. Nothing is announced to sites or the outbox.
	if fresh, gErr := h.eventQueries.GetEventRaw(ctx, ev.ID); gErr == nil {
		ev = fresh
	}
	sessions := []SessionResponse{SessionFromRow(sess, false)}
	h.hydrateSessionSalesStates(ctx, sessions)
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"event":   h.hydratedEvent(ctx, ev),
		"session": sessions[0],
	})
}

// salesModeLockedCreate answers the refusal of sales_mode control_only in the
// body of the ordinary session create.
func salesModeLockedCreate(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) {
	writeSalesModeLocked(w, r, gen.SessionSalesModePlatform, gen.SessionSalesModeControlOnly,
		"a control_only session is created with POST /v1/organizations/"+orgID.String()+"/control-sessions, not here")
}
