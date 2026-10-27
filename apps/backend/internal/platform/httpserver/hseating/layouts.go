// layouts.go serves saved seat layouts, their application to a session and
// the allocator annotations of a seating plan (task S1d, spec
// 08_architecture/26_seat_layout_management_plan_ru.md sections 3.2-3.3):
//
//	GET    /v1/organizations/{org_id}/seating-plans/{plan_id}/layouts
//	POST   /v1/organizations/{org_id}/seating-plans/{plan_id}/layouts
//	GET    /v1/organizations/{org_id}/seating-plans/{plan_id}/layouts/{layout_id}
//	PATCH  /v1/organizations/{org_id}/seating-plans/{plan_id}/layouts/{layout_id}
//	DELETE /v1/organizations/{org_id}/seating-plans/{plan_id}/layouts/{layout_id}
//	POST   /v1/organizations/{org_id}/sessions/{session_id}/layouts/apply
//	GET    /v1/organizations/{org_id}/seating-plans/{plan_id}/annotations
//	PATCH  /v1/organizations/{org_id}/seating-plans/{plan_id}/annotations
//
// Applying a layout is seatlayout.ApplyLayout: ONE transaction with the
// sessions row locked first, recorded as a seat batch of kind apply_layout
// (so it can be reverted from the batch history). Everything is gated by
// session.seats.manage in the path organization; the shims
// (seating_layout_store_shims.go) have checked the membership, and the
// session routes also that the session is the organization's.
//
// A plan is usable by an organization when it owns it or the plan is shared
// (visibility other than private); a private plan of another organization is
// the route's own 404. Annotations are the PLAN's data and are written only
// by its owner (guardrail 13: another organization forks instead).
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating/layoutgen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

const layoutBodyLimit = 4 << 20 // 10 000 seats x ~60 bytes, with room

// SeatLayoutResponse is the wire shape of one layout (openapi SeatLayout).
type SeatLayoutResponse struct {
	ID             string                   `json:"id"`
	OrgID          string                   `json:"org_id"`
	SeatingPlanID  string                   `json:"seating_plan_id"`
	Name           string                   `json:"name"`
	Source         string                   `json:"source"`
	Algorithm      *string                  `json:"algorithm"`
	Params         json.RawMessage          `json:"params"`
	Payload        seatlayout.LayoutPayload `json:"payload"`
	CreatedBy      *string                  `json:"created_by"`
	CreatedByLabel string                   `json:"created_by_label"`
	CreatedAt      string                   `json:"created_at"`
	UpdatedAt      string                   `json:"updated_at"`
}

// SeatLayoutSummaryResponse is one row of the layout list: the slots and the
// counts, not the seat map (openapi SeatLayoutSummary).
type SeatLayoutSummaryResponse struct {
	ID              string                  `json:"id"`
	OrgID           string                  `json:"org_id"`
	SeatingPlanID   string                  `json:"seating_plan_id"`
	Name            string                  `json:"name"`
	Source          string                  `json:"source"`
	Algorithm       *string                 `json:"algorithm"`
	Params          json.RawMessage         `json:"params"`
	Slots           []seatlayout.LayoutSlot `json:"slots"`
	AssignmentCount int32                   `json:"assignment_count"`
	ClosedCount     int32                   `json:"closed_count"`
	CreatedBy       *string                 `json:"created_by"`
	CreatedByLabel  string                  `json:"created_by_label"`
	CreatedAt       string                  `json:"created_at"`
	UpdatedAt       string                  `json:"updated_at"`
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

func seatLayoutResponse(row gen.SeatLayoutRow) (SeatLayoutResponse, error) {
	var payload seatlayout.LayoutPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return SeatLayoutResponse{}, err
	}
	if payload.Assignments == nil {
		payload.Assignments = map[string]string{}
	}
	if payload.ClosedSeatKeys == nil {
		payload.ClosedSeatKeys = []string{}
	}
	return SeatLayoutResponse{
		ID: row.ID.String(), OrgID: row.OrgID.String(), SeatingPlanID: row.SeatingPlanID.String(),
		Name: row.Name, Source: row.Source, Algorithm: row.Algorithm, Params: rawOrNull(row.Params),
		Payload: payload, CreatedBy: uuidStr(row.CreatedBy), CreatedByLabel: row.CreatedByLabel,
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// layoutScope reads {org_id} and {plan_id} and checks the dependencies.
func (h *Handler) layoutScope(w http.ResponseWriter, r *http.Request) (orgID, planID uuid.UUID, ok bool) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return uuid.Nil, uuid.Nil, false
	}
	if orgID, ok = httputil.UUIDPathParam(w, r, "org_id"); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if planID, ok = httputil.UUIDPathParam(w, r, "plan_id"); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, planID, true
}

