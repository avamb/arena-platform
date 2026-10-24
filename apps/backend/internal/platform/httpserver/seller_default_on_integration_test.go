//go:build integration

package httpserver

// P1a (owner decision 2026-10-14): the seller block of the hosted sales page
// is shown BY DEFAULT as soon as the profile is complete, with no flag set,
// and only a platform superadmin can hide it (settings.hosted_page.seller_hidden,
// PATCH .../seller-display). The legacy show_seller flag decides nothing.
// GET .../seller-profile reports complete / seller_shown, and the answer
// agrees with what the public page does.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// publishThroughChannel gives the fixture's organization a published event
// behind an active feed token of its channel and returns the public page path.
func (f *sellerAccessFixture) publishThroughChannel(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	q := gen.New(f.srv.pgxPool)
	run := uuid.NewString()
	var orgSlug string
	if err := f.srv.pgxPool.QueryRow(ctx, `SELECT slug FROM organizations WHERE id = $1`, f.org).Scan(&orgSlug); err != nil {
		t.Fatalf("org slug: %v", err)
	}
	desc := "default-on seller test"
	event, err := q.InsertEvent(ctx, f.org, "Default On "+run, &desc, "published", "public", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	eventSlug := "default-on-" + run
	if _, err := q.UpdateEventMetadata(ctx, event.ID, f.org, &eventSlug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}
	ft, err := q.InsertFeedToken(ctx, "default-on-"+run, f.channel, "default-on test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	if _, err := q.PublishEvent(ctx, event.ID, ft.ID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.srv.pgxPool.Exec(c, `DELETE FROM event_publications WHERE event_id = $1`, event.ID)
		_, _ = f.srv.pgxPool.Exec(c, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, f.channel)
		_, _ = f.srv.pgxPool.Exec(c, `DELETE FROM events WHERE id = $1`, event.ID)
	})
	return "/v1/public/pages/" + orgSlug + "/" + eventSlug
}

// pageSeller reads the public page and reports whether it carries a seller.
func (f *sellerAccessFixture) pageSeller(t *testing.T, pagePath string) bool {
	t.Helper()
	resp, err := f.ts.Client().Get(f.ts.URL + pagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page = %d: %s", resp.StatusCode, body)
	}
	return strings.Contains(string(body), `"seller":{`)
}

func TestSellerDefaultOn_CompleteProfileShowsAndOnlyTheOperatorHides(t *testing.T) {
	f := newSellerAccessFixture(t)
	ctx := context.Background()
	base := "/v1/organizations/" + f.org.String()
	display := base + "/channels/" + f.channel.String() + "/seller-display"
	pagePath := f.publishThroughChannel(t)
	owner := f.member(t, "org_admin")
	sa := f.member(t, "superadmin")

	// The fixture's channel carries a complete profile and the LEGACY flag on.
	// Take the flag away: nothing may change, the page decides on the data.
	if _, err := f.srv.pgxPool.Exec(ctx, `UPDATE sales_channels
		SET settings = '{"hosted_page":{"enabled":true,"public_contact":{"email":"tickets@teatro.example"}}}'::jsonb
		WHERE id = $1`, f.channel); err != nil {
		t.Fatal(err)
	}

	// 1. A complete profile, no flag at all: the page shows the seller, and
	// the profile says so with the same words.
	if !f.pageSeller(t, pagePath) {
		t.Fatal("a complete profile with no flag must show the seller block")
	}
	st, out := f.do(t, http.MethodGet, base+"/seller-profile", owner, "", "")
	if st != http.StatusOK {
		t.Fatalf("GET profile: %d %v", st, out)
	}
	prof, _ := out["seller_profile"].(map[string]any)
	chans, _ := prof["channels"].([]any)
	ch, _ := chans[0].(map[string]any)
	if prof["complete"] != true || prof["seller_shown"] != true || ch["complete"] != true || ch["seller_shown"] != true || ch["seller_hidden"] != false {
		t.Fatalf("profile must report shown: %v", prof)
	}

	// 2. The owner cannot hide it: 403, and the page still shows it. The
	// legacy flag set to false by the owner changes nothing either.
	st, out = f.do(t, http.MethodPatch, display, owner, "", `{"seller_hidden": true}`)
	if st != http.StatusForbidden || prom0113Code(out) != "seller.hide_superadmin_only" {
		t.Fatalf("owner hides: %d %v, want 403 seller.hide_superadmin_only", st, out)
	}
	if got := f.setting(t, "hosted_page,seller_hidden"); got != "<nil>" {
		t.Errorf("a refused hide wrote seller_hidden = %q", got)
	}
	if st, out = f.do(t, http.MethodPatch, display, owner, "", `{"show_seller": false}`); st != http.StatusOK {
		t.Fatalf("legacy flag off: %d %v", st, out)
	}
	if !f.pageSeller(t, pagePath) {
		t.Fatal("the legacy show_seller flag must not hide the block")
	}
	// Repeating the stored value (false) is not a change and is allowed.
	if st, out = f.do(t, http.MethodPatch, display, owner, "", `{"seller_hidden": false}`); st != http.StatusOK {
		t.Errorf("owner repeats seller_hidden=false: %d %v", st, out)
	}

	// 3. A platform superadmin (with a reason) hides it: the page drops the
	// block and the profile says "hidden by the operator", not "incomplete".
	if st, out = f.do(t, http.MethodPatch, display, sa, "", `{"seller_hidden": true}`); st != http.StatusBadRequest || prom0113Code(out) != "superadmin.missing_reason" {
		t.Errorf("superadmin without reason: %d %v", st, out)
	}
	st, out = f.do(t, http.MethodPatch, display, sa, "operator hides the seller block", `{"seller_hidden": true}`)
	if st != http.StatusOK {
		t.Fatalf("superadmin hides: %d %v", st, out)
	}
	if got := f.setting(t, "hosted_page,seller_hidden"); got != "true" {
		t.Fatalf("seller_hidden = %q", got)
	}
	if !strings.Contains(f.setting(t, "hosted_page,public_contact,email"), "tickets@") {
		t.Error("hiding must keep the public contact")
	}
	if f.pageSeller(t, pagePath) {
		t.Fatal("the page still shows the seller after the operator hid it")
	}
	prof, _ = out["seller_profile"].(map[string]any)
	chans, _ = prof["channels"].([]any)
	ch, _ = chans[0].(map[string]any)
	if prof["complete"] != true || prof["seller_shown"] != false || ch["seller_hidden"] != true || ch["complete"] != true || ch["seller_shown"] != false {
		t.Fatalf("hidden profile: %v", prof)
	}

	// 4. The generic channel PATCH cannot unhide it behind the route's back.
	chPatch := base + "/channels/" + f.channel.String()
	f.do(t, http.MethodPatch, chPatch, owner, "", `{"settings":{"hosted_page":{"enabled":true,"seller_hidden":false}}}`)
	if got := f.setting(t, "hosted_page,seller_hidden"); got != "true" {
		t.Errorf("channel PATCH changed seller_hidden to %q", got)
	}
	// The owner cannot show it again either.
	if st, out = f.do(t, http.MethodPatch, display, owner, "", `{"seller_hidden": false}`); st != http.StatusForbidden {
		t.Errorf("owner unhides: %d %v, want 403", st, out)
	}
	if f.pageSeller(t, pagePath) {
		t.Fatal("the owner brought the block back")
	}

	// 5. The superadmin shows it again.
	if st, out = f.do(t, http.MethodPatch, display, sa, "operator shows the seller block", `{"seller_hidden": false}`); st != http.StatusOK {
		t.Fatalf("superadmin unhides: %d %v", st, out)
	}
	if got := f.setting(t, "hosted_page,seller_hidden"); got != "<nil>" {
		t.Errorf("seller_hidden after unhide = %q, want the key removed", got)
	}
	if !f.pageSeller(t, pagePath) {
		t.Fatal("the page must show the seller after the operator showed it again")
	}
}

