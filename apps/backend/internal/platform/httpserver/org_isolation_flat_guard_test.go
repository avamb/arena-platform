package httpserver

// org_isolation_flat_guard_test.go — SEC-3: the class "a route without
// {org_id} in its path must still keep the caller inside their organization".
//
// SEC-2 (org_isolation_sweep_table_test.go) walks the routes WITH {org_id}.
// This guard walks the rest. Every registered route outside /v1/organizations/
// {org_id}/... must be named by exactly one entry of flatRouteFamilies, and each
// entry states HOW the route stays inside an organization (or why it has no
// organization data at all). A new flat route that no entry covers fails the
// test: the author must read the handler, give it a guard that loads the row
// and answers the route's own 404 for a foreign one (Server.rowOrgAccess,
// orgread, or a membership check on the row's organization), and then add
// the route to the table with the name of that guard.
//
// The table is the index, the integration tests named in the notes are the
// proof: org_isolation_sweep_integration_test.go (SEC-2),
// org_isolation_flat_sweep_integration_test.go, org_isolation_checkout_
// integration_test.go, org_isolation_media_integration_test.go,
// org_isolation_billing_integration_test.go and the other org_isolation_*
// files.

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// flatHow says how a family of routes is kept inside an organization.
type flatHow string

const (
	// flatPublic: reachable without a login by design; the credential is a
	// token in the URL, a provider signature or no credential at all.
	flatPublic flatHow = "public"
	// flatOwnAccount: acts on the caller's own account only.
	flatOwnAccount flatHow = "own account"
	// flatPlatform: gated by a platform-level permission that no organization
	// role or organization API key can hold (superadmin, operator, service token).
	flatPlatform flatHow = "platform only"
	// flatRowGuard: loads the row, then asks the organization guard
	// (Server.rowOrgAccess, orgread or a membership check) and answers the
	// route's own 404 for a foreign row.
	flatRowGuard flatHow = "row guard"
	// flatSharedRead: a catalog that is shared for reading by design; the parts
	// that are not shared are removed from the answer.
	flatSharedRead flatHow = "shared read"
	// flatNoOrgData: touches no organization data.
	flatNoOrgData flatHow = "no organization data"
	// flatScannerFamily: the barcode inventory and door-scanner routes, which
	// are reviewed together with the scanner integration.
	flatScannerFamily flatHow = "scanner family"
)

type flatFamily struct {
	// method is an upper-case HTTP method, or "*" for every method.
	method string
	// path is a chi route with parameter names; a trailing "*" matches the
	// prefix. Parameter names are ignored when matching.
	path string
	how  flatHow
	// note names the guard or the credential.
	note string
}

