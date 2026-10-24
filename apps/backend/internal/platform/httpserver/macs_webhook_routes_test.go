// macs_webhook_routes_test.go — the MACS subscriber routes are managed by a
// platform superadmin only: the server POSTs ticket data to the registered
// URL. An ordinary org member (owner, manager, API key) gets 403
// webhook.superadmin_only; a superadmin still needs X-Admin-Reason on PUT and
// DELETE.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func macsWebhookPath(orgID uuid.UUID) string {
	return "/v1/organizations/" + orgID.String() + "/macs-webhook"
}

func TestMACSWebhookRoutes_OrgMemberWhoIsNotSuperadminGets403(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookServer(t) // admits the actor as a member of any org
	token := mintJWT(t, s.stub, wpWebhookTestActorID)
	p := macsWebhookPath(uuid.New())
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, p, strings.NewReader(`{"callback_url":"https://site.example/api/_wh/tickets"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Admin-Reason", "test")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "webhook.superadmin_only") {
			t.Errorf("%s %s = %d %s, want 403 webhook.superadmin_only", m, p, w.Code, w.Body.String())
		}
	}
}

func TestMACSWebhookRoutes_SuperadminNeedsAReasonToChange(t *testing.T) {
	t.Parallel()
	s := buildWPWebhookServer(t)
	token := mintWPSuperadminJWT(t, s)
	p := macsWebhookPath(uuid.New())
	for _, m := range []string{http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, p, strings.NewReader(`{"callback_url":"https://site.example/api/_wh/tickets"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "superadmin.missing_reason") {
			t.Errorf("%s %s = %d %s, want 400 superadmin.missing_reason", m, p, w.Code, w.Body.String())
		}
	}
}
