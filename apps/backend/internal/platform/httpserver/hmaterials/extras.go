// Package hmaterials serves the materials of an event's public page.
//
// This file and its extras_*.go siblings are task 1-2b of the event-center
// execution spec: the "where else to buy" partner links, the map point and
// the photo report. Routes (all under /v1/organizations/{org_id}/events/{event_id}):
//
//	GET|POST        /partner-links
//	PATCH|DELETE    /partner-links/{link_id}
//	PUT             /partner-links/order
//	GET|PUT|DELETE  /map-point
//	GET|POST        /photo-report
//	PATCH|DELETE    /photo-report/{photo_id}
//	PUT             /photo-report/order
//
// Reads need event.read, writes event.update (the mount applies the gate and
// the membership check of {org_id}). Every handler also ties the event to the
// path organization: a foreign, unknown or deleted event answers the route's
// own 404 event.not_found.
//
// Limits live in the schema (migration 0153: position CHECK + UNIQUE
// (event_id, position)) and in platform/eventmaterials. An insert takes the
// lowest free position in one statement; a lost race is retried, then
// answered as 409.
package hmaterials

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxBodyBytes bounds every JSON body of these routes.
const maxBodyBytes = 64 << 10

// insertAttempts is how many times an insert is tried when another request
// takes the chosen position first.
const insertAttempts = 3

// Constraint names of migration 0153 that the handlers tell apart.
const (
	partnerPositionConstraint = "event_partner_links_position_uq"
	reportPositionConstraint  = "event_report_photos_position_uq"
	reportMediaConstraint     = "event_report_photos_media_uq"
)

// ExtrasHandler serves the partner links, the map point and the photo report.
type ExtrasHandler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger
}

// NewExtras builds the handler. pool may be nil: every route then answers 503.
func NewExtras(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger) *ExtrasHandler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &ExtrasHandler{pool: pool, audit: auditWriter, logger: logger}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

func (h *ExtrasHandler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.pool == nil || h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

// scope reads {org_id} and {event_id} and checks that the event is an active
// event of that organization.
func (h *ExtrasHandler) scope(w http.ResponseWriter, r *http.Request) (orgID, eventID uuid.UUID, ok bool) {
	if !h.ready(w, r) {
		return uuid.Nil, uuid.Nil, false
	}
	orgID, ok = httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	eventID, ok = httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	owner, err := h.queries.GetEventOrgForMaterials(r.Context(), eventID)
	if err != nil || owner != orgID {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("materials: event lookup failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"event.materials_failed", "could not read the event, please try again", r,
			))
			return uuid.Nil, uuid.Nil, false
		}
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
		return uuid.Nil, uuid.Nil, false
	}
	return orgID, eventID, true
}

// pathID reads one more UUID path parameter; a malformed one is the route's
// own 404 (the row cannot exist).
func pathID(w http.ResponseWriter, r *http.Request, name, code, what string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(code, what+" not found", r))
		return uuid.Nil, false
	}
	return id, true
}

// readBody decodes the JSON body strictly: one object, no unknown members.
func readBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || strings.TrimSpace(string(raw)) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"event.invalid_body", "the request body is missing or too large; send a JSON object as the documentation shows", r,
		))
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"event.invalid_body", "the request body is not valid JSON for this route; please check the field names and types", r,
		))
		return false
	}
	return true
}

// inTx runs fn in one transaction and commits when it returns nil.
func (h *ExtrasHandler) inTx(ctx context.Context, fn func(q *gen.Queries, tx pgx.Tx) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(h.queries.WithTx(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// writeAudit records a mutation inside its transaction.
func (h *ExtrasHandler) writeAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action string, eventID uuid.UUID, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "event",
		ResourceID:   eventID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

func (h *ExtrasHandler) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("materials: "+what+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"event.materials_failed", "could not save the change, please try again", r,
	))
}

// uniqueViolation returns the constraint of a 23505 error.
func uniqueViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// idsMatch reports whether ids is exactly a permutation of current.
func idsMatch(current []uuid.UUID, ids []uuid.UUID) bool {
	if len(current) != len(ids) {
		return false
	}
	want := make(map[uuid.UUID]bool, len(current))
	for _, id := range current {
		want[id] = true
	}
	for _, id := range ids {
		if !want[id] {
			return false
		}
		delete(want, id)
	}
	return len(want) == 0
}

// orderRequest is the body of the two PUT .../order routes.
type orderRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

func invalidOrder(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
		"event.invalid_order",
		"ids must list every item of this event exactly once; please reload the list and try again",
		r, map[string]any{"field": "ids"},
	))
}
