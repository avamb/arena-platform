//go:build integration

// billing_admin_review_integration_test.go — the review of PR #189 (billing
// B1-5, price lists and rates): the series locks of a revoke, "strictly in the
// future" judged on a clock read after the locks, a close that never extends,
// the rollback when the audit writer fails, the 503 without a durable audit
// writer, and a separate 403 per role.
//
// Run against a scratch database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestBillingAdminReview_
package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbilling"
)

// reviewAudit is an audit writer that reports itself durable and then fails,
// like a database that refuses the audit insert.
type reviewAudit struct{}

func (reviewAudit) Write(context.Context, audit.Event) error { return errors.New("audit refused") }
func (reviewAudit) WriteTx(context.Context, pgx.Tx, audit.Event) error {
	return errors.New("audit refused")
}
func (reviewAudit) DurableInTx() bool { return true }

// miniBilling is the billing admin handler behind its own chi router, with a
// chosen clock and audit writer. It carries the superadmin as the actor and
// nothing else: the access rules are tested through the real router.
func (e *billingAdminEnv) miniBilling(t *testing.T, w audit.Writer, now func() time.Time) http.Handler {
	t.Helper()
	h := hbilling.NewAdmin(e.srv.pgxPool, w, nil)
	if now != nil {
		h.WithClock(now)
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			ctx := auth.WithActor(req.Context(), auth.Actor{ID: e.superID.String(), Type: "user"})
			next.ServeHTTP(rw, req.WithContext(ctx))
		})
	})
	r.Post("/price-lists", h.HandleCreatePriceList)
	r.Post("/price-lists/{id}/revoke", h.HandleRevokePriceList)
	r.Post("/rate-overrides", h.HandleCreateRateOverride)
	r.Post("/rate-overrides/{id}/close", h.HandleCloseRateOverride)
	r.Delete("/rate-overrides/{id}", h.HandleDeleteRateOverride)
	r.Post("/accounts/{account_id}/terms", h.HandleCreateAccountTerms)
	return r
}

func miniDo(h http.Handler, method, path, body string) (int, string) {
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Admin-Reason", "b15 review test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *billingAdminEnv) wantMini(t *testing.T, h http.Handler, want int, wantCode, method, path, body string) {
	t.Helper()
	st, out := miniDo(h, method, path, body)
	if st != want {
		t.Errorf("%s %s: got %d %s, want %d", method, path, st, out, want)
		return
	}
	if wantCode != "" {
		if c := billingErrCode(out); c != wantCode {
			t.Errorf("%s %s: code %q, want %q (%s)", method, path, c, wantCode, out)
		}
	}
}

func (e *billingAdminEnv) rateRow(t *testing.T, currency string, from time.Time, to *time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.srv.pgxPool.QueryRow(context.Background(), `
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, created_by)
VALUES ($1, $2, 10, 20, $3, $4, 'fixture') RETURNING id`, e.orgID, currency, from, to).Scan(&id); err != nil {
		t.Fatalf("insert rate: %v", err)
	}
	return id
}

func (e *billingAdminEnv) termsBody(from time.Time, code string) string {
	return fmt.Sprintf(`{"effective_from":%q,"price_list_code":%q,"plan_kind":"standard","billing_cycle":"monthly","shortfall_policy":"invoice_postpaid"}`,
		rfc(from), code)
}

// ─── item 2: strictly in the future, judged on the clock ──────────────────

