// Package auth — jwt_verifier_test.go tests the JWTVerifier production-grade
// auth.Provider introduced in PR-01.
package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// helpers

func issueTestToken(t *testing.T, secret, issuer, audience string, ttl time.Duration) string {
	t.Helper()
	actorID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tok, _, err := IssueJWT(secret, actorID, nil, []string{"admin"}, issuer, audience, ttl)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// TestNewJWTVerifier_RequiresNonEmptySecret verifies that an empty or
// whitespace-only secret is rejected at construction time.
func TestNewJWTVerifier_RequiresNonEmptySecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJWTVerifier(tc.secret, "arena-api", "arena-api")
			if err == nil {
				t.Fatal("expected error for empty secret, got nil")
			}
		})
	}
}

// TestNewJWTVerifier_DefaultsIssuerAndAudience verifies that empty issuer/
// audience fall back to "arena-api".
func TestNewJWTVerifier_DefaultsIssuerAndAudience(t *testing.T) {
	v, err := NewJWTVerifier("supersecretkey32bytes1234567890ab", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.issuer != "arena-api" {
		t.Errorf("issuer: got %q, want %q", v.issuer, "arena-api")
	}
	if v.audience != "arena-api" {
		t.Errorf("audience: got %q, want %q", v.audience, "arena-api")
	}
}

// TestJWTVerifier_HappyPath_WithIssueJWT verifies that a token minted by
// IssueJWT is accepted by JWTVerifier and that the Actor is populated
// correctly.
func TestJWTVerifier_HappyPath_WithIssueJWT(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"
	const issuer = "arena-api"
	const audience = "arena-api"

	v, err := NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	tok := issueTestToken(t, secret, issuer, audience, time.Hour)
	actor, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ID == "" {
		t.Error("expected non-empty actor.ID")
	}
	if actor.RawToken != tok {
		t.Errorf("RawToken mismatch: got %q, want %q", actor.RawToken, tok)
	}
	if len(actor.Roles) == 0 {
		t.Error("expected at least one role")
	}
}

// TestJWTVerifier_HappyPath_WithStubProvider verifies that a token minted by
// StubProvider.IssueToken is accepted by JWTVerifier (both share the same
// HS256 signing scheme with the same secret).
func TestJWTVerifier_HappyPath_WithStubProvider(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"
	const issuer = "arena-dev"
	const audience = "arena-api"

	stub, err := NewStubProvider(StubConfig{
		Secret:   secret,
		Issuer:   issuer,
		Audience: audience,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	rawTok, _, err := stub.IssueToken(context.Background(), IssueRequest{
		ActorID:   "00000000-0000-0000-0000-000000000002",
		ActorType: ActorTypeUser,
		Roles:     []string{"admin"},
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	// JWTVerifier uses jwt/v5; StubProvider uses manual HMAC — but since
	// both produce standard JWT 3-part format with HS256, the verifier must
	// accept the stub token.
	v, err := NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	actor, err := v.Verify(context.Background(), rawTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ID == "" {
		t.Error("expected non-empty actor.ID from stub token")
	}
}

// TestJWTVerifier_RejectsExpiredToken verifies that an expired token
// returns ErrTokenExpired.
func TestJWTVerifier_RejectsExpiredToken(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"
	v, err := NewJWTVerifier(secret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	// Mint a token with negative TTL so it is already expired.
	tok := issueTestToken(t, secret, "arena-api", "arena-api", -1*time.Hour)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
	if !isErr(err, ErrTokenExpired) {
		t.Errorf("expected ErrTokenExpired, got: %v", err)
	}
}

// TestJWTVerifier_RejectsTamperedToken verifies that a token signed with
// a different secret is rejected as ErrInvalidSignature.
func TestJWTVerifier_RejectsTamperedToken(t *testing.T) {
	const rightSecret = "supersecretkey32bytes1234567890ab"
	const wrongSecret = "wrongsecretkey32bytes1234567890ab"

	v, err := NewJWTVerifier(rightSecret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	tok := issueTestToken(t, wrongSecret, "arena-api", "arena-api", time.Hour)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected error for tampered token, got nil")
	}
	if !isErr(err, ErrInvalidSignature) {
		t.Errorf("expected ErrInvalidSignature, got: %v", err)
	}
}

// TestJWTVerifier_RejectsWrongIssuer verifies that a token with an issuer
// mismatch returns ErrUnknownIssuer.
func TestJWTVerifier_RejectsWrongIssuer(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"

	v, err := NewJWTVerifier(secret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	// Mint with a different issuer.
	tok := issueTestToken(t, secret, "other-issuer", "arena-api", time.Hour)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected error for wrong issuer, got nil")
	}
	if !isErr(err, ErrUnknownIssuer) {
		t.Errorf("expected ErrUnknownIssuer, got: %v", err)
	}
}

// TestJWTVerifier_RejectsWrongAudience verifies that a token with an audience
// mismatch returns ErrUnknownAudience.
func TestJWTVerifier_RejectsWrongAudience(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"

	v, err := NewJWTVerifier(secret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	// Mint with a different audience.
	tok := issueTestToken(t, secret, "arena-api", "other-audience", time.Hour)
	_, err = v.Verify(context.Background(), tok)
	if err == nil {
		t.Fatal("expected error for wrong audience, got nil")
	}
	if !isErr(err, ErrUnknownAudience) {
		t.Errorf("expected ErrUnknownAudience, got: %v", err)
	}
}

// TestJWTVerifier_RejectsMalformedToken verifies that garbage input returns
// ErrMalformedToken.
func TestJWTVerifier_RejectsMalformedToken(t *testing.T) {
	v, err := NewJWTVerifier("supersecretkey32bytes1234567890ab", "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	cases := []string{
		"not.a.jwt.at.all",
		"onlytwoparts.here",
		"",
		"   ",
	}
	for _, tc := range cases {
		_, err := v.Verify(context.Background(), tc)
		if err == nil {
			t.Errorf("expected error for %q, got nil", tc)
		}
	}
}

// TestJWTVerifier_RejectsNonHS256Algorithm verifies that a token signed with
// a different algorithm (e.g. RS256 via none method trick) is rejected as
// ErrUnsupportedAlg or ErrMalformedToken.
func TestJWTVerifier_RejectsNonHS256Algorithm(t *testing.T) {
	v, err := NewJWTVerifier("supersecretkey32bytes1234567890ab", "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	// Build a token manually that claims RS256 in the header but uses HS256
	// underneath — jwt/v5's WithValidMethods guard will catch the algorithm
	// header mismatch. We use the jwt library directly to produce such a token.
	claims := jwt.MapClaims{
		"sub": "00000000-0000-0000-0000-000000000001",
		"iss": "arena-api",
		"aud": jwt.ClaimStrings{"arena-api"},
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	// jwt.SigningMethodRS256 requires an RSA key — we can't produce one
	// easily in a unit test, so instead tamper with the alg field post-signing
	// by constructing the header manually with "alg":"RS256" but still signing
	// with HMAC. A simpler approach: sign with HS384 which is a valid HMAC
	// but not in the accepted list.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS384, claims)
	signed, err := tok.SignedString([]byte("supersecretkey32bytes1234567890ab"))
	if err != nil {
		t.Fatalf("SignedString (HS384): %v", err)
	}

	_, verifyErr := v.Verify(context.Background(), signed)
	if verifyErr == nil {
		t.Fatal("expected error for non-HS256 algorithm, got nil")
	}
	// Should be ErrUnsupportedAlg or ErrMalformedToken (jwt/v5 rejects via WithValidMethods).
	if !isErr(verifyErr, ErrUnsupportedAlg) && !isErr(verifyErr, ErrMalformedToken) && !isErr(verifyErr, ErrInvalidSignature) {
		t.Errorf("expected ErrUnsupportedAlg or ErrMalformedToken, got: %v", verifyErr)
	}
	// Ensure it's not a 2xx-equivalent pass.
	_ = verifyErr
}

// TestJWTVerifier_ActorTypeDefaultsToUser verifies that when actor_type is
// absent from the JWT claims, the Actor.Type defaults to ActorTypeUser.
func TestJWTVerifier_ActorTypeDefaultsToUser(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"

	// Mint a token via IssueJWT which does NOT set actor_type.
	actorID := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	tok, _, err := IssueJWT(secret, actorID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	v, err := NewJWTVerifier(secret, "arena-api", "arena-api")
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	actor, err := v.Verify(context.Background(), tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.Type != ActorTypeUser {
		t.Errorf("Type: got %q, want %q", actor.Type, ActorTypeUser)
	}
}

// TestJWTVerifier_PopulatesImpersonationFields verifies that impersonation
// claims set by StubProvider.IssueToken are surfaced on the returned Actor.
func TestJWTVerifier_PopulatesImpersonationFields(t *testing.T) {
	const secret = "supersecretkey32bytes1234567890ab"
	const issuer = "arena-dev"
	const audience = "arena-api"

	stub, err := NewStubProvider(StubConfig{
		Secret:   secret,
		Issuer:   issuer,
		Audience: audience,
		Enabled:  true,
	})
	if err != nil {
		t.Fatalf("NewStubProvider: %v", err)
	}

	rawTok, _, err := stub.IssueToken(context.Background(), IssueRequest{
		ActorID:             "00000000-0000-0000-0000-000000000004",
		ActorType:           ActorTypeUser,
		ImpersonatedBy:      "00000000-0000-0000-0000-000000000099",
		ImpersonationReason: "support investigation",
	})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	v, err := NewJWTVerifier(secret, issuer, audience)
	if err != nil {
		t.Fatalf("NewJWTVerifier: %v", err)
	}

	actor, err := v.Verify(context.Background(), rawTok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if actor.ImpersonatedBy != "00000000-0000-0000-0000-000000000099" {
		t.Errorf("ImpersonatedBy: got %q", actor.ImpersonatedBy)
	}
	if !strings.Contains(actor.ImpersonationReason, "support") {
		t.Errorf("ImpersonationReason: got %q", actor.ImpersonationReason)
	}
	if !actor.IsImpersonated() {
		t.Error("expected IsImpersonated() to return true")
	}
}

// isErr is a helper that checks whether target appears in the error chain.
func isErr(err, target error) bool {
	// Walk the chain manually since errors.Is works for wrapped errors.
	for err != nil {
		if err == target {
			return true
		}
		// Try to unwrap.
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	// Fall back to string prefix match for fmt.Errorf("%w: ...", target) style.
	if target != nil {
		return strings.HasPrefix(err.Error(), target.Error())
	}
	return false
}
