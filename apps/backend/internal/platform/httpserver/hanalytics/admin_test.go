package hanalytics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func adminReq(t *testing.T, h http.HandlerFunc, method, target, body, reason string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if reason != "" {
		r.Header.Set("X-Admin-Reason", reason)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestContactCodes_ValidationBeforeAnyDatabaseWork(t *testing.T) {
	h := NewAdmin(AdminOptions{}) // no pool, no audit: only validation can answer
	cases := []struct {
		name, body, reason string
		want               int
	}{
		{"missing reason", `{"twenty_person_ids":["a"]}`, "", http.StatusBadRequest},
		{"not json", `nope`, "x", http.StatusBadRequest},
		{"unknown field", `{"twenty_person_ids":["a"],"x":1}`, "x", http.StatusBadRequest},
		{"trailing data", `{"twenty_person_ids":["a"]} {}`, "x", http.StatusBadRequest},
		{"empty list", `{"twenty_person_ids":[]}`, "x", http.StatusUnprocessableEntity},
		{"bad id", `{"twenty_person_ids":["a b"]}`, "x", http.StatusUnprocessableEntity},
		{"too many", `{"twenty_person_ids":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 501), ",") + `]}`, "x", http.StatusUnprocessableEntity},
		{"valid but no database", `{"twenty_person_ids":["a"]}`, "x", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		w := adminReq(t, h.HandleContactCodes, http.MethodPost, "/v1/admin/analytics/contact-codes", c.body, c.reason)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
}

func TestProfiles_QueryValidation(t *testing.T) {
	h := NewAdmin(AdminOptions{})
	cases := []struct {
		target string
		want   int
	}{
		{"/p?changed_since=yesterday", http.StatusBadRequest},
		{"/p?limit=0", http.StatusBadRequest},
		{"/p?limit=201", http.StatusBadRequest},
		{"/p?limit=abc", http.StatusBadRequest},
		{"/p?after=%21%21", http.StatusBadRequest},
		{"/p?changed_since=2026-11-01T00:00:00Z&limit=200", http.StatusServiceUnavailable}, // valid, then no database
		{"/p", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		w := adminReq(t, h.HandleProfiles, http.MethodGet, c.target, "", "")
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.target, w.Code, c.want, w.Body.String())
		}
	}
}

func TestUUIDOrEmpty(t *testing.T) {
	if uuidOrEmpty("api_key:123") != "" || uuidOrEmpty("") != "" {
		t.Error("a non-uuid actor must become empty")
	}
	if got := uuidOrEmpty("2b0f4b1e-8a4e-4a47-a2a0-1f0c6f0a9d11"); got == "" {
		t.Error("a uuid actor was dropped")
	}
}
