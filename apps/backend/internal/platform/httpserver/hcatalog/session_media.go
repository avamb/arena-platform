// session_media.go implements the per-session media gallery endpoints
// (AB-47b, feature #435).
//
// Data model — migration 0085:
//
//	session_media_items(id, session_id, kind, media_id, video_url, position, ...)
//
// Every row belongs to one of two kinds:
//   - kind='poster' → media_id references a media_objects row of
//     owner_type='session_poster'; video_url is NULL.
//   - kind='video'  → video_url is an https URL on a host in the allowlist
//     (YouTube / VK / RuTube / Vimeo); media_id is NULL.
//
// Ordering is by position (unique per session). The single per-session
// COVER (sessions.poster_media_id, or the event fallback) shipped in AB-47
// is untouched — this table is additive.
//
// Endpoints:
//
//	GET /v1/sessions/{id}/media — return the ordered gallery.
//	PUT /v1/sessions/{id}/media — replace the whole ordered gallery
//	                              atomically (no reorder/patch endpoints).
//
// Handler-level constants and rules:
//
//   - Poster cap: MaxPostersPerGallery (5) rows of kind='poster' per session.
//     This is a handler constant, NOT a DB CHECK — raising it does not need
//     a migration.
//   - Video allowlist: MaxTotalItems items per gallery, video hosts limited
//     to youtube.com / youtu.be / vk.com / rutube.ru / vimeo.com and their
//     www. subdomains. HTTPS required.
//   - Positions on write are ignored — the handler renumbers to 0..N-1
//     in payload order to guarantee unique positions.
//   - Replace is atomic: DELETE + N × INSERT in a single transaction.
package hcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// MaxPostersPerGallery is the maximum number of kind='poster' rows the
// handler accepts per session. Owner decision (AB-47b, 2026-08-04): five.
// This is a HANDLER constant — the migration deliberately omits the
// row-count CHECK so raising the cap later does not need a schema change.
const MaxPostersPerGallery = 5

// MaxTotalItems bounds the total number of rows in one gallery. The cap
// keeps the payload size trivial and prevents a video-only spam vector.
const MaxTotalItems = 20

// PosterOwnerType is the media_objects.owner_type the handler requires
// for kind='poster' entries. Chosen to reuse the existing allowlist entry
// added by AB-47 — no widening of the media_objects.owner_type CHECK
// (AGENTS.md documents that trap).
const PosterOwnerType = "session_poster"

// allowedVideoHosts is the host allowlist for kind='video' entries.
// Matched case-insensitively after stripping a leading "www.".
var allowedVideoHosts = map[string]bool{
	"youtube.com": true,
	"youtu.be":    true,
	"vk.com":      true,
	"rutube.ru":   true,
	"vimeo.com":   true,
}

// ─────────────────────────────────────────────────────────────────────────────
// Request/response types
// ─────────────────────────────────────────────────────────────────────────────

// SessionMediaItemRequest is one entry of a PUT payload. Exactly one of
// media_id (for kind='poster') or video_url (for kind='video') must be set.
// The optional position field is ignored — the handler renumbers.
type SessionMediaItemRequest struct {
	Kind     string  `json:"kind"`
	MediaID  *string `json:"media_id"`
	VideoURL *string `json:"video_url"`
}

// SessionMediaReplaceRequest is the PUT body: the ORDERED list of items.
// A missing "items" field or an empty list clears the gallery.
type SessionMediaReplaceRequest struct {
	Items []SessionMediaItemRequest `json:"items"`
}