// effective_from == now is refused, one second later passes, in every
// create, revoke, close and delete. The handler's clock is fixed, so the
// boundary is exact.
func TestBillingAdminReview_BoundaryEffectiveFromEqualsNow(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	T := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	h := e.miniBilling(t, e.srv.audit, func() time.Time { return T })
	acc := e.account(t)

	// Create a price list.
	atNow := e.code()
	// The request-level check (before the lock) answers with the price-list code;
	// the post-lock re-check answers effective_from_not_future for a clock that moved.
	e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.invalid_price_list", http.MethodPost, "/price-lists",
		priceListBody(atNow, rfc(T), fixedRule(70, 120)))
	var n int
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM price_lists WHERE code = $1`, atNow).Scan(&n)
	if n != 0 {
		t.Errorf("a refused create left %d versions", n)
	}
	e.wantMini(t, h, http.StatusCreated, "", http.MethodPost, "/price-lists", priceListBody(e.code(), rfc(T.Add(time.Second)), fixedRule(70, 120)))

	// Create an individual rate.
	rate := func(from time.Time) string {
		return fmt.Sprintf(`{"org_id":%q,"currency":"USD","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, rfc(from))
	}
	e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.effective_from_not_future", http.MethodPost, "/rate-overrides", rate(T))
	e.wantMini(t, h, http.StatusCreated, "", http.MethodPost, "/rate-overrides", rate(T.Add(time.Second)))

	// Create account terms.
	e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.effective_from_not_future", http.MethodPost,
		"/accounts/"+acc.ID.String()+"/terms", e.termsBody(T, "standard-2026"))
	e.wantMini(t, h, http.StatusCreated, "", http.MethodPost,
		"/accounts/"+acc.ID.String()+"/terms", e.termsBody(T.Add(time.Second), "standard-2026"))

	// Revoke: a version that starts exactly now is in force.
	c1, c2 := e.code(), e.code()
	startsNow := e.insertVersionAt(t, c1, 1, T, 70, 120)
	startsLater := e.insertVersionAt(t, c2, 1, T.Add(time.Second), 70, 120)
	e.wantMini(t, h, http.StatusConflict, "billing.price_list_in_force", http.MethodPost, "/price-lists/"+startsNow.String()+"/revoke", "")
	e.wantMini(t, h, http.StatusOK, "", http.MethodPost, "/price-lists/"+startsLater.String()+"/revoke", "")

	// Close: the end must be after now.
	open := e.rateRow(t, "CZK", T.Add(-time.Hour), nil)
	e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.invalid_period", http.MethodPost, "/rate-overrides/"+open.String()+"/close",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(T)))
	e.wantMini(t, h, http.StatusOK, "", http.MethodPost, "/rate-overrides/"+open.String()+"/close",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(T.Add(time.Second))))

	// Delete: a rate that starts exactly now has started.
	startsNowRate := e.rateRow(t, "GBP", T, nil)
	startsLaterRate := e.rateRow(t, "PLN", T.Add(time.Second), nil)
	e.wantMini(t, h, http.StatusConflict, "", http.MethodDelete, "/rate-overrides/"+startsNowRate.String(), "")
	e.wantMini(t, h, http.StatusNoContent, "", http.MethodDelete, "/rate-overrides/"+startsLaterRate.String(), "")
}

// ─── item 1: series locks ─────────────────────────────────────────────────

type result struct {
	status int
	body   string
	err    error
}

func (e *billingAdminEnv) rawPost(path, body string) result {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		return result{err: err}
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+e.super)
	req.Header.Set("X-Admin-Reason", "b15 race")
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		return result{err: err}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{status: resp.StatusCode, body: string(b)}
}

// race runs the calls at the same moment and returns their results.
func race(calls ...func() result) []result {
	out := make([]result, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = c()
		}()
	}
	close(start)
	wg.Wait()
	return out
}