// usablePlan loads the plan an organization may build layouts on. A missing
// plan and a private plan of another organization answer the same 404.
func (h *Handler) usablePlan(w http.ResponseWriter, r *http.Request, orgID, planID uuid.UUID) (gen.SeatingPlanAnnotationsRow, bool) {
	plan, err := h.queries.GetSeatingPlanAnnotations(r.Context(), planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writePlanNotFound(w, r)
			return plan, false
		}
		h.logger.Error("seating: layout plan lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to load the seating plan", r,
		))
		return plan, false
	}
	if plan.OwnerOrgID != orgID && plan.Visibility == "private" {
		writePlanNotFound(w, r)
		return plan, false
	}
	return plan, true
}

func writePlanNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"seating_plan.not_found", "seating plan not found", r,
	))
}

func writeLayoutNotFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
		"seat_layout.not_found", "seat layout not found", r,
	))
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// HandleListSeatLayouts serves GET .../seating-plans/{plan_id}/layouts.
func (h *Handler) HandleListSeatLayouts(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	if _, ok := h.usablePlan(w, r, orgID, planID); !ok {
		return
	}
	rows, err := h.queries.ListSeatLayoutSummaries(r.Context(), orgID, planID)
	if err != nil {
		h.logger.Error("seating: list layouts failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to list the layouts", r,
		))
		return
	}
	items := make([]SeatLayoutSummaryResponse, 0, len(rows))
	for _, row := range rows {
		var slots []seatlayout.LayoutSlot
		if err := json.Unmarshal(row.Slots, &slots); err != nil {
			h.logger.Error("seating: decode layout slots failed", slog.String("layout_id", row.ID.String()), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seat_layout.failed", "failed to read a layout", r,
			))
			return
		}
		if slots == nil {
			slots = []seatlayout.LayoutSlot{}
		}
		items = append(items, SeatLayoutSummaryResponse{
			ID: row.ID.String(), OrgID: row.OrgID.String(), SeatingPlanID: row.SeatingPlanID.String(),
			Name: row.Name, Source: row.Source, Algorithm: row.Algorithm, Params: rawOrNull(row.Params),
			Slots: slots, AssignmentCount: row.AssignmentCount, ClosedCount: row.ClosedCount,
			CreatedBy: uuidStr(row.CreatedBy), CreatedByLabel: row.CreatedByLabel,
			CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"layouts": items, "max_layouts": seatlayout.MaxLayoutsPerPlan,
	})
}

// HandleGetSeatLayout serves GET .../layouts/{layout_id}.
func (h *Handler) HandleGetSeatLayout(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	layoutID, ok := httputil.UUIDPathParam(w, r, "layout_id")
	if !ok {
		return
	}
	row, err := h.queries.GetSeatLayout(r.Context(), layoutID, orgID, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeLayoutNotFound(w, r)
			return
		}
		h.logger.Error("seating: get layout failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to load the layout", r,
		))
		return
	}
	h.writeLayout(w, r, http.StatusOK, row)
}

func (h *Handler) writeLayout(w http.ResponseWriter, r *http.Request, status int, row gen.SeatLayoutRow) {
	resp, err := seatLayoutResponse(row)
	if err != nil {
		h.logger.Error("seating: decode layout failed", slog.String("layout_id", row.ID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to read the layout", r,
		))
		return
	}
	httputil.WriteJSON(w, status, resp)
}

