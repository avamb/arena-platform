package eventbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func newStubArena(t *testing.T, handler http.HandlerFunc) *ArenaClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewArenaClient(srv.URL+"/", "svc-token", srv.Client())
}

func TestArenaClient_AcceptInvitation(t *testing.T) {
	t.Parallel()
	c := newStubArena(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bot/invitations/accept" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer svc-token" {
			t.Errorf("service token missing: %q", r.Header.Get("Authorization"))
		}
		// Every bot call names its channel, so the audit log can tell a change
		// made through the bot from one made in admin-web.
		if got := r.Header.Get("X-Client-Channel"); got != "telegram_bot" {
			t.Errorf("X-Client-Channel = %q, want telegram_bot", got)
		}
		var req AcceptInvitationRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Code {
		case "good":
			_ = json.NewEncoder(w).Encode(AcceptInvitationResponse{UserID: "u", OrgID: "o", OrgName: "Org", Role: "owner", Locale: "ru"})
		case "used":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"bot.invitation_not_found","message":"gone"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}
	})
	res, err := c.AcceptInvitation(context.Background(), AcceptInvitationRequest{Code: "good", Email: "a@b.c", TelegramUserID: 1})
	if err != nil || res.OrgName != "Org" || res.Role != "owner" {
		t.Fatalf("good: %+v, %v", res, err)
	}
	_, err = c.AcceptInvitation(context.Background(), AcceptInvitationRequest{Code: "used", Email: "a@b.c", TelegramUserID: 1})
	if !IsAPIError(err, http.StatusNotFound) || APIErrorCode(err) != "bot.invitation_not_found" {
		t.Fatalf("used: want 404 bot.invitation_not_found, got %v", err)
	}
	_, err = c.AcceptInvitation(context.Background(), AcceptInvitationRequest{Code: "x", Email: "a@b.c", TelegramUserID: 1})
	if !IsAPIError(err, http.StatusInternalServerError) || !strings.HasPrefix(APIErrorCode(err), "http.") {
		t.Fatalf("500 without envelope: got %v", err)
	}
}

func TestArenaClient_MeAndEvents(t *testing.T) {
	t.Parallel()
	orgA, orgB := uuid.New(), uuid.New()
	c := newStubArena(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer user-jwt" {
			t.Errorf("user jwt missing on %s: %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == "/v1/me":
			_, _ = w.Write([]byte(`{"user":{"id":"u"},"organization_memberships":[
				{"org_id":"` + orgA.String() + `","org_name":"A","role":"org_admin","status":"active"},
				{"org_id":"` + orgB.String() + `","org_name":"B","role":"organizer","status":"inactive"},
				{"org_id":"not-a-uuid","org_name":"C","role":"organizer","status":"active"}]}`))
		case r.URL.Path == "/v1/organizations/"+orgA.String()+"/events":
			_, _ = w.Write([]byte(`{"events":[{"id":"` + uuid.NewString() + `","org_id":"` + orgA.String() + `","name":"Show","status":"published","visibility":"public","display_number":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","venue_names":[]}]}`))
		case r.URL.Path == "/v1/organizations/"+orgB.String()+"/events":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"org.access_denied","message":"no"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ms, err := c.Me(context.Background(), "user-jwt")
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if len(ms) != 1 || ms[0].OrgID != orgA || ms[0].Role != "org_admin" {
		t.Fatalf("Me should keep only active memberships with a valid org id: %+v", ms)
	}
	events, err := c.ListEvents(context.Background(), "user-jwt", orgA)
	if err != nil || len(events) != 1 || events[0].Name != "Show" {
		t.Fatalf("ListEvents: %+v, %v", events, err)
	}
	if _, err := c.ListEvents(context.Background(), "user-jwt", orgB); !IsAPIError(err, http.StatusForbidden) {
		t.Fatalf("foreign org should surface the 403, got %v", err)
	}
}
