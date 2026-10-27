//go:build integration

package httpserver

// A venue the platform operator verified is open to every organization
// (migration 0172): it is listed in the other organization's shared list, dates
// can be held there, and it cannot be deleted under them. The owner stays its
// only editor, and only a platform superadmin verifies.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestVenueSharing_VerifiedVenueIsOpenToOtherOrganizations(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	srv := buildBotIntegrationServer(t, pool)
	q := gen.New(pool)

	a := newBotInviteFixture(t, pool)
	b := newBotInviteFixture(t, pool)
	admin, err := q.InsertUser(ctx, "venue-admin-"+uuid.NewString()[:8]+"@arena-integration.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	venueA, event, sessionB := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM audit_events WHERE resource_type = 'venue' AND resource_id = $1`, venueA.String())
		_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, event)
		_, _ = pool.Exec(bg, `DELETE FROM events WHERE id = $1`, event)
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, venueA)
		_, _ = pool.Exec(bg, `DELETE FROM user_roles WHERE user_id = $1`, admin.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, admin.ID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, admin.ID); err != nil {
		t.Fatalf("grant superadmin: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone, country, contact_email, status)
		VALUES ($1, $2, 'Shared Hall', 'Europe/Prague', 'CZ', 'secret@owner.test', 'active')`, venueA, a.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events (id, org_id, name, status, visibility)
		VALUES ($1, $2, 'Guest Show', 'draft', 'public')`, event, b.orgID); err != nil {
		t.Fatal(err)
	}

	token := func(user uuid.UUID) string {
		tok, _, err := auth.IssueJWT(botTestJWTSecret, user, nil, nil, botTestJWTIssuer, botTestJWTAudience, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	call := func(user uuid.UUID, reason, method, path, body string) (int, []byte) {
		return consentBearer(t, srv, token(user), reason, method, path, body)
	}
	sharedIDs := func(org uuid.UUID, user uuid.UUID) []string {
		code, raw := call(user, "", http.MethodGet, "/v1/organizations/"+org.String()+"/shared-venues", "")
		if code != http.StatusOK {
			t.Fatalf("shared list: %d %s", code, raw)
		}
		var out struct {
			Venues []struct {
				ID           string  `json:"id"`
				ContactEmail *string `json:"contact_email"`
			} `json:"venues"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, v := range out.Venues {
			if v.ContactEmail != nil {
				t.Errorf("a foreign venue leaks its contact e-mail: %s", raw)
			}
			ids = append(ids, v.ID)
		}
		return ids
	}
	contains := func(ids []string, id uuid.UUID) bool {
		for _, x := range ids {
			if x == id.String() {
				return true
			}
		}
		return false
	}
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour)
	sessionBody := `{"venue_id":"` + venueA.String() + `","start_at":"` + start.Format(time.RFC3339) +
		`","end_at":"` + start.Add(2*time.Hour).Format(time.RFC3339) + `","capacity_override":50,"currency":"EUR"}`
	createSession := func() (int, []byte) {
		return call(b.ownerID, "", http.MethodPost, "/v1/organizations/"+b.orgID.String()+"/events/"+event.String()+"/sessions", sessionBody)
	}
	review := func(user uuid.UUID, status string) (int, []byte) {
		return call(user, "approving the hall", http.MethodPatch, "/v1/admin/venues/"+venueA.String()+"/review", `{"review_status":"`+status+`"}`)
	}

	// Not verified: invisible to B, B cannot hold a date there.
	if contains(sharedIDs(b.orgID, b.ownerID), venueA) {
		t.Fatal("an unverified venue is listed for another organization")
	}
	if code, raw := createSession(); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "session.venue_org_mismatch") {
		t.Fatalf("session on an unverified foreign venue: %d %s", code, raw)
	}

	// Only a platform superadmin verifies.
	if code, raw := review(a.ownerID, "verified"); code != http.StatusForbidden {
		t.Fatalf("owner verifies own venue: %d %s", code, raw)
	}
	if code, raw := call(admin.ID, "", http.MethodPatch, "/v1/admin/venues/"+venueA.String()+"/review", `{"review_status":"verified"}`); code != http.StatusBadRequest {
		t.Fatalf("no X-Admin-Reason: %d %s", code, raw)
	}
	if code, raw := review(admin.ID, "maybe"); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status: %d %s", code, raw)
	}
	if code, raw := review(admin.ID, "verified"); code != http.StatusOK || !strings.Contains(string(raw), `"verified"`) {
		t.Fatalf("verify: %d %s", code, raw)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.venue.review' AND resource_id = $1`, venueA.String()).Scan(&audits); err != nil || audits < 1 {
		t.Fatalf("audit rows = %d (%v)", audits, err)
	}

	// Verified: listed for B (not for the owner), B holds a date there.
	if !contains(sharedIDs(b.orgID, b.ownerID), venueA) {
		t.Fatal("a verified venue is not listed for another organization")
	}
	if contains(sharedIDs(a.orgID, a.ownerID), venueA) {
		t.Fatal("the owner sees its own venue in the shared list")
	}
	code, raw := createSession()
	if code != http.StatusCreated {
		t.Fatalf("session on a verified foreign venue: %d %s", code, raw)
	}
	var created struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	_ = json.Unmarshal(raw, &created)
	if id, perr := uuid.Parse(created.Session.ID); perr == nil {
		sessionB = id
	}
	// The listing can be narrowed.
	if code, raw := call(b.ownerID, "", http.MethodGet, "/v1/organizations/"+b.orgID.String()+"/shared-venues?country=DE", ""); code != http.StatusOK || strings.Contains(string(raw), venueA.String()) {
		t.Fatalf("country filter: %d %s", code, raw)
	}
	if code, _ := call(b.ownerID, "", http.MethodGet, "/v1/organizations/"+b.orgID.String()+"/shared-venues?limit=0", ""); code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", code)
	}
	// B cannot read A's shared list through A's path, and cannot edit A's venue.
	if code, _ := call(b.ownerID, "", http.MethodGet, "/v1/organizations/"+a.orgID.String()+"/shared-venues", ""); code != http.StatusForbidden {
		t.Fatalf("another organization's path: %d", code)
	}
	if code, _ := call(b.ownerID, "", http.MethodPatch, "/v1/organizations/"+b.orgID.String()+"/venues/"+venueA.String(), `{"name":"Mine now"}`); code != http.StatusNotFound {
		t.Fatalf("foreign venue edit through own path: %d", code)
	}

	// A cannot delete a venue another organization holds dates at.
	if code, raw := call(a.ownerID, "", http.MethodDelete, "/v1/organizations/"+a.orgID.String()+"/venues/"+venueA.String(), ""); code != http.StatusConflict || !strings.Contains(string(raw), "venue.in_use_by_other_org") {
		t.Fatalf("delete under another organization's dates: %d %s", code, raw)
	}

	// Taking the verification back stops NEW dates only.
	if code, raw := review(admin.ID, "unverified"); code != http.StatusOK {
		t.Fatalf("unverify: %d %s", code, raw)
	}
	if contains(sharedIDs(b.orgID, b.ownerID), venueA) {
		t.Fatal("an unverified venue is still listed")
	}
	if code, _ := createSession(); code != http.StatusUnprocessableEntity {
		t.Fatalf("new date after the verification was taken back: %d", code)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id = $1 AND deleted_at IS NULL`, sessionB).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the existing date must stay: n=%d err=%v", n, err)
	}
}
