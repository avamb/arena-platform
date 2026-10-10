package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
)

func TestClientChannelMiddleware(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
		wantOK bool
	}{
		{"telegram bot", "telegram_bot", audit.ChannelTelegramBot, true},
		{"normalized", "  Admin_Web ", audit.ChannelAdminWeb, true},
		{"unknown value is ignored, not rejected", "curl", "", false},
		{"absent", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			var gotOK bool
			h := clientChannelMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, gotOK = audit.ClientChannelFromContext(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.header != "" {
				req.Header.Set(audit.HeaderClientChannel, tc.header)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204 (the header must never break a request)", rr.Code)
			}
			if got != tc.want || gotOK != tc.wantOK {
				t.Fatalf("channel = (%q, %v), want (%q, %v)", got, gotOK, tc.want, tc.wantOK)
			}
		})
	}
}

// The middleware must be on the real router's global chain: a probe route added
// to the assembled server sees the channel declared in the header.
func TestClientChannelMiddleware_RegisteredOnRealRouter(t *testing.T) {
	srv := buildServerInfoServer(t, clock.NewFake(time.Now()))
	var got string
	srv.router.Get("/__client_channel_probe", func(w http.ResponseWriter, r *http.Request) {
		got, _ = audit.ClientChannelFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/__client_channel_probe", nil)
	req.Header.Set(audit.HeaderClientChannel, audit.ChannelTelegramBot)
	rr := httptest.NewRecorder()
	srv.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if got != audit.ChannelTelegramBot {
		t.Fatalf("channel seen by the handler = %q, want %q", got, audit.ChannelTelegramBot)
	}
}
