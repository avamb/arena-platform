// payment_configs_write.go houses the POST and PATCH handlers for the
// payment-provider-config CRUD surface (feature #237). Splitting the
// write surface out of payment_configs.go keeps each file under the
// internal/platform/httpserver/ size budget enforced by the
// httpserver_file_size_175_test gate (feature #175).
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ─────────────────────────────────────────────────────────────────────────────
// Request bodies
// ─────────────────────────────────────────────────────────────────────────────

// createPaymentConfigRequest is the request body for POST.
type createPaymentConfigRequest struct {
	Provider          string            `json:"provider"`
	Mode              string            `json:"mode"`
	ProviderAccountID *string           `json:"provider_account_id"`
	PublicConfig      json.RawMessage   `json:"public_config"`
	Secrets           map[string]string `json:"secrets"`
	IsActive          *bool             `json:"is_active"`
}

// updatePaymentConfigRequest is the partial-update body. The `secrets`
// field is a patch map: non-empty value REPLACES the existing value;
// empty-string value DELETES the key; keys not mentioned are untouched.
type updatePaymentConfigRequest struct {
	ProviderAccountID *string           `json:"provider_account_id"`
	PublicConfig      json.RawMessage   `json:"public_config"`
	Secrets           map[string]string `json:"secrets"`
	IsActive          *bool             `json:"is_active"`
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/payment-configs
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleCreatePaymentConfig(w http.ResponseWriter, r *http.Request) {
	if s.paymentConfigQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.empty_body", "request body is required", r))
		return
	}

	var req createPaymentConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Provider = strings.TrimSpace(strings.ToLower(req.Provider))
	req.Mode = strings.TrimSpace(strings.ToLower(req.Mode))

	if req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_config.invalid_provider", "provider is required", r,
			map[string]any{"field": "provider", "allowed": supportedProviderList()},
		))
		return
	}
	if !supportedPaymentProviders[req.Provider] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_config.unsupported_provider",
			fmt.Sprintf("provider %q is not supported", req.Provider), r,
			map[string]any{"field": "provider", "allowed": supportedProviderList()},
		))
		return
	}
	if req.Mode == "" {
		req.Mode = "test"
	}
	if !supportedModes[req.Mode] {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"payment_config.invalid_mode", "mode must be 'test' or 'live'", r,
			map[string]any{"field": "mode", "allowed": []string{"test", "live"}},
		))
		return
	}

	// Optional provider_account_id: trim and treat empty as nil.
	var providerAccountID *string
	if req.ProviderAccountID != nil {
		trimmed := strings.TrimSpace(*req.ProviderAccountID)
		if trimmed != "" {
			providerAccountID = &trimmed
		}
	}

	// public_config: validate it is a JSON object when supplied.
	publicConfig := req.PublicConfig
	if len(publicConfig) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(publicConfig, &probe); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"payment_config.invalid_public_config",
				"public_config must be a JSON object", r,
				map[string]any{"field": "public_config"},
			))
			return
		}
	}

	// Build the secrets jsonb from the patch map (empty patch -> '{}').
	secretsJSON, _, err := mergeSecrets(nil, req.Secrets)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"payment_config.invalid_secrets", err.Error(), r,
		))
		return
	}

	status := deriveStatus(req.Provider, secretsJSON)
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	row, err := s.paymentConfigQueries.InsertPaymentProviderConfig(
		ctx, orgID, req.Provider, req.Mode, providerAccountID,
		publicConfig, secretsJSON, status, isActive,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"payment_config.duplicate",
				"a payment provider config for this provider+mode already exists in this organization",
				r,
			))
			return
		}
		s.logger.Error("payment_config: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.insert_failed", "failed to create payment config", r,
		))
		return
	}

	s.writePaymentConfigAudit(ctx, r, "v1.payment_config.create", row.ID.String(), map[string]any{
		"org_id":   orgID.String(),
		"provider": req.Provider,
		"mode":     req.Mode,
		"status":   status,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"payment_config": paymentConfigFromRow(row),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/payment-configs/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleUpdatePaymentConfig(w http.ResponseWriter, r *http.Request) {
	if s.paymentConfigQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	orgID, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.empty_body", "request body is required", r))
		return
	}

	var req updatePaymentConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("payment_config.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Fetch existing row so we can merge secrets and recompute status.
	existing, err := s.paymentConfigQueries.GetPaymentProviderConfigByID(ctx, id, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		s.logger.Error("payment_config: pre-update get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.update_failed", "failed to load payment config", r,
		))
		return
	}

	var providerAccountID *string
	if req.ProviderAccountID != nil {
		trimmed := strings.TrimSpace(*req.ProviderAccountID)
		providerAccountID = &trimmed
	}

	publicConfig := req.PublicConfig
	if len(publicConfig) > 0 {
		var probe map[string]any
		if err := json.Unmarshal(publicConfig, &probe); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"payment_config.invalid_public_config",
				"public_config must be a JSON object", r,
				map[string]any{"field": "public_config"},
			))
			return
		}
	}

	mergedSecrets, secretsChanged, err := mergeSecrets(existing.Secrets, req.Secrets)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"payment_config.invalid_secrets", err.Error(), r,
		))
		return
	}
	var secretsParam json.RawMessage
	if secretsChanged {
		secretsParam = mergedSecrets
	}

	status := deriveStatus(existing.Provider, mergedSecrets)

	row, err := s.paymentConfigQueries.UpdatePaymentProviderConfig(
		ctx, id, orgID, providerAccountID, publicConfig, secretsParam, status, req.IsActive,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("payment_config.not_found", "payment config not found", r))
			return
		}
		s.logger.Error("payment_config: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"payment_config.update_failed", "failed to update payment config", r,
		))
		return
	}

	s.writePaymentConfigAudit(ctx, r, "v1.payment_config.update", row.ID.String(), map[string]any{
		"org_id":          orgID.String(),
		"provider":        row.Provider,
		"mode":            row.Mode,
		"status":          status,
		"secrets_changed": secretsChanged,
		"is_active_set":   req.IsActive != nil,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"payment_config": paymentConfigFromRow(row),
	})
}
