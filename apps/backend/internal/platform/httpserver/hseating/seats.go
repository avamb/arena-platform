// seats.go implements the operator seat block/unblock endpoint
// (feature #308, Wave SEAT-B4).
//
//	PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seats
//
// The request accepts three mutually-cooperative selectors — `seat_keys`,
// `sectors`, and `rows` — that are expanded server-side to the concrete
// set of session_seats rows to transition. Only the two admin transitions
// available↔blocked are attempted; seats in held/sold status are skipped
// per-seat with a reason and are never silently mutated. Re-blocking an
// already-blocked seat is a documented no-op (idempotent).
//
// Contract source: 09_autoforge/seating_backlog.md §7 SEAT-B4.
//
//   - Requires the `event_session.assign_seating_plan` permission (same
//     operational role used for SEAT-B2 binding).
//   - Every request emits one audit event with the seat-key list + actor.
//   - Blocked seats surface as `blocked` in the seat-status endpoint
//     (SEAT-B3), map to BSS `0 INACCESSIBLE` in the Bil24 gateway (future
//     Wave SEAT-D), are excluded from availability counters, and cannot
//     be reserved (409 `reservation.seats_conflict` in Wave SEAT-C1).
//   - Blocking does NOT shrink `sessions.capacity_total` — it is a sales
//     hold, not a capacity change.
//   - The transaction bumps `sessions.seat_status_version` exactly once
//     and stamps every mutated row with the new value so delta seat-status
//     pollers observe a single monotonic step per operator request.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// seatsBodyLimit caps the JSON payload for the block/unblock endpoint.
// A single seat_key is ~30 bytes and the largest realistic bulk operator
// action stays well under 512 KiB even for a 10 000-seat arena.
const seatsBodyLimit = 512 * 1024

// seatsPatchAction is the enum of admin actions accepted by the endpoint.
const (
	seatsActionBlock   = "block"
	seatsActionUnblock = "unblock"
)

// per-seat outcome codes emitted in the response.
const (
	seatOutcomeBlocked   = "blocked"
	seatOutcomeUnblocked = "unblocked"
	seatOutcomeNoop      = "noop"
	seatOutcomeSkipped   = "skipped"
)

// reasons attached to skipped outcomes.
const (
	seatReasonHeld     = "held"
	seatReasonSold     = "sold"
	seatReasonNotFound = "seat_not_found"
)

// seatsRowSelector picks every seat in a single (sector, row) pair.
type seatsRowSelector struct {
	Sector string `json:"sector"`
	Row    string `json:"row"`
}

// seatsPatchRequest is the strict-decoded shape of the request body.
// At least one selector list MUST be non-empty; every list is optional
// on its own so operators can combine "block sector A + row B/12" in a
// single call.
type seatsPatchRequest struct {
	Action   string             `json:"action"`
	SeatKeys []string           `json:"seat_keys,omitempty"`
	Sectors  []string           `json:"sectors,omitempty"`
	Rows     []seatsRowSelector `json:"rows,omitempty"`
}

