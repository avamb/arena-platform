// wp_webhook.go implements the Bil24-compat WordPress webhook subscriber
// admin endpoint (feature #507, W1-B7d; spec §9.2). Dependency #506 (the
// bil24wire dispatcher) delivers the recurring events; this endpoint is how a
// WordPress site (Lampyris, Vino&Co) registers itself for them.
//
// Three routes, all gated on the `channel.update` permission and requiring
// the `X-Admin-Reason` header — the same audit-trail convention as the
// gateway-credential endpoints (gateway_credential.go, feature #473):
//
//	PUT    /v1/organizations/{org_id}/channels/{id}/wp-webhook
//	         → deactivates the previous active bil24_wp subscriber for the
//	           channel, creates a new one, SYNCHRONOUSLY sends a `test`
//	           envelope (10s timeout) and reports the outcome without ever
//	           failing the request on a delivery error.
//	GET    /v1/organizations/{org_id}/channels/{id}/wp-webhook
//	         → returns the active subscriber summary (no signing_secret).
//	DELETE /v1/organizations/{org_id}/channels/{id}/wp-webhook
//	         → deactivates the active subscriber; 404 if there was none.
//
// Scoped per SALES CHANNEL (kind='bil24_wp'), unlike the org-scoped MACS
// webhook (macs_webhook.go) — one WordPress site is one sales channel, and
// each registers its own callback (migration 0094).
package hcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/bil24wire"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// wpWebhookTestTimeout bounds the synchronous `test` delivery the PUT handler
// performs before responding (spec §9.2).
const wpWebhookTestTimeout = 10 * time.Second

// ─────────────────────────────────────────────────────────────────────────────
// Response shapes
// ─────────────────────────────────────────────────────────────────────────────

// wpWebhookSummaryResponse is the read-shape returned by GET and DELETE: never
// includes signing_secret.
type wpWebhookSummaryResponse struct {
	ChannelID   string `json:"channel_id"`
	CallbackURL string `json:"callback_url"`
	Active      bool   `json:"active"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func wpWebhookSummary(row gen.WPWebhookAdminRow) wpWebhookSummaryResponse {
	return wpWebhookSummaryResponse{
		ChannelID:   row.ChannelID.String(),
		CallbackURL: row.CallbackURL,
		Active:      row.Active,
		CreatedAt:   row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   row.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// wpWebhookTestDelivery reports the outcome of the synchronous `test` ping the
// PUT handler performs. A delivery failure is reported here, never as a PUT
// error — the registration itself succeeded regardless of what the site did
// with the ping.
type wpWebhookTestDelivery struct {
	OK         bool `json:"ok"`
	HTTPStatus int  `json:"http_status"`
}

// wpWebhookRegisteredResponse is the one-shot response returned by PUT. This
// is the ONLY moment signing_secret is echoed back; subsequent GETs never
// include it.
type wpWebhookRegisteredResponse struct {
	ChannelID     string                `json:"channel_id"`
	CallbackURL   string                `json:"callback_url"`
	SigningSecret string                `json:"signing_secret"`
	Active        bool                  `json:"active"`
	CreatedAt     string                `json:"created_at"`
	UpdatedAt     string                `json:"updated_at"`
	TestDelivery  wpWebhookTestDelivery `json:"test_delivery"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Shared guards
// ─────────────────────────────────────────────────────────────────────────────

// channelBelongsToOrg confirms the channel resolves for the given org,
// writing the appropriate error response (404/500) and returning false when
// it does not. Shared by all three verbs so channel scoping is identical to
// the gateway-credential endpoint's.
func (h *Handler) channelBelongsToOrg(ctx context.Context, w http.ResponseWriter, r *http.Request, chID, orgID uuid.UUID) bool {
	if _, err := h.channelQueries.GetSalesChannelByID(ctx, chID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"channel.not_found", "sales channel not found", r,
			))
			return false
		}
		h.logger.Error("wp-webhook: channel lookup failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"channel.get_failed", "failed to get sales channel", r,
		))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// GET
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetChannelWPWebhook handles GET .../wp-webhook.
func (h *Handler) HandleGetChannelWPWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
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
	if !h.channelBelongsToOrg(ctx, w, r, chID, orgID) {
		return
	}
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	q := gen.New(pool)
	row, err := q.GetActiveWPSubscriberByChannel(ctx, chID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"wp_webhook.not_found", "no active WordPress webhook subscriber for this channel", r,
			))
			return
		}
		h.logger.Error("wp-webhook: get subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"wp_webhook.query_failed", "failed to query WordPress webhook subscriber", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, wpWebhookSummary(row))
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT (register / re-register)
// ─────────────────────────────────────────────────────────────────────────────

// wpWebhookUpsertRequest is the request body for PUT .../wp-webhook.
type wpWebhookUpsertRequest struct {
	CallbackURL   string `json:"callback_url"`
	SigningSecret string `json:"signing_secret"`
}

// HandlePutChannelWPWebhook handles PUT .../wp-webhook: deactivates the
// channel's previous active bil24_wp subscriber, creates a new one, then
// SYNCHRONOUSLY sends a `test` envelope and reports the outcome. Delivery
// failure never fails the request — the registration already succeeded.
func (h *Handler) HandlePutChannelWPWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
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
	reason, ok := httputil.RequireAdminReason(w, r)
	if !ok {
		return
	}
	if !h.requireOrgMembership(w, r, h.channelQueries, orgID) {
		return
	}
	if !h.channelBelongsToOrg(ctx, w, r, chID, orgID) {
		return
	}

	var req wpWebhookUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"wp_webhook.invalid_body", "request body must be valid JSON", r,
		))
		return
	}
	if req.CallbackURL == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"wp_webhook.missing_callback_url", "callback_url is required", r,
		))
		return
	}
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	q := gen.New(pool)

	// Deactivate any existing active bil24_wp subscriber for this channel
	// (pgx.ErrNoRows on first registration is expected and ignored).
	_, _ = q.DeactivateWPSubscriberByChannel(ctx, chID)

	row, err := q.CreateWPWebhookSubscriber(ctx, chID, req.CallbackURL, req.SigningSecret)
	if err != nil {
		h.logger.Error("wp-webhook: create subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"wp_webhook.create_failed", "failed to create WordPress webhook subscriber", r,
		))
		return
	}

	delivery := sendWPWebhookTest(ctx, row.CallbackURL, row.SigningSecret)

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if err := h.audit.Write(ctx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.channel.wp_webhook.registered",
			ResourceType: "sales_channel",
			ResourceID:   chID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":                orgID.String(),
				"callback_url":          req.CallbackURL,
				"reason":                reason,
				"test_delivery_ok":      delivery.OK,
				"test_http_status":      delivery.HTTPStatus,
				"webhook_subscriber_id": row.ID.String(),
			},
		}); err != nil {
			h.logger.Error("wp-webhook: audit write failed", "error", err.Error())
		}
	}

	httputil.WriteJSON(w, http.StatusOK, wpWebhookRegisteredResponse{
		ChannelID:     row.ChannelID.String(),
		CallbackURL:   row.CallbackURL,
		SigningSecret: row.SigningSecret,
		Active:        row.Active,
		CreatedAt:     row.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     row.UpdatedAt.UTC().Format(time.RFC3339),
		TestDelivery:  delivery,
	})
}

