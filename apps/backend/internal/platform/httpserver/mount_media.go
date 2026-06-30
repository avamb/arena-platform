package httpserver

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmedia"
)

// mountMediaRoutes wires the /v1/media surface from feature #286 (G-2).
//
//	POST   /v1/media               media.write   — multipart upload
//	GET    /v1/media/{id}          media.read    — metadata + signed URL
//	DELETE /v1/media/{id}          media.delete  — soft-delete
//	GET    /v1/media-files/{id}    no auth       — signature-gated download
//
// The download endpoint is intentionally outside the permission-gated
// group: it verifies an HMAC signature embedded in the URL query string,
// so anyone bearing a valid signed URL may stream the bytes. This mirrors
// the S3 presigned-URL contract a downstream consumer would otherwise
// expect when MEDIA_BACKEND=s3.
func (s *Server) mountMediaRoutes(r chi.Router) {
	// Routes are mounted regardless of whether the storage backend is
	// configured so the OpenAPI <-> code drift check stays accurate and
	// so a deliberate misconfiguration surfaces as a structured
	// `media.storage_unavailable` 503 response instead of a 404. The
	// handlers guard media == nil internally.
	h := hmedia.New(s.media, s.logger)
	if s.stub != nil && s.stub.Enabled() {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.write", "media")
			pr.Post("/media", h.CreateMedia)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.read", "media")
			pr.Get("/media/{id}", h.GetMedia)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.delete", "media")
			pr.Delete("/media/{id}", h.DeleteMedia)
		})
	}
	// Public download endpoint — signature is the credential.
	r.Get("/media-files/{id}", h.DownloadMedia)
}
