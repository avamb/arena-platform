// text_format_shims.go wires the text-formatting route
// (POST /v1/organizations/{org_id}/texts/format, spec 44 RT-04) to the model.
// The key is the poster one (POSTER_LLM_API_KEY) and lives on arena-api only.
package httpserver

import (
	"log/slog"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/htextformat"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/textformat"
)

// resolveTextFormatter builds the formatter of a Server: the Anthropic client
// when POSTER_LLM_API_KEY is set, else nil (a test points POSTER_LLM_BASE_URL at
// a stub Messages API, as the poster reading does). It returns an untyped nil
// interface when there is none, so the route can tell "not configured" from
// "configured".
func resolveTextFormatter(cfg *config.Config) textformat.Formatter {
	if cfg == nil {
		return nil
	}
	f := textformat.NewAnthropic(cfg.PosterLLMAPIKey, cfg.TextFormatLLMModel, cfg.PosterLLMBaseURL, nil)
	if !f.Configured() {
		return nil
	}
	return f
}

// logTextFormatting says at start whether the route can use the model; it
// never prints the key.
func (s *Server) logTextFormatting() {
	if s.textFormatter != nil {
		s.logger.Info("text formatting enabled", slog.String("prompt_version", textformat.PromptVersion))
		return
	}
	s.logger.Info("text formatting disabled", slog.String("reason", "POSTER_LLM_API_KEY is empty"))
}

// textFormatHandler builds the handler of the route with the caps of the
// config (a config built without the fields gets no cap).
func (s *Server) textFormatHandler() *htextformat.Handler {
	h := htextformat.New(s.logger).WithFormatter(s.textFormatter).WithRowOrgAccess(s.rowOrgAccess)
	if s.cfg != nil {
		h.WithLimits(s.cfg.TextFormatsPerUserPerDay, s.cfg.TextFormatsPerOrgPerDay)
	}
	return h
}
