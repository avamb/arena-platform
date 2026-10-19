//go:build integration

// billing_admin_integration_test.go — the superadmin screen "Price lists and
// rates" (billing wave B-A, task B1-5): /v1/admin/billing/*.
//
// Every request goes through the REAL router with a JWT whose authority is
// its database rows (a platform superadmin, an organization owner) — the
// shape sec1Env mints. The numbers in these tests are test numbers on test
// price-list codes (b15-*), never the policy's: the screen takes every number
// from the operator.
//
// Run against a scratch database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestBillingAdmin_
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/billing"
)

type billingAdminEnv struct {
	*sec1Env
	superID uuid.UUID
	super   string
	owner   string
	orgID   uuid.UUID
	codes   []string
}

func newBillingAdminEnv(t *testing.T) *billingAdminEnv {
	t.Helper()
	e := &billingAdminEnv{sec1Env: newSec1Env(t)}
	e.superID, e.super = e.superadmin(t)
	e.orgID = e.org(t, "B15")
	ownerID := e.user(t, "b15owner")
	e.member(t, ownerID, e.orgID, "org_admin")
	e.owner = e.token(t, ownerID)
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM org_rate_overrides WHERE org_id = $1`,
			`DELETE FROM billing_accounts WHERE org_id = $1`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, e.orgID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		if len(e.codes) > 0 {
			if _, err := e.srv.pgxPool.Exec(ctx, `DELETE FROM price_lists WHERE code = ANY($1)`, e.codes); err != nil {
				t.Logf("cleanup price lists: %v", err)
			}
		}
	})
	return e
}

// code makes a fresh price-list code and remembers it for cleanup.
func (e *billingAdminEnv) code() string {
	c := "b15-" + strings.ToLower(uuid.NewString()[:8])
	e.codes = append(e.codes, c)
	return c
}

// fixedRule is the one rule a rated standard account needs, with test numbers.
func fixedRule(unseated, seated int64) string {
	return fmt.Sprintf(`{"rule_type":"fixed_per_ticket","line":"standard","event_kind":"ticket_sold","params":{"unseated_minor":%d,"seated_minor":%d}}`, unseated, seated)
}

func priceListBody(code, from string, rules ...string) string {
	return fmt.Sprintf(`{"code":%q,"currency":"EUR","effective_from":%q,"bonus_share_cap_bp":3000,"rules":[%s]}`,
		code, from, strings.Join(rules, ","))
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// insertVersionAt inserts a price-list version straight into the table, as an
// older deploy left it: the screen itself never writes a start in the past.
func (e *billingAdminEnv) insertVersionAt(t *testing.T, code string, version int, from time.Time, unseated, seated int64) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := e.srv.pgxPool.QueryRow(ctx, `
INSERT INTO price_lists (code, version, currency, effective_from, created_by, reason)
VALUES ($1, $2, 'EUR', $3, 'fixture', 'b15 test') RETURNING id`, code, version, from).Scan(&id); err != nil {
		t.Fatalf("insert price list: %v", err)
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, params, sort_order)
VALUES ($1, 'fixed_per_ticket', 'standard', 'ticket_sold', $2::jsonb, 10)`, id,
		fmt.Sprintf(`{"unseated_minor":%d,"seated_minor":%d}`, unseated, seated)); err != nil {
		t.Fatalf("insert rule: %v", err)
	}
	return id
}

func jsonField(t *testing.T, body, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func billingErrCode(body string) string { return sec1Code(body) }

// auditRows counts the audit rows of an action on a resource.
func (e *billingAdminEnv) auditRows(t *testing.T, action string, resourceID string) (n int, actor, reason string) {
	t.Helper()
	rows, err := e.srv.pgxPool.Query(context.Background(),
		`SELECT COALESCE(actor_id::text, ''), COALESCE(metadata->>'reason', '') FROM audit_events WHERE action = $1 AND resource_id = $2`,
		action, resourceID)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&actor, &reason); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n, actor, reason
}

func (e *billingAdminEnv) account(t *testing.T) gen.BillingAccountRow {
	t.Helper()
	acc, err := billing.EnsureAccount(context.Background(), e.q, e.orgID, "ES")
	if err != nil {
		t.Fatalf("EnsureAccount: %v", err)
	}
	return acc
}

// ─── access ───────────────────────────────────────────────────────────────

