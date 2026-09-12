// public_url_shims.go holds the *Server helpers that answer the question
// "which absolute URL do we hand to somebody outside this process?" (feature
// #535, spec 08_architecture/22_site_facing_gaps_w1s1_ru.md §2.1).
//
// Two origins exist and they are NOT interchangeable:
//
//   - APP_PUBLIC_URL is the SPA/admin origin — links a human clicks.
//   - API_PUBLIC_URL is the API's own origin — links a machine (the WordPress
//     plugin, a Bil24-compat site) has to FETCH: the gateway base_url/image_url,
//     ticket PDF links, signed poster URLs.
//
// Building a site-facing link on the SPA origin produces a URL that resolves to
// the admin front-end and never reaches a handler, which is exactly the class of
// bug #535 exists to close. They coincide on single-host deployments only,
// which is why apiPublicURL falls back to APP_PUBLIC_URL.
package httpserver

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// mediaPublicURLTTL is the validity window of the signed media URLs handed to
// third-party sites (spec 22 §2.1). 24h: WordPress sites re-sync every 15
// minutes, so a day-long window always hands them a live link while keeping the
// signature short-lived enough to matter.
const mediaPublicURLTTL = 24 * time.Hour

// There is deliberately no appPublicURL() accessor here: nothing inside the
// server builds a link on the SPA origin any more (auth e-mails render theirs
// from cfg.AppPublicURL inside the mailer), and an idle accessor is exactly
// what a future site-facing link would be tempted to reach for.

// apiPublicURL returns the canonical public origin of the API itself
// (API_PUBLIC_URL, falling back to APP_PUBLIC_URL on single-host deployments),
// with any trailing slash removed. "" means the operator has configured
// neither, which is a supported development-mode behaviour — production with
// the Bil24 gateway enabled is rejected at config-validation time instead.
func (s *Server) apiPublicURL() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.APIPublicBaseURL()
}

// signedMediaURL turns a media_objects id into an absolute, signed download
// URL a third party can fetch. It returns "" when media storage is not wired or
// the object cannot be signed, so callers can fall back to their legacy
// projection instead of emitting a broken link.
func (s *Server) signedMediaURL(ctx context.Context, id uuid.UUID) string {
	if s.media == nil {
		return ""
	}
	raw, err := s.media.SignedDownloadURL(ctx, id, mediaPublicURLTTL)
	if err != nil || raw == "" {
		if err != nil && s.logger != nil {
			s.logger.Warn("media: signed url failed",
				slog.String("media_id", id.String()),
				slog.String("error", err.Error()))
		}
		return ""
	}
	// External backends (S3 presign) already return an absolute URL; the local
	// backend returns a host-relative signed path that needs the API origin.
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return s.apiPublicURL() + raw
}
