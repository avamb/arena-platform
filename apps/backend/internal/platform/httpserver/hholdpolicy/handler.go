// Package hholdpolicy serves the superadmin screen "Seat hold" (spec
// 08_architecture/67, section 7.2): the two hold profiles (standard and
// high_demand) and the profile of one event.
//
//	GET /v1/admin/hold-policy
//	PUT /v1/admin/hold-policy/{profile}
//	PUT /v1/admin/events/{event_id}/hold-profile
//
// The routes need a platform superadmin (the mount checks it), every write
// needs X-Admin-Reason and writes its audit_events row, with the old and the
// new values, on the change's transaction. A handler without a durable audit
// writer answers 503 and changes nothing. Times travel in seconds, the poll
// interval in milliseconds, as the table stores them.
package hholdpolicy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

const maxBody = 1 << 16

// Audit actions of the screen.
const (
	AuditProfileUpdate = "v1.admin.hold_policy.update"
	AuditEventProfile  = "v1.admin.event.hold_profile.set"
)

// Handler serves the hold policy admin routes.
type Handler struct {
	pool   *pgxpool.Pool
	audit  audit.Writer
	logger *slog.Logger
	now    func() time.Time
}

// New builds the handler. A nil pool makes every route answer 503.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, audit: auditWriter, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// profileWire is one profile on the wire.
type profileWire struct {
	Profile            string     `json:"profile"`
	SelectSeconds      int32      `json:"select_seconds"`
	PaymentSeconds     int32      `json:"payment_seconds"`
	ExtendSeconds      int32      `json:"extend_seconds"`
	ExtendCount        int32      `json:"extend_count"`
	PayingSeconds      int32      `json:"paying_seconds"`
	MaxSeatsPerHold    int32      `json:"max_seats_per_hold"`
	MaxActivePerDevice int32      `json:"max_active_per_device"`
	MaxActivePerIP     int32      `json:"max_active_per_ip"`
	ReholdPauseSeconds int32      `json:"rehold_pause_seconds"`
	PollIntervalMs     int32      `json:"poll_interval_ms"`
	UpdatedAt          time.Time  `json:"updated_at"`
	UpdatedBy          *uuid.UUID `json:"updated_by"`
}

func toWire(p gen.HoldPolicyProfile) profileWire {
	return profileWire{
		Profile: p.Name, SelectSeconds: p.SelectSeconds, PaymentSeconds: p.PaymentSeconds,
		ExtendSeconds: p.ExtendSeconds, ExtendCount: p.ExtendCount, PayingSeconds: p.PayingSeconds,
		MaxSeatsPerHold: p.MaxSeatsPerHold, MaxActivePerDevice: p.MaxActivePerDevice,
		MaxActivePerIP: p.MaxActivePerIP, ReholdPauseSeconds: p.ReholdPauseSeconds,
		PollIntervalMs: p.PollIntervalMs, UpdatedAt: p.UpdatedAt.UTC(), UpdatedBy: p.UpdatedBy,
	}
}

// values is the audit form of a profile: the numbers only.
func values(p gen.HoldPolicyProfile) map[string]any {
	return map[string]any{
		"select_seconds": p.SelectSeconds, "payment_seconds": p.PaymentSeconds,
		"extend_seconds": p.ExtendSeconds, "extend_count": p.ExtendCount,
		"paying_seconds": p.PayingSeconds, "max_seats_per_hold": p.MaxSeatsPerHold,
		"max_active_per_device": p.MaxActivePerDevice, "max_active_per_ip": p.MaxActivePerIP,
		"rehold_pause_seconds": p.ReholdPauseSeconds, "poll_interval_ms": p.PollIntervalMs,
	}
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request, write bool) bool {
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "the database is not available right now, please try again in a moment", r))
		return false
	}
	if write && !audit.IsDurableInTx(h.audit) {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.audit_unavailable", "nothing was changed: the audit log is not available, please try again in a moment", r))
		return false
	}
	return true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "admin hold policy: "+what, "error", err, "request_id", logging.RequestID(r.Context()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"hold_policy.admin_failed", "nothing was changed: the operation failed, please try again", r))
}

func unprocessable(w http.ResponseWriter, r *http.Request, code, msg string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(code, msg, r))
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"hold_policy.invalid_body", "the request body is not valid JSON of the expected shape: "+err.Error(), r))
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"hold_policy.invalid_body", "the request body has data after the JSON object", r))
		return false
	}
	return true
}

