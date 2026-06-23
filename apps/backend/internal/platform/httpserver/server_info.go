// server_info.go implements GET /v1/server-info — the minimal public read
// endpoint introduced by feature #104.
//
// The handler demonstrates the complete arena_new request chain:
//
//	router (chi) → handler (*Server method) → sqlc (SelectServerTime) → JSON response
//
// Response fields:
//   - version:         cfg.AppVersion (injected at startup)
//   - build_sha:       extracted from runtime/debug.ReadBuildInfo (vcs.revision)
//   - server_time:     PostgreSQL now() via gen.Queries.SelectServerTime (sqlc)
//   - environment:     cfg.AppEnv (development / staging / production)
//   - locales:         cfg.ActiveLocales (BCP-47 tags supported by this deployment)
//   - welcome_message: i18n.Localize("server.welcome") — locale resolved from
//     the request via Accept-Language → ?lang= → default negotiation
//
// The endpoint is always public (no JWT required); authentication is out of
// scope for this milestone.
package httpserver

import (
	"net/http"
	"runtime/debug"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// serverInfoResponse is the JSON body returned by GET /v1/server-info.
// snake_case per the project's API response style guide.
type serverInfoResponse struct {
	Version        string   `json:"version"`
	BuildSha       string   `json:"build_sha"`
	ServerTime     string   `json:"server_time"`
	Environment    string   `json:"environment"`
	Locales        []string `json:"locales"`
	WelcomeMessage string   `json:"welcome_message"`
}

// handleServerInfo serves GET /v1/server-info.
//
// Chain:
//  1. Route matched by chi's router.
//  2. Handler reads cfg, clock, and sqlc Queries from the *Server receiver.
//  3. gen.Queries.SelectServerTime executes `SELECT now()` via pgx (sqlc layer).
//  4. JSON response marshalled and written.
//
// When siQueries is nil (e.g. in unit tests without a DB pool) the server_time
// falls back to s.clk.Now() so the endpoint still returns 200 with correct
// static metadata.
func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// Resolve welcome_message via i18n. The locale is already negotiated by
	// LocaleMiddleware (when Bundle was provided at startup); i18n.Localize reads
	// the Localizer from ctx and falls back to the English string when not set.
	welcomeMsg := i18n.Localize(ctx, "server.welcome", "Welcome to Arena Platform!", nil)

	// Determine server_time: prefer the PostgreSQL clock (via sqlc) so the
	// response demonstrates the full chain. Fall back to the injected clock
	// when no DB pool is available (unit tests, degraded mode).
	serverTime := s.clk.Now().UTC()
	if s.siQueries != nil {
		dbTime, err := s.siQueries.SelectServerTime(ctx)
		if err != nil {
			// Non-fatal: log and continue with the fallback clock value.
			logger.Warn("server-info: SelectServerTime failed", "error", err)
		} else {
			serverTime = dbTime.UTC()
		}
	}

	resp := serverInfoResponse{
		Version:        s.cfg.AppVersion,
		BuildSha:       readBuildSHA(),
		ServerTime:     serverTime.Format(time.RFC3339Nano),
		Environment:    string(s.cfg.AppEnv),
		Locales:        s.cfg.ActiveLocales,
		WelcomeMessage: welcomeMsg,
	}

	writeJSON(w, http.StatusOK, resp)
}

// readBuildSHA extracts the vcs.revision setting from runtime/debug.ReadBuildInfo.
// This is the git commit SHA embedded by the Go toolchain at build time when
// the binary is built from a VCS working directory. Returns "dev" when building
// outside a VCS context (e.g. local `go run` without a git repo).
func readBuildSHA() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}
	return "dev"
}