// Two future versions of one code that start at the same moment: each can be
// revoked while the other stays, both cannot. The accounts that name the code
// must keep a version, whatever the interleaving.
func TestBillingAdminReview_ParallelRevokeKeepsAVersion(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	from := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	for round := 0; round < 12; round++ {
		code := e.code()
		a := e.insertVersionAt(t, code, 1, from, 70, 120)
		b := e.insertVersionAt(t, code, 2, from, 80, 130)
		// Terms that name the code, later than both versions.
		st, out := e.do(t, http.MethodPost, "/v1/admin/billing/accounts/"+acc.ID.String()+"/terms", e.super, "b15 depend",
			e.termsBody(from.Add(time.Duration(48+round)*time.Hour), code))
		if st != http.StatusCreated {
			t.Fatalf("round %d: terms: %d %s", round, st, out)
		}
		res := race(
			func() result { return e.rawPost("/v1/admin/billing/price-lists/"+a.String()+"/revoke", "") },
			func() result { return e.rawPost("/v1/admin/billing/price-lists/"+b.String()+"/revoke", "") },
		)
		ok, inUse := 0, 0
		for _, r := range res {
			switch {
			case r.err != nil:
				t.Fatalf("round %d: %v", round, r.err)
			case r.status == http.StatusOK:
				ok++
			case r.status == http.StatusConflict && billingErrCode(r.body) == "billing.price_list_in_use":
				inUse++
			default:
				t.Errorf("round %d: unexpected answer %d %s", round, r.status, r.body)
			}
		}
		var left int
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM price_lists WHERE code = $1 AND revoked_at IS NULL`, code).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if ok != 1 || inUse != 1 || left != 1 {
			t.Fatalf("round %d: %d revoked, %d refused, %d versions left; want exactly one revoke and one version left", round, ok, inUse, left)
		}
	}
}

// A revoke and a creation of terms that name the same single future version:
// exactly one of them wins, and terms never point at a revoked version.
func TestBillingAdminReview_RevokeVersusCreateTerms(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	from := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	for round := 0; round < 12; round++ {
		code := e.code()
		v := e.insertVersionAt(t, code, 1, from, 70, 120)
		termsFrom := from.Add(time.Duration(200+round) * time.Hour)
		res := race(
			func() result { return e.rawPost("/v1/admin/billing/price-lists/"+v.String()+"/revoke", "") },
			func() result {
				return e.rawPost("/v1/admin/billing/accounts/"+acc.ID.String()+"/terms", e.termsBody(termsFrom, code))
			},
		)
		for _, r := range res {
			if r.err != nil {
				t.Fatalf("round %d: %v", round, r.err)
			}
		}
		revoke, terms := res[0], res[1]
		var revoked bool
		var termsRows int
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM price_lists WHERE id = $1`, v).Scan(&revoked); err != nil {
			t.Fatal(err)
		}
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM billing_account_terms WHERE account_id = $1 AND price_list_code = $2`, acc.ID, code).Scan(&termsRows); err != nil {
			t.Fatal(err)
		}
		switch {
		case revoke.status == http.StatusOK && terms.status == http.StatusUnprocessableEntity && revoked && termsRows == 0:
			if c := billingErrCode(terms.body); c != "billing.price_list_not_found" {
				t.Errorf("round %d: terms refused with %q", round, c)
			}
		case terms.status == http.StatusCreated && revoke.status == http.StatusConflict && !revoked && termsRows == 1:
			if c := billingErrCode(revoke.body); c != "billing.price_list_in_use" {
				t.Errorf("round %d: revoke refused with %q", round, c)
			}
		default:
			t.Fatalf("round %d: revoke %d %s / terms %d %s, revoked=%v, terms rows=%d: terms may never name a revoked version",
				round, revoke.status, revoke.body, terms.status, terms.body, revoked, termsRows)
		}
	}
}

// ─── item 3: closing never extends ────────────────────────────────────────

func TestBillingAdminReview_CloseNeverExtends(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	h := e.miniBilling(t, e.srv.audit, nil)

	from := now.Add(-48 * time.Hour)
	end := now.Add(10 * time.Hour)
	id := e.rateRow(t, "EUR", from, &end)
	path := "/rate-overrides/" + id.String() + "/close"
	read := func() time.Time {
		var to time.Time
		if err := e.srv.pgxPool.QueryRow(ctx, `SELECT effective_to FROM org_rate_overrides WHERE id = $1`, id).Scan(&to); err != nil {
			t.Fatal(err)
		}
		return to
	}
	for _, to := range []time.Time{end.Add(time.Hour), end.Add(24 * time.Hour)} {
		e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.invalid_period", http.MethodPost, path, fmt.Sprintf(`{"effective_to":%q}`, rfc(to)))
	}
	// The same end changes nothing and is refused too: a close must shorten.
	e.wantMini(t, h, http.StatusUnprocessableEntity, "billing.invalid_period", http.MethodPost, path, fmt.Sprintf(`{"effective_to":%q}`, rfc(end)))
	if got := read(); !got.Equal(end) {
		t.Fatalf("effective_to = %s after refused closes, want %s", got, end)
	}
	e.wantMini(t, h, http.StatusOK, "", http.MethodPost, path, fmt.Sprintf(`{"effective_to":%q}`, rfc(end.Add(-time.Hour))))
	if got := read(); !got.Equal(end.Add(-time.Hour)) {
		t.Fatalf("effective_to = %s after a shortening, want %s", got, end.Add(-time.Hour))
	}

	// The SQL repeats the rule: even a call that skips the handler's check cannot extend.
	if _, err := e.q.SetOrgRateOverrideEnd(ctx, id, end.Add(48*time.Hour), now); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("SetOrgRateOverrideEnd extended the period: %v", err)
	}

	// An open rate followed by another one that was inserted behind its back:
	// closing it past the next start is the overlap refusal, before it is fine.
	open := e.rateRow(t, "USD", now.Add(-time.Hour), nil)
	nextFrom := now.Add(5 * time.Hour)
	e.rateRow(t, "USD", nextFrom, nil)
	openPath := "/rate-overrides/" + open.String() + "/close"
	e.wantMini(t, h, http.StatusConflict, "billing.rate_override_overlap", http.MethodPost, openPath, fmt.Sprintf(`{"effective_to":%q}`, rfc(nextFrom.Add(time.Hour))))
	e.wantMini(t, h, http.StatusOK, "", http.MethodPost, openPath, fmt.Sprintf(`{"effective_to":%q}`, rfc(nextFrom)))
}

// ─── item 4: audit failure, no writer, who is let in ──────────────────────

// When the audit writer fails, nothing the request changed survives, on all
// six write routes.
func TestBillingAdminReview_AuditFailureRollsBackEveryWrite(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	now := time.Now().UTC().Truncate(time.Second)
	future := now.Add(72 * time.Hour)
	h := e.miniBilling(t, reviewAudit{}, nil)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := e.srv.pgxPool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Create a price list: no version, no rule.
	code := e.code()
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/price-lists", priceListBody(code, rfc(future), fixedRule(70, 120)))
	if n := count(`SELECT count(*) FROM price_lists WHERE code = $1`, code); n != 0 {
		t.Errorf("create: %d versions survived the audit failure", n)
	}

	// Revoke: the version stays unrevoked.
	rcode := e.code()
	v := e.insertVersionAt(t, rcode, 1, future, 70, 120)
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/price-lists/"+v.String()+"/revoke", "")
	if n := count(`SELECT count(*) FROM price_lists WHERE id = $1 AND revoked_at IS NULL`, v); n != 1 {
		t.Errorf("revoke: the version was revoked although the audit failed")
	}

	// Create a rate: no row.
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/rate-overrides",
		fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, rfc(future)))
	if n := count(`SELECT count(*) FROM org_rate_overrides WHERE org_id = $1 AND currency = 'EUR'`, e.orgID); n != 0 {
		t.Errorf("create rate: %d rows survived", n)
	}

	// Creating a rate also closes the open one before it: that close is undone too.
	open := e.rateRow(t, "USD", now.Add(-time.Hour), nil)
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/rate-overrides",
		fmt.Sprintf(`{"org_id":%q,"currency":"USD","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, rfc(future)))
	if n := count(`SELECT count(*) FROM org_rate_overrides WHERE id = $1 AND effective_to IS NULL`, open); n != 1 {
		t.Errorf("create rate: the open rate was closed although the audit failed")
	}
	if n := count(`SELECT count(*) FROM org_rate_overrides WHERE org_id = $1 AND currency = 'USD'`, e.orgID); n != 1 {
		t.Errorf("create rate: a new USD row survived")
	}

	// Close: the end is unchanged.
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/rate-overrides/"+open.String()+"/close",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(future)))
	if n := count(`SELECT count(*) FROM org_rate_overrides WHERE id = $1 AND effective_to IS NULL`, open); n != 1 {
		t.Errorf("close: the rate was closed although the audit failed")
	}

	// Delete: the row stays.
	notStarted := e.rateRow(t, "CZK", future, nil)
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodDelete, "/rate-overrides/"+notStarted.String(), "")
	if n := count(`SELECT count(*) FROM org_rate_overrides WHERE id = $1`, notStarted); n != 1 {
		t.Errorf("delete: the rate was deleted although the audit failed")
	}

	// Terms: no new row.
	before := count(`SELECT count(*) FROM billing_account_terms WHERE account_id = $1`, acc.ID)
	e.wantMini(t, h, http.StatusInternalServerError, "billing.admin_failed", http.MethodPost, "/accounts/"+acc.ID.String()+"/terms",
		e.termsBody(future, "standard-2026"))
	if n := count(`SELECT count(*) FROM billing_account_terms WHERE account_id = $1`, acc.ID); n != before {
		t.Errorf("terms: %d rows before, %d after a failed audit", before, n)
	}
}