// sendWPWebhookTest POSTs a `{type:"test", data:null}` envelope to callbackURL
// with a 10s timeout, signing the body when signingSecret is non-empty.
// Never returns an error: transport failures and non-2xx statuses both
// collapse into wpWebhookTestDelivery{OK:false}, since the caller must not
// fail the PUT on a delivery problem (spec §9.2).
func sendWPWebhookTest(ctx context.Context, callbackURL, signingSecret string) wpWebhookTestDelivery {
	env := bil24wire.Envelope{
		ID:      time.Now().UTC().UnixNano() & 0x7fffffffffffffff,
		Created: time.Now().UTC().Format(time.RFC3339),
		Type:    bil24wire.SiteEventTest,
		Data:    nil,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return wpWebhookTestDelivery{OK: false, HTTPStatus: 0}
	}

	reqCtx, cancel := context.WithTimeout(ctx, wpWebhookTestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return wpWebhookTestDelivery{OK: false, HTTPStatus: 0}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Arena-Event-Type", env.Type)
	if signingSecret != "" {
		req.Header.Set("X-Arena-Signature", bil24wire.Sign(body, signingSecret))
	}

	client := &http.Client{Timeout: wpWebhookTestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return wpWebhookTestDelivery{OK: false, HTTPStatus: 0}
	}
	defer resp.Body.Close() //nolint:errcheck

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	return wpWebhookTestDelivery{OK: ok, HTTPStatus: resp.StatusCode}
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE (deactivate)
// ─────────────────────────────────────────────────────────────────────────────

// HandleDeleteChannelWPWebhook handles DELETE .../wp-webhook: deactivates the
// channel's active bil24_wp subscriber. 404 when there was none.
func (h *Handler) HandleDeleteChannelWPWebhook(pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
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
	if !h.channelBelongsToOrg(ctx, w, r, chID, orgID) {
		return
	}
	if pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	q := gen.New(pool)
	row, err := q.DeactivateWPSubscriberByChannel(ctx, chID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"wp_webhook.not_found", "no active WordPress webhook subscriber for this channel", r,
			))
			return
		}
		h.logger.Error("wp-webhook: deactivate subscriber failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"wp_webhook.deactivate_failed", "failed to deactivate WordPress webhook subscriber", r,
		))
		return
	}

	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		if err := h.audit.Write(ctx, audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.channel.wp_webhook.deleted",
			ResourceType: "sales_channel",
			ResourceID:   chID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"org_id":                orgID.String(),
				"webhook_subscriber_id": row.ID.String(),
			},
		}); err != nil {
			h.logger.Error("wp-webhook: audit write failed", "error", err.Error())
		}
	}

	httputil.WriteJSON(w, http.StatusOK, wpWebhookSummary(row))
}