// flatRouteFamilies is matched in order, the first entry that fits wins, so a
// specific route sits above the family it is carved out of.
var flatRouteFamilies = []flatFamily{
	// ── operations and diagnostics ──────────────────────────────────────────
	{"GET", "/healthz", flatPublic, "liveness probe"},
	{"GET", "/readyz", flatPublic, "readiness probe"},
	{"GET", "/metrics", flatPlatform, "scrape token"},
	{"*", "/v1/info", flatPublic, "build information"},
	{"*", "/v1/info-slow", flatPublic, "graceful-shutdown probe"},
	{"*", "/v1/server-info", flatPublic, "build information"},
	{"*", "/v1/echo", flatPublic, "diagnostic echo"},
	{"*", "/v1/dev/*", flatPublic, "development token helpers"},
	{"*", "/compat/bil24/*", flatPublic, "gateway token of the sales channel"},

	// ── sign-in, own account ────────────────────────────────────────────────
	{"*", "/v1/auth/*", flatPublic, "credentials in the request body"},
	{"*", "/v1/me", flatOwnAccount, "the caller's own profile"},
	{"*", "/v1/me/*", flatOwnAccount, "the caller's own data requests"},

	// ── anonymous buyer and widget flows ────────────────────────────────────
	{"*", "/v1/public/*", flatPublic, "feed token or checkout token in the URL"},
	{"GET", "/v1/feeds/{token}", flatPublic, "feed token in the URL"},
	{"GET", "/v1/event-sessions/{id}/schema", flatPublic, "published sessions only"},
	{"GET", "/v1/event-sessions/{id}/seat-status", flatPublic, "published sessions only"},
	{"GET", "/v1/event-sessions/{id}/layout.svg", flatPublic, "published sessions only"},
	{"GET", "/v1/media-files/{id}", flatPublic, "signed URL"},
	{"GET", "/v1/geo/countries", flatPublic, "reference data"},
	{"GET", "/v1/geo/cities", flatPublic, "reference data"},
	{"*", "/v1/onboarding/*", flatPublic, "applicant access token"},
	{"GET", "/v1/billing/tariffs/active", flatPublic, "published tariff"},

	// ── provider callbacks ──────────────────────────────────────────────────
	{"POST", "/v1/payment-intents/webhook", flatPublic, "provider signature"},
	{"POST", "/v1/payment-intents/webhook/{config_id}", flatPublic, "provider signature of that configuration"},
	{"POST", "/v1/refunds/webhook", flatPublic, "provider signature"},
	{"POST", "/v1/billing/stripe/webhook", flatPublic, "provider signature"},

	// ── platform operators, service tokens ──────────────────────────────────
	{"GET", "/v1/admin/tickets", flatPlatform, "superadmin.read"},
	{"*", "/v1/admin/tickets/*", flatRowGuard, "htickets.RowOrgAccess"},
	{"*", "/v1/admin/*", flatPlatform, "superadmin or platform permission"},
	{"*", "/v1/operator-networks", flatPlatform, "network.* permissions"},
	{"*", "/v1/operator-networks/*", flatPlatform, "network.* permissions"},
	{"*", "/v1/bot/*", flatPlatform, "service token of the Telegram bot"},
	{"*", "/v1/webhooks/subscribers", flatPlatform, "requirePlatformSuperadmin"},
	{"*", "/v1/webhooks/subscribers/*", flatPlatform, "requirePlatformSuperadmin"},
	{"GET", "/v1/reconciliation/exceptions", flatPlatform, "reconciliation.review"},
	{"PATCH", "/v1/reconciliation/reports/{id}/review", flatPlatform, "reconciliation.review"},
	{"PATCH", "/v1/reconciliation/reports/{id}/lines/{line_id}", flatPlatform, "reconciliation.review"},
	{"POST", "/v1/billing/tariffs", flatPlatform, "billing.admin"},
	{"POST", "/v1/billing/invoices/generate", flatPlatform, "billing.admin"},
	{"POST", "/v1/barcode-batches/{id}/approve", flatPlatform, "barcode_batch.approve"},
	{"POST", "/v1/barcode-batches/{id}/reject", flatPlatform, "barcode_batch.approve"},

	// ── rows of one organization behind a row guard ─────────────────────────
	{"GET", "/v1/checkout/{id}/tickets", flatRowGuard, "htickets.RowOrgAccess"},
	{"GET", "/v1/tickets/{id}/credential", flatRowGuard, "htickets.RowOrgAccess"},
	{"POST", "/v1/tickets/{id}/cancel", flatRowGuard, "rowOrgAccess on the ticket's own organization"},
	{"POST", "/v1/complimentary/{id}/revoke", flatRowGuard, "enforceMembershipInOrg on the issuance"},
	{"*", "/v1/checkout/*", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/reservations", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/reservations/*", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/refunds", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/refunds/*", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/payment-intents", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/payment-intents/*", flatRowGuard, "hcheckout.RowOrgAccess"},
	{"*", "/v1/media", flatRowGuard, "hmedia.RowOrgAccess"},
	{"*", "/v1/media/*", flatRowGuard, "hmedia.RowOrgAccess"},
	{"*", "/v1/billing/invoices/*", flatRowGuard, "hbilling.RowOrgAccess"},
	{"*", "/v1/billing/stripe/*", flatRowGuard, "hbilling.RowOrgAccess"},
	{"GET", "/v1/reconciliation/reports/{id}", flatRowGuard, "reportReadable (partner or session organization)"},
	{"POST", "/v1/reconciliation/reports", flatRowGuard, "membership of the allocation's organizations"},
	{"*", "/v1/events", flatRowGuard, "orgread"},
	{"*", "/v1/events/*", flatRowGuard, "orgread, requireEventInOrg"},
	{"*", "/v1/organizations/{id}", flatRowGuard, "membership authority on the path organization"},
	{"*", "/v1/sessions/{id}/media", flatRowGuard, "requireOrgMembership on the session's organization"},
	{"*", "/v1/event-sessions/{id}/seats/*", flatRowGuard, "membership on the session's organization"},
	{"POST", "/v1/venues/{venue_id}/seating-plans", flatRowGuard, "membership of the owner organization"},
	{"PATCH", "/v1/seating-plans/{id}", flatRowGuard, "membership of the owner organization"},
	{"POST", "/v1/seating-plans/{id}/versions", flatRowGuard, "membership of the owner organization"},
	{"POST", "/v1/seating-plans/{id}/fork", flatRowGuard, "membership of the target organization"},

	// ── catalog shared for reading ──────────────────────────────────────────
	{"GET", "/v1/venues", flatSharedRead, "contacts of other organizations removed"},
	{"GET", "/v1/venues/{id}", flatSharedRead, "contacts of other organizations removed"},
	{"GET", "/v1/venues/{venue_id}/seating-plans", flatSharedRead, "plans are shared for reading"},
	{"GET", "/v1/seating-plans/{id}", flatSharedRead, "plans are shared for reading"},
	{"GET", "/v1/seating-plans/{id}/versions", flatSharedRead, "plans are shared for reading"},
	{"GET", "/v1/seating-plans/{id}/versions/{n}", flatSharedRead, "plans are shared for reading"},

	// ── nothing of an organization ──────────────────────────────────────────
	{"POST", "/v1/organizations", flatOwnAccount, "creates a new organization, touches no existing one"},
	{"GET", "/v1/organizations", flatRowGuard, "lists the caller's memberships only"},
	{"*", "/v1/stripe/connect/*", flatNoOrgData, "OAuth redirect helpers, nothing is stored"},

	// ── barcode inventory and door scanner ──────────────────────────────────
	{"*", "/v1/barcodes", flatScannerFamily, "barcode and scanner family"},
	{"*", "/v1/barcodes/*", flatScannerFamily, "barcode and scanner family"},
	{"*", "/v1/barcode-batches", flatScannerFamily, "barcode and scanner family"},
	{"*", "/v1/barcode-batches/*", flatScannerFamily, "barcode and scanner family"},
	{"*", "/v1/scan", flatScannerFamily, "barcode and scanner family"},
	{"*", "/v1/scanner/*", flatScannerFamily, "barcode and scanner family"},
}