// Through the real router: no audit writer, or one that only logs, makes
// every write answer 503 and change nothing, while the reads still work. The
// writer the production server builds by itself is a durable one.
func TestBillingAdminReview_NoDurableAuditWriterIs503(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	if !audit.IsDurableInTx(e.srv.audit) {
		t.Fatalf("the default audit writer of the server (%T) does not write audit_events on the change's transaction", e.srv.audit)
	}
	prod := e.srv.audit
	t.Cleanup(func() { e.srv.audit = prod })

	code := e.code()
	future := rfc(time.Now().UTC().Add(72 * time.Hour))
	v := e.insertVersionAt(t, e.code(), 1, time.Now().UTC().Add(48*time.Hour), 70, 120)
	acc := e.account(t)
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/admin/billing/price-lists", priceListBody(code, future, fixedRule(70, 120))},
		{http.MethodPost, "/v1/admin/billing/price-lists/" + v.String() + "/revoke", ""},
		{http.MethodPost, "/v1/admin/billing/rate-overrides", fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, future)},
		{http.MethodPost, "/v1/admin/billing/rate-overrides/" + uuid.NewString() + "/close", fmt.Sprintf(`{"effective_to":%q}`, future)},
		{http.MethodDelete, "/v1/admin/billing/rate-overrides/" + uuid.NewString(), ""},
		{http.MethodPost, "/v1/admin/billing/accounts/" + acc.ID.String() + "/terms", e.termsBody(time.Now().UTC().Add(72*time.Hour), "standard-2026")},
	}
	for name, w := range map[string]audit.Writer{"nil": nil, "log-only": audit.NewSlogWriter(nil)} {
		e.srv.audit = w
		for _, wr := range writes {
			out := e.expect(t, http.StatusServiceUnavailable, wr.method, wr.path, e.super, "b15 no audit", wr.body)
			if c := billingErrCode(out); c != "dependency.audit_unavailable" {
				t.Errorf("%s writer, %s %s: code %q", name, wr.method, wr.path, c)
			}
		}
		for _, p := range []string{"/v1/admin/billing/price-lists", "/v1/admin/billing/rate-overrides", "/v1/admin/billing/accounts"} {
			e.expect(t, http.StatusOK, http.MethodGet, p, e.super, "", "")
		}
	}
	var n int
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM price_lists WHERE code = $1 OR (id = $2 AND revoked_at IS NOT NULL)`, code, v).Scan(&n)
	if n != 0 {
		t.Errorf("a refused write changed %d price list rows", n)
	}
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM org_rate_overrides WHERE org_id = $1`, e.orgID).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused write left %d rates (%v)", n, err)
	}
}

