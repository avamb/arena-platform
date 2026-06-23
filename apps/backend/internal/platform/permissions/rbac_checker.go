package permissions

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// RBACQuerier is the minimal database interface the DBChecker needs.
// *gen.Queries satisfies this interface; it is declared here to avoid a direct
// import of the gen package from the platform layer (clean architecture boundary).
type RBACQuerier interface {
	// GetPermissionsForRoles returns the sorted list of permission names held
	// by any of the supplied role names.
	GetPermissionsForRoles(ctx context.Context, roleNames []string) ([]string, error)
}

// MembershipQuerier is the optional secondary interface used by DBChecker to
// resolve membership-derived roles at request time (feature #120).
//
// When wired, DBChecker unions the actor's JWT roles with the roles derived from
// the user's active memberships before resolving permissions. This means that
// granting or revoking a membership takes effect on the next request without
// requiring a new JWT to be issued.
//
// *gen.Queries satisfies this interface; it is declared here to avoid a direct
// import of the gen package from the platform layer (clean architecture boundary).
type MembershipQuerier interface {
	// GetActiveRolesForUser returns the distinct set of role names held by a
	// user across all organizations (active memberships only).
	GetActiveRolesForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
}

// DBChecker is a production Checker that resolves permissions by querying the
// roles / permissions / role_permissions tables created by migration 0008_rbac.
//
// # Permission resolution algorithm
//
//  1. Extract the authenticated actor from the context (via auth.ActorFromContext).
//  2. Start with actor.Roles (role names embedded in the JWT) as the base role set.
//  3. If a MembershipQuerier is wired, union the JWT roles with the roles derived
//     from the user's active memberships (queried fresh per request).
//  4. Look up all permission names held by the combined role set from the database.
//  5. Check whether the requested action appears in that set.
//
// # Caching
//
// DBChecker maintains an in-process, per-role-set permission cache (sync.Map).
// The cache is keyed by the sorted, comma-joined role names. Cache entries are
// never evicted during a single server lifetime — role→permission mappings are
// stable configuration data that changes only when an operator explicitly updates
// the database, triggering a deployment restart. This avoids the complexity of
// TTL-based invalidation for the foundation milestone; the cache can be made
// TTL-aware in a later milestone without changing the Checker interface.
//
// Membership-derived roles are resolved fresh on each request (not cached) so
// that grant/revoke operations take effect immediately without a server restart.
// The combined role set IS cached: if two requests arrive with the same effective
// role set (JWT + memberships), the second request hits the permission cache.
//
// The cache is safe for concurrent use by multiple goroutines.
type DBChecker struct {
	db          RBACQuerier
	memberships MembershipQuerier // optional; nil = no membership lookup (feature #120)

	// permCache maps a sorted-role-set key to the set of permission names.
	// key: strings.Join(sortedRoles, ",")
	// value: map[string]struct{} — the set of permission names
	permCache sync.Map
}

// NewDBChecker constructs a DBChecker that uses db to resolve permissions.
// The db argument is typically *gen.Queries constructed from a *pgxpool.Pool.
func NewDBChecker(db RBACQuerier) *DBChecker {
	return &DBChecker{db: db}
}

// WithMembershipQuerier returns a new DBChecker that also resolves
// membership-derived roles at permission-check time (feature #120).
// The querier is typically *gen.Queries constructed from a *pgxpool.Pool.
func (c *DBChecker) WithMembershipQuerier(mq MembershipQuerier) *DBChecker {
	return &DBChecker{
		db:          c.db,
		memberships: mq,
		// Note: the new checker starts with an empty cache so stale entries
		// from the original checker do not carry over.
	}
}

// Check implements Checker. It returns nil when the authenticated actor's roles
// include at least one role that has the given action (permission name).
//
// Returns *PermissionDeniedError (wrapped in ErrPermissionDenied) when:
//   - no actor is found in the context (unauthenticated request), or
//   - none of the actor's roles holds the required permission.
//
// Returns a plain error (infrastructure failure) when the DB query fails; the
// middleware maps those to HTTP 500.
//
// When a MembershipQuerier is wired (feature #120), the actor's effective role
// set is the union of the JWT roles and the membership-derived roles. Membership
// roles are resolved fresh on each call so that grant/revoke takes effect
// immediately without requiring a new JWT.
func (c *DBChecker) Check(ctx context.Context, action, resource string) error {
	actor, ok := auth.ActorFromContext(ctx)
	if !ok {
		return &PermissionDeniedError{Action: action, Resource: resource}
	}

	// Build the effective role set: start with JWT roles.
	roles := make([]string, len(actor.Roles))
	copy(roles, actor.Roles)

	// Union with membership-derived roles when the querier is wired.
	if c.memberships != nil && actor.ID != "" {
		uid, err := uuid.Parse(actor.ID)
		if err == nil {
			memberRoles, err := c.memberships.GetActiveRolesForUser(ctx, uid)
			if err == nil {
				roles = append(roles, memberRoles...)
			}
			// If GetActiveRolesForUser fails (e.g. DB blip), fall back to
			// JWT-only roles rather than failing the whole request. Permission
			// checks may be too restrictive in that case (safer than too permissive).
		}
	}

	if len(roles) == 0 {
		return &PermissionDeniedError{Action: action, Resource: resource}
	}

	perms, err := c.resolvePermissions(ctx, roles)
	if err != nil {
		return fmt.Errorf("permissions: db resolver: %w", err)
	}

	if _, has := perms[action]; !has {
		return &PermissionDeniedError{Action: action, Resource: resource}
	}
	return nil
}

// resolvePermissions returns the full set of permission names held by the given
// roles. Results are cached in-process keyed by the sorted role combination.
func (c *DBChecker) resolvePermissions(ctx context.Context, roles []string) (map[string]struct{}, error) {
	key := roleSetKey(roles)

	if cached, ok := c.permCache.Load(key); ok {
		return cached.(map[string]struct{}), nil
	}

	// Cache miss — query the database.
	names, err := c.db.GetPermissionsForRoles(ctx, roles)
	if err != nil {
		return nil, err
	}

	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}

	// Store in cache. If another goroutine raced us and stored first, use the
	// already-stored value (LoadOrStore semantics) to keep the cache consistent.
	actual, _ := c.permCache.LoadOrStore(key, set)
	return actual.(map[string]struct{}), nil
}

// InvalidateCache clears the in-process permission cache. Call this in tests
// or after a live role/permission configuration change.
func (c *DBChecker) InvalidateCache() {
	c.permCache.Range(func(k, _ any) bool {
		c.permCache.Delete(k)
		return true
	})
}

// roleSetKey returns a stable string key for a set of role names so that
// {"admin","user"} and {"user","admin"} produce the same cache key.
func roleSetKey(roles []string) string {
	sorted := make([]string, len(roles))
	copy(sorted, roles)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
