// verify_test.go — unit coverage of the seating plan verification rules
// (spec 08_architecture/64 section 3.4): the pure decisions and the gate
// branches that answer before any query.
package hseating

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestSeatingPlanVerify_DecideVisibilityChange(t *testing.T) {
	cases := []struct {
		current, asked string
		want           VisibilityChange
	}{
		{"", "private", VisibilityChangeAllowed},
		{"", "public_template", VisibilityChangeAllowed},
		{"", "operator_verified", VisibilityChangeVerifyRequired},
		{"private", "operator_verified", VisibilityChangeVerifyRequired},
		{"public_template", "operator_verified", VisibilityChangeVerifyRequired},
		{"private", "shared_read", VisibilityChangeAllowed},
		{"operator_verified", "operator_verified", VisibilityChangeAllowed},
		{"operator_verified", "private", VisibilityChangeLeavesTemplate},
		{"operator_verified", "public_template", VisibilityChangeTemplateLocked},
		{"operator_verified", "shared_read", VisibilityChangeTemplateLocked},
	}
	for _, c := range cases {
		if got := DecideVisibilityChange(c.current, c.asked); got != c.want {
			t.Errorf("DecideVisibilityChange(%q, %q) = %d, want %d", c.current, c.asked, got, c.want)
		}
	}
}

func TestSeatingPlanVerify_ForkableByOtherOrganization(t *testing.T) {
	cases := []struct {
		status, visibility string
		want               bool
	}{
		{"active", "operator_verified", true},
		{"active", "public_template", true},
		{"active", "shared_read", false},
		{"active", "private", false},
		{"draft", "operator_verified", false},
		{"archived", "public_template", false},
	}
	for _, c := range cases {
		if got := ForkableByOtherOrganization(c.status, c.visibility); got != c.want {
			t.Errorf("ForkableByOtherOrganization(%q, %q) = %v, want %v", c.status, c.visibility, got, c.want)
		}
	}
}

func TestSeatingPlanVerify_FilterAndNote(t *testing.T) {
	if s, ok := ParseVerificationStateFilter(""); !ok || s != "unverified" {
		t.Errorf("empty filter = %q %v, want unverified", s, ok)
	}
	for _, s := range []string{"unverified", "verified", "needs_fix"} {
		if got, ok := ParseVerificationStateFilter(s); !ok || got != s {
			t.Errorf("filter %q = %q %v", s, got, ok)
		}
	}
	if _, ok := ParseVerificationStateFilter("approved"); ok {
		t.Error("filter approved accepted")
	}
	if n, ok := NormalizeNeedsFixNote("  Row 7 has 18 seats  "); !ok || n != "Row 7 has 18 seats" {
		t.Errorf("note = %q %v", n, ok)
	}
	if _, ok := NormalizeNeedsFixNote("   "); ok {
		t.Error("blank note accepted")
	}
	if _, ok := NormalizeNeedsFixNote(strings.Repeat("я", 1000)); !ok {
		t.Error("1000-character note refused")
	}
	if _, ok := NormalizeNeedsFixNote(strings.Repeat("я", 1001)); ok {
		t.Error("1001-character note accepted")
	}
}

func newVerifyHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		queries: gen.New(unusedDBTX{t: t}),
		pool:    unusedTxStarter{t: t},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func verifyRequest(method, path, planID, body string, superadmin bool) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("plan_id", planID)
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
	if superadmin {
		ctx = auth.WithSuperadminOrgAccess(ctx)
	}
	return r.WithContext(ctx)
}

// Holding seating_plan.verify is not enough: the routes need a platform
// superadmin, and the writes need X-Admin-Reason.
func TestSeatingPlanVerify_GateAnswersBeforeTheDatabase(t *testing.T) {
	h := newVerifyHandler(t)
	plan := uuid.NewString()
	for _, c := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		body    string
	}{
		{"list", h.HandleAdminListSeatingPlans, http.MethodGet, ""},
		{"verify", h.HandleVerifySeatingPlan, http.MethodPost, ""},
		{"needs-fix", h.HandleNeedsFixSeatingPlan, http.MethodPost, `{"note":"x"}`},
	} {
		rec := httptest.NewRecorder()
		c.handler(rec, verifyRequest(c.method, "/v1/admin/seating-plans", plan, c.body, false))
		if rec.Code != http.StatusForbidden || decodeCode(t, rec.Body) != "seating_plan.verify_superadmin_only" {
			t.Errorf("%s without superadmin: %d", c.name, rec.Code)
		}
	}
	for _, c := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"verify", h.HandleVerifySeatingPlan},
		{"needs-fix", h.HandleNeedsFixSeatingPlan},
	} {
		rec := httptest.NewRecorder()
		c.handler(rec, verifyRequest(http.MethodPost, "/v1/admin/seating-plans", plan, `{"note":"x"}`, true))
		if rec.Code != http.StatusBadRequest || decodeCode(t, rec.Body) != "superadmin.missing_reason" {
			t.Errorf("%s without X-Admin-Reason: %d", c.name, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	r := verifyRequest(http.MethodPost, "/v1/admin/seating-plans", plan, `{"note":"  "}`, true)
	r.Header.Set("X-Admin-Reason", "check")
	h.HandleNeedsFixSeatingPlan(rec, r)
	if rec.Code != http.StatusBadRequest || decodeCode(t, rec.Body) != "seating_plan.invalid_note" {
		t.Errorf("needs-fix with a blank note: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	r = verifyRequest(http.MethodGet, "/v1/admin/seating-plans?verification_state=approved", plan, "", true)
	h.HandleAdminListSeatingPlans(rec, r)
	if rec.Code != http.StatusBadRequest || decodeCode(t, rec.Body) != "seating_plan.invalid_verification_state" {
		t.Errorf("list with an unknown state: %d", rec.Code)
	}
}
