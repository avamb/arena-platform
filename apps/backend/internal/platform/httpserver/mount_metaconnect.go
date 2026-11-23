package httpserver

// mount_metaconnect.go - the Meta business connection of an organization
// (spec 08_architecture/66, stage M1). The organization routes are gated on
// org.update in the path organization (the owner) and refuse an API key, like
// conversion-settings. The callback and Meta's two signed_request callbacks
// are public: the signed state and the app-secret signature are the
// credentials. The callback never exchanges the code: it hands code and state
// to the event center in the URL fragment and the signed-in owner posts them
// to .../integrations/meta/finish (no login CSRF). The routes are always mounted; when META_* is incomplete GET
// answers enabled=false and connect answers 503 meta.disabled.

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hmetaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/metaconnect"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/secretbox"
)

func (s *Server) metaConnectHandler() *hmetaconnect.Handler {
	var (
		settings hmetaconnect.Settings
		box      *secretbox.Box
		graph    *metaconnect.Client
	)
	if c := s.cfg; c != nil {
		settings = hmetaconnect.Settings{
			Enabled:               c.MetaEnabled(),
			AppID:                 c.MetaAppID,
			AppSecret:             c.MetaAppSecret,
			ConfigID:              c.MetaLoginConfigID,
			GraphVersion:          c.MetaGraphVersion,
			RedirectURL:           c.MetaOAuthRedirectURL,
			EventCenterURL:        c.EventCenterPublicURL,
			DataDeletionStatusURL: c.MetaDataDeletionStatusURL,
			WebhookVerifyToken:    c.MetaWebhookVerifyToken,
			OwnerTelegram:         c.EventsTelegramBotToken != "",
		}
		if settings.Enabled {
			if edges, err := metaconnect.ParseEdges(c.MetaAssetEdges); err == nil {
				settings.Edges = edges
			} else if s.logger != nil {
				s.logger.Error("metaconnect: META_ASSET_EDGES is invalid, the Meta connection is off", "error", err.Error())
			}
			if b, err := secretbox.ParseKeys(c.MetaTokenEncKey); err == nil {
				box = b
			}
			graph = metaconnect.NewClient(c.MetaGraphBaseURL, c.MetaGraphVersion, c.MetaAppID, c.MetaAppSecret, nil)
		}
	}
	return hmetaconnect.New(s.pgxPool, s.audit, settings, box, graph, s.logger)
}

func (s *Server) mountMetaConnectRoutes(r chi.Router) {
	// Public: the login callback (signed state) and Meta's callbacks
	// (signed_request with the app secret).
	r.Get("/integrations/meta/callback", s.handleMetaCallback)
	r.Post("/integrations/meta/deauthorize", s.handleMetaDeauthorize)
	r.Post("/integrations/meta/data-deletion", s.handleMetaDataDeletion)
	// Meta's app webhook (business_integration_uninstall): GET is the
	// subscription check (verify token), POST carries X-Hub-Signature-256.
	r.Get("/integrations/meta/webhook", s.handleMetaWebhookVerify)
	r.Post("/integrations/meta/webhook", s.handleMetaWebhook)
	if !s.authEnabled() {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "org.update", "organizations")
		pr.Post("/organizations/{org_id}/integrations/meta/connect", s.handleMetaConnect)
		pr.Post("/organizations/{org_id}/integrations/meta/finish", s.handleMetaFinish)
		pr.Get("/organizations/{org_id}/integrations/meta", s.handleGetMetaIntegration)
		pr.Patch("/organizations/{org_id}/integrations/meta", s.handlePatchMetaIntegration)
		pr.Delete("/organizations/{org_id}/integrations/meta", s.handleDeleteMetaIntegration)
		pr.Get("/organizations/{org_id}/integrations/meta/page", s.handleGetMetaPage)
	})
}

func (s *Server) handleGetMetaPage(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandlePage(w, r)
}

func (s *Server) handleMetaCallback(w http.ResponseWriter, r *http.Request) {
	s.metaConnectHandler().HandleCallback(w, r)
}

func (s *Server) handleMetaDeauthorize(w http.ResponseWriter, r *http.Request) {
	s.metaConnectHandler().HandleDeauthorize(w, r)
}

func (s *Server) handleMetaDataDeletion(w http.ResponseWriter, r *http.Request) {
	s.metaConnectHandler().HandleDataDeletion(w, r)
}

func (s *Server) handleMetaWebhookVerify(w http.ResponseWriter, r *http.Request) {
	s.metaConnectHandler().HandleWebhookVerify(w, r)
}

func (s *Server) handleMetaWebhook(w http.ResponseWriter, r *http.Request) {
	s.metaConnectHandler().HandleWebhook(w, r)
}

func (s *Server) handleMetaConnect(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandleConnect(w, r)
}

func (s *Server) handleMetaFinish(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandleFinish(w, r)
}

func (s *Server) handleGetMetaIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandleGet(w, r)
}

func (s *Server) handlePatchMetaIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandlePatch(w, r)
}

func (s *Server) handleDeleteMetaIntegration(w http.ResponseWriter, r *http.Request) {
	if !s.enforceOrgMembership(w, r, "org_id") {
		return
	}
	s.metaConnectHandler().HandleDelete(w, r)
}
