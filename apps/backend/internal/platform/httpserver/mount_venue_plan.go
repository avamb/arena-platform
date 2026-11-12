package httpserver

// mount_venue_plan.go — the hall plan intake (spec 08_architecture/64, wave
// 1), served by hvenueplan.
//
// Organization routes need seating_plan.create (writes) or seating_plan.read
// (reads) IN the path organization and its membership (orgGated). The agent
// role holds neither, so an agent never approves. The handlers read every
// child by (id, org_id): another organization's intake, source or draft is
// the route's own 404. An organization API key is refused with 403 after that
// lookup. Administrator routes need venue_plan_intake.curate (granted to
// platform_superadmin only, never to an API key) and the superadmin marker;
// writes and the source download need X-Admin-Reason.

import (
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hvenueplan"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/venueplan"
)

var (
	venuePlanCreatePath = regexp.MustCompile(`^/v1/organizations/[^/]+/venues/[^/]+/plan-intakes/?$`)
	venuePlanDraftPath  = regexp.MustCompile(`^/v1/admin/plan-intakes/[^/]+/drafts/?$`)
)

// venuePlanUploadBodyLimit lets the two upload routes of the intake past the
// global body limit (BODY_LIMIT_BYTES, 1 MiB): the create route takes up to
// 80 MB of files, the administrator's draft one SVG of up to 3 MB. Each
// handler caps its own body exactly. Every other request answers 0.
func venuePlanUploadBodyLimit(r *http.Request) int64 {
	if r.Method != http.MethodPost {
		return 0
	}
	switch {
	case venuePlanCreatePath.MatchString(r.URL.Path):
		return hvenueplan.CreateBodyLimit
	case venuePlanDraftPath.MatchString(r.URL.Path):
		return hvenueplan.DraftBodyLimit
	}
	return 0
}

// SetVenuePlanNotifier sets who hears of new, escalated and approved intakes
// (spec 64 section 3.2). Nil = nobody. Safe to call after New, before serving.
func (s *Server) SetVenuePlanNotifier(n venueplan.Notifier) { s.venuePlanNotifier = n }

func (s *Server) mountVenuePlanRoutes(r chi.Router) {
	// Mounted without a database too: the handlers answer 503 then, and the
	// route guards see the routes.
	if !s.authEnabled() {
		return
	}
	h := hvenueplan.New(s.pgxPool, s.media, s.audit, s.logger).
		WithNotifier(func() venueplan.Notifier { return s.venuePlanNotifier })
	const in = "/organizations/{org_id}/plan-intakes/{intake_id}"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.read", "seating")
		pr.Get("/organizations/{org_id}/plan-intakes", s.orgGated(h.HandleList))
		pr.Get(in, s.orgGated(h.HandleGet))
		pr.Get(in+"/sources/{source_id}", s.orgGated(h.HandleSource))
		pr.Get(in+"/drafts/{n}/svg", s.orgGated(h.HandleDraftSVG))
	})
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "seating_plan.create", "seating")
		pr.Post("/organizations/{org_id}/venues/{venue_id}/plan-intakes", s.orgGated(h.HandleCreate))
		pr.Post(in+"/answers", s.orgGated(h.HandleAnswers))
		pr.Post(in+"/corrections-text", s.orgGated(h.HandleCorrectionText))
		pr.Post(in+"/escalate", s.orgGated(h.HandleEscalate))
		pr.Post(in+"/cancel", s.orgGated(h.HandleCancel))
		pr.Post(in+"/approve", s.orgGated(h.HandleApprove))
	})
	const adm = "/admin/plan-intakes/{intake_id}"
	r.Group(func(pr chi.Router) {
		s.applyAuth(pr, "venue_plan_intake.curate", "venue_plan_intakes")
		pr.Get("/admin/plan-intakes", h.HandleAdminList)
		pr.Get(adm, h.HandleAdminGet)
		pr.Get(adm+"/sources/{source_id}", h.HandleAdminSource)
		pr.Get(adm+"/drafts/{n}/svg", h.HandleAdminDraftSVG)
		pr.Post(adm+"/claim", h.HandleAdminClaim)
		pr.Post(adm+"/drafts", h.HandleAdminDraft)
		pr.Post(adm+"/return", h.HandleAdminReturn)
		pr.Post(adm+"/approve", h.HandleAdminApprove)
		pr.Post(adm+"/reject", h.HandleAdminReject)
	})
}
