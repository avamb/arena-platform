// Package permissions implements the PermissionChecker boundary for the
// arena_new backend foundation milestone.
//
// # PLACEHOLDER — This is a milestone scaffold
//
// The real role/scope enforcement (RBAC, tenant-scoped permissions, OAuth
// scopes) is out of scope for this milestone and will be delivered in a later
// milestone on top of this foundation.  The boundary defined here is stable:
// the Checker interface, error types, and HTTP middleware will not change shape
// when the real implementation arrives — only AllowAllChecker is swapped for a
// real engine.
//
// # Usage
//
// Wire AllowAllChecker in production (any authenticated actor passes for this
// milestone) and DenyAllChecker in tests that verify rejection paths:
//
//	// production main / server setup
//	var permChecker permissions.Checker = permissions.AllowAll()
//
//	// test that expects a 403
//	var permChecker permissions.Checker = permissions.DenyAll()
//
// Use RequirePermission to protect individual routes:
//
//	r.With(permissions.RequirePermission(permChecker, "events", "write")).
//	    Post("/events", handleCreateEvent)
package permissions

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// -----------------------------------------------------------------------------
// Error types
// -----------------------------------------------------------------------------

// ErrPermissionDenied is returned by Checker.Check when the caller does not
// hold the required permission.  Wrap it via fmt.Errorf("…: %w",
// ErrPermissionDenied) to attach context without losing identity.
var ErrPermissionDenied = errors.New("permissions: permission denied")

// PermissionDeniedError is a structured error returned by DenyAllChecker and
// the real engine.  It satisfies both the error interface and the adapters/http
// extension interfaces (so DomainErrStatus maps it to HTTP 403 automatically).
type PermissionDeniedError struct {
	Action   string
	Resource string
}

// Error implements the error interface.
func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied: action=%q resource=%q", e.Action, e.Resource)
}

// Unwrap returns ErrPermissionDenied so errors.Is works on wrapped values.
func (e *PermissionDeniedError) Unwrap() error { return ErrPermissionDenied }

// HTTPStatus returns 403 Forbidden so the adapters/http.DomainErrStatus mapper
// picks it up without any extra registration.
func (e *PermissionDeniedError) HTTPStatus() int { return http.StatusForbidden }

// -----------------------------------------------------------------------------
// Checker interface  (the PermissionBoundary)
// -----------------------------------------------------------------------------

// Checker is the contract that every permission-enforcement adapter implements.
//
// Action is a verb-style string like "create", "read", "update", "delete" or a
// domain-specific action like "publish", "scan".
//
// Resource is the resource type being acted on, e.g. "event", "ticket",
// "organization".  Future milestones will add a resource ID parameter for
// row-level checks; callers must not rely on the argument count staying
// constant.
//
// A nil error means the actor is permitted.  ErrPermissionDenied (or a value
// wrapping it) is returned when the actor is denied.  Other errors signal
// infrastructure failures (cache unavailable, DB timeout, etc.) and should
// surface as HTTP 500.
type Checker interface {
	// Check validates that the actor stored in ctx may perform action on
	// resource.  The actor is retrieved from the context via
	// auth.ActorFromContext — callers MUST place the auth middleware before any
	// RequirePermission middleware so the actor is present.
	Check(ctx context.Context, action, resource string) error
}

// -----------------------------------------------------------------------------
// AllowAllChecker — milestone no-op implementation
// -----------------------------------------------------------------------------

// allowAllChecker is the milestone-phase Checker: any authenticated actor passes.
// Use AllowAll() to construct one.
type allowAllChecker struct{}

// AllowAll returns a Checker that unconditionally returns nil for every
// (action, resource) pair.  All actors — authenticated or not — are permitted.
//
// This is the production implementation for the foundation milestone.  The real
// RBAC engine replaces it in the next milestone without touching any call site.
func AllowAll() Checker { return allowAllChecker{} }

// Check always returns nil (allow).
func (allowAllChecker) Check(_ context.Context, _, _ string) error { return nil }

// -----------------------------------------------------------------------------
// DenyAllChecker — test helper
// -----------------------------------------------------------------------------

// denyAllChecker is a Checker that rejects every request.  Use DenyAll() to
// construct one.
type denyAllChecker struct{}

// DenyAll returns a Checker that unconditionally returns a *PermissionDeniedError
// for every (action, resource) pair.  Useful in unit tests that verify that
// the 403 path is exercised correctly.
func DenyAll() Checker { return denyAllChecker{} }

// Check always returns a *PermissionDeniedError.
func (denyAllChecker) Check(_ context.Context, action, resource string) error {
	return &PermissionDeniedError{Action: action, Resource: resource}
}

// -----------------------------------------------------------------------------
// HTTP middleware — RequirePermission
// -----------------------------------------------------------------------------

// RequirePermission returns a net/http middleware that calls
// checker.Check(ctx, action, resource) before forwarding the request to the
// next handler.
//
// On success (nil error) the request is forwarded unchanged.
// On *PermissionDeniedError the middleware writes HTTP 403 with a JSON error
// envelope and the "permissions.denied" error code, then stops the chain.
// On any other error (infrastructure failure) it writes HTTP 500 with
// "permissions.internal_error".
//
// The middleware reads the request context's logger (via logging.FromContext) so
// all permission denials are emitted as structured WARN log entries that carry
// the full request_id / trace_id correlation chain.
//
// Example chi usage:
//
//	r.With(permissions.RequirePermission(checker, "event", "write")).
//	    Post("/events", createEventHandler)
func RequirePermission(checker Checker, action, resource string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := checker.Check(r.Context(), action, resource); err != nil {
				logger := logging.FromContext(r.Context())

				if errors.Is(err, ErrPermissionDenied) {
					logger.Warn("permissions: access denied",
						"action", action,
						"resource", resource,
					)
					writePermError(w, http.StatusForbidden, "permissions.denied",
						"You do not have permission to perform this action.")
					return
				}

				// Infrastructure error (DB timeout, cache miss, etc.)
				logger.Error("permissions: checker error",
					"action", action,
					"resource", resource,
					"error", err,
				)
				writePermError(w, http.StatusInternalServerError, "permissions.internal_error",
					"An internal error occurred while evaluating permissions.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writePermError writes the uniform JSON error envelope used by this package.
// It does NOT depend on adapters/http.WriteError to keep the platform layer
// free of adapter-layer imports (clean architecture boundary).
func writePermError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Minimal envelope — identical in shape to adapters/http.ErrorEnvelope so
	// API clients only need to parse one format.
	body := fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message)
	_, _ = w.Write([]byte(body))
}
