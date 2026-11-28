package httpserver

// scanner_app_device_token_test.go — spec 69, MA-10, no database: a scanner
// device token (`Bearer sdt_…`) is refused before routing outside
// /v1/scanner-app/, s.authMiddleware() never lets one through, and the
// scanner-app group accepts nothing but a device token.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

func ma10DeviceToken(t *testing.T) string {
	t.Helper()
	raw, _, err := scannerapp.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	return raw
}

func ma10ErrorCode(t *testing.T, body string) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return env.Error.Code
}

func TestScannerAppMA10_RootRefusal(t *testing.T) {
	device := ma10DeviceToken(t)
	cases := []struct {
		name, target, auth string
		wantNext           bool
	}{
		{"device on /v1/me", "/v1/me", "Bearer " + device, false},
		{"device lower-case scheme", "/v1/me", "bearer " + device, false},
		{"device with junk body", "/v1/organizations/x/events", "Bearer sdt_not-a-real-token", false},
		{"device on healthz", "/healthz", "Bearer " + device, false},
		{"device on compat gateway", "/compat/bil24/json", "Bearer " + device, false},
		{"device on unknown path", "/nope", "Bearer " + device, false},
		{"device on group root", "/v1/scanner-app", "Bearer " + device, false},
		{"device on look-alike prefix", "/v1/scanner-appx/me", "Bearer " + device, false},
		{"device on escaped slash", "/v1/scanner-app%2F..%2Fme", "Bearer " + device, false},
		{"device on scanner-app", "/v1/scanner-app/me", "Bearer " + device, true},
		{"device on unknown scanner-app path", "/v1/scanner-app/other", "Bearer " + device, true},
		{"jwt-like on /v1/me", "/v1/me", "Bearer eyJhbGciOiJIUzI1NiJ9.e30.x", true},
		{"api key on /v1/me", "/v1/me", "Bearer ak_live_abc", true},
		{"basic with device value", "/v1/me", "Basic " + device, true},
		{"no header", "/v1/me", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			called := false
			h := refuseDeviceTokenOutsideScannerApp(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusTeapot)
			}))
			req := httptest.NewRequest(http.MethodGet, c.target, nil)
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if called != c.wantNext {
				t.Fatalf("next called = %v, want %v (status %d)", called, c.wantNext, rec.Code)
			}
			if !c.wantNext {
				if rec.Code != http.StatusNotFound || ma10ErrorCode(t, rec.Body.String()) != "http.not_found" {
					t.Fatalf("got %d %s, want 404 http.not_found", rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func TestScannerAppMA10_AuthMiddlewareRefusesDeviceToken(t *testing.T) {
	s := buildDriftTestServer(t)
	called := false
	h := s.authMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	// Even on a scanner-app path: authMiddleware is never the device chain.
	for _, target := range []string{"/v1/me", "/v1/scanner-app/me"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+ma10DeviceToken(t))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if called {
			t.Fatalf("%s: next was called for a device token", target)
		}
		if rec.Code != http.StatusNotFound || ma10ErrorCode(t, rec.Body.String()) != "http.not_found" {
			t.Fatalf("%s: got %d %s, want 404 http.not_found", target, rec.Code, rec.Body.String())
		}
	}

	// A real JWT still goes through.
	tok, _, err := s.stub.IssueToken(context.Background(), auth.IssueRequest{ActorID: "00000000-0000-0000-0000-000000000001"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("JWT: called=%v status=%d, want the next handler", called, rec.Code)
	}
}

// Through the real router of a server without a database: the device token
// is a 404 on /v1/me, reaches the scanner-app group (503, no database), and
// the group refuses every other credential with 401.
func TestScannerAppMA10_RouterWithoutDatabase(t *testing.T) {
	s := buildDriftTestServer(t)
	jwt, _, err := s.stub.IssueToken(context.Background(), auth.IssueRequest{ActorID: "00000000-0000-0000-0000-000000000001", Roles: []string{"platform_superadmin"}})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	device := ma10DeviceToken(t)
	cases := []struct {
		name, method, target, auth string
		wantStatus                 int
		wantCode                   string
	}{
		{"device on /v1/me", http.MethodGet, "/v1/me", "Bearer " + device, http.StatusNotFound, "http.not_found"},
		{"device on echo", http.MethodPost, "/v1/echo", "Bearer " + device, http.StatusNotFound, "http.not_found"},
		{"device on dev token", http.MethodPost, "/v1/dev/token", "Bearer " + device, http.StatusNotFound, "http.not_found"},
		{"device on scanner-app me", http.MethodGet, "/v1/scanner-app/me", "Bearer " + device, http.StatusServiceUnavailable, "dependency.database_unavailable"},
		{"jwt on scanner-app me", http.MethodGet, "/v1/scanner-app/me", "Bearer " + jwt, http.StatusUnauthorized, "auth.invalid_token"},
		{"api key on scanner-app me", http.MethodGet, "/v1/scanner-app/me", "Bearer ak_live_abcdef", http.StatusUnauthorized, "auth.invalid_token"},
		{"no header on scanner-app me", http.MethodGet, "/v1/scanner-app/me", "", http.StatusUnauthorized, "auth.missing_token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.target, strings.NewReader(""))
			if c.auth != "" {
				req.Header.Set("Authorization", c.auth)
			}
			rec := httptest.NewRecorder()
			s.Router().ServeHTTP(rec, req)
			if rec.Code != c.wantStatus || ma10ErrorCode(t, rec.Body.String()) != c.wantCode {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), c.wantStatus, c.wantCode)
			}
			if c.wantStatus == http.StatusUnauthorized && rec.Header().Get(auth.HeaderWWWAuthenticate) == "" {
				t.Fatal("401 without WWW-Authenticate")
			}
		})
	}
}

// The dev token route never mints a device actor.
func TestScannerAppMA10_DevTokenRefusesDeviceType(t *testing.T) {
	s := buildDriftTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token", strings.NewReader(`{"actor_type":"device"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("dev token minted a device actor: %d %s", rec.Code, rec.Body.String())
	}
}