func TestBillingAdmin_OnlyASuperadminWithAReasonGetsIn(t *testing.T) {
	e := newBillingAdminEnv(t)
	code := e.code()
	future := rfc(time.Now().Add(72 * time.Hour))
	create := priceListBody(code, future, fixedRule(70, 120))
	acc := e.account(t)
	listPaths := []string{
		"/v1/admin/billing/price-lists", "/v1/admin/billing/rate-overrides", "/v1/admin/billing/accounts",
		"/v1/admin/billing/price-lists/" + uuid.NewString(),
	}
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/admin/billing/price-lists", create},
		{http.MethodPost, "/v1/admin/billing/price-lists/" + uuid.NewString() + "/revoke", ""},
		{http.MethodPost, "/v1/admin/billing/rate-overrides", fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":1,"seated_minor":2,"effective_from":%q}`, e.orgID, future)},
		{http.MethodPost, "/v1/admin/billing/rate-overrides/" + uuid.NewString() + "/close", fmt.Sprintf(`{"effective_to":%q}`, future)},
		{http.MethodDelete, "/v1/admin/billing/rate-overrides/" + uuid.NewString(), ""},
		{http.MethodPost, "/v1/admin/billing/accounts/" + acc.ID.String() + "/terms", `{}`},
	}

	// An organization owner and an organization API key never see any of it.
	apiKey := e.key(t, e.orgID, "org.read", "event.read")
	for _, tok := range []string{e.owner, apiKey} {
		for _, p := range listPaths {
			e.expect(t, http.StatusForbidden, http.MethodGet, p, tok, "b15", "")
		}
		for _, w := range writes {
			e.expect(t, http.StatusForbidden, w.method, w.path, tok, "b15", w.body)
		}
	}
	for _, p := range listPaths[:3] {
		e.expect(t, http.StatusUnauthorized, http.MethodGet, p, "", "", "")
	}

	// The superadmin without X-Admin-Reason: 400 on every write, nothing written.
	for _, w := range writes {
		out := e.expect(t, http.StatusBadRequest, w.method, w.path, e.super, "", w.body)
		if c := billingErrCode(out); c != "superadmin.missing_reason" {
			t.Errorf("%s %s: code %q, want superadmin.missing_reason", w.method, w.path, c)
		}
	}
	var n int
	_ = e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM price_lists WHERE code = $1`, code).Scan(&n)
	if n != 0 {
		t.Errorf("a refused create left %d price list rows", n)
	}
	// The reads need no reason.
	for _, p := range listPaths[:3] {
		e.expect(t, http.StatusOK, http.MethodGet, p, e.super, "", "")
	}
}

// ─── price lists ──────────────────────────────────────────────────────────

func TestBillingAdmin_FutureVersionDoesNotTouchRateUntilItsDate(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	code := e.code()
	acc := e.account(t)

	// v1 is in force from the start of 2026 (fixture). The account is put on
	// it through the screen from t1.
	e.insertVersionAt(t, code, 1, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 77, 133)
	t1 := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/accounts/"+acc.ID.String()+"/terms",
		e.super, "b15 assign test list", fmt.Sprintf(
			`{"effective_from":%q,"price_list_code":%q,"plan_kind":"standard","billing_cycle":"monthly","shortfall_policy":"invoice_postpaid"}`, rfc(t1), code))

	rateAt := func(at time.Time, seated bool) (int64, int32) {
		t.Helper()
		terms, err := billing.TermsAt(ctx, e.q, acc.ID, at)
		if err != nil {
			t.Fatalf("TermsAt(%s): %v", at, err)
		}
		c, err := billing.Rate(terms, billing.Event{Kind: billing.KindTicketSold, OccurredAt: at, Seated: seated,
			Currency: "EUR", UnitPriceMinor: 5000, PricingClass: billing.PricingClassStandard})
		if err != nil {
			t.Fatalf("Rate: %v", err)
		}
		return c.AmountMinor, c.PriceListVersion
	}

	// v2 through the screen, starting at t2 in the future.
	t2 := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	v2 := e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/price-lists", e.super, "b15 new prices",
		priceListBody(code, rfc(t2), fixedRule(91, 151)))
	if jsonField(t, v2, "version") != "2" || jsonField(t, v2, "status") != "scheduled" {
		t.Fatalf("v2 = %s", v2)
	}
	v2ID := jsonField(t, v2, "id")

	for _, tc := range []struct {
		at      time.Time
		seated  bool
		want    int64
		version int32
	}{
		{t1.Add(time.Minute), false, 77, 1},
		{t1.Add(time.Minute), true, 133, 1},
		{t2.Add(-time.Second), false, 77, 1}, // one second before the start: still v1
		{t2, false, 91, 2},
		{t2.Add(24 * time.Hour), true, 151, 2},
	} {
		got, ver := rateAt(tc.at, tc.seated)
		if got != tc.want || ver != tc.version {
			t.Errorf("at %s seated=%v: %d (v%d), want %d (v%d)", tc.at.Format(time.RFC3339), tc.seated, got, ver, tc.want, tc.version)
		}
	}

	// Revoke v2 before it starts: v1 prices everything again.
	e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/billing/price-lists/"+v2ID+"/revoke", e.super, "b15 changed my mind", "")
	if got, ver := rateAt(t2.Add(24*time.Hour), false); got != 77 || ver != 1 {
		t.Errorf("after the revoke: %d (v%d), want 77 (v1)", got, ver)
	}
	// A second revoke of the same version is refused.
	out := e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/price-lists/"+v2ID+"/revoke", e.super, "b15 again", "")
	if c := billingErrCode(out); c != "billing.price_list_already_revoked" {
		t.Errorf("second revoke: %q", c)
	}
	// A new version at the same start gets the next number (3): the revoked one still counts.
	v3 := e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/price-lists", e.super, "b15 new prices again",
		priceListBody(code, rfc(t2), fixedRule(95, 155)))
	if jsonField(t, v3, "version") != "3" {
		t.Errorf("v3 = %s", v3)
	}
	if got, ver := rateAt(t2.Add(time.Hour), false); got != 95 || ver != 3 {
		t.Errorf("v3 in force: %d (v%d), want 95 (v3)", got, ver)
	}
}

func TestBillingAdmin_RevokeOnlyBeforeTheStart(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	code := e.code()
	started := e.insertVersionAt(t, code, 1, time.Now().UTC().Add(-time.Hour), 70, 120)

	out := e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/price-lists/"+started.String()+"/revoke", e.super, "b15 too late", "")
	if c := billingErrCode(out); c != "billing.price_list_in_force" {
		t.Errorf("revoke after the start: code %q, want billing.price_list_in_force", c)
	}
	var revoked *time.Time
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT revoked_at FROM price_lists WHERE id = $1`, started).Scan(&revoked); err != nil || revoked != nil {
		t.Errorf("the version in force was revoked anyway: %v %v", revoked, err)
	}
	if n, _, _ := e.auditRows(t, "v1.admin.billing.price_list.revoke", started.String()); n != 0 {
		t.Errorf("a refused revoke wrote %d audit rows", n)
	}
	e.expect(t, http.StatusNotFound, http.MethodPost, "/v1/admin/billing/price-lists/"+uuid.NewString()+"/revoke", e.super, "b15 nothing", "")

	// The first version accounts depend on cannot be revoked: they would have no prices.
	dep := e.code()
	acc := e.account(t)
	first := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	v1 := e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/price-lists", e.super, "b15 first", priceListBody(dep, rfc(first), fixedRule(60, 110)))
	e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/accounts/"+acc.ID.String()+"/terms", e.super, "b15 depend", fmt.Sprintf(
		`{"effective_from":%q,"price_list_code":%q,"plan_kind":"standard","billing_cycle":"monthly","shortfall_policy":"invoice_postpaid"}`,
		rfc(first.Add(time.Hour)), dep))
	out = e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/price-lists/"+jsonField(t, v1, "id")+"/revoke", e.super, "b15 depend", "")
	if c := billingErrCode(out); c != "billing.price_list_in_use" {
		t.Errorf("revoking the version accounts depend on: %q", c)
	}
}