type seatLayoutCreateRequest struct {
	Name      string                     `json:"name"`
	Source    string                     `json:"source"`
	Algorithm *string                    `json:"algorithm"`
	Params    map[string]json.RawMessage `json:"params"`
	Payload   *seatlayout.LayoutPayload  `json:"payload"`
	SessionID *string                    `json:"session_id"`
}

func (h *Handler) writeLayoutError(w http.ResponseWriter, r *http.Request, err error) bool {
	var le *seatlayout.LayoutError
	if errors.As(err, &le) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(le.Code, le.Message, r, le.Details))
		return true
	}
	return false
}

// HandleCreateSeatLayout serves POST .../seating-plans/{plan_id}/layouts.
func (h *Handler) HandleCreateSeatLayout(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	var req seatLayoutCreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, layoutBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", "request body is not a valid seat layout", r,
			map[string]any{"reason": err.Error(), "max_bytes": layoutBodyLimit},
		))
		return
	}
	name, err := seatlayout.NormalizeLayoutName(req.Name)
	if err != nil {
		h.writeLayoutError(w, r, err)
		return
	}
	source := req.Source
	if source == "" {
		source = seatlayout.SourceManual
	}
	plan, ok := h.usablePlan(w, r, orgID, planID)
	if !ok {
		return
	}

	algorithm, params, ok := h.layoutAlgorithm(w, r, source, req)
	if !ok {
		return
	}
	payload, ok := h.layoutPayloadFor(w, r, source, req, orgID, planID, plan)
	if !ok {
		return
	}
	norm, err := payload.Normalize()
	if err != nil {
		if !h.writeLayoutError(w, r, err) {
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seat_layout.failed", "failed to check the layout", r,
			))
		}
		return
	}
	payloadJSON, err := json.Marshal(norm)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to encode the layout", r,
		))
		return
	}

	actor := batchActor(r.Context())
	var row gen.SeatLayoutRow
	err = h.inLayoutTx(r.Context(), func(tx pgx.Tx, txq *gen.Queries) error {
		if err := txq.LockSeatLayoutsForPlan(r.Context(), orgID, planID); err != nil {
			return err
		}
		n, err := txq.CountSeatLayouts(r.Context(), orgID, planID)
		if err != nil {
			return err
		}
		if n >= seatlayout.MaxLayoutsPerPlan {
			return errLayoutLimit
		}
		row, err = txq.InsertSeatLayout(r.Context(), gen.InsertSeatLayoutParams{
			OrgID: orgID, SeatingPlanID: planID, Name: name, Source: source,
			Algorithm: algorithm, Params: params, Payload: payloadJSON,
			CreatedBy: actor.ID, CreatedByLabel: actor.Label,
		})
		if err != nil {
			return err
		}
		return h.writeAuditTx(r.Context(), tx, r, "v1.seating_plan.seat_layout.create", planID.String(), map[string]any{
			"layout_id": row.ID.String(), "org_id": orgID.String(), "source": source,
			"algorithm": algorithm, "slot_count": len(norm.Slots),
		})
	})
	switch {
	case errors.Is(err, errLayoutLimit):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.limit_reached", "this plan already has the maximum number of layouts", r,
			map[string]any{"max": seatlayout.MaxLayoutsPerPlan},
		))
	case err != nil && isUniqueViolation(err):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seat_layout.duplicate_name", "a layout with this name already exists on this plan", r,
		))
	case err != nil:
		h.logger.Error("seating: create layout failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to save the layout", r,
		))
	default:
		h.writeLayout(w, r, http.StatusCreated, row)
	}
}

var errLayoutLimit = errors.New("seating: layout limit reached")

