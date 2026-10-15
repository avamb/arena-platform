//go:build integration

// public_feed_buyer_fields_integration_test.go — P1b: the buyer's name and
// phone are offered on every hosted checkout and are optional unless the
// channel's collect_name / collect_phone flag makes them required.
//
//   - GET .../events/{id}: buyer_fields carries name and phone with
//     enabled=true always, required = the channel flag.
//   - POST .../checkout/start: an empty (or whitespace-only) value is refused
//     with 422 checkout.buyer_<field>_required only when that flag is on;
//     otherwise the order is created and a typed value is kept.
//
// The cart is free (zero total) so the test needs no payment provider.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicFeedBuyerFields
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// newFreeBuyerFieldsFixture builds the hosted fixture with a free category and
// the channel's two flags set as asked.
func newFreeBuyerFieldsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string, collectName, collectPhone bool) *hostedOrgFixture {
	t.Helper()
	f := newHostedOrgFixture(t, ctx, pool, label)
	if _, err := pool.Exec(ctx,
		`UPDATE ticket_tiers SET pricing_mode = 'free', price_amount = 0 WHERE id = $1`, f.tierID); err != nil {
		f.cleanup()
		t.Fatalf("make the tier free: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE sales_channels SET collect_name = $2, collect_phone = $3 WHERE id = $1`,
		f.channelID, collectName, collectPhone); err != nil {
		f.cleanup()
		t.Fatalf("set buyer flags: %v", err)
	}
	return f
}

// postCheckoutStart sends a one-ticket free checkout with exactly the buyer
// object given (the fixture's own helper always sends a name and a phone).
func (f *hostedOrgFixture) postCheckoutStart(t *testing.T, srv *Server, buyer map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"session_id": f.sessionID.String(),
		"tier_id":    f.tierID.String(),
		"qty":        1,
		"buyer":      buyer,
		"return_url": hostedTicketsBaseURL + "/shows",
	})
	if err != nil {
		t.Fatalf("marshal checkout/start request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+f.feedToken+"/checkout/start", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

type buyerFieldWire struct {
	Key      string `json:"key"`
	Required bool   `json:"required"`
	Enabled  bool   `json:"enabled"`
}

// sessionBuyerFields reads GET .../events/{id} and returns the fixture
// session's buyer_fields.
func (f *hostedOrgFixture) sessionBuyerFields(t *testing.T, srv *Server) []buyerFieldWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), nil)
	req.Header.Set("Origin", hostedTicketsBaseURL)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET event = %d; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Event struct {
			Sessions []struct {
				ID          string           `json:"id"`
				BuyerFields []buyerFieldWire `json:"buyer_fields"`
			} `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	for _, s := range body.Event.Sessions {
		if s.ID == f.sessionID.String() {
			return s.BuyerFields
		}
	}
	t.Fatalf("session %s not in the event answer", f.sessionID)
	return nil
}

func buyerFieldsErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode error envelope: %v; body: %s", err, body)
	}
	return env.Error.Code
}

func buyerFieldsDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func TestPublicFeedBuyerFields_SessionReportsFlagAsRequiredAndAlwaysEnabled(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	cases := []struct {
		label       string
		name, phone bool
	}{
		{"bf-oo", false, false},
		{"bf-ro", true, false},
		{"bf-or", false, true},
		{"bf-rr", true, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("name_required=%t_phone_required=%t", tc.name, tc.phone), func(t *testing.T) {
			f := newFreeBuyerFieldsFixture(t, ctx, pool, tc.label, tc.name, tc.phone)
			defer f.cleanup()
			got := f.sessionBuyerFields(t, srv)
			want := []buyerFieldWire{
				{Key: "email", Required: true, Enabled: true},
				{Key: "name", Required: tc.name, Enabled: true},
				{Key: "phone", Required: tc.phone, Enabled: true},
			}
			if len(got) != len(want) {
				t.Fatalf("buyer_fields = %+v; want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("buyer_fields[%d] = %+v; want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

func TestPublicFeedBuyerFields_CheckoutStartRequiresOnlyWhatTheFlagSays(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())
	q := gen.New(pool)

	cases := []struct {
		label        string
		collectName  bool
		collectPhone bool
		name, phone  any // nil = key absent
		wantStatus   int
		wantCode     string
		wantName     string // "" = NULL
		wantPhone    string
	}{
		// Both optional: empty accepted, typed values kept.
		{"c1", false, false, nil, nil, http.StatusCreated, "", "", ""},
		{"c2", false, false, "", "  ", http.StatusCreated, "", "", ""},
		{"c3", false, false, "Jane Smith", "+34600111222", http.StatusCreated, "", "Jane Smith", "+34600111222"},
		// Name required.
		{"c4", true, false, nil, nil, http.StatusUnprocessableEntity, "checkout.buyer_name_required", "", ""},
		{"c5", true, false, "   ", nil, http.StatusUnprocessableEntity, "checkout.buyer_name_required", "", ""},
		{"c6", true, false, "Jane Smith", nil, http.StatusCreated, "", "Jane Smith", ""},
		// Phone required.
		{"c7", false, true, "Jane Smith", nil, http.StatusUnprocessableEntity, "checkout.buyer_phone_required", "", ""},
		{"c8", false, true, nil, "+34600111222", http.StatusCreated, "", "", "+34600111222"},
		// Both required: the name is checked first.
		{"c9", true, true, nil, nil, http.StatusUnprocessableEntity, "checkout.buyer_name_required", "", ""},
		{"c10", true, true, "Jane Smith", "+34600111222", http.StatusCreated, "", "Jane Smith", "+34600111222"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			f := newFreeBuyerFieldsFixture(t, ctx, pool, "bs-"+tc.label, tc.collectName, tc.collectPhone)
			defer f.cleanup()

			b := map[string]any{"email": f.buyerMail}
			if tc.name != nil {
				b["name"] = tc.name
			}
			if tc.phone != nil {
				b["phone"] = tc.phone
			}
			code, body := f.postCheckoutStart(t, srv, b)
			if code != tc.wantStatus {
				t.Fatalf("checkout/start = %d, want %d; body: %s", code, tc.wantStatus, body)
			}
			if tc.wantStatus != http.StatusCreated {
				if got := buyerFieldsErrorCode(t, body); got != tc.wantCode {
					t.Fatalf("error code = %q, want %q", got, tc.wantCode)
				}
				// A refused request takes no inventory.
				if fp := f.footprint(t, ctx); fp.reservations != 0 || fp.unavailable != 0 {
					t.Errorf("a refused buyer took inventory: %+v", fp)
				}
				return
			}
			var start hostedStartResponse
			if err := json.Unmarshal(body, &start); err != nil {
				t.Fatalf("decode checkout/start: %v", err)
			}
			order, err := q.GetOrderByCheckoutSession(ctx, uuid.MustParse(start.CheckoutSession.ID))
			if err != nil {
				t.Fatalf("GetOrderByCheckoutSession: %v", err)
			}
			if got := buyerFieldsDeref(order.BuyerName); got != tc.wantName {
				t.Errorf("orders.buyer_name = %q, want %q", got, tc.wantName)
			}
			if got := buyerFieldsDeref(order.BuyerPhone); got != tc.wantPhone {
				t.Errorf("orders.buyer_phone = %q, want %q", got, tc.wantPhone)
			}
		})
	}
}
