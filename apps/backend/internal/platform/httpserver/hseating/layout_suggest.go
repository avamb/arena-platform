// layout_suggest.go serves the seat-layout allocator (spec 26 §4.1, task S1e
// of spec 40):
//
//	GET  /v1/organizations/{org_id}/sessions/{session_id}/seat-layout/algorithms
//	POST /v1/organizations/{org_id}/sessions/{session_id}/seat-layout/suggest
//
// Both are read-only. The list names every algorithm of the layoutgen
// registry with its parameter schema, so clients draw the form without
// knowing any algorithm. suggest reads the geometry of the session's plan
// version and the state of its seats, runs one algorithm and answers the
// proposal plus its revenue estimate. It never writes: applying a layout is
// the seat-batch route of S1d, and the algorithm's id and parameters are
// stored with a saved layout there.
//
// The annotations (stage, section weights, obstructed seats) are the ones a
// request carries; a request without any uses the ones stored with the plan
// (seating_plans.annotations, task S1d), pruned to what the session's plan
// version still has.
//
// The shim (seating_shims.go) has already checked membership of {org_id}
// and that the session belongs to it.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// suggestBodyLimit caps the suggest body. The largest part is the optional
// list of obstructed seat keys (about 30 bytes each).
const suggestBodyLimit = 1 << 20

// LayoutAlgorithmItem is one algorithm of the list.
type LayoutAlgorithmItem struct {
	ID                  string            `json:"id"`
	LabelKey            string            `json:"label_key"`
	DescriptionKey      string            `json:"description_key,omitempty"`
	RequiredAnnotations []string          `json:"required_annotations"`
	Params              []layoutgen.Param `json:"params"`
}

// LayoutAlgorithmsResponse is the answer of the list route.
type LayoutAlgorithmsResponse struct {
	SessionID  string                `json:"session_id"`
	Currency   string                `json:"currency"`
	Algorithms []LayoutAlgorithmItem `json:"algorithms"`
}

// LayoutSuggestRequest is the body of suggest.
type LayoutSuggestRequest struct {
	Algorithm   string                     `json:"algorithm"`
	Params      map[string]json.RawMessage `json:"params"`
	Annotations *layoutgen.Annotations     `json:"annotations"`
}

// Where the annotations of a suggestion came from.
const (
	annotationsFromRequest = "request"
	annotationsFromPlan    = "plan"
	annotationsFromNone    = "none"
)

// LayoutSuggestResponse is the answer of suggest.
type LayoutSuggestResponse struct {
	SessionID            string           `json:"session_id"`
	Algorithm            string           `json:"algorithm"`
	Params               layoutgen.Values `json:"params"`
	Currency             string           `json:"currency"`
	SeatingPlanVersionID string           `json:"seating_plan_version_id"`
	GeometryChecksum     string           `json:"geometry_checksum"`
	// AnnotationsSource is "request" when the body carried annotations,
	// "plan" when the ones stored with the plan were used, "none" otherwise.
	AnnotationsSource string            `json:"annotations_source"`
	SeatStatusVersion int64             `json:"seat_status_version"`
	Layout            layoutgen.Layout  `json:"layout"`
	Metrics           layoutgen.Metrics `json:"metrics"`
}

// LayoutAlgorithmItems lists the process-wide registry.
func LayoutAlgorithmItems() []LayoutAlgorithmItem {
	algs := layoutgen.List()
	out := make([]LayoutAlgorithmItem, 0, len(algs))
	for _, a := range algs {
		s := a.Params()
		req := s.RequiredAnnotations
		if req == nil {
			req = []string{}
		}
		params := s.Params
		if params == nil {
			params = []layoutgen.Param{}
		}
		out = append(out, LayoutAlgorithmItem{
			ID: a.ID(), LabelKey: s.LabelKey, DescriptionKey: s.DescriptionKey,
			RequiredAnnotations: req, Params: params,
		})
	}
	return out
}

// HandleListLayoutAlgorithms serves GET .../seat-layout/algorithms.
func (h *Handler) HandleListLayoutAlgorithms(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}
	currency, err := h.queries.GetSessionCurrency(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			return
		}
		h.logger.Error("seating: layout algorithms session lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.algorithms_failed", "failed to load the session", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, LayoutAlgorithmsResponse{
		SessionID:  sessionID.String(),
		Currency:   currency,
		Algorithms: LayoutAlgorithmItems(),
	})
}

