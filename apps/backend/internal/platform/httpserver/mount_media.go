package httpserver

import "github.com/go-chi/chi/v5"

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
	// handlers guard `s.media == nil` internally.
	if s.stub != nil && s.stub.Enabled() {
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.write", "media")
			pr.Post("/media", s.handleCreateMedia)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.read", "media")
			pr.Get("/media/{id}", s.handleGetMedia)
		})
		r.Group(func(pr chi.Router) {
			s.applyAuth(pr, "media.delete", "media")
			pr.Delete("/media/{id}", s.handleDeleteMedia)
		})
	}
	// Public download endpoint — signature is the credential.
	r.Get("/media-files/{id}", s.handleDownloadMedia)
}
