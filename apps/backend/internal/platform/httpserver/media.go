// media.go implements the /v1/media endpoints introduced by feature
// #286 (Wave G, G-2):
//
//	POST   /v1/media               — multipart upload, returns media_object (media.write)
//	GET    /v1/media/{id}          — metadata + signed download URL       (media.read)
//	DELETE /v1/media/{id}          — soft-delete                          (media.delete)
//	GET    /v1/media-files/{id}    — signed local-backend download stream (no auth — signature IS the credential)
//
// Bytes live in the storage adapter configured by MEDIA_BACKEND. The
// metadata row lives in media_objects (migration 0052). Soft-deleted
// rows are reaped by the media-gc worker handler.
package httpserver

import (
	"encoding/json"
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
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// mediaUploadMaxBytes caps the size of a single multipart upload. 32 MiB
// is comfortably larger than every current owner_type (org_logo,
// event_poster, artist_photo) and leaves room for future PDFs/manifests
// without revisiting the limit on the first new owner kind.
const mediaUploadMaxBytes = 32 * 1024 * 1024

// signedURLTTL is the lifetime of GET /v1/media/{id} signed URLs. Seven
// minutes is long enough for browsers to chase redirects and warm CDN
// caches without being so generous that a leaked link is durable.
const signedURLTTL = 7 * time.Minute

// mediaObjectResponse is the JSON shape returned by POST and GET. The
// signed_url field is non-empty on GET responses (the consumer needs a
// fetchable URL) and is omitted ("") on POST responses (the caller
// already has the bytes they just uploaded).
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

// =====================================================================
// POST /v1/media
// =====================================================================

func (s *Server) handleCreateMedia(w http.ResponseWriter, r *http.Request) {
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"media.storage_unavailable",
			"media storage backend is not configured",
			r,
		))
		return
	}
	ctx := r.Context()

	// Cap the multipart parse so an oversized upload cannot exhaust
	// process memory or temp-file space.
	r.Body = http.MaxBytesReader(w, r.Body, mediaUploadMaxBytes)
	if err := r.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"media.invalid_multipart",
			"cannot parse multipart form: "+err.Error(),
			r,
		))
		return
	}

	ownerType := strings.TrimSpace(r.FormValue("owner_type"))
	if _, ok := mediastore.AllowedOwnerTypes[ownerType]; !ok {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
			writeJSON(w, http.StatusBadRequest, errorEnvelope(
				"media.invalid_org_id", "org_id must be a valid UUID", r,
			))
			return
		}
		orgID = &id
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
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
	// Strip any boundary or charset parameter; we store the bare media type.
	if mt, _, perr := mime.ParseMediaType(contentType); perr == nil {
		contentType = mt
	}

	key, err := mediastore.NewStorageKey(ownerType)
	if err != nil {
		s.logger.Error("media: key generation failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.key_failed", "failed to generate storage key", r,
		))
		return
	}

	checksum, size, err := s.media.PutAndStream(ctx, key, contentType, file)
	if err != nil {
		s.logger.Error("media: storage put failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.put_failed", "failed to write object bytes to storage backend", r,
		))
		return
	}
	if size <= 0 {
		// Roll back the empty object to avoid littering the bucket.
		_ = s.media.Storage().Delete(ctx, key)
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"media.empty_file", "uploaded file is empty", r,
		))
		return
	}

	obj, err := s.media.Insert(ctx, mediastore.InsertInput{
		OrgID:          orgID,
		OwnerType:      ownerType,
		OwnerID:        ownerID,
		StorageBackend: s.media.Backend(),
		StorageKey:     key,
		ContentType:    contentType,
		ByteSize:       size,
		ChecksumSHA256: checksum,
	})
	if err != nil {
		// Compensating delete keeps the bucket and DB consistent if the
		// metadata insert failed after the bytes landed.
		_ = s.media.Storage().Delete(ctx, key)
		s.logger.Error("media: insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.insert_failed", "failed to record media object metadata", r,
		))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"media_object": mediaObjectFromRow(obj),
	})
}

// =====================================================================
// GET /v1/media/{id}
// =====================================================================

func (s *Server) handleGetMedia(w http.ResponseWriter, r *http.Request) {
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"media.storage_unavailable",
			"media storage backend is not configured",
			r,
		))
		return
	}
	ctx := r.Context()

	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}
	obj, err := s.media.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		s.logger.Error("media: get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.get_failed", "failed to fetch media object", r,
		))
		return
	}

	signedURL, err := s.media.SignedURL(id, obj.StorageKey, signedURLTTL)
	if err != nil {
		s.logger.Error("media: signing failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.signing_failed", "failed to build signed download URL", r,
		))
		return
	}

	resp := mediaObjectFromRow(obj)
	resp.SignedURL = signedURL
	resp.SignedURLTTLs = int64(signedURLTTL.Seconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"media_object": resp,
	})
}

// =====================================================================
// DELETE /v1/media/{id}
// =====================================================================

func (s *Server) handleDeleteMedia(w http.ResponseWriter, r *http.Request) {
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"media.storage_unavailable",
			"media storage backend is not configured",
			r,
		))
		return
	}
	ctx := r.Context()

	id, ok := uuidPathParam(w, r, "id")
	if !ok {
		return
	}

	if err := s.media.SoftDelete(ctx, id); err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		s.logger.Error("media: soft-delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.delete_failed", "failed to soft-delete media object", r,
		))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// GET /v1/media-files/{id}  (no auth — signature IS the credential)
// =====================================================================

func (s *Server) handleDownloadMedia(w http.ResponseWriter, r *http.Request) {
	if s.media == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorEnvelope(
			"media.storage_unavailable",
			"media storage backend is not configured",
			r,
		))
		return
	}
	ctx := r.Context()

	idStr := strings.TrimSpace(chi.URLParam(r, "id"))
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"media.invalid_id", "media id must be a valid UUID", r,
		))
		return
	}

	expires := strings.TrimSpace(r.URL.Query().Get("expires"))
	sig := strings.TrimSpace(r.URL.Query().Get("sig"))
	if expires == "" {
		writeJSON(w, http.StatusBadRequest, errorEnvelope(
			"media.invalid_signature", "expires query parameter is required", r,
		))
		return
	}
	if err := s.media.VerifyLocalSignature(id, expires, sig); err != nil {
		writeJSON(w, http.StatusUnauthorized, errorEnvelope(
			"media.invalid_signature", err.Error(), r,
		))
		return
	}

	obj, err := s.media.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"media.not_found", "media object not found", r,
			))
			return
		}
		s.logger.Error("media: download lookup failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
			"media.get_failed", "failed to fetch media object", r,
		))
		return
	}

	res, err := s.media.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorEnvelope(
				"media.bytes_missing", "media bytes are no longer available", r,
			))
			return
		}
		s.logger.Error("media: storage get failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorEnvelope(
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

// jsonReply is a convenience for tests that may want to construct the
// response shape without going through the live handler.
func mediaJSONReply(obj mediastore.Object) []byte {
	b, _ := json.Marshal(map[string]any{"media_object": mediaObjectFromRow(obj)})
	return b
}