// inLayoutTx runs fn in a transaction and commits.
func (h *Handler) inLayoutTx(ctx context.Context, fn func(tx pgx.Tx, txq *gen.Queries) error) error {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx, h.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// layoutAlgorithm checks the allocator id and parameters of a create
// request. An allocator layout names a registered algorithm; any other
// source may carry one too (a proposal the organizer tuned by hand).
func (h *Handler) layoutAlgorithm(w http.ResponseWriter, r *http.Request, source string, req seatLayoutCreateRequest) (*string, json.RawMessage, bool) {
	valid := false
	for _, s := range seatlayout.LayoutSources {
		if s == source {
			valid = true
		}
	}
	if !valid {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_source", "source must be manual, geometry, allocator or copied_from_session", r,
			map[string]any{"source": source},
		))
		return nil, nil, false
	}
	if req.Algorithm == nil {
		if source == seatlayout.SourceAllocator || req.Params != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seat_layout.algorithm_required", "an allocator layout names its algorithm; params need an algorithm", r,
				map[string]any{"field": "algorithm"},
			))
			return nil, nil, false
		}
		return nil, nil, true
	}
	if source == seatlayout.SourceGeometry || source == seatlayout.SourceCopiedFromSession {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", "this source is not made by an algorithm", r,
			map[string]any{"field": "algorithm"},
		))
		return nil, nil, false
	}
	alg, found := layoutgen.Lookup(*req.Algorithm)
	if !found {
		ids := []string{}
		for _, a := range layoutgen.List() {
			ids = append(ids, a.ID())
		}
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.unknown_algorithm", "unknown layout algorithm", r,
			map[string]any{"algorithm": *req.Algorithm, "available": ids},
		))
		return nil, nil, false
	}
	values, err := alg.Params().Validate(req.Params)
	if err != nil {
		writeLayoutError(w, r, h.logger, err)
		return nil, nil, false
	}
	raw, err := json.Marshal(values)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to encode the algorithm parameters", r,
		))
		return nil, nil, false
	}
	id := alg.ID()
	return &id, raw, true
}

// layoutPayloadFor builds the payload a create request stands for: the
// request's own, the plan's geometry or the session's current layout.
func (h *Handler) layoutPayloadFor(w http.ResponseWriter, r *http.Request, source string, req seatLayoutCreateRequest, orgID, planID uuid.UUID, plan gen.SeatingPlanAnnotationsRow) (seatlayout.LayoutPayload, bool) {
	ctx := r.Context()
	invalid := func(field, msg string) (seatlayout.LayoutPayload, bool) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", msg, r, map[string]any{"field": field},
		))
		return seatlayout.LayoutPayload{}, false
	}
	fail := func(err error) (seatlayout.LayoutPayload, bool) {
		if !h.writeLayoutError(w, r, err) {
			h.logger.Error("seating: build layout failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seat_layout.failed", "failed to build the layout", r,
			))
		}
		return seatlayout.LayoutPayload{}, false
	}
	switch source {
	case seatlayout.SourceManual, seatlayout.SourceAllocator:
		if req.Payload == nil {
			return invalid("payload", "payload is required for this source")
		}
		if req.SessionID != nil {
			return invalid("session_id", "session_id is only for source copied_from_session")
		}
		return *req.Payload, true

	case seatlayout.SourceGeometry:
		if req.Payload != nil || req.SessionID != nil {
			return invalid("payload", "a geometry layout is built from the plan; send no payload or session_id")
		}
		if plan.CurrentVersionID == nil {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"seat_layout.plan_has_no_version", "the plan has no version to take categories from", r,
			))
			return seatlayout.LayoutPayload{}, false
		}
		ver, err := h.queries.GetSeatingPlanVersionByID(ctx, *plan.CurrentVersionID)
		if err != nil {
			return fail(err)
		}
		var g seating.Geometry
		if err := json.Unmarshal(ver.Geometry, &g); err != nil {
			return fail(err)
		}
		overrides := map[string]string{}
		if p, err := h.queries.GetSeatingPlanByID(ctx, planID); err == nil && len(p.CategoryNameOverrides) > 0 {
			_ = json.Unmarshal(p.CategoryNameOverrides, &overrides)
		}
		payload, err := seatlayout.FromGeometry(g, overrides)
		if err != nil {
			return fail(err)
		}
		return payload, true

	default: // copied_from_session
		if req.Payload != nil {
			return invalid("payload", "a layout copied from a session is built from it; send session_id only")
		}
		if req.SessionID == nil {
			return invalid("session_id", "session_id is required for source copied_from_session")
		}
		sid, err := uuid.Parse(*req.SessionID)
		if err != nil {
			return invalid("session_id", "session_id must be a valid UUID")
		}
		payload, status, err := h.sessionLayout(ctx, orgID, planID, sid)
		if err != nil {
			switch status {
			case http.StatusNotFound:
				httputil.WriteJSON(w, status, httputil.ErrorEnvelope("session.not_found", "session not found", r))
			case http.StatusUnprocessableEntity:
				httputil.WriteJSON(w, status, httputil.ErrorEnvelope(
					"seat_layout.plan_mismatch", "the session is not on this seating plan", r,
				))
			default:
				return fail(err)
			}
			return seatlayout.LayoutPayload{}, false
		}
		return payload, true
	}
}

