package permissions

import (
	"context"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// countingRBACQuerier counts DB hits so the TTL behaviour is observable.
type countingRBACQuerier struct {
	perms map[string][]string
	calls int
}

func (c *countingRBACQuerier) GetPermissionsForRoles(_ context.Context, roleNames []string) ([]string, error) {
	c.calls++
	seen := make(map[string]struct{})
	var out []string
	for _, r := range roleNames {
		for _, p := range c.perms[r] {
			if _, dup := seen[p]; !dup {
				seen[p] = struct{}{}
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func ctxWithRole(role string) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{
		ID:    "00000000-0000-0000-0000-000000000001",
		Type:  auth.ActorTypeUser,
		Roles: []string{role},
	})
}

// TestAB2_PermCacheExpiresAfterTTL verifies that a cached role→permission set
// is re-read from the database once permCacheTTL has elapsed, so live grants
// converge without a process restart (backlog AB-2).
func TestAB2_PermCacheExpiresAfterTTL(t *testing.T) {
	q := &countingRBACQuerier{perms: map[string][]string{"platform_superadmin": {"superadmin.read"}}}
	c := NewDBChecker(q)

	current := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return current }

	ctx := ctxWithRole("platform_superadmin")

	if err := c.Check(ctx, "superadmin.read", "test"); err != nil {
		t.Fatalf("first check: unexpected error: %v", err)
	}
	if err := c.Check(ctx, "superadmin.read", "test"); err != nil {
		t.Fatalf("second check: unexpected error: %v", err)
	}
	if q.calls != 1 {
		t.Fatalf("expected 1 DB call while cached, got %d", q.calls)
	}

	// Simulate an operator granting a new permission, then advance past TTL.
	q.perms["platform_superadmin"] = append(q.perms["platform_superadmin"], "org.create")
	current = current.Add(permCacheTTL + time.Second)

	if err := c.Check(ctx, "org.create", "test"); err != nil {
		t.Fatalf("post-TTL check: grant not visible after cache expiry: %v", err)
	}
	if q.calls != 2 {
		t.Fatalf("expected 2 DB calls after TTL expiry, got %d", q.calls)
	}
}

// TestAB2_PermCacheServedWithinTTL verifies the hot path still avoids DB reads
// inside the TTL window.
func TestAB2_PermCacheServedWithinTTL(t *testing.T) {
	q := &countingRBACQuerier{perms: map[string][]string{"admin": {"org.read"}}}
	c := NewDBChecker(q)

	current := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return current }

	ctx := ctxWithRole("admin")
	for i := 0; i < 5; i++ {
		current = current.Add(10 * time.Second) // stays under permCacheTTL between resolutions
		if err := c.Check(ctx, "org.read", "test"); err != nil {
			t.Fatalf("check %d: unexpected error: %v", i, err)
		}
	}
	if q.calls != 1 {
		t.Fatalf("expected 1 DB call within TTL, got %d", q.calls)
	}
}
