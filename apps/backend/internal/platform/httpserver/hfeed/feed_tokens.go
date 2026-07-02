// feed_tokens.go implements the agent feed token management API (feature #122).
//
// Agent feed tokens are public read-only credentials (ADR-013: federated feeds)
// that allow external agents (widgets, embeds, scanner apps) to consume a sales
// channel's event feed without possessing a full JWT session. The token value is
// a cryptographically random 32-byte hex string that is safe to embed in client HTML.
//
// Management endpoints (require JWT auth + named permission):
//
//	POST   /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens        — issue token (feed_token.create)
//	GET    /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens        — list tokens (feed_token.read)
//	GET    /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}   — get single (feed_token.read)
//	DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}   — revoke token (feed_token.delete)
//
// Public feed read endpoint (no auth, token in path):
//
//	GET /v1/feeds/{token} — public feed; validates token, updates last_used_at, returns channel info
//
// Token revocation (Step 2):
//
//	DELETE sets is_active=false + revoked_at=now(). The row is kept for audit.
//	Subsequent public feed reads for a revoked token return 404.
//
// Last-used-at update (Step 3):
//
//	TouchFeedTokenLastUsed is called best-effort after a successful public feed
//	read. Errors in the update do not block the response.
package hfeed

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// FeedTokenResponse is the JSON representation of a single agent feed token.
// The token value is included in full so the issuing caller can record it.
// (Subsequent reads via GET will also include it — it is a public credential.)
type FeedTokenResponse struct {
	ID             string  `json:"id"`
	Token          string  `json:"token"`
	SalesChannelID string  `json:"sales_channel_id"`
	Label          string  `json:"label"`
	IsActive       bool    `json:"is_active"`
	RevokedAt      *string `json:"revoked_at"`
	LastUsedAt     *string `json:"last_used_at"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// FeedTokenFromRow converts a gen.FeedTokenRow to a FeedTokenResponse.
func FeedTokenFromRow(ft gen.FeedTokenRow) FeedTokenResponse {
	r := FeedTokenResponse{
		ID:             ft.ID.String(),
		Token:          ft.Token,
		SalesChannelID: ft.SalesChannelID.String(),
		Label:          ft.Label,
		IsActive:       ft.IsActive,
		CreatedAt:      ft.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      ft.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if ft.RevokedAt != nil {
		s := ft.RevokedAt.UTC().Format(time.RFC3339)
		r.RevokedAt = &s
	}
	if ft.LastUsedAt != nil {
		s := ft.LastUsedAt.UTC().Format(time.RFC3339)
		r.LastUsedAt = &s
	}
	return r
}

// GenerateFeedToken returns a cryptographically random 32-byte hex string
// suitable for use as a public feed token credential.
func GenerateFeedToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens
// ─────────────────────────────────────────────────────────────────────────────

// createFeedTokenRequest is the request body for the issue-token endpoint.
type createFeedTokenRequest struct {
	Label string `json:"label"`
}

// HandleCreateFeedToken serves POST /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
// Requires JWT + "feed_token.create" permission.
func (h *Handler) HandleCreateFeedToken(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := httputil.UUIDPathParam(w, r, "channel_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("feed_token.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}

	var req createFeedTokenRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("feed_token.invalid_json", "request body is not valid JSON", r))
			return
		}
	}
	req.Label = strings.TrimSpace(req.Label)

	// Generate cryptographically random token.
	tokenValue, err := GenerateFeedToken()
	if err != nil {
		h.logger.Error("feed_token: generate token failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.generate_failed", "failed to generate token", r,
		))
		return
	}

	ft, err := h.feedTokenQueries.InsertFeedToken(ctx, tokenValue, channelID, req.Label)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			httputil.WriteJSON(w, http.StatusConflict, httputil.ErrorEnvelope(
				"feed_token.duplicate", "a feed token with that value already exists", r,
			))
			return
		}
		h.logger.Error("feed_token: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.insert_failed", "failed to create feed token", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"feed_token": FeedTokenFromRow(ft),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens
// ─────────────────────────────────────────────────────────────────────────────

// HandleListFeedTokens serves GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
// Requires JWT + "feed_token.read" permission.
func (h *Handler) HandleListFeedTokens(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := httputil.UUIDPathParam(w, r, "channel_id")
	if !ok {
		return
	}

	rows, err := h.feedTokenQueries.ListFeedTokensByChannel(ctx, channelID)
	if err != nil {
		h.logger.Error("feed_token: list failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.list_failed", "failed to list feed tokens", r,
		))
		return
	}

	result := make([]FeedTokenResponse, 0, len(rows))
	for _, ft := range rows {
		result = append(result, FeedTokenFromRow(ft))
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"feed_tokens": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetFeedToken serves GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}.
// Requires JWT + "feed_token.read" permission.
func (h *Handler) HandleGetFeedToken(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := httputil.UUIDPathParam(w, r, "channel_id")
	if !ok {
		return
	}
	tokenID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	ft, err := h.feedTokenQueries.GetFeedTokenByID(ctx, tokenID, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("feed_token.not_found", "feed token not found", r))
			return
		}
		h.logger.Error("feed_token: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.get_failed", "failed to get feed token", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"feed_token": FeedTokenFromRow(ft),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}
// ─────────────────────────────────────────────────────────────────────────────

// HandleRevokeFeedToken serves DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}.
// Sets is_active=false + revoked_at=now(). Writes an audit event. The row is
// retained for audit purposes. Requires JWT + "feed_token.delete" permission.
func (h *Handler) HandleRevokeFeedToken(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil || h.pool == nil {
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
	channelID, ok := httputil.UUIDPathParam(w, r, "channel_id")
	if !ok {
		return
	}
	tokenID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: revoke + audit in one atomic write.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.feedTokenQueries.WithTx(tx)

	revoked, err := qtx.RevokeFeedToken(ctx, tokenID, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("feed_token.not_found", "feed token not found", r))
			return
		}
		h.logger.Error("feed_token: revoke failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.revoke_failed", "failed to revoke feed token", r,
		))
		return
	}

	// Write audit event inside the same transaction.
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.feed_token.revoke",
			ResourceType: "agent_feed_token",
			ResourceID:   tokenID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"channel_id": channelID.String(),
				"org_id":     orgID.String(),
				"label":      revoked.Label,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("feed_token: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"feed_token.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"feed_token": FeedTokenFromRow(revoked),
		"revoked":    true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/feeds/{token} — public feed endpoint (no JWT required)
// ─────────────────────────────────────────────────────────────────────────────

// HandlePublicFeed serves GET /v1/feeds/{token}.
// This is the public read-only feed endpoint. No JWT is required — the token
// in the path is the credential. Rejected if the token is unknown or revoked.
//
// Step 3: TouchFeedTokenLastUsed is called best-effort after a successful
// token validation. Errors in the touch do not block the response.
func (h *Handler) HandlePublicFeed(w http.ResponseWriter, r *http.Request) {
	if h.feedTokenQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	tokenValue := chi.URLParam(r, "token")
	if tokenValue == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("feed_token.missing_token", "token is required in path", r))
		return
	}

	ft, err := h.feedTokenQueries.GetFeedTokenByToken(ctx, tokenValue)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("feed_token.not_found", "feed token not found or revoked", r))
			return
		}
		h.logger.Error("feed_token: public feed lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"feed_token.lookup_failed", "failed to look up feed token", r,
		))
		return
	}

	// Reject revoked tokens.
	if !ft.IsActive {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope("feed_token.not_found", "feed token not found or revoked", r))
		return
	}

	// Step 3: Touch last_used_at best-effort (run in background; do not block on error).
	go func() {
		if err := h.feedTokenQueries.TouchFeedTokenLastUsed(ctx, tokenValue); err != nil {
			h.logger.Warn("feed_token: touch last_used_at failed", slog.String("error", err.Error()))
		}
	}()

	// Return the token details for the feed consumer to use.
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"feed_token":       FeedTokenFromRow(ft),
		"sales_channel_id": ft.SalesChannelID.String(),
	})
}
