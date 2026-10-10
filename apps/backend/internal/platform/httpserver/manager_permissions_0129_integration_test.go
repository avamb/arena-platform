//go:build integration

// manager_permissions_0129_integration_test.go — EC-17 (spec 35 §3): a
// manager (membership role organizer, the role the Telegram bot's "manager"
// invitation creates) reaches every operational gate the event center needs:
// orders, tickets, refunds, promo codes, complimentary tickets, scans,
// reports and category edits. Migration 0129 grants them; before it a
// manager answered 403 on all of them (the way 0122 found session.update
// missing in the first live run).
//
// The proof is the permission gate, not the handlers: every request goes
// through the REAL router with a JWT whose only authority is the membership
// row (an empty roles claim, exactly what the bot mints), and a route that
// lets the manager past the gate answers its own 400/404 for a bogus body or
// id — never 403. An agent membership is the control: it must still be
// refused where it was before.
//
// Run against a fresh migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

func TestManagerPermissions0129_OrganizerPassesEveryEventCenterGate(t *testing.T) {
	// The fixture's own server signs with the same secret; a second server
	// is only the cheapest way to learn it.
	_, secret := productionIntegrationServer(t)
	f := newProm0113Fixture(t)
	ctx := context.Background()
	org := f.org(t, "Mgr")

	member := func(role string) string {
		t.Helper()
		user, err := f.q.InsertUser(ctx, "mgr0129-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := f.q.InsertMembership(ctx, user.ID, org, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		// Only the membership carries the authority — no roles claim.
		tok, _, err := auth.IssueJWT(secret, user.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	manager, agent := member("organizer"), member("agent")

	random := uuid.NewString()
	orgPath := "/v1/organizations/" + org.String()
	gates := []struct {
		perm   string
		method string
		path   string
		body   string
	}{
		{"order.write", http.MethodPost, orgPath + "/orders/" + random + "/cancel", `{"reason":"test"}`},
		{"ticket.cancel", http.MethodPost, "/v1/tickets/" + random + "/cancel", `{"reason":"test","refund_mode":"none"}`},
		{"ticket.update", http.MethodGet, "/v1/admin/tickets/" + random + "/delivery", ""},
		{"refund.read", http.MethodGet, "/v1/refunds/" + random, ""},
		{"refund.create", http.MethodPost, "/v1/refunds", `{"payment_intent_id":"` + random + `","amount":1,"currency":"EUR"}`},
		{"refund.approve", http.MethodPost, "/v1/refunds/" + random + "/approve", "{}"},
		{"promo.read", http.MethodGet, orgPath + "/promo-codes", ""},
		{"promo.create", http.MethodPost, orgPath + "/promo-codes", `{}`},
		{"promo.update", http.MethodPatch, orgPath + "/promo-codes/" + random, `{"is_active":false}`},
		{"promo.delete", http.MethodDelete, orgPath + "/promo-codes/" + random, ""},
		{"complimentary.read", http.MethodGet, orgPath + "/complimentary", ""},
		{"complimentary.issue", http.MethodPost, orgPath + "/complimentary", `{}`},
		{"scan_event.read", http.MethodGet, "/v1/admin/tickets/" + random + "/scans", ""},
		{"report.read", http.MethodGet, "/v1/events/" + random + "/report", ""},
		{"report.generate", http.MethodPost, "/v1/events/" + random + "/report", ""},
		{"tier.update", http.MethodPatch, orgPath + "/events/" + random + "/sessions/" + random + "/tiers/" + random, `{"name":"x"}`},
		// EC-10: the event card's status buttons (granted since 0014/0074, driven here
		// so the first live run under the manager does not find a 403).
		{"event.publish", http.MethodPost, orgPath + "/events/" + random + "/status", `{"status":"published"}`},
		{"event.delete", http.MethodDelete, orgPath + "/events/" + random, ""},
		{"event.delete", http.MethodGet, orgPath + "/events/" + random + "/delete-impact", ""},
	}

	for _, g := range gates {
		st, out := f.do(t, g.method, g.path, manager, g.body)
		if st == http.StatusForbidden || st == http.StatusUnauthorized {
			t.Errorf("manager %s %s (%s): refused with %d %v — migration 0129 must grant it", g.method, g.path, g.perm, st, out)
			continue
		}
		if st >= http.StatusInternalServerError {
			t.Errorf("manager %s %s (%s): %d %v", g.method, g.path, g.perm, st, out)
		}
	}

	// The owner holds every one of them too (0129 said so; order.write was
	// the one it did not, until 0131).
	owner := member("org_admin")
	for _, g := range gates {
		st, out := f.do(t, g.method, g.path, owner, g.body)
		if st == http.StatusForbidden || st == http.StatusUnauthorized {
			t.Errorf("owner %s %s (%s): refused with %d %v", g.method, g.path, g.perm, st, out)
		}
	}

	// The control: an agent still has none of the manager's new authority
	// (agents sell; refunds and promo codes are not theirs).
	for _, g := range gates {
		if g.perm == "scan_event.read" || g.perm == "refund.read" {
			continue // seeded for agents long before this migration (0028, 0055)
		}
		st, out := f.do(t, g.method, g.path, agent, g.body)
		if st != http.StatusForbidden {
			t.Errorf("agent %s %s (%s): %d %v, want 403", g.method, g.path, g.perm, st, out)
		}
	}

	// The grant is exactly the list the spec names — a static look at the
	// live role_permissions so a later migration cannot silently widen it to
	// the owner-only permissions.
	rows, err := f.q.DB().Query(ctx, `
		SELECT p.name FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id AND r.name = 'organizer' AND r.org_id IS NULL
		JOIN permissions p ON p.id = rp.permission_id
		WHERE p.name LIKE 'membership.%' OR p.name LIKE 'payment_config.%'
		   OR p.name LIKE 'billing.%' OR p.name = 'api_key.manage' OR p.name = 'org.update'`)
	if err != nil {
		t.Fatalf("read organizer grants: %v", err)
	}
	defer rows.Close()
	var ownerOnly []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "membership.read" {
			continue // 0011: a member may see who else is in the organization
		}
		ownerOnly = append(ownerOnly, name)
	}
	if len(ownerOnly) > 0 {
		t.Fatalf("organizer holds owner-only permissions: %s", strings.Join(ownerOnly, ", "))
	}
}
