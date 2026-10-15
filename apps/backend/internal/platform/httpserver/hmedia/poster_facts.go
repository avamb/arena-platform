package hmedia

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

const (
	// posterReadTimeout bounds one model call, the same 45 seconds the
	// Telegram bot always allowed it; the wizards show "reading…" meanwhile.
	posterReadTimeout = 45 * time.Second
	// posterWriteSlack is the time after the model deadline left to write the answer.
	posterWriteSlack = 10 * time.Second
	// posterMaxBytes is the model API's own per-image ceiling.
	posterMaxBytes = 5 << 20
	// PosterReadsWindow is the sliding window of the per-organization cap.
	PosterReadsWindow = 24 * time.Hour
)

// posterMediaStore is the part of mediastore.Repo the route reads, narrow so
// the handler is testable without a database.
type posterMediaStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (mediastore.Object, error)
	Storage() storage.Storage
}

// WithPosterReader wires the vision model behind
// POST /v1/organizations/{org_id}/media/{media_id}/poster-facts. A nil reader
// leaves the route answering 503 media.poster_reader_not_configured; a nil
// limiter switches the per-organization daily cap off. The receiver is
// returned for chaining.
func (h *Handler) WithPosterReader(reader posterread.Reader, limiter ratelimit.Limiter) *Handler {
	h.poster = reader
	h.posterLimiter = limiter
	return h
}

// withPosterStore replaces the media lookup (tests).
func (h *Handler) withPosterStore(s posterMediaStore) *Handler {
	h.posterStore = s
	return h
}

// posterFactsResponse is the body of a successful read.
type posterFactsResponse struct {
	Facts posterread.Facts `json:"facts"`
}

// PosterFacts serves POST /v1/organizations/{org_id}/media/{media_id}/poster-facts:
// it reads the poster stored as a media object of THIS organization with the
// vision model and answers what is printed on it. The values are hints for a
// wizard to offer as buttons; nothing is stored and the answer is not cached.
//
// Order of the checks matters: nothing that costs money (the cap slot, the
// bytes, the model) is touched before the caller, the organization and the
// image have been proven, and a refused request never takes a slot.
func (h *Handler) PosterFacts(w http.ResponseWriter, r *http.Request) {
	if h.poster == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.poster_reader_not_configured", "reading posters is not configured on this server", r,
		))
		return
	}
	if h.posterStore == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.storage_unavailable", "media storage backend is not configured", r,
		))
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	mediaID, ok := httputil.UUIDPathParam(w, r, "media_id")
	if !ok {
		return
	}
	// The caller must act for the PATH organization (membership, the API
	// key's own organization, a superadmin with a reason). A read costs the
	// organization a slot of its daily cap, so it counts as a write.
	if h.rowOrgAccess == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.org_access_unavailable", "organization access guard is not configured", r,
		))
		return
	}
	if !h.rowOrgAccess(w, r, orgID, true, "media.not_found", "media object not found") {
		return
	}

	ctx := r.Context()
	obj, err := h.posterStore.GetByID(ctx, mediaID)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			writeMediaNotFound(w, r)
			return
		}
		h.logger.Error("media: poster facts lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to fetch media object", r,
		))
		return
	}
	// A foreign object, or one that belongs to nobody, reads as missing.
	if obj.OrgID == nil || *obj.OrgID != orgID {
		writeMediaNotFound(w, r)
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(obj.ContentType))
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
			"media.poster_unsupported_type", "only a JPEG or PNG image can be read", r,
		))
		return
	}
	if obj.ByteSize > posterMaxBytes {
		writePosterTooLarge(w, r)
		return
	}

	// Only a request that passed every check above takes a slot of the cap.
	if h.posterLimiter != nil && !h.posterLimiter.Allow(orgID.String()) {
		httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelope(
			"media.poster_reads_exhausted", "this organization has used its poster readings for today", r,
		))
		return
	}

	res, err := h.posterStore.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"media.bytes_missing", "media bytes are no longer available", r,
			))
			return
		}
		h.logger.Error("media: poster bytes read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to read media object bytes", r,
		))
		return
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, posterMaxBytes+1))
	_ = res.Body.Close()
	if err != nil {
		h.logger.Error("media: poster bytes read failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to read media object bytes", r,
		))
		return
	}
	if len(data) > posterMaxBytes {
		writePosterTooLarge(w, r)
		return
	}

	// The router's request timeout (30 s) and the server's write deadline are
	// shorter than a slow model call, so the read runs on its own deadline and
	// the connection's write deadline moves past it, as the refund routes do.
	// A caller that hangs up does not stop a read that was already paid for.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(posterReadTimeout + posterWriteSlack))
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), posterReadTimeout)
	defer cancel()
	facts, err := h.poster.Read(readCtx, data, mediaType)
	if err != nil {
		if errors.Is(err, posterread.ErrNotConfigured) {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"media.poster_reader_not_configured", "reading posters is not configured on this server", r,
			))
			return
		}
		// The error text may quote the model's answer: log it, never return it.
		h.logger.Warn("media: poster reading failed", slog.String("error", err.Error()))
		// 503, never 502: Cloudflare replaces an origin 502 with its own page
		// and the error code would not reach the wizard.
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.poster_read_failed", "the poster could not be read right now", r,
		))
		return
	}
	if facts.Categories == nil {
		facts.Categories = []posterread.Category{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, http.StatusOK, posterFactsResponse{Facts: facts})
}

func writePosterTooLarge(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
		"media.poster_too_large", "the image is larger than 5 MiB, the most that can be read", r,
	))
}
