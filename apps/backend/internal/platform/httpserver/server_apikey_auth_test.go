package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/tests/orgauthcases"
)

// apiKeyRecorder is a Store that answers from a single pre-built row and
// records TouchLastUsed calls, so the middleware can be exercised without a
// database. Insert is never used by the authentication path.
type apiKeyRecorder struct {
	key      apikeys.APIKey
	notFound bool
	touches  int
}

func (r *apiKeyRecorder) InsertAPIKey(context.Context, uuid.UUID, *uuid.UUID, string, string, string, []string, uuid.UUID, *time.Time) (apikeys.APIKey, error) {
	return apikeys.APIKey{}, nil
}

func (r *apiKeyRecorder) GetAPIKeyByPrefix(_ context.Context, keyPrefix string) (apikeys.APIKey, error) {
	if r.notFound || keyPrefix != r.key.KeyPrefix {
		return apikeys.APIKey{}, apikeys.ErrNotFound
	}
	return r.key, nil
}

func (r *apiKeyRecorder) TouchLastUsed(context.Context, uuid.UUID, time.Time) error {
	r.touches++
	return nil
}

// newTestAPIKey mints a raw key plus the row a store would return for it.
func newTestAPIKey(t *testing.T, scopes []string) (raw string, key apikeys.APIKey) {
	t.Helper()
	prefix, secret, raw, err := apikeys.GenerateRawKey()
	if err != nil {
		t.Fatalf("GenerateRawKey: %v", err)
	}
	// bcrypt.MinCost keeps the test fast; production hashes at apikeys.BcryptCost.
	hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return raw, apikeys.APIKey{
		ID:        uuid.New(),
		OrgID:     orgauthcases.KeyOrgID,
		Name:      "lampyris-ops",
		KeyPrefix: prefix,
		KeyHash:   string(hash),
		Scopes:    scopes,
		CreatedBy: uuid.New(),
		CreatedAt: time.Now(),
	}
}

// serveWithAPIKeyAuth runs one request through s.authMiddleware() and returns
// the response plus the actor the downstream handler observed.
func serveWithAPIKeyAuth(s *Server, req *http.Request) (*httptest.ResponseRecorder, auth.Actor, bool) {
	var seen auth.Actor
	var ok bool
	h := s.authMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, ok = auth.ActorFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen, ok
}

func newAPIKeyServer(store apikeys.Store, rl ratelimit.Limiter) *Server {
	return &Server{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		apiKeyStore: store,
		apiKeyRL:    rl,
	}
}

