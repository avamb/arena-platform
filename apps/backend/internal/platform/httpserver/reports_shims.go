// reports_shims.go bridges the *Server god-object to the hreports sub-package.
// All handler and response-shaping logic lives in hreports/; these thin
// delegating methods preserve the unexported *Server method surface so test
// files and mount files (mount_admin.go) compile unchanged.
//
// The report.deliver worker-job enqueue hook (enqueueReportDeliveryJob) moved
// into hreports/report_delivery_enqueue.go as an unexported Handler method —
// its only caller is HandleTriggerEventReport inside the sub-package, so no
// *Server delegate is required.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hreports"
)

// reportsHandler constructs a hreports.Handler from the server's dependencies.
func (s *Server) reportsHandler() *hreports.Handler {
	return hreports.New(
		s.reportQueries,
		s.workerPool,
		s.logger,
	)
}

// ─── type aliases ─────────────────────────────────────────────────────────────
// These let test files in package httpserver reference types that now live in
// hreports without importing that package directly.

type eventReportResponse = hreports.EventReportResponse

//nolint:unused // source-grep witness: report_159_test.go asserts the symbol name.
type eventReportLineResponse = hreports.EventReportLineResponse

// ─── pure-function forwarders ─────────────────────────────────────────────────

// buildEventReportResponse forwards to hreports.BuildEventReportResponse so
// that report_159_test.go (package httpserver) continues to call the pure
// response builder directly.
func buildEventReportResponse(r gen.EventReportRow, lines []gen.EventReportLineRow) eventReportResponse {
	return hreports.BuildEventReportResponse(r, lines)
}

// ─── event report handler shims ───────────────────────────────────────────────

func (s *Server) handleGetEventReport(w http.ResponseWriter, r *http.Request) {
	s.reportsHandler().HandleGetEventReport(w, r)
}

func (s *Server) handleTriggerEventReport(w http.ResponseWriter, r *http.Request) {
	s.reportsHandler().HandleTriggerEventReport(w, r)
}
