package httpserver

// org_isolation_sweep_table_test.go — the data half of the organization
// isolation sweep (SEC-2) and the coverage guard that keeps it complete.
//
// THE CLASS. A route with {org_id} in its path plus a child id ({event_id},
// {session_id}, {channel_id}, {id}, ...) is gated twice by the router: a
// permission (RBAC) and "is the caller a member of {org_id}". Neither proves
// that the CHILD belongs to {org_id}. The permission checker unions the roles
// of ALL a caller's memberships and every organization API key carries the
// scopes it was issued with, so an owner of organization B holds feed_token.*,
// inventory.*, session.update, ... and, without a third check, reaches
// organization A's rows by their UUIDs (which the public feeds and hosted pages
// publish). Every such handler must verify that the child's organization is the
// path organization and answer the route's own 404 when it is not.
//
// The sweep (org_isolation_sweep_integration_test.go) calls each hole as the
// attacker (an org-B owner, an org-B manager, an org-B API key) with org A's
// ids under /v1/organizations/<B>/... and proves A's rows did not move. This
// untagged file holds the table and the guard:
//
//   - sweepRows is the table. Add a row for every new route of the class.
//   - TestOrgIsolationCoverage_EveryOrgChildRouteIsSwept walks the real router
//     and FAILS when a route containing {org_id} plus another path parameter is
//     neither in sweepRows nor in orgIsolationAuditedSafe below.
//   - orgIsolationAuditedSafe lists the routes audited safe, grouped by the
//     package that guards them. An entry is a promise that the handler checks
//     the child's organization; say HOW in the group comment.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sweepIDs names the rows a sweep request is built from. In the attack run
// Org is the attacker's organization (B) while every other id belongs to the
// victim (A); in the control run all of them belong to Org.
type sweepIDs struct {
	Org        uuid.UUID // {org_id} of the path
	OwnVenue   uuid.UUID // a venue of Org (create-session body)
	EventOther uuid.UUID // an event of Org (event/session mismatch rows)

	Event    uuid.UUID
	Session1 uuid.UUID // GA session: ledger, tier, places, allocation, issuance
	Session2 uuid.UUID // empty GA session: seating bind target
	Session3 uuid.UUID // seated session: block/unblock target
	Session4 uuid.UUID // CRUD session with a paid order and a contact e-mail
	Tier     uuid.UUID
	Channel  uuid.UUID

	FeedToken   uuid.UUID
	Alloc       uuid.UUID
	Issuance    uuid.UUID
	PlanVersion uuid.UUID
	PartnerOrg  uuid.UUID // partner of Alloc
}

// sweepRow is one hole x one method.
type sweepRow struct {
	hole  string // H3..H8, SUSPECT
	name  string
	route string // "METHOD /v1/..." exactly as the router registers it
	perm  string // permission the route's gate asks for
	path  func(id sweepIDs) string
	body  func(id sweepIDs) string
	// attackOnly rows have no meaningful control run (they mix the caller's
	// event with the victim's session).
	attackOnly bool
	// controlStatus is the status the control run must NOT be refused with;
	// zero means "any 2xx".
	controlStatus int
}

func orgPath(id sweepIDs) string { return "/v1/organizations/" + id.Org.String() }

func sessionBase(id sweepIDs, event, session uuid.UUID) string {
	return orgPath(id) + "/events/" + event.String() + "/sessions/" + session.String()
}

func qtyBody(_ sweepIDs) string { return `{"quantity":5}` }

func futureTime(hours int) string {
	return time.Now().UTC().Add(time.Duration(hours) * time.Hour).Truncate(time.Hour).Format(time.RFC3339)
}