// sessionLayout reads a session's categories and seats in one read-only
// snapshot and turns them into a layout. 404: not the organization's
// session; 422: on another plan.
func (h *Handler) sessionLayout(ctx context.Context, orgID, planID, sessionID uuid.UUID) (seatlayout.LayoutPayload, int, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := h.queries.WithTx(tx)
	oc, err := q.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return seatlayout.LayoutPayload{}, http.StatusNotFound, err
		}
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	if oc.OrgID != orgID {
		return seatlayout.LayoutPayload{}, http.StatusNotFound, pgx.ErrNoRows
	}
	sp, err := q.GetSessionPlanID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return seatlayout.LayoutPayload{}, http.StatusUnprocessableEntity, err
		}
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	if sp != planID {
		return seatlayout.LayoutPayload{}, http.StatusUnprocessableEntity, errors.New("plan mismatch")
	}
	tiers, err := q.ListTicketTiersBySession(ctx, sessionID)
	if err != nil {
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	colors, err := q.ListSessionTierColors(ctx, sessionID)
	if err != nil {
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	seats, err := q.ListSessionSeatAssignments(ctx, sessionID)
	if err != nil {
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	payload, err := seatlayout.FromSession(tiers, colors, seats)
	if err != nil {
		var le *seatlayout.LayoutError
		if errors.As(err, &le) {
			return seatlayout.LayoutPayload{}, http.StatusBadRequest, err
		}
		return seatlayout.LayoutPayload{}, http.StatusInternalServerError, err
	}
	return payload, http.StatusOK, nil
}

type seatLayoutRenameRequest struct {
	Name string `json:"name"`
}

// HandleRenameSeatLayout serves PATCH .../layouts/{layout_id}.
func (h *Handler) HandleRenameSeatLayout(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	layoutID, ok := httputil.UUIDPathParam(w, r, "layout_id")
	if !ok {
		return
	}
	var req seatLayoutRenameRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", "request body is not valid", r, map[string]any{"reason": err.Error()},
		))
		return
	}
	name, err := seatlayout.NormalizeLayoutName(req.Name)
	if err != nil {
		h.writeLayoutError(w, r, err)
		return
	}
	var row gen.SeatLayoutRow
	err = h.inLayoutTx(r.Context(), func(tx pgx.Tx, txq *gen.Queries) error {
		var err error
		row, err = txq.RenameSeatLayout(r.Context(), layoutID, orgID, planID, name)
		if err != nil {
			return err
		}
		return h.writeAuditTx(r.Context(), tx, r, "v1.seating_plan.seat_layout.rename", planID.String(), map[string]any{
			"layout_id": layoutID.String(), "org_id": orgID.String(),
		})
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeLayoutNotFound(w, r)
	case err != nil && isUniqueViolation(err):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seat_layout.duplicate_name", "a layout with this name already exists on this plan", r,
		))
	case err != nil:
		h.logger.Error("seating: rename layout failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to rename the layout", r,
		))
	default:
		h.writeLayout(w, r, http.StatusOK, row)
	}
}

