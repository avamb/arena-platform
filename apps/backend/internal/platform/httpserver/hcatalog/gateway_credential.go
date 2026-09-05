// gateway_credential.go — Bil24-compat gateway credential admin endpoint
// (feature #473, W1-A1d; spec §5.4).
//
// Three routes, all gated on the `channel.update` permission and requiring
// the `X-Admin-Reason` header (superadmin-audit convention shared with the
// org/membership admin surfaces):
//
//	PUT    /v1/organizations/{org_id}/channels/{id}/gateway-credential
//	         → generates a fresh 32-byte secret, bcrypts it into
//	           settings.gateway.token_hash, flips settings.gateway.enabled=true,
//	           stamps settings.gateway.token_rotated_at, and returns the
//	           plaintext token ONCE together with the wire fid
//	           (channel.display_number), base_url and image_url derived from
//	           APP_PUBLIC_URL, and rotated_at.
//	GET    /v1/organizations/{org_id}/channels/{id}/gateway-credential
//	         → returns {fid, enabled, rotated_at} (token/hash never exposed).
//	DELETE /v1/organizations/{org_id}/channels/{id}/gateway-credential
//	         → flips settings.gateway.enabled=false (idempotent; token_hash is
//	           retained so a subsequent GET still reflects rotated_at).
//
// PUT and DELETE emit the audit action `v1.channel.gateway_credential.rotated`
// (the disable path is a rotation with an intentionally revoked outcome; the
// metadata `enabled` field distinguishes the two).
package hcatalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// gatewayTokenBytes is the length of the plaintext shared secret we mint. 32
// bytes → 64 hex chars → 256 bits of entropy, above the bcrypt 72-byte input
// cap and comfortably above any realistic guessing budget.
const gatewayTokenBytes = 32

// generateGatewayToken mints a fresh plaintext token and returns its bcrypt
// hash side-by-side. The token is hex-encoded so it survives round-tripping
// through JSON, URL, and shell contexts without escaping surprises.
//
// Exported for the sibling unit test which asserts (a) two invocations produce
// distinct tokens (crypto/rand is really the source), (b) the hash actually
// verifies the token, and (c) the token length matches spec.
func GenerateGatewayToken() (token, hash string, err error) {
	buf := make([]byte, gatewayTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(buf)
	h, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", "", err
	}
	return token, string(h), nil
}

// gatewayCredentialGetResponse is the read-shape returned by GET. The token is
// never exposed; only its presence (via `enabled`) and the last rotation time.
type gatewayCredentialGetResponse struct {
	FID       int64  `json:"fid"`
	Enabled   bool   `json:"enabled"`
	RotatedAt string `json:"rotated_at"`
}

// gatewayCredentialPutResponse is the one-shot response returned by PUT. This
// is the ONLY moment the plaintext `token` is visible: subsequent GETs never
// include it.
type gatewayCredentialPutResponse struct {
	FID       int64  `json:"fid"`
	Token     string `json:"token"`
	BaseURL   string `json:"base_url"`
	ImageURL  string `json:"image_url"`
	RotatedAt string `json:"rotated_at"`
}

// gatewaySettingsPersisted is the JSONB projection we serialise back into
// sales_channels.settings.gateway. The wire shape is fixed by spec §5.1 and
// mirrored on the read side by hbil24/auth.go:parseGatewaySettings.
type gatewaySettingsPersisted struct {
	Enabled        bool   `json:"enabled"`
	TokenHash      string `json:"token_hash,omitempty"`
	TokenRotatedAt string `json:"token_rotated_at,omitempty"`
	DefaultLocale  string `json:"default_locale,omitempty"`
}

// mergeGatewayIntoSettings decodes the existing settings JSONB, replaces (or
// creates) the `gateway` sub-object, and returns the new blob. Any other keys
// under `settings` are preserved verbatim so this endpoint never clobbers
// unrelated per-channel config (feature_token metadata, MACS creds, …).
func mergeGatewayIntoSettings(existing json.RawMessage, gw gatewaySettingsPersisted) (json.RawMessage, error) {
	obj := map[string]any{}
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &obj); err != nil {
			// Malformed on-disk settings are treated as empty: the admin
			// endpoint is the operator's escape hatch and must not fail on a
			// corrupted blob.
			obj = map[string]any{}
		}
	}
	obj["gateway"] = gw
	// Drop the legacy top-level fallback if present: PUT is the migration
	// event described in auth.go's parser doc comment.
	delete(obj, "gateway_token_hash")
	return json.Marshal(obj)
}

