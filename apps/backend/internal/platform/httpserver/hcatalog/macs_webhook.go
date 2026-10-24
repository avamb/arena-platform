// macs_webhook.go implements the MACS webhook subscriber management endpoints.
//
// AB-50c (feature #439):
//
//	GET    /v1/organizations/{org_id}/macs-webhook  — get active subscriber
//	PUT    /v1/organizations/{org_id}/macs-webhook  — upsert subscriber
//	DELETE /v1/organizations/{org_id}/macs-webhook  — deactivate subscriber
package hcatalog

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// macsCallbackPathSuffix is the only path a MACS receiver serves: the
// WordPress plugin mounts its importer at `/api/_wh/tickets`
// (class-lops-macs.php). A subscriber URL pointing anywhere else silently
// swallows every envelope — the site answers 200 with an HTML page and the
// outbox marks the sale delivered — so W1-Mb (spec §10 M4) rejects it at
// registration time instead of losing sales to a typo.
const macsCallbackPathSuffix = "/api/_wh/tickets"

// validMACSCallbackURL reports whether u addresses a MACS receiver. The
// PATH is judged, not the whole string, so the suffix cannot be smuggled in
// through a query or fragment. A trailing slash is tolerated (a site owner
// pasting from a browser bar usually carries one); a query or fragment is not.
// Whether the address may be reached at all is egress.Policy.CheckURL's job.
func validMACSCallbackURL(u string) bool {
	parsed, err := url.Parse(strings.TrimSpace(u))
	if err != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(u, "#") {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), macsCallbackPathSuffix)
}

// MACSWebhookResponse is the response body for MACS webhook subscriber endpoints.
type MACSWebhookResponse struct {
	ID            string `json:"id"`
	OrgID         string `json:"org_id"`
	CallbackURL   string `json:"callback_url"`
	SigningSecret string `json:"signing_secret,omitempty"`
	Active        bool   `json:"active"`
	Kind          string `json:"kind"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func macsWebhookResponse(row gen.WebhookSubscriberRow, includeSecret bool) MACSWebhookResponse {
	orgID := ""
	if row.OrgID != nil {
		orgID = row.OrgID.String()
	}
	r := MACSWebhookResponse{
		ID:          row.ID.String(),
		OrgID:       orgID,
		CallbackURL: row.CallbackURL,
		Active:      row.Active,
		Kind:        row.Kind,
		CreatedAt:   row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   row.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if includeSecret {
		r.SigningSecret = row.SigningSecret
	}
	return r
}

// HandleGetMACSWebhook handles GET /v1/organizations/{org_id}/macs-webhook.
func (h *Handler) HandleGetMACSWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.pool_unavailable", "database pool unavailable", r,
		))
		return
	}

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	q := gen.New(pool)
	row, err := q.GetMACSSubscriberByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"macs.webhook_not_found", "no active MACS webhook subscriber for this org", r,
			))
			return
		}
		h.logger.Error("macs-webhook: get subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.webhook_query_failed", "failed to query MACS webhook subscriber", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, macsWebhookResponse(row, false))
}

// macsWebhookUpsertRequest is the request body for PUT /macs-webhook.
type macsWebhookUpsertRequest struct {
	CallbackURL   string `json:"callback_url"`
	SigningSecret string `json:"signing_secret"`
}

// HandleUpsertMACSWebhook handles PUT /v1/organizations/{org_id}/macs-webhook.
// Deactivates any existing MACS subscriber for the org and creates a new one.
func (h *Handler) HandleUpsertMACSWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	// Platform-superadmin route (mount_catalog.go): the reason is part of
	// the request, so it is checked before anything else.
	if _, ok := httputil.RequireAdminReason(w, r); !ok {
		return
	}
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.pool_unavailable", "database pool unavailable", r,
		))
		return
	}

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	var req macsWebhookUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.invalid_body", "request body must be valid JSON", r,
		))
		return
	}
	req.CallbackURL = strings.TrimSpace(req.CallbackURL)
	if req.CallbackURL == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.missing_callback_url", "callback_url is required", r,
		))
		return
	}
	if !validMACSCallbackURL(req.CallbackURL) {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"macs.invalid_callback_url",
			"callback_url must end with "+macsCallbackPathSuffix,
			r,
		))
		return
	}
	if err := h.webhookEgress.CheckURL(r.Context(), req.CallbackURL); err != nil {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"macs.invalid_callback_url",
			"callback_url must be an absolute https:// address on the public internet", r,
		))
		return
	}

	q := gen.New(pool)
	ctx := r.Context()

	// Deactivate any existing active MACS subscriber for this org.
	_, _ = q.DeactivateMACSSubscriberByOrg(ctx, orgID)

	secret, err := resolveSigningSecret(req.SigningSecret)
	if err != nil {
		h.logger.Error("macs-webhook: generate signing secret failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.webhook_create_failed", "failed to create MACS webhook subscriber", r,
		))
		return
	}

	// Create the new subscriber.
	row, err := q.CreateMACSSubscriber(ctx, orgID, req.CallbackURL, secret)
	if err != nil {
		h.logger.Error("macs-webhook: create subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.webhook_create_failed", "failed to create MACS webhook subscriber", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(r.Context())
		if err := h.audit.Write(r.Context(), audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.macs_webhook.upsert",
			ResourceType: "macs_webhook",
			ResourceID:   row.ID.String(),
			RequestID:    logging.RequestID(r.Context()),
			TraceID:      logging.TraceID(r.Context()),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":          orgID.String(),
				"callback_origin": egress.Origin(req.CallbackURL),
			},
		}); err != nil {
			h.logger.Error("macs-webhook: audit write failed", "error", err.Error())
		}
	}

	// Include signing_secret on PUT response so callers can record it.
	httputil.WriteJSON(w, http.StatusOK, macsWebhookResponse(row, true))
}

// HandleDeleteMACSWebhook handles DELETE /v1/organizations/{org_id}/macs-webhook.
func (h *Handler) HandleDeleteMACSWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	// Platform-superadmin route (mount_catalog.go): the reason is part of
	// the request, so it is checked before anything else.
	if _, ok := httputil.RequireAdminReason(w, r); !ok {
		return
	}
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"macs.pool_unavailable", "database pool unavailable", r,
		))
		return
	}

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.sessionQueries, orgID) {
		return
	}

	q := gen.New(pool)
	row, err := q.DeactivateMACSSubscriberByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"macs.webhook_not_found", "no active MACS webhook subscriber for this org", r,
			))
			return
		}
		h.logger.Error("macs-webhook: deactivate subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.webhook_deactivate_failed", "failed to deactivate MACS webhook subscriber", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(r.Context())
		if err := h.audit.Write(r.Context(), audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.macs_webhook.delete",
			ResourceType: "macs_webhook",
			ResourceID:   row.ID.String(),
			RequestID:    logging.RequestID(r.Context()),
			TraceID:      logging.TraceID(r.Context()),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id": orgID.String(),
			},
		}); err != nil {
			h.logger.Error("macs-webhook: audit write failed", "error", err.Error())
		}
	}

	httputil.WriteJSON(w, http.StatusOK, macsWebhookResponse(row, false))
}
