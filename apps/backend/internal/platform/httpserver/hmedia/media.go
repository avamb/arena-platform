// media.go implements the /v1/media endpoints (feature #286, G-2):
//
//	POST   /v1/media               — multipart upload, returns media_object (media.write)
//	GET    /v1/media/{id}          — metadata + signed download URL          (media.read)
//	DELETE /v1/media/{id}          — soft-delete                             (media.delete)
//	GET    /v1/media-files/{id}    — signature-gated download stream         (no auth)
package hmedia

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

const (
	mediaUploadMaxBytes = 32 * 1024 * 1024
	signedURLTTL        = 7 * time.Minute
)

type mediaObjectResponse struct {
	ID             string  `json:"id"`
	OrgID          *string `json:"org_id"`
	OwnerType      string  `json:"owner_type"`
	OwnerID        *string `json:"owner_id"`
	StorageBackend string  `json:"storage_backend"`
	StorageKey     string  `json:"storage_key"`
	ContentType    string  `json:"content_type"`
	ByteSize       int64   `json:"byte_size"`
	ChecksumSHA256 string  `json:"checksum_sha256"`
	Width          *int32  `json:"width"`
	Height         *int32  `json:"height"`
	CreatedAt      string  `json:"created_at"`
	SignedURL      string  `json:"signed_url,omitempty"`
	SignedURLTTLs  int64   `json:"signed_url_ttl_seconds,omitempty"`
}

func mediaObjectFromRow(obj mediastore.Object) mediaObjectResponse {
	var orgID, ownerID *string
	if obj.OrgID != nil {
		s := obj.OrgID.String()
		orgID = &s
	}
	if obj.OwnerID != nil {
		s := obj.OwnerID.String()
		ownerID = &s
	}
	return mediaObjectResponse{
		ID:             obj.ID.String(),
		OrgID:          orgID,
		OwnerType:      obj.OwnerType,
		OwnerID:        ownerID,
		StorageBackend: obj.StorageBackend,
		StorageKey:     obj.StorageKey,
		ContentType:    obj.ContentType,
		ByteSize:       obj.ByteSize,
		ChecksumSHA256: obj.ChecksumSHA256,
		Width:          obj.Width,
		Height:         obj.Height,
		CreatedAt:      obj.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// CreateMedia serves POST /v1/media.
func (h *Handler) CreateMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.storage_unavailable", "media storage backend is not configured", r,
		))
		return
	}
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, mediaUploadMaxBytes)
	//nolint:gosec // G120 false positive: the body is capped by MaxBytesReader above.
	if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.invalid_multipart", "cannot parse multipart form: "+err.Error(), r,
		))
		return
	}

	ownerType := strings.TrimSpace(r.FormValue("owner_type"))
	if _, ok := mediastore.AllowedOwnerTypes[ownerType]; !ok {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.invalid_owner_type",
			fmt.Sprintf("owner_type must be one of org_logo, event_poster, artist_photo; got %q", ownerType),
			r,
		))
		return
	}

	var ownerID *uuid.UUID
	if raw := strings.TrimSpace(r.FormValue("owner_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"media.invalid_owner_id", "owner_id must be a valid UUID", r,
			))
			return
		}
		ownerID = &id
	}

	var orgID *uuid.UUID
	if raw := strings.TrimSpace(r.FormValue("org_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"media.invalid_org_id", "org_id must be a valid UUID", r,
			))
			return
		}
		orgID = &id
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.missing_file", "request must include a multipart 'file' part", r,
		))
		return
	}
	defer func() { _ = file.Close() }()

	contentType := strings.TrimSpace(r.FormValue("content_type"))
	if contentType == "" {
		contentType = header.Header.Get("Content-Type")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if mt, _, perr := mime.ParseMediaType(contentType); perr == nil {
		contentType = mt
	}

	key, err := mediastore.NewStorageKey(ownerType)
	if err != nil {
		h.logger.Error("media: key generation failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.key_failed", "failed to generate storage key", r,
		))
		return
	}

	checksum, size, err := h.media.PutAndStream(ctx, key, contentType, file)
	if err != nil {
		h.logger.Error("media: storage put failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.put_failed", "failed to write object bytes to storage backend", r,
		))
		return
	}
	if size <= 0 {
		_ = h.media.Storage().Delete(ctx, key)
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.empty_file", "uploaded file is empty", r,
		))
		return
	}

	obj, err := h.media.Insert(ctx, mediastore.InsertInput{
		OrgID:          orgID,
		OwnerType:      ownerType,
		OwnerID:        ownerID,
		StorageBackend: h.media.Backend(),
		StorageKey:     key,
		ContentType:    contentType,
		ByteSize:       size,
		ChecksumSHA256: checksum,
	})
	if err != nil {
		_ = h.media.Storage().Delete(ctx, key)
		h.logger.Error("media: insert failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.insert_failed", "failed to record media object metadata", r,
		))
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"media_object": mediaObjectFromRow(obj),
	})
}

// GetMedia serves GET /v1/media/{id}.
func (h *Handler) GetMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.storage_unavailable", "media storage backend is not configured", r,
		))
		return
	}
	ctx := r.Context()

	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	obj, err := h.media.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		h.logger.Error("media: get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to fetch media object", r,
		))
		return
	}

	signedURL, err := h.media.SignedURL(id, obj.StorageKey, signedURLTTL)
	if err != nil {
		h.logger.Error("media: signing failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.signing_failed", "failed to build signed download URL", r,
		))
		return
	}

	resp := mediaObjectFromRow(obj)
	resp.SignedURL = signedURL
	resp.SignedURLTTLs = int64(signedURLTTL.Seconds())
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"media_object": resp,
	})
}

// DeleteMedia serves DELETE /v1/media/{id}.
func (h *Handler) DeleteMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.storage_unavailable", "media storage backend is not configured", r,
		))
		return
	}
	ctx := r.Context()

	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.media.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		h.logger.Error("media: soft-delete failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.delete_failed", "failed to soft-delete media object", r,
		))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DownloadMedia serves GET /v1/media-files/{id} (no auth — signature is the credential).
func (h *Handler) DownloadMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"media.storage_unavailable", "media storage backend is not configured", r,
		))
		return
	}
	ctx := r.Context()

	idStr := strings.TrimSpace(chi.URLParam(r, "id"))
	id, err := uuid.Parse(idStr)
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.invalid_id", "media id must be a valid UUID", r,
		))
		return
	}

	expires := strings.TrimSpace(r.URL.Query().Get("expires"))
	sig := strings.TrimSpace(r.URL.Query().Get("sig"))
	if expires == "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"media.invalid_signature", "expires query parameter is required", r,
		))
		return
	}
	if err := h.media.VerifyLocalSignature(id, expires, sig); err != nil {
		httputil.WriteJSON(w, http.StatusUnauthorized, httputil.ErrorEnvelope(
			"media.invalid_signature", err.Error(), r,
		))
		return
	}

	obj, err := h.media.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		h.logger.Error("media: download lookup failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to fetch media object", r,
		))
		return
	}

	res, err := h.media.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
				"media.bytes_missing", "media bytes are no longer available", r,
			))
			return
		}
		h.logger.Error("media: storage get failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"media.get_failed", "failed to read media object bytes", r,
		))
		return
	}
	defer func() { _ = res.Body.Close() }()

	if obj.ContentType != "" {
		w.Header().Set("Content-Type", obj.ContentType)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(obj.ByteSize, 10))
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, res.Body)
}
