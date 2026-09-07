package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcustomerimports"
)

// customerImportsHandler constructs the hcustomerimports.Handler from the
// Server's wired dependencies. RunImport needs the raw *pgxpool.Pool (it
// opens its own transactions) and the media repo, in addition to the
// customer-import *gen.Queries used for the create/get/list-rows CRUD.
func (s *Server) customerImportsHandler() *hcustomerimports.Handler {
	return hcustomerimports.New(s.customerImportQueries, s.pool, s.pgxPool, s.media, s.audit, s.logger)
}

func (s *Server) handleCreateCustomerImport(w http.ResponseWriter, r *http.Request) {
	s.customerImportsHandler().HandleCreateCustomerImport(w, r)
}

func (s *Server) handleDryRunCustomerImport(w http.ResponseWriter, r *http.Request) {
	s.customerImportsHandler().HandleDryRunCustomerImport(w, r)
}

func (s *Server) handleApplyCustomerImport(w http.ResponseWriter, r *http.Request) {
	s.customerImportsHandler().HandleApplyCustomerImport(w, r)
}

func (s *Server) handleGetCustomerImport(w http.ResponseWriter, r *http.Request) {
	s.customerImportsHandler().HandleGetCustomerImport(w, r)
}

func (s *Server) handleListCustomerImportRows(w http.ResponseWriter, r *http.Request) {
	s.customerImportsHandler().HandleListCustomerImportRows(w, r)
}

// mountCustomerImportRoutes mounts the platform.superadmin customer-imports
// admin surface (feature #520, W1-C7b, epic #468, spec §12.4): create an
// import record referencing an already-uploaded file_media_id, run it in
// dry_run/apply mode synchronously (via customerimport.RunImport, feature
// #519 — no worker_jobs enqueue), and inspect the resulting report/rows.
func (s *Server) mountCustomerImportRoutes(r chi.Router) {
	if !s.authEnabled() || s.customerImportQueries == nil || s.pool == nil {
		return
	}
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "superadmin.read", "customer_imports")
		pr.Post("/admin/customer-imports", s.handleCreateCustomerImport)
		pr.Post("/admin/customer-imports/{id}/dry-run", s.handleDryRunCustomerImport)
		pr.Post("/admin/customer-imports/{id}/apply", s.handleApplyCustomerImport)
		pr.Get("/admin/customer-imports/{id}", s.handleGetCustomerImport)
		pr.Get("/admin/customer-imports/{id}/rows", s.handleListCustomerImportRows)
	})
}
