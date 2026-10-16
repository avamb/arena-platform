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
	writeTermsPage(rec, legaldocs.Rendering{ID: uuid.New(), Locale: "he", DocumentVersion: 2,
		HTML: "<article>x</article>\n", SHA256: strings.Repeat("a", 64)})
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Fatalf("csp = %q", csp)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<html lang="he" dir="rtl">`) || !strings.Contains(body, "Version 2") ||
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
