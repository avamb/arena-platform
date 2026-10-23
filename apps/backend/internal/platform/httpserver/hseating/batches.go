// batches.go serves the seat-batch surface of a session (task S1c, spec
// 08_architecture/26_seat_layout_management_plan_ru.md section 3.3):
//
//	POST /v1/organizations/{org_id}/sessions/{session_id}/seat-batches
//	POST /v1/organizations/{org_id}/sessions/{session_id}/seat-batches/{batch_id}/revert
//	GET  /v1/organizations/{org_id}/sessions/{session_id}/seat-batches
//	GET  /v1/organizations/{org_id}/sessions/{session_id}/seats/{seat_key}/history
//
// Every write goes through seatlayout (ApplyBatch / Revert). The shim
// (seating_batch_shims.go) has checked the membership of {org_id} and that
// the session belongs to it; the queries are scoped by (session, org) again.
package hseating

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/seatlayout"
)

const (
	seatBatchBodyLimit      = 1 << 20
	seatBatchDefaultLimit   = 20
	seatBatchMaxLimit       = 50
	seatHistoryDefaultLimit = 50
	seatHistoryMaxLimit     = 100
)

// batchActor turns the request's actor into a batch actor: the id when it
// is a UUID, the actor type as the label.
func batchActor(ctx context.Context) seatlayout.Actor {
	actor, _ := auth.ActorFromContext(ctx)
	out := seatlayout.Actor{Label: string(actor.Type)}
	if id, err := uuid.Parse(actor.ID); err == nil {
		out.ID = &id
	}
	return out
}

type seatBatchChangeRequest struct {
	SeatKey string  `json:"seat_key"`
	TierID  *string `json:"tier_id"`
	Status  *string `json:"status"`
}

type seatBatchRequest struct {
	Kind     string                   `json:"kind"`
	Changes  []seatBatchChangeRequest `json:"changes"`
	NewTiers []json.RawMessage        `json:"new_tiers"`
}

// SeatBatchResponse is the wire shape of one batch (openapi SeatBatch).
type SeatBatchResponse struct {
	ID                string                     `json:"id"`
	SessionID         string                     `json:"session_id"`
	Kind              string                     `json:"kind"`
	ActorID           *string                    `json:"actor_id"`
	ActorLabel        string                     `json:"actor_label"`
	SeatStatusVersion int64                      `json:"seat_status_version"`
	AppliedAt         string                     `json:"applied_at"`
	RevertedAt        *string                    `json:"reverted_at"`
	RevertedByBatchID *string                    `json:"reverted_by_batch_id"`
	RevertsBatchID    *string                    `json:"reverts_batch_id"`
	Changes           []seatlayout.AppliedChange `json:"changes"`
	Skipped           []seatlayout.SkippedSeat   `json:"skipped"`
	UnchangedSeatKeys []string                   `json:"unchanged_seat_keys,omitempty"`
}

func uuidStr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

func timeStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func seatBatchResponse(b seatlayout.Batch, withNoops bool) SeatBatchResponse {
	out := SeatBatchResponse{
		ID: b.ID.String(), SessionID: b.SessionID.String(), Kind: string(b.Kind),
		ActorID: uuidStr(b.ActorID), ActorLabel: b.ActorLabel, SeatStatusVersion: b.SeatStatusVersion,
		AppliedAt: b.AppliedAt.UTC().Format(time.RFC3339), RevertedAt: timeStr(b.RevertedAt),
		RevertedByBatchID: uuidStr(b.RevertedByBatchID), RevertsBatchID: uuidStr(b.RevertsBatchID),
		Changes: b.Changes, Skipped: b.Skipped,
	}
	if out.Changes == nil {
		out.Changes = []seatlayout.AppliedChange{}
	}
	if out.Skipped == nil {
		out.Skipped = []seatlayout.SkippedSeat{}
	}
	if withNoops {
		out.UnchangedSeatKeys = make([]string, 0, len(b.Noops))
		for _, n := range b.Noops {
			out.UnchangedSeatKeys = append(out.UnchangedSeatKeys, n.SeatKey)
		}
	}
	return out
}

func (h *Handler) batchScope(w http.ResponseWriter, r *http.Request) (orgID, sessionID uuid.UUID, ok bool) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return uuid.Nil, uuid.Nil, false
	}
	if orgID, ok = httputil.UUIDPathParam(w, r, "org_id"); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	if sessionID, ok = httputil.UUIDPathParam(w, r, "session_id"); !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, sessionID, true
}

