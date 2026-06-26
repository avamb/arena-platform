package httpserver

import "github.com/go-chi/chi/v5"

// mountOperatorNetworkRoutes mounts the operator network CRUD endpoints
// (feature #208). The handlers themselves live in networks.go.
//
// Permission gating relies on the existing applyAuth helper:
//
//   - network.read     bound to platform_superadmin + network_operator + admin
//   - network.update   bound to platform_superadmin + network_operator + admin
//   - network.create   bound ONLY to platform_superadmin + admin
//   - network.archive  bound ONLY to platform_superadmin + admin
//
// (See migration 0044_network_permissions.sql / feature #206.) That binding
// pattern means create and archive are already restricted to platform
// superadmins without any extra route-level guard.
func (s *Server) mountOperatorNetworkRoutes(r chi.Router) {
	if s.stub == nil || !s.stub.Enabled() || s.networkQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.read", "networks")
		pr.Get("/operator-networks", s.handleListOperatorNetworks)
		pr.Get("/operator-networks/{id}", s.handleGetOperatorNetwork)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.create", "networks")
		pr.Post("/operator-networks", s.handleCreateOperatorNetwork)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.update", "networks")
		pr.Patch("/operator-networks/{id}", s.handleUpdateOperatorNetwork)
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.archive", "networks")
		pr.Post("/operator-networks/{id}/archive", s.handleArchiveOperatorNetwork)
	})
}
