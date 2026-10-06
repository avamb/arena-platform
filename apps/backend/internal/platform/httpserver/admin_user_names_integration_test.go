//go:build integration

// admin_user_names_integration_test.go — the optional first and last name of a
// user (migration 0123): an invited member can be created with a name, the
// admin members list shows e-mail and name next to the membership (and accepts
// the org_admin owner role), PATCH /v1/admin/users/{user_id} sets, keeps and
// clears each name, and a name over 100 characters is refused without a write.
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestAdminUserNamesIntegration
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func adminNamesCall(t *testing.T, h http.HandlerFunc, method, path, body string, params map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Admin-Reason", "integration: user names")
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	h(w, r)
	return w
}

type adminNamesMember struct {
	ID        string  `json:"id"`
	UserID    string  `json:"user_id"`
	Role      string  `json:"role"`
	Email     string  `json:"email"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
}

func TestAdminUserNamesIntegration_MembersShowEmailAndName(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildEmailIntegrationServer(t, pool, onboardingTestPublicURL)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	named := "names-vera-" + suffix + "@arena-integration.test"
	bare := "names-bare-" + suffix + "@arena-integration.test"
	orgID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Names Org "+suffix, "names-org-"+suffix); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	t.Cleanup(func() {
		sweepOnboardingRows(pool, bare, nil)
		sweepOnboardingRows(pool, named, &orgID)
	})
	orgParams := map[string]string{"org_id": orgID.String()}
	membersPath := "/v1/admin/organizations/" + orgID.String() + "/members"

	// A new e-mail with a name, as the OWNER role.
	w := adminNamesCall(t, srv.handleAdminAddMember, http.MethodPost, membersPath,
		fmt.Sprintf(`{"email":%q,"role":"org_admin","first_name":"  Vera ","last_name":"Petrova"}`, named), orgParams)
	if w.Code != http.StatusCreated {
		t.Fatalf("add named owner: %d %s", w.Code, w.Body.String())
	}
	// A new e-mail without any name: both stay NULL.
	w = adminNamesCall(t, srv.handleAdminAddMember, http.MethodPost, membersPath,
		fmt.Sprintf(`{"email":%q,"role":"organizer"}`, bare), orgParams)
	if w.Code != http.StatusCreated {
		t.Fatalf("add bare manager: %d %s", w.Code, w.Body.String())
	}
	// A name over 100 characters is refused, and the user it would have named
	// is not left behind (the whole request rolls back).
	tooLong := "names-long-" + suffix + "@arena-integration.test"
	t.Cleanup(func() { sweepOnboardingRows(pool, tooLong, nil) })
	w = adminNamesCall(t, srv.handleAdminAddMember, http.MethodPost, membersPath,
		fmt.Sprintf(`{"email":%q,"role":"organizer","first_name":%q}`, tooLong, strings.Repeat("x", 101)), orgParams)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("over-long name: %d %s, want 422", w.Code, w.Body.String())
	}
	var leftover int
	_ = pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE email = $1`, tooLong).Scan(&leftover)
	if leftover != 0 {
		t.Fatalf("the refused request left %d user rows behind", leftover)
	}

	list := func() map[string]adminNamesMember {
		t.Helper()
		w := adminNamesCall(t, srv.handleAdminListMembers, http.MethodGet, membersPath, "", orgParams)
		if w.Code != http.StatusOK {
			t.Fatalf("list members: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Memberships []adminNamesMember `json:"memberships"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		byMail := map[string]adminNamesMember{}
		for _, m := range out.Memberships {
			byMail[m.Email] = m
		}
		return byMail
	}
	got := list()
	v, ok := got[named]
	if !ok || v.Role != "org_admin" || v.FirstName == nil || *v.FirstName != "Vera" || v.LastName == nil || *v.LastName != "Petrova" {
		t.Fatalf("named owner row = %+v (found %v)", v, ok)
	}
	b, ok := got[bare]
	if !ok || b.Role != "organizer" || b.FirstName != nil || b.LastName != nil {
		t.Fatalf("bare manager row = %+v (found %v)", b, ok)
	}

	// The role dropdown's missing value: a manager becomes an owner.
	w = adminNamesCall(t, srv.handleAdminChangeMemberRole, http.MethodPatch, membersPath+"/"+b.ID,
		`{"role":"org_admin"}`, map[string]string{"org_id": orgID.String(), "membership_id": b.ID})
	if w.Code != http.StatusOK {
		t.Fatalf("change role to org_admin: %d %s", w.Code, w.Body.String())
	}

	// PATCH the user: set the first name, keep the last (absent key).
	userPath := "/v1/admin/users/" + b.UserID
	userParams := map[string]string{"user_id": b.UserID}
	w = adminNamesCall(t, srv.handleAdminUpdateUser, http.MethodPatch, userPath, `{"first_name":"Anna"}`, userParams)
	if w.Code != http.StatusOK {
		t.Fatalf("patch first name: %d %s", w.Code, w.Body.String())
	}
	w = adminNamesCall(t, srv.handleAdminUpdateUser, http.MethodPatch, userPath, `{"last_name":"Ivanova"}`, userParams)
	if w.Code != http.StatusOK {
		t.Fatalf("patch last name: %d %s", w.Code, w.Body.String())
	}
	b = list()[bare]
	if b.Role != "org_admin" || b.FirstName == nil || *b.FirstName != "Anna" || b.LastName == nil || *b.LastName != "Ivanova" {
		t.Fatalf("after patches: %+v", b)
	}
	// null clears one name and leaves the other.
	w = adminNamesCall(t, srv.handleAdminUpdateUser, http.MethodPatch, userPath, `{"first_name":null}`, userParams)
	if w.Code != http.StatusOK {
		t.Fatalf("clear first name: %d %s", w.Code, w.Body.String())
	}
	b = list()[bare]
	if b.FirstName != nil || b.LastName == nil || *b.LastName != "Ivanova" {
		t.Fatalf("after clearing the first name: %+v", b)
	}

	// Refusals: an empty body, no name key, an over-long name, an unknown user.
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"no name key", `{}`, http.StatusBadRequest},
		{"too long", fmt.Sprintf(`{"last_name":%q}`, strings.Repeat("y", 101)), http.StatusUnprocessableEntity},
	} {
		if w := adminNamesCall(t, srv.handleAdminUpdateUser, http.MethodPatch, userPath, tc.body, userParams); w.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.want)
		}
	}
	missing := uuid.NewString()
	if w := adminNamesCall(t, srv.handleAdminUpdateUser, http.MethodPatch, "/v1/admin/users/"+missing, `{"first_name":"Z"}`,
		map[string]string{"user_id": missing}); w.Code != http.StatusNotFound {
		t.Errorf("unknown user: %d %s, want 404", w.Code, w.Body.String())
	}
	// The failed requests changed nothing.
	if b = list()[bare]; b.LastName == nil || *b.LastName != "Ivanova" {
		t.Fatalf("a refused patch changed the name: %+v", b)
	}
}