// HandleSuggestLayout serves POST .../seat-layout/suggest.
func (h *Handler) HandleSuggestLayout(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	sessionID, ok := httputil.UUIDPathParam(w, r, "session_id")
	if !ok {
		return
	}

	// Everything the caller can get wrong is answered before the database.
	var req LayoutSuggestRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, suggestBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seat_layout.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return
	}
	alg, found := layoutgen.Lookup(req.Algorithm)
	if !found {
		ids := []string{}
		for _, a := range layoutgen.List() {
			ids = append(ids, a.ID())
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.unknown_algorithm", "unknown layout algorithm", r,
			map[string]any{"algorithm": req.Algorithm, "available": ids},
		))
		return
	}
	values, err := alg.Params().Validate(req.Params)
	if err != nil {
		writeLayoutError(w, r, h.logger, err)
		return
	}
	var ann layoutgen.Annotations
	annSource := annotationsFromNone
	if req.Annotations != nil {
		ann = *req.Annotations
		annSource = annotationsFromRequest
	}
	if err := ann.Validate(); err != nil {
		writeLayoutError(w, r, h.logger, err)
		return
	}

	in, status, err := h.loadLayoutInput(r.Context(), sessionID)
	if err != nil {
		switch status {
		case http.StatusNotFound:
			httputil.WriteJSON(w, status, httputil.ErrorEnvelope("session.not_found", "session not found", r))
		case http.StatusUnprocessableEntity:
			httputil.WriteJSON(w, status, httputil.ErrorEnvelope(
				"seat_layout.no_seating_plan", "the session has no seating plan", r,
			))
		default:
			h.logger.Error("seating: layout suggest load failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seat_layout.suggest_failed", "failed to load the session's seats", r,
			))
		}
		return
	}

	if req.Annotations == nil {
		if stored, ok := h.storedAnnotations(r.Context(), in.planID, in.geometry); ok {
			ann, annSource = stored, annotationsFromPlan
		}
	}

	layout, metrics, err := layoutgen.Run(alg, in.geometry, ann, in.state, values)
	if err != nil {
		writeLayoutError(w, r, h.logger, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, LayoutSuggestResponse{
		SessionID:            sessionID.String(),
		Algorithm:            alg.ID(),
		Params:               values,
		Currency:             in.state.Currency,
		SeatingPlanVersionID: in.versionID,
		GeometryChecksum:     in.checksum,
		AnnotationsSource:    annSource,
		SeatStatusVersion:    in.seatStatusVersion,
		Layout:               layout,
		Metrics:              metrics,
	})
}

// layoutInput is what suggest reads from the database.
type layoutInput struct {
	geometry          seating.Geometry
	state             layoutgen.SessionState
	versionID         string
	planID            uuid.UUID
	checksum          string
	seatStatusVersion int64
}

// loadLayoutInput reads the session, its plan version and its seats in one
// read-only REPEATABLE READ snapshot, so the seat statuses and the frozen
// prices describe the same moment as seat_status_version. The returned
// status is 404 (no session) or 422 (no seating plan) on a caller-facing
// failure, 500 otherwise.
func (h *Handler) loadLayoutInput(ctx context.Context, sessionID uuid.UUID) (layoutInput, int, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return layoutInput{}, http.StatusInternalServerError, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := h.queries.WithTx(tx)

	adm, err := q.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return layoutInput{}, http.StatusNotFound, err
		}
		return layoutInput{}, http.StatusInternalServerError, err
	}
	if adm.SeatingPlanVersionID == nil || adm.AdmissionMode == "general_admission" {
		return layoutInput{}, http.StatusUnprocessableEntity, errors.New("no seating plan")
	}
	ver, err := q.GetSeatingPlanVersionByID(ctx, *adm.SeatingPlanVersionID)
	if err != nil {
		return layoutInput{}, http.StatusInternalServerError, err
	}
	var g seating.Geometry
	if err := json.Unmarshal(ver.Geometry, &g); err != nil {
		return layoutInput{}, http.StatusInternalServerError, err
	}
	currency, err := q.GetSessionCurrency(ctx, sessionID)
	if err != nil {
		return layoutInput{}, http.StatusInternalServerError, err
	}
	rows, err := q.ListSessionSeatLayoutStates(ctx, sessionID)
	if err != nil {
		return layoutInput{}, http.StatusInternalServerError, err
	}
	return layoutInput{
		geometry:          g,
		state:             SessionLayoutState(currency, rows),
		versionID:         ver.ID.String(),
		planID:            ver.SeatingPlanID,
		checksum:          ver.GeometryChecksum,
		seatStatusVersion: adm.SeatStatusVersion,
	}, http.StatusOK, nil
}

