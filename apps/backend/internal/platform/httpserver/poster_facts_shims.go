// poster_facts_shims.go wires the poster-reading route
// (POST /v1/organizations/{org_id}/media/{media_id}/poster-facts) to the
// vision model. The model key lives on arena-api only (2026-10-14): the
// Telegram bot and the web wizard both call this route instead of the model.
package httpserver

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmedia"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// PosterReader is what the route reads a poster with (posterread.Reader),
// re-declared so Options can name it without a second import in wire.go.
type PosterReader = posterread.Reader

// resolvePosterReader picks the reader of a Server: the injected one (tests),
// else the Anthropic reader when POSTER_LLM_API_KEY is set, else nil. It
// returns an untyped nil interface when there is none, so the route can tell
// "not configured" from "configured".
func resolvePosterReader(opts Options) PosterReader {
	if opts.PosterReader != nil {
		return opts.PosterReader
	}
	if opts.Config == nil {
		return nil
	}
	reader := posterread.NewAnthropic(opts.Config.PosterLLMAPIKey, opts.Config.PosterLLMModel, opts.Config.PosterLLMBaseURL, nil)
	if !reader.Configured() {
		return nil
	}
	return reader
}

// posterReadLimiter builds the per-organization daily cap of the route, or
// nil when POSTER_READS_PER_ORG_PER_DAY is 0 (no cap). A config built without
// the field (tests that fill config.Config by hand) gets no cap too.
func posterReadLimiter(cfg *config.Config) ratelimit.Limiter {
	if cfg == nil || cfg.PosterReadsPerOrgPerDay <= 0 {
		return nil
	}
	return ratelimit.New(ratelimit.Config{
		MaxAttempts: cfg.PosterReadsPerOrgPerDay,
		Window:      hmedia.PosterReadsWindow,
	})
}

// logPosterReading says at start whether the route can read posters; it never
// prints the key.
func (s *Server) logPosterReading() {
	if s.posterReader != nil {
		s.logger.Info("poster reading enabled")
		return
	}
	s.logger.Info("poster reading disabled", slog.String("reason", "POSTER_LLM_API_KEY is empty"))
}
