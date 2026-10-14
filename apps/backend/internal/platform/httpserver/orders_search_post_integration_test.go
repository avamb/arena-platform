//go:build integration

// orders_search_post_integration_test.go — POST .../orders/search (owner
// decision 2026-10-14): the search moves into the request body, so a buyer's
// e-mail or phone never sits in a URL. It must find exactly what GET finds,
// refuse a query string and stray fields, and keep the order.read gate.

package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (f *ecOrdersFixture) postSearch(path, token, body string) (int, map[string]any) {
	f.t.Helper()
	resp := integDoRequest(f.t, f.ts.Client(), http.MethodPost, f.ts.URL+path, token, body)
	raw := integReadBody(f.t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			f.t.Fatalf("POST %s: decode %q: %v", path, raw, err)
		}
	}
	return resp.StatusCode, out
}

func orderIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	rows, _ := body["orders"].([]any)
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	return ids
}

func TestEventCenterOrders_PostSearchMatchesGet(t *testing.T) {
	f := newECOrdersFixture(t)
	search := "/v1/organizations/" + f.orgA.String() + "/orders/search"

	cases := []struct{ query, body string }{
		{"", `{}`},
		{"q=" + f.accountEmail, `{"q":"` + f.accountEmail + `"}`},
		{"q=00" + f.phoneDigits, `{"q":"00` + f.phoneDigits + `"}`},
		{fmt.Sprintf("q=%%23%d", f.paidSystemID), fmt.Sprintf(`{"q":"#%d"}`, f.paidSystemID)},
		{"tab=unpaid", `{"tab":"unpaid"}`},
		{"limit=1&offset=1", `{"limit":1,"offset":1}`},
		{"event_id=" + f.eventA.String() + "&tab=paid", `{"event_id":"` + f.eventA.String() + `","tab":"paid"}`},
	}
	for _, tc := range cases {
		want, wantTotal, _ := f.listIDs(tc.query)
		code, body := f.postSearch(search, f.managerTok, tc.body)
		if code != http.StatusOK {
			t.Fatalf("POST %s: %d %v", tc.body, code, body)
		}
		got := orderIDs(t, body)
		if strings.Join(got, ",") != strings.Join(want, ",") || body["total_count"] != wantTotal {
			t.Errorf("POST %s: ids %v total %v, GET %q gave %v total %v", tc.body, got, body["total_count"], tc.query, want, wantTotal)
		}
	}

	// The same validation as GET, with the same codes.
	for body, wantCode := range map[string]string{
		`{"tab":"refunded"}`:                       "orders.invalid_tab",
		`{"limit":0}`:                              "orders.invalid_limit",
		`{"session_id":"not-a-uuid"}`:              "orders.invalid_session_id",
		`{"q":"x","buyer_email":"a@example.test"}`: "orders.invalid_body",
		`{"q":"x"} {"q":"y"}`:                      "orders.invalid_body",
		`["q"]`:                                    "orders.invalid_body",
	} {
		code, out := f.postSearch(search, f.managerTok, body)
		if code != http.StatusBadRequest || ecErrCode(out) != wantCode {
			t.Errorf("POST %s: %d %v, want 400 %s", body, code, out, wantCode)
		}
	}
	// A foreign session is 404, like on GET.
	if code, out := f.postSearch(search, f.managerTok, `{"session_id":"`+f.sessionB.String()+`"}`); code != http.StatusNotFound {
		t.Errorf("foreign session: %d %v", code, out)
	}
	// The search never travels in the URL of this route.
	if code, out := f.postSearch(search+"?q="+f.accountEmail, f.managerTok, `{}`); code != http.StatusBadRequest || ecErrCode(out) != "orders.query_in_url" {
		t.Errorf("query string: %d %v", code, out)
	}
	// The order.read gate holds: an agent is refused, and so is a manager in
	// an organization they are not a member of.
	if code, out := f.postSearch(search, f.agentTok, `{}`); code != http.StatusForbidden {
		t.Errorf("agent: %d %v", code, out)
	}
	if code, out := f.postSearch("/v1/organizations/"+uuid.NewString()+"/orders/search", f.managerTok, `{}`); code != http.StatusForbidden {
		t.Errorf("foreign org: %d %v", code, out)
	}
}
