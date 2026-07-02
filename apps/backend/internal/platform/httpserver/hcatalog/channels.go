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
package hcatalog

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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response type
// ─────────────────────────────────────────────────────────────────────────────

type channelResponse = ChannelResponse

// ChannelResponse is the exported form of channelResponse, for use by the
// httpserver shim layer (channels_test.go references channelResponse directly
// from package httpserver via a type alias in catalog_shims.go).
type ChannelResponse struct {
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

// SettingsForResponse normalizes a raw channel-settings payload for API
// responses; the httpserver shim layer forwards to it (channels_test.go calls
// settingsForResponse from package httpserver via catalog_shims.go).
func SettingsForResponse(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

// MaskProviderAccountID returns a masked rendering of the merchant credential
// suitable for read endpoints. Preserves the last 4 characters.
func MaskProviderAccountID(in *string) *string {
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

func channelFromRow(ch gen.SalesChannelRow) channelResponse {
	return ChannelFromRow(ch)
}

// ChannelFromRow is the exported form of channelFromRow, for use by the
// httpserver shim layer (channels_test.go calls channelFromRow from package
// httpserver via a forwarder in catalog_shims.go).
func ChannelFromRow(ch gen.SalesChannelRow) ChannelResponse {
	return ChannelResponse{
		ID:                     ch.ID.String(),
		OrgID:                  ch.OrgID.String(),
		Name:                   ch.Name,
		PaymentMode:            ch.PaymentMode,
		Provider:               ch.Provider,
		ProviderAccountID:      ch.ProviderAccountID,
		FeePercent:             ch.FeePercent,
		ReservationTTLOverride: ch.ReservationTTLOverride,
		Settings:               SettingsForResponse(ch.Settings),
		CreatedAt:              ch.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              ch.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func channelFromRowMasked(ch gen.SalesChannelRow) channelResponse {
	return ChannelFromRowMasked(ch)
}

// ChannelFromRowMasked is the exported form of channelFromRowMasked, for use by
// the httpserver shim layer (channels_test.go calls channelFromRowMasked from
// package httpserver via a forwarder in catalog_shims.go).
func ChannelFromRowMasked(ch gen.SalesChannelRow) ChannelResponse {
	resp := ChannelFromRow(ch)
	resp.ProviderAccountID = MaskProviderAccountID(ch.ProviderAccountID)
	return resp
}

// ─────────────────────────────────────────────────────────────────────────────
// Config validation
// ─────────────────────────────────────────────────────────────────────────────

// ValidateChannelConfig enforces the business rules for payment mode / provider
// combinations. Returns a descriptive error message or "" when valid.
func ValidateChannelConfig(paymentMode, provider, providerAccountID string) string {
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

	if paymentMode == "direct_merchant" && strings.TrimSpace(providerAccountID) == "" {
		return "provider_account_id is required when payment_mode is 'direct_merchant'"
	}

	return ""
}

// NormalizeChannelSettings validates and returns the canonical form of the
// settings JSON field. Returns a non-empty error message when invalid.
func NormalizeChannelSettings(raw json.RawMessage) (json.RawMessage, string) {
	if len(raw) == 0 {
		return nil, ""
	}
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

type createChannelRequest struct {
	Name                   string          `json:"name"`
	PaymentMode            string          `json:"payment_mode"`
	Provider               string          `json:"provider"`
	ProviderAccountID      string          `json:"provider_account_id"`
	FeePercent             string          `json:"fee_percent"`
	ReservationTTLOverride *int32          `json:"reservation_ttl_override"`
	Settings               json.RawMessage `json:"settings"`
}

func (h *Handler) HandleCreateChannel(w http.ResponseWriter, r *http.Request) {
	if h.channelQueries == nil || h.pool == nil {
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

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.empty_body", "request body is required", r))
		return
	}

	var req createChannelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PaymentMode = strings.TrimSpace(req.PaymentMode)
	req.Provider = strings.TrimSpace(req.Provider)
	req.ProviderAccountID = strings.TrimSpace(req.ProviderAccountID)

	if req.Name == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"channel.invalid_name", "name is required", r,
			map[string]any{"field": "name"},
		))
		return
	}

	if req.PaymentMode == "" {
		req.PaymentMode = "direct_merchant"
	}
	if req.Provider == "" {
		req.Provider = "stripe"
	}
	if req.FeePercent == "" {
		req.FeePercent = "0.00"
	}

	if msg := ValidateChannelConfig(req.PaymentMode, req.Provider, req.ProviderAccountID); msg != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
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

	settings, settingsErr := NormalizeChannelSettings(req.Settings)
	if settingsErr != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"channel.invalid_settings", settingsErr, r,
			map[string]any{"field": "settings"},
		))
		return
	}

	ch, err := h.channelQueries.InsertSalesChannel(ctx,
		orgID, req.Name, req.PaymentMode, req.Provider,
		providerAccountID, req.FeePercent, req.ReservationTTLOverride, settings,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"channel.duplicate",
				"a channel with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("channel: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.insert_failed", "failed to create sales channel", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"channel": channelFromRow(ch),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleListChannels(w http.ResponseWriter, r *http.Request) {
	if h.channelQueries == nil {
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

	rows, err := h.channelQueries.ListSalesChannelsByOrg(ctx, orgID)
	if err != nil {
		h.logger.Error("channel: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.list_failed", "failed to list sales channels", r,
		))
		return
	}

	result := make([]channelResponse, 0, len(rows))
	for _, ch := range rows {
		result = append(result, channelFromRowMasked(ch))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"channels": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleGetChannel(w http.ResponseWriter, r *http.Request) {
	if h.channelQueries == nil {
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
	chID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	ch, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		h.logger.Error("channel: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRowMasked(ch),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PATCH /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

type updateChannelRequest struct {
	Name                   string          `json:"name"`
	PaymentMode            string          `json:"payment_mode"`
	Provider               string          `json:"provider"`
	ProviderAccountID      *string         `json:"provider_account_id"`
	FeePercent             *string         `json:"fee_percent"`
	ReservationTTLOverride *int32          `json:"reservation_ttl_override"`
	Settings               json.RawMessage `json:"settings"`
}

func (h *Handler) HandleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if h.channelQueries == nil || h.pool == nil {
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
	chID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.empty_body", "request body is required", r))
		return
	}

	var req updateChannelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("channel.invalid_json", "request body is not valid JSON", r))
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.PaymentMode = strings.TrimSpace(req.PaymentMode)
	req.Provider = strings.TrimSpace(req.Provider)

	if req.PaymentMode != "" || req.Provider != "" {
		providerAccountID := ""
		if req.ProviderAccountID != nil {
			providerAccountID = strings.TrimSpace(*req.ProviderAccountID)
		}
		if req.PaymentMode == "direct_merchant" {
			if msg := ValidateChannelConfig(req.PaymentMode, req.Provider, providerAccountID); msg != "" {
				httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
					"channel.invalid_config", msg, r,
					map[string]any{"field": "payment_mode"},
				))
				return
			}
		}
		if req.PaymentMode != "" && req.PaymentMode != "direct_merchant" && req.PaymentMode != "merchant_of_record" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"channel.invalid_config",
				fmt.Sprintf("payment_mode must be 'direct_merchant' or 'merchant_of_record', got %q", req.PaymentMode),
				r,
				map[string]any{"field": "payment_mode"},
			))
			return
		}
		if req.Provider != "" && req.Provider != "stripe" && req.Provider != "allpay" {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
				"channel.invalid_config",
				fmt.Sprintf("provider must be 'stripe' or 'allpay', got %q", req.Provider),
				r,
				map[string]any{"field": "provider"},
			))
			return
		}
	}

	settings, settingsErr := NormalizeChannelSettings(req.Settings)
	if settingsErr != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"channel.invalid_settings", settingsErr, r,
			map[string]any{"field": "settings"},
		))
		return
	}

	updated, err := h.channelQueries.UpdateSalesChannel(ctx,
		chID, orgID, req.Name, req.PaymentMode, req.Provider,
		req.ProviderAccountID, req.FeePercent, req.ReservationTTLOverride, settings,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"channel.duplicate",
				"a channel with that name already exists in this organization",
				r,
			))
			return
		}
		h.logger.Error("channel: update failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.update_failed", "failed to update sales channel", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRow(updated),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/channels/{id}
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) HandleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if h.channelQueries == nil || h.pool == nil {
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
	chID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.channelQueries.WithTx(tx)

	deleted, err := qtx.SoftDeleteSalesChannel(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("channel.not_found", "sales channel not found", r))
			return
		}
		h.logger.Error("channel: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.delete_failed", "failed to delete sales channel", r,
		))
		return
	}

	if h.audit != nil {
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
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"channel_name": deleted.Name,
				"org_id":       orgID.String(),
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("channel: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"channel.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"channel": channelFromRow(deleted),
		"deleted": true,
	})
}
