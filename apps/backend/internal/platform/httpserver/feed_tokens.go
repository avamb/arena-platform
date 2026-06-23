// feed_tokens.go implements the agent feed token management API (feature #122).
//
// Agent feed tokens are public read-only credentials (ADR-013: federated feeds)
// that allow external agents (widgets, embeds, scanner apps) to consume a sales
// channel's event feed without possessing a full JWT session. The token value is
// a cryptographically random 32-byte hex string that is safe to embed in client HTML.
//
// Management endpoints (require JWT auth + named permission):
//
//   POST   /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens        — issue token (feed_token.create)
//   GET    /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens        — list tokens (feed_token.read)
//   GET    /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}   — get single (feed_token.read)
//   DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}   — revoke token (feed_token.delete)
//
// Public feed read endpoint (no auth, token in path):
//
//   GET /v1/feeds/{token} — public feed; validates token, updates last_used_at, returns channel info
//
// Token revocation (Step 2):
//   DELETE sets is_active=false + revoked_at=now(). The row is kept for audit.
//   Subsequent public feed reads for a revoked token return 404.
//
// Last-used-at update (Step 3):
//   TouchFeedTokenLastUsed is called best-effort after a successful public feed
//   read. Errors in the update do not block the response.
package httpserver

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

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ─────────────────────────────────────────────────────────────────────────────
// Response types
// ─────────────────────────────────────────────────────────────────────────────

// feedTokenResponse is the JSON representation of a single agent feed token.
// The token value is included in full so the issuing caller can record it.
// (Subsequent reads via GET will also include it — it is a public credential.)
type feedTokenResponse struct {
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

// feedTokenFromRow converts a gen.FeedTokenRow to a feedTokenResponse.
func feedTokenFromRow(ft gen.FeedTokenRow) feedTokenResponse {
	r := feedTokenResponse{
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

// generateFeedToken returns a cryptographically random 32-byte hex string
// suitable for use as a public feed token credential.
func generateFeedToken() (string, error) {
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

// handleCreateFeedToken serves POST /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
// Requires JWT + "feed_token.create" permission.
func (s *Server) handleCreateFeedToken(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil || s.pool == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := uuidPathParam(w, r, "channel_id")
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("feed_token.invalid_body", "cannot read request body: "+err.Error(), r))
		return
	}

	var req createFeedTokenRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, errorEnvelope("feed_token.invalid_json", "request body is not valid JSON", r))
			return
		}
	}
	req.Label = strings.TrimSpace(req.Label)

	// Generate cryptographically random token.
	tokenValue, err := generateFeedToken()
	if err != nil {
		s.logger.Error("feed_token: generate token failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.generate_failed", "failed to generate token", r,
		))
		return
	}

	ft, err := s.feedTokenQueries.InsertFeedToken(ctx, tokenValue, channelID, req.Label)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			writeJSON(w, http.StatusConflict, errorEnvelope(
				"feed_token.duplicate", "a feed token with that value already exists", r,
			))
			return
		}
		s.logger.Error("feed_token: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.insert_failed", "failed to create feed token", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"feed_token": feedTokenFromRow(ft),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens
// ─────────────────────────────────────────────────────────────────────────────

// handleListFeedTokens serves GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens.
// Requires JWT + "feed_token.read" permission.
func (s *Server) handleListFeedTokens(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := uuidPathParam(w, r, "channel_id")
	if !ok {
		return
	}

	rows, err := s.feedTokenQueries.ListFeedTokensByChannel(ctx, channelID)
	if err != nil {
		s.logger.Error("feed_token: list failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.list_failed", "failed to list feed tokens", r,
		))
		return
	}

	result := make([]feedTokenResponse, 0, len(rows))
	for _, ft := range rows {
		result = append(result, feedTokenFromRow(ft))
	}
	writeJSON(w, http.StatusOK, map[string]any{"feed_tokens": result})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleGetFeedToken serves GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}.
