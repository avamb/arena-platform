// Package networkscope implements authorization helpers that constrain a
// network_operator actor (feature #203) to the operator_networks they have
// been assigned to, and to the organizations / events / orders / tickets /
// reports / channels that reach them through the
// network -> network_organizations -> organization chain established by
// migration 0043_operator_networks.sql (feature #204).
//
// # Layering
//
// This package is layered ON TOP of the existing platform/permissions
// PermissionBoundary — it does not replace it. The expected request guard
// pipeline is:
//
//	auth.RequireAuth(...)
//	  -> permissions.RequirePermission(checker, "network.read", "network")
//	     -> networkscope.RequireNetworkScope(scoper, "network_id")
//	        -> handler
//
// permissions answers the question "is this actor allowed to perform
// network.read?". networkscope answers the orthogonal question "is the
// SPECIFIC resource being addressed (this network_id / org_id / event_id)
// reachable from one of THIS actor's assigned networks?". Both must pass.
//
// The package intentionally does NOT weaken any existing permission check:
// the helpers ALWAYS allow platform_superadmin (the documented bypass) and
// otherwise default-deny. There is no path that silently widens an existing
// guard.
//
// # Bypass roles
//
// Two role labels bypass network-scope enforcement entirely so the platform
// owner is never locked out of operational tooling:
//
//   - "platform_superadmin" — full platform access (see migration 0034 and
//     feature #166); always returns nil from every scope helper.
//   - "admin"               — legacy broad-grant role from migration 0008,
//     preserved here so re-seeding stays idempotent and the original admin
//     UI keeps working while network_operator is rolled out.
//
// platform_operator does NOT bypass: feature #206 explicitly preserves its
// behavior unchanged, and it gets no network.* permission grant.
package networkscope

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// ErrOutOfScope is the sentinel returned (wrapped) by every assert helper
// when the actor is authenticated but the addressed resource is not reachable
// via any of the actor's assigned networks.
//
// Wrap via fmt.Errorf("…: %w", ErrOutOfScope) to attach context without losing
// identity. Tests should use errors.Is(err, ErrOutOfScope) rather than
// string-matching.
var ErrOutOfScope = errors.New("networkscope: resource out of network scope")

// ErrUnauthenticated is returned when the helper cannot find an actor on the
// context. The middleware maps it to HTTP 401 (rather than 403) so an
// anonymous request can be reissued with credentials.
var ErrUnauthenticated = errors.New("networkscope: unauthenticated")

// OutOfScopeError is the structured error returned by all assert helpers
// when the actor exists but lacks scope to the addressed resource. It
// satisfies the adapters/http DomainErrStatus extension (HTTPStatus() int)
// so the existing error envelope maps it to HTTP 403 automatically.
type OutOfScopeError struct {
	// Resource is the resource type the actor tried to address, e.g.
	// "network", "organization", "event", "order", "ticket".
	Resource string
	// ResourceID is the addressed ID (empty when the request never reached
	// the lookup stage, e.g. malformed UUID).
	ResourceID string
}

// Error implements the error interface.
func (e *OutOfScopeError) Error() string {
	if e.ResourceID == "" {
		return fmt.Sprintf("networkscope: %s out of scope", e.Resource)
	}
	return fmt.Sprintf("networkscope: %s %s out of scope", e.Resource, e.ResourceID)
}

// Unwrap returns ErrOutOfScope so errors.Is works on wrapped values.
func (e *OutOfScopeError) Unwrap() error { return ErrOutOfScope }

// HTTPStatus returns 403 Forbidden so the adapters/http error mapper picks
// it up without any extra registration.
func (e *OutOfScopeError) HTTPStatus() int { return http.StatusForbidden }

// -----------------------------------------------------------------------------
// Querier — the data boundary
// -----------------------------------------------------------------------------

