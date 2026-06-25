// Package auth — context.go defines the AuthContext value type that is
// embedded in every authenticated request context during this milestone.
//
// # PLACEHOLDER
//
// This is a BOUNDARY PLACEHOLDER for the Backend Foundation Milestone.
// The real identity module (OAuth 2.0, magic-link, password auth) is
// out of scope and will be delivered in a subsequent milestone.
// Call sites that read AuthContext from context will not need to change
// when the real provider replaces StubProvider — only the JWT issuer /
// verifier changes.
//
// Usage:
//
//	// Read from a request context (inside an authenticated handler):
//	if ac, ok := auth.FromContext(r.Context()); ok {
//	    log.Info("actor", "id", ac.ActorID, "roles", ac.Roles)
//	}
//
//	// Write to a context (inside middleware / tests):
//	ctx = auth.WithAuthContext(ctx, auth.AuthContext{ActorID: id, ...})
package auth

import (
	"context"

	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// AuthContext
// -----------------------------------------------------------------------------

// AuthContext is the authenticated principal attached to every request context
// after successful JWT validation. It is the canonical "who is calling this
// API?" value for the foundation milestone.
//
// Fields map to standard JWT claims:
//   - ActorID  → "sub" (subject)
//   - OrgID    → "org_id" (custom claim; nil for personal/dev tokens)
//   - Roles    → "roles" (custom claim array)
//   - TokenID  → "jti" (JWT ID; unique per token issuance)
//
// All fields are immutable after construction. The struct is intentionally
// kept small: extended claims (permissions, tenant context, etc.) belong in
// the domain layer and should be derived from AuthContext, not stored in it.
// Renaming to auth.Context would shadow the std-lib `context.Context` import in
// every caller; keeping AuthContext is a deliberate readability trade-off.
//
//nolint:revive // intentional: avoids clashing with stdlib context.Context name
type AuthContext struct {
	// ActorID is the UUID identifying the authenticated principal ("sub" claim).
	// For stub / dev tokens issued by StubProvider, this is the actor_id
	// passed to POST /v1/dev/auth/token.
	ActorID uuid.UUID

	// OrgID is the organisation the actor is acting on behalf of.
	// Nil for personal access tokens and the dev-stub tokens issued by
	// StubProvider (org concept is not yet wired in this milestone).
	OrgID *uuid.UUID

	// Roles is the coarse-grained role set encoded in the JWT ("roles" claim).
	// The PermissionBoundary uses these to make allow/deny decisions; the real
	// RBAC rules live in the domain layer and are out of scope here.
	Roles []string

	// TokenID is the JWT ID ("jti" claim). It is unique per token issuance
	// and can be used for token revocation look-ups in a later milestone.
	TokenID string
}

// -----------------------------------------------------------------------------
// Context key and helpers
// -----------------------------------------------------------------------------

// authCtxKey is the unexported context key used to store / retrieve an
// AuthContext. The type is private so external packages cannot accidentally
// store a different value under the same key.
type authCtxKey struct{}

// WithAuthContext returns a new context that carries ac. Call this from JWT
// validation middleware after successfully verifying the bearer token.
func WithAuthContext(ctx context.Context, ac AuthContext) context.Context {
	return context.WithValue(ctx, authCtxKey{}, ac)
}

// FromContext retrieves the AuthContext previously stored by WithAuthContext.
// The second return value is false when the request was never authenticated
// (e.g. an anonymous route or a missing / invalid bearer token — in which
// case the auth middleware already rejected the request before reaching the
// handler).
func FromContext(ctx context.Context) (AuthContext, bool) {
	if ctx == nil {
		return AuthContext{}, false
	}
	ac, ok := ctx.Value(authCtxKey{}).(AuthContext)
	return ac, ok
}
