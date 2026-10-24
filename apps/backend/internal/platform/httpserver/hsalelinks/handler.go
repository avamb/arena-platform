// Package hsalelinks serves the organization routes of tracked sales links
// (spec 43 section 4.2, task QL-4):
//
//	POST  /v1/organizations/{org_id}/sales-links
//	GET   /v1/organizations/{org_id}/sales-links
//	GET   /v1/organizations/{org_id}/sales-links/{id}
//	PATCH /v1/organizations/{org_id}/sales-links/{id}
//	GET   /v1/organizations/{org_id}/sales-links/{id}/stats
//
// Reads need sales_link.read, writes sales_link.manage (the mount applies the
// gate and the membership check of {org_id}). Every row is loaded by
// (id, org_id): a link of another organization is the route's own 404
// sales_link.not_found, and a target (event, session, promoter) of another
// organization is a 422 sales_link.invalid_target, never a 404.
//
// The QR image route of QL-5 belongs to this package too and is not here yet.
package hsalelinks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// maxBodyBytes bounds every JSON body of these routes.
const maxBodyBytes = 16 << 10

// Handler serves the sales link routes.
type Handler struct {
	pool    *pgxpool.Pool
	queries *gen.Queries
	audit   audit.Writer
	logger  *slog.Logger

	// shortBase is SHORT_LINK_BASE_URL, ticketsBase PUBLIC_TICKETS_BASE_URL.
	shortBase   string
	ticketsBase string
}

// New builds the handler. pool may be nil: every route then answers 503.
func New(pool *pgxpool.Pool, auditWriter audit.Writer, logger *slog.Logger, shortBase, ticketsBase string) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{pool: pool, audit: auditWriter, logger: logger, shortBase: shortBase, ticketsBase: ticketsBase}
	if pool != nil {
		h.queries = gen.New(pool)
	}
	return h
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) bool {
	if h.pool == nil || h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return false
	}
	return true
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, msg, r))
}

func (h *Handler) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.Error("sales links: "+what+" failed", slog.String("error", err.Error()))
	writeErr(w, r, http.StatusInternalServerError, "sales_link.failed", "could not complete the request, please try again")
}

// orgID reads {org_id}.
func (h *Handler) orgID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if !h.ready(w, r) {
		return uuid.Nil, false
	}
	return httputil.UUIDPathParam(w, r, "org_id")
}

// linkID reads {id}; a malformed id is the route's own 404 (the row cannot exist).
func linkID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil {
		writeErr(w, r, http.StatusNotFound, "sales_link.not_found", "sales link not found")
		return uuid.Nil, false
	}
	return id, true
}

// readBody decodes the JSON body strictly: one object, no unknown members.
func readBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(raw) > maxBodyBytes || strings.TrimSpace(string(raw)) == "" {
		writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_body",
			"the request body is missing or too large, please send a JSON object as the documentation shows")
		return false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_body",
			"the request body is not valid JSON for this route, please check the field names and types")
		return false
	}
	return true
}

// beginTx starts a transaction and returns it with queries bound to it.
func (h *Handler) beginTx(ctx context.Context) (pgx.Tx, *gen.Queries, error) {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, nil, err
	}
	return tx, h.queries.WithTx(tx), nil
}

// writeAudit records one change inside its transaction. A handler built without
// an audit writer skips it (unit tests); production always has one.
func (h *Handler) writeAudit(ctx context.Context, tx pgx.Tx, r *http.Request, action string, link gen.SalesLinkRow, meta map[string]any) error {
	if h.audit == nil {
		return nil
	}
	actor, _ := auth.ActorFromContext(ctx)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["org_id"] = link.OrgID.String()
	meta["code"] = link.Code
	return h.audit.WriteTx(ctx, tx, audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user", // rewritten to api_key by audit.WithServiceActor
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "sales_link",
		ResourceID:   link.ID.String(),
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     meta,
	})
}

// createdBy is the acting user's id; an organization API key has none.
func createdBy(ctx context.Context) *uuid.UUID {
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

// createdVia is the client channel the request declared (allowlisted by the
// router's middleware), else "api".
func createdVia(ctx context.Context) string {
	if v, ok := audit.ClientChannelFromContext(ctx); ok {
		return v
	}
	return "api"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	httputil.WriteJSON(w, status, payload)
}

// shortHost is the host of SHORT_LINK_BASE_URL: the host a new link is issued
// under, recorded on its row. Empty (the column default) when the base is not
// a usable address.
func (h *Handler) shortHost() string {
	u, err := url.Parse(strings.TrimSpace(h.shortBase))
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Host)
}