// HandleCreateSeatBatch serves POST .../sessions/{session_id}/seat-batches.
func (h *Handler) HandleCreateSeatBatch(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	var req seatBatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, seatBatchBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// The decoder's own text (a field name, an offset, "http: request
		// body too large") goes to details, never into the message.
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"seat_batch.invalid_body", "request body is not a valid seat batch", r,
			map[string]any{"reason": err.Error(), "max_bytes": seatBatchBodyLimit},
		))
		return
	}
	// new_tiers is only counted here; seatlayout.Validate refuses a
	// non-empty list (ErrNewTiersUnsupported, one place for the rule).
	newTiers := make([]seatlayout.NewTier, len(req.NewTiers))
	changes := make([]seatlayout.Change, 0, len(req.Changes))
	for i, c := range req.Changes {
		ch := seatlayout.Change{SeatKey: c.SeatKey, Status: c.Status}
		if c.TierID != nil {
			id, err := uuid.Parse(*c.TierID)
			if err != nil {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"seat_batch.invalid_change", "changes[].tier_id must be a valid UUID", r,
					map[string]any{"index": i, "seat_key": c.SeatKey},
				))
				return
			}
			ch.TierID = &id
		}
		changes = append(changes, ch)
	}
	kind := seatlayout.Kind(req.Kind)
	batch, err := seatlayout.ApplyBatch(r.Context(), h.pool, h.queries, seatlayout.BatchInput{
		OrgID: orgID, SessionID: sessionID, Actor: batchActor(r.Context()), Kind: kind, Changes: changes,
		NewTiers: newTiers,
		Audit: func(ctx context.Context, tx pgx.Tx, b seatlayout.Batch) error {
			return h.writeSessionAuditTx(ctx, tx, r, "v1.session.seat_batch.apply", sessionID, map[string]any{
				"batch_id":            b.ID.String(),
				"kind":                string(b.Kind),
				"seat_status_version": b.SeatStatusVersion,
				"changed_count":       len(b.Changes),
				"skipped_count":       len(b.Skipped),
				"unchanged_count":     len(b.Noops),
			})
		},
	})
	if err != nil {
		h.writeBatchError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, seatBatchResponse(batch, true))
}

// HandleRevertSeatBatch serves POST .../seat-batches/{batch_id}/revert.
func (h *Handler) HandleRevertSeatBatch(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	batchID, ok := httputil.UUIDPathParam(w, r, "batch_id")
	if !ok {
		return
	}
	batch, err := seatlayout.Revert(r.Context(), h.pool, h.queries, seatlayout.RevertInput{
		OrgID: orgID, SessionID: sessionID, BatchID: batchID, Actor: batchActor(r.Context()),
		Audit: func(ctx context.Context, tx pgx.Tx, b seatlayout.Batch) error {
			return h.writeSessionAuditTx(ctx, tx, r, "v1.session.seat_batch.revert", sessionID, map[string]any{
				"batch_id":            b.ID.String(),
				"reverts_batch_id":    batchID.String(),
				"seat_status_version": b.SeatStatusVersion,
				"changed_count":       len(b.Changes),
				"skipped_count":       len(b.Skipped),
			})
		},
	})
	if err != nil {
		h.writeBatchError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, seatBatchResponse(batch, true))
}

func (h *Handler) writeBatchError(w http.ResponseWriter, r *http.Request, err error) {
	var inErr *seatlayout.InputError
	var tierErr *seatlayout.TierError
	var auditErr *seatlayout.AuditError
	var nothing *seatlayout.NothingToRevertError
	switch {
	case errors.As(err, &nothing):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelopeWithDetails(
			"seat_batch.nothing_to_revert",
			"no seat of this batch can be put back; revert the later batch that changed them first", r,
			map[string]any{"skipped": nothing.Skipped},
		))
	case errors.As(err, &inErr):
		status := http.StatusBadRequest
		if inErr.Code == "seat_batch.too_many_changes" {
			status = http.StatusUnprocessableEntity
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelopeWithDetails(inErr.Code, inErr.Message, r, inErr.Details))
	case errors.Is(err, seatlayout.ErrNewTiersUnsupported):
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"seat_batch.new_tiers_unsupported", "creating a category inside a batch is not supported yet", r,
		))
	case errors.As(err, &tierErr):
		ids := make([]string, 0, len(tierErr.TierIDs))
		for _, id := range tierErr.TierIDs {
			ids = append(ids, id.String())
		}
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"seat_batch.tier_not_in_session", "tier_id does not reference an active category of this session", r,
			map[string]any{"tier_ids": ids},
		))
	case errors.Is(err, seatlayout.ErrSessionNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("session.not_found", "session not found", r))
	case errors.Is(err, seatlayout.ErrBatchNotFound):
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("seat_batch.not_found", "seat batch not found", r))
	case errors.Is(err, seatlayout.ErrAlreadyReverted):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seat_batch.already_reverted", "this batch has already been reverted", r,
		))
	case errors.Is(err, seatlayout.ErrRevertOfRevert):
		httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
			"seat_batch.revert_not_allowed", "a revert cannot be reverted; apply a new batch instead", r,
		))
	case errors.As(err, &auditErr):
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seating.audit_failed", "failed to write audit event", r,
		))
	default:
		h.logger.Error("seating: seat batch failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"seat_batch.failed", "failed to apply the seat batch", r,
		))
	}
}

