//go:build integration

// permission_outside_tx_integration_test.go — the per-organization permission
// check of the seating-plan update and new-version routes must run BEFORE the
// handler opens its transaction. The checker reads roles through the pool; a
// check made while the handler holds a transaction needs a second pool
// connection, and under a full pool that waits forever (AGENTS.md: never take
// a pool connection inside an open transaction).
package hseating

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// connCountingChecker allows every check and records how many pool
// connections the request already held when the check ran.
type connCountingChecker struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	held []int32
}

func (c *connCountingChecker) Check(context.Context, string, string) error { return nil }

func (c *connCountingChecker) CheckInOrg(context.Context, string, uuid.UUID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = append(c.held, c.pool.Stat().AcquiredConns())
	return nil
}

func TestSeatingPlanMutations_PermissionCheckedOutsideTransaction(t *testing.T) {
	pool := ga47Pool(t)
	ctx := context.Background()
	f := newGA47Fixture(t, ctx, pool, ga47Geometry())
	defer f.cleanup()
	defer func() {
		// The new-version case adds a version the fixture does not know.
		bg := context.Background()
		_, _ = pool.Exec(bg, `UPDATE seating_plans SET current_version_id = NULL WHERE id = $1`, f.planID)
		_, _ = pool.Exec(bg, `DELETE FROM seating_plan_versions WHERE seating_plan_id = $1 AND id <> $2`, f.planID, f.planVerID)
	}()

	userID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		userID, "seat-tx-"+userID.String()[:8]+"@example.test"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) }()
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'organizer')`,
		userID, f.orgID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	h := ga47Handler(pool)
	geo, err := json.Marshal(ga47Geometry())
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		body   string
		handle http.HandlerFunc
	}{
		{"update", `{"name":"Renamed"}`, h.HandleUpdateSeatingPlan},
		{"new version", `{"geometry":` + string(geo) + `}`, h.HandleCreateSeatingPlanVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &connCountingChecker{pool: pool}
			rc := chi.NewRouteContext()
			rc.URLParams.Add("id", f.planID.String())
			rctx := context.WithValue(context.Background(), chi.RouteCtxKey, rc)
			rctx = auth.WithActor(rctx, auth.Actor{ID: userID.String()})
			rctx = permissions.WithRequiredPermission(rctx, "seating_plan.update")
			rctx = permissions.WithOrgChecker(rctx, checker)

			req := httptest.NewRequest(http.MethodPatch, "/", bytes.NewBufferString(tc.body)).WithContext(rctx)
			rec := httptest.NewRecorder()
			tc.handle(rec, req)
			if rec.Code >= 300 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if len(checker.held) == 0 {
				t.Fatal("the per-organization check never ran")
			}
			for _, n := range checker.held {
				if n != 0 {
					t.Fatalf("permission checked while the handler held %d pool connection(s)", n)
				}
			}
		})
	}
}
