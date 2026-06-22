// info.go wires GET /v1/info — the read-only example endpoint described in
// app_spec.txt §api_endpoints_summary.
//
// The handler returns the static service metadata required by the spec
// (service name, version, commit, supported locales) and, crucially for
// feature #5 ("Backend API queries real database"), issues a SELECT against
// PostgreSQL so the query tracer surfaces SQL traffic for every /v1/info
// call. The SQL result is included in the response so callers (and
// integration tests) can confirm the database round-trip happened end-to-end.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// infoResponse is the JSON envelope returned by GET /v1/info. snake_case per
// the project's API response style.
type infoResponse struct {
	App              string   `json:"app"`
	Version          string   `json:"version"`
	Commit           string   `json:"commit"`
	Env              string   `json:"env"`
	SupportedLocales []string `json:"supported_locales"`
	DefaultLocale    string   `json:"default_locale"`
	// ActiveLocale is the locale resolved for this specific request using the
	// negotiation chain: Accept-Language → ?lang= → default_locale. It tells
	// the client which language will be used for localized error messages on
	// subsequent requests that use the same locale negotiation inputs.
	ActiveLocale string `json:"active_locale"`
	ServerTime   string `json:"server_time"`
	DBVersion    string `json:"db_version,omitempty"`
	DBNow        string `json:"db_now,omitempty"`
	RequestID    string `json:"request_id"`
	TraceID      string `json:"trace_id"`
}

// handleInfo serves GET /v1/info. It runs a real SELECT against PostgreSQL
// so the query tracer (feature #5) has SQL to observe; the result is folded
// into the response so the endpoint is also a useful diagnostic for the
// running database version.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := logging.FromContext(ctx)

	// Resolve the active locale for this request using the standard
	// negotiation chain (Accept-Language → ?lang= → default). The result is
	// included in the response so clients know which language will be used for
	// localized messages on this and subsequent calls.
	acceptLang := r.Header.Get("Accept-Language")
	activeLocale := i18n.NegotiateLocale(
		acceptLang,
		r.URL.Query().Get("lang"),
		"", // no user preferred_locale at this (unauthenticated) endpoint
		s.cfg.DefaultLocale,
		s.cfg.ActiveLocales,
	)
	// Emit a structured DEBUG log for every locale resolution so operators can
	// audit which locale was selected and what the client originally requested.
	// This satisfies feature #55 step 4: "locale_resolved=en, locale_requested=xx".
	logger.Debug("locale resolved",
		"locale_resolved", activeLocale,
		"locale_requested", acceptLang,
	)

	resp := infoResponse{
		App:              s.cfg.AppName,
		Version:          s.cfg.AppVersion,
		Commit:           s.cfg.AppCommit,
		Env:              string(s.cfg.AppEnv),
		SupportedLocales: s.cfg.ActiveLocales,
		DefaultLocale:    s.cfg.DefaultLocale,
		ActiveLocale:     activeLocale,
		ServerTime:       time.Now().UTC().Format(time.RFC3339Nano),
		RequestID:        r.Header.Get("X-Request-Id"),
		TraceID:          logging.TraceID(ctx),
	}

	// Issue a real SELECT so the query tracer surfaces SQL traffic for
	// every /v1/info call. The SELECT also returns the running Postgres
	// version + clock — useful operational diagnostics. We deliberately
	// reach for s.pool (the pgxpool) here rather than s.db (the readiness
	// Pinger), because /v1/info needs to actually execute SQL.
	if s.pool != nil {
		var dbVersion string
		var dbNow time.Time
		err := s.pool.QueryRow(ctx,
			`SELECT current_setting('server_version') AS version, now() AS db_now`,
		).Scan(&dbVersion, &dbNow)
		if err != nil {
			// Non-fatal: log and continue with the static metadata.
			logger.Warn("info: select db version/now failed", "error", err.Error())
		} else {
			resp.DBVersion = dbVersion
			resp.DBNow = dbNow.UTC().Format(time.RFC3339Nano)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
