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

// posterProvider is a poster reader that knows whether it has a key.
type posterProvider interface {
	posterread.Reader
	Configured() bool
}

// buildPosterProvider builds the reader of one provider from the config; an
// unknown name yields nil.
func buildPosterProvider(cfg *config.Config, provider string, logger *slog.Logger) posterProvider {
	switch provider {
	case config.PosterProviderAnthropic:
		return posterread.NewAnthropic(cfg.PosterLLMAPIKey, cfg.PosterLLMModel, cfg.PosterLLMBaseURL, nil).WithLogger(logger)
	case config.PosterProviderGemini:
		return posterread.NewGemini(cfg.GeminiAPIKey, cfg.PosterGeminiModel, cfg.PosterGeminiBaseURL, nil).WithLogger(logger)
	}
	return nil
}

// posterProviders names the primary and the fallback provider that carry a
// key in cfg ("" when absent). A primary without a key is replaced by a keyed
// fallback; with neither, both are empty.
func posterProviders(cfg *config.Config) (primary, fallback string) {
	if cfg == nil {
		return "", ""
	}
	first := cfg.PosterLLMProvider
	if first == "" {
		first = config.PosterProviderAnthropic
	}
	has := func(p string) bool {
		b := buildPosterProvider(cfg, p, nil)
		return b != nil && b.Configured()
	}
	haveFirst := has(first)
	haveSecond := cfg.PosterLLMFallbackProvider != "" && has(cfg.PosterLLMFallbackProvider)
	switch {
	case haveFirst && haveSecond:
		return first, cfg.PosterLLMFallbackProvider
	case haveFirst:
		return first, ""
	case haveSecond:
		return cfg.PosterLLMFallbackProvider, ""
	}
	return "", ""
}

// resolvePosterReader picks the reader of a Server: the injected one (tests),
// else the configured provider (POSTER_LLM_PROVIDER, default anthropic) with
// the optional fallback provider tried once when it fails, else nil. A
// provider without its key does not count (see posterProviders). It returns
// an untyped nil interface when there is none, so the route can tell "not
// configured" from "configured".
func resolvePosterReader(opts Options) PosterReader {
	if opts.PosterReader != nil {
		return opts.PosterReader
	}
	primary, fallback := posterProviders(opts.Config)
	if primary == "" {
		return nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	first := buildPosterProvider(opts.Config, primary, logger)
	if fallback == "" {
		return first
	}
	return posterread.WithFallback(first, buildPosterProvider(opts.Config, fallback, logger), primary, fallback, logger)
}

// posterReadLimiter builds the per-organization daily cap of the route, or
// nil when POSTER_READS_PER_ORG_PER_DAY is 0 (no cap). A config built without
// the field (tests that fill config.Config by hand) gets no cap too.
func posterReadLimiter(cfg *config.Config) hmedia.PosterQuota {
	if cfg == nil || cfg.PosterReadsPerOrgPerDay <= 0 {
		return nil
	}
	return ratelimit.New(ratelimit.Config{
		MaxAttempts: cfg.PosterReadsPerOrgPerDay,
		Window:      hmedia.PosterReadsWindow,
	})
}

// logPosterReading says at start whether the route can read posters and with
// which provider; it never prints a key.
func (s *Server) logPosterReading() {
	if s.posterReader == nil {
		s.logger.Info("poster reading disabled",
			slog.String("reason", "no poster model key (POSTER_LLM_API_KEY or GEMINI_API_KEY) is set for the chosen provider"))
		return
	}
	var attrs []any
	if primary, fallback := posterProviders(s.cfg); primary != "" {
		attrs = append(attrs, slog.String("provider", primary))
		if fallback != "" {
			attrs = append(attrs, slog.String("fallback_provider", fallback))
		}
	}
	s.logger.Info("poster reading enabled", attrs...)
}
