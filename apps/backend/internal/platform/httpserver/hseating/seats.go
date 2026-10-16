// seats.go implements the operator seat block/unblock endpoint
// (feature #308, Wave SEAT-B4).
//
//	PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seats
//
// The request accepts three mutually-cooperative selectors — `seat_keys`,
// `sectors`, and `rows` — that are expanded server-side to the concrete
// set of session_seats rows to transition. Only the two admin transitions
// available↔unavailable are attempted; seats in held/sold status are
// skipped per-seat with a reason and are never silently mutated. Blocking
// an already-unavailable seat is a documented no-op (idempotent).
//
// Contract source: 09_autoforge/seating_backlog.md §7 SEAT-B4.
// Status naming: the admin-withheld state is `unavailable` (renamed from
// `blocked` in migration 0081, AB-49); the wire actions stay `block` /
// `unblock` — they are operation verbs, not the status value.
//
//   - Requires the `event_session.assign_seating_plan` permission (same
//     operational role used for SEAT-B2 binding).
//   - Every request emits one audit event with the seat-key list + actor.
//   - Withheld seats surface as `unavailable` in the seat-status endpoint
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/txretry"
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

// per-seat outcome codes emitted in the response. The changed outcomes name
// the resulting seat status ('unavailable' after block, 'available' after
// unblock) — renamed from 'blocked'/'unblocked' together with the status
// value (migration 0081).
const (
	seatOutcomeBlocked   = "unavailable"
	seatOutcomeUnblocked = "available"
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

	// The write goes through seatlayout.ApplyBatch (task S1c): one
	// transaction that bumps sessions.seat_status_version exactly once (the
	// sessions row lock, first statement), locks the seats FOR UPDATE in
	// seat_key order, stamps every changed row with the new version — so the
	// SEAT-B3 seat-status delta endpoint returns the whole batch atomically —
	// and records a session_seat_batches row (kind close/open). held/sold
	// seats are skipped with a reason; the batch never fails because of them.
	kind, status := seatlayout.KindClose, seatlayout.StatusUnavailable
	auditAction := "v1.session.seats.block"
	if req.Action == seatsActionUnblock {
		kind, status = seatlayout.KindOpen, seatlayout.StatusAvailable
		auditAction = "v1.session.seats.unblock"
	}
	orgID, _ := httputil.UUIDPathParam(w, r, "org_id")

	var batch seatlayout.Batch
	if len(targetKeys) > 0 {
		changes := make([]seatlayout.Change, 0, len(targetKeys))
		for _, key := range targetKeys {
			st := status
			changes = append(changes, seatlayout.Change{SeatKey: key, Status: &st})
		}
		var err error
		batch, err = seatlayout.ApplyBatch(ctx, h.pool, h.queries, seatlayout.BatchInput{
			OrgID:     orgID,
			SessionID: sessionID,
			Actor:     batchActor(ctx),
			Kind:      kind,
			Changes:   changes,
			Audit: func(ctx context.Context, tx pgx.Tx, b seatlayout.Batch) error {
				outcomes, summary := seatOutcomes(req.Action, unknown, targetKeys, b)
				return h.writeSeatsPatchAudit(ctx, tx, r, auditAction, sessionID, eventID, req.Action,
					&b.ID, b.SeatStatusVersion, outcomes, summary)
			},
		})
		if err != nil {
			h.writeSeatsBatchError(w, r, err)
			return
		}
	} else {
		// Only unknown keys (or empty sectors / rows): nothing to lock in
		// session_seats. The attempt is still audited and still bumps the
		// version, as this endpoint always did.
		v, err := h.auditOnlySeatsPatch(ctx, r, auditAction, sessionID, eventID, req.Action, unknown)
		if err != nil {
			h.writeSeatsBatchError(w, r, err)
			return
		}
		batch.SeatStatusVersion = v
	}

	outcomes, summary := seatOutcomes(req.Action, unknown, targetKeys, batch)
	httputil.WriteJSON(w, http.StatusOK, seatsPatchResponse{
		SessionID:         sessionID.String(),
		Action:            req.Action,
		SeatStatusVersion: batch.SeatStatusVersion,
		Outcomes:          outcomes,
		Summary:           summary,
	})
}

// writeSeatsPatchAudit emits the one audit event of a block/unblock request.
// Every operator request is audited even when no seat actually changed —
// the attempt itself is the auditable action. The metadata carries the
// effective + skipped counts so a reviewer can reconstruct the outcome
// without re-issuing the seat-status snapshot, and the batch_id that
// GET .../seat-batches lists. The action name (`v1.session.seats.block` /
// `.unblock`) mirrors the SEAT-B2 `v1.session.seating.bind` convention.
func (h *Handler) writeSeatsPatchAudit(
	ctx context.Context, tx pgx.Tx, r *http.Request, auditAction string,
	sessionID, eventID uuid.UUID, action string, batchID *uuid.UUID, version int64,
	outcomes []seatOutcome, summary seatsSummary,
) error {
	changedKeys := make([]string, 0, summary.Changed)
	for _, o := range outcomes {
		if o.Outcome == seatOutcomeBlocked || o.Outcome == seatOutcomeUnblocked {
			changedKeys = append(changedKeys, o.SeatKey)
		}
	}
	meta := map[string]any{
		"event_id":            eventID.String(),
		"action":              action,
		"seat_status_version": version,
		"changed_seat_keys":   changedKeys,
		"requested_count":     summary.Requested,
		"changed_count":       summary.Changed,
		"noop_count":          summary.Noop,
		"skipped_count":       summary.Skipped,
	}
	if batchID != nil {
		meta["batch_id"] = batchID.String()
	}
	return h.writeSessionAuditTx(ctx, tx, r, auditAction, sessionID, meta)
}

