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

// #395: a superadmin may cross an organization membership boundary only after
// both the role and the real superadmin.read permission check have succeeded.
func TestAB12_SuperadminOrgAccessRequiresRolePermissionReasonAndAudit(t *testing.T) {
	aw := &captureAuditWriter{}
	s := New(Options{
		Config:      &config.Config{RequestTimeout: time.Second, BodyLimitBytes: 1024},
		Audit:       aw,
		Permissions: permissions.AllowAll(),
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+uuid.NewString()+"/bank-accounts", nil)
	request.Header.Set("X-Admin-Reason", "bootstrap a new organization")
	request = request.WithContext(auth.WithActor(request.Context(), auth.Actor{
		ID: uuid.NewString(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"},
	}))
	seenByHandler := false
	s.markSuperadminOrgAccess(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenByHandler = auth.HasSuperadminOrgAccess(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), request)
	if !seenByHandler {
		t.Fatal("platform_superadmin with superadmin.read must receive org-access marker")
	}
	if len(aw.getEvents()) != 1 || aw.getEvents()[0].Metadata["reason"] != "bootstrap a new organization" {
		t.Fatalf("expected one reason-carrying audit event, got %#v", aw.getEvents())
	}

	// The downstream membership guard is responsible for making the reason
	// mandatory even after the permission-derived marker is present.
	missingReason := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithSuperadminOrgAccess(context.Background()))
	if s.enforceMembershipInOrg(httptest.NewRecorder(), missingReason, uuid.New()) {
		t.Fatal("missing X-Admin-Reason must not bypass organization membership")
	}

	nonAdmin := httptest.NewRequest(http.MethodGet, "/v1/organizations/"+uuid.NewString(), nil)
	nonAdmin = nonAdmin.WithContext(auth.WithActor(nonAdmin.Context(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser}))
	seenByHandler = true
	s.markSuperadminOrgAccess(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenByHandler = auth.HasSuperadminOrgAccess(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), nonAdmin)
	if seenByHandler {
		t.Fatal("a non-superadmin must never receive the membership-bypass marker")
	}
}
