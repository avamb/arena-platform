package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A device actor is built only by the device-token middleware (spec 69,
// MA-10): a correctly signed JWT whose actor_type is "device" is refused by
// both verifiers, and the stub never mints one.

const deviceRefusalSecret = "supersecretkey32bytes1234567890ab"

func signDeviceClaimJWT(t *testing.T, issuer, audience string) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":        "00000000-0000-0000-0000-000000000009",
		"iss":        issuer,
		"aud":        []string{audience},
		"iat":        now.Unix(),
		"nbf":        now.Add(-time.Minute).Unix(),
		"exp":        now.Add(time.Hour).Unix(),
		"actor_type": "device",
		"roles":      []string{"platform_superadmin"},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(deviceRefusalSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func TestJWTVerifier_RefusesDeviceActorType(t *testing.T) {
	v, err := NewJWTVerifier(deviceRefusalSecret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}
	_, err = v.Verify(context.Background(), signDeviceClaimJWT(t, "arena-api", "arena-api"))
	if !errors.Is(err, ErrDeviceActorClaim) {
		t.Fatalf("Verify: got %v, want ErrDeviceActorClaim", err)
	}
}

func TestStubProvider_RefusesDeviceActorType(t *testing.T) {
	p, err := NewStubProvider(StubConfig{Secret: deviceRefusalSecret, Enabled: true})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	_, err = p.Verify(context.Background(), signDeviceClaimJWT(t, p.Issuer(), p.Audience()))
	if !errors.Is(err, ErrDeviceActorClaim) {
		t.Fatalf("Verify: got %v, want ErrDeviceActorClaim", err)
	}
}

func TestStubProvider_NeverMintsDeviceActorType(t *testing.T) {
	p, err := NewStubProvider(StubConfig{Secret: deviceRefusalSecret, Enabled: true})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	tok, _, err := p.IssueToken(context.Background(), IssueRequest{ActorID: "00000000-0000-0000-0000-000000000009", ActorType: ActorTypeDevice})
	if !errors.Is(err, ErrDeviceActorClaim) || tok != "" {
		t.Fatalf("IssueToken: got token=%q err=%v, want ErrDeviceActorClaim", tok, err)
	}
}

func TestMiddleware_DeviceActorJWT_IsInvalidToken(t *testing.T) {
	p, err := NewStubProvider(StubConfig{Secret: deviceRefusalSecret, Enabled: true})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}
	called := false
	h := Middleware(p, MiddlewareOptions{})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+signDeviceClaimJWT(t, p.Issuer(), p.Audience()))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if called {
		t.Fatal("next was called for a device actor_type JWT")
	}
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"auth.invalid_token"`) {
		t.Fatalf("got %d %s, want 401 auth.invalid_token", rec.Code, rec.Body.String())
	}
}
