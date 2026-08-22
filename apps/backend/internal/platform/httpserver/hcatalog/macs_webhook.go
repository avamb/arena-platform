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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

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
	if req.CallbackURL == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"macs.missing_callback_url", "callback_url is required", r,
		))
		return
	}

	q := gen.New(pool)
	ctx := r.Context()

	// Deactivate any existing active MACS subscriber for this org.
	_, _ = q.DeactivateMACSSubscriberByOrg(ctx, orgID)

	// Create the new subscriber.
	row, err := q.CreateMACSSubscriber(ctx, orgID, req.CallbackURL, req.SigningSecret)
	if err != nil {
		h.logger.Error("macs-webhook: create subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"macs.webhook_create_failed", "failed to create MACS webhook subscriber", r,
		))
		return
	}

	// Include signing_secret on PUT response so callers can record it.
	httputil.WriteJSON(w, http.StatusOK, macsWebhookResponse(row, true))
}

// HandleDeleteMACSWebhook handles DELETE /v1/organizations/{org_id}/macs-webhook.
func (h *Handler) HandleDeleteMACSWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
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

	httputil.WriteJSON(w, http.StatusOK, macsWebhookResponse(row, false))
}