// seatOutcome documents the fate of a single seat inside the response.
// Reason is populated only when Outcome == seatOutcomeSkipped (or for
// missing seats when the caller supplied an unknown seat_key).
type seatOutcome struct {
	SeatKey string `json:"seat_key"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
	Status  string `json:"status"`
}

// seatsPatchResponse is the JSON envelope returned to the caller.
type seatsPatchResponse struct {
	SessionID         string        `json:"session_id"`
	Action            string        `json:"action"`
	SeatStatusVersion int64         `json:"seat_status_version"`
	Outcomes          []seatOutcome `json:"outcomes"`
	Summary           seatsSummary  `json:"summary"`
}

// seatsSummary rolls up per-outcome counts for the response envelope.
type seatsSummary struct {
	Requested int `json:"requested"`
	Changed   int `json:"changed"`
	Noop      int `json:"noop"`
	Skipped   int `json:"skipped"`
}

// ─────────────────────────────────────────────────────────────────────────────
// HandlePatchSessionSeats
// ─────────────────────────────────────────────────────────────────────────────

// HandlePatchSessionSeats serves the SEAT-B4 endpoint. See the file-level
// docstring for the full contract.
func (h *Handler) HandlePatchSessionSeats(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	if _, ok := httputil.UUIDPathParam(w, r, "org_id"); !ok {
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

	req, ok := readSeatsPatchRequest(w, r)
	if !ok {
		return
	}
	if !validateSeatsPatchRequest(w, r, req) {
		return
	}

	// Confirm session existence + scope by event. GA sessions do not have
	// materialized session_seats rows so the "no seats matched" branch
	// naturally returns an empty outcome set with a stable 200 rather
	// than surprising the operator with a 404.
	if _, err := h.queries.GetSessionSeatingBinding(ctx, sessionID, eventID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("seating: seats patch lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.seats_patch_failed", "failed to load session for seat mutation", r,
		))
		return
	}

	// Load all materialized seats once and expand every selector against
	// this snapshot. session_seats rowcount is bounded by the venue's
	// physical seat count (typically < 10 000) so a full scan under an
	// operator-priority endpoint is acceptable and simpler than per-selector
	// queries.
	allSeats, err := h.queries.ListSessionSeats(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating: seats patch list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.seats_patch_failed", "failed to load session seats", r,
		))
		return
	}

	seatByKey := make(map[string]gen.SessionSeatRow, len(allSeats))
	sectorIndex := make(map[string][]string, 16)
	rowIndex := make(map[string]map[string][]string, 16)
	for _, s := range allSeats {
		seatByKey[s.SeatKey] = s
		sectorIndex[s.SectorName] = append(sectorIndex[s.SectorName], s.SeatKey)
		if rowIndex[s.SectorName] == nil {
			rowIndex[s.SectorName] = make(map[string][]string, 8)
		}
		rowIndex[s.SectorName][s.RowName] = append(
			rowIndex[s.SectorName][s.RowName], s.SeatKey,
		)
	}

	// Expand selectors → target seat-key set. Order is preserved from the
	// selectors' source order + then seat_key ASC inside each expansion so
	// responses are stable and easy to diff in operator UIs.
	targetKeys, unknown := expandSeatSelectors(req, seatByKey, sectorIndex, rowIndex)

	// Begin transaction. The version bump is issued exactly once so all
	// mutated rows share the same status_version stamp — that keeps the
	// SEAT-B3 seat-status delta endpoint returning the whole batch atomically.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.queries.WithTx(tx)

	newVersion, err := qtx.IncrementSessionSeatStatusVersion(ctx, sessionID)
	if err != nil {
		h.logger.Error("seating: seats patch version bump failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.seats_patch_failed", "failed to bump seat_status_version", r,
		))
		return
	}

	outcomes := make([]seatOutcome, 0, len(targetKeys)+len(unknown))
	summary := seatsSummary{Requested: len(targetKeys) + len(unknown)}

	for _, key := range unknown {
		outcomes = append(outcomes, seatOutcome{
			SeatKey: key,
			Outcome: seatOutcomeSkipped,
			Reason:  seatReasonNotFound,
			Status:  "",
		})
		summary.Skipped++
	}

	changedKeys := make([]string, 0, len(targetKeys))
	for _, key := range targetKeys {
		row := seatByKey[key]
		outcome, err := applySeatAction(ctx, qtx, req.Action, row, newVersion)
		if err != nil {
			h.logger.Error("seating: seats patch apply failed",
				slog.String("seat_key", key),
				slog.String("error", err.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"seating.seats_patch_failed", "failed to apply seat transition", r,
			))
			return
		}
		outcomes = append(outcomes, outcome)
		switch outcome.Outcome {
		case seatOutcomeBlocked, seatOutcomeUnblocked:
			summary.Changed++
			changedKeys = append(changedKeys, key)
		case seatOutcomeNoop:
			summary.Noop++
		case seatOutcomeSkipped:
			summary.Skipped++
		}
	}

	// Every operator request emits one audit event even when no seat actually
	// changed — the attempt itself is the auditable action, and downstream
	// forensic queries need to see "operator X asked to block row 12 at
	// 15:04:05" whether or not any seat was already blocked. The metadata
	// carries the effective + skipped seat lists so a reviewer can
	// reconstruct the outcome without re-issuing the seat-status snapshot.
	// The action name (`v1.session.seats.block` / `.unblock`) mirrors the
	// SEAT-B2 `v1.session.seating.bind` convention so forensic queries can
	// filter on the `v1.session.seats.*` prefix.
	auditAction := "v1.session.seats.block"
	if req.Action == seatsActionUnblock {
		auditAction = "v1.session.seats.unblock"
	}
	if err := h.writeSessionAuditTx(ctx, tx, r, auditAction, sessionID, map[string]any{
		"event_id":            eventID.String(),
		"action":              req.Action,
		"seat_status_version": newVersion,
		"changed_seat_keys":   changedKeys,
		"requested_count":     summary.Requested,
		"changed_count":       summary.Changed,
		"noop_count":          summary.Noop,
		"skipped_count":       summary.Skipped,
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

	httputil.WriteJSON(w, http.StatusOK, seatsPatchResponse{
		SessionID:         sessionID.String(),
		Action:            req.Action,
		SeatStatusVersion: newVersion,
		Outcomes:          outcomes,
		Summary:           summary,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Request decoding + validation
// ─────────────────────────────────────────────────────────────────────────────

// readSeatsPatchRequest slurps and strictly decodes the request body. On
// decode failure a 400 seating.invalid_body envelope is emitted and the
// caller MUST return.
func readSeatsPatchRequest(w http.ResponseWriter, r *http.Request) (seatsPatchRequest, bool) {
	var out seatsPatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, seatsBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seating.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return seatsPatchRequest{}, false
	}
	return out, true
}

// validateSeatsPatchRequest enforces the action enum and the "at least
// one non-empty selector" rule. Row selectors must carry both sector and
// row (an empty pair is an operator mistake worth surfacing eagerly).
func validateSeatsPatchRequest(w http.ResponseWriter, r *http.Request, req seatsPatchRequest) bool {
	if req.Action != seatsActionBlock && req.Action != seatsActionUnblock {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.invalid_action",
			"action must be one of block|unblock",
			r,
			map[string]any{"field": "action"},
		))
		return false
	}
	if len(req.SeatKeys) == 0 && len(req.Sectors) == 0 && len(req.Rows) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seating.no_selectors",
			"at least one of seat_keys, sectors, rows must be non-empty",
			r,
			map[string]any{"fields": []string{"seat_keys", "sectors", "rows"}},
		))
		return false
	}
	for i, row := range req.Rows {
		if row.Sector == "" || row.Row == "" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seating.invalid_row_selector",
				"rows[].sector and rows[].row are required and must be non-empty",
				r,
				map[string]any{"field": "rows", "index": i},
			))
			return false
		}
	}
	return true
}

// expandSeatSelectors expands the caller-supplied selectors against the
// in-memory session_seats snapshot. Returns (targetKeys, unknown):
//   - targetKeys is the deterministically-sorted, deduplicated list of
//     seat_keys that resolve to a real session_seat row.
//   - unknown is the list of caller-supplied seat_keys that did not
//     resolve — reported per-key in the response as skipped with reason
//     "seat_not_found" so the operator can spot typos without a 400.
//
// Sector / row selectors never contribute to unknown — an empty sector or
// (sector,row) pair simply resolves to zero seats, which is treated as a
// silent no-op. This mirrors Bil24 parity (blocking an empty row is not
// an error, it's just uninteresting).
func expandSeatSelectors(
	req seatsPatchRequest,
	seatByKey map[string]gen.SessionSeatRow,
	sectorIndex map[string][]string,
	rowIndex map[string]map[string][]string,
) (targetKeys, unknown []string) {
	seen := make(map[string]struct{}, 64)
	unknown = make([]string, 0)

	for _, key := range req.SeatKeys {
		if _, dup := seen[key]; dup {
			continue
		}
		if _, ok := seatByKey[key]; !ok {
			seen[key] = struct{}{}
			unknown = append(unknown, key)
			continue
		}
		seen[key] = struct{}{}
	}
	for _, sector := range req.Sectors {
		for _, key := range sectorIndex[sector] {
			seen[key] = struct{}{}
		}
	}
	for _, rowSel := range req.Rows {
		if bySector, ok := rowIndex[rowSel.Sector]; ok {
			for _, key := range bySector[rowSel.Row] {
				seen[key] = struct{}{}
			}
		}
	}

	targetKeys = make([]string, 0, len(seen))
	for key := range seen {
		if _, ok := seatByKey[key]; ok {
			targetKeys = append(targetKeys, key)
		}
	}
	sort.Strings(targetKeys)
	sort.Strings(unknown)
	return targetKeys, unknown
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-seat transition
// ─────────────────────────────────────────────────────────────────────────────

// applySeatAction dispatches to the correct DB helper for the given action
// and translates the resulting row status into a stable outcome envelope.
// held/sold seats are skipped with a reason; re-blocking or re-opening a
// seat that is already in the target state is a noop (idempotent).
func applySeatAction(
	ctx context.Context,
	qtx *gen.Queries,
	action string,
	row gen.SessionSeatRow,
	statusVersion int64,
) (seatOutcome, error) {
	switch action {
	case seatsActionBlock:
		return applySeatBlock(ctx, qtx, row, statusVersion)
	case seatsActionUnblock:
		return applySeatUnblock(ctx, qtx, row, statusVersion)
	default:
		// Guarded by validateSeatsPatchRequest — reaching here is a bug.
		return seatOutcome{}, errors.New("unreachable: invalid action")
	}
}

// applySeatBlock performs the `block` transition. Only 'available' rows
// transition; 'blocked' is a noop; 'held' and 'sold' are skipped so
// active reservations / issued tickets are never invalidated mid-flight.
func applySeatBlock(
	ctx context.Context,
	qtx *gen.Queries,
	row gen.SessionSeatRow,
	statusVersion int64,
) (seatOutcome, error) {
	switch row.Status {
	case "available":
		updated, err := qtx.BlockSessionSeat(ctx, row.ID, statusVersion)
		if err != nil {
			// A concurrent transition may have moved the seat out of
			// 'available' between the snapshot read and the UPDATE. Treat
			// that as a skipped outcome rather than a hard failure so the
			// rest of the batch still applies.
			if errors.Is(err, pgx.ErrNoRows) {
				return seatOutcome{
					SeatKey: row.SeatKey,
					Outcome: seatOutcomeSkipped,
					Reason:  "concurrent_transition",
					Status:  row.Status,
				}, nil
			}
			return seatOutcome{}, err
		}
		return seatOutcome{
			SeatKey: updated.SeatKey,
			Outcome: seatOutcomeBlocked,
			Status:  updated.Status,
		}, nil
	case "blocked":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeNoop,
			Status:  row.Status,
		}, nil
	case "held":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  seatReasonHeld,
			Status:  row.Status,
		}, nil
	case "sold":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  seatReasonSold,
			Status:  row.Status,
		}, nil
	default:
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  "unknown_status",
			Status:  row.Status,
		}, nil
	}
}

// applySeatUnblock performs the `unblock` transition. Only 'blocked' rows
// transition; 'available' is a noop; 'held' and 'sold' are skipped (the
// seat is already committed to a reservation / ticket so there is nothing
// for the operator to reopen).
func applySeatUnblock(
	ctx context.Context,
	qtx *gen.Queries,
	row gen.SessionSeatRow,
	statusVersion int64,
) (seatOutcome, error) {
	switch row.Status {
	case "blocked":
		updated, err := qtx.UnblockSessionSeat(ctx, row.ID, statusVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return seatOutcome{
					SeatKey: row.SeatKey,
					Outcome: seatOutcomeSkipped,
					Reason:  "concurrent_transition",
					Status:  row.Status,
				}, nil
			}
			return seatOutcome{}, err
		}
		return seatOutcome{
			SeatKey: updated.SeatKey,
			Outcome: seatOutcomeUnblocked,
			Status:  updated.Status,
		}, nil
	case "available":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeNoop,
			Status:  row.Status,
		}, nil
	case "held":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  seatReasonHeld,
			Status:  row.Status,
		}, nil
	case "sold":
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  seatReasonSold,
			Status:  row.Status,
		}, nil
	default:
		return seatOutcome{
			SeatKey: row.SeatKey,
			Outcome: seatOutcomeSkipped,
			Reason:  "unknown_status",
			Status:  row.Status,
		}, nil
	}
}

// Audit note: the SEAT-B4 audit trail entry is emitted via the shared
// session-scoped writeSessionAuditTx helper (bind.go) with an explicit
// action of `v1.session.seats.block` / `v1.session.seats.unblock`.