// SessionMediaItemResponse is one entry of the gallery response.
type SessionMediaItemResponse struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	MediaID   *string `json:"media_id"`
	VideoURL  *string `json:"video_url"`
	Position  int     `json:"position"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// SessionMediaGalleryResponse is the top-level GET/PUT response body.
type SessionMediaGalleryResponse struct {
	SessionID string                     `json:"session_id"`
	Items     []SessionMediaItemResponse `json:"items"`
}

func sessionMediaItemFromRow(row gen.SessionMediaItemRow) SessionMediaItemResponse {
	out := SessionMediaItemResponse{
		ID:        row.ID.String(),
		Kind:      row.Kind,
		Position:  int(row.Position),
		CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if row.MediaID != nil {
		s := row.MediaID.String()
		out.MediaID = &s
	}
	if row.VideoURL != nil {
		out.VideoURL = row.VideoURL
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Video URL validation
// ─────────────────────────────────────────────────────────────────────────────

// validateVideoURL enforces the AB-47b video URL rules:
//   - Non-empty, parseable as a URL.
//   - Scheme MUST be "https" (no http, no javascript:, no data:).
//   - Host (case-insensitive, stripped of a leading "www.") MUST be in the
//     allowedVideoHosts allowlist.
//
// Returns (normalized_url, ""). If invalid, returns ("", human message).
func validateVideoURL(raw string) (string, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "video_url must not be empty"
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "video_url is not a valid URL"
	}
	if strings.ToLower(u.Scheme) != "https" {
		return "", "video_url must use https scheme"
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return "", "video_url must include a host"
	}
	// Strip port if present.
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.TrimPrefix(host, "www.")
	if !allowedVideoHosts[host] {
		return "", "video_url host not in allowlist (youtube.com, youtu.be, vk.com, rutube.ru, vimeo.com)"
	}
	return trimmed, ""
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /v1/sessions/{id}/media
// ─────────────────────────────────────────────────────────────────────────────

// HandleGetSessionMedia serves GET /v1/sessions/{id}/media. Requires JWT +
// membership in the session's owning organization (session.read scope
// enforced by the router).
func (h *Handler) HandleGetSessionMedia(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	orgCtx, err := h.sessionQueries.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("session_media: org context lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.lookup_failed", "failed to resolve session", r,
		))
		return
	}
	if !h.requireOrgMembership(w, r, h.sessionQueries, orgCtx.OrgID) {
		return
	}

	rows, err := h.sessionQueries.ListSessionMediaItems(ctx, sessionID)
	if err != nil {
		h.logger.Error("session_media: list failed",
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.list_failed", "failed to load session media gallery", r,
		))
		return
	}
	items := make([]SessionMediaItemResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, sessionMediaItemFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, SessionMediaGalleryResponse{
		SessionID: sessionID.String(),
		Items:     items,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// PUT /v1/sessions/{id}/media
// ─────────────────────────────────────────────────────────────────────────────

// HandleReplaceSessionMedia serves PUT /v1/sessions/{id}/media. Replaces the
// whole ordered gallery atomically. Requires session.update scope on the
// owning organization.
func (h *Handler) HandleReplaceSessionMedia(w http.ResponseWriter, r *http.Request) {
	if h.sessionQueries == nil || h.pool == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}
	ctx := r.Context()

	sessionID, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	orgCtx, err := h.sessionQueries.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"session.not_found", "session not found", r,
			))
			return
		}
		h.logger.Error("session_media: org context lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.lookup_failed", "failed to resolve session", r,
		))
		return
	}
	if !h.requireOrgMembership(w, r, h.sessionQueries, orgCtx.OrgID) {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"session_media.invalid_body", "cannot read request body: "+err.Error(), r,
		))
		return
	}
	if len(body) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"session_media.empty_body", "request body is required", r,
		))
		return
	}
	var req SessionMediaReplaceRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"session_media.invalid_json", "request body is not valid JSON", r,
		))
		return
	}

	// Validate and normalize.
	type normalized struct {
		kind     string
		mediaID  *uuid.UUID
		videoURL *string
	}
	items := make([]normalized, 0, len(req.Items))
	posterCount := 0
	if len(req.Items) > MaxTotalItems {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"session_media.too_many_items",
			"gallery accepts at most 20 items", r,
			map[string]any{"max": MaxTotalItems, "received": len(req.Items)},
		))
		return
	}
	for i, item := range req.Items {
		kind := strings.TrimSpace(item.Kind)
		switch kind {
		case "poster":
			if item.MediaID == nil || strings.TrimSpace(*item.MediaID) == "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.missing_media_id",
					"poster item requires media_id", r,
					map[string]any{"index": i},
				))
				return
			}
			if item.VideoURL != nil && strings.TrimSpace(*item.VideoURL) != "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.kind_payload_conflict",
					"poster item must not carry video_url", r,
					map[string]any{"index": i},
				))
				return
			}
			mediaID, parseErr := uuid.Parse(strings.TrimSpace(*item.MediaID))
			if parseErr != nil {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.invalid_media_id",
					"media_id must be a valid UUID", r,
					map[string]any{"index": i},
				))
				return
			}
			// Owner-type check — poster media MUST be owner_type='session_poster'.
			ownerType, ownerErr := h.sessionQueries.GetMediaObjectOwnerType(ctx, mediaID)
			if ownerErr != nil {
				if errors.Is(ownerErr, pgx.ErrNoRows) {
					httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
						"session_media.media_not_found",
						"media_id does not reference an existing media object", r,
						map[string]any{"index": i, "media_id": mediaID.String()},
					))
					return
				}
				h.logger.Error("session_media: media owner_type lookup failed", slog.String("error", ownerErr.Error()))
				httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
					"session_media.media_lookup_failed", "failed to load media object", r,
				))
				return
			}
			if ownerType != PosterOwnerType {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.media_owner_type_mismatch",
					"poster media must have owner_type='session_poster'", r,
					map[string]any{
						"index":         i,
						"media_id":      mediaID.String(),
						"actual_owner":  ownerType,
						"expected":      PosterOwnerType,
					},
				))
				return
			}
			posterCount++
			if posterCount > MaxPostersPerGallery {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.too_many_posters",
					"gallery accepts at most 5 posters per session", r,
					map[string]any{"max": MaxPostersPerGallery},
				))
				return
			}
			items = append(items, normalized{kind: "poster", mediaID: &mediaID})
		case "video":
			if item.VideoURL == nil || strings.TrimSpace(*item.VideoURL) == "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.missing_video_url",
					"video item requires video_url", r,
					map[string]any{"index": i},
				))
				return
			}
			if item.MediaID != nil && strings.TrimSpace(*item.MediaID) != "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.kind_payload_conflict",
					"video item must not carry media_id", r,
					map[string]any{"index": i},
				))
				return
			}
			normalizedURL, vErr := validateVideoURL(*item.VideoURL)
			if vErr != "" {
				httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
					"session_media.invalid_video_url",
					vErr, r,
					map[string]any{"index": i},
				))
				return
			}
			items = append(items, normalized{kind: "video", videoURL: &normalizedURL})
		default:
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
				"session_media.invalid_kind",
				"kind must be one of: poster, video", r,
				map[string]any{"index": i, "kind": kind},
			))
			return
		}
	}

	// Atomic replace: DELETE + INSERTs in a single transaction.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.logger.Error("session_media: begin tx failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.tx_begin_failed", "failed to begin transaction", r,
		))
		return
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	txQueries := h.sessionQueries.WithTx(tx)

	if err := txQueries.DeleteSessionMediaItems(ctx, sessionID); err != nil {
		h.logger.Error("session_media: delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.delete_failed", "failed to clear existing gallery", r,
		))
		return
	}
	inserted := make([]gen.SessionMediaItemRow, 0, len(items))
	for i, it := range items {
		row, insertErr := txQueries.InsertSessionMediaItem(ctx, sessionID, it.kind, it.mediaID, it.videoURL, int16(i))
		if insertErr != nil {
			h.logger.Error("session_media: insert failed",
				slog.String("session_id", sessionID.String()),
				slog.Int("index", i),
				slog.String("error", insertErr.Error()),
			)
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"session_media.insert_failed", "failed to insert gallery row", r,
			))
			return
		}
		inserted = append(inserted, row)
	}
	if h.audit != nil {
		actor, _ := auth.ActorFromContext(ctx)
		auditEv := audit.Event{
			OccurredAt:   time.Now().UTC(),
			ActorType:    "user",
			ActorID:      actor.ID,
			Action:       "v1.session_media.replace",
			ResourceType: "session_media",
			ResourceID:   sessionID.String(),
			RequestID:    logging.RequestID(ctx),
			TraceID:      logging.TraceID(ctx),
			IP:           httputil.ExtractClientIP(r),
			Metadata: map[string]any{
				"item_count":   len(items),
				"poster_count": posterCount,
			},
		}
		if err := h.audit.WriteTx(ctx, tx, auditEv); err != nil {
			h.logger.Error("session_media: audit write failed", slog.String("error", err.Error()))
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"session_media.audit_failed", "failed to write audit event", r,
			))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("session_media: commit failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"session_media.commit_failed", "failed to commit gallery replacement", r,
		))
		return
	}

	respItems := make([]SessionMediaItemResponse, 0, len(inserted))
	for _, row := range inserted {
		respItems = append(respItems, sessionMediaItemFromRow(row))
	}
	httputil.WriteJSON(w, http.StatusOK, SessionMediaGalleryResponse{
		SessionID: sessionID.String(),
		Items:     respItems,
	})
}
