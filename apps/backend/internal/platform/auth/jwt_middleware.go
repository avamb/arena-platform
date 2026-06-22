// Package auth — jwt_middleware.go implements ValidateJWT, the net/http
// middleware that verifies HMAC-SHA256 (HS256) bearer tokens using the
// github.com/golang-jwt/jwt/v5 library and attaches the resulting
// AuthContext to the request context.
//
// # PLACEHOLDER
//
// ValidateJWT is part of the Auth Boundary PLACEHOLDER for the Backend
// Foundation Milestone. In a later milestone the HS256 shared-secret scheme
// will be replaced by RS256 / ECDSA verification against a real IdP (Keycloak,
// Auth0, or custom). The middleware signature and the AuthContext type it
// populates will remain stable.
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// arenaClaims is the custom JWT claims set recognised by ValidateJWT.
// Standard JWT claims are embedded via jwt.RegisteredClaims; the arena-specific
// fields (actor_type, roles, org_id) extend the standard payload.
type arenaClaims struct {
	jwt.RegisteredClaims

	// ActorType discriminates the principal kind ("stub_user", "user", etc.).
	// Stored as a string claim so it survives JSON round-trips without an enum.
	ActorType string `json:"actor_type,omitempty"`

	// Roles is the coarse-grained role set. Maps to AuthContext.Roles.
	Roles []string `json:"roles,omitempty"`

	// OrgID is an optional organisation UUID string. Maps to AuthContext.OrgID.
	OrgID string `json:"org_id,omitempty"`
}

// ValidateJWT returns a net/http middleware that:
//  1. Extracts the Authorization Bearer token from the request header.
//  2. Parses and validates the HS256 signature against secret.
//  3. Checks standard time claims (exp, nbf, iat) via the jwt/v5 library.
//  4. Populates an AuthContext and stores it in the request context via
//     WithAuthContext so downstream handlers can call FromContext.
//
// On any validation failure the middleware writes a 401 JSON error envelope
// (matching the project-wide error shape) and does NOT call the next handler.
// A WWW-Authenticate: Bearer realm="arena" challenge header is always present
// on 401 responses so generic HTTP clients can detect the scheme.
//
// The middleware is intentionally ignorant of the StubProvider — it only needs
// the shared secret string to verify HS256 tokens. This decoupling means the
// same ValidateJWT wrapper can be used even when the issuer switches to RS256
// (just pass different validation options; the middleware signature stays the
// same).
func ValidateJWT(secret string) func(http.Handler) http.Handler {
	keyFunc := func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: algorithm %q is not HS256",
				ErrUnsupportedAlg, t.Header["alg"])
		}
		return []byte(secret), nil
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken, err := bearerFromHeader(r)
			if err != nil {
				writeAuthError(w, r, http.StatusUnauthorized, "auth.missing_token", err)
				return
			}

			var claims arenaClaims
			tok, err := jwt.ParseWithClaims(rawToken, &claims, keyFunc,
				jwt.WithValidMethods([]string{"HS256"}),
				jwt.WithExpirationRequired(),
				jwt.WithLeeway(5*time.Second),
			)
			if err != nil {
				status, code := mapJWTError(err)
				writeAuthError(w, r, status, code, err)
				return
			}
			if !tok.Valid {
				writeAuthError(w, r, http.StatusUnauthorized, "auth.invalid_token",
					errors.New("token is not valid"))
				return
			}

			ac, convErr := claimsToAuthContext(claims)
			if convErr != nil {
				writeAuthError(w, r, http.StatusUnauthorized, "auth.malformed_token", convErr)
				return
			}

			ctx := WithAuthContext(r.Context(), ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// claimsToAuthContext converts parsed JWT claims into an AuthContext.
// Returns an error when the "sub" claim is not a valid UUID — the middleware
// then rejects the token with 401 rather than panicking downstream.
func claimsToAuthContext(c arenaClaims) (AuthContext, error) {
	subStr := c.Subject
	if subStr == "" {
		return AuthContext{}, errors.New("JWT is missing the 'sub' claim")
	}
	actorID, err := uuid.Parse(subStr)
	if err != nil {
		return AuthContext{}, fmt.Errorf("JWT 'sub' claim %q is not a valid UUID: %w", subStr, err)
	}

	var orgID *uuid.UUID
	if c.OrgID != "" {
		parsed, err := uuid.Parse(c.OrgID)
		if err != nil {
			return AuthContext{}, fmt.Errorf("JWT 'org_id' claim %q is not a valid UUID: %w", c.OrgID, err)
		}
		orgID = &parsed
	}

	tokenID := c.ID // jwt.RegisteredClaims.ID is the "jti" claim

	return AuthContext{
		ActorID: actorID,
		OrgID:   orgID,
		Roles:   c.Roles,
		TokenID: tokenID,
	}, nil
}

// mapJWTError translates jwt/v5 errors into the project's HTTP status + dotted
// error code. The jwt/v5 library returns sentinel errors via errors.Is so each
// branch is precise.
func mapJWTError(err error) (int, string) {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return http.StatusUnauthorized, "auth.token_expired"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return http.StatusUnauthorized, "auth.token_not_yet_valid"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return http.StatusUnauthorized, "auth.invalid_signature"
	case errors.Is(err, jwt.ErrTokenMalformed):
		return http.StatusUnauthorized, "auth.malformed_token"
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return http.StatusUnauthorized, "auth.invalid_token"
	case errors.Is(err, ErrUnsupportedAlg):
		return http.StatusUnauthorized, "auth.unsupported_alg"
	default:
		return http.StatusUnauthorized, "auth.invalid_token"
	}
}

// IssueJWT mints a signed HS256 JWT using the standard jwt/v5 library.
// This is the counterpart to ValidateJWT — both share the same secret so
// tokens minted here are accepted by the middleware.
//
// Parameters:
//   - secret    : the HMAC signing secret (must match ValidateJWT's secret)
//   - actorID   : UUID of the authenticated principal (stored as "sub" claim)
//   - orgID     : optional organisation UUID (stored as "org_id" claim; "" = omit)
//   - roles     : coarse-grained role labels (stored as "roles" claim)
//   - issuer    : "iss" claim value (e.g. "arena-dev")
//   - audience  : "aud" claim value (e.g. "arena-api")
//   - ttl       : token lifetime; exp = now + ttl
//
// Returns the signed token string and the absolute expiry time, or an error.
func IssueJWT(
	secret string,
	actorID uuid.UUID,
	orgID *uuid.UUID,
	roles []string,
	issuer, audience string,
	ttl time.Duration,
) (string, time.Time, error) {
	if strings.TrimSpace(secret) == "" {
		return "", time.Time{}, errors.New("auth: IssueJWT requires a non-empty secret")
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)

	orgIDStr := ""
	if orgID != nil {
		orgIDStr = orgID.String()
	}

	claims := arenaClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   actorID.String(),
			Audience:  jwt.ClaimStrings{audience},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.New().String(), // unique jti per issuance
		},
		Roles: roles,
		OrgID: orgIDStr,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign JWT: %w", err)
	}
	return signed, exp, nil
}
