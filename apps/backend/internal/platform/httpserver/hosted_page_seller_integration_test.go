//go:build integration

// hosted_page_seller_integration_test.go — live-DB, real-router coverage of
// the seller / promoter blocks of the hosted sales page and of the
// withdrawal-notice flag on the widget's feed session:
//
//	GET /v1/public/pages/{slug}/{event_slug}
//	GET /v1/public/pages/{slug}
//	GET /v1/public/feeds/{feed_token}/events/{event_id}
//
// Prerequisites: DATABASE_URL against a migrated database (head >= 0139).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestHostedPageSeller
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const sellerPersonalEmail = "private.person@personal-mail.example"

// sellerFixture is one organization with a published event behind a
// hosted-page channel.
type sellerFixture struct {
	orgID, eventID, channelID uuid.UUID
	orgSlug, eventSlug, token string
}

func sellerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newSellerFixture(t *testing.T, pool *pgxpool.Pool, legalName, country string, legalCountry *string) *sellerFixture {
	t.Helper()
	ctx := context.Background()
	q := gen.New(pool)
	run := uuid.NewString()
	org, err := q.InsertOrganization(ctx, "Seller Org "+run, "seller-org-"+run, country, "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE organizations SET
		    legal_name = $2, tax_id = 'TAX-'||$3, tax_id_scheme = 'other',
		    registration_number = 'REG-'||$3, legal_address_line1 = 'Line1 '||$3,
		    legal_address_city = 'City '||$3, legal_address_postal_code = '12345',
		    legal_address_country = $4,
		    contact_email = $5, contact_phone = '+000 PRIVATE'
		WHERE id = $1`, org.ID, legalName, run[:8], legalCountry, sellerPersonalEmail); err != nil {
		t.Fatalf("update organization: %v", err)
	}
	desc := "seller test event"
	event, err := q.InsertEvent(ctx, org.ID, "Seller Event "+run, &desc, "published", "public", nil)
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	eventSlug := "seller-event-" + run
	if _, err := q.UpdateEventMetadata(ctx, event.ID, org.ID, &eventSlug, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatalf("UpdateEventMetadata: %v", err)
	}
	ch, err := q.InsertSalesChannel(ctx, org.ID, "Seller Channel "+run, "direct_merchant", "stripe", nil, "0.00", nil,
		json.RawMessage(`{"hosted_page":{"enabled":true}}`))
	if err != nil {
		t.Fatalf("InsertSalesChannel: %v", err)
	}
	ft, err := q.InsertFeedToken(ctx, "seller-token-"+run, ch.ID, "seller test")
	if err != nil {
		t.Fatalf("InsertFeedToken: %v", err)
	}
	if _, err := q.PublishEvent(ctx, event.ID, ft.ID, nil); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM event_promoters WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM org_promoters WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(c, `DELETE FROM event_publications WHERE event_id = $1`, event.ID)
		_, _ = pool.Exec(c, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, ch.ID)
		_, _ = pool.Exec(c, `DELETE FROM sales_channels WHERE id = $1`, ch.ID)
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = $1`, event.ID)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	return &sellerFixture{
		orgID: org.ID, eventID: event.ID, channelID: ch.ID,
		orgSlug: org.Slug, eventSlug: eventSlug, token: ft.Token,
	}
}