func TestSellerDefaultOn_IncompleteProfileIsNotShownAndSaysWhy(t *testing.T) {
	f := newSellerAccessFixture(t)
	ctx := context.Background()
	base := "/v1/organizations/" + f.org.String()
	pagePath := f.publishThroughChannel(t)
	owner := f.member(t, "org_admin")

	if !f.pageSeller(t, pagePath) {
		t.Fatal("precondition: the fixture profile is complete")
	}
	// Emptying one required field takes the block off the page at once, with
	// no write to any channel flag, and the profile agrees.
	if _, err := f.srv.pgxPool.Exec(ctx, `UPDATE organizations SET tax_id = NULL, tax_id_scheme = NULL WHERE id = $1`, f.org); err != nil {
		t.Fatal(err)
	}
	if f.pageSeller(t, pagePath) {
		t.Fatal("an incomplete profile must not show the seller block")
	}
	st, out := f.do(t, http.MethodGet, base+"/seller-profile", owner, "", "")
	if st != http.StatusOK {
		t.Fatalf("GET profile: %d %v", st, out)
	}
	prof, _ := out["seller_profile"].(map[string]any)
	chans, _ := prof["channels"].([]any)
	ch, _ := chans[0].(map[string]any)
	missing, _ := prof["missing"].([]any)
	if prof["complete"] != false || prof["seller_shown"] != false || ch["complete"] != false || ch["seller_shown"] != false || ch["seller_hidden"] != false ||
		len(missing) != 1 || missing[0] != "tax_id" {
		t.Fatalf("incomplete profile: %v", prof)
	}
	// The e-mail of the channel is part of completeness too.
	if _, err := f.srv.pgxPool.Exec(ctx, `UPDATE organizations SET tax_id = 'B12345678', tax_id_scheme = 'other' WHERE id = $1`, f.org); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.pgxPool.Exec(ctx, `UPDATE sales_channels SET settings = '{"hosted_page":{"enabled":true}}'::jsonb WHERE id = $1`, f.channel); err != nil {
		t.Fatal(err)
	}
	if f.pageSeller(t, pagePath) {
		t.Fatal("a channel without a public e-mail must not show the seller block")
	}
	st, out = f.do(t, http.MethodGet, base+"/seller-profile", owner, "", "")
	prof, _ = out["seller_profile"].(map[string]any)
	chans, _ = prof["channels"].([]any)
	ch, _ = chans[0].(map[string]any)
	if st != http.StatusOK || prof["complete"] != true || prof["seller_shown"] != false || ch["complete"] != false {
		t.Fatalf("channel without e-mail: %d %v", st, prof)
	}
}
