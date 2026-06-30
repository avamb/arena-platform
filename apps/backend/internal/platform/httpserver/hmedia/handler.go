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
}

// New constructs a Handler. media may be nil; each method guards against it
// and returns 503 so the server starts cleanly without a storage backend.
func New(media *mediastore.Repo, logger *slog.Logger) *Handler {
	return &Handler{media: media, logger: logger}
}