// Querier is the narrow data interface that the Scoper needs to evaluate
// scope. The concrete production implementation wraps *gen.Queries (whose
// ListNetworksByUser / ListNetworksByOrganization methods produce the rows
// we project down to bare IDs here) — defining the interface in terms of
// primitives keeps the networkscope package free of an adapter-layer import
// and makes the unit tests trivial to fake.
type Querier interface {
	// ListNetworkIDsByUser returns the IDs of every non-archived
	// operator_network the user holds an ACTIVE network_users row on.
	// Returns an empty slice (not an error) when the user has no
	// assignments.
	ListNetworkIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)

	// ListNetworkIDsByOrganization returns the IDs of every non-archived
	// operator_network the organization is currently attached to via an
	// ACTIVE network_organizations row. assignmentKind may be nil (any
	// kind), or one of "organizer" / "agent" to filter to a single
	// attachment kind. Returns an empty slice (not an error) when the org
	// is not attached to any live network.
	ListNetworkIDsByOrganization(
		ctx context.Context,
		orgID uuid.UUID,
		assignmentKind *string,
	) ([]uuid.UUID, error)
}

// -----------------------------------------------------------------------------
// Bypass roles
// -----------------------------------------------------------------------------

// BypassRoles is the canonical list of role names that bypass every
// network-scope check. Exported so tests and downstream packages can
// reference it (rather than duplicating string literals).
//
// IMPORTANT: do NOT add "platform_operator" here — feature #206 explicitly
// preserves its behavior, and it must not gain implicit network access.
var BypassRoles = []string{"platform_superadmin", "admin"}

// IsBypassRole reports whether role grants an unconditional scope bypass.
func IsBypassRole(role string) bool {
	for _, r := range BypassRoles {
		if r == role {
			return true
		}
	}
	return false
}

// actorHasBypass reports whether actor holds any BypassRole.
func actorHasBypass(a auth.Actor) bool {
	for _, r := range a.Roles {
		if IsBypassRole(r) {
			return true
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// Scoper — the helper engine
// -----------------------------------------------------------------------------

// Scoper provides the network-scope helpers used by request guards.
//
// Construct one with NewScoper(querier) and share it across the process —
// it is safe for concurrent use by multiple goroutines because the
// embedded Querier is expected to be a *gen.Queries (or equivalent
// connection-pooled wrapper) which is itself goroutine-safe.
type Scoper struct {
	db Querier
}

// NewScoper constructs a Scoper backed by querier. Passing a nil querier
// is a programmer error and the constructor will panic — that is preferable
// to silently allowing every request through.
func NewScoper(querier Querier) *Scoper {
	if querier == nil {
		panic("networkscope: NewScoper requires a non-nil Querier")
	}
	return &Scoper{db: querier}
}

// LoadAssignedNetworks returns the set of operator_network IDs the actor
// in ctx is currently assigned to as a network_operator (active network_users
// rows on non-archived networks).
//
// Bypass actors (platform_superadmin / admin) are returned a nil slice and
// nil error: callers MUST treat nil from a bypass actor as "all networks"
// rather than "no networks". The IsBypass helper makes this explicit at the
// call site.
//
// Returns ErrUnauthenticated when no actor is present on the context.
func (s *Scoper) LoadAssignedNetworks(ctx context.Context) ([]uuid.UUID, error) {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		return nil, ErrUnauthenticated
	}
	if actorHasBypass(actor) {
		return nil, nil
	}
	uid, err := uuid.Parse(actor.ID)
	if err != nil {
		// An authenticated actor whose ID is not a UUID cannot own
		// network_users rows. Default-deny rather than skipping the
		// lookup silently.
		return nil, nil
	}
	return s.db.ListNetworkIDsByUser(ctx, uid)
}

// IsBypass reports whether the actor on ctx holds any BypassRole. Useful
// when callers need to short-circuit a list query rather than asserting on
// a specific resource ID.
func (s *Scoper) IsBypass(ctx context.Context) bool {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok {
		return false
	}
	return actorHasBypass(actor)
}

// AssertNetworkInScope returns nil when the actor on ctx is allowed to
// address the given operator_network ID. Returns:
//
//   - ErrUnauthenticated when no actor is on the context.
//   - *OutOfScopeError (wrapping ErrOutOfScope) when the actor exists but
//     networkID is not one of the actor's assigned networks.
//   - A plain error when the underlying Querier call fails (DB blip etc.).
//
// Bypass actors always succeed.
func (s *Scoper) AssertNetworkInScope(ctx context.Context, networkID uuid.UUID) error {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		return ErrUnauthenticated
	}
	if actorHasBypass(actor) {
		return nil
	}
	uid, err := uuid.Parse(actor.ID)
	if err != nil {
		return &OutOfScopeError{Resource: "network", ResourceID: networkID.String()}
	}
	ids, err := s.db.ListNetworkIDsByUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("networkscope: load assigned networks: %w", err)
	}
	for _, id := range ids {
		if id == networkID {
			return nil
		}
	}
	return &OutOfScopeError{Resource: "network", ResourceID: networkID.String()}
}