func TestAuthMiddleware_APIKey_ValidKeyProducesServiceActor(t *testing.T) {
	raw, key := newTestAPIKey(t, []string{"event.read", "session.read"})
	store := &apiKeyRecorder{key: key}
	s := newAPIKeyServer(store, newAPIKeyRateLimiter())

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec, actor, ok := serveWithAPIKeyAuth(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if !ok {
		t.Fatal("no actor on the downstream context")
	}
	if actor.Type != auth.ActorTypeService {
		t.Fatalf("actor type = %q, want service", actor.Type)
	}
	if actor.ID != key.ID.String() {
		t.Fatalf("actor id = %q, want the api key id %q", actor.ID, key.ID)
	}
	if actor.OrgID != key.OrgID.String() {
		t.Fatalf("actor org = %q, want %q", actor.OrgID, key.OrgID)
	}
	if len(actor.Roles) != 0 {
		t.Fatalf("service actor must carry no roles, got %v", actor.Roles)
	}
	if !actor.HasPermission("event.read") || actor.HasPermission("order.write") {
		t.Fatalf("permissions must equal scopes, got %v", actor.Permissions)
	}
	if store.touches != 1 {
		t.Fatalf("last_used_at touches = %d, want 1", store.touches)
	}
}

func TestAuthMiddleware_APIKey_RejectionsAnswer401(t *testing.T) {
	rawValid, key := newTestAPIKey(t, []string{"event.read"})
	revoked := key
	revokedAt := time.Now().Add(-time.Hour)
	revoked.RevokedAt = &revokedAt
	expiredKey := key
	expiredAt := time.Now().Add(-time.Minute)
	expiredKey.ExpiresAt = &expiredAt

	cases := []struct {
		name  string
		store *apiKeyRecorder
		raw   string
	}{
		{"unknown prefix", &apiKeyRecorder{key: key, notFound: true}, rawValid},
		{"wrong secret", &apiKeyRecorder{key: key}, apikeys.KeyWirePrefix + key.KeyPrefix + "_" + "x0000000000000000000000000000000000000000000"},
		{"revoked key", &apiKeyRecorder{key: revoked}, rawValid},
		{"expired key", &apiKeyRecorder{key: expiredKey}, rawValid},
		{"malformed token", &apiKeyRecorder{key: key}, "ak_short_secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newAPIKeyServer(tc.store, newAPIKeyRateLimiter())
			req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
			req.Header.Set("Authorization", "Bearer "+tc.raw)
			rec, _, ok := serveWithAPIKeyAuth(s, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if ok {
				t.Fatal("downstream handler must not run for a rejected key")
			}
			if got := rec.Header().Get(auth.HeaderWWWAuthenticate); got != `Bearer realm="arena"` {
				t.Fatalf("WWW-Authenticate = %q, want the arena challenge", got)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			// Every rejection collapses into one code so the caller cannot
			// distinguish "unknown key" from "wrong secret".
			if body.Error.Code != "auth.invalid_token" {
				t.Fatalf("error code = %q, want auth.invalid_token", body.Error.Code)
			}
		})
	}
}

func TestAuthMiddleware_APIKey_RateLimitedPerKey(t *testing.T) {
	raw, key := newTestAPIKey(t, []string{"event.read"})
	store := &apiKeyRecorder{key: key}
	// Two requests per window stands in for the production budget of
	// APIKeyRateLimit so the test does not have to issue 600 requests.
	rl := ratelimit.New(ratelimit.Config{MaxAttempts: 2, Window: time.Minute})
	s := newAPIKeyServer(store, rl)

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		rec, _, _ := serveWithAPIKeyAuth(s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec, _, ok := serveWithAPIKeyAuth(s, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if ok {
		t.Fatal("downstream handler must not run for a rate-limited key")
	}
	if got := rl.Count(key.ID.String()); got != 3 {
		t.Fatalf("limiter keyed by api_key.id counted %d, want 3", got)
	}
}

func TestAuthMiddleware_APIKey_ProductionBudgetIs600PerMinute(t *testing.T) {
	if APIKeyRateLimit != 600 {
		t.Fatalf("APIKeyRateLimit = %d, want 600 (spec §13.1)", APIKeyRateLimit)
	}
	if APIKeyRateWindow != time.Minute {
		t.Fatalf("APIKeyRateWindow = %s, want 1m (spec §13.1)", APIKeyRateWindow)
	}
}

// jwtOnlyProvider stands in for the production verifier so the fall-through
// path can be observed without minting a real token.
type jwtOnlyProvider struct{ called bool }

func (p *jwtOnlyProvider) Verify(_ context.Context, token string) (auth.Actor, error) {
	p.called = true
	return auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser, RawToken: token}, nil
}

func TestAuthMiddleware_NonAPIKeyBearerFallsThroughToJWT(t *testing.T) {
	provider := &jwtOnlyProvider{}
	s := newAPIKeyServer(&apiKeyRecorder{notFound: true}, newAPIKeyRateLimiter())
	s.verifier = provider

	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	rec, actor, ok := serveWithAPIKeyAuth(s, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !provider.called {
		t.Fatal("a non-ak_ bearer must be verified by the JWT provider")
	}
	if !ok || actor.Type != auth.ActorTypeUser {
		t.Fatalf("actor = %+v, want a user actor", actor)
	}
}

func TestServiceKeyFromHeader(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"api key", "Bearer ak_abcdefghijkl_secret", true},
		{"lowercase scheme", "bearer ak_abcdefghijkl_secret", true},
		{"jwt", "Bearer header.payload.signature", false},
		{"basic scheme", "Basic ak_abcdefghijkl_secret", false},
		{"absent", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			_, got := serviceKeyFromHeader(req)
			if got != tc.want {
				t.Fatalf("isServiceKey = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestActorIsMemberOfOrgServer_ServiceActor runs the shared org-auth table
// against the server-level guard — the sixth consumer of the same rule.
func TestActorIsMemberOfOrgServer_ServiceActor(t *testing.T) {
	for _, tc := range orgauthcases.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := actorIsMemberOfOrgServer(tc.Ctx, nil, tc.OrgID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.WantMember {
				t.Fatalf("member = %v, want %v", got, tc.WantMember)
			}
		})
	}
}

// TestEnforceMembershipInOrg_ServiceActorDeniedOtherOrg pins the ordering bug
// this feature had to avoid: the service decision must precede the
// membershipQueries nil-guard, or a key of another organization slips through
// the permissive branches in hpayments / hbankaccounts.
func TestEnforceMembershipInOrg_ServiceActorDeniedOtherOrg(t *testing.T) {
	s := newAPIKeyServer(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/orgs/x", nil)
	req = req.WithContext(orgauthcases.ServiceCtx(orgauthcases.KeyOrgID))
	rec := httptest.NewRecorder()

	if s.enforceMembershipInOrg(rec, req, orgauthcases.OtherOrgID) {
		t.Fatal("service actor must not reach another organization")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/orgs/x", nil)
	req = req.WithContext(orgauthcases.ServiceCtx(orgauthcases.KeyOrgID))
	if !s.enforceMembershipInOrg(rec, req, orgauthcases.KeyOrgID) {
		t.Fatalf("service actor must reach its own organization, got %d", rec.Code)
	}
}
