package httpserver

// billing_admin_audit_guard_test.go — every WRITE route of the superadmin
// screen "Price lists and rates" (/v1/admin/billing/*, task B1-5) must have
// its audit action named here, and the integration test
// (billing_admin_integration_test.go) must see that action in audit_events
// after the call. A new write route without a line in billingAdminWriteRoutes
// turns this test red, so a route added later cannot skip the audit.

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
)

// billingAdminWriteRoutes maps "METHOD /path" to the audit action the route
// writes in the same transaction as its change.
var billingAdminWriteRoutes = map[string]string{
	"POST /v1/admin/billing/price-lists":                 hbilling.AuditPriceListCreate,
	"POST /v1/admin/billing/price-lists/{id}/revoke":     hbilling.AuditPriceListRevoke,
	"POST /v1/admin/billing/rate-overrides":              hbilling.AuditRateOverrideCreate,
	"POST /v1/admin/billing/rate-overrides/{id}/close":   hbilling.AuditRateOverrideClose,
	"DELETE /v1/admin/billing/rate-overrides/{id}":       hbilling.AuditRateOverrideDelete,
	"POST /v1/admin/billing/accounts/{account_id}/terms": hbilling.AuditAccountTermsCreate,
}

func TestBillingAdmin_EveryWriteRouteHasAnAuditAction(t *testing.T) {
	s := buildDriftTestServer(t)
	routes := chiRouteSet(t, s.router)

	seen := map[string]bool{}
	for path, methods := range routes {
		if !strings.HasPrefix(path, "/v1/admin/billing/") {
			continue
		}
		for method := range methods {
			switch strings.ToUpper(method) {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				seen[strings.ToUpper(method)+" "+path] = true
			}
		}
	}
	var missing, stale []string
	for key := range seen {
		if billingAdminWriteRoutes[key] == "" {
			missing = append(missing, key)
		}
	}
	for key := range billingAdminWriteRoutes {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("write routes of /v1/admin/billing without an audit action in billingAdminWriteRoutes: %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("billingAdminWriteRoutes names routes that are not mounted: %v", stale)
	}
	if len(seen) == 0 {
		t.Error("no write route of /v1/admin/billing is mounted")
	}
}