func TestBillingAdmin_CreateValidation(t *testing.T) {
	e := newBillingAdminEnv(t)
	code := e.code()
	now := time.Now().UTC()
	post := func(want int, body string) string {
		t.Helper()
		return e.expect(t, want, http.MethodPost, "/v1/admin/billing/price-lists", e.super, "b15 validation", body)
	}
	// Not in the future.
	if c := billingErrCode(post(http.StatusUnprocessableEntity, priceListBody(code, rfc(now.Add(-time.Hour)), fixedRule(1, 2)))); c != "billing.invalid_price_list" {
		t.Errorf("past start: %q", c)
	}
	future := rfc(now.Add(30 * 24 * time.Hour))
	for _, body := range map[string]string{
		"no rules":          priceListBody(code, future),
		"unknown rule type": priceListBody(code, future, `{"rule_type":"made_up","line":"standard","event_kind":"ticket_sold","params":{}}`),
		"not computed yet":  priceListBody(code, future, `{"rule_type":"subscription","line":"standard","event_kind":"ticket_sold","params":{}}`),
		"unknown param":     priceListBody(code, future, `{"rule_type":"fixed_per_ticket","line":"standard","event_kind":"ticket_sold","params":{"unseated_minor":1,"seated_minor":2,"extra":3}}`),
		"missing param":     priceListBody(code, future, `{"rule_type":"fixed_per_ticket","line":"standard","event_kind":"ticket_sold","params":{"unseated_minor":1}}`),
		"negative amount":   priceListBody(code, future, fixedRule(-1, 2)),
		"wrong line":        priceListBody(code, future, `{"rule_type":"fixed_per_ticket","line":"venue","event_kind":"ticket_sold","params":{"unseated_minor":1,"seated_minor":2}}`),
		"duplicate rule":    priceListBody(code, future, fixedRule(1, 2), fixedRule(3, 4)),
		"sale currency on a fixed rule": priceListBody(code, future,
			`{"rule_type":"fixed_per_ticket","line":"standard","event_kind":"ticket_sold","sale_currency":"EUR","params":{"unseated_minor":1,"seated_minor":2}}`),
		"low price without currency": priceListBody(code, future,
			`{"rule_type":"low_price_formula","line":"standard","event_kind":"ticket_sold","params":{"threshold_minor":1,"strict_below":true,"pct_bp":1,"min_minor":1,"max_minor":2,"seated_surcharge_minor":0}}`),
		"bad code":      strings.Replace(priceListBody(code, future, fixedRule(1, 2)), code, "Bad Code!", 1),
		"bad currency":  strings.Replace(priceListBody(code, future, fixedRule(1, 2)), `"EUR"`, `"euro"`, 1),
		"no bonus cap":  strings.Replace(priceListBody(code, future, fixedRule(1, 2)), `"bonus_share_cap_bp":3000,`, ``, 1),
		"bonus cap 5bp": strings.Replace(priceListBody(code, future, fixedRule(1, 2)), `3000`, `5`, 1),
	} {
		post(http.StatusUnprocessableEntity, body)
	}
	// An unknown JSON key is a 400, not a silent drop.
	post(http.StatusBadRequest, strings.Replace(priceListBody(code, future, fixedRule(1, 2)), `{"code"`, `{"surprise":1,"code"`, 1))
	var n int
	_ = e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM price_lists WHERE code = $1`, code).Scan(&n)
	if n != 0 {
		t.Fatalf("refused creates left %d rows", n)
	}

	// A good one, with a pack, and then the ordering rule: the next version
	// must start after the latest one that was not revoked.
	good := fmt.Sprintf(`{"code":%q,"currency":"EUR","effective_from":%q,"bonus_share_cap_bp":2500,"locked":true,
	  "rules":[%s,{"rule_type":"fixed_per_event","line":"standard","event_kind":"event_published","params":{"amount_minor":500}}],
	  "packs":[{"tier":1,"face_minor":1000,"unseated_rate_minor":50,"seated_surcharge_minor":30}]}`, code, future, fixedRule(70, 120))
	out := post(http.StatusCreated, good)
	var created struct {
		Version  int32 `json:"version"`
		Locked   bool  `json:"locked"`
		BonusCap int32 `json:"bonus_share_cap_bp"`
		Rules    []any `json:"rules"`
		Packs    []struct {
			Currency  string `json:"currency"`
			FaceMinor int64  `json:"face_minor"`
			Active    bool   `json:"active"`
		} `json:"packs"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || !created.Locked || created.BonusCap != 2500 || len(created.Rules) != 2 ||
		len(created.Packs) != 1 || created.Packs[0].Currency != "EUR" || created.Packs[0].FaceMinor != 1000 || !created.Packs[0].Active {
		t.Fatalf("created = %s", out)
	}
	for _, from := range []string{future, rfc(now.Add(24 * time.Hour))} {
		out = post(http.StatusConflict, priceListBody(code, from, fixedRule(1, 2)))
		if c := billingErrCode(out); c != "billing.effective_from_not_after_latest" {
			t.Errorf("not after latest (%s): %q", from, c)
		}
	}
	// A later version that drops the event fee warns, it is not refused.
	later := e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/billing/price-lists", e.super, "b15 later",
		priceListBody(code, rfc(now.Add(60*24*time.Hour)), fixedRule(71, 121)))
	var w struct {
		Warnings []string `json:"warnings"`
	}
	_ = json.Unmarshal([]byte(later), &w)
	if len(w.Warnings) != 1 || !strings.Contains(w.Warnings[0], "fixed_per_event") {
		t.Errorf("warnings = %v, want one naming fixed_per_event", w.Warnings)
	}

	// The list shows both with their status; GET by id returns rules and packs.
	list := e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/billing/price-lists?code="+code, e.super, "", "")
	var l struct {
		Items []struct {
			ID         string `json:"id"`
			Version    int    `json:"version"`
			Status     string `json:"status"`
			RulesCount int    `json:"rules_count"`
			PacksCount int    `json:"packs_count"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &l); err != nil || len(l.Items) != 2 || l.Items[0].Version != 1 ||
		l.Items[0].Status != "scheduled" || l.Items[0].RulesCount != 2 || l.Items[0].PacksCount != 1 || l.Items[1].RulesCount != 1 {
		t.Fatalf("list = %s (%v)", list, err)
	}
	detail := e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/billing/price-lists/"+l.Items[0].ID, e.super, "", "")
	if !strings.Contains(detail, `"unseated_minor":70`) || !strings.Contains(detail, `"face_minor":1000`) {
		t.Errorf("detail lacks rules or packs: %s", detail)
	}
}

// ─── individual rates ─────────────────────────────────────────────────────

func TestBillingAdmin_IndividualRates(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	now := time.Now().UTC()
	start1 := now.Add(24 * time.Hour).Truncate(time.Second)
	start2 := now.Add(72 * time.Hour).Truncate(time.Second)
	rateBody := func(unseated, seated int64, from time.Time, extra string) string {
		return fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":%d,"seated_minor":%d,"effective_from":%q%s}`,
			e.orgID, unseated, seated, rfc(from), extra)
	}
	post := func(want int, body string) string {
		t.Helper()
		return e.expect(t, want, http.MethodPost, "/v1/admin/billing/rate-overrides", e.super, "b15 rate", body)
	}
	// Validation.
	post(http.StatusUnprocessableEntity, rateBody(60, 100, now.Add(-time.Hour), ""))
	post(http.StatusUnprocessableEntity, rateBody(-1, 100, start1, ""))
	post(http.StatusUnprocessableEntity, strings.Replace(rateBody(60, 100, start1, ""), `"unseated_minor":60,`, ``, 1))
	post(http.StatusUnprocessableEntity, rateBody(60, 100, start1, fmt.Sprintf(`,"effective_to":%q`, rfc(start1))))
	post(http.StatusNotFound, strings.Replace(rateBody(60, 100, start1, ""), e.orgID.String(), uuid.NewString(), 1))

	first := post(http.StatusCreated, rateBody(61, 101, start1, `,"note":"contract 15"`))
	firstID := jsonField(t, first, "id")
	if jsonField(t, first, "status") != "scheduled" || jsonField(t, first, "effective_to") != "" {
		t.Fatalf("first = %s", first)
	}
	// A rate that starts before or with the latest one is refused.
	if c := billingErrCode(post(http.StatusConflict, rateBody(1, 2, start1, ""))); c != "billing.rate_override_overlap" {
		t.Errorf("same start: %q", c)
	}
	// The next rate closes the open one at its own start.
	second := post(http.StatusCreated, rateBody(62, 102, start2, ""))
	if !strings.Contains(second, firstID) {
		t.Errorf("second does not report closing the first: %s", second)
	}
	var to *time.Time
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT effective_to FROM org_rate_overrides WHERE id = $1`, firstID).Scan(&to); err != nil ||
		to == nil || !to.Equal(start2) {
		t.Fatalf("first.effective_to = %v (%v), want %s", to, err, start2)
	}

	// Rate effect: nothing before the start, the first inside its period, the second after.
	ovAt := func(at time.Time) *gen.OrgRateOverrideRow {
		t.Helper()
		terms, err := billing.TermsAt(ctx, e.q, acc.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		return terms.Override
	}
	if ov := ovAt(start1.Add(-time.Second)); ov != nil {
		t.Errorf("a rate applies before its start: %+v", ov)
	}
	if ov := ovAt(start1); ov == nil || ov.UnseatedMinor != 61 {
		t.Errorf("at the first start: %+v", ov)
	}
	if ov := ovAt(start2.Add(-time.Second)); ov == nil || ov.UnseatedMinor != 61 {
		t.Errorf("just before the second: %+v", ov)
	}
	if ov := ovAt(start2); ov == nil || ov.SeatedMinor != 102 {
		t.Errorf("at the second start: %+v", ov)
	}

	// Close the second for the future: an end that is in the past or before the start is refused.
	secondID := jsonField(t, second, "id")
	closePath := "/v1/admin/billing/rate-overrides/" + secondID + "/close"
	end := start2.Add(48 * time.Hour)
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, closePath, e.super, "b15 close", fmt.Sprintf(`{"effective_to":%q}`, rfc(now.Add(-time.Hour))))
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, closePath, e.super, "b15 close", fmt.Sprintf(`{"effective_to":%q}`, rfc(start2.Add(-time.Minute))))
	closed := e.expect(t, http.StatusOK, http.MethodPost, closePath, e.super, "b15 close", fmt.Sprintf(`{"effective_to":%q}`, rfc(end)))
	if jsonField(t, closed, "effective_to") == "" {
		t.Errorf("closed = %s", closed)
	}
	// The first cannot be stretched past the second's start.
	e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/rate-overrides/"+firstID+"/close", e.super, "b15 close",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(start2.Add(time.Hour))))

	// A rate that has started is never removed and never closed backwards.
	started := uuid.New()
	if _, err := e.srv.pgxPool.Exec(ctx, `