func (h *Handler) auditTx(r *http.Request, tx pgx.Tx, action, resourceType, resourceID, reason string, meta map[string]any) error {
	a, _ := auth.ActorFromContext(r.Context())
	meta["reason"] = reason
	return h.audit.WriteTx(r.Context(), tx, audit.Event{
		OccurredAt:   h.now(),
		ActorType:    "user",
		ActorID:      a.ID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(r.Context()),
		TraceID:      logging.TraceID(r.Context()),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

func validProfileName(name string) bool {
	return name == holdpolicy.ProfileStandard || name == holdpolicy.ProfileHighDemand
}

// HandleList serves GET /v1/admin/hold-policy: both profiles.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, false) {
		return
	}
	rows, err := gen.New(h.pool).ListHoldPolicyProfiles(r.Context())
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	out := make([]profileWire, 0, len(rows))
	for _, row := range rows {
		out = append(out, toWire(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

// putBody is the body of PUT /v1/admin/hold-policy/{profile}; every field is
// required.
type putBody struct {
	SelectSeconds      *int32 `json:"select_seconds"`
	PaymentSeconds     *int32 `json:"payment_seconds"`
	ExtendSeconds      *int32 `json:"extend_seconds"`
	ExtendCount        *int32 `json:"extend_count"`
	PayingSeconds      *int32 `json:"paying_seconds"`
	MaxSeatsPerHold    *int32 `json:"max_seats_per_hold"`
	MaxActivePerDevice *int32 `json:"max_active_per_device"`
	MaxActivePerIP     *int32 `json:"max_active_per_ip"`
	ReholdPauseSeconds *int32 `json:"rehold_pause_seconds"`
	PollIntervalMs     *int32 `json:"poll_interval_ms"`
}

func (b putBody) complete() bool {
	return b.SelectSeconds != nil && b.PaymentSeconds != nil && b.ExtendSeconds != nil && b.ExtendCount != nil &&
		b.PayingSeconds != nil && b.MaxSeatsPerHold != nil && b.MaxActivePerDevice != nil &&
		b.MaxActivePerIP != nil && b.ReholdPauseSeconds != nil && b.PollIntervalMs != nil
}

// HandlePut serves PUT /v1/admin/hold-policy/{profile}.
func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "profile")
	if !validProfileName(name) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"hold_policy.profile_not_found", "the profile is standard or high_demand", r))
		return
	}
	var body putBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !body.complete() {
		unprocessable(w, r, "hold_policy.invalid", "every field of the profile is required")
		return
	}
	next := gen.HoldPolicyProfile{
		Name: name, SelectSeconds: *body.SelectSeconds, PaymentSeconds: *body.PaymentSeconds,
		ExtendSeconds: *body.ExtendSeconds, ExtendCount: *body.ExtendCount, PayingSeconds: *body.PayingSeconds,
		MaxSeatsPerHold: *body.MaxSeatsPerHold, MaxActivePerDevice: *body.MaxActivePerDevice,
		MaxActivePerIP: *body.MaxActivePerIP, ReholdPauseSeconds: *body.ReholdPauseSeconds,
		PollIntervalMs: *body.PollIntervalMs,
	}
	if err := holdpolicy.FromRow(next).Validate(); err != nil {
		unprocessable(w, r, "hold_policy.invalid", err.Error())
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	before, err := q.GetHoldPolicyProfile(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"hold_policy.profile_not_found", "the profile does not exist", r))
		return
	}
	if err != nil {
		h.fail(w, r, "read profile", err)
		return
	}
	var actor *uuid.UUID
	if a, _ := auth.ActorFromContext(ctx); a.ID != "" {
		if id, perr := uuid.Parse(a.ID); perr == nil {
			actor = &id
		}
	}
	after, err := q.UpdateHoldPolicyProfile(ctx, gen.UpdateHoldPolicyProfileParams{
		Name: name, SelectSeconds: next.SelectSeconds, PaymentSeconds: next.PaymentSeconds,
		ExtendSeconds: next.ExtendSeconds, ExtendCount: next.ExtendCount, PayingSeconds: next.PayingSeconds,
		MaxSeatsPerHold: next.MaxSeatsPerHold, MaxActivePerDevice: next.MaxActivePerDevice,
		MaxActivePerIP: next.MaxActivePerIP, ReholdPauseSeconds: next.ReholdPauseSeconds,
		PollIntervalMs: next.PollIntervalMs, UpdatedBy: actor,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" {
			unprocessable(w, r, "hold_policy.invalid", "a value is outside the allowed range")
			return
		}
		h.fail(w, r, "update profile", err)
		return
	}
	if err := h.auditTx(r, tx, AuditProfileUpdate, "hold_policy_profile", name, reason,
		map[string]any{"profile": name, "before": values(before), "after": values(after)}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, toWire(after))
}

// eventProfileBody is the body of PUT /v1/admin/events/{event_id}/hold-profile:
// the key "profile" is required, null returns the event to standard.
type eventProfileBody struct {
	Profile json.RawMessage `json:"profile"`
}

// HandlePutEventProfile serves PUT /v1/admin/events/{event_id}/hold-profile.
func (h *Handler) HandlePutEventProfile(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w, r, true) {
		return
	}
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	eventID, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		unprocessable(w, r, "hold_policy.invalid_event_id", "event_id must be a UUID")
		return
	}
	var body eventProfileBody
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Profile) == 0 {
		unprocessable(w, r, "hold_policy.invalid", `the field "profile" is required: standard, high_demand or null`)
		return
	}
	var profile *string
	if err := json.Unmarshal(body.Profile, &profile); err != nil || (profile != nil && !validProfileName(*profile)) {
		unprocessable(w, r, "hold_policy.invalid", `profile must be "standard", "high_demand" or null`)
		return
	}

	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.fail(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := gen.New(tx)
	before, err := q.GetEventHoldProfile(ctx, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"event.not_found", "the event does not exist", r))
		return
	}
	if err != nil {
		h.fail(w, r, "read event", err)
		return
	}
	if _, err := q.SetEventHoldProfile(ctx, eventID, profile); err != nil {
		h.fail(w, r, "set event profile", err)
		return
	}
	if err := h.auditTx(r, tx, AuditEventProfile, "event", eventID.String(), reason,
		map[string]any{"before": before, "after": profile}); err != nil {
		h.fail(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.fail(w, r, "commit", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"event_id": eventID, "profile": profile})
}