var flatParamName = regexp.MustCompile(`\{[^}/]*\}`)

func flatNormalize(path string) string {
	return flatParamName.ReplaceAllString(path, "{}")
}

func (f flatFamily) matches(method, path string) bool {
	if f.method != "*" && f.method != method {
		return false
	}
	want := flatNormalize(f.path)
	if strings.HasSuffix(want, "/*") {
		prefix := strings.TrimSuffix(want, "*")
		return strings.HasPrefix(path, prefix)
	}
	return want == path
}

// TestOrgIsolationFlatRoutes_EveryFlatRouteIsClassified fails when a route
// without {org_id} is not named by flatRouteFamilies, and when an entry names
// nothing (renamed or removed route).
func TestOrgIsolationFlatRoutes_EveryFlatRouteIsClassified(t *testing.T) {
	s := buildDriftTestServer(t)
	routes := chiRouteSet(t, s.router)

	used := make([]bool, len(flatRouteFamilies))
	var unclassified []string
	for path, methods := range routes {
		if strings.Contains(path, "{org_id}") {
			continue
		}
		norm := flatNormalize(path)
		for method := range methods {
			method = strings.ToUpper(method)
			found := false
			for i, fam := range flatRouteFamilies {
				if fam.matches(method, norm) {
					used[i] = true
					found = true
					break
				}
			}
			if !found {
				unclassified = append(unclassified, method+" "+path)
			}
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("%d route(s) without {org_id} are not classified in flatRouteFamilies.\n"+
			"Such a route is gated by a permission only: its handler must load the row it acts on and\n"+
			"answer the route's own 404 when the row belongs to another organization (Server.rowOrgAccess,\n"+
			"orgread, or a membership check on the row's organization). Read the handler, add the guard, add a\n"+
			"case to the matching org_isolation_*_integration_test.go, then add the route here:\n  %s",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}
	for i, fam := range flatRouteFamilies {
		if !used[i] {
			t.Errorf("flatRouteFamilies entry %q %q matches no registered route (renamed or removed?)", fam.method, fam.path)
		}
		if fam.how == "" || strings.TrimSpace(fam.note) == "" {
			t.Errorf("flatRouteFamilies entry %q %q needs a how and a note", fam.method, fam.path)
		}
	}
}
