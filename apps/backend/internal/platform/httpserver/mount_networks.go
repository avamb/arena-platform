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

	// Network user assignment endpoints (feature #209).
	//
	// All three are gated by `network.manage_users`, which per migration
	// 0044_network_permissions.sql is bound only to platform_superadmin and
	// the legacy admin role -- never to network_operator. That binding
	// pattern satisfies the "only platform_superadmin can edit the roster"
	// requirement without an extra route-level guard.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.manage_users", "networks")
		pr.Get("/admin/networks/{id}/users", s.handleListNetworkUsers)
		pr.Post("/admin/networks/{id}/users", s.handleAssignNetworkUser)
		pr.Delete("/admin/networks/{id}/users/{userId}", s.handleRemoveNetworkUser)
	})

	// Organizer attachment endpoints (feature #210).
	//
	// Gated by `network.manage_organizers`. Per 0044_network_permissions.sql
	// this permission is bound to platform_superadmin, network_operator, and
	// admin -- so the day-to-day operator can manage their organizer roster
	// without needing platform_superadmin.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.manage_organizers", "networks")
		pr.Get("/admin/networks/{id}/organizers",
			s.handleListNetworkOrganizations(networkAssignmentKindOrganizer))
		pr.Post("/admin/networks/{id}/organizers",
			s.handleAttachNetworkOrganization(networkAssignmentKindOrganizer))
		pr.Delete("/admin/networks/{id}/organizers/{orgId}",
			s.handleDetachNetworkOrganization(networkAssignmentKindOrganizer))
	})

	// Agent attachment endpoints (feature #210).
	//
	// Gated by `network.manage_agents`. Same role-binding set as
	// network.manage_organizers per 0044_network_permissions.sql.
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "network.manage_agents", "networks")
		pr.Get("/admin/networks/{id}/agents",
			s.handleListNetworkOrganizations(networkAssignmentKindAgent))
		pr.Post("/admin/networks/{id}/agents",
			s.handleAttachNetworkOrganization(networkAssignmentKindAgent))
		pr.Delete("/admin/networks/{id}/agents/{orgId}",
			s.handleDetachNetworkOrganization(networkAssignmentKindAgent))
	})
}
