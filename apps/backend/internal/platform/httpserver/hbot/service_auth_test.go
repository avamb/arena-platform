package hbot

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireServiceToken(t *testing.T) {
	t.Parallel()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	call := func(token, header string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/bot/invitations/accept", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		RequireServiceToken(token)(next).ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// An unset token never opens the route.
	if code, body := call("", "Bearer anything"); code != http.StatusServiceUnavailable || !strings.Contains(body, "bot.service_not_configured") {
		t.Fatalf("unset token: status=%d body=%s; want 503 bot.service_not_configured", code, body)
	}
	if code, _ := call("", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("unset token, no header: status=%d; want 503", code)
	}
	// A configured token demands an exact match.
	if code, _ := call("s3cret", ""); code != http.StatusUnauthorized {
		t.Fatalf("missing header: status=%d; want 401", code)
	}
	if code, _ := call("s3cret", "Bearer s3cre"); code != http.StatusUnauthorized {
		t.Fatalf("short token: status=%d; want 401", code)
	}
	if code, _ := call("s3cret", "Bearer s3cret1"); code != http.StatusUnauthorized {
		t.Fatalf("long token: status=%d; want 401", code)
	}
	if code, _ := call("s3cret", "s3cret"); code != http.StatusUnauthorized {
		t.Fatalf("no Bearer scheme: status=%d; want 401", code)
	}
	if code, _ := call("s3cret", "Bearer s3cret"); code != http.StatusNoContent {
		t.Fatalf("right token: status=%d; want 204 from next", code)
	}
}