// AssertOrganizationInScope returns nil when at least one of the
// non-archived operator_networks the organization is currently attached to
// (via an active network_organizations row) is also one of the actor's
// assigned networks.
//
// assignmentKind constrains the lookup: pass nil to accept either kind,
// pointer to "organizer" or "agent" to require a specific attachment kind.
//
// Bypass actors always succeed.
//
// Returns:
//   - ErrUnauthenticated when no actor is on the context.
//   - *OutOfScopeError when no overlapping network exists.
//   - A plain error when the Querier call fails.
func (s *Scoper) AssertOrganizationInScope(
	ctx context.Context,
	orgID uuid.UUID,
	assignmentKind *string,
) error {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok || !actor.IsAuthenticated() {
		return ErrUnauthenticated
	}
	if actorHasBypass(actor) {
		return nil
	}
	uid, err := uuid.Parse(actor.ID)
	if err != nil {
		return &OutOfScopeError{Resource: "organization", ResourceID: orgID.String()}
	}

	userNets, err := s.db.ListNetworkIDsByUser(ctx, uid)
	if err != nil {
		return fmt.Errorf("networkscope: load assigned networks: %w", err)
	}
	if len(userNets) == 0 {
		return &OutOfScopeError{Resource: "organization", ResourceID: orgID.String()}
	}

	orgNets, err := s.db.ListNetworkIDsByOrganization(ctx, orgID, assignmentKind)
	if err != nil {
		return fmt.Errorf("networkscope: load org networks: %w", err)
	}
	if intersects(userNets, orgNets) {
		return nil
	}
	return &OutOfScopeError{Resource: "organization", ResourceID: orgID.String()}
}

// AssertResourceInScope is the generic helper used for resources reached
// through a known owner_organization_id (orders, tickets, events, channels,
// reports, etc.). Resolve the owning organization ID at the call site and
// pass it here together with a descriptive resource label.
//
// resourceLabel is used only for the *OutOfScopeError so observability and
// the JSON error envelope can attribute the denial; it has no effect on the
// decision.
//
// Bypass actors always succeed.
func (s *Scoper) AssertResourceInScope(
	ctx context.Context,
	resourceLabel, resourceID string,
	ownerOrgID uuid.UUID,
) error {
	if err := s.AssertOrganizationInScope(ctx, ownerOrgID, nil); err != nil {
		// Re-tag the OutOfScopeError with the caller's resource label so
		// the API response identifies the addressed resource (e.g.
		// "order") rather than the intermediate organization.
		var scopeErr *OutOfScopeError
		if errors.As(err, &scopeErr) {
			return &OutOfScopeError{Resource: resourceLabel, ResourceID: resourceID}
		}
		return err
	}
	return nil
}

// intersects reports whether the two ID slices share at least one element.
// O(len(a)*len(b)) — acceptable because both slices are small (a is the
// caller's network assignments, b is the resource's attachments; both are
// usually single-digit and capped by tens).
func intersects(a, b []uuid.UUID) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// -----------------------------------------------------------------------------
// HTTP middleware
// -----------------------------------------------------------------------------

