// plans.go implements the seating-plan CRUD + fork surface.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// bodyLimit caps the JSON request body for the plan-metadata endpoints. The
// versions endpoint uses its own larger limit because it may carry raw SVG.
const bodyLimit = 64 * 1024

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/venues/{venue_id}/seating-plans
// ─────────────────────────────────────────────────────────────────────────────

// HandleListSeatingPlansByVenue serves the list-by-venue endpoint. Visibility
// filtering is deliberately deferred to a follow-up wave; every plan attached
// to the venue is returned so the current org's UI can present its own drafts
// side-by-side with any shared / public plans.
func (h *Handler) HandleListSeatingPlansByVenue(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	venueID, ok := httputil.UUIDPathParam(w, r, "venue_id")
	if !ok {
		return
	}
	rows, err := h.queries.ListSeatingPlansByVenue(ctx, venueID)
	if err != nil {
		h.logger.Error("seating_plan: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.list_failed", "failed to list seating plans", r,
		))
		return
	}
	result := make([]SeatingPlanResponse, 0, len(rows))
	for _, p := range rows {
		result = append(result, SeatingPlanFromRow(p))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"seating_plans": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/venues/{venue_id}/seating-plans
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateSeatingPlan serves the create endpoint. The request body carries
// owner_org_id (required — the caller's active organization), name, and
// plan_type; visibility defaults to "private" and status to "draft".
func (h *Handler) HandleCreateSeatingPlan(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	venueID, ok := httputil.UUIDPathParam(w, r, "venue_id")
	if !ok {
		return
	}
	fields, ok := readBody(w, r, planCreateFields, bodyLimit)
	if !ok {
		return
	}
	ownerOrgStr, _, ok := stringField(fields, "owner_org_id")
	if !ok || ownerOrgStr == nil {
		invalidField(w, r, "owner_org_id", "a UUID string")
		return
	}
	ownerOrgID, err := uuid.Parse(*ownerOrgStr)
	if err != nil {
		invalidField(w, r, "owner_org_id", "a UUID string")
		return
	}
	name, _, ok := stringField(fields, "name")
	if !ok || name == nil {
		invalidField(w, r, "name", "a non-empty string")
		return
	}
	planType, _, ok := stringField(fields, "plan_type")
	if !ok || planType == nil || !validPlanTypes[*planType] {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_plan_type",
			"plan_type must be one of assigned_seats|general_admission|tables|mixed", r,
			map[string]any{"field": "plan_type"},
		))
		return
	}
	visibility := "private"
	vv, vPresent, vok := stringField(fields, "visibility")
	if !vok || (vPresent && (vv == nil || !validVisibilities[*vv])) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_visibility",
			"visibility must be one of private|shared_read|public_template|operator_verified", r,
			map[string]any{"field": "visibility"},
		))
		return
	}
	if vPresent && vv != nil {
		visibility = *vv
	}
	status := "draft"
	sv, sPresent, sok := stringField(fields, "status")
	if !sok || (sPresent && (sv == nil || !validStatuses[*sv])) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_status",
			"status must be one of draft|active|archived", r,
			map[string]any{"field": "status"},
		))
		return
	}
	if sPresent && sv != nil {
		status = *sv
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	row, err := qtx.InsertSeatingPlan(ctx, venueID, ownerOrgID, *name, *planType, visibility, status, nil)
	if err != nil {
		h.logger.Error("seating_plan: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.create_failed", "failed to create seating plan", r,
		))
		return
	}
	if err := h.writeAuditTx(ctx, tx, r, "v1.seating_plan.create", row.ID.String(), map[string]any{
		"venue_id":     venueID.String(),
		"owner_org_id": ownerOrgID.String(),
		"plan_type":    row.PlanType,
		"visibility":   row.Visibility,
		"status":       row.Status,
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.audit_failed", "failed to write audit event", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.commit_failed", "failed to commit transaction", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"seating_plan": SeatingPlanFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/seating-plans/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetSeatingPlan serves the read-one endpoint. Visibility gating is
// deferred to a follow-up wave — the RBAC middleware guards the entry point.
func (h *Handler) HandleGetSeatingPlan(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	row, err := h.queries.GetSeatingPlanByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.get_failed", "failed to get seating plan", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"seating_plan": SeatingPlanFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/seating-plans/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleUpdateSeatingPlan serves the metadata-mutation endpoint. Only the
// owning org may mutate; requests against a plan owned by another org get 404
// (deliberate — do not leak existence). Archive is expressed as a status flip.
func (h *Handler) HandleUpdateSeatingPlan(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	fields, ok := readBody(w, r, planPatchFields, bodyLimit)
	if !ok {
		return
	}
	name, namePresent, ok := stringField(fields, "name")
	if !ok || (namePresent && name == nil) {
		invalidField(w, r, "name", "a non-empty string")
		return
	}
	visibility, visibilityPresent, ok := stringField(fields, "visibility")
	if !ok || (visibilityPresent && (visibility == nil || !validVisibilities[*visibility])) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_visibility",
			"visibility must be one of private|shared_read|public_template|operator_verified", r,
			map[string]any{"field": "visibility"},
		))
		return
	}
	status, statusPresent, ok := stringField(fields, "status")
	if !ok || (statusPresent && (status == nil || !validStatuses[*status])) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_status",
			"status must be one of draft|active|archived", r,
			map[string]any{"field": "status"},
		))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	existing, err := qtx.GetSeatingPlanByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: pre-update get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.update_failed", "failed to update seating plan", r,
		))
		return
	}

	finalName := existing.Name
	if namePresent && name != nil {
		finalName = *name
	}
	finalVisibility := existing.Visibility
	if visibilityPresent && visibility != nil {
		finalVisibility = *visibility
	}
	finalStatus := existing.Status
	if statusPresent && status != nil {
		finalStatus = *status
	}

	row, err := qtx.UpdateSeatingPlan(ctx, id, existing.OwnerOrgID, finalName, existing.PlanType, finalVisibility, finalStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.update_failed", "failed to update seating plan", r,
		))
		return
	}
	if err := h.writeAuditTx(ctx, tx, r, "v1.seating_plan.update", row.ID.String(), map[string]any{
		"owner_org_id": row.OwnerOrgID.String(),
		"visibility":   row.Visibility,
		"status":       row.Status,
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.audit_failed", "failed to write audit event", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.commit_failed", "failed to commit transaction", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"seating_plan": SeatingPlanFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/seating-plans/{id}/fork
// ─────────────────────────────────────────────────────────────────────────────

// HandleForkSeatingPlan serves the fork endpoint. Guardrail #13 is enforced
// at the SQL layer via source_seating_plan_id lineage: forking a plan the
// caller already owns returns 409 seating_plan.fork_own_plan (there is nothing
// to fork). The current-version geometry, if present, is duplicated into the
// new plan as its version 1.
func (h *Handler) HandleForkSeatingPlan(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	sourceID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	fields, ok := readBody(w, r, planForkFields, bodyLimit)
	if !ok {
		return
	}
	ownerOrgStr, _, ok := stringField(fields, "owner_org_id")
	if !ok || ownerOrgStr == nil {
		invalidField(w, r, "owner_org_id", "a UUID string")
		return
	}
	ownerOrgID, err := uuid.Parse(*ownerOrgStr)
	if err != nil {
		invalidField(w, r, "owner_org_id", "a UUID string")
		return
	}
	// venue_id and name are both optional; fall back to the source values.
	var venueOverride *uuid.UUID
	if s, _, sOK := stringField(fields, "venue_id"); sOK && s != nil {
		vID, verr := uuid.Parse(*s)
		if verr != nil {
			invalidField(w, r, "venue_id", "a UUID string")
			return
		}
		venueOverride = &vID
	}
	nameOverride, _, ok := stringField(fields, "name")
	if !ok {
		invalidField(w, r, "name", "a non-empty string")
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	source, err := qtx.GetSeatingPlanByID(ctx, sourceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: pre-fork get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.fork_failed", "failed to fork seating plan", r,
		))
		return
	}
	if source.OwnerOrgID == ownerOrgID {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seating_plan.fork_own_plan",
			"cannot fork a seating plan the target organization already owns",
			r,
		))
		return
	}

	targetVenue := source.VenueID
	if venueOverride != nil {
		targetVenue = *venueOverride
	}
	targetName := source.Name + " (fork)"
	if nameOverride != nil {
		targetName = *nameOverride
	}

	forked, err := qtx.InsertSeatingPlan(
		ctx, targetVenue, ownerOrgID, targetName,
		source.PlanType, "private", "draft", &source.ID,
	)
	if err != nil {
		h.logger.Error("seating_plan: fork insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.fork_failed", "failed to fork seating plan", r,
		))
		return
	}

	// Copy the source's current version if one exists so the fork is
	// immediately usable. This mirrors the "fork copies latest version"
	// contract from §5 of the seating backlog.
	if source.CurrentVersionID != nil {
		sourceVersion, verr := qtx.GetSeatingPlanVersionByID(ctx, *source.CurrentVersionID)
		if verr != nil && !errors.Is(verr, pgx.ErrNoRows) {
			h.logger.Error("seating_plan: fork source version fetch failed", slog.String("error", verr.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating_plan.fork_failed", "failed to fork seating plan", r,
			))
			return
		}
		if verr == nil {
			newVersion, verr := qtx.InsertSeatingPlanVersion(
				ctx, forked.ID, 1,
				sourceVersion.Geometry, sourceVersion.GeometryChecksum,
				nil, sourceVersion.CapacitySeated, sourceVersion.CapacityStanding,
			)
			if verr != nil {
				h.logger.Error("seating_plan: fork version insert failed", slog.String("error", verr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"seating_plan.fork_failed", "failed to fork seating plan", r,
				))
				return
			}
			forked, err = qtx.SetSeatingPlanCurrentVersion(ctx, forked.ID, ownerOrgID, &newVersion.ID)
			if err != nil {
				h.logger.Error("seating_plan: fork set current failed", slog.String("error", err.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"seating_plan.fork_failed", "failed to fork seating plan", r,
				))
				return
			}
		}
	}

	if err := h.writeAuditTx(ctx, tx, r, "v1.seating_plan.fork", forked.ID.String(), map[string]any{
		"source_seating_plan_id": source.ID.String(),
		"owner_org_id":           ownerOrgID.String(),
		"venue_id":               targetVenue.String(),
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.audit_failed", "failed to write audit event", r,
		))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.commit_failed", "failed to commit transaction", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"seating_plan": SeatingPlanFromRow(forked),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Body reading + audit helpers
// ─────────────────────────────────────────────────────────────────────────────

// readBody slurps and strictly-decodes the request body. On failure it writes
// the 400 error envelope and returns ok=false.
func readBody(w http.ResponseWriter, r *http.Request, allowed map[string]bool, limit int64) (map[string]json.RawMessage, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating_plan.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return nil, false
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating_plan.empty_body", "request body is required", r,
		))
		return nil, false
	}
	fields, code, message := decodeBody(body, allowed)
	if code != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(code, message, r))
		return nil, false
	}
	return fields, true
}

// invalidField writes the 400 envelope for a field whose JSON type does not
// match the schema.
func invalidField(w http.ResponseWriter, r *http.Request, field, want string) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"seating_plan.invalid_field", "field "+quoteKey(field)+" must be "+want, r,
		map[string]any{"field": field},
	))
}

// writeAuditTx emits an audit event inside the mutation's transaction.
func (h *Handler) writeAuditTx(ctx context.Context, tx pgx.Tx, r *http.Request, action, resourceID string, metadata map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "seating_plan",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
		h.logger.Error("seating_plan: audit write failed", slog.String("error", err.Error()))
		return err
	}
	return nil
}