// SessionLayoutState turns the seat rows into the allocator's view: sold
// and held seats are frozen at their order price (the list price when no
// order line exists yet, or for a seat sold upstream), closed seats are
// marked closed, and a held seat whose reservation is no longer live is
// free (the expiry sweep is about to release it).
func SessionLayoutState(currency string, rows []gen.SessionSeatLayoutStateRow) layoutgen.SessionState {
	st := layoutgen.SessionState{
		Currency: currency,
		SoldHeld: map[string]layoutgen.FrozenSeat{},
		Closed:   map[string]bool{},
	}
	for _, row := range rows {
		if row.Status == layoutgen.StatusHeld && row.HoldExpired {
			// A dead hold the expiry sweep has not reached yet: free.
			continue
		}
		switch row.Status {
		case layoutgen.StatusSold, layoutgen.StatusHeld:
			var price int64
			switch {
			case row.OrderPrice != nil:
				price = *row.OrderPrice
			case row.TierPrice != nil:
				price = *row.TierPrice
			}
			st.SoldHeld[row.SeatKey] = layoutgen.FrozenSeat{Status: row.Status, PriceMinor: price}
		case "unavailable":
			st.Closed[row.SeatKey] = true
		}
	}
	return st
}

// writeLayoutError maps a layoutgen error to its HTTP answer.
func writeLayoutError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var pe *layoutgen.ParamError
	var ae *layoutgen.AnnotationError
	var re *layoutgen.AnnotationRequiredError
	var ce *layoutgen.ContractError
	switch {
	case errors.As(err, &pe):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_param", pe.Error(), r,
			map[string]any{"param": pe.Param, "reason": pe.Reason},
		))
	case errors.As(err, &ae):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_annotation", ae.Error(), r,
			map[string]any{"annotation": ae.Annotation, "reason": ae.Reason},
		))
	case errors.As(err, &re):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.annotation_required", re.Error(), r,
			map[string]any{"annotation": re.Annotation},
		))
	case errors.Is(err, layoutgen.ErrNoSeats):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seat_layout.no_seats", "the seating plan has no seats", r,
		))
	case errors.As(err, &ce):
		logger.Error("seating: layout algorithm broke its contract", slog.String("error", ce.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.algorithm_failed", "the layout algorithm failed", r,
		))
	default:
		logger.Error("seating: layout suggest failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.suggest_failed", "failed to suggest a layout", r,
		))
	}
}

// storedAnnotations reads the annotations stored with a plan and prunes them
// to the geometry of the version the session uses. ok is false when the plan
// has none (or they cannot be read: a broken row must not stop a proposal,
// which then asks for the stage as before).
func (h *Handler) storedAnnotations(ctx context.Context, planID uuid.UUID, g seating.Geometry) (layoutgen.Annotations, bool) {
	row, err := h.queries.GetSeatingPlanAnnotations(ctx, planID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn("seating: stored annotations unreadable", slog.String("plan_id", planID.String()), slog.String("error", err.Error()))
		}
		return layoutgen.Annotations{}, false
	}
	var ann layoutgen.Annotations
	if err := json.Unmarshal(row.Annotations, &ann); err != nil {
		h.logger.Warn("seating: stored annotations undecodable", slog.String("plan_id", planID.String()), slog.String("error", err.Error()))
		return layoutgen.Annotations{}, false
	}
	ann = ann.PruneTo(g)
	if ann.Validate() != nil {
		return layoutgen.Annotations{}, false
	}
	if ann.FocalPoint == nil && len(ann.SectionWeights) == 0 && len(ann.ObstructedSeatKeys) == 0 {
		return layoutgen.Annotations{}, false
	}
	return ann, true
}
