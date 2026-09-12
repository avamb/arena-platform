// superadmin_org_bypass_531_test.go — feature #531 (W1-S0a): the superadmin
// org-access bypass in markSuperadminOrgAccess must resolve roles server-side
// via the same membership source permissions.DBChecker uses, not from the
// JWT roles claim. Real login/refresh-issued JWTs carry no roles claim
// (hauth/login.go issues tokens with roles=nil), so an actor with empty
// actor.Roles but platform_superadmin in the database must still receive the
// bypass marker. See 08_architecture/21_superadmin_org_access_parity_ru.md §3.1.
package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// TestSuperadminOrgBypass531_ResolvesRolesFromDBWhenJWTClaimEmpty is the core
// regression test: a JWT with no roles claim (the real shape login/refresh
// issue) must still receive the cross-tenant marker when the user's DB-side
// memberships resolve to platform_superadmin.
func TestSuperadminOrgBypass531_ResolvesRolesFromDBWhenJWTClaimEmpty(t *testing.T) {
	t.Parallel()

	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"platform_superadmin": {"superadmin.read"},
		},
	}
	actorID := uuid.New()
	memberships := &fakeMembershipQuerier{
		rolesByUser: map[string][]string{
			actorID.String(): {"platform_superadmin"},
		},
	}
	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(memberships)

	aw := &captureAuditWriter{}
	s := New(Options{
		Config:      &config.Config{RequestTimeout: time.Second, BodyLimitBytes: 1024},
		Audit:       aw,
		Permissions: checker,
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/channels", nil)
	request.Header.Set("X-Admin-Reason", "verify #531 db-resolved bypass")
	// Roles deliberately empty: this is the shape a real login-issued JWT has.
	request = request.WithContext(auth.WithActor(request.Context(), auth.Actor{
		ID: actorID.String(), Type: auth.ActorTypeUser, Roles: nil,
	}))

	sawMarker := false
	s.markSuperadminOrgAccess(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawMarker = auth.HasSuperadminOrgAccess(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), request)

	if !sawMarker {
		t.Fatal("actor with empty JWT roles but platform_superadmin in DB memberships must receive the org-access bypass marker")
	}
	if len(aw.getEvents()) != 1 || aw.getEvents()[0].Metadata["reason"] != "verify #531 db-resolved bypass" {
		t.Fatalf("expected one reason-carrying audit event, got %#v", aw.getEvents())
	}
}

// TestSuperadminOrgBypass531_PlainUserWithEmptyRolesStaysDenied ensures the
// DB-resolution path does not accidentally grant the bypass to a user who
// merely lacks a roles claim but has no platform_superadmin membership role
// either.
func TestSuperadminOrgBypass531_PlainUserWithEmptyRolesStaysDenied(t *testing.T) {
	t.Parallel()

	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"organizer": {"org.read"},
		},
	}
	actorID := uuid.New()
	memberships := &fakeMembershipQuerier{
		rolesByUser: map[string][]string{
			actorID.String(): {"organizer"},
		},
	}
	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(memberships)

	s := New(Options{
		Config:      &config.Config{RequestTimeout: time.Second, BodyLimitBytes: 1024},
		Permissions: checker,
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/channels", nil)
	request.Header.Set("X-Admin-Reason", "should never be granted")
	request = request.WithContext(auth.WithActor(request.Context(), auth.Actor{
		ID: actorID.String(), Type: auth.ActorTypeUser, Roles: nil,
	}))

	sawMarker := true
	s.markSuperadminOrgAccess(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawMarker = auth.HasSuperadminOrgAccess(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), request)

	if sawMarker {
		t.Fatal("a non-superadmin membership role must never receive the org-access bypass marker")
	}
}

// TestSuperadminOrgBypass531_ServiceActorNeverBypasses guards the explicit
// carve-out: API-key service actors have no roles and no memberships row, so
// SuperadminOrgBypass must short-circuit to false rather than attempting a
// membership lookup keyed by a non-user actor ID.
func TestSuperadminOrgBypass531_ServiceActorNeverBypasses(t *testing.T) {
	t.Parallel()

	rbac := &fakeRBACQuerier120{
		permsByRole: map[string][]string{
			"platform_superadmin": {"superadmin.read"},
		},
	}
	memberships := &fakeMembershipQuerier{rolesByUser: map[string][]string{}}
	checker := permissions.NewDBChecker(rbac).WithMembershipQuerier(memberships)

	if checker.SuperadminOrgBypass(context.Background(), auth.Actor{
		ID: uuid.NewString(), Type: auth.ActorTypeService, Permissions: []string{"superadmin.read"},
	}) {
		t.Fatal("a service actor must never receive the superadmin org-access bypass")
	}
}

// TestSuperadminOrgBypass531_FallsBackToJWTClaimWhenCheckerDoesNotImplementInterface
// pins the fallback behaviour for Checkers that don't implement
// permissions.SuperadminBypassChecker (AllowAllChecker/DenyAllChecker), used
// by simpler test wiring elsewhere in the suite: the legacy JWT-claim-only
// check must still work unchanged.
func TestSuperadminOrgBypass531_FallsBackToJWTClaimWhenCheckerDoesNotImplementInterface(t *testing.T) {
	t.Parallel()

	s := New(Options{
		Config:      &config.Config{RequestTimeout: time.Second, BodyLimitBytes: 1024},
		Permissions: permissions.AllowAll(),
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/channels", nil)
	request.Header.Set("X-Admin-Reason", "legacy path")
	request = request.WithContext(auth.WithActor(request.Context(), auth.Actor{
		ID: uuid.NewString(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"},
	}))

	sawMarker := false
	s.markSuperadminOrgAccess(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawMarker = auth.HasSuperadminOrgAccess(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), request)

	if !sawMarker {
		t.Fatal("JWT-claim role platform_superadmin must still grant the bypass via AllowAllChecker's fallback path")
	}
}
