package httputil

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireStaticBearer(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	call := func(token, header string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		RequireStaticBearer(token, "svc.not_configured")(next).ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := call("", "Bearer anything"); code != http.StatusServiceUnavailable || !strings.Contains(body, "svc.not_configured") {
		t.Fatalf("unset token: %d %s", code, body)
	}
	if code, _ := call("", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("unset token, no header: %d", code)
	}
	for _, h := range []string{"", "Bearer s3cre", "Bearer s3cret1", "s3cret", "bearer s3cret"} {
		if code, _ := call("s3cret", h); code != http.StatusUnauthorized {
			t.Errorf("header %q: %d, want 401", h, code)
		}
	}
	if code, _ := call("s3cret", "Bearer s3cret"); code != http.StatusNoContent {
		t.Fatalf("right token: %d", code)
	}
}