// auditOnlySeatsPatch handles a block/unblock request whose selectors
// resolved to no seat at all: bump the version and write the audit event,
// no batch row (there is nothing to revert).
func (h *Handler) auditOnlySeatsPatch(
	ctx context.Context, r *http.Request, auditAction string,
	sessionID, eventID uuid.UUID, action string, unknown []string,
) (int64, error) {
	var version int64
	err := txretry.Do(ctx, txretry.DefaultAttempts, func() error {
		tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		v, err := h.queries.WithTx(tx).IncrementSessionSeatStatusVersion(ctx, sessionID)
		if err != nil {
			return err
		}
		outcomes, summary := seatOutcomes(action, unknown, nil, seatlayout.Batch{SeatStatusVersion: v})
		if err := h.writeSeatsPatchAudit(ctx, tx, r, auditAction, sessionID, eventID, action,
			nil, v, outcomes, summary); err != nil {
			return &seatlayout.AuditError{Err: err}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		version = v
		return nil
	})
	return version, err
}

// writeSeatsBatchError maps a seatlayout error of the block/unblock PATCH
// to this route's codes.
func (h *Handler) writeSeatsBatchError(w http.ResponseWriter, r *http.Request, err error) {
	var auditErr *seatlayout.AuditError
	switch {
	case errors.Is(err, seatlayout.ErrSessionNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"session.not_found", "session not found", r,
		))
	case errors.As(err, &auditErr):
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.audit_failed", "failed to write audit event", r,
		))
	default:
		h.logger.Error("seating: seats patch batch failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.seats_patch_failed", "failed to apply seat transition", r,
		))
	}
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
// Per-seat outcome
// ─────────────────────────────────────────────────────────────────────────────

// seatOutcomes builds the response outcomes of a block/unblock request:
// unknown keys first (skipped, seat_not_found), then every target key in
// seat_key order with the batch's decision for it.
func seatOutcomes(action string, unknown, targetKeys []string, b seatlayout.Batch) ([]seatOutcome, seatsSummary) {
	results := make(map[string]seatlayout.Result, len(targetKeys))
	for _, c := range b.Changes {
		results[c.SeatKey] = seatlayout.Result{SeatKey: c.SeatKey, Outcome: seatlayout.OutcomeApplied, From: c.From, To: c.To}
	}
	for _, n := range b.Noops {
		st := seatlayout.SeatState{Status: n.Status}
		results[n.SeatKey] = seatlayout.Result{SeatKey: n.SeatKey, Outcome: seatlayout.OutcomeNoop, From: st, To: st}
	}
	for _, sk := range b.Skipped {
		st := seatlayout.SeatState{Status: sk.Status}
		results[sk.SeatKey] = seatlayout.Result{SeatKey: sk.SeatKey, Outcome: seatlayout.OutcomeSkipped, Reason: sk.Reason, From: st, To: st}
	}

	outcomes := make([]seatOutcome, 0, len(unknown)+len(targetKeys))
	summary := seatsSummary{Requested: len(unknown) + len(targetKeys)}
	for _, key := range unknown {
		outcomes = append(outcomes, seatOutcome{SeatKey: key, Outcome: seatOutcomeSkipped, Reason: seatReasonNotFound})
		summary.Skipped++
	}
	for _, key := range targetKeys {
		res, ok := results[key]
		if !ok {
			res = seatlayout.Result{SeatKey: key, Outcome: seatlayout.OutcomeSkipped, Reason: seatlayout.ReasonNotFound}
		}
		o := seatOutcomeFromResult(action, res)
		outcomes = append(outcomes, o)
		switch o.Outcome {
		case seatOutcomeBlocked, seatOutcomeUnblocked:
			summary.Changed++
		case seatOutcomeNoop:
			summary.Noop++
		default:
			summary.Skipped++
		}
	}
	return outcomes, summary
}

// seatOutcomeFromResult translates one seatlayout decision into this
// route's outcome envelope: a change names the resulting status
// ('unavailable' after block, 'available' after unblock), a seat already in
// the target state is a noop, held/sold seats are skipped with the reason.
func seatOutcomeFromResult(action string, res seatlayout.Result) seatOutcome {
	switch res.Outcome {
	case seatlayout.OutcomeApplied:
		outcome := seatOutcomeBlocked
		if action == seatsActionUnblock {
			outcome = seatOutcomeUnblocked
		}
		return seatOutcome{SeatKey: res.SeatKey, Outcome: outcome, Status: res.To.Status}
	case seatlayout.OutcomeNoop:
		return seatOutcome{SeatKey: res.SeatKey, Outcome: seatOutcomeNoop, Status: res.From.Status}
	default:
		return seatOutcome{SeatKey: res.SeatKey, Outcome: seatOutcomeSkipped, Reason: res.Reason, Status: res.From.Status}
	}
}

// seatOutcomeFor is the block/unblock decision for one seat row — the same
// seatlayout.Decide the batch runs under its lock, mapped to this route's
// outcome. An unknown action is an error (guarded at the boundary by
// validateSeatsPatchRequest).
func seatOutcomeFor(action string, row gen.SessionSeatRow) (seatOutcome, error) {
	var status string
	switch action {
	case seatsActionBlock:
		status = seatlayout.StatusUnavailable
	case seatsActionUnblock:
		status = seatlayout.StatusAvailable
	default:
		return seatOutcome{}, errors.New("unreachable: invalid action")
	}
	res := seatlayout.Decide(&row, seatlayout.Target{SeatKey: row.SeatKey, Status: &status}, nil)
	return seatOutcomeFromResult(action, res), nil
}