// RequireNetworkScope is a chi-style middleware that extracts a URL parameter
// (by name) as a UUID and asserts that the actor on the request context is
// in-scope for that operator_network.
//
// Example wiring:
//
//	r.Route("/v1/operator-networks/{network_id}", func(r chi.Router) {
//	    r.Use(networkscope.RequireNetworkScope(scoper, "network_id"))
//	    r.Get("/", listMembers)
//	    r.Get("/sales", listSales)
//	})
//
// On success the request is forwarded unchanged. On any failure the
// middleware writes a JSON error envelope and stops the chain.
func RequireNetworkScope(scoper *Scoper, paramName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := chi.URLParam(r, paramName)
			id, err := uuid.Parse(raw)
			if err != nil {
				writeScopeError(w, r, http.StatusBadRequest,
					"networkscope.invalid_id",
					"The supplied network identifier is not a valid UUID.",
					"network", raw)
				return
			}
			if err := scoper.AssertNetworkInScope(r.Context(), id); err != nil {
				handleScopeError(w, r, err, "network", id.String())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireOrganizationScope is the org-flavoured counterpart of
// RequireNetworkScope. It reads the org UUID from the named URL parameter and
// asserts that at least one operator_network reaches it via an active
// network_organizations row that is also one of the actor's assigned
// networks.
//
// Pass kind = nil to accept either organizer or agent attachments; pass a
// pointer to "organizer" or "agent" to require a specific attachment kind.
func RequireOrganizationScope(scoper *Scoper, paramName string, kind *string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := chi.URLParam(r, paramName)
			id, err := uuid.Parse(raw)
			if err != nil {
				writeScopeError(w, r, http.StatusBadRequest,
					"networkscope.invalid_id",
					"The supplied organization identifier is not a valid UUID.",
					"organization", raw)
				return
			}
			if err := scoper.AssertOrganizationInScope(r.Context(), id, kind); err != nil {
				handleScopeError(w, r, err, "organization", id.String())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// handleScopeError translates a Scoper error into an HTTP response. Errors
// of type *OutOfScopeError become 403; ErrUnauthenticated becomes 401; any
// other error (Querier failure) becomes 500.
func handleScopeError(w http.ResponseWriter, r *http.Request, err error, resource, resourceID string) {
	logger := logging.FromContext(r.Context())

	if errors.Is(err, ErrUnauthenticated) {
		logger.Warn("networkscope: unauthenticated",
			"resource", resource,
			"resource_id", resourceID,
		)
		writeScopeError(w, r, http.StatusUnauthorized,
			"networkscope.unauthenticated",
			"Authentication is required to access this resource.",
			resource, resourceID)
		return
	}

	var scopeErr *OutOfScopeError
	if errors.As(err, &scopeErr) {
		logger.Warn("networkscope: out of scope",
			"resource", resource,
			"resource_id", resourceID,
		)
		writeScopeError(w, r, http.StatusForbidden,
			"networkscope.out_of_scope",
			"This resource is not within your assigned network scope.",
			resource, resourceID)
		return
	}

	logger.Error("networkscope: lookup error",
		"resource", resource,
		"resource_id", resourceID,
		"error", err,
	)
	writeScopeError(w, r, http.StatusInternalServerError,
		"networkscope.internal_error",
		"An internal error occurred while evaluating network scope.",
		resource, resourceID)
}

// writeScopeError emits the uniform JSON envelope used by this package. Kept
// deliberately separate from adapters/http.WriteError to preserve the
// platform-layer-has-no-adapter-imports boundary.
func writeScopeError(w http.ResponseWriter, _ *http.Request, status int, code, message, resource, resourceID string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	body := fmt.Sprintf(
		`{"error":{"code":%q,"message":%q,"resource":%q,"resource_id":%q}}`,
		code, message, resource, resourceID,
	)
	_, _ = w.Write([]byte(body))
}
