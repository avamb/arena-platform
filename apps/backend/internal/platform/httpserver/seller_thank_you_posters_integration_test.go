//go:build integration

package httpserver

// The posters switch of the thank-you page's "You may also like" block:
// PATCH .../seller-display writes settings.site.thank_you_posters, absent
// keeps it, the profile reports it (true by default), every other setting
// survives, and the generic channel PATCH cannot overwrite it.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSellerDisplay_ThankYouPosters(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	call := sellerCaller(t, srv)
	ctx := context.Background()
	hosted, _ := sellerChannelFixture(t, f, "TYP")
	base := "/v1/organizations/" + f.orgID.String()
	display := base + "/channels/" + hosted.String() + "/seller-display"

	stored := func() string {
		var v string
		_ = pool.QueryRow(ctx, `SELECT COALESCE(settings #>> '{site,thank_you_posters}', 'absent') FROM sales_channels WHERE id = $1`, hosted).Scan(&v)
		return v
	}
	posters := func() bool {
		code, raw := call(f.ownerID, http.MethodGet, base+"/seller-profile", "")
		if code != http.StatusOK {
			t.Fatalf("GET profile: %d %s", code, raw)
		}
		return decodeSellerProfile(t, raw).Channels[0].ThankYouPosters
	}

	if !posters() || stored() != "absent" {
		t.Fatalf("default must be on and unstored, got posters=%v stored=%s", posters(), stored())
	}
	if code, raw := call(f.ownerID, http.MethodPatch, display, `{"thank_you_posters": false}`); code != http.StatusOK || !strings.Contains(string(raw), `"thank_you_posters":false`) {
		t.Fatalf("switch off: %d %s", code, raw)
	}
	if stored() != "false" || posters() {
		t.Fatalf("after off: stored=%s posters=%v", stored(), posters())
	}
	// Absent keeps; another key does not touch it; other settings survive.
	if code, raw := call(f.ownerID, http.MethodPatch, display, `{"public_contact": {"phone": "+34 600 000 000"}}`); code != http.StatusOK {
		t.Fatalf("contact: %d %s", code, raw)
	}
	if stored() != "false" {
		t.Errorf("an absent member must keep the value, got %s", stored())
	}
	var other string
	_ = pool.QueryRow(ctx, `SELECT settings #>> '{other_key}' FROM sales_channels WHERE id = $1`, hosted).Scan(&other)
	if other != "stays" {
		t.Errorf("other settings lost: %q", other)
	}
	// null is refused.
	if code, raw := call(f.ownerID, http.MethodPatch, display, `{"thank_you_posters": null}`); code != http.StatusBadRequest {
		t.Errorf("null: %d %s", code, raw)
	}
	// The generic channel PATCH cannot overwrite it.
	if code, raw := call(f.ownerID, http.MethodPatch, base+"/channels/"+hosted.String(), `{"settings": {"hosted_page": {"enabled": true}, "site": {"thank_you_posters": true}}}`); code != http.StatusOK {
		t.Fatalf("generic patch: %d %s", code, raw)
	}
	if stored() != "false" {
		t.Errorf("the generic route changed the switch: %s", stored())
	}
	if code, raw := call(f.ownerID, http.MethodPatch, display, `{"thank_you_posters": true}`); code != http.StatusOK || stored() != "true" || !posters() {
		t.Fatalf("switch on: %d %s stored=%s", code, raw, stored())
	}
}
