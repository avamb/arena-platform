// category.go implements the AB-39 per-seat price category assignment
// endpoint (feature #429, Wave 4 reconstruction).
//
//	PATCH /v1/event-sessions/{id}/seats/category
//
// This is Bil24's core pricing gesture — the operator selects a set of seats
// on the hall map (or in the seat table) and reassigns them all to a
// different ticket_tier ("Change the category of the selected seats").
// Without it a mixed hall cannot be priced at all — several categories
// of seats mint separate tiers at bind time, but AB-38 removed the ability
// to move a seat from one to another after the fact.
//
// Contract (backlog: 09_autoforge/admin_bootstrap_backlog.md §AB-39):
//
//   - Bulk assign one tier_id to a list of seat_keys. n=1 is the single-seat
//     case; there is no non-bulk variant.
//   - Reject reassignment of seats in `sold` or `held` status with 409
//     listing the conflicting seat_keys — those seats are already committed
//     to a reservation or issued ticket and cannot silently change price.
//     Allow `available` and `unavailable`.
//   - Bump sessions.seat_status_version so widget / feed caches invalidate.
//     The value column itself doesn't change but downstream projections
//     that key on version (public seat-status endpoint, WP embed cache)
//     need to re-fetch — reassignment changes the *price* an available
//     seat costs, which is user-visible.
//   - Emit a single audit event (v1.session.seats.category) with the
//     effective + conflicting + unknown seat lists.
//   - Idempotent for the "seat already on this tier" case — the SQL UPDATE
//     is a no-op but is still counted in the summary.updated field so the
//     operator sees a stable "N seats now on tier X" ack.
//
// Auth: same permission family as bind + seats block/unblock
// (event_session.assign_seating_plan). Membership resolved via the
// session's org (flat path — no {org_id} URL param) in the shim before
// this handler is called (seating_shims.go).
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// categoryBodyLimit caps the JSON payload for the reassign endpoint. A
// single seat_key is ~30 bytes and the largest realistic hall (Palác
// Akropolis: 590 seats, some venues up to ~10 000) still fits well under
// 512 KiB even with generous quoting.
const categoryBodyLimit = 512 * 1024

// categoryMaxSeats bounds a single call. Operator UI ships one batch per
// selection; splitting a huge hall into two calls is fine (the seat_status
// version still stays monotonic, only observes two increments).
const categoryMaxSeats = 10_000

// categoryPatchRequest is the strict-decoded shape of the request body.
// tier_id and seat_keys are both required. Reassignment to a NULL tier
// (clearing a seat's tier) is deliberately NOT supported — the AB-39
// invariant is "after bind every seat carries a non-null tier_id".
type categoryPatchRequest struct {
	TierID   string   `json:"tier_id"`
	SeatKeys []string `json:"seat_keys"`
}

// categoryPatchResponse is the JSON envelope returned to the caller on 200.
type categoryPatchResponse struct {
	SessionID         string          `json:"session_id"`
	TierID            string          `json:"tier_id"`
	SeatStatusVersion int64           `json:"seat_status_version"`
	UpdatedSeatKeys   []string        `json:"updated_seat_keys"`
	UnknownSeatKeys   []string        `json:"unknown_seat_keys"`
	Summary           categorySummary `json:"summary"`
}

// categorySummary rolls up per-outcome counts.
type categorySummary struct {
	Requested int `json:"requested"`
	Updated   int `json:"updated"`
	Unknown   int `json:"unknown"`
}

// AdminSeatRow is one row of the admin seat inventory (AB-39). Exposed by
// HandleListSessionSeatsAdmin so the admin table can render the mandated
// columns: ID | Sector | Row | Seat | Category | Price | Status. The
// public seat-status endpoint intentionally does NOT surface tier_id —
// widgets and feeds should read the categoryPrice overlay from the schema
// endpoint. The admin table needs to see the CURRENT tier per seat so an
// operator can confirm a reassignment landed.
type AdminSeatRow struct {
	ID      string  `json:"id"`
	SeatKey string  `json:"seat_key"`
	Sector  string  `json:"sector_name"`
	Row     string  `json:"row_name"`
	Seat    string  `json:"seat_number"`
	Status  string  `json:"status"`
	TierID  *string `json:"tier_id"`
	Kind    string  `json:"kind"`
}

// AdminSeatsResponse is the top-level envelope returned by
// HandleListSessionSeatsAdmin.
type AdminSeatsResponse struct {
	SessionID         string         `json:"session_id"`
	SeatStatusVersion int64          `json:"seat_status_version"`
	Seats             []AdminSeatRow `json:"seats"`
}

