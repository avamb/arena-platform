// channels.go implements the sales channel CRUD API endpoints (feature #121).
//
// Sales channels define the payment and commercial configuration for an
// organization (ADR-011: direct-merchant default). Each channel is scoped to
// one organization and controls how payments are processed.
//
// Endpoints:
//
//	POST   /v1/organizations/{org_id}/channels        — create a sales channel (channel.create)
//	GET    /v1/organizations/{org_id}/channels        — list channels for an org (channel.read)
//	GET    /v1/organizations/{org_id}/channels/{id}   — get a single channel (channel.read)
//	PATCH  /v1/organizations/{org_id}/channels/{id}   — update a channel (channel.update)
//	DELETE /v1/organizations/{org_id}/channels/{id}   — soft-delete a channel (channel.delete)
//
// All endpoints are gated by JWT auth + a named permission.
//
// Config validation (Step 3):
//
//	When payment_mode = "direct_merchant", provider_account_id is required.
//	This is enforced in validateChannelConfig() before any DB write.
//
// TTL override resolution (Step 4):
//
//	reservation_ttl_override overrides the parent organization's
//	reservation_ttl_seconds when non-nil. The effective TTL is included in the
//	response as effective_reservation_ttl_seconds.
package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

// channelResponse is the JSON representation of a single sales channel.
//
// provider_account_id is masked in GET/LIST responses (channelFromRowMasked)
// so the raw merchant credential is never echoed back to API consumers.
// Create/update responses keep the raw value so callers can verify what
// they just wrote (channelFromRow).
type channelResponse struct {
	ID                     string          `json:"id"`
	OrgID                  string          `json:"org_id"`
	Name                   string          `json:"name"`
	PaymentMode            string          `json:"payment_mode"`
	Provider               string          `json:"provider"`
	ProviderAccountID      *string         `json:"provider_account_id"`
	FeePercent             string          `json:"fee_percent"`
	ReservationTTLOverride *int32          `json:"reservation_ttl_override"`
	Settings               json.RawMessage `json:"settings"`
	CreatedAt              string          `json:"created_at"`
	UpdatedAt              string          `json:"updated_at"`
}

// settingsForResponse returns a non-nil JSON object for the response body.
// Empty/null DB values become "{}" so consumers never have to special-case
// missing settings.
func settingsForResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// maskProviderAccountID returns a masked rendering of the merchant credential
// suitable for read endpoints. The mask preserves the last 4 characters so
// operators can still recognise which account a channel is wired to, e.g.
//
//	"acct_1Q2W3E4R5T6Y" -> "****6Y"
//	"M123" -> "****" (anything <= 4 chars collapses to a fixed mask)
//	""     -> "" (empty stays empty; nil stays nil)
//
// Returning a pointer matches the SalesChannelRow shape; nil in -> nil out.
func maskProviderAccountID(in *string) *string {
	if in == nil {
		return nil
	}
	raw := *in
	if raw == "" {
		empty := ""
		return &empty
	}
	const tail = 4
	if len(raw) <= tail {
		masked := "****"
		return &masked
	}
	masked := "****" + raw[len(raw)-tail:]
	return &masked
}