// sweepRows is the table. Order matters only for readability.
func sweepRows() []sweepRow {
	const inv = "/v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/inventory"
	rows := []sweepRow{
		// ── H3 feed tokens: {channel_id} is not tied to {org_id}; the raw token is returned.
		{hole: "H3", name: "feed tokens list", route: "GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens", perm: "feed_token.read",
			path: func(id sweepIDs) string { return orgPath(id) + "/channels/" + id.Channel.String() + "/feed-tokens" }},
		{hole: "H3", name: "feed token mint", route: "POST /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens", perm: "feed_token.create",
			path:          func(id sweepIDs) string { return orgPath(id) + "/channels/" + id.Channel.String() + "/feed-tokens" },
			body:          func(_ sweepIDs) string { return `{"label":"sweep"}` },
			controlStatus: 201},
		{hole: "H3", name: "feed token get", route: "GET /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", perm: "feed_token.read",
			path: func(id sweepIDs) string {
				return orgPath(id) + "/channels/" + id.Channel.String() + "/feed-tokens/" + id.FeedToken.String()
			}},
		{hole: "H3", name: "feed token revoke", route: "DELETE /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens/{id}", perm: "feed_token.delete",
			path: func(id sweepIDs) string {
				return orgPath(id) + "/channels/" + id.Channel.String() + "/feed-tokens/" + id.FeedToken.String()
			},
			controlStatus: 200},

		// ── H4 inventory ledger: {event_id}/{session_id} are not tied to {org_id}.
		{hole: "H4", name: "inventory list", route: "GET " + inv, perm: "inventory.read",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session1) + "/inventory" }},
		{hole: "H4", name: "inventory init", route: "POST " + inv, perm: "inventory.reserve",
			path:          func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session2) + "/inventory" },
			body:          func(_ sweepIDs) string { return `{"capacity_total":1}` },
			controlStatus: 201},
		{hole: "H4", name: "inventory reserve", route: "POST " + inv + "/reserve", perm: "inventory.reserve",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session1) + "/inventory/reserve" }, body: qtyBody},
		{hole: "H4", name: "inventory release", route: "POST " + inv + "/release", perm: "inventory.release",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session1) + "/inventory/release" }, body: qtyBody},
		{hole: "H4", name: "inventory confirm", route: "POST " + inv + "/confirm", perm: "inventory.confirm",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session1) + "/inventory/confirm" }, body: qtyBody},
		// The same five with the caller's OWN event and the victim's session: the
		// session must belong to the path event, not only to the path organization.
		{hole: "H4", name: "inventory list (own event, foreign session)", route: "GET " + inv, perm: "inventory.read", attackOnly: true,
			path: func(id sweepIDs) string { return sessionBase(id, id.EventOther, id.Session1) + "/inventory" }},
		{hole: "H4", name: "inventory reserve (own event, foreign session)", route: "POST " + inv + "/reserve", perm: "inventory.reserve", attackOnly: true,
			path: func(id sweepIDs) string { return sessionBase(id, id.EventOther, id.Session1) + "/inventory/reserve" }, body: qtyBody},
		{hole: "H4", name: "inventory release (own event, foreign session)", route: "POST " + inv + "/release", perm: "inventory.release", attackOnly: true,
			path: func(id sweepIDs) string { return sessionBase(id, id.EventOther, id.Session1) + "/inventory/release" }, body: qtyBody},
		{hole: "H4", name: "inventory confirm (own event, foreign session)", route: "POST " + inv + "/confirm", perm: "inventory.confirm", attackOnly: true,
			path: func(id sweepIDs) string { return sessionBase(id, id.EventOther, id.Session1) + "/inventory/confirm" }, body: qtyBody},

		// ── H5 external allocations: {org_id} is the PARTNER; session/tier come from the body.
		{hole: "H5", name: "allocation get", route: "GET /v1/organizations/{org_id}/external-allocations/{id}", perm: "allocation.read",
			path: func(id sweepIDs) string { return orgPath(id) + "/external-allocations/" + id.Alloc.String() }},
		{hole: "H5", name: "allocation patch", route: "PATCH /v1/organizations/{org_id}/external-allocations/{id}", perm: "allocation.update",
			path: func(id sweepIDs) string { return orgPath(id) + "/external-allocations/" + id.Alloc.String() },
			body: func(_ sweepIDs) string { return `{"status":"active"}` }},
		{hole: "H5", name: "allocation create (no category)", route: "POST /v1/organizations/{org_id}/external-allocations", perm: "allocation.create",
			path: func(id sweepIDs) string { return orgPath(id) + "/external-allocations" },
			body: func(id sweepIDs) string {
				return fmt.Sprintf(`{"session_id":%q,"quota_qty":3,"status":"active"}`, id.Session1)
			},
			controlStatus: 201},
		{hole: "H5", name: "allocation create (category)", route: "POST /v1/organizations/{org_id}/external-allocations", perm: "allocation.create",
			path: func(id sweepIDs) string { return orgPath(id) + "/external-allocations" },
			body: func(id sweepIDs) string {
				return fmt.Sprintf(`{"session_id":%q,"tier_id":%q,"quota_qty":3,"status":"active"}`, id.Session1, id.Tier)
			},
			controlStatus: 201},

		// ── H6 complimentary tickets: session/tier come from the body; the get ignores {org_id}.
		{hole: "H6", name: "complimentary issue", route: "POST /v1/organizations/{org_id}/complimentary", perm: "complimentary.issue",
			path: func(id sweepIDs) string { return orgPath(id) + "/complimentary" },
			body: func(id sweepIDs) string {
				return fmt.Sprintf(`{"session_id":%q,"tier_id":%q,"qty":1,"recipients":["sweep-victim@example.test"],"batch_id":%q}`,
					id.Session1, id.Tier, "sweep-"+uuid.NewString())
			},
			controlStatus: 201},
		{hole: "H6", name: "complimentary get", route: "GET /v1/organizations/{org_id}/complimentary/{id}", perm: "complimentary.read",
			path: func(id sweepIDs) string { return orgPath(id) + "/complimentary/" + id.Issuance.String() }},

		// ── H7 session CRUD: {event_id} is not tied to {org_id}.
		{hole: "H7", name: "session list", route: "GET /v1/organizations/{org_id}/events/{event_id}/sessions", perm: "session.read",
			path: func(id sweepIDs) string { return orgPath(id) + "/events/" + id.Event.String() + "/sessions" }},
		{hole: "H7", name: "session get", route: "GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}", perm: "session.read",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session4) }},
		{hole: "H7", name: "session create", route: "POST /v1/organizations/{org_id}/events/{event_id}/sessions", perm: "session.create",
			path: func(id sweepIDs) string { return orgPath(id) + "/events/" + id.Event.String() + "/sessions" },
			body: func(id sweepIDs) string {
				return fmt.Sprintf(`{"venue_id":%q,"start_at":%q,"end_at":%q,"status":"draft","currency":"EUR","capacity_override":10}`,
					id.OwnVenue, futureTime(24*40), futureTime(24*40+2))
			},
			controlStatus: 201},
		{hole: "H7", name: "session cancel (patch)", route: "PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}", perm: "session.update",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session4) },
			body: func(_ sweepIDs) string { return `{"status":"cancelled"}` }},
		{hole: "H7", name: "session move (patch)", route: "PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}", perm: "session.update",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session4) },
			body: func(_ sweepIDs) string {
				return fmt.Sprintf(`{"start_at":%q,"end_at":%q}`, futureTime(24*50), futureTime(24*50+2))
			}},
		{hole: "H7", name: "session delete", route: "DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{id}", perm: "session.delete",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session4) }},
		// Control for the pre-existing event scoping of the get: the caller's own event
		// with the victim's session was already a 404 and must stay one.
		{hole: "H7", name: "session get (own event, foreign session)", route: "GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}", perm: "session.read", attackOnly: true,
			path: func(id sweepIDs) string { return sessionBase(id, id.EventOther, id.Session4) }},

		// ── H8 seating bind and seat block/unblock: only the path organization was checked.
		{hole: "H8", name: "seating bind", route: "POST /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seating", perm: "event_session.assign_seating_plan",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session2) + "/seating" },
			body: func(id sweepIDs) string {
				return fmt.Sprintf(`{"seating_plan_version_id":%q,"admission_mode":"general_admission","category_tier_map":{},"auto_create_tiers":true}`, id.PlanVersion)
			}},
		{hole: "H8", name: "seat block", route: "PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/seats", perm: "event_session.assign_seating_plan",
			path: func(id sweepIDs) string { return sessionBase(id, id.Event, id.Session3) + "/seats" },
			body: func(_ sweepIDs) string { return `{"action":"block","seat_keys":["S|A|1","S|A|2"]}` }},
	}
	return rows
}

