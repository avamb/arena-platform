//go:build integration

// checkout_attribution_integration_test.go — QL-6, spec 43 section 4.4:
// checkout/start takes an optional `attribution` object, cleans it, resolves
// its ref to a sales link of the SAME organization before the hold, and the
// order copies it. A label never costs a sale: an invalid or stale object is
// a 201 with an empty attribution.
//
// The cart is free (zero total) so the test needs no payment provider.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestCheckoutAttribution
package httpserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

// attrTestCode draws a random sales-link code (the alphabet of spec 43).
func attrTestCode(t *testing.T) string {
	t.Helper()
	b := make([]byte, attribution.CodeLen)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	for i := range b {
		b[i] = attribution.CodeAlphabet[int(b[i])%len(attribution.CodeAlphabet)]
	}
	return string(b)
}

// attrTestLink inserts an org_page sales link of orgID.
func attrTestLink(t *testing.T, ctx context.Context, q *gen.Queries, orgID uuid.UUID, label string) gen.SalesLinkRow {
	t.Helper()
	for i := 0; i < 8; i++ {
		l, ok, err := q.InsertSalesLinkIfCodeFree(ctx, gen.InsertSalesLinkParams{
			OrgID: orgID, Code: attrTestCode(t), TargetKind: "org_page", Label: label,
		})
		if err != nil {
			t.Fatalf("InsertSalesLinkIfCodeFree: %v", err)
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no free sales-link code after 8 draws")
	return gen.SalesLinkRow{}
}

// attrTestCleanup unlinks the orders of the given organizations from their
// sales links and deletes those links, the hits and the extra organizations.
// It must run BEFORE the hosted fixture's own cleanup (orders reference the
// links, and the links reference the organization).
func attrTestCleanup(pool gen.DBTX, orgIDs []uuid.UUID, extraOrgs []uuid.UUID) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `UPDATE orders SET sales_link_id = NULL WHERE sales_link_id IN
	    (SELECT id FROM sales_links WHERE org_id = ANY($1::uuid[]))`, orgIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM sales_link_hits WHERE sales_link_id IN
	    (SELECT id FROM sales_links WHERE org_id = ANY($1::uuid[]))`, orgIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM sales_links WHERE org_id = ANY($1::uuid[])`, orgIDs)
	_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = ANY($1::uuid[])`, extraOrgs)
}

func TestCheckoutAttribution_StartWritesTheOrdersAttribution(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	foreign, err := q.InsertOrganization(ctx, "QL6 Foreign "+uuid.NewString()[:8],
		"ql6-foreign-"+uuid.NewString()[:8], "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	defer attrTestCleanup(pool, []uuid.UUID{foreign.ID}, []uuid.UUID{foreign.ID})
	other := attrTestLink(t, ctx, q, foreign.ID, "Somebody else's poster")
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)

	const ownRef = "<own>" // replaced by the fixture's own link code
	cases := []struct {
		name        string
		attribution any // nil = key absent
		wantOwn     bool
		wantRef     string // "" = the stored attribution is '{}'
		wantSource  string
	}{
		{"own link", map[string]any{"ref": ownRef, "utm_source": "qr", "landed_at": now}, true, ownRef, "qr"},
		{"own link, upper case", map[string]any{"ref": "upper:" + ownRef}, true, ownRef, ""},
		{"another organization's link", map[string]any{"ref": other.Code, "landed_at": now}, false, other.Code, ""},
		{"not an object", 42, false, "", ""},
		{"touch older than 7 days", map[string]any{"ref": ownRef, "landed_at": old}, false, "", ""},
		{"no field at all", nil, false, "", ""},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One fixture per case: the free category has few places.
			f := newFreeBuyerFieldsFixture(t, ctx, pool, fmt.Sprintf("attr%d", i), false, false)
			defer f.cleanup()
			defer attrTestCleanup(pool, []uuid.UUID{f.orgID}, nil)
			own := attrTestLink(t, ctx, q, f.orgID, "Poster at the door")
			var wantLink *uuid.UUID
			if tc.wantOwn {
				wantLink = &own.ID
			}
			wantRef := tc.wantRef
			if wantRef == ownRef {
				wantRef = own.Code
			}
			if m, ok := tc.attribution.(map[string]any); ok {
				switch m["ref"] {
				case ownRef:
					m["ref"] = own.Code
				case "upper:" + ownRef:
					m["ref"] = "  " + attrUpper(own.Code) + " "
				}
			}
			body := map[string]any{
				"session_id": f.sessionID.String(),
				"tier_id":    f.tierID.String(),
				"qty":        1,
				"buyer":      map[string]any{"email": uuid.NewString()[:8] + "-" + f.buyerMail},
				"return_url": hostedTicketsBaseURL + "/shows",
			}
			if tc.attribution != nil {
				body["attribution"] = tc.attribution
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost,
				"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			srv.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("case %d: checkout/start = %d; body: %s", i, rec.Code, rec.Body.String())
			}
			var start hostedStartResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
				t.Fatalf("decode checkout/start: %v", err)
			}
			csID := uuid.MustParse(start.CheckoutSession.ID)
			order, err := q.GetOrderByCheckoutSession(ctx, csID)
			if err != nil {
				t.Fatalf("GetOrderByCheckoutSession: %v", err)
			}
			switch {
			case wantLink == nil && order.SalesLinkID != nil:
				t.Errorf("orders.sales_link_id = %s, want NULL", order.SalesLinkID)
			case wantLink != nil && (order.SalesLinkID == nil || *order.SalesLinkID != *wantLink):
				t.Errorf("orders.sales_link_id = %v, want %s", order.SalesLinkID, wantLink)
			}
			var got map[string]any
			if err := json.Unmarshal(order.Attribution, &got); err != nil {
				t.Fatalf("decode orders.attribution %s: %v", order.Attribution, err)
			}
			if wantRef == "" {
				if len(got) != 0 {
					t.Errorf("orders.attribution = %s, want {}", order.Attribution)
				}
			} else {
				if got["ref"] != wantRef {
					t.Errorf("orders.attribution.ref = %v, want %s", got["ref"], wantRef)
				}
				if tc.wantSource != "" && got["utm_source"] != tc.wantSource {
					t.Errorf("orders.attribution.utm_source = %v, want %s", got["utm_source"], tc.wantSource)
				}
				if _, ok := got["landed_at"].(string); !ok {
					t.Errorf("orders.attribution.landed_at missing: %s", order.Attribution)
				}
			}
			// The checkout session carries the same object the order copied.
			cs, err := q.GetCheckoutSessionByID(ctx, csID)
			if err != nil {
				t.Fatalf("GetCheckoutSessionByID: %v", err)
			}
			if !attrJSONEqual(t, cs.Attribution, order.Attribution) {
				t.Errorf("checkout_sessions.attribution = %s, orders.attribution = %s", cs.Attribution, order.Attribution)
			}
		})
	}
}

func attrUpper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

func attrJSONEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("decode %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