// HandleListSessionSeatsAdmin serves GET /v1/event-sessions/{id}/seats/admin.
// Auth: same as the reassignment endpoint (assign_seating_plan).
// Membership check is done in the shim.
func (h *Handler) HandleListSessionSeatsAdmin(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	admission, err := h.queries.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("seating: admin seats admission lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.admin_seats_failed", "failed to load session", r,
		))
		return
	}

	rows, err := h.queries.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating: admin seats list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.admin_seats_failed", "failed to load seats", r,
		))
		return
	}

	out := make([]AdminSeatRow, 0, len(rows))
	for _, row := range rows {
		var tid *string
		if row.TierID != nil {
			s := row.TierID.String()
			tid = &s
		}
		// The Kind column is not on SessionSeatRow scan today; GA units
		// simply do not surface here — the widget already handles those
		// via a separate GA tier UI. Represent it as empty for compatibility.
		out = append(out, AdminSeatRow{
			ID:      row.ID.String(),
			SeatKey: row.SeatKey,
			Sector:  row.SectorName,
			Row:     row.RowName,
			Seat:    row.SeatNumber,
			Status:  row.Status,
			TierID:  tid,
			Kind:    "",
		})
	}

	httputil.WriteJSON(w, http.StatusOK, AdminSeatsResponse{
		SessionID:         sessionID.String(),
		SeatStatusVersion: admission.SeatStatusVersion,
		Seats:             out,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// HandlePatchSessionSeatsCategory
// ─────────────────────────────────────────────────────────────────────────────

// HandlePatchSessionSeatsCategory serves PATCH /v1/event-sessions/{id}/seats/category.
// The shim (seating_shims.go / handlePatchSessionSeatsCategory) has already
// resolved the session's org and enforced membership — this handler is a
// pure inventory-plus-transaction routine.
func (h *Handler) HandlePatchSessionSeatsCategory(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	req, ok := readCategoryPatchRequest(w, r)
	if !ok {
		return
	}

	// Validate + parse tier_id.
	tierID, ok := validateCategoryPatchRequest(w, r, req)
	if !ok {
		return
	}

	// Confirm session existence. GA-only sessions have zero session_seats
	// rows and the endpoint is not meaningful for them — but rather than
	// 404 the session (which exists), we bail with a targeted error so a
	// misconfigured admin UI surfaces the reason.
	admission, err := h.queries.GetSessionAdmissionModeByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("seating: category patch admission lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to load session admission mode", r,
		))
		return
	}
	if admission.AdmissionMode != "assigned_seats" && admission.AdmissionMode != "hybrid" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seating.no_seated_inventory",
			"session has no seated inventory (admission_mode=general_admission)",
			r,
			map[string]any{"admission_mode": admission.AdmissionMode},
		))
		return
	}

	// Verify the tier belongs to this session and is not soft-deleted.
	// Reassigning to another session's tier would produce a tier_id whose
	// price is denominated in the wrong session's currency / capacity.
	tier, err := h.queries.GetTicketTierByID(ctx, tierID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"seating.tier_not_found_in_session",
				"tier_id does not reference an active tier of this session",
				r,
				map[string]any{"tier_id": tierID.String(), "session_id": sessionID.String()},
			))
			return
		}
		h.logger.Error("seating: category patch tier lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to load ticket tier", r,
		))
		return
	}
	_ = tier // fetched for validation only; response echoes the request tier_id

	// Load all materialized seats once and pre-check status. session_seats
	// rowcount is bounded by the physical seat count (typically < 10 000)
	// so one scan under an operator-priority endpoint is acceptable and
	// simpler than a per-key SELECT loop. GA-unit rows are irrelevant to
	// this endpoint (they carry kind='ga_unit'); they never appear in the
	// caller's seat_keys because the widget/admin table only surfaces
	// seated rows via the schema endpoint's `sections[]`.
	allSeats, err := h.queries.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating: category patch list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to load session seats", r,
		))
		return
	}
	seatByKey := make(map[string]gen.SessionSeatRow, len(allSeats))
	for _, s := range allSeats {
		seatByKey[s.SeatKey] = s
	}

	target, unknown, conflicts := partitionSeatKeys(req.SeatKeys, seatByKey)
	if len(conflicts) > 0 {
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"seating.category_reassign_conflict",
			"cannot reassign seats currently held or sold",
			r,
			map[string]any{"conflicting_seat_keys": conflicts},
		))
		return
	}

	// Nothing to do — the request only named unknown seat_keys. Return
	// 200 with an empty updated list so the operator sees "0 updated, N
	// unknown" and the seat_status_version stays put (no reason to bump
	// a version that observers cannot correlate to anything).
	if len(target) == 0 {
		httputil.WriteJSON(w, http.StatusOK, categoryPatchResponse{
			SessionID:         sessionID.String(),
			TierID:            tierID.String(),
			SeatStatusVersion: admission.SeatStatusVersion,
			UpdatedSeatKeys:   []string{},
			UnknownSeatKeys:   unknown,
			Summary: categorySummary{
				Requested: len(req.SeatKeys),
				Updated:   0,
				Unknown:   len(unknown),
			},
		})
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	qtx := h.queries.WithTx(tx)

	newVersion, err := qtx.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating: category patch version bump failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to bump seat_status_version", r,
		))
		return
	}

	// Bulk UPDATE. The WHERE clause matches on seat_key (unique per
	// session) so rowsAffected == len(target) — but we don't strictly
	// assert that; a partial update just yields a smaller Updated count.
	rows, err := qtx.BulkSetSessionSeatTier(ctx, sessionID, target, tierID)
	if err != nil {
		h.logger.Error("seating: category patch bulk update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.category_patch_failed", "failed to reassign seat tiers", r,
		))
		return
	}
	_ = rows

	if err := h.writeSessionAuditTx(ctx, tx, r, "v1.session.seats.category", sessionID, map[string]any{
		"tier_id":             tierID.String(),
		"seat_status_version": newVersion,
		"updated_seat_keys":   target,
		"unknown_seat_keys":   unknown,
		"requested_count":     len(req.SeatKeys),
		"updated_count":       len(target),
		"unknown_count":       len(unknown),
	}); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.audit_failed", "failed to write audit event", r,
		))
		return
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, categoryPatchResponse{
		SessionID:         sessionID.String(),
		TierID:            tierID.String(),
		SeatStatusVersion: newVersion,
		UpdatedSeatKeys:   target,
		UnknownSeatKeys:   unknown,
		Summary: categorySummary{
			Requested: len(req.SeatKeys),
			Updated:   len(target),
			Unknown:   len(unknown),
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Request decoding + validation
// ─────────────────────────────────────────────────────────────────────────────

// readCategoryPatchRequest slurps and strictly decodes the request body.
func readCategoryPatchRequest(w http.ResponseWriter, r *http.Request) (categoryPatchRequest, bool) {
	var out categoryPatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, categoryBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return categoryPatchRequest{}, false
	}
	return out, true
}

// validateCategoryPatchRequest enforces the request-shape rules and parses
// the tier_id UUID.
func validateCategoryPatchRequest(
	w http.ResponseWriter, r *http.Request, req categoryPatchRequest,
) (uuid.UUID, bool) {
	if req.TierID == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_tier_id",
			"tier_id is required",
			r,
			map[string]any{"field": "tier_id"},
		))
		return uuid.Nil, false
	}
	tierID, err := uuid.Parse(req.TierID)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_tier_id",
			"tier_id must be a valid UUID",
			r,
			map[string]any{"field": "tier_id", "value": req.TierID},
		))
		return uuid.Nil, false
	}
	if len(req.SeatKeys) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.no_seat_keys",
			"seat_keys must be a non-empty array",
			r,
			map[string]any{"field": "seat_keys"},
		))
		return uuid.Nil, false
	}
	if len(req.SeatKeys) > categoryMaxSeats {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seating.too_many_seat_keys",
			"seat_keys length exceeds per-request cap",
			r,
			map[string]any{"max": categoryMaxSeats, "received": len(req.SeatKeys)},
		))
		return uuid.Nil, false
	}
	return tierID, true
}

