package hmedia

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// Handler holds the narrow set of dependencies needed by the /v1/media
// and /v1/media-files endpoints.
type Handler struct {
	media  *mediastore.Repo
	logger *slog.Logger
	// publicBase, when set, names the API origin a host-relative signed
	// download URL (the local backend) is absolutized onto in GET /v1/media/{id}.
	publicBase func() string
	// rowOrgAccess keeps a caller inside its organizations (org_access.go).
	rowOrgAccess RowOrgAccess
	// poster reads a poster with the vision model (poster_facts.go); nil
	// answers 503 media.poster_reader_not_configured. posterLimiter is the
	// per-organization daily cap, nil = no cap. posterStore is the media
	// lookup of that route (the same Repo, narrowed for tests).
	poster        posterread.Reader
	posterLimiter ratelimit.Limiter
	posterStore   posterMediaStore
}

// New constructs a Handler. media may be nil; each method guards against it
// and returns 503 so the server starts cleanly without a storage backend.
func New(media *mediastore.Repo, logger *slog.Logger) *Handler {
	h := &Handler{media: media, logger: logger}
	if media != nil { // never wrap a nil *Repo in a non-nil interface
		h.posterStore = media
	}
	return h
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
