// gdpr_shims.go bridges the *Server god-object to the hgdpr sub-package. All
// handler bodies and the GDPRProcessor implementation live in hgdpr/; these
// thin delegating methods preserve the *Server method surface so
// mount_commerce.go and gdpr_164_test.go compile unchanged.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hgdpr"
)

// gdprHandler constructs an hgdpr.Handler from the server's dependencies. A
// fresh handler per request keeps the wiring uniform with hgeo / hbilling /
// hreconciliation and avoids stale captures when test code mutates *Server
// fields between calls.
func (s *Server) gdprHandler() *hgdpr.Handler {
	return hgdpr.New(
		s.gdprQueries,
		s.pool,
	)
}

// ─── data subject request handler shims ───────────────────────────────────────

func (s *Server) handleDataExportRequest(w http.ResponseWriter, r *http.Request) {
	s.gdprHandler().HandleDataExportRequest(w, r)
}

func (s *Server) handleDataDeleteRequest(w http.ResponseWriter, r *http.Request) {
	s.gdprHandler().HandleDataDeleteRequest(w, r)
}

func (s *Server) handleListDataRequests(w http.ResponseWriter, r *http.Request) {
	s.gdprHandler().HandleListDataRequests(w, r)
}

func (s *Server) handleRecordConsent(w http.ResponseWriter, r *http.Request) {
	s.gdprHandler().HandleRecordConsent(w, r)
}

// ─── pure-function forwarders ─────────────────────────────────────────────────
//
// gdpr_164_test.go calls these unexported names unqualified — keep them live
// in package httpserver so callers do not learn about the hgdpr sub-package.

func dataSubjectRequestResponse(r gen.DataSubjectRequestRow) map[string]any {
	return hgdpr.DataSubjectRequestResponse(r)
}

func formatTimePtr(t *time.Time) any {
	return hgdpr.FormatTimePtr(t)
}

// ─── GDPRProcessor bridge ─────────────────────────────────────────────────────

// GDPRProcessor preserves the original exported worker API in package
// httpserver. The implementation lives in hgdpr; this wrapper keeps the
// unexported pool/queries fields that gdpr_164_test.go inspects directly
// (a type alias cannot expose unexported fields across packages).
type GDPRProcessor struct {
	pool    PoolDB
	queries *gen.Queries
	inner   *hgdpr.GDPRProcessor
}

// NewGDPRProcessor constructs a GDPRProcessor.
// pool must be a *pgxpool.Pool (or any PoolDB implementation) for transaction
// support. queries must be constructed from the same pool for read-only
// queries. Delegates to hgdpr.NewGDPRProcessor.
func NewGDPRProcessor(pool PoolDB, queries *gen.Queries, logger *slog.Logger) *GDPRProcessor {
	return &GDPRProcessor{
		pool:    pool,
		queries: queries,
		inner:   hgdpr.NewGDPRProcessor(pool, queries, logger),
	}
}

// ProcessPendingRequests polls up to limit pending data_subject_requests and
// processes each one. Delegates to hgdpr.(*GDPRProcessor).ProcessPendingRequests.
func (p *GDPRProcessor) ProcessPendingRequests(ctx context.Context, limit int32) (int, error) {
	return p.inner.ProcessPendingRequests(ctx, limit)
}
