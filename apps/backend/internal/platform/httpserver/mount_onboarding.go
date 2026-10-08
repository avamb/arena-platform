package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbot"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/onboarding"
)

// onboardingHandler builds the onboarding HTTP handler from the server's
// dependencies. The service is stateless (every fact lives in the database),
// so building it per request is cheap and keeps the Server struct unchanged.
func (s *Server) onboardingHandler() *honboarding.Handler {
	adminURL, siteURL := "", ""
	secret, salt := "", ""
	trusted := 0
	production := false
	if s.cfg != nil {
		adminURL = s.cfg.AppPublicURL
		siteURL = s.cfg.OnboardingSiteURL
		secret = s.cfg.OnboardingTurnstileSecret
		salt = s.cfg.JWTSecretStub
		trusted = s.cfg.TrustedProxyCount
		production = s.cfg.IsProduction()
	}
	svc := onboarding.New(onboarding.Options{
		Pool: s.pgxPool, Audit: s.audit, Logger: s.logger, AdminURL: adminURL, SiteURL: siteURL,
	})
	verifier := &honboarding.TurnstileVerifier{Secret: secret, Required: production}
	return honboarding.New(svc, verifier, salt, trusted, s.logger)
}

// onb adapts an honboarding.Handler method to an http.HandlerFunc and answers
// 503 while the raw pgx pool is not wired.
func (s *Server) onb(fn func(*honboarding.Handler, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.pgxPool == nil {
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r,
			))
			return
		}
		fn(s.onboardingHandler(), w, r)
	}
}

// mountOnboardingRoutes mounts the organizer application flow
// (08_architecture/34_onboarding_applications_ru.md, appendix A).
//
// The public routes are called by the website straight from the browser and
// exist only while ONBOARDING_ENABLED is on; they carry no login — an
// application is reached through its access token. The operator routes need
// onboarding.review (settings: onboarding.settings), which only
// platform_superadmin holds, and every state change needs X-Admin-Reason.
func (s *Server) mountOnboardingRoutes(r chi.Router) {
	if s.cfg != nil && s.cfg.OnboardingEnabled {
		r.Get("/onboarding/form-schema", s.onb((*honboarding.Handler).HandleFormSchema))
		r.Post("/onboarding/applications", s.onb((*honboarding.Handler).HandleStart))
		r.Get("/onboarding/applications/{id}", s.onb((*honboarding.Handler).HandleGet))
		r.Put("/onboarding/applications/{id}/answers", s.onb((*honboarding.Handler).HandleSaveAnswers))
		r.Post("/onboarding/applications/{id}/submit", s.onb((*honboarding.Handler).HandleSubmit))
		r.Post("/onboarding/confirm", s.onb((*honboarding.Handler).HandleConfirm))
		r.Post("/onboarding/resume", s.onb((*honboarding.Handler).HandleResume))

		// The Telegram bot's side: guarded by BOT_SERVICE_TOKEN, never open.
		serviceToken := ""
		if s.cfg != nil {
			serviceToken = s.cfg.BotServiceToken
		}
		r.Group(func(br chi.Router) {
			br.Use(hbot.RequireServiceToken(serviceToken))
			br.Get("/bot/onboarding/form-schema", s.onb((*honboarding.Handler).HandleBotSchema))
			br.Get("/bot/onboarding/application", s.onb((*honboarding.Handler).HandleBotCurrent))
			br.Post("/bot/onboarding/applications", s.onb((*honboarding.Handler).HandleBotStart))
			br.Put("/bot/onboarding/applications/{id}/answers", s.onb((*honboarding.Handler).HandleBotSaveAnswers))
			br.Post("/bot/onboarding/applications/{id}/email-code", s.onb((*honboarding.Handler).HandleBotEmailCode))
			br.Post("/bot/onboarding/applications/{id}/confirm-email", s.onb((*honboarding.Handler).HandleBotConfirmEmail))
			br.Post("/bot/onboarding/applications/{id}/submit", s.onb((*honboarding.Handler).HandleBotSubmit))
			br.Post("/bot/onboarding/applications/{id}/site-link", s.onb((*honboarding.Handler).HandleBotSiteLink))
			br.Post("/bot/onboarding/notifications/claim", s.onb((*honboarding.Handler).HandleBotClaimNotices))
		})
	}

	if !s.authEnabled() || s.orgQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "onboarding.review", "onboarding")
		pr.Get("/admin/onboarding/applications", s.onb((*honboarding.Handler).HandleAdminList))
		pr.Get("/admin/onboarding/applications/{id}", s.onb((*honboarding.Handler).HandleAdminGet))
		pr.Post("/admin/onboarding/applications/{id}/recheck", s.onb((*honboarding.Handler).HandleAdminRecheck))
		pr.Post("/admin/onboarding/applications/{id}/approve", s.onb((*honboarding.Handler).HandleAdminApprove))
		pr.Post("/admin/onboarding/applications/{id}/reject", s.onb((*honboarding.Handler).HandleAdminReject))
		pr.Post("/admin/onboarding/applications/{id}/request-info", s.onb((*honboarding.Handler).HandleAdminRequestInfo))
		pr.Post("/admin/onboarding/applications/{id}/extend", s.onb((*honboarding.Handler).HandleAdminExtend))
		pr.Post("/admin/onboarding/applications/{id}/resend", s.onb((*honboarding.Handler).HandleAdminResend))
		pr.Post("/admin/onboarding/applications/{id}/purge", s.onb((*honboarding.Handler).HandleAdminPurge))
		pr.Post("/admin/onboarding/applications/{id}/notes", s.onb((*honboarding.Handler).HandleAdminAddNote))
		pr.Get("/admin/onboarding/settings", s.onb((*honboarding.Handler).HandleAdminGetSettings))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "onboarding.settings", "onboarding")
		pr.Put("/admin/onboarding/settings", s.onb((*honboarding.Handler).HandleAdminPutSettings))
	})
}