// HandleDeleteSeatLayout serves DELETE .../layouts/{layout_id}.
func (h *Handler) HandleDeleteSeatLayout(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	layoutID, ok := httputil.UUIDPathParam(w, r, "layout_id")
	if !ok {
		return
	}
	var n int64
	err := h.inLayoutTx(r.Context(), func(tx pgx.Tx, txq *gen.Queries) error {
		var err error
		n, err = txq.DeleteSeatLayout(r.Context(), layoutID, orgID, planID)
		if err != nil || n == 0 {
			return err
		}
		return h.writeAuditTx(r.Context(), tx, r, "v1.seating_plan.seat_layout.delete", planID.String(), map[string]any{
			"layout_id": layoutID.String(), "org_id": orgID.String(),
		})
	})
	switch {
	case err != nil:
		h.logger.Error("seating: delete layout failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to delete the layout", r,
		))
	case n == 0:
		writeLayoutNotFound(w, r)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// ─── apply ──────────────────────────────────────────────────────────────────

type seatLayoutApplyRequest struct {
	LayoutID           string            `json:"layout_id"`
	SlotTiers          map[string]string `json:"slot_tiers"`
	CreateMissingTiers bool              `json:"create_missing_tiers"`
	DryRun             bool              `json:"dry_run"`
}

// SeatLayoutApplyResponse is the answer of the apply route (openapi
// SeatLayoutApplyResult). In a dry run nothing was written and BatchID and
// SeatStatusVersion are null.
type SeatLayoutApplyResponse struct {
	DryRun            bool                       `json:"dry_run"`
	LayoutID          string                     `json:"layout_id"`
	Algorithm         *string                    `json:"algorithm"`
	BatchID           *string                    `json:"batch_id"`
	SeatStatusVersion *int64                     `json:"seat_status_version"`
	Slots             []seatlayout.SlotTier      `json:"slots"`
	Changes           []seatlayout.AppliedChange `json:"changes"`
	Skipped           []seatlayout.SkippedSeat   `json:"skipped"`
	UnchangedSeatKeys []string                   `json:"unchanged_seat_keys"`
}

// HandleApplySeatLayout serves POST .../sessions/{session_id}/layouts/apply.
func (h *Handler) HandleApplySeatLayout(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	var req seatLayoutApplyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", "request body is not valid", r, map[string]any{"reason": err.Error()},
		))
		return
	}
	layoutID, err := uuid.Parse(req.LayoutID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.invalid_body", "layout_id must be a valid UUID", r, map[string]any{"field": "layout_id"},
		))
		return
	}
	slotTiers := make(map[string]uuid.UUID, len(req.SlotTiers))
	for slot, id := range req.SlotTiers {
		tid, err := uuid.Parse(id)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seat_layout.invalid_body", "slot_tiers values must be valid UUIDs", r,
				map[string]any{"field": "slot_tiers", "slot": slot},
			))
			return
		}
		slotTiers[slot] = tid
	}

	row, err := h.queries.GetSeatLayoutByID(r.Context(), layoutID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeLayoutNotFound(w, r)
			return
		}
		h.logger.Error("seating: apply layout lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to load the layout", r,
		))
		return
	}
	var payload seatlayout.LayoutPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		h.logger.Error("seating: decode stored layout failed", slog.String("layout_id", layoutID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to read the layout", r,
		))
		return
	}

	res, err := seatlayout.ApplyLayout(r.Context(), h.pool, h.queries, seatlayout.ApplyLayoutInput{
		OrgID: orgID, SessionID: sessionID, PlanID: row.SeatingPlanID, Actor: batchActor(r.Context()),
		Payload: payload, SlotTiers: slotTiers, CreateMissingTiers: req.CreateMissingTiers, DryRun: req.DryRun,
		Audit: func(ctx context.Context, tx pgx.Tx, b seatlayout.Batch) error {
			return h.writeSessionAuditTx(ctx, tx, r, "v1.session.seat_layout.apply", sessionID, map[string]any{
				"batch_id":            b.ID.String(),
				"layout_id":           layoutID.String(),
				"algorithm":           row.Algorithm,
				"seat_status_version": b.SeatStatusVersion,
				"changed_count":       len(b.Changes),
				"skipped_count":       len(b.Skipped),
				"unchanged_count":     len(b.Noops),
			})
		},
	})
	if err != nil {
		h.writeApplyLayoutError(w, r, err)
		return
	}
	out := SeatLayoutApplyResponse{
		DryRun: res.DryRun, LayoutID: layoutID.String(), Algorithm: row.Algorithm,
		Slots: res.Slots, Changes: res.Batch.Changes, Skipped: res.Batch.Skipped,
		UnchangedSeatKeys: make([]string, 0, len(res.Batch.Noops)),
	}
	if out.Slots == nil {
		out.Slots = []seatlayout.SlotTier{}
	}
	if out.Changes == nil {
		out.Changes = []seatlayout.AppliedChange{}
	}
	if out.Skipped == nil {
		out.Skipped = []seatlayout.SkippedSeat{}
	}
	for _, n := range res.Batch.Noops {
		out.UnchangedSeatKeys = append(out.UnchangedSeatKeys, n.SeatKey)
	}
	status := http.StatusCreated
	if res.DryRun {
		status = http.StatusOK
	} else {
		id := res.Batch.ID.String()
		v := res.Batch.SeatStatusVersion
		out.BatchID, out.SeatStatusVersion = &id, &v
	}
	httputil.WriteJSON(w, status, out)
}