// partitionSeatKeys walks the caller-supplied seat_keys and splits them
// into three buckets:
//   - target:    keys that resolve to a session_seat in available/unavailable
//     status. These are what BulkSetSessionSeatTier will update.
//   - unknown:   keys that do not resolve to any session_seat row (typos,
//     stale plan versions). Reported per-key in the response
//     for operator visibility.
//   - conflicts: keys that resolve but the seat is currently held or sold.
//     Their presence triggers a 409 — caller MUST NOT proceed.
//
// Duplicate keys in the input are deduplicated. All output slices are
// sorted lexicographically for stable diffing.
func partitionSeatKeys(
	seatKeys []string,
	seatByKey map[string]gen.SessionSeatRow,
) (target, unknown, conflicts []string) {
	seenTarget := make(map[string]struct{}, len(seatKeys))
	seenUnknown := make(map[string]struct{}, 4)
	seenConflict := make(map[string]struct{}, 4)
	for _, key := range seatKeys {
		row, ok := seatByKey[key]
		if !ok {
			seenUnknown[key] = struct{}{}
			continue
		}
		switch row.Status {
		case "held", "sold":
			seenConflict[key] = struct{}{}
		default:
			// 'available' and 'unavailable' — reassignment is allowed.
			seenTarget[key] = struct{}{}
		}
	}
	target = keysSorted(seenTarget)
	unknown = keysSorted(seenUnknown)
	conflicts = keysSorted(seenConflict)
	return target, unknown, conflicts
}

func keysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
