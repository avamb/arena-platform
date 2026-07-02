// geo_shims.go bridges the *Server god-object to the hgeo sub-package. All
// handler bodies live in hgeo/; these thin delegating methods preserve the
// unexported *Server method surface so mount_iam.go and geo_test.go compile
// unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hgeo"
)

// geoHandler constructs an hgeo.Handler from the server's dependencies. A
// fresh handler per request keeps the wiring uniform with hbilling /
// hreconciliation and avoids stale captures when test code mutates *Server
// fields between calls.
func (s *Server) geoHandler() *hgeo.Handler {
	return hgeo.New(
		s.geoQueries,
		s.pool,
		s.cfg,
	)
}

// geoLocale delegates to hgeo.(*Handler).GeoLocale. geo_test.go calls this
// unexported method directly on a bare &Server{cfg: …} value.
func (s *Server) geoLocale(r *http.Request) string {
	return s.geoHandler().GeoLocale(r)
}

// ─── public read handler shims ────────────────────────────────────────────────

func (s *Server) handleListCountries(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleListCountries(w, r)
}

func (s *Server) handleListCities(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleListCities(w, r)
}

// ─── admin write handler shims ────────────────────────────────────────────────

func (s *Server) handleCreateCountry(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleCreateCountry(w, r)
}

func (s *Server) handleUpdateCountry(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleUpdateCountry(w, r)
}

func (s *Server) handleCreateCity(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleCreateCity(w, r)
}

func (s *Server) handleUpdateCity(w http.ResponseWriter, r *http.Request) {
	s.geoHandler().HandleUpdateCity(w, r)
}

// ─── pure-function forwarders ─────────────────────────────────────────────────

// geoFirstNonEmpty forwards to hgeo.FirstNonEmpty. geo_test.go calls the
// original lowercase name unqualified — keep it live in package httpserver so
// callers do not learn about the hgeo sub-package.
func geoFirstNonEmpty(vals ...string) string {
	return hgeo.FirstNonEmpty(vals...)
}
