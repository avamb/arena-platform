package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestRequireWebStepUp(t *testing.T) {
	t.Parallel()
	const secret = "guard-test-secret-of-32-bytes-ok!!"
	user := uuid.New()
	sid := uuid.New()
	org := uuid.NewString()
	live := map[uuid.UUID]bool{sid: true}
	var checkErr error
	s := &Server{
		cfg: &config.Config{JWTSecretStub: secret, JWTIssuer: "arena-api"},
		stepUpSessionCheck: func(_ context.Context, sessionID, userID uuid.UUID) (bool, error) {
			if checkErr != nil {
				return false, checkErr
			}
			return userID == user && live[sessionID], nil
		},
	}
	good, _, _ := auth.IssueStepUp(secret, "arena-api", user, sid.String(), org)
	otherOrg, _, _ := auth.IssueStepUp(secret, "arena-api", user, sid.String(), uuid.NewString())
	otherSession, _, _ := auth.IssueStepUp(secret, "arena-api", user, uuid.NewString(), org)
	foreign, _, _ := auth.IssueStepUp(secret, "arena-api", uuid.New(), sid.String(), org)
	unbound, _, _ := auth.IssueJWT(secret, user, nil, nil, "arena-api", auth.StepUpAudience, auth.StepUpTTL)

	type req struct {
		header  string // X-Client-Channel
		claim   string // "chn" of the access token
		sid     string // "sid" of the access token
		token   string // X-Step-Up-Token
		pathOrg string // {org_id} of the route; "" = a flat route
	}
	var seenOrg string
	reached := false
	h := s.requireWebStepUp(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached = true
		seenOrg, _ = stepUpOrgFromContext(r.Context())
	}))
	run := func(c req) int {
		reached, seenOrg = false, ""
		r := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader("{}"))
		ctx := auth.WithActor(r.Context(), auth.Actor{
			ID: user.String(), Type: auth.ActorTypeUser, SessionID: c.sid, Channel: c.claim,
		})
		if c.header != "" {
			ctx = audit.WithClientChannel(ctx, c.header)
		}
		if c.pathOrg != "" {
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("org_id", c.pathOrg)
			ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		}
		r = r.WithContext(ctx)
		if c.token != "" {
			r.Header.Set(StepUpHeader, c.token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	web := audit.ChannelEventCenterWeb
	tg := audit.ChannelEventCenterTG
	s1 := sid.String()
	for name, c := range map[string]struct {
		req  req
		pass bool
		code int
	}{
		"bot is never asked":       {req{header: "telegram_bot"}, true, 0},
		"admin-web is never asked": {req{header: "admin_web", sid: s1}, true, 0},
		"no channel, no claim":     {req{sid: s1}, true, 0},
		"web header, no token":     {req{header: web, sid: s1}, false, http.StatusForbidden},
		// S-1: a web session that leaves the header out is still asked.
		"web claim, no header, no token": {req{claim: web, sid: s1}, false, http.StatusForbidden},
		"web claim with the token":       {req{claim: web, sid: s1, token: good}, true, 0},
		"web header with the token":      {req{header: web, sid: s1, token: good}, true, 0},
		"right organization in the path": {req{claim: web, sid: s1, token: good, pathOrg: org}, true, 0},
		"another organization in path":   {req{claim: web, sid: s1, token: otherOrg, pathOrg: org}, false, http.StatusForbidden},
		"token of another sign-in":       {req{claim: web, sid: s1, token: otherSession}, false, http.StatusForbidden},
		"another person's token":         {req{claim: web, sid: s1, token: foreign}, false, http.StatusForbidden},
		"token without session or org":   {req{claim: web, sid: s1, token: unbound}, false, http.StatusForbidden},
		"web request without a sign-in":  {req{header: web, token: good}, false, http.StatusForbidden},
		"signed out":                     {req{claim: web, sid: uuid.NewString(), token: good}, false, http.StatusForbidden},
		// RT-12a: a Telegram mini app session (chn=event_center_tg) is asked too,
		// with or without the header the page sends.
		"tg claim, no token":                 {req{claim: tg, sid: s1}, false, http.StatusForbidden},
		"tg claim, web header, no token":     {req{claim: tg, header: web, sid: s1}, false, http.StatusForbidden},
		"tg claim with the token":            {req{claim: tg, sid: s1, token: good}, true, 0},
		"tg claim, header, with token":       {req{claim: tg, header: web, sid: s1, token: good}, true, 0},
		"tg claim, right org in the path":    {req{claim: tg, sid: s1, token: good, pathOrg: org}, true, 0},
		"tg claim, another org in path":      {req{claim: tg, sid: s1, token: otherOrg, pathOrg: org}, false, http.StatusForbidden},
		"tg claim, token of another sign-in": {req{claim: tg, sid: s1, token: otherSession}, false, http.StatusForbidden},
		"tg claim, signed out":               {req{claim: tg, sid: uuid.NewString(), token: good}, false, http.StatusForbidden},
	} {
		code := run(c.req)
		if reached != c.pass || (!c.pass && code != c.code) {
			t.Errorf("%s: reached=%v code=%d", name, reached, code)
		}
		if c.pass && c.req.token != "" && seenOrg != org {
			t.Errorf("%s: the handler saw step-up organization %q, want %q", name, seenOrg, org)
		}
	}

	// A sign-out ends the sign-in: the same token stops working.
	live[sid] = false
	if code := run(req{claim: web, sid: s1, token: good}); reached || code != http.StatusForbidden {
		t.Errorf("after sign-out: reached=%v code=%d", reached, code)
	}
	live[sid] = true
	checkErr = errors.New("database down")
	if code := run(req{claim: web, sid: s1, token: good}); reached || code != http.StatusServiceUnavailable {
		t.Errorf("sign-in check failing: reached=%v code=%d", reached, code)
	}
}

// TestRowOrgAccess_HoldsAFlatMoneyRouteToTheStepUpOrganization: on a flat
// money route the organization comes from the loaded row, so rowOrgAccess
// compares it with the step-up token's organization.
func TestRowOrgAccess_HoldsAFlatMoneyRouteToTheStepUpOrganization(t *testing.T) {
	t.Parallel()
	s := &Server{}
	row := uuid.New()
	r := httptest.NewRequest(http.MethodPost, "/v1/refunds/x/approve", nil)
	r = r.WithContext(withStepUpOrg(r.Context(), uuid.NewString()))
	rec := httptest.NewRecorder()
	if s.rowOrgAccess(rec, r, row, true, "refund.not_found", "refund not found") {
		t.Fatal("a row of another organization passed")
	}
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "auth.step_up_required") {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

// stepUpMoneyRoutes are the routes that move money or decide a refund. Each
// one MUST run requireWebStepUp (T-1). Spec 08_architecture/37 section 4.5
// lists the routes that join this table when the web gets them (payment
// configs, bank accounts, order and ticket cancellation, payment-intent
// transition).
var stepUpMoneyRoutes = []string{
	"POST /v1/organizations/{org_id}/orders/{order_id}/refunds",
	"POST /v1/organizations/{org_id}/refunds/{refund_id}/retry",
	"POST /v1/organizations/{org_id}/refunds/{refund_id}/resolve",
	"POST /v1/organizations/{org_id}/tickets/{ticket_id}/review-hold/clear",
	"POST /v1/organizations/{org_id}/tickets/{ticket_id}/scan-refund-exception",
	"POST /v1/refunds",
	"POST /v1/refunds/{id}/approve",
	"POST /v1/refunds/{id}/reject",
	// 4-2: the web cancels an unpaid order after the password question.
	"POST /v1/organizations/{org_id}/orders/{id}/cancel",
	// The merchandise shop (spec 47): a refund returns money, a cancel ends
	// an unpaid order.
	"POST /v1/organizations/{org_id}/shop/orders/{order_id}/refund",
	"POST /v1/organizations/{org_id}/shop/orders/{order_id}/cancel",
	// Resolving a late payment may return the whole payment (SB-04).
	"POST /v1/organizations/{org_id}/shop/orders/{order_id}/resolve",
}

// stepUpExempt are writes whose path looks like money but which are not a
// person's action in the web: each needs a reason.
var stepUpExempt = map[string]string{
	"POST /v1/refunds/webhook": "provider webhook: no person, verified by the provider's signature",
}

// TestStepUp_EveryMoneyRouteRunsTheGuard walks the real router. It fails when
// a route of stepUpMoneyRoutes lost the guard or disappeared, and when a new
// write whose path names refunds, review holds or scan exceptions is neither
// guarded nor exempted with a reason.
func TestStepUp_EveryMoneyRouteRunsTheGuard(t *testing.T) {
	s := buildDriftTestServer(t)
	guarded := map[string]bool{}
	all := map[string]bool{}
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		key := strings.ToUpper(method) + " " + route
		all[key] = true
		for _, mw := range mws {
			if fn := runtime.FuncForPC(reflect.ValueOf(mw).Pointer()); fn != nil &&
				strings.Contains(fn.Name(), ").requireWebStepUp") {
				guarded[key] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	for _, key := range stepUpMoneyRoutes {
		if !all[key] {
			t.Errorf("%s is not registered (renamed or removed? update stepUpMoneyRoutes)", key)
			continue
		}
		if !guarded[key] {
			t.Errorf("%s does not run requireWebStepUp: the web could move money without the password", key)
		}
	}
	listed := map[string]bool{}
	for _, key := range stepUpMoneyRoutes {
		listed[key] = true
	}
	var unlisted []string
	for key := range all {
		method := strings.SplitN(key, " ", 2)[0]
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			continue
		}
		lower := strings.ToLower(key)
		if !strings.Contains(lower, "refund") && !strings.Contains(lower, "review-hold") {
			continue
		}
		if listed[key] || stepUpExempt[key] != "" {
			continue
		}
		unlisted = append(unlisted, key)
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("money-looking writes that neither run requireWebStepUp nor are exempted:\n  %s\n"+
			"add pr.Use(s.requireWebStepUp) to the route's group and list it in stepUpMoneyRoutes,\n"+
			"or give it a reason in stepUpExempt", strings.Join(unlisted, "\n  "))
	}
	for key := range stepUpExempt {
		if !all[key] {
			t.Errorf("stepUpExempt names %s, which is not registered", key)
		}
	}
}