// pageParams reads limit/offset; a bad value is a 400.
// ParseInt with bitSize 32 keeps every value inside int32 by construction.
func pageParams(w http.ResponseWriter, r *http.Request, def, max int32) (int32, int32, bool) {
	limit, offset := def, int32(0)
	if v := r.URL.Query().Get("limit"); v != "" {
		n64, err := strconv.ParseInt(v, 10, 32)
		n := int32(n64)
		if err != nil || n < 1 || n > max {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seat_batch.invalid_page", "limit must be an integer between 1 and the maximum", r,
				map[string]any{"field": "limit", "max": max},
			))
			return 0, 0, false
		}
		limit = n
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n64, err := strconv.ParseInt(v, 10, 32)
		n := int32(n64)
		if err != nil || n < 0 || n > 1_000_000 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"seat_batch.invalid_page", "offset must be a non-negative integer", r,
				map[string]any{"field": "offset"},
			))
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

// SeatBatchSummaryResponse is one row of the batch list (openapi
// SeatBatchSummary): the batch without its changes / skipped arrays, only
// their sizes. The full batch is GET .../seat-batches/{batch_id}.
type SeatBatchSummaryResponse struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	Kind              string  `json:"kind"`
	ActorID           *string `json:"actor_id"`
	ActorLabel        string  `json:"actor_label"`
	SeatStatusVersion int64   `json:"seat_status_version"`
	AppliedAt         string  `json:"applied_at"`
	RevertedAt        *string `json:"reverted_at"`
	RevertedByBatchID *string `json:"reverted_by_batch_id"`
	RevertsBatchID    *string `json:"reverts_batch_id"`
	ChangedCount      int32   `json:"changed_count"`
	SkippedCount      int32   `json:"skipped_count"`
}

type seatBatchListResponse struct {
	Items   []SeatBatchSummaryResponse `json:"items"`
	Total   int64                      `json:"total"`
	HasMore bool                       `json:"has_more"`
}

// HandleListSeatBatches serves GET .../sessions/{session_id}/seat-batches.
// The list never carries the per-seat arrays (a batch may hold 10 000
// seats); the card of one batch does.
func (h *Handler) HandleListSeatBatches(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	limit, offset, ok := pageParams(w, r, seatBatchDefaultLimit, seatBatchMaxLimit)
	if !ok {
		return
	}
	ctx := r.Context()
	total, err := h.queries.CountSessionSeatBatches(ctx, sessionID, orgID)
	if err == nil {
		rows, lerr := h.queries.ListSessionSeatBatchSummaries(ctx, sessionID, orgID, limit, offset)
		err = lerr
		if err == nil {
			items := make([]SeatBatchSummaryResponse, 0, len(rows))
			for _, b := range rows {
				items = append(items, SeatBatchSummaryResponse{
					ID: b.ID.String(), SessionID: b.SessionID.String(), Kind: b.Kind,
					ActorID: uuidStr(b.ActorID), ActorLabel: b.ActorLabel, SeatStatusVersion: b.SeatStatusVersion,
					AppliedAt: b.AppliedAt.UTC().Format(time.RFC3339), RevertedAt: timeStr(b.RevertedAt),
					RevertedByBatchID: uuidStr(b.RevertedByBatchID), RevertsBatchID: uuidStr(b.RevertsBatchID),
					ChangedCount: b.ChangedCount, SkippedCount: b.SkippedCount,
				})
			}
			httputil.WriteJSON(w, http.StatusOK, seatBatchListResponse{
				Items: items, Total: total, HasMore: int64(offset)+int64(len(items)) < total,
			})
			return
		}
	}
	h.logger.Error("seating: list seat batches failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"seat_batch.list_failed", "failed to load seat batches", r,
	))
}

