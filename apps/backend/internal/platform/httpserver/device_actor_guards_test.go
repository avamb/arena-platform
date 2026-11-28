// device_actor_guards_test.go — a door-scanner device (spec 69, MA-10) is
// refused by every server-level legacy org guard, its own organization
// included, and never gets the superadmin bypass. membershipQueries is a
// *gen.Queries with no database: a device that fell through to the
// membership lookup would panic instead of quietly being refused.
package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

func newDeviceGuardServer() *Server {
	return &Server{
		logger:            slog.New(slog.NewTextHandler(discardWriter{}, nil)),
		membershipQueries: gen.New(nil),
		perms:             permissions.AllowAll(),
	}
}

func deviceOwnOrgCtx(org uuid.UUID) context.Context {
	return auth.WithActor(context.Background(), auth.NewDeviceActor(uuid.NewString(), org.String()))
}

func worstDeviceCtx(org uuid.UUID) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{
		ID:          uuid.NewString(),
		Type:        auth.ActorTypeDevice,
		Roles:       []string{"platform_superadmin"},
		Permissions: []string{"scan.read", "superadmin.read"},
		OrgID:       org.String(),
	})
}

func deviceErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return env.Error.Code
}

func TestRowOrgAccess_DeviceOwnOrgIsRoutes404(t *testing.T) {
	s := newDeviceGuardServer()
	org := uuid.New()
	for _, write := range []bool{false, true} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/refunds/x", nil).WithContext(deviceOwnOrgCtx(org))
		if s.rowOrgAccess(w, r, org, write, "refund.not_found", "refund not found") {
			t.Fatalf("write=%v: a device passed rowOrgAccess for its own organization", write)
		}
		if w.Code != http.StatusNotFound || deviceErrCode(t, w.Body.Bytes()) != "refund.not_found" {
			t.Fatalf("write=%v: code=%d body=%s, want the route's 404", write, w.Code, w.Body.String())
		}
	}
}

func TestEnforceMembershipInOrg_DeviceOwnOrgIs403(t *testing.T) {
	s := newDeviceGuardServer()
	org := uuid.New()
	for name, ctx := range map[string]context.Context{"device": deviceOwnOrgCtx(org), "worst device": worstDeviceCtx(org)} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/organizations/x", nil).WithContext(ctx)
		if s.enforceMembershipInOrg(w, r, org) {
			t.Fatalf("%s passed enforceMembershipInOrg for its own organization", name)
		}
		if w.Code != http.StatusForbidden || deviceErrCode(t, w.Body.Bytes()) != "org.access_denied" {
			t.Fatalf("%s: code=%d body=%s, want 403 org.access_denied", name, w.Code, w.Body.String())
		}
	}
}

func TestCallerActsFor_DeviceNever(t *testing.T) {
	s := newDeviceGuardServer()
	org := uuid.New()
	for _, target := range []uuid.UUID{org, uuid.New()} {
		r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(worstDeviceCtx(org))
		acts, err := s.callerActsFor(r, target)
		if err != nil || acts {
			t.Fatalf("callerActsFor = (%v, %v), want (false, nil)", acts, err)
		}
	}
}

// TestSuperadminOrgBypass_FallbackRefusesDevice covers the fallback branch
// (a checker without SuperadminOrgBypass): AllowAll would answer yes to
// superadmin.read, and the mis-built device carries platform_superadmin.
func TestSuperadminOrgBypass_FallbackRefusesDevice(t *testing.T) {
	s := newDeviceGuardServer()
	org := uuid.New()
	actor, _ := auth.ActorFromContext(worstDeviceCtx(org))
	if s.superadminOrgBypass(worstDeviceCtx(org), actor) {
		t.Fatal("a device actor must never get the superadmin bypass")
	}
	// Control: the same wiring grants it to a user carrying the role.
	user := auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"}}
	if !s.superadminOrgBypass(auth.WithActor(context.Background(), user), user) {
		t.Fatal("control: the fallback must grant the bypass to a user with the role")
	}
}