// extractGatewaySettings pulls out the persisted gateway block for the GET
// path. Returns the zero value when the block is absent so the caller can
// respond with enabled=false and an empty rotated_at.
func extractGatewaySettings(raw json.RawMessage) gatewaySettingsPersisted {
	if len(raw) == 0 {
		return gatewaySettingsPersisted{}
	}
	var shape struct {
		Gateway *gatewaySettingsPersisted `json:"gateway"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil || shape.Gateway == nil {
		return gatewaySettingsPersisted{}
	}
	return *shape.Gateway
}

// ─────────────────────────────────────────────────────────────────────────────
// GET
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetChannelGatewayCredential returns the current gateway-credential
// summary for a channel: `{fid, enabled, rotated_at}`. Never exposes the token
// or its hash. Requires org membership + X-Admin-Reason (superadmin path).
func (h *Handler) HandleGetChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
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

	if _, ok := httputil.RequireAdminReason(w, r); !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.channelQueries, orgID) {
		return
	}

	ch, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return
		}
		h.logger.Error("gateway_credential: channel lookup failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	gw := extractGatewaySettings(ch.Settings)
	httputil.WriteJSON(w, http.StatusOK, gatewayCredentialGetResponse{
		FID:       ch.DisplayNumber,
		Enabled:   gw.Enabled,
		RotatedAt: gw.TokenRotatedAt,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT (rotate / provision)
// ─────────────────────────────────────────────────────────────────────────────

// HandlePutChannelGatewayCredential mints a fresh gateway secret, persists its
// bcrypt hash under settings.gateway, flips enabled=true, and returns the
// plaintext token ONCE alongside the wire fid + WordPress-plugin URLs.
func (h *Handler) HandlePutChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
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

	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.channelQueries, orgID) {
		return
	}

	ch, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return
		}
		h.logger.Error("gateway_credential: channel lookup failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	token, hash, err := GenerateGatewayToken()
	if err != nil {
		h.logger.Error("gateway_credential: secret mint failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.gateway_credential.mint_failed", "failed to generate secret", r,
		))
		return
	}

	rotatedAt := time.Now().UTC()
	prev := extractGatewaySettings(ch.Settings)
	next := gatewaySettingsPersisted{
		Enabled:        true,
		TokenHash:      hash,
		TokenRotatedAt: rotatedAt.Format(time.RFC3339),
		DefaultLocale:  prev.DefaultLocale, // preserve caller-managed locale
	}
	updated, err := mergeGatewayIntoSettings(ch.Settings, next)
	if err != nil {
		h.logger.Error("gateway_credential: settings merge failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.gateway_credential.merge_failed", "failed to merge settings", r,
		))
		return
	}

	if err := h.persistGatewaySettings(ctx, chID, orgID, updated, "rotated", reason, r); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return
		}
		h.logger.Error("gateway_credential: persist failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.gateway_credential.persist_failed", "failed to persist gateway credential", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, gatewayCredentialPutResponse{
		FID:       ch.DisplayNumber,
		Token:     token,
		BaseURL:   h.publicBaseURL, // empty string when APP_PUBLIC_URL unset (spec §5.4)
		ImageURL:  h.publicBaseURL, // same origin; the plugins concatenate /compat/bil24/image?fid=… themselves
		RotatedAt: rotatedAt.Format(time.RFC3339),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE (disable)
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteChannelGatewayCredential flips settings.gateway.enabled=false.
// The token_hash is retained so an operator can inspect the previous rotation
// timestamp via GET; a follow-up PUT is required to re-enable.
func (h *Handler) HandleDeleteChannelGatewayCredential(w http.ResponseWriter, r *http.Request) {
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

	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}

	if !h.requireOrgMembership(w, r, h.channelQueries, orgID) {
		return
	}

	ch, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return
		}
		h.logger.Error("gateway_credential: channel lookup failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return
	}

	prev := extractGatewaySettings(ch.Settings)
	prev.Enabled = false
	updated, err := mergeGatewayIntoSettings(ch.Settings, prev)
	if err != nil {
		h.logger.Error("gateway_credential: settings merge failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.gateway_credential.merge_failed", "failed to merge settings", r,
		))
		return
	}

	if err := h.persistGatewaySettings(ctx, chID, orgID, updated, "disabled", reason, r); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return
		}
		h.logger.Error("gateway_credential: persist failed",
			slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.gateway_credential.persist_failed", "failed to persist gateway credential", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, gatewayCredentialGetResponse{
		FID:       ch.DisplayNumber,
		Enabled:   false,
		RotatedAt: prev.TokenRotatedAt,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Persistence helper (shared by PUT and DELETE)
// ─────────────────────────────────────────────────────────────────────────────

// persistGatewaySettings performs the UpdateSalesChannel + audit-event write
// atomically inside a single pgx transaction. `outcome` is either "rotated"
// or "disabled" and is stashed on the audit event's Metadata so operators can
// distinguish the two without decoding the settings blob.
func (h *Handler) persistGatewaySettings(
	ctx context.Context,
	chID, orgID uuid.UUID,
	settings json.RawMessage,
	outcome, reason string,
	r *http.Request,
) error {
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.channelQueries.WithTx(tx)
	if _, err := qtx.UpdateSalesChannel(ctx,
		chID, orgID,
		"", "", "", nil, nil, nil, // no changes to name/payment/provider/… fields
		settings,
	); err != nil {
		return err
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		ev := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.channel.gateway_credential.rotated",
			ResourceType: "sales_channel",
			ResourceID:   chID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":  orgID.String(),
				"outcome": outcome, // "rotated" or "disabled"
				"reason":  reason,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, ev); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
