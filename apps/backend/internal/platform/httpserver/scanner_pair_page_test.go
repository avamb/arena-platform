package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScanPair_PageLanguagesAndHeaders(t *testing.T) {
	cases := []struct{ accept, lang, want string }{
		{"", "en", "Open the app"},
		{"de-DE,de;q=0.9", "en", "Open the app"},
		{"ru-RU,ru;q=0.9,en;q=0.5", "ru", "Открыть приложение"},
		{"en-US,ru;q=0.5", "en", "Open the app"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/scan/pair", nil)
		if tc.accept != "" {
			req.Header.Set("Accept-Language", tc.accept)
		}
		handleScanPairPage(rec, req)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, tc.want) || !strings.Contains(body, `lang="`+tc.lang+`"`) {
			t.Errorf("accept %q: code %d, lang/want mismatch", tc.accept, rec.Code)
		}
		for _, s := range []string{"play.google.com", "apps.apple.com", "/scan/pair.js"} {
			if !strings.Contains(body, s) {
				t.Errorf("accept %q: page lacks %s", tc.accept, s)
			}
		}
		h := rec.Header()
		if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" ||
			h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("accept %q: missing security headers: %v", tc.accept, h)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self'") ||
			strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "http") {
			t.Errorf("accept %q: bad CSP %q", tc.accept, csp)
		}
	}
}

func TestScanPair_ScriptRoute(t *testing.T) {
	rec := httptest.NewRecorder()
	handleScanPairScript(rec, httptest.NewRequest(http.MethodGet, "/scan/pair.js", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") ||
		rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), "intent://") {
		t.Fatalf("script route wrong: %d %v", rec.Code, rec.Header())
	}
}

func TestWellKnown_AppleAppSiteAssociation(t *testing.T) {
	rec := httptest.NewRecorder()
	appleAppSiteAssociationHandler("ABCDE12345")(rec, httptest.NewRequest(http.MethodGet, "/.well-known/apple-app-site-association", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var got struct {
		Applinks struct {
			Details []struct {
				AppIDs     []string            `json:"appIDs"`
				Components []map[string]string `json:"components"`
			} `json:"details"`
		} `json:"applinks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	d := got.Applinks.Details
	if len(d) != 1 || d[0].AppIDs[0] != "ABCDE12345.com.arenasoldout.macs" || d[0].Components[0]["/"] != "/scan/pair*" {
		t.Fatalf("unexpected body %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	appleAppSiteAssociationHandler("")(rec, httptest.NewRequest(http.MethodGet, "/.well-known/apple-app-site-association", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "http.not_found") {
		t.Fatalf("empty config must answer the standard 404, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestWellKnown_AssetLinks(t *testing.T) {
	fp := strings.TrimSuffix(strings.Repeat("AA:", 32), ":")
	rec := httptest.NewRecorder()
	assetLinksHandler([]string{fp})(rec, httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil))
	var got []struct {
		Relation []string `json:"relation"`
		Target   struct {
			Namespace    string   `json:"namespace"`
			Package      string   `json:"package_name"`
			Fingerprints []string `json:"sha256_cert_fingerprints"`
		} `json:"target"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(got) != 1 || got[0].Relation[0] != "delegate_permission/common.handle_all_urls" ||
		got[0].Target.Package != "com.arenasoldout.macs" || got[0].Target.Fingerprints[0] != fp {
		t.Fatalf("unexpected: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	assetLinksHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/.well-known/assetlinks.json", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "http.not_found") {
		t.Fatalf("empty config must answer the standard 404, got %d", rec.Code)
	}
}