func (h *Handler) writeApplyLayoutError(w http.ResponseWriter, r *http.Request, err error) {
	var le *seatlayout.LayoutError
	var un *seatlayout.UnmappedSlotsError
	var ce *seatlayout.SlotCurrencyError
	switch {
	case errors.As(err, &le):
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(le.Code, le.Message, r, le.Details))
	case errors.As(err, &un):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.slots_unmapped",
			"some slots have no category in this session; map them in slot_tiers or set create_missing_tiers", r,
			map[string]any{"slots": un.Slots},
		))
	case errors.As(err, &ce):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seat_layout.currency_mismatch", "a slot is priced in another currency than the session", r,
			map[string]any{"slot": ce.SlotKey, "session_currency": ce.Want, "slot_currency": ce.Got},
		))
	case errors.Is(err, seatlayout.ErrSessionHasNoPlan):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seat_layout.no_seating_plan", "the session has no seating plan", r,
		))
	case errors.Is(err, seatlayout.ErrPlanMismatch):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seat_layout.plan_mismatch", "the layout is for another seating plan than the session's", r,
		))
	case errors.Is(err, seatlayout.ErrSlotTierNotInSession):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seat_layout.tier_not_in_session", "slot_tiers names a category that is not a live category of this session", r,
		))
	default:
		h.writeBatchError(w, r, err)
	}
}

// ─── annotations ────────────────────────────────────────────────────────────

// PlanAnnotationsResponse is the answer of the annotation routes (openapi
// SeatingPlanAnnotations).
type PlanAnnotationsResponse struct {
	SeatingPlanID string                `json:"seating_plan_id"`
	Annotations   layoutgen.Annotations `json:"annotations"`
	// Writable is true for the organization that owns the plan.
	Writable  bool   `json:"writable"`
	UpdatedAt string `json:"updated_at"`
}

func annotationsResponse(row gen.SeatingPlanAnnotationsRow, orgID uuid.UUID) (PlanAnnotationsResponse, error) {
	var ann layoutgen.Annotations
	if len(row.Annotations) > 0 {
		if err := json.Unmarshal(row.Annotations, &ann); err != nil {
			return PlanAnnotationsResponse{}, err
		}
	}
	return PlanAnnotationsResponse{
		SeatingPlanID: row.ID.String(), Annotations: ann, Writable: row.OwnerOrgID == orgID,
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
	}, nil
}

