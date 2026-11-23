// barcode_superadmin_scope_test.go — POST /v1/barcodes/authorities and every
// /v1/barcode-batches* route are for a platform superadmin only. Another
// caller gets 403 barcode.superadmin_only before the handler runs; a platform
// superadmin passes the gate. GET /v1/barcodes/authorities keeps its
// barcode.read permission.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type barcodeScopeRoute struct{ method, path, body string }

func barcodeSuperadminOnlyRoutes() []barcodeScopeRoute {
	const id = "00000000-0000-0000-0000-000000000001"
	return []barcodeScopeRoute{
		// No label: a caller past the gate gets the handler's 400, so the
		// test needs no database.
		{http.MethodPost, "/v1/barcodes/authorities", `{"type":"guest_list"}`},
		{http.MethodGet, "/v1/barcode-batches", ""},
		{http.MethodGet, "/v1/barcode-batches/" + id, ""},
		{http.MethodPost, "/v1/barcode-batches", `{}`},
		{http.MethodPost, "/v1/barcode-batches/" + id + "/approve", ""},
		{http.MethodPost, "/v1/barcode-batches/" + id + "/reject", ""},
	}
}

func serveBarcodeScope(s *Server, tok string, rt barcodeScopeRoute) *httptest.ResponseRecorder {
	var req *http.Request
	if rt.body != "" {
		req = httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(rt.method, rt.path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	return rec
}

func TestBarcodeScope_NonSuperadminGets403(t *testing.T) {
	s := buildBarcodeBatchServer(t)
	for _, role := range []string{"admin", "org_admin"} {
		tok := mintBarcodeBatchTokenWithRole(t, s, role)
		for _, rt := range barcodeSuperadminOnlyRoutes() {
			rec := serveBarcodeScope(s, tok, rt)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: want 403, got %d (body=%s)", rt.method, rt.path, role, rec.Code, rec.Body.String())
				continue
			}
			if !strings.Contains(rec.Body.String(), "barcode.superadmin_only") {
				t.Errorf("%s %s as %s: want barcode.superadmin_only, got %s", rt.method, rt.path, role, rec.Body.String())
			}
		}
	}
}

func TestBarcodeScope_SuperadminPassesGate(t *testing.T) {
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchToken(t, s)
	for _, rt := range barcodeSuperadminOnlyRoutes() {
		rec := serveBarcodeScope(s, tok, rt)
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s as platform superadmin: the gate refused with %d (body=%s)", rt.method, rt.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "barcode.superadmin_only") {
			t.Errorf("%s %s as platform superadmin: got barcode.superadmin_only", rt.method, rt.path)
		}
	}
}

func TestBarcodeScope_ListAuthoritiesStaysOpenToBarcodeRead(t *testing.T) {
	s := buildBarcodeBatchServer(t)
	tok := mintBarcodeBatchTokenWithRole(t, s, "admin")
	rec := serveBarcodeScope(s, tok, barcodeScopeRoute{http.MethodGet, "/v1/barcodes/authorities", ""})
	if rec.Code == http.StatusForbidden || strings.Contains(rec.Body.String(), "barcode.superadmin_only") {
		t.Fatalf("GET /v1/barcodes/authorities as admin: want the handler, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}
