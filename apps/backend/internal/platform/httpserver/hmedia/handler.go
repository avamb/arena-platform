package hmedia

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// Handler holds the narrow set of dependencies needed by the /v1/media
// and /v1/media-files endpoints.
type Handler struct {
	media  *mediastore.Repo
	logger *slog.Logger
	// publicBase, when set, names the API origin a host-relative signed
	// download URL (the local backend) is absolutized onto in GET /v1/media/{id}.
	publicBase func() string
}

// New constructs a Handler. media may be nil; each method guards against it
// and returns 503 so the server starts cleanly without a storage backend.
func New(media *mediastore.Repo, logger *slog.Logger) *Handler {
	return &Handler{media: media, logger: logger}
}

// WithPublicBaseURL makes GET /v1/media/{id} answer an absolute signed_url:
// the local storage backend signs a host-relative path, which a client
// handing the URL on (the Telegram bot names it as the event-bundle's
// bigPosterUrl, and the import fetches it) cannot use. base is read per
// request, so a server whose public URL is set late still answers right.
func (h *Handler) WithPublicBaseURL(base func() string) *Handler {
	h.publicBase = base
	return h
}