// HandleGetPlanAnnotations serves GET .../seating-plans/{plan_id}/annotations.
func (h *Handler) HandleGetPlanAnnotations(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	row, ok := h.usablePlan(w, r, orgID, planID)
	if !ok {
		return
	}
	resp, err := annotationsResponse(row, orgID)
	if err != nil {
		h.logger.Error("seating: decode annotations failed", slog.String("plan_id", planID.String()), slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to read the annotations", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

var annotationFields = map[string]bool{"focal_point": true, "section_weights": true, "obstructed_seat_keys": true}

// maxObstructedSeats bounds the stored list (a hall of 10 000 seats).
const maxObstructedSeats = 10_000

// HandlePatchPlanAnnotations serves PATCH .../seating-plans/{plan_id}/annotations.
// A key that is absent keeps its stored value, null clears it, a value
// replaces it. The result is checked against the plan's current version.
func (h *Handler) HandlePatchPlanAnnotations(w http.ResponseWriter, r *http.Request) {
	orgID, planID, ok := h.layoutScope(w, r)
	if !ok {
		return
	}
	plan, ok := h.usablePlan(w, r, orgID, planID)
	if !ok {
		return
	}
	if plan.OwnerOrgID != orgID {
		// A shared plan is readable, not editable: fork it (guardrail 13).
		writePlanNotFound(w, r)
		return
	}
	body, ok := readBody(w, r, annotationFields, 1<<20)
	if !ok {
		return
	}
	var cur layoutgen.Annotations
	if len(plan.Annotations) > 0 {
		if err := json.Unmarshal(plan.Annotations, &cur); err != nil {
			h.logger.Error("seating: decode stored annotations failed", slog.String("plan_id", planID.String()), slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seat_layout.failed", "failed to read the annotations", r,
			))
			return
		}
	}
	for key, raw := range body {
		isNull := strings.TrimSpace(string(raw)) == "null"
		var err error
		switch key {
		case "focal_point":
			cur.FocalPoint = nil
			if !isNull {
				var p seating.Point
				if err = decodeStrict(raw, &p); err == nil {
					cur.FocalPoint = &p
				}
			}
		case "section_weights":
			cur.SectionWeights = nil
			if !isNull {
				var m map[string]float64
				if err = decodeStrict(raw, &m); err == nil {
					cur.SectionWeights = m
				}
			}
		case "obstructed_seat_keys":
			cur.ObstructedSeatKeys = nil
			if !isNull {
				var l []string
				if err = decodeStrict(raw, &l); err == nil {
					if len(l) > maxObstructedSeats {
						err = errors.New("too many seats")
					}
					cur.ObstructedSeatKeys = l
				}
			}
		}
		if err != nil {
			invalidField(w, r, key, "a valid value of the documented shape")
			return
		}
	}
	if err := cur.Validate(); err != nil {
		writeLayoutError(w, r, h.logger, err)
		return
	}
	if plan.CurrentVersionID != nil {
		ver, err := h.queries.GetSeatingPlanVersionByID(r.Context(), *plan.CurrentVersionID)
		if err == nil {
			var g seating.Geometry
			if json.Unmarshal(ver.Geometry, &g) == nil {
				if err := cur.CheckGeometry(g); err != nil {
					writeLayoutError(w, r, h.logger, err)
					return
				}
			}
		}
	}
	stored, err := json.Marshal(cur)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to encode the annotations", r,
		))
		return
	}
	var row gen.SeatingPlanAnnotationsRow
	err = h.inLayoutTx(r.Context(), func(tx pgx.Tx, txq *gen.Queries) error {
		var err error
		row, err = txq.SetSeatingPlanAnnotations(r.Context(), planID, orgID, stored)
		if err != nil {
			return err
		}
		return h.writeAuditTx(r.Context(), tx, r, "v1.seating_plan.annotations.update", planID.String(), map[string]any{
			"has_focal_point": cur.FocalPoint != nil, "section_weights": len(cur.SectionWeights),
			"obstructed_seats": len(cur.ObstructedSeatKeys),
		})
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writePlanNotFound(w, r)
			return
		}
		h.logger.Error("seating: save annotations failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to save the annotations", r,
		))
		return
	}
	resp, err := annotationsResponse(row, orgID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_layout.failed", "failed to read the annotations", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

func decodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
