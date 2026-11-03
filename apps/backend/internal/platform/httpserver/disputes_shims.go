// disputes_shims.go wires the dispute engine (internal/platform/disputes,
// spec 51.3) for arena-api: verified dispute webhooks of the per-config
// route are recorded, and the organizer's dispute routes read and answer
// them. arena-worker builds the same service for its jobs
// (cmd/arena-worker/disputes.go).
package httpserver

import (
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// disputeService builds the service, or nil when the server has no raw
// pool (unit-test servers).
func (s *Server) disputeService() *disputes.Service {
	if s.pgxPool == nil {
		return nil
	}
	return disputes.New(disputes.Options{
		DB:      s.pgxPool,
		Modules: hcheckout.NewRefundModuleSource(gen.New(s.pgxPool), s.paymentModuleOptions()),
		Logger:  s.logger,
	})
}

// disputeRecorder is the webhook side; a nil interface when there is no
// service, so the webhook acknowledges a dispute and writes nothing.
func (s *Server) disputeRecorder() hcheckout.DisputeRecorder {
	if svc := s.disputeService(); svc != nil {
		return svc
	}
	return nil
}