// Each kind of caller is refused on its own: the owner, a manager, an agent
// of the organization and an organization API key never reach a price list.
func TestBillingAdminReview_EachRoleIsRefused(t *testing.T) {
	e := newBillingAdminEnv(t)
	acc := e.account(t)
	future := rfc(time.Now().UTC().Add(72 * time.Hour))

	manager := e.user(t, "b15manager")
	e.member(t, manager, e.orgID, "organizer")
	agent := e.user(t, "b15agent")
	e.member(t, agent, e.orgID, "agent")
	callers := map[string]string{
		"owner":   e.owner,
		"manager": e.token(t, manager),
		"agent":   e.token(t, agent),
		"api_key": e.key(t, e.orgID, "org.read", "event.read"),
	}
	reads := []string{
		"/v1/admin/billing/price-lists", "/v1/admin/billing/rate-overrides", "/v1/admin/billing/accounts",
		"/v1/admin/billing/price-lists/" + uuid.NewString(),
	}
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/admin/billing/price-lists", priceListBody(e.code(), future, fixedRule(70, 120))},
		{http.MethodPost, "/v1/admin/billing/price-lists/" + uuid.NewString() + "/revoke", ""},
		{http.MethodPost, "/v1/admin/billing/rate-overrides", fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, future)},
		{http.MethodPost, "/v1/admin/billing/rate-overrides/" + uuid.NewString() + "/close", fmt.Sprintf(`{"effective_to":%q}`, future)},
		{http.MethodDelete, "/v1/admin/billing/rate-overrides/" + uuid.NewString(), ""},
		{http.MethodPost, "/v1/admin/billing/accounts/" + acc.ID.String() + "/terms", `{}`},
	}
	for name, tok := range callers {
		t.Run(name, func(t *testing.T) {
			for _, p := range reads {
				e.expect(t, http.StatusForbidden, http.MethodGet, p, tok, "b15", "")
			}
			for _, w := range writes {
				e.expect(t, http.StatusForbidden, w.method, w.path, tok, "b15 "+name, w.body)
			}
		})
	}
	var n int
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM org_rate_overrides WHERE org_id = $1`, e.orgID).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused caller left %d rates (%v)", n, err)
	}
}