INSERT INTO org_rate_overrides (id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, created_by)
VALUES ($1, $2, 'USD', 10, 20, $3, NULL, 'fixture')`, started, e.orgID, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.expect(t, http.StatusConflict, http.MethodDelete, "/v1/admin/billing/rate-overrides/"+started.String(), e.super, "b15 delete started", "")
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, "/v1/admin/billing/rate-overrides/"+started.String()+"/close", e.super, "b15 close backwards",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(now.Add(-time.Hour))))
	var still int
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM org_rate_overrides WHERE id = $1 AND effective_to IS NULL`, started).Scan(&still)
	if still != 1 {
		t.Errorf("the started rate was changed")
	}
	// ... but it can be closed for the future; then a closed (ended) one answers 409.
	e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/billing/rate-overrides/"+started.String()+"/close", e.super, "b15 close started",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(now.Add(time.Hour))))
	ended := uuid.New()
	if _, err := e.srv.pgxPool.Exec(ctx, `
INSERT INTO org_rate_overrides (id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, created_by)
VALUES ($1, $2, 'CZK', 10, 20, $3, $4, 'fixture')`, ended, e.orgID, now.Add(-72*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if c := billingErrCode(e.expect(t, http.StatusConflict, http.MethodPost, "/v1/admin/billing/rate-overrides/"+ended.String()+"/close", e.super, "b15 close ended",
		fmt.Sprintf(`{"effective_to":%q}`, rfc(now.Add(time.Hour))))); c != "billing.rate_override_ended" {
		t.Errorf("close of an ended rate: %q", c)
	}

	// A rate that has not started can be removed; the audit row keeps its numbers.
	e.expect(t, http.StatusNoContent, http.MethodDelete, "/v1/admin/billing/rate-overrides/"+secondID, e.super, "b15 remove second", "")
	var gone int
	_ = e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM org_rate_overrides WHERE id = $1`, secondID).Scan(&gone)
	if gone != 0 {
		t.Errorf("the removed rate is still there")
	}
	e.expect(t, http.StatusNotFound, http.MethodDelete, "/v1/admin/billing/rate-overrides/"+secondID, e.super, "b15 remove again", "")

	// The list, by organization.
	list := e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/billing/rate-overrides?org_id="+e.orgID.String(), e.super, "", "")
	if !strings.Contains(list, `"org_name"`) || !strings.Contains(list, "ended") || !strings.Contains(list, "scheduled") {
		t.Errorf("list = %s", list)
	}
	e.expect(t, http.StatusUnprocessableEntity, http.MethodGet, "/v1/admin/billing/rate-overrides?org_id=nope", e.super, "", "")
}

// ─── accounts and terms ───────────────────────────────────────────────────

func TestBillingAdmin_AccountTermsAndFlags(t *testing.T) {
	e := newBillingAdminEnv(t)
	ctx := context.Background()
	acc := e.account(t)
	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	path := "/v1/admin/billing/accounts/" + acc.ID.String() + "/terms"
	terms := func(from time.Time, code, plan, cycle string) string {
		return fmt.Sprintf(`{"effective_from":%q,"price_list_code":%q,"plan_kind":%q,"billing_cycle":%q,"shortfall_policy":"invoice_postpaid","due_days":14}`,
			rfc(from), code, plan, cycle)
	}

	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, path, e.super, "b15", terms(time.Now().Add(-time.Hour), billing.StandardPriceListCode, "venue", "monthly"))
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, path, e.super, "b15", terms(future, billing.StandardPriceListCode, "weird", "monthly"))
	if c := billingErrCode(e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, path, e.super, "b15", terms(future, "b15-no-such-list", "standard", "monthly"))); c != "billing.price_list_not_found" {
		t.Errorf("unknown price list: %q", c)
	}
	e.expect(t, http.StatusNotFound, http.MethodPost, "/v1/admin/billing/accounts/"+uuid.NewString()+"/terms", e.super, "b15", terms(future, billing.StandardPriceListCode, "venue", "monthly"))

	// A list with no venue rule cannot carry a venue plan.
	noVenue := e.code()
	e.insertVersionAt(t, noVenue, 1, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 70, 120)
	if c := billingErrCode(e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, path, e.super, "b15", terms(future, noVenue, "venue", "monthly"))); c != "billing.price_list_lacks_rule" {
		t.Errorf("venue plan on a list without a venue rule: %q", c)
	}
	// The seeded list has one: the venue flag is set through the screen.
	out := e.expect(t, http.StatusCreated, http.MethodPost, path, e.super, "b15 permanent venue", terms(future, billing.StandardPriceListCode, "venue", "monthly"))
	if jsonField(t, out, "plan_kind") != "venue" {
		t.Fatalf("terms = %s", out)
	}
	// Terms must start after the latest.
	if c := billingErrCode(e.expect(t, http.StatusConflict, http.MethodPost, path, e.super, "b15", terms(future, billing.StandardPriceListCode, "standard", "monthly"))); c != "billing.terms_not_after_latest" {
		t.Errorf("not after latest: %q", c)
	}

	// The venue plan applies from its date, not before.
	before, err := billing.TermsAt(ctx, e.q, acc.ID, future.Add(-time.Second))
	if err != nil || before.PlanKind() != billing.PlanStandard {
		t.Errorf("before the date: %v %v", before.PlanKind(), err)
	}
	after, err := billing.TermsAt(ctx, e.q, acc.ID, future)
	if err != nil || after.PlanKind() != billing.PlanVenue {
		t.Errorf("from the date: %v %v", after.PlanKind(), err)
	}

	// The list: flags from rows. Make the venue terms current by moving them back,
	// and give the organization an individual rate and a tools account.
	if _, err := e.srv.pgxPool.Exec(ctx, `UPDATE billing_account_terms SET effective_from = now() - interval '1 hour' WHERE account_id = $1 AND plan_kind = 'venue'`, acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, created_by)
VALUES ($1, 'EUR', 5, 6, now() - interval '1 day', 'fixture')`, e.orgID); err != nil {
		t.Fatal(err)
	}
	var toolsID uuid.UUID
	if err := e.srv.pgxPool.QueryRow(ctx, `
INSERT INTO billing_accounts (org_id, currency, issuer_id, account_kind) VALUES ($1, 'USD', $2, 'tools') RETURNING id`,
		e.orgID, acc.IssuerID).Scan(&toolsID); err != nil {
		t.Fatal(err)
	}
	list := e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/billing/accounts?org_id="+e.orgID.String(), e.super, "", "")
	var l struct {
		Items []struct {
			ID    string `json:"id"`
			Kind  string `json:"account_kind"`
			Flags struct {
				Venue          bool `json:"venue"`
				Tools          bool `json:"tools"`
				ExistingClient bool `json:"existing_client"`
			} `json:"flags"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(list), &l); err != nil || len(l.Items) != 2 {
		t.Fatalf("accounts = %s (%v)", list, err)
	}
	for _, it := range l.Items {
		switch it.ID {
		case acc.ID.String():
			if !it.Flags.Venue || it.Flags.Tools || !it.Flags.ExistingClient {
				t.Errorf("organizer account flags = %+v", it.Flags)
			}
		case toolsID.String():
			if it.Flags.Venue || !it.Flags.Tools || !it.Flags.ExistingClient {
				t.Errorf("tools account flags = %+v", it.Flags)
			}
		}
	}

	// A tools account is prepaid only. It has no terms yet, so the first row is free to name the list.
	toolsPath := "/v1/admin/billing/accounts/" + toolsID.String() + "/terms"
	if _, err := e.srv.pgxPool.Exec(ctx, `
INSERT INTO billing_account_terms (account_id, effective_from, price_list_code, plan_kind, billing_cycle, shortfall_policy, issuer_id, created_by, reason)
VALUES ($1, '2026-01-01T00:00:00Z', 'standard-2026', 'standard', 'prepaid', 'invoice_postpaid', $2, 'fixture', 'b15')`, toolsID, acc.IssuerID); err != nil {
		t.Fatal(err)
	}
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPost, toolsPath, e.super, "b15", terms(future, billing.StandardPriceListCode, "standard", "monthly"))
	e.expect(t, http.StatusCreated, http.MethodPost, toolsPath, e.super, "b15", terms(future, billing.StandardPriceListCode, "standard", "prepaid"))
}

// ─── audit ────────────────────────────────────────────────────────────────

// Every write route writes exactly one audit row with the operator's id and
// the X-Admin-Reason, and a refused call writes none. The routes tested are
// the ones billingAdminWriteRoutes names (the unit guard keeps the two in step).
func TestBillingAdmin_EveryWriteIsAudited(t *testing.T) {
	e := newBillingAdminEnv(t)
	acc := e.account(t)
	code := e.code()
	e.insertVersionAt(t, code, 1, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 70, 120)
	future := time.Now().UTC().Add(96 * time.Hour).Truncate(time.Second)

	type step struct {
		route  string // key into billingAdminWriteRoutes
		method string
		path   func() string
		body   string
		want   int
		// resource is the audit resource_id, learned from the answer.
		resource func(resp string) string
	}
	var v2ID, rateID string
	fixed := func(p string) func() string { return func() string { return p } }
	steps := []step{
		{"POST /v1/admin/billing/price-lists", http.MethodPost, fixed("/v1/admin/billing/price-lists"),
			priceListBody(code, rfc(future), fixedRule(80, 130)), http.StatusCreated,
			func(r string) string { v2ID = jsonField(t, r, "id"); return v2ID }},
		{"POST /v1/admin/billing/rate-overrides", http.MethodPost, fixed("/v1/admin/billing/rate-overrides"),
			fmt.Sprintf(`{"org_id":%q,"currency":"EUR","unseated_minor":3,"seated_minor":4,"effective_from":%q}`, e.orgID, rfc(future)), http.StatusCreated,
			func(r string) string { rateID = jsonField(t, r, "id"); return rateID }},
		{"POST /v1/admin/billing/rate-overrides/{id}/close", http.MethodPost,
			func() string { return "/v1/admin/billing/rate-overrides/" + rateID + "/close" },
			fmt.Sprintf(`{"effective_to":%q}`, rfc(future.Add(24*time.Hour))), http.StatusOK,
			func(string) string { return rateID }},
		{"DELETE /v1/admin/billing/rate-overrides/{id}", http.MethodDelete,
			func() string { return "/v1/admin/billing/rate-overrides/" + rateID }, "", http.StatusNoContent,
			func(string) string { return rateID }},
		{"POST /v1/admin/billing/accounts/{account_id}/terms", http.MethodPost,
			fixed("/v1/admin/billing/accounts/" + acc.ID.String() + "/terms"),
			fmt.Sprintf(`{"effective_from":%q,"price_list_code":%q,"plan_kind":"standard","billing_cycle":"monthly","shortfall_policy":"block_publishing"}`, rfc(future), code), http.StatusCreated,
			func(string) string { return acc.ID.String() }},
		{"POST /v1/admin/billing/price-lists/{id}/revoke", http.MethodPost,
			func() string { return "/v1/admin/billing/price-lists/" + v2ID + "/revoke" }, "", http.StatusOK,
			func(string) string { return v2ID }},
	}
	covered := map[string]bool{}
	for _, s := range steps {
		path := s.path()
		reason := "b15 audit " + strings.ToLower(s.method)
		out := e.expect(t, s.want, s.method, path, e.super, reason, s.body)
		res := s.resource(out)
		action := billingAdminWriteRoutes[s.route]
		n, actor, gotReason := e.auditRows(t, action, res)
		if n != 1 || actor != e.superID.String() || gotReason != reason {
			t.Errorf("%s: %d audit rows (action %s, resource %s), actor %q reason %q; want 1 row, actor %s, reason %q",
				s.route, n, action, res, actor, gotReason, e.superID, reason)
		}
		covered[s.route] = true
	}
	for route := range billingAdminWriteRoutes {
		if !covered[route] {
			t.Errorf("the audit test does not exercise %s", route)
		}
	}
}
