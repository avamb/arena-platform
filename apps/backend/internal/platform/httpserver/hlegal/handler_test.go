package hlegal

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/legaldocs"
)

func TestRequestLocale(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x?locale=cs-CZ", nil)
	r.Header.Set("Accept-Language", "he-IL,he;q=0.9")
	if l, fromHeader := requestLocale(r); l != "cs-CZ" || fromHeader {
		t.Fatalf("query: %q %v", l, fromHeader)
	}
	r = httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set("Accept-Language", "he-IL;q=1,he;q=0.9")
	if l, fromHeader := requestLocale(r); legaldocs.NormalizeLocale(l) != "he" || !fromHeader {
		t.Fatalf("header: %q %v", l, fromHeader)
	}
}

func TestWriteTermsPage_NoScriptsAndRightToLeft(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTermsPage(rec, rendered(t, "he", legaldocs.Values{"seller.legal_name": "<script>alert(1)</script>"}))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("csp = %q", csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "&lt;script&gt;alert(1)") ||
		!strings.Contains(body, `<html lang="he" dir="rtl">`) || !strings.Contains(body, "Version 2") ||
		strings.Contains(body, "<script") || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("page: %s", body)
	}
}

func TestHandlers_WithoutDatabaseAnswer503(t *testing.T) {
	h := New(nil, nil, nil, nil, 0)
	for name, fn := range map[string]http.HandlerFunc{
		"terms": h.HandlePublicTerms, "list": h.HandleList, "get": h.HandleGet,
		"create": h.HandleCreate, "publish": h.HandlePublish,
	} {
		rec := httptest.NewRecorder()
		fn(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}

// rendered is a rendering exactly as legaldocs.RenderTerms stores it.
func rendered(t *testing.T, locale string, v legaldocs.Values) legaldocs.Rendering {
	t.Helper()
	tpl, err := legaldocs.Parse("## Terms\n{{seller.legal_name}}")
	if err != nil {
		t.Fatal(err)
	}
	html := legaldocs.Render(tpl, v, locale)
	return legaldocs.Rendering{ID: uuid.New(), Locale: locale, DocumentVersion: 2,
		HTML: html, SHA256: legaldocs.SHA256Hex([]byte(html))}
}

// TestWriteTermsPage_RefusesMarkupRenderDidNotWrite: the second line of
// defence. A stored rendering whose HTML was changed (a script, an
// attribute, a hand-edited row) or no longer matches its hash is never
// interpolated into the page.
func TestWriteTermsPage_RefusesMarkupRenderDidNotWrite(t *testing.T) {
	good := rendered(t, "en", legaldocs.Values{"seller.legal_name": "Acme"})
	if !renderingIntact(good) {
		t.Fatalf("a real rendering is refused: %q", good.HTML)
	}
	injected := strings.Replace(good.HTML, "Acme", "<script>alert(1)</script>", 1)
	cases := map[string]legaldocs.Rendering{
		"script with matching hash": {Locale: "en", HTML: injected, SHA256: legaldocs.SHA256Hex([]byte(injected))},
		"hash mismatch":             {Locale: "en", HTML: strings.Replace(good.HTML, "Acme", "Evil", 1), SHA256: good.SHA256},
		"not an article":            {Locale: "en", HTML: "<p>x</p>", SHA256: legaldocs.SHA256Hex([]byte("<p>x</p>"))},
	}
	for name, rd := range cases {
		rec := httptest.NewRecorder()
		writeTermsPage(rec, rd)
		body := rec.Body.String()
		if rec.Code != http.StatusInternalServerError || strings.Contains(body, "<script") ||
			strings.Contains(body, "Evil") || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: code %d type %q body %q", name, rec.Code, rec.Header().Get("Content-Type"), body)
		}
	}
}