// channelFromRow converts a SalesChannelRow to a channelResponse without
// touching the credentials. Use this only on write paths (POST/PATCH/DELETE
// responses) where the caller already knows the secret they just supplied.
func channelFromRow(ch gen.SalesChannelRow) channelResponse {
	return channelResponse{
		ID:                     ch.ID.String(),
		OrgID:                  ch.OrgID.String(),
		Name:                   ch.Name,
		PaymentMode:            ch.PaymentMode,
		Provider:               ch.Provider,
		ProviderAccountID:      ch.ProviderAccountID,
		FeePercent:             ch.FeePercent,
		ReservationTTLOverride: ch.ReservationTTLOverride,
		Settings:               settingsForResponse(ch.Settings),
		CreatedAt:              ch.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              ch.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// channelFromRowMasked is the GET-path serializer: it masks the merchant
// credential so unauthorised observers of a list/read response can never
// see the raw provider_account_id value (feature #236).
func channelFromRowMasked(ch gen.SalesChannelRow) channelResponse {
	resp := channelFromRow(ch)
	resp.ProviderAccountID = maskProviderAccountID(ch.ProviderAccountID)
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Config validation (Step 3)
// ─────────────────────────────────────────────────────────────────────────────

// validateChannelConfig enforces the business rules for payment mode / provider
// combinations:
//
//   - payment_mode must be "direct_merchant" or "merchant_of_record"
//   - provider must be "stripe" or "allpay"
//   - provider_account_id is REQUIRED when payment_mode = "direct_merchant"
//
// Returns a descriptive error message suitable for an API 400 response, or ""
// when the config is valid.
func validateChannelConfig(paymentMode, provider, providerAccountID string) string {
	switch paymentMode {
	case "direct_merchant", "merchant_of_record":
		// valid
	case "":
		return "payment_mode is required"
	default:
		return fmt.Sprintf("payment_mode must be 'direct_merchant' or 'merchant_of_record', got %q", paymentMode)
	}

	switch provider {
	case "stripe", "allpay":
		// valid
	case "":
		return "provider is required"
	default:
		return fmt.Sprintf("provider must be 'stripe' or 'allpay', got %q", provider)
	}

	// ADR-011: direct_merchant requires a provider account ID so the payment
	// provider knows which merchant account to credit.
	if paymentMode == "direct_merchant" && strings.TrimSpace(providerAccountID) == "" {
		return "provider_account_id is required when payment_mode is 'direct_merchant'"
	}

	return ""
}

// normalizeChannelSettings validates the raw JSON supplied by the client as
// the channel `settings` field. The value must either be omitted (nil/empty)
// or be a valid JSON object. JSON arrays, scalars, and malformed payloads
// are rejected with a 400.
//
// Returns the canonical representation to write to the DB and a non-empty
// error message when invalid.
func normalizeChannelSettings(raw json.RawMessage) (json.RawMessage, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	// json.RawMessage may carry whitespace; verify it parses as an object.
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, "settings must be a valid JSON object"
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, "settings must be a JSON object (not an array or scalar)"
	}
	return raw, ""
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/channels
// ─────────────────────────────────────────────────────────────────────────────

// createChannelRequest is the request body for POST /v1/organizations/{org_id}/channels.
type createChannelRequest struct {
	Name                   string          `json:"name"`
	PaymentMode            string          `json:"payment_mode"`
	Provider               string          `json:"provider"`
	ProviderAccountID      string          `json:"provider_account_id"`
	FeePercent             string          `json:"fee_percent"`
	ReservationTTLOverride *int32          `json:"reservation_ttl_override"`
	Settings               json.RawMessage `json:"settings"`
}

// handleCreateChannel serves POST /v1/organizations/{org_id}/channels.
// Requires JWT + "channel.create" permission.
func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if s.channelQueries == nil || s.pool == nil {
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
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.empty_body", "request body is required", r))
		return
	}

	var req createChannelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Normalize.
	req.Name = strings.TrimSpace(req.Name)
	req.PaymentMode = strings.TrimSpace(req.PaymentMode)
	req.Provider = strings.TrimSpace(req.Provider)
	req.ProviderAccountID = strings.TrimSpace(req.ProviderAccountID)

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"channel.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	// Apply defaults.
	if req.PaymentMode == "" {
		req.PaymentMode = "direct_merchant"
	}
	if req.Provider == "" {
		req.Provider = "stripe"
	}
	if req.FeePercent == "" {
		req.FeePercent = "0.00"
	}

	// Config validation (Step 3).
	if msg := validateChannelConfig(req.PaymentMode, req.Provider, req.ProviderAccountID); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"channel.invalid_config", msg, r,
			map[string]any{"field": "payment_mode"},
		))
		return
	}

	var providerAccountID *string
	if req.ProviderAccountID != "" {
		s := req.ProviderAccountID
		providerAccountID = &s
	}

	// Validate settings is a JSON object when provided.
	settings, settingsErr := normalizeChannelSettings(req.Settings)
	if settingsErr != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"channel.invalid_settings", settingsErr, r,
			map[string]any{"field": "settings"},
		))
		return
	}

	ch, err := s.channelQueries.InsertSalesChannel(ctx,
		orgID, req.Name, req.PaymentMode, req.Provider,
		providerAccountID, req.FeePercent, req.ReservationTTLOverride, settings,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"channel.duplicate",
				"a channel with that name already exists in this organization",
				r,
			))
			return
		}
		s.logger.Error("channel: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.insert_failed", "failed to create sales channel", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"channel": channelFromRow(ch),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels
// ─────────────────────────────────────────────────────────────────────────────

// handleListChannels serves GET /v1/organizations/{org_id}/channels.
// Requires JWT + "channel.read" permission.
func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	if s.channelQueries == nil {
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

	rows, err := s.channelQueries.ListSalesChannelsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("channel: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.list_failed", "failed to list sales channels", r,
		))
		return
	}

	result := make([]channelResponse, 0, len(rows))
	for _, ch := range rows {
		result = append(result, channelFromRowMasked(ch))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetChannel serves GET /v1/organizations/{org_id}/channels/{id}.
// Requires JWT + "channel.read" permission.
func (s *Server) handleGetChannel(w http.ResponseWriter, r *http.Request) {
	if s.channelQueries == nil {
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
	chID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	ch, err := s.channelQueries.GetSalesChannelByID(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		s.logger.Error("channel: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRowMasked(ch),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

// updateChannelRequest is the request body for PATCH /v1/organizations/{org_id}/channels/{id}.
// All fields are optional; empty/nil values leave the existing value unchanged.
type updateChannelRequest struct {
	Name                   string          `json:"name"`
	PaymentMode            string          `json:"payment_mode"`
	Provider               string          `json:"provider"`
	ProviderAccountID      *string         `json:"provider_account_id"`
	FeePercent             *string         `json:"fee_percent"`
	ReservationTTLOverride *int32          `json:"reservation_ttl_override"`
	Settings               json.RawMessage `json:"settings"`
}

// handleUpdateChannel serves PATCH /v1/organizations/{org_id}/channels/{id}.
// Requires JWT + "channel.update" permission.
func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if s.channelQueries == nil || s.pool == nil {
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
	chID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.empty_body", "request body is required", r))
		return
	}

	var req updateChannelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("channel.invalid_json", "request body is not valid JSON", r))
		return
	}

	// Normalize string fields.
	req.Name = strings.TrimSpace(req.Name)
	req.PaymentMode = strings.TrimSpace(req.PaymentMode)
	req.Provider = strings.TrimSpace(req.Provider)

	// Validate config if any config fields are being updated.
	if req.PaymentMode != "" || req.Provider != "" {
		providerAccountID := ""
		if req.ProviderAccountID != nil {
			providerAccountID = strings.TrimSpace(*req.ProviderAccountID)
		}
		// We only validate the combination when payment_mode is explicitly set to direct_merchant.
		// Partial updates that only change one field may not trigger this — the DB constraint
		// will catch any mismatches for partial updates.
		if req.PaymentMode == "direct_merchant" {
			if msg := validateChannelConfig(req.PaymentMode, req.Provider, providerAccountID); msg != "" {
				writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
					"channel.invalid_config", msg, r,
					map[string]any{"field": "payment_mode"},
				))
				return
			}
		}
		if req.PaymentMode != "" && req.PaymentMode != "direct_merchant" && req.PaymentMode != "merchant_of_record" {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"channel.invalid_config",
				fmt.Sprintf("payment_mode must be 'direct_merchant' or 'merchant_of_record', got %q", req.PaymentMode),
				r,
				map[string]any{"field": "payment_mode"},
			))
			return
		}
		if req.Provider != "" && req.Provider != "stripe" && req.Provider != "allpay" {
			writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
				"channel.invalid_config",
				fmt.Sprintf("provider must be 'stripe' or 'allpay', got %q", req.Provider),
				r,
				map[string]any{"field": "provider"},
			))
			return
		}
	}

	// Validate settings is a JSON object when provided.
	settings, settingsErr := normalizeChannelSettings(req.Settings)
	if settingsErr != "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelopeWithDetails(
			"channel.invalid_settings", settingsErr, r,
			map[string]any{"field": "settings"},
		))
		return
	}

	updated, err := s.channelQueries.UpdateSalesChannel(ctx,
		chID, orgID, req.Name, req.PaymentMode, req.Provider,
		req.ProviderAccountID, req.FeePercent, req.ReservationTTLOverride, settings,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"channel.duplicate",
				"a channel with that name already exists in this organization",
				r,
			))
			return
		}
		s.logger.Error("channel: update failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.update_failed", "failed to update sales channel", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleDeleteChannel serves DELETE /v1/organizations/{org_id}/channels/{id}.
// Performs a soft-delete (sets deleted_at = now()) and writes an audit event.
// Requires JWT + "channel.delete" permission.
func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if s.channelQueries == nil || s.pool == nil {
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
	chID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: soft-delete + audit in one atomic write.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.channelQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteSalesChannel(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		s.logger.Error("channel: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.delete_failed", "failed to delete sales channel", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if s.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.channel.delete",
			ResourceType: "sales_channel",
			ResourceID:   chID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"channel_name": deleted.Name,
				"org_id":       orgID.String(),
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("channel: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"channel.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"channel.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRow(deleted),
		"deleted": true,
	})
}
