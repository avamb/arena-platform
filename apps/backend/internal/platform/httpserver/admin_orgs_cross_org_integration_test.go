//go:build integration

// admin_orgs_cross_org_integration_test.go — SEC-1 (org-isolation audit
// 2026-10-10, hole H2): the organization routes of the admin console
//
//	POST  /v1/admin/organizations
//	PATCH /v1/admin/organizations/{id}
//	GET   /v1/admin/organizations/{id}/sender-dns
//	POST  /v1/admin/organizations/{id}/archive
//
// are the platform superadmin's. They used to check only org.create /
// org.update / org.delete — held by every owner (org_admin) — plus a
// non-empty X-Admin-Reason, so the owner of one organization could rename,
// re-address or archive any other, create organizations without an approved
// application, and mark their own organization's KYB "verified" (the guard
// against that lives on PATCH /v1/organizations/{id} only).
//
// Shares the sec1Env helpers of memberships_cross_org_integration_test.go.
package httpserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func (e *sec1Env) orgState(t *testing.T, id uuid.UUID) (name, kyb string, archived bool) {
	t.Helper()
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT name, kyb_status, deleted_at IS NOT NULL FROM organizations WHERE id = $1`, id,
	).Scan(&name, &kyb, &archived); err != nil {
		t.Fatalf("read organization %s: %v", id, err)
	}
	return name, kyb, archived
}

func (e *sec1Env) orgBySlug(t *testing.T, slug string) (uuid.UUID, bool) {
	t.Helper()
	var id uuid.UUID
	err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT id FROM organizations WHERE slug = $1`, slug).Scan(&id)
	if err != nil {
		return uuid.Nil, false
	}
	e.orgs = append(e.orgs, id)
	return id, true
}

// (c) An owner (JWT whose only authority is an org_admin membership) and an
// organization API key with org.* scopes are refused on every admin
// organization route, foreign org or their own.
func TestSEC1_AdminOrgRoutesRefuseOwnersAndKeys(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	callers := map[string]string{
		"owner":   e.token(t, owner),
		"api key": e.key(t, orgB, "org.create", "org.update", "org.delete", "org.read"),
	}
	nameA, _, _ := e.orgState(t, orgA)
	_, kybB, _ := e.orgState(t, orgB)

	for label, tok := range callers {
		t.Run(label, func(t *testing.T) {
			pathA := "/v1/admin/organizations/" + orgA.String()
			e.expect(t, http.StatusForbidden, http.MethodPatch, pathA, tok, "sec1", `{"name":"SEC1 pwned `+uuid.NewString()[:6]+`"}`)
			e.expect(t, http.StatusForbidden, http.MethodPatch, pathA, tok, "sec1", `{"sender_email":"attacker@example.test"}`)
			e.expect(t, http.StatusForbidden, http.MethodGet, pathA+"/sender-dns", tok, "sec1", "")
			e.expect(t, http.StatusForbidden, http.MethodPost, pathA+"/archive", tok, "sec1", "")
			e.expect(t, http.StatusForbidden, http.MethodPatch, "/v1/admin/organizations/"+orgB.String(), tok, "sec1", `{"legal_name":"SEC1 Ltd","kyb_status":"verified"}`)
			slug := "sec1-rogue-" + uuid.NewString()[:8]
			e.expect(t, http.StatusForbidden, http.MethodPost, "/v1/admin/organizations", tok, "sec1", `{"name":"SEC1 rogue `+slug+`","slug":"`+slug+`","country":"EE"}`)
			if _, made := e.orgBySlug(t, slug); made {
				t.Errorf("%s created an organization through the admin route", label)
			}
		})
	}

	if name, _, archived := e.orgState(t, orgA); name != nameA || archived {
		t.Errorf("org A changed: name %q (was %q), archived %v", name, nameA, archived)
	}
	if _, kyb, _ := e.orgState(t, orgB); kyb != kybB {
		t.Errorf("org B kyb_status %q, was %q: the owner self-verified", kyb, kybB)
	}
}

// The real superadmin keeps every admin organization route.
func TestSEC1_SuperadminKeepsAdminOrgRoutes(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	_, tok := e.superadmin(t)
	pathA := "/v1/admin/organizations/" + orgA.String()

	e.expect(t, http.StatusBadRequest, http.MethodPatch, pathA, tok, "", `{"name":"no reason"}`)
	newName := "SEC1 renamed " + uuid.NewString()[:6]
	e.expect(t, http.StatusOK, http.MethodPatch, pathA, tok, "sec1", `{"name":"`+newName+`"}`)
	e.expect(t, http.StatusOK, http.MethodPatch, "/v1/admin/organizations/"+orgB.String(), tok, "sec1", `{"legal_name":"SEC1 Ltd","kyb_status":"verified"}`)
	slug := "sec1-made-" + uuid.NewString()[:8]
	e.expect(t, http.StatusCreated, http.MethodPost, "/v1/admin/organizations", tok, "sec1", `{"name":"SEC1 made `+slug+`","slug":"`+slug+`","country":"EE"}`)
	if _, made := e.orgBySlug(t, slug); !made {
		t.Error("superadmin create left no organization")
	}
	e.expect(t, http.StatusOK, http.MethodPost, pathA+"/archive", tok, "sec1", "")

	if name, _, archived := e.orgState(t, orgA); name != newName || !archived {
		t.Errorf("org A: name %q archived %v", name, archived)
	}
	if _, kyb, _ := e.orgState(t, orgB); kyb != "verified" {
		t.Errorf("org B kyb_status %q, want verified", kyb)
	}
}
