//go:build integration

// org_archived_guard_integration_test.go — an organization archived by a
// platform superadmin (POST /v1/admin/organizations/{id}/archive) is closed
// to its members and its own API keys on every org-scoped route
// (refuseArchivedOrganization), while the superadmin still reads it.
//
// Found on production 2026-10-25: the owner of an archived organization kept
// working in it through the Telegram bot, because the membership guards never
// looked at organizations.deleted_at.
//
// Run against a scratch database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestArchivedOrg_
package httpserver

import (
	"net/http"
	"testing"
)

func TestArchivedOrg_MembersAndKeysGetNotFoundSuperadminStillReads(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "Archived"), e.org(t, "Active")
	owner := e.user(t, "owner")
	e.member(t, owner, orgA, "org_admin")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)
	keyA := e.key(t, orgA, "event.read", "import.bil24_session")
	keyB := e.key(t, orgB, "event.read", "import.bil24_session")
	_, saTok := e.superadmin(t)

	base := func(org string) string { return "/v1/organizations/" + org }
	a, b := base(orgA.String()), base(orgB.String())
	// A dry run writes nothing; an empty bundle is refused by the import's own
	// validation, which is enough to tell "served" from "refused as archived".
	const importPath = "/imports/event-bundle?dry_run=true"
	const bundle = `{}`

	served := func(t *testing.T, method, path, bearer, reason, body string) {
		t.Helper()
		st, out := e.do(t, method, path, bearer, reason, body)
		if st == http.StatusNotFound && sec1Code(out) == "org.not_found" {
			t.Errorf("%s %s: refused as archived (%s), want it served", method, path, out)
		}
		if st >= http.StatusInternalServerError {
			t.Errorf("%s %s: %d %s", method, path, st, out)
		}
	}
	refused := func(t *testing.T, method, path, bearer, body string) {
		t.Helper()
		out := e.expect(t, http.StatusNotFound, method, path, bearer, "", body)
		if code := sec1Code(out); code != "org.not_found" {
			t.Errorf("%s %s: code %q, want org.not_found", method, path, code)
		}
	}

	// Before the archive both organizations are served.
	e.expect(t, http.StatusOK, http.MethodGet, a+"/events", tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/events", keyA, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/bot-team", tok, "", "")
	served(t, http.MethodPost, a+importPath, tok, "", bundle)
	served(t, http.MethodPost, a+importPath, keyA, "", bundle)

	// The operator archives A through the real admin route.
	e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/organizations/"+orgA.String()+"/archive", saTok, "archive-guard test", "")

	// The owner and A's own API key: 404 org.not_found, reads and writes alike.
	refused(t, http.MethodGet, a+"/events", tok, "")
	refused(t, http.MethodGet, a+"/events", keyA, "")
	refused(t, http.MethodGet, a+"/bot-team", tok, "")
	refused(t, http.MethodPost, a+importPath, tok, bundle)
	refused(t, http.MethodPost, a+importPath, keyA, bundle)

	// The organization they still own next to it is untouched.
	e.expect(t, http.StatusOK, http.MethodGet, b+"/events", tok, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, b+"/events", keyB, "", "")
	e.expect(t, http.StatusOK, http.MethodGet, b+"/bot-team", tok, "", "")
	served(t, http.MethodPost, b+importPath, keyB, "", bundle)

	// Someone who was never a member keeps the gate's own 403: the guard
	// reveals nothing about another organization.
	stranger := e.user(t, "stranger")
	e.member(t, stranger, orgB, "org_admin")
	e.expect(t, http.StatusForbidden, http.MethodGet, a+"/events", e.token(t, stranger), "", "")

	// The platform superadmin still reads the archived organization, with the
	// X-Admin-Reason the membership guards demand as before.
	e.expect(t, http.StatusOK, http.MethodGet, a+"/events", saTok, "archive-guard test", "")
	e.expect(t, http.StatusOK, http.MethodGet, a+"/bot-team", saTok, "archive-guard test", "")
	served(t, http.MethodPost, a+importPath, saTok, "archive-guard test", bundle)
	e.expect(t, http.StatusBadRequest, http.MethodGet, a+"/events", saTok, "", "")
}
