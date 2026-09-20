// Package mediaresolver adapts the media store to the narrow
// delivery.MediaResolver interface the ticket-delivery worker uses to put
// the organizer logo and the event poster on a ticket.
//
// Until this package existed NOTHING implemented delivery.MediaResolver in
// production: the interface, its ErrLogoNotFound sentinel and both call
// sites (resolveBranding for the logo, resolvePoster for the poster) were
// shipped and unit-tested against stubs, while arena-worker — the only
// process that renders real ticket e-mails — built the handler with
// HandlerOptions.Media left nil. Both images therefore degraded silently on
// every live ticket ever sent.
//
// Two things have to come back from one media id:
//
//   - the BYTES, embedded into the PDF (the logo band, the poster column);
//   - a fetchable URL, used by the e-mail body's <img src> for the logo.
//
// The URL half is the part a worker cannot get for free. mediastore signs
// the local /v1/media-files/{id} path but returns it HOST-RELATIVE, which is
// meaningless inside an e-mail, so the API origin has to be prepended
// exactly as the API process does it for site-facing links
// (httpserver.Server.signedMediaURL, feature #535 spec 22 §2.1): absolute
// URLs from an external backend pass through untouched, a relative signed
// path gets API_PUBLIC_URL (falling back to APP_PUBLIC_URL) in front.
//
// Every failure mode is reported as an error and NEVER as a panic or a
// blocked send: delivery.resolveBranding falls back to the platform logo and
// delivery.resolvePoster drops the poster. A missing image must never cost a
// buyer their ticket.
package mediaresolver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// SignedURLTTL is how long the signed logo URL embedded in a ticket e-mail
// stays valid.
//
// 30 days, not the 24h the API hands to site catalogs: an e-mail is opened
// whenever the buyer gets round to it — often days after the purchase, and
// again on the way to the venue — and an expired URL renders as a broken
// image in the header. Nothing sensitive sits behind the signature (it is a
// public brand logo), and no S3 presigning is involved: the media adapter
// implements no PresignGet, so every backend signs the platform's own
// /v1/media-files/{id} path, which has no AWS 7-day ceiling.
const SignedURLTTL = 30 * 24 * time.Hour

// maxObjectBytes caps what may be read into memory for one e-mail. Logos and
// web-sized posters are far below this; anything above is an upload mistake
// (a print master) and is refused rather than multiplied across every
// recipient of a session. delivery.resolvePoster applies its own, tighter
// ceiling afterwards — this one exists so the read itself stays bounded.
const maxObjectBytes = 16 << 20 // 16 MiB

// Source is the slice of *mediastore.Repo this adapter needs. Declared as an
// interface so the adapter can be exercised without a database.
type Source interface {
	GetByID(ctx context.Context, id uuid.UUID) (mediastore.Object, error)
	Storage() storage.Storage
	SignedDownloadURL(ctx context.Context, id uuid.UUID, ttl time.Duration) (string, error)
}

// Resolver implements delivery.MediaResolver over a media store.
type Resolver struct {
	src     Source
	baseURL string
	logger  *slog.Logger
}

// Interface check: the whole point of this package.
var _ delivery.MediaResolver = (*Resolver)(nil)

// New returns a Resolver reading through src, absolutizing relative signed
// URLs onto apiPublicBaseURL (config.Config.APIPublicBaseURL(); "" is
// tolerated and simply leaves the URL host-relative).
//
// A nil src yields a nil delivery.MediaResolver — NOT a non-nil interface
// holding a nil pointer — so a caller may pass the result straight into
// delivery.HandlerOptions.Media when media storage is not configured and get
// exactly today's behaviour (platform logo, no poster).
func New(src Source, apiPublicBaseURL string, logger *slog.Logger) delivery.MediaResolver {
	if src == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		src:     src,
		baseURL: strings.TrimRight(strings.TrimSpace(apiPublicBaseURL), "/"),
		logger:  logger,
	}
}

// ResolveLogo implements delivery.MediaResolver. It resolves any media
// object, not just a logo (the poster goes through it too); the method name
// predates the second caller.
//
// A media id that is malformed, unknown or soft-deleted, and an object whose
// bytes are gone from the storage backend, all come back wrapped around
// delivery.ErrLogoNotFound so the handler falls back without retrying.
// Anything else (a media-store outage) is returned verbatim.
//
// The signed URL is best effort: when it cannot be produced the bytes are
// still returned, because the PDF embed is the half that matters most and an
// empty URL only drops the <img> from the e-mail header.
func (r *Resolver) ResolveLogo(ctx context.Context, mediaID string) ([]byte, string, error) {
	if r == nil || r.src == nil {
		return nil, "", fmt.Errorf("mediaresolver: no media source configured: %w", delivery.ErrLogoNotFound)
	}
	id, err := uuid.Parse(strings.TrimSpace(mediaID))
	if err != nil {
		return nil, "", fmt.Errorf("mediaresolver: media id %q is not a uuid: %w", mediaID, delivery.ErrLogoNotFound)
	}

	obj, err := r.src.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, mediastore.ErrNotFound) {
			return nil, "", fmt.Errorf("mediaresolver: media %s: %w", id, delivery.ErrLogoNotFound)
		}
		return nil, "", fmt.Errorf("mediaresolver: load media %s: %w", id, err)
	}

	raw, err := r.read(ctx, obj)
	if err != nil {
		return nil, "", err
	}

	return raw, r.signedURL(ctx, id), nil
}

// read streams the object's bytes with a hard ceiling. The body is always
// closed before returning — on Windows an open handle also blocks the
// media-gc job from deleting the file later.
func (r *Resolver) read(ctx context.Context, obj mediastore.Object) ([]byte, error) {
	res, err := r.src.Storage().Get(ctx, obj.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("mediaresolver: media %s has no stored bytes: %w", obj.ID, delivery.ErrLogoNotFound)
		}
		return nil, fmt.Errorf("mediaresolver: read media %s: %w", obj.ID, err)
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("mediaresolver: read media %s: %w", obj.ID, err)
	}
	if len(raw) > maxObjectBytes {
		return nil, fmt.Errorf("mediaresolver: media %s is larger than the %d-byte ceiling", obj.ID, maxObjectBytes)
	}
	return raw, nil
}

// signedURL renders the fetchable URL for an e-mail <img src>, or "" when it
// cannot be produced. Mirrors httpserver.Server.signedMediaURL: an absolute
// URL from an external backend is handed over untouched, a host-relative
// signed path is prefixed with the API's public origin.
func (r *Resolver) signedURL(ctx context.Context, id uuid.UUID) string {
	raw, err := r.src.SignedDownloadURL(ctx, id, SignedURLTTL)
	if err != nil || raw == "" {
		if err != nil {
			r.logger.Warn("mediaresolver: signed url failed; the e-mail header renders without the logo image",
				slog.String("media_id", id.String()),
				slog.String("error", err.Error()),
			)
		}
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return r.baseURL + raw
}