// Requires JWT + "feed_token.read" permission.
func (s *Server) handleGetFeedToken(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	_, ok := uuidPathParam(w, r, "org_id")
	if !ok {
		return
	}
	channelID, ok := uuidPathParam(w, r, "channel_id")
	if !ok {
		return
	}
	tokenID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	ft, err := s.feedTokenQueries.GetFeedTokenByID(ctx, tokenID, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("feed_token.not_found", "feed token not found", r))
			return
		}
		s.logger.Error("feed_token: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.get_failed", "failed to get feed token", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"feed_token": feedTokenFromRow(ft),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}
// ─────────────────────────────────────────────────────────────────────────────

// handleRevokeFeedToken serves DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}.
// Sets is_active=false + revoked_at=now(). Writes an audit event. The row is
// retained for audit purposes. Requires JWT + "feed_token.delete" permission.
func (s *Server) handleRevokeFeedToken(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil || s.pool == nil {
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
	channelID, ok := uuidPathParam(w, r, "channel_id")
	if !ok {
		return
	}
	tokenID, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	// Open transaction: revoke + audit in one atomic write.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "failed to begin transaction", r,
		))
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := s.feedTokenQueries.WithTx(tx)

	revoked, err := qtx.RevokeFeedToken(ctx, tokenID, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("feed_token.not_found", "feed token not found", r))
			return
		}
		s.logger.Error("feed_token: revoke failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.revoke_failed", "failed to revoke feed token", r,
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
			Action:       "v1.feed_token.revoke",
			ResourceType: "agent_feed_token",
			ResourceID:   tokenID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           extractClientIP(r),
			Metadata: map[string]any{
				"channel_id": channelID.String(),
				"org_id":     orgID.String(),
				"label":      revoked.Label,
			},
		}
		if err := s.audit.WriteTx(ctx, tx, auditEv); err != nil {
			s.logger.Error("feed_token: audit write failed", slog.String("error", err.Error()))
			writeJSON(w, http.StatusInternalServerError, errorEnvelope(
				"feed_token.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.commit_failed", "failed to commit transaction", r,
		))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"feed_token": feedTokenFromRow(revoked),
		"revoked":    true,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/feeds/{token} — public feed endpoint (no JWT required)
// ─────────────────────────────────────────────────────────────────────────────

// handlePublicFeed serves GET /v1/feeds/{token}.
// This is the public read-only feed endpoint. No JWT is required — the token
// in the path is the credential. Rejected if the token is unknown or revoked.
//
// Step 3: TouchFeedTokenLastUsed is called best-effort after a successful
// token validation. Errors in the touch do not block the response.
func (s *Server) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	if s.feedTokenQueries == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	tokenValue := chi.URLParam(r, "token")
	if tokenValue == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope("feed_token.missing_token", "token is required in path", r))
		return
	}

	ft, err := s.feedTokenQueries.GetFeedTokenByToken(ctx, tokenValue)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, errorEnvelope("feed_token.not_found", "feed token not found or revoked", r))
			return
		}
		s.logger.Error("feed_token: public feed lookup failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"feed_token.lookup_failed", "failed to look up feed token", r,
		))
		return
	}

	// Reject revoked tokens.
	if !ft.IsActive {
		writeJSON(w, http.StatusNotFound, errorEnvelope("feed_token.not_found", "feed token not found or revoked", r))
		return
	}

	// Step 3: Touch last_used_at best-effort (run in background; do not block on error).
	go func() {
		if err := s.feedTokenQueries.TouchFeedTokenLastUsed(ctx, tokenValue); err != nil {
			s.logger.Warn("feed_token: touch last_used_at failed", slog.String("error", err.Error()))
		}
	}()

	// Return the token details for the feed consumer to use.
	writeJSON(w, http.StatusOK, map[string]any{
		"feed_token":       feedTokenFromRow(ft),
		"sales_channel_id": ft.SalesChannelID.String(),
	})
}
