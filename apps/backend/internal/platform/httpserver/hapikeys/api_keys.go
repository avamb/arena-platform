// api_keys.go implements the HTTP handlers for the organization API-keys
// management surface (feature #514, W1-C1c; spec §13.1):
//
//	GET    /v1/organizations/{org_id}/api-keys       — list
//	POST   /v1/organizations/{org_id}/api-keys       — issue
//	DELETE /v1/organizations/{org_id}/api-keys/{id}  — revoke
//
// All three verbs require org membership (or platform-superadmin +
// X-Admin-Reason) via requireOrgMembership, the `api_key.manage` permission
// (enforced by the caller's route-mount, mirroring hbankaccounts), and the
// `X-Admin-Reason` header — this surface mints and destroys server-to-server
// credentials, the same superadmin-audit convention used by
// hcatalog/gateway_credential.go. Never expose key_hash in any response.
package hapikeys

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/api-keys
// ─────────────────────────────────────────────────────────────────────────────

// HandleList serves GET /v1/organizations/{org_id}/api-keys.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	if _, ok := httputil.RequireAdminReason(w, r); !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	rows, err := h.queries.ListAPIKeysByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("api_key: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"api_key.list_failed", "failed to list api keys", r,
		))
		return
	}

	result := make([]APIKeyResponse, 0, len(rows))
	for _, row := range rows {
		result = append(result, APIKeyFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"api_keys": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/api-keys
// ─────────────────────────────────────────────────────────────────────────────

// HandleCreate serves POST /v1/organizations/{org_id}/api-keys. It issues a
// fresh key via apikeys.Issue and returns the raw wire token exactly once.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}

	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	var req createAPIKeyRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"api_key.invalid_body", "request body is not valid JSON: "+err.Error(), r,
		))
		return
	}

	if err := apikeys.ValidateScopes(req.Scopes); err != nil {
		status := http.StatusUnprocessableEntity
		code := "api_key.invalid_scopes"
		if errors.Is(err, apikeys.ErrForbiddenScope) {
			code = "api_key.forbidden_scope"
		}
		httputil.WriteJSON(w, status, httputil.ErrorEnvelope(code, err.Error(), r))
		return
	}

	actor, _ := auth.ActorFromContext(ctx)
	createdBy, err := actorUserID(actor)
	if err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"api_key.actor_required", "a user-authenticated actor is required to issue an api key", r,
		))
		return
	}

	store := apikeys.NewStoreFromQueries(h.queries)
	key, raw, err := apikeys.Issue(ctx, store, apikeys.IssueInput{
		OrgID:     orgID,
		ChannelID: req.ChannelID,
		Name:      req.Name,
		Scopes:    req.Scopes,
		CreatedBy: createdBy,
		ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		if errors.Is(err, apikeys.ErrNameRequired) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"api_key.name_required", "name is required", r,
			))
			return
		}
		if errors.Is(err, apikeys.ErrEmptyScopes) || errors.Is(err, apikeys.ErrForbiddenScope) {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"api_key.invalid_scopes", err.Error(), r,
			))
			return
		}
		h.logger.Error("api_key: issue failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"api_key.create_failed", "failed to create api key", r,
		))
		return
	}

	h.writeAPIKeyAudit(ctx, r, "v1.api_key.create", key.ID.String(), map[string]any{
		"org_id": orgID.String(),
		"name":   key.Name,
		"scopes": key.Scopes,
		"reason": reason,
	})

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"api_key": CreateAPIKeyResponse{
			APIKeyResponse: apiKeyResponseFrom(key),
			APIKey:         raw,
		},
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/api-keys/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleRevoke serves DELETE /v1/organizations/{org_id}/api-keys/{id}.
// Revoking an already-revoked key is idempotent (still 200); revoking a key
// that never existed in this org answers 404.
func (h *Handler) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, orgID) {
		return
	}

	existing, err := h.queries.GetAPIKeyByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"api_key.not_found", "api key not found", r,
			))
			return
		}
		h.logger.Error("api_key: pre-revoke get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"api_key.revoke_failed", "failed to revoke api key", r,
		))
		return
	}

	// RevokeAPIKey's WHERE revoked_at IS NULL clause makes it a no-op (no
	// error) when the key is already revoked; re-fetch to get the
	// authoritative row (including the original revoked_at) for the response.
	if err := h.queries.RevokeAPIKey(ctx, id, orgID); err != nil {
		h.logger.Error("api_key: revoke failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"api_key.revoke_failed", "failed to revoke api key", r,
		))
		return
	}

	revoked := existing
	if existing.RevokedAt == nil {
		refreshed, err := h.queries.GetAPIKeyByID(ctx, id, orgID)
		if err != nil {
			h.logger.Error("api_key: post-revoke get failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"api_key.revoke_failed", "failed to revoke api key", r,
			))
			return
		}
		revoked = refreshed

		h.writeAPIKeyAudit(ctx, r, "v1.api_key.revoke", id.String(), map[string]any{
			"org_id": orgID.String(),
			"name":   revoked.Name,
			"reason": reason,
		})
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"api_key": APIKeyFromRow(revoked),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared helpers
// ─────────────────────────────────────────────────────────────────────────────

// actorUserID parses actor.ID as a UUID, returning an error for empty or
// non-UUID actors (e.g. an unauthenticated or malformed context) — an api
// key can only ever be attributed to a real user, never to another service
// actor (api_key.manage is a forbidden scope, so a service actor can never
// legitimately reach this handler in the first place).
func actorUserID(actor auth.Actor) (uuid.UUID, error) {
	return uuid.Parse(actor.ID)
}

func apiKeyResponseFrom(key apikeys.APIKey) APIKeyResponse {
	return APIKeyResponse{
		ID:         key.ID,
		OrgID:      key.OrgID,
		ChannelID:  key.ChannelID,
		Name:       key.Name,
		KeyPrefix:  key.KeyPrefix,
		Scopes:     key.Scopes,
		CreatedBy:  key.CreatedBy,
		CreatedAt:  key.CreatedAt,
		LastUsedAt: key.LastUsedAt,
		ExpiresAt:  key.ExpiresAt,
		RevokedAt:  key.RevokedAt,
	}
}

// writeAPIKeyAudit emits a best-effort (non-transactional) audit event. The
// api-key mutation itself is not part of a caller-visible transaction (Issue
// / RevokeAPIKey are single statements), so unlike hbankaccounts this does
// not need a WriteTx variant.
func (h *Handler) writeAPIKeyAudit(ctx context.Context, r *http.Request, action, resourceID string, metadata map[string]any) {
	if h.audit == nil {
		return
	}
	actor, _ := auth.ActorFromContext(ctx)
	ev := audit.Event{
		OccurredAt:   time.Now().UTC(),
		ActorType:    "user",
		ActorID:      actor.ID,
		Action:       action,
		ResourceType: "api_key",
		ResourceID:   resourceID,
		RequestID:    logging.RequestID(ctx),
		TraceID:      logging.TraceID(ctx),
		IP:           httputil.ExtractClientIP(r),
		Metadata:     metadata,
	}
	if err := h.audit.Write(ctx, ev); err != nil {
		h.logger.Error("api_key: audit write failed", slog.String("error", err.Error()))
	}
}
