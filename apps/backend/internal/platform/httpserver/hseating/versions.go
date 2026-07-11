// versions.go implements the seating_plan_versions create + read endpoints.
package hseating

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// versionBodyLimit caps the JSON body for the version-create endpoint. Raw
// SVG payloads can be sizable; 4 MiB matches the media-store upload ceiling
// used elsewhere in the platform for authoring assets.
const versionBodyLimit = 4 * 1024 * 1024

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/seating-plans/{id}/versions
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreateSeatingPlanVersion serves the version-create endpoint. The body
// carries either `svg` (raw SVG string parsed by the domain importer) or a
// pre-built `geometry` JSON object. Validation errors from the importer are
// surfaced as 422 with per-element details so the editor can highlight the
// offending SVG nodes.
func (h *Handler) HandleCreateSeatingPlanVersion(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	planID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	fields, ok := readBody(w, r, versionCreateFields, versionBodyLimit)
	if !ok {
		return
	}
	svg, svgPresent, ok := stringField(fields, "svg")
	if !ok {
		invalidField(w, r, "svg", "a string")
		return
	}
	geomRaw, geomPresent := fields["geometry"]
	if svgPresent == geomPresent {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating_plan.version_body_invalid",
			"exactly one of svg or geometry must be supplied",
			r,
		))
		return
	}

	var (
		geometry seating.Geometry
		imported ImportOutcome
	)
	if svgPresent && svg != nil {
		g, warnings, errs := seating.ImportSVG([]byte(*svg))
		if len(errs) > 0 {
			details := make([]map[string]any, 0, len(errs))
			for _, e := range errs {
				details = append(details, map[string]any{
					"code":    e.Code,
					"element": e.Element,
					"detail":  e.Detail,
				})
			}
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"seating_plan.version_validation_failed",
				"seating plan version failed geometry validation",
				r,
				map[string]any{"errors": details},
			))
			return
		}
		geometry = g
		imported = ImportOutcome{Warnings: warnings}
	} else {
		var g seating.Geometry
		if err := json.Unmarshal(geomRaw, &g); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"seating_plan.version_body_invalid",
				"geometry field is not a valid canonical geometry object: "+err.Error(),
				r,
			))
			return
		}
		geometry = seating.Canonicalize(g)
	}

	checksum, err := seating.Checksum(geometry)
	if err != nil {
		h.logger.Error("seating_plan: checksum failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to compute geometry checksum", r,
		))
		return
	}
	canonicalJSON, err := seating.CanonicalJSON(geometry)
	if err != nil {
		h.logger.Error("seating_plan: canonical marshal failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to marshal canonical geometry", r,
		))
		return
	}

	var svgAssetID *uuid.UUID
	if s, _, sOK := stringField(fields, "svg_asset_media_id"); sOK && s != nil {
		id, err := uuid.Parse(*s)
		if err != nil {
			invalidField(w, r, "svg_asset_media_id", "a UUID string")
			return
		}
		svgAssetID = &id
	}
	capacityStanding, capacityStandingPresent, ok := intField(fields, "capacity_standing")
	if !ok {
		invalidField(w, r, "capacity_standing", "an integer")
		return
	}
	if !capacityStandingPresent {
		capacityStanding = 0
	}
	// SeatCount() is derived from a validated Geometry payload whose seat
	// count is bounded by the §5.3 canvas limits — well below math.MaxInt32
	// — so the narrowing conversion is safe.
	seatCount := geometry.SeatCount()
	if seatCount < 0 || seatCount > math.MaxInt32 {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seating_plan.capacity_overflow",
			"imported geometry exceeds the supported seat-count range",
			r,
		))
		return
	}
	capacitySeated := int32(seatCount) //nolint:gosec // bound-checked above

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	plan, err := qtx.GetSeatingPlanByID(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.not_found", "seating plan not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: pre-version get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to create seating plan version", r,
		))
		return
	}
	latest, err := qtx.GetLatestSeatingPlanVersionNumber(ctx, plan.ID)
	if err != nil {
		h.logger.Error("seating_plan: latest version lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to create seating plan version", r,
		))
		return
	}
	nextVersion := latest + 1

	row, err := qtx.InsertSeatingPlanVersion(
		ctx, plan.ID, nextVersion, canonicalJSON, checksum,
		svgAssetID, capacitySeated, capacityStanding,
	)
	if err != nil {
		h.logger.Error("seating_plan: version insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to create seating plan version", r,
		))
		return
	}
	plan, err = qtx.SetSeatingPlanCurrentVersion(ctx, plan.ID, plan.OwnerOrgID, &row.ID)
	if err != nil {
		h.logger.Error("seating_plan: set current version failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_create_failed", "failed to set current seating plan version", r,
		))
		return
	}
	if err := h.writeAuditTx(ctx, tx, r, "v1.seating_plan.version.create", row.ID.String(), map[string]any{
		"seating_plan_id":   plan.ID.String(),
		"owner_org_id":      plan.OwnerOrgID.String(),
		"version_number":    row.VersionNumber,
		"geometry_checksum": row.GeometryChecksum,
		"capacity_seated":   row.CapacitySeated,
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

	response := map[string]any{
		"seating_plan":         SeatingPlanFromRow(plan),
		"seating_plan_version": SeatingPlanVersionFromRow(row),
	}
	if len(imported.Warnings) > 0 {
		warns := make([]map[string]any, 0, len(imported.Warnings))
		for _, wn := range imported.Warnings {
			warns = append(warns, map[string]any{
				"code":    wn.Code,
				"element": wn.Element,
				"detail":  wn.Detail,
			})
		}
		response["warnings"] = warns
	}
	httputil.WriteJSON(w, http.StatusCreated, response)
}

// ImportOutcome bundles the non-error import channel (warnings) so the
// version handler can echo import advisories back to the caller.
type ImportOutcome struct {
	Warnings []seating.ValidationError
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/seating-plans/{id}/versions/{n}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetSeatingPlanVersion serves the read-one-version endpoint. `n` is a
// 1-based positional version number scoped to the plan; not a version UUID.
func (h *Handler) HandleGetSeatingPlanVersion(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()
	planID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	nStr := chi.URLParam(r, "n")
	n, err := strconv.Atoi(nStr)
	if err != nil || n < 1 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating_plan.invalid_version_number",
			"version number must be a positive integer", r,
			map[string]any{"field": "n"},
		))
		return
	}
	if n > math.MaxInt32 {
		// version_number is an int32 column; anything above the range
		// cannot exist, so answer 404 without touching the database.
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"seating_plan.version_not_found", "seating plan version not found", r,
		))
		return
	}
	v, err := h.queries.GetSeatingPlanVersionByNumber(ctx, planID, int32(n)) //nolint:gosec // G109 false positive: n is bounded by the math.MaxInt32 guard above
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"seating_plan.version_not_found", "seating plan version not found", r,
			))
			return
		}
		h.logger.Error("seating_plan: version get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating_plan.version_get_failed", "failed to get seating plan version", r,
		))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"seating_plan_version": SeatingPlanVersionFromRow(v),
	})
}
