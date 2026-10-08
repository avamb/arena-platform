//go:build integration

// org_kyb_self_verify_integration_test.go — APP-01 / ONB-01: an organization
// owner must not be able to mark their own organization verified. kyb_status
// is the gate for live payments (hpayments.requireKYBForLive and
// hcheckout.SelectProviderConfig), and PATCH /v1/organizations/{id} used to
// accept any value of it from any member with org.update.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	  ./apps/backend/internal/platform/httpserver/ -run TestOrgKYB_
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOrgKYB_OwnerCannotSelfVerify(t *testing.T) {
	srv, _ := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	client := ts.Client()
	ctx := context.Background()

	orgID := uuid.New()
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO organizations (id, name, slug, country, legal_name) VALUES ($1, $2, $3, 'EE', 'KYB Test OU')`,
		orgID, "KYB Org "+orgID.String(), "kyb-org-"+orgID.String(),
	); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	ownerEmail := fmt.Sprintf("kyb-owner-%d@example.com", time.Now().UnixNano())
	const password = "Test1234!"
	registerUser(t, client, ts.URL, ownerEmail, password)
	var ownerID uuid.UUID
	if err := srv.pgxPool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, ownerEmail).Scan(&ownerID); err != nil {
		t.Fatalf("look up owner: %v", err)
	}
	if _, err := srv.pgxPool.Exec(ctx,
		`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'org_admin')`, ownerID, orgID,
	); err != nil {
		t.Fatalf("grant owner membership: %v", err)
	}
	token := loginUser(t, client, ts.URL, ownerEmail, password)
	orgURL := ts.URL + "/v1/organizations/" + orgID.String()

	kyb := func() string {
		var s string
		if err := srv.pgxPool.QueryRow(ctx, `SELECT kyb_status FROM organizations WHERE id = $1`, orgID).Scan(&s); err != nil {
			t.Fatalf("read kyb_status: %v", err)
		}
		return s
	}
	patch := func(status string) (int, string) {
		resp := integDoRequest(t, client, http.MethodPatch, orgURL, token, fmt.Sprintf(`{"kyb_status":%q}`, status))
		return resp.StatusCode, integReadBody(t, resp)
	}

	// The hole: the owner asks for "verified" and must be refused.
	code, body := patch("verified")
	if code != http.StatusForbidden || !strings.Contains(body, "org.kyb_status_superadmin_only") {
		t.Fatalf("owner set verified: status %d body %s (want 403 org.kyb_status_superadmin_only)", code, body)
	}
	if got := kyb(); got != "unverified" {
		t.Fatalf("kyb_status = %q after a refused request, want unverified", got)
	}
	// Nor may the owner reject themselves or write any other transition.
	if code, _ := patch("rejected"); code != http.StatusForbidden {
		t.Fatalf("owner set rejected: status %d, want 403", code)
	}

	// Asking for a review (unverified -> pending) is the owner's own move.
	if code, body := patch("pending"); code != http.StatusOK {
		t.Fatalf("owner requests review: status %d body %s", code, body)
	}
	if got := kyb(); got != "pending" {
		t.Fatalf("kyb_status = %q, want pending", got)
	}
	// Repeating the stored value is harmless.
	if code, body := patch("pending"); code != http.StatusOK {
		t.Fatalf("owner repeats pending: status %d body %s", code, body)
	}

	// A PATCH that does not mention kyb_status leaves it alone and still works.
	resp := integDoRequest(t, client, http.MethodPatch, orgURL, token, `{"contact_email":"kyb-owner@example.com"}`)
	if body := integReadBody(t, resp); resp.StatusCode != http.StatusOK {
		t.Fatalf("owner edits contact e-mail: status %d body %s", resp.StatusCode, body)
	}
	if got := kyb(); got != "pending" {
		t.Fatalf("kyb_status = %q after an unrelated edit, want pending", got)
	}
	var verifiedAt *time.Time
	_ = srv.pgxPool.QueryRow(ctx, `SELECT kyb_verified_at FROM organizations WHERE id = $1`, orgID).Scan(&verifiedAt)
	if verifiedAt != nil {
		t.Fatalf("kyb_verified_at = %v, want NULL", verifiedAt)
	}
}
