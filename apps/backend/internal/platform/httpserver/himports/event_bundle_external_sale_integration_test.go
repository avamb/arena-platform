//go:build integration

package himports

// event_bundle_external_sale_integration_test.go — spec 63 §14.4 (migration
// 0221): actionEvent.externalSale of the arena event bundle. An object hands
// the session to a partner box office (no categoryList needed), the read
// gives it back, absent keeps the mode, null puts the session back on the
// platform, and the lock rules and the address check answer the house errors.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f *bundle525Fixture) salesMode(ctx context.Context, t *testing.T, sessionID uuid.UUID) (mode string, url *string, minor *int64) {
	t.Helper()
	if err := f.pool.QueryRow(ctx, `SELECT sales_mode, external_ticket_url, display_price_from_minor
	  FROM sessions WHERE id = $1`, sessionID).Scan(&mode, &url, &minor); err != nil {
		t.Fatalf("read sales mode: %v", err)
	}
	return mode, url, minor
}

func TestEventBundleExternalSale_ImportReadAndRoundTrip(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	// An object with no categoryList creates a partner-sold session.
	ext := f.payload()
	ext.CategoryList = nil
	ext.ActionEvent.ExternalSale = []byte(`{"url":"https://www.eventim.de/event/1","sellerName":"Eventim","priceFrom":39.5,"currency":"EUR"}`)
	// The dry run of a NEW partner-sold session must not ask for capacity: the
	// event center's wizard runs it before every save (staging 2026-11-23).
	if dry := dryRun(t, f, h, ext); !dry.Valid {
		t.Fatalf("dry run refuses a new external session: %+v", dry.Errors)
	}
	rec, created := f.call(h, ext)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create external: %d %s", rec.Code, rec.Body.String())
	}
	mode, url, minor := f.salesMode(ctx, t, created.SessionID)
	if mode != "external" || url == nil || *url != "https://www.eventim.de/event/1" || minor == nil || *minor != 3950 {
		t.Fatalf("stored = %q %v %v", mode, url, minor)
	}
	var tiers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, created.SessionID).Scan(&tiers); err != nil || tiers != 0 {
		t.Fatalf("an external session owns %d tiers (%v)", tiers, err)
	}

	// The read gives the block back, and what was read round-trips.
	first, raw := f.readBundles(h, created.EventID)
	if len(first.Bundles) != 1 || !strings.Contains(string(first.Bundles[0].ActionEvent.ExternalSale), `"sellerName":"Eventim"`) {
		t.Fatalf("read does not carry externalSale: %s", raw)
	}
	before := catalogSnapshot(ctx, t, pool, f.orgID)
	if dry := dryRun(t, f, h, first.Bundles[0]); !dry.Valid {
		t.Fatalf("dry run refuses the bundle that was read: %+v", dry.Errors)
	}
	f.reimport(h, first)
	if after := catalogSnapshot(ctx, t, pool, f.orgID); after != before {
		t.Fatalf("re-importing the read bundle changed rows:\nbefore %s\nafter  %s", before, after)
	}

	// An external session takes no categories: 409 session.sold_externally.
	withCats := first.Bundles[0]
	withCats.CategoryList = f.payload().CategoryList
	rec, _ = f.call(h, withCats)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session.sold_externally") {
		t.Fatalf("categories on an external session: %d %s", rec.Code, rec.Body.String())
	}

	// A refused address is 422 import.invalid_external_sale, found by the dry
	// run too, and writes nothing.
	bad := first.Bundles[0]
	bad.ActionEvent.ExternalSale = []byte(`{"url":"http://203.0.113.9/x","sellerName":"Eventim"}`)
	rec, _ = f.call(h, bad)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_external_sale") {
		t.Fatalf("bad address: %d %s", rec.Code, rec.Body.String())
	}
	dry := dryRun(t, f, h, bad)
	found := false
	for _, e := range dry.Errors {
		if e.Code == "import.invalid_external_sale" && e.Field == "/actionEvent/externalSale/url" {
			found = true
		}
	}
	if dry.Valid || !found {
		t.Fatalf("dry run missed the bad address: %+v", dry.Errors)
	}

	// null puts the session back on the platform (nothing blocks it), with
	// the categories of the same bundle.
	back := first.Bundles[0]
	back.ActionEvent.ExternalSale = []byte("null")
	back.CategoryList = f.payload().CategoryList
	if rec, _ := f.call(h, back); rec.Code != http.StatusOK {
		t.Fatalf("back to platform: %d %s", rec.Code, rec.Body.String())
	}
	if mode, url, minor := f.salesMode(ctx, t, created.SessionID); mode != "platform" || url != nil || minor != nil {
		t.Fatalf("after null: %q %v %v", mode, url, minor)
	}

	// Now it owns categories: handing it to a partner is locked.
	lock := first.Bundles[0]
	lock.CategoryList = nil
	rec, _ = f.call(h, lock)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "session.sales_mode_locked") {
		t.Fatalf("locked switch: %d %s", rec.Code, rec.Body.String())
	}
	if mode, _, _ := f.salesMode(ctx, t, created.SessionID); mode != "platform" {
		t.Fatalf("a refused switch left the mode %q", mode)
	}
}

// A bundle re-sent for a session a partner already sells, with no externalSale
// and no categoryList (what a client that does not touch the sales mode sends),
// keeps the session external: absent means untouched, and the missing
// categories are not an error. The same bundle for a platform session is still
// refused with import.categories_required.
func TestEventBundleExternalSale_AbsentKeepsExternalAndNeedsNoCategories(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	ext := f.payload()
	ext.CategoryList = nil
	ext.ActionEvent.ExternalSale = []byte(`{"url":"https://www.eventim.de/event/2","sellerName":"Eventim"}`)
	rec, created := f.call(h, ext)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create external: %d %s", rec.Code, rec.Body.String())
	}

	again := f.payload()
	again.CategoryList = nil
	again.ActionEvent.ExternalSale = nil
	again.ActionEvent.ArenaSessionID = created.SessionID.String()
	again.ExternalRef = ""
	rec, _ = f.call(h, again)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-send without externalSale and categories: %d %s", rec.Code, rec.Body.String())
	}
	if mode, url, _ := f.salesMode(ctx, t, created.SessionID); mode != "external" || url == nil || *url != "https://www.eventim.de/event/2" {
		t.Fatalf("an absent externalSale changed the session: %q %v", mode, url)
	}

	// A platform session addressed the same way still needs its categories.
	plain := f.payload()
	plain.ExternalRef += ":plain"
	rec, made := f.call(h, plain)
	if rec.Code != http.StatusOK || !made.Created {
		t.Fatalf("create platform: %d %s", rec.Code, rec.Body.String())
	}
	bare := f.payload()
	bare.CategoryList = nil
	bare.ActionEvent.ArenaSessionID = made.SessionID.String()
	bare.ExternalRef = ""
	rec, _ = f.call(h, bare)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.categories_required") {
		t.Fatalf("platform session without categories: %d %s", rec.Code, rec.Body.String())
	}
}
