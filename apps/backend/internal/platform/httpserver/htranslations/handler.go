// Package htranslations serves the organizer API of an event's content
// translations (spec 08_architecture/63 section 16.4). Routes, all under
// /v1/organizations/{org_id}/events/{event_id}:
//
//	GET     /translations                 event.read
//	PUT     /translations/{locale}        event.update  (a hand-made translation)
//	DELETE  /translations/{locale}        event.update
//	POST    /translations/machine         event.update  (queues content.translate)
//	PUT     /report-text                  event.update  (the original photo-report text)
//
// The mount applies the permission gate and the membership check of {org_id};
// every handler ties the event to the path organization (else 404
// event.not_found). The texts of a translation are sanitized by
// contenttranslate; a manual translation is stamped with the hash of the
// CURRENT original, so it is fresh until the original changes.
package htranslations

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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/contenttranslate"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxBodyBytes bounds every JSON body of these routes (a description is up to
// 20 000 characters, a few bytes each).
const maxBodyBytes = 256 << 10

// Handler serves the translation routes.
type Handler struct {
	pool    *pgxpool.Pool
	audit   audit.Writer
	logger  *slog.Logger
	machine bool
}

// New builds the handler. pool may be nil: every route then answers 503.
// machine says whether TRANSLATE_LLM_API_KEY is set on this process.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, machine bool, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{pool: pool, audit: auditWriter, machine: machine, logger: logger}
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return false
	}
	return true
}

func notFound(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("event.not_found", "event not found", r))
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("translations: "+what+" failed", slog.String("error", err.Error()))
	httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
		"translation.failed", "could not complete the request, please try again", r))
}

// scope reads {org_id} and {event_id} and loads the original of the event; a
// foreign, unknown or deleted event answers 404 event.not_found.
func (h *Handler) scope(w http.ResponseWriter, r *http.Request) (uuid.UUID, contenttranslate.EventOriginal, bool) {
	if !h.ready(w, r) {
		return uuid.Nil, contenttranslate.EventOriginal{}, false
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return uuid.Nil, contenttranslate.EventOriginal{}, false
	}
	eventID, ok := httputil.UUIDPathParam(w, r, "event_id")
	if !ok {
		return uuid.Nil, contenttranslate.EventOriginal{}, false
	}
	orig, err := contenttranslate.LoadEventOriginal(r.Context(), h.pool, orgID, eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w, r)
		return uuid.Nil, orig, false
	}
	if err != nil {
		h.failed(w, r, "load event", err)
		return uuid.Nil, orig, false
	}
	return orgID, orig, true
}

// readBody decodes the JSON body strictly: one object, no unknown members.
func readBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || strings.TrimSpace(string(raw)) == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"translation.invalid_body", "the request body is missing or too large; send a JSON object as the documentation shows", r))
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"translation.invalid_body", "the request body is not valid JSON for this route; please check the field names and types", r))
		return false
	}
	return true
}

// inTx runs fn in one transaction and commits when it returns nil.
func (h *Handler) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (h *Handler) writeAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action string, eventID uuid.UUID, meta map[string]any) error {
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

// callerUserID is the id of the calling person for updated_by: nil for an API
// key or any caller that is not a users row.
func callerUserID(ctx context.Context) *uuid.UUID {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || actor.Type != auth.ActorTypeUser {
		return nil
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil
	}
	return &id
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// fieldRefused answers 422 for a field the sanitizer or a limit refuses.
func fieldRefused(w http.ResponseWriter, r *http.Request, fe *contenttranslate.FieldError) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"translation.invalid_field", "the field "+fe.Field+" was refused: "+fe.Reason, r,
		map[string]any{"field": fe.Field, "reason": fe.Reason}))
}

func invalidLocale(w http.ResponseWriter, r *http.Request, why string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"translation.invalid_locale", why, r,
		map[string]any{"supported_locales": contenttranslate.Supported}))
}

func pathLocaleRaw(r *http.Request) string { return chi.URLParam(r, "locale") }

// pathLocale returns the normalized {locale} of the path when it is supported
// and differs from the content locale.
func pathLocale(r *http.Request, contentLocale string) (string, bool) {
	loc, ok := contenttranslate.NormalizeLocale(pathLocaleRaw(r))
	if !ok || loc == contentLocale {
		return "", false
	}
	return loc, true
}