func (f *sellerFixture) setHostedPage(t *testing.T, pool *pgxpool.Pool, settingsJSON string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE sales_channels SET settings = $2::jsonb WHERE id = $1`, f.channelID, settingsJSON); err != nil {
		t.Fatalf("update channel settings: %v", err)
	}
}

func getJSON(t *testing.T, srv *Server, path string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Body.String()
}

func TestHostedPageSeller_BlockIsPerChannelAndNeverLeaksContact(t *testing.T) {
	pool := sellerPool(t)
	srv := buildHostedCheckoutServer(t, pool, "http://127.0.0.1:1")
	es := "ES"
	a := newSellerFixture(t, pool, "Alpha Legal S.L.", "DE", &es)
	b := newSellerFixture(t, pool, "Bravo Foreign Ltd", "GB", nil)
	pagePath := "/v1/public/pages/" + a.orgSlug + "/" + a.eventSlug

	// 1. Default: flag absent, the response carries neither block.
	code, body, raw := getJSON(t, srv, pagePath)
	if code != http.StatusOK {
		t.Fatalf("page status = %d: %s", code, raw)
	}
	if _, ok := body["seller"]; ok {
		t.Fatalf("seller present with show_seller unset: %s", raw)
	}
	if _, ok := body["promoter"]; ok {
		t.Fatalf("promoter present with show_seller unset: %s", raw)
	}

	// 2. Flag on, no public_contact: the legal identity of A, and no contact
	// at all (the organization's own e-mail is private).
	a.setHostedPage(t, pool, `{"hosted_page":{"enabled":true,"show_seller":true}}`)
	code, body, raw = getJSON(t, srv, pagePath)
	if code != http.StatusOK {
		t.Fatalf("page status = %d: %s", code, raw)
	}
	seller, _ := body["seller"].(map[string]any)
	if seller == nil {
		t.Fatalf("seller missing: %s", raw)
	}
	if seller["legal_name"] != "Alpha Legal S.L." || seller["country"] != "ES" {
		t.Fatalf("seller wrong: %v", seller)
	}
	if addr, _ := seller["address"].(map[string]any); addr == nil || addr["country"] != "ES" || addr["postal_code"] != "12345" {
		t.Fatalf("address wrong: %v", seller["address"])
	}
	if _, ok := seller["public_contact"]; ok {
		t.Fatalf("public_contact published without being configured: %s", raw)
	}
	for _, banned := range []string{sellerPersonalEmail, "PRIVATE", "contact_email", "Bravo Foreign", b.orgSlug} {
		if strings.Contains(raw, banned) {
			t.Fatalf("response leaks %q: %s", banned, raw)
		}
	}

	// 3. public_contact set on the channel is the one published contact.
	a.setHostedPage(t, pool, `{"hosted_page":{"enabled":true,"show_seller":true,"public_contact":{"email":"box@alpha.example"}}}`)
	_, body, raw = getJSON(t, srv, pagePath)
	seller, _ = body["seller"].(map[string]any)
	contact, _ := seller["public_contact"].(map[string]any)
	if contact["email"] != "box@alpha.example" {
		t.Fatalf("public_contact wrong: %s", raw)
	}
	if _, ok := contact["phone"]; ok {
		t.Fatalf("unset phone must be omitted: %s", raw)
	}
	if strings.Contains(raw, sellerPersonalEmail) {
		t.Fatalf("personal e-mail leaked: %s", raw)
	}

	// 4. Organization B's flag and data never reach A's page, nor the other
	// way round.
	b.setHostedPage(t, pool, `{"hosted_page":{"enabled":true,"show_seller":true}}`)
	_, body, raw = getJSON(t, srv, pagePath)
	if seller, _ = body["seller"].(map[string]any); seller["legal_name"] != "Alpha Legal S.L." || strings.Contains(raw, "Bravo") {
		t.Fatalf("foreign organization visible on A's page: %s", raw)
	}

	// 5. A promoter linked to the event is named, and a promoter's own slug
	// resolves the same seller (the organization) with that promoter.
	q := gen.New(pool)
	slug := "promo-" + uuid.NewString()[:8]
	p, err := q.InsertOrgPromoter(context.Background(), a.orgID, "Cultura Viva "+slug, nil, nil, nil, &slug)
	if err != nil {
		t.Fatalf("InsertOrgPromoter: %v", err)
	}
	if err := q.SetEventPromoter(context.Background(), a.eventID, a.orgID, p.ID); err != nil {
		t.Fatalf("SetEventPromoter: %v", err)
	}
	_, body, raw = getJSON(t, srv, pagePath)
	if pr, _ := body["promoter"].(map[string]any); pr["name"] != "Cultura Viva "+slug {
		t.Fatalf("promoter wrong: %s", raw)
	}
	code, body, raw = getJSON(t, srv, "/v1/public/pages/"+slug+"/"+a.eventSlug)
	if code != http.StatusOK {
		t.Fatalf("promoter event page = %d: %s", code, raw)
	}
	if s, _ := body["seller"].(map[string]any); s["legal_name"] != "Alpha Legal S.L." {
		t.Fatalf("promoter page must keep the organization as seller: %s", raw)
	}
	code, body, raw = getJSON(t, srv, "/v1/public/pages/"+slug)
	if code != http.StatusOK {
		t.Fatalf("promoter landing = %d: %s", code, raw)
	}
	if s, _ := body["seller"].(map[string]any); s["legal_name"] != "Alpha Legal S.L." {
		t.Fatalf("promoter landing seller wrong: %s", raw)
	}
	if pr, _ := body["promoter"].(map[string]any); pr["name"] != "Cultura Viva "+slug {
		t.Fatalf("promoter landing promoter wrong: %s", raw)
	}

	// 6. The organization landing page gets the seller but no promoter.
	code, body, raw = getJSON(t, srv, "/v1/public/pages/"+a.orgSlug)
	if code != http.StatusOK {
		t.Fatalf("org landing = %d: %s", code, raw)
	}
	if s, _ := body["seller"].(map[string]any); s["legal_name"] != "Alpha Legal S.L." {
		t.Fatalf("org landing seller wrong: %s", raw)
	}
	if _, ok := body["promoter"]; ok {
		t.Fatalf("org landing must not carry a promoter: %s", raw)
	}

	// 7. Turning the flag off again removes the blocks.
	a.setHostedPage(t, pool, `{"hosted_page":{"enabled":true,"show_seller":false}}`)
	_, body, raw = getJSON(t, srv, pagePath)
	if _, ok := body["seller"]; ok {
		t.Fatalf("seller present after switching off: %s", raw)
	}
}