// sweepRouteSet is the set of "METHOD route" keys of sweepRows.
func sweepRouteSet() map[string]bool {
	out := map[string]bool{}
	for _, r := range sweepRows() {
		out[r.route] = true
	}
	return out
}

// orgIsolationAuditedSafe lists the routes of the class that were READ and found
// to verify the child's organization (audit of master 4fbf2ff, 2026-10-10). Each
// group says which guard does it. A new route does NOT belong here unless its
// handler really loads the child and compares its organization to {org_id}; if
// in doubt add a sweepRows entry instead — a failing row is cheaper than a leak.
var orgIsolationAuditedSafe = map[string]string{
	// hcatalog tiers, price schedules, bulk pricing: requireSessionInOrg
	// (hcatalog/tier_org.go, EC-15) and requireEventInOrg; tier_org_isolation_integration_test.go.
	"DELETE /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}":             "tiers",
	"GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers":                     "tiers",
	"GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}":                "tiers",
	"GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule": "tiers",
	"PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}":              "tiers",
	"POST /v1/organizations/{org_id}/events/{event_id}/sessions/pricing-bulk":                          "tiers",
	"POST /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers":                    "tiers",
	"PUT /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule": "tiers",

	// hcatalog event gallery and videos (task 1-2a, event_materials.go):
	// materialsPrep runs requireEventInOrg first and every query is scoped by
	// (id, event_id). event_materials_integration_test.go drives a foreign event.
	"DELETE /v1/organizations/{org_id}/events/{event_id}/media-items/{item_id}": "event materials",
	"DELETE /v1/organizations/{org_id}/events/{event_id}/videos/{video_id}":     "event materials",
	"GET /v1/organizations/{org_id}/events/{event_id}/media-items":              "event materials",
	"GET /v1/organizations/{org_id}/events/{event_id}/videos":                   "event materials",
	"PATCH /v1/organizations/{org_id}/events/{event_id}/media-items/{item_id}":  "event materials",
	"POST /v1/organizations/{org_id}/events/{event_id}/media-items":             "event materials",
	"POST /v1/organizations/{org_id}/events/{event_id}/videos":                  "event materials",
	"PUT /v1/organizations/{org_id}/events/{event_id}/media-items/order":        "event materials",
	"PUT /v1/organizations/{org_id}/events/{event_id}/videos/order":             "event materials",

	// hcatalog events, artists, contact, promoter link, status, delete impact:
	// requireEventInOrg or a query scoped by (id, org_id).
	"DELETE /v1/organizations/{org_id}/events/{id}":                  "events",
	"DELETE /v1/organizations/{org_id}/events/{id}/artists/{aid}":    "events",
	"GET /v1/organizations/{org_id}/events/{event_id}/delete-impact": "events",
	// himports.HandleEventBundleRead (task 3-2): GetEventRaw, then the event's
	// organization must equal the path organization (404 event.not_found).
	// TestEventBundleRead_OrganizationBoundary drives a foreign, unknown and
	// deleted event and a foreign organization.
	"GET /v1/organizations/{org_id}/events/{event_id}/event-bundle":              "himports",
	"GET /v1/organizations/{org_id}/events/{id}/artists":                         "events",
	"PATCH /v1/organizations/{org_id}/events/{id}":                               "events",
	"PATCH /v1/organizations/{org_id}/events/{id}/artists/{aid}":                 "events",
	"POST /v1/organizations/{org_id}/events/{id}/artists":                        "events",
	"POST /v1/organizations/{org_id}/events/{id}/status":                         "events",
	"PUT /v1/organizations/{org_id}/events/{event_id}/contact":                   "events",
	"PUT /v1/organizations/{org_id}/events/{id}/promoter":                        "events",
	"PATCH /v1/organizations/{org_id}/promoters/{id}":                            "promoters",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/change-impact":         "events (requireEventInOrg on the session's event)",
	"GET /v1/organizations/{org_id}/events/{event_id}/sessions/{id}/macs-export": "macs_export.go checks the session against event AND organization",

	// hmaterials.ExtrasHandler (task 1-2b): every handler starts with scope(),
	// which loads the event and answers 404 event.not_found unless its
	// organization is the path organization; the child rows (link, photo) are
	// then read and written by (id, event_id), and a photo's media_id must be an
	// event_report object of the path organization. Covered by
	// TestEventMaterialsExtras_StayInsideTheOrganization (all routes, as an org-B owner).
	"GET /v1/organizations/{org_id}/events/{event_id}/partner-links":              "hmaterials",
	"POST /v1/organizations/{org_id}/events/{event_id}/partner-links":             "hmaterials",
	"PUT /v1/organizations/{org_id}/events/{event_id}/partner-links/order":        "hmaterials",
	"PATCH /v1/organizations/{org_id}/events/{event_id}/partner-links/{link_id}":  "hmaterials",
	"DELETE /v1/organizations/{org_id}/events/{event_id}/partner-links/{link_id}": "hmaterials",
	"GET /v1/organizations/{org_id}/events/{event_id}/map-point":                  "hmaterials",
	"PUT /v1/organizations/{org_id}/events/{event_id}/map-point":                  "hmaterials",
	"DELETE /v1/organizations/{org_id}/events/{event_id}/map-point":               "hmaterials",
	"GET /v1/organizations/{org_id}/events/{event_id}/photo-report":               "hmaterials",
	"POST /v1/organizations/{org_id}/events/{event_id}/photo-report":              "hmaterials",
	"PUT /v1/organizations/{org_id}/events/{event_id}/photo-report/order":         "hmaterials",
	"PATCH /v1/organizations/{org_id}/events/{event_id}/photo-report/{photo_id}":  "hmaterials",
	"DELETE /v1/organizations/{org_id}/events/{event_id}/photo-report/{photo_id}": "hmaterials",
	// Task 1-2c: HandleCopyMaterials starts with scope() (the target event of
	// the path organization) and then requires the SOURCE event to be a live
	// event of the same organization (404 event.not_found otherwise); the
	// copy statements filter the source rows by org_id too.
	// TestEventMaterialsCopy_StaysInsideTheOrganization drives both sides.
	"POST /v1/organizations/{org_id}/events/{event_id}/materials/copy-from/{source_event_id}": "hmaterials",

	// hcatalog channels CRUD, gateway credential, WordPress webhook: queries scoped by (id, org_id).
	"DELETE /v1/organizations/{org_id}/channels/{id}":                    "channels",
	"DELETE /v1/organizations/{org_id}/channels/{id}/gateway-credential": "channels",
	"DELETE /v1/organizations/{org_id}/channels/{id}/wp-webhook":         "channels",
	"GET /v1/organizations/{org_id}/channels/{id}":                       "channels",
	"GET /v1/organizations/{org_id}/channels/{id}/gateway-credential":    "channels",
	"GET /v1/organizations/{org_id}/channels/{id}/wp-webhook":            "channels",
	"PATCH /v1/organizations/{org_id}/channels/{id}":                     "channels",
	"PUT /v1/organizations/{org_id}/channels/{id}/gateway-credential":    "channels",
	"PUT /v1/organizations/{org_id}/channels/{id}/wp-webhook":            "channels",
	// hseller: the channel is looked up among the PATH organization's hosted-page
	// channels only (covered by TestSellerProfile_StaysInsideTheOrganization).
	"PATCH /v1/organizations/{org_id}/channels/{channel_id}/seller-display": "hseller",

	// hcatalog venues: update/delete scoped by (id, org_id).
	"DELETE /v1/organizations/{org_id}/venues/{id}": "venues",
	"PATCH /v1/organizations/{org_id}/venues/{id}":  "venues",

	// horders, hcustomers, hexport, hreports: queries scoped by org_id.
	"GET /v1/organizations/{org_id}/customers/{id}":                             "hcustomers",
	"GET /v1/organizations/{org_id}/events/{event_id}/sales.csv":                "hexport",
	"GET /v1/organizations/{org_id}/events/{event_id}/summary":                  "horders",
	"GET /v1/organizations/{org_id}/orders/{id}":                                "horders",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/sales.csv":            "hexport",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/summary":              "horders",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/summary.csv":          "horders",
	"POST /v1/organizations/{org_id}/orders/{id}/cancel":                        "horders",
	"POST /v1/organizations/{org_id}/orders/{order_id}/resend-tickets":          "horders",
	"GET /v1/organizations/{org_id}/orders/{order_id}/consents":                 "horders.HandleConsents (GetOrderByID(order, path org), then the consents by (path org, the order's checkout session); covered by TestCheckoutConsent_OrderConsentsStayInsideTheOrganization)",
	"GET /v1/organizations/{org_id}/orders/{order_id}/letter-interactions":      "horders.HandleLetterInteractions (GetOrderByID(order, path org), then the letters by (order, path org); covered by TestBrevoWebhook_LetterInteractionsCarryUserAgentAndStayInsideTheOrganization)",
	"GET /v1/organizations/{org_id}/orders/{order_id}/refund-options":           "refunds.Engine.Options (order loaded inside the path org; covered by TestRefundOrgRoutes_OptionsCreateReplayAndIsolation)",
	"POST /v1/organizations/{org_id}/orders/{order_id}/refunds":                 "refunds.Engine.CreateBatch (order loaded inside the path org; covered by TestRefundOrgRoutes_OptionsCreateReplayAndIsolation)",
	"GET /v1/organizations/{org_id}/refunds/{refund_id}":                        "refunds.Engine.Get (refund loaded inside the path org; covered by TestRefundOrgRoutes_QueueAndDetailStayInsideTheOrganization)",
	"POST /v1/organizations/{org_id}/refunds/{refund_id}/retry":                 "refunds.Engine.Retry (refund loaded inside the path org; covered by TestRefundOrgRoutes_RetryAndResolve)",
	"POST /v1/organizations/{org_id}/refunds/{refund_id}/resolve":               "refunds.Engine.Resolve (refund loaded inside the path org; covered by TestRefundOrgRoutes_RetryAndResolve)",
	"POST /v1/organizations/{org_id}/tickets/{ticket_id}/scan-refund-exception": "platform superadmin only; the ticket must belong to the path org, checked inside refunds.Engine.GrantScanException (covered by TestScanException_SuperadminOnlyInsideTheOrganization)",
	"POST /v1/organizations/{org_id}/tickets/{ticket_id}/review-hold/clear":     "platform superadmin only; the ticket's organization (GetTicketOrgID) must be the path org (covered by TestReviewHoldClear_SuperadminOnlyInsideTheOrganization)",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/sample-ticket":        "hsample",

	// S1c seat batches: seatBatchShim runs requireSessionInPathOrg, and
	// seatlayout / the batch queries are scoped by (session, path org)
	// again (covered by TestSeatBatches_StayInsideTheOrganization).
	"POST /v1/organizations/{org_id}/sessions/{session_id}/seat-batches":                   "hseating",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/seat-batches/{batch_id}":         "hseating",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/seat-batches":                    "hseating",
	"POST /v1/organizations/{org_id}/sessions/{session_id}/seat-batches/{batch_id}/revert": "hseating",
	"GET /v1/organizations/{org_id}/sessions/{session_id}/seats/{seat_key}/history":        "hseating",
	"POST /v1/organizations/{org_id}/media/{media_id}/poster-facts":                        "hmedia.PosterFacts (rowOrgAccess, then the object must carry the path org; covered by TestPosterFacts_RouteReadsOwnPosterAndKeepsOrganizationsApart)",

	// hcheckout promo codes, hpayments payment configs, hbankaccounts, hapikeys,
	// hbot invitations: queries scoped by (id, org_id).
	"DELETE /v1/organizations/{org_id}/api-keys/{id}":                            "hapikeys",
	"DELETE /v1/organizations/{org_id}/bank-accounts/{id}":                       "hbankaccounts",
	"DELETE /v1/organizations/{org_id}/bot-invitations/{id}":                     "hbot",
	"DELETE /v1/organizations/{org_id}/payment-configs/{id}":                     "hpayments",
	"DELETE /v1/organizations/{org_id}/promo-codes/{id}":                         "promo",
	"GET /v1/organizations/{org_id}/payment-configs/{id}":                        "hpayments",
	"GET /v1/organizations/{org_id}/promo-codes/{id}":                            "promo",
	"GET /v1/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv": "promo",
	"PATCH /v1/organizations/{org_id}/bank-accounts/{id}":                        "hbankaccounts",
	"PATCH /v1/organizations/{org_id}/payment-configs/{id}":                      "hpayments",
	"PATCH /v1/organizations/{org_id}/promo-codes/{id}":                          "promo",
	"POST /v1/organizations/{org_id}/bot-invitations/{id}/resend":                "hbot",
	"POST /v1/organizations/{org_id}/payment-configs/{id}/verify":                "hpayments",

	// hiam membership routes: the {org_id} is ignored by the handlers (H1/H2 of the
	// audit). Owned by SEC-1, covered by its memberships_cross_org_integration_test.go.
	"DELETE /v1/admin/organizations/{org_id}/members/{membership_id}": "SEC-1",
	"DELETE /v1/organizations/{org_id}/members/{user_id}":             "SEC-1",
	"PATCH /v1/admin/organizations/{org_id}/members/{membership_id}":  "SEC-1",
}

var orgIsolationParam = regexp.MustCompile(`\{[a-z_]+\}`)

// TestOrgIsolationCoverage_EveryOrgChildRouteIsSwept fails when a route that has
// {org_id} plus another path parameter is in neither sweepRows nor the audited
// safe list — the next route of the class cannot ship unguarded — and when an
// entry of either list names a route that no longer exists.
func TestOrgIsolationCoverage_EveryOrgChildRouteIsSwept(t *testing.T) {
	s := buildDriftTestServer(t)
	routes := chiRouteSet(t, s.router)

	registered := map[string]bool{}
	var unlisted []string
	swept := sweepRouteSet()
	for path, methods := range routes {
		if !strings.Contains(path, "{org_id}") {
			continue
		}
		child := false
		for _, p := range orgIsolationParam.FindAllString(path, -1) {
			if p != "{org_id}" {
				child = true
			}
		}
		for method := range methods {
			key := strings.ToUpper(method) + " " + path
			registered[key] = true
			if !child {
				continue
			}
			if swept[key] {
				continue
			}
			if _, ok := orgIsolationAuditedSafe[key]; ok {
				continue
			}
			unlisted = append(unlisted, key)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("%d route(s) with {org_id} plus a child id are not covered by the organization isolation sweep.\n"+
			"A route like this is gated by a permission and by membership of {org_id} only: the handler must\n"+
			"load the child (event, session, channel, token, ...) and answer the route's own 404 when it belongs\n"+
			"to another organization. Add a row to sweepRows (org_isolation_sweep_table_test.go) and fix the\n"+
			"handler, or, if you READ the handler and it already checks the child's organization, list the route\n"+
			"in orgIsolationAuditedSafe with the guard's name:\n  %s", len(unlisted), strings.Join(unlisted, "\n  "))
	}

	for key := range swept {
		if !registered[key] {
			t.Errorf("sweepRows names %q, which is not a registered route (renamed or removed?)", key)
		}
	}
	for key := range orgIsolationAuditedSafe {
		if !registered[key] {
			t.Errorf("orgIsolationAuditedSafe names %q, which is not a registered route (renamed or removed?)", key)
		}
	}
}
