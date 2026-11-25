//go:build integration

package httpserver

// checkout_marketing_integration_test.go — the thank-you page's subscription
// (POST /v1/public/checkout/{token}/marketing), marketing_offer on the
// order status and page_url of the public feed's events.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/marketing"
)

func TestCheckoutMarketing_ThankYouSubscription(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "tym")
	defer f.cleanup()
	defer growthCleanup(t, f)()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	code, body := f.startWith(t, srv, f.buyerMail, "", nil)
	if code != http.StatusCreated {
		t.Fatalf("checkout/start = %d: %s", code, body)
	}
	var start hostedStartResponse
	if err := json.Unmarshal(body, &start); err != nil {
		t.Fatal(err)
	}
	path := "/v1/public/checkout/" + start.CheckoutToken + "/marketing"
	ver := marketing.ThankYouTextVersion
	// A page on the platform's tickets origin is NOT one of the channel's site
	// origins, so the return address falls back to the hosted tickets page.
	req := map[string]any{"text_version": ver, "page_url": hostedTicketsBaseURL + "/shows/thanks?x=1"}
	setOptIn := func(on bool) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO org_growth_settings (org_id, cart_reminder_enabled, marketing_optin_enabled)
			VALUES ($1, true, $2)
			ON CONFLICT (org_id) DO UPDATE SET marketing_optin_enabled = $2`, f.orgID, on); err != nil {
			t.Fatal(err)
		}
	}
	jobs := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'subscription_id' IN
			(SELECT id::text FROM marketing_subscriptions WHERE org_id = $2)`, marketing.JobTypeConfirmEmail, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	status := func() map[string]any {
		rec := growthGet(srv, "/v1/public/checkout/"+start.CheckoutToken)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("status = %d %v: %s", rec.Code, err, rec.Body.String())
		}
		return out
	}

	offerOf := func() any {
		v, ok := status()["marketing_offer"]
		if !ok {
			t.Fatal("marketing_offer is missing from the status")
		}
		return v
	}

	// The organizer does not offer mailings (no settings row): 409, no offer.
	if c, raw := growthPost(t, srv, path, req); c != http.StatusConflict || !strings.Contains(string(raw), "marketing.not_offered") {
		t.Fatalf("not offered = %d %s, want 409 marketing.not_offered", c, raw)
	}
	if v := offerOf(); v != nil {
		t.Fatalf("marketing_offer without the organizer's tick = %v, want null", v)
	}
	if n := jobs(); n != 0 {
		t.Fatalf("confirmation jobs for a not offered org = %d", n)
	}
	setOptIn(true)

	// Unknown token, then an unpaid order.
	if c, _ := growthPost(t, srv, "/v1/public/checkout/"+strings.Repeat("a", 64)+"/marketing", req); c != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", c)
	}
	if c, raw := growthPost(t, srv, path, req); c != http.StatusConflict || !strings.Contains(string(raw), "checkout.not_paid") {
		t.Fatalf("unpaid = %d %s, want 409 checkout.not_paid", c, raw)
	}
	if v := offerOf(); v != nil {
		t.Fatalf("marketing_offer of an unpaid order = %v, want null", v)
	}

	// Bad bodies.
	if c, _ := growthPost(t, srv, path, map[string]any{"text_version": "x"}); c != http.StatusBadRequest {
		t.Fatalf("short text_version = %d, want 400", c)
	}
	if c, raw := growthPost(t, srv, path, map[string]any{"text_version": "thankyou-v1"}); c != http.StatusBadRequest || !strings.Contains(string(raw), "checkout.invalid_text_version") {
		t.Fatalf("another text_version = %d %s, want 400 checkout.invalid_text_version", c, raw)
	}
	if c, raw := growthPost(t, srv, path, map[string]any{"text_version": marketing.TextVersion}); c != http.StatusBadRequest || !strings.Contains(string(raw), "checkout.invalid_text_version") {
		t.Fatalf("the checkout tick's version = %d %s, want 400", c, raw)
	}
	if c, _ := growthPost(t, srv, path, map[string]any{"text_version": ver, "page_url": "https://u:p@x.example/"}); c != http.StatusBadRequest {
		t.Fatalf("page_url with user info = %d, want 400", c)
	}
	if c, _ := growthPost(t, srv, path, map[string]any{"text_version": ver, "email": "a@b.example"}); c != http.StatusBadRequest {
		t.Fatalf("e-mail in the body = %d, want 400", c)
	}

	// Paid: the offer names the organizer and the only accepted version.
	if _, err := pool.Exec(ctx, `UPDATE orders SET status = 'paid', paid_at = now() WHERE org_id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	var orgName string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(NULLIF(btrim(legal_name), ''), name) FROM organizations WHERE id = $1`, f.orgID).Scan(&orgName); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(offerOf()); string(got) != `{"org_name":`+quoteJSON(orgName)+`,"text_version":"`+ver+`"}` {
		t.Fatalf("marketing_offer = %s", got)
	}

	// 202 pending, one confirmation job, the repeat is absorbed.
	c, raw := growthPost(t, srv, path, req)
	if c != http.StatusAccepted || strings.TrimSpace(string(raw)) != `{"status":"pending"}` {
		t.Fatalf("paid = %d %s", c, raw)
	}
	if n := jobs(); n != 1 {
		t.Fatalf("confirmation jobs = %d, want 1", n)
	}
	var src, page string
	if err := pool.QueryRow(ctx, `SELECT source, COALESCE(page_url, '') FROM marketing_subscriptions WHERE org_id = $1 AND email_normalized = lower($2)`,
		f.orgID, f.buyerMail).Scan(&src, &page); err != nil || src != "site" || page != hostedTicketsBaseURL {
		t.Fatalf("subscription source %q page %q: %v", src, page, err)
	}
	c, raw = growthPost(t, srv, path, req)
	if c != http.StatusAccepted || strings.TrimSpace(string(raw)) != `{"status":"pending"}` {
		t.Fatalf("repeat = %d %s", c, raw)
	}
	if n := jobs(); n != 1 {
		t.Fatalf("confirmation jobs after the repeat = %d, want 1", n)
	}
	if v := offerOf(); v != nil {
		t.Fatalf("marketing_offer with a pending subscription = %v, want null", v)
	}

	// Confirmed: nothing is written.
	if _, err := pool.Exec(ctx, `UPDATE marketing_subscriptions SET status = 'confirmed' WHERE org_id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE org_id = $1`, f.orgID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	c, raw = growthPost(t, srv, path, req)
	if c != http.StatusAccepted || strings.TrimSpace(string(raw)) != `{"status":"already_confirmed"}` {
		t.Fatalf("confirmed = %d %s", c, raw)
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE org_id = $1`, f.orgID).Scan(&after); err != nil || after != before {
		t.Fatalf("journal rows %d -> %d, %v", before, after, err)
	}
	if v := offerOf(); v != nil {
		t.Fatalf("marketing_offer with a confirmed subscription = %v, want null", v)
	}

	// A page on one of the CHANNEL's site origins is the return address as is
	// (query and fragment dropped).
	for _, s := range []string{
		`DELETE FROM worker_jobs WHERE job_type = 'marketing.confirm_email' AND payload->>'subscription_id' IN
		   (SELECT id::text FROM marketing_subscriptions WHERE org_id = $1)`,
		`DELETE FROM marketing_consents WHERE org_id = $1`,
		`DELETE FROM marketing_subscriptions WHERE org_id = $1`,
	} {
		if _, err := pool.Exec(ctx, s, f.orgID); err != nil {
			t.Fatal(err)
		}
	}
	if v := offerOf(); v == nil {
		t.Fatal("marketing_offer after the subscription was removed = null, want the offer")
	}
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = COALESCE(settings, '{}'::jsonb) || '{"site":{"origins":["https://shop.arena-integration.test"]}}'::jsonb WHERE id = $1`, f.channelID); err != nil {
		t.Fatal(err)
	}
	site := map[string]any{"text_version": ver, "page_url": "https://shop.arena-integration.test/thanks?x=1#a"}
	if c, raw := growthPost(t, srv, path, site); c != http.StatusAccepted {
		t.Fatalf("site page = %d %s", c, raw)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(page_url, '') FROM marketing_subscriptions WHERE org_id = $1`, f.orgID).Scan(&page); err != nil || page != "https://shop.arena-integration.test/thanks" {
		t.Fatalf("page of a channel origin = %q, %v", page, err)
	}

	// The organizer switches the tick off, then the organization is deleted.
	setOptIn(false)
	if c, raw := growthPost(t, srv, path, req); c != http.StatusConflict || !strings.Contains(string(raw), "marketing.not_offered") {
		t.Fatalf("switched off = %d %s, want 409", c, raw)
	}
	setOptIn(true)
	if _, err := pool.Exec(ctx, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `UPDATE organizations SET deleted_at = NULL WHERE id = $1`, f.orgID)
	}()
	if c, raw := growthPost(t, srv, path, req); c != http.StatusNotFound {
		t.Fatalf("deleted org = %d %s, want 404", c, raw)
	}
	if v := offerOf(); v != nil {
		t.Fatalf("marketing_offer of a deleted org = %v, want null", v)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestPublicFeedEvents_PageURL(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	f := newHostedOrgFixture(t, ctx, pool, "pgu")
	defer f.cleanup()

	stripe := newStubStripe(t)
	srv := buildHostedCheckoutServer(t, pool, stripe.baseURL())

	var orgSlug string
	if err := pool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, f.orgID).Scan(&orgSlug); err != nil {
		t.Fatal(err)
	}
	// No event slug: empty address.
	if _, err := pool.Exec(ctx, `UPDATE events SET slug = NULL WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	read := func(path string, single bool) string {
		rec := growthGet(srv, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if single {
			var out struct {
				Event struct {
					PageURL *string `json:"page_url"`
				} `json:"event"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Event.PageURL == nil {
				t.Fatalf("single event page_url missing (%v): %s", err, rec.Body.String())
			}
			return *out.Event.PageURL
		}
		var out struct {
			Events []struct {
				ID      string  `json:"id"`
				PageURL *string `json:"page_url"`
			} `json:"events"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Events) == 0 {
			t.Fatalf("list (%v): %s", err, rec.Body.String())
		}
		for _, e := range out.Events {
			if e.ID == f.eventID.String() {
				if e.PageURL == nil {
					t.Fatalf("page_url missing: %s", rec.Body.String())
				}
				return *e.PageURL
			}
		}
		t.Fatalf("event not in the list: %s", rec.Body.String())
		return ""
	}
	list := "/v1/public/feeds/" + f.feedToken + "/events"
	one := list + "/" + f.eventID.String()
	if got := read(list, false); got != "" {
		t.Fatalf("page_url without a slug = %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET slug = 'page-url-show' WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	want := hostedTicketsBaseURL + "/" + orgSlug + "/page-url-show"
	if got := read(list, false); got != want {
		t.Fatalf("list page_url = %q, want %q", got, want)
	}
	if got := read(one, true); got != want {
		t.Fatalf("event page_url = %q, want %q", got, want)
	}
}