// HandleGetSeatBatch serves GET .../sessions/{session_id}/seat-batches/{batch_id}:
// one batch with every changed and skipped seat.
func (h *Handler) HandleGetSeatBatch(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	batchID, ok := httputil.UUIDPathParam(w, r, "batch_id")
	if !ok {
		return
	}
	row, err := h.queries.GetSessionSeatBatch(r.Context(), batchID, sessionID, orgID)
	if err == nil {
		b, derr := seatlayout.BatchFromRow(row)
		if derr == nil {
			httputil.WriteJSON(w, http.StatusOK, seatBatchResponse(b, false))
			return
		}
		err = derr
	} else if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("seat_batch.not_found", "seat batch not found", r))
		return
	}
	h.logger.Error("seating: get seat batch failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"seat_batch.list_failed", "failed to load the seat batch", r,
	))
}

// SeatHistoryEntry is one batch as seen from one seat (openapi SeatHistoryEntry).
type SeatHistoryEntry struct {
	BatchID           string                `json:"batch_id"`
	Kind              string                `json:"kind"`
	ActorLabel        string                `json:"actor_label"`
	AppliedAt         string                `json:"applied_at"`
	RevertedAt        *string               `json:"reverted_at"`
	RevertedByBatchID *string               `json:"reverted_by_batch_id"`
	Outcome           string                `json:"outcome"`
	Reason            string                `json:"reason,omitempty"`
	From              *seatlayout.SeatState `json:"from,omitempty"`
	To                *seatlayout.SeatState `json:"to,omitempty"`
}

type seatHistoryResponse struct {
	SeatKey string             `json:"seat_key"`
	Items   []SeatHistoryEntry `json:"items"`
	Total   int64              `json:"total"`
	HasMore bool               `json:"has_more"`
}

// historyEntry projects a batch onto one seat: changed (from/to) or skipped
// (reason).
func historyEntry(b seatlayout.Batch, seatKey string) SeatHistoryEntry {
	e := SeatHistoryEntry{
		BatchID: b.ID.String(), Kind: string(b.Kind), ActorLabel: b.ActorLabel,
		AppliedAt: b.AppliedAt.UTC().Format(time.RFC3339), RevertedAt: timeStr(b.RevertedAt),
		RevertedByBatchID: uuidStr(b.RevertedByBatchID),
	}
	for _, c := range b.Changes {
		if c.SeatKey == seatKey {
			from, to := c.From, c.To
			e.Outcome, e.From, e.To = "changed", &from, &to
			return e
		}
	}
	for _, s := range b.Skipped {
		if s.SeatKey == seatKey {
			e.Outcome, e.Reason = "skipped", s.Reason
			return e
		}
	}
	e.Outcome = "skipped"
	return e
}

// HandleSeatHistory serves GET .../sessions/{session_id}/seats/{seat_key}/history.
// seat_key is path-escaped by the client ("Parter|A|1" -> "Parter%7CA%7C1").
func (h *Handler) HandleSeatHistory(w http.ResponseWriter, r *http.Request) {
	orgID, sessionID, ok := h.batchScope(w, r)
	if !ok {
		return
	}
	seatKey, err := url.PathUnescape(chi.URLParam(r, "seat_key"))
	if err != nil || seatKey == "" || len(seatKey) > 200 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"seat_batch.invalid_seat_key", "seat_key must be a non-empty, path-escaped seat key", r,
		))
		return
	}
	limit, offset, ok := pageParams(w, r, seatHistoryDefaultLimit, seatHistoryMaxLimit)
	if !ok {
		return
	}
	ctx := r.Context()
	probe := seatlayout.SeatProbe(seatKey)
	total, err := h.queries.CountSeatBatchHistory(ctx, sessionID, orgID, probe)
	if err == nil {
		rows, lerr := h.queries.ListSeatBatchHistory(ctx, sessionID, orgID, probe, limit, offset)
		err = lerr
		if err == nil {
			items := make([]SeatHistoryEntry, 0, len(rows))
			for _, row := range rows {
				b, derr := seatlayout.BatchFromRow(row)
				if derr != nil {
					err = derr
					break
				}
				items = append(items, historyEntry(b, seatKey))
			}
			if err == nil {
				httputil.WriteJSON(w, http.StatusOK, seatHistoryResponse{
					SeatKey: seatKey, Items: items, Total: total,
					HasMore: int64(offset)+int64(len(items)) < total,
				})
				return
			}
		}
	}
	h.logger.Error("seating: seat history failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"seat_batch.list_failed", "failed to load the seat history", r,
	))
}
