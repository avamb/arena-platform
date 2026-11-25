package hcatalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func str(s string) *string { return &s }

func TestParseSalesModeWrite(t *testing.T) {
	const good = `{"url":"https://www.eventim.de/x","seller_name":"Eventim","price_from":39.5,"currency":"eur"}`
	cases := []struct {
		name     string
		mode     *string
		external string
		status   int // 0 = accepted
		code     string
	}{
		{"nothing", nil, "", 0, ""},
		{"platform", str("platform"), "", 0, ""},
		{"platform with null block", str("platform"), "null", 0, ""},
		{"external", str("external"), good, 0, ""},
		{"block only", nil, good, 0, ""},
		{"unknown mode", str("partner"), "", http.StatusBadRequest, "session.invalid_sales_mode"},
		{"platform with block", str("platform"), good, http.StatusUnprocessableEntity, "session.external_not_applicable"},
		{"external with null block", str("external"), "null", http.StatusUnprocessableEntity, "session.external_required"},
		{"http address", str("external"), `{"url":"http://www.eventim.de/x","seller_name":"E"}`, http.StatusUnprocessableEntity, "session.invalid_external_url"},
		{"no name", str("external"), `{"url":"https://www.eventim.de/x","seller_name":""}`, http.StatusUnprocessableEntity, "session.invalid_external_seller_name"},
		{"price without currency", str("external"), `{"url":"https://www.eventim.de/x","seller_name":"E","price_from":5}`, http.StatusUnprocessableEntity, "session.invalid_external_currency"},
		{"not an object", str("external"), `"x"`, http.StatusBadRequest, "session.invalid_json"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			var raw json.RawMessage
			if c.external != "" {
				raw = json.RawMessage(c.external)
			}
			out, ok := parseSalesModeWrite(rec, r, c.mode, raw)
			if c.status == 0 {
				if !ok {
					t.Fatalf("refused: %d %s", rec.Code, rec.Body.String())
				}
				if c.name == "external" && (!out.ExternalPresent || out.Sale.PriceFromMinor == nil || *out.Sale.PriceFromMinor != 3950 || *out.Sale.Currency != "EUR") {
					t.Fatalf("sale = %+v", out)
				}
				return
			}
			if ok || rec.Code != c.status {
				t.Fatalf("ok=%v status=%d body=%s, want %d", ok, rec.Code, rec.Body.String(), c.status)
			}
			var env map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if e, _ := env["error"].(map[string]any); e["code"] != c.code {
				t.Fatalf("code = %v, want %s", e["code"], c.code)
			}
		})
	}
}
