//go:build integration

package httpserver

// org_isolation_flat_sweep_integration_test.go — SEC-3: routes WITHOUT an
// {org_id} in the path that load a row by its UUID (a ticket, a checkout
// session, ...). The organization comes from the row, so the handler must
// compare it with the caller's organizations and answer the route's own 404
// for a row of another organization.
//
// Same attacker and world as the SEC-2 sweep (org_isolation_sweep_integration_test.go):
// organization B's owner JWT and manager JWT (empty roles claim, the
// membership row is the only authority) and an organization-B API key call
// each route with organization A's ids, and A's rows must not move. The
// control run makes the same calls on B's own world.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// flatIDs names the rows a flat-route request is built from.
type flatIDs struct {
	Ticket   uuid.UUID // the active ticket of the world's paid order
	Checkout uuid.UUID // that ticket's checkout session
}

type flatSweepRow struct {
	name  string
	route string // "METHOD /v1/..." as the router registers it
	perm  string
	path  func(id flatIDs) string
	body  func(id flatIDs) string
	// prepare runs on the world before the call (both runs).
	prepare func(t *testing.T, e *sweepEnv, id flatIDs)
	// controlStatus is the status the control run must answer; zero means any 2xx.
	controlStatus int
	// admin rows need an X-Admin-Reason header.
	admin bool
}

func flatSweepRows() []flatSweepRow {
	withDelivery := func(t *testing.T, e *sweepEnv, id flatIDs) {
		e.exec(t, `INSERT INTO delivery_jobs (ticket_id, recipient_email) VALUES ($1, 'sweep-holder@example.test') ON CONFLICT (ticket_id) DO NOTHING`, id.Ticket)
	}
	return []flatSweepRow{
		{name: "checkout tickets", route: "GET /v1/checkout/{id}/tickets", perm: "ticket.read",
			path: func(id flatIDs) string { return "/v1/checkout/" + id.Checkout.String() + "/tickets" }},
		{name: "ticket credential", route: "GET /v1/tickets/{id}/credential", perm: "credential.read",
			path: func(id flatIDs) string { return "/v1/tickets/" + id.Ticket.String() + "/credential" }},
		{name: "admin ticket delivery", route: "GET /v1/admin/tickets/{id}/delivery", perm: "ticket.update", admin: true,
			path:    func(id flatIDs) string { return "/v1/admin/tickets/" + id.Ticket.String() + "/delivery" },
			prepare: withDelivery},
		{name: "admin ticket delivery resend", route: "POST /v1/admin/tickets/{id}/delivery/resend", perm: "ticket.update", admin: true,
			path:          func(id flatIDs) string { return "/v1/admin/tickets/" + id.Ticket.String() + "/delivery/resend" },
			prepare:       withDelivery,
			controlStatus: http.StatusAccepted},
		{name: "admin ticket scans", route: "GET /v1/admin/tickets/{id}/scans", perm: "scan_event.read", admin: true,
			path: func(id flatIDs) string { return "/v1/admin/tickets/" + id.Ticket.String() + "/scans" }},
	}
}

func (e *sweepEnv) flatIDs(t *testing.T, w *sweepWorld) flatIDs {
	t.Helper()
	var id flatIDs
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id, checkout_session_id FROM tickets WHERE order_id = $1`, w.orderID).Scan(&id.Ticket, &id.Checkout); err != nil {
		t.Fatalf("world ticket: %v", err)
	}
	// Registered after newWorld's cleanup, so it runs first: a credential
	// the control run generated must go before its ticket.
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM ticket_credentials WHERE ticket_id = $1`, id.Ticket)
	})
	return id
}

// flatSnapshot adds the rows a flat ticket route could create to the SEC-2
// snapshot: a lazily generated credential.
func (e *sweepEnv) flatSnapshot(t *testing.T, w *sweepWorld, id flatIDs) map[string]string {
	t.Helper()
	out := e.snapshot(t, w)
	var n string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*)::text FROM ticket_credentials WHERE ticket_id = $1`, id.Ticket).Scan(&n); err != nil {
		t.Fatalf("snapshot credentials: %v", err)
	}
	out["ticket credentials"] = n
	return out
}

func (e *sweepEnv) doFlat(t *testing.T, row flatSweepRow, id flatIDs, token string) (int, map[string]any) {
	t.Helper()
	reason := ""
	if row.admin {
		reason = "org isolation sweep"
	}
	return e.doFlatReason(t, row, id, token, reason)
}

// doFlatReason is doFlat with an explicit X-Admin-Reason ("" sends none).
func (e *sweepEnv) doFlatReason(t *testing.T, row flatSweepRow, id flatIDs, token, reason string) (int, map[string]any) {
	t.Helper()
	body := ""
	if row.body != nil {
		body = row.body(id)
	}
	method := strings.SplitN(row.route, " ", 2)[0]
	req, err := http.NewRequest(method, e.ts.URL+row.path(id), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s: %v", row.route, err)
	}
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

func TestOrgIsolationFlatSweep_ForeignRowsAreNotFoundAndNothingMoves(t *testing.T) {
	e := newSweepEnv(t)
	for _, row := range flatSweepRows() {
		for _, caller := range e.callers() {
			row, caller := row, caller
			t.Run(row.name+"/"+caller.name, func(t *testing.T) {
				orgA := e.newOrg(t, "A")
				w := e.newWorld(t, orgA, e.orgC)
				id := e.flatIDs(t, w)
				if row.prepare != nil {
					row.prepare(t, e, id)
				}
				before := e.flatSnapshot(t, w, id)

				status, out := e.doFlat(t, row, id, caller.token)

				want := http.StatusNotFound
				if caller.lacks(row.perm) {
					want = http.StatusForbidden
				}
				if status != want {
					t.Errorf("%s as %s: status %d, want %d (%v)", row.name, caller.name, status, want, out)
				}
				if diffs := diffSnapshots(before, e.flatSnapshot(t, w, id)); len(diffs) > 0 {
					t.Errorf("%s as %s moved organization A's rows:\n  %s", row.name, caller.name, strings.Join(diffs, "\n  "))
				}
			})
		}
	}
}

// The same calls on organization B's OWN rows keep working.
func TestOrgIsolationFlatSweep_OwnOrganizationStillWorks(t *testing.T) {
	e := newSweepEnv(t)
	for _, row := range flatSweepRows() {
		row := row
		t.Run(row.name, func(t *testing.T) {
			w := e.newWorld(t, e.orgB, e.orgB)
			id := e.flatIDs(t, w)
			if row.prepare != nil {
				row.prepare(t, e, id)
			}
			status, out := e.doFlat(t, row, id, e.owner)
			if row.controlStatus != 0 {
				if status != row.controlStatus {
					t.Errorf("own %s: status %d, want %d (%v)", row.name, status, row.controlStatus, out)
				}
				return
			}
			if status < 200 || status > 299 {
				t.Errorf("own %s: status %d, want 2xx (%v)", row.name, status, out)
			}
		})
	}
}

// ─── a cancelled session keeps its tickets reachable ────────────────────────

// userIn creates a user holding role in org and returns a JWT whose only
// authority is the membership row.
func (e *sweepEnv) userIn(t *testing.T, org uuid.UUID, role string) string {
	t.Helper()
	ctx := context.Background()
	u, err := e.q.InsertUser(ctx, "sweep-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := e.q.InsertMembership(ctx, u.ID, org, role); err != nil {
		t.Fatalf("InsertMembership(%s): %v", role, err)
	}
	return e.jwtFor(t, u.ID)
}

func (e *sweepEnv) jwtFor(t *testing.T, user uuid.UUID) string {
	t.Helper()
	tok, _, err := auth.IssueJWT(e.secret, user, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

// platformSuperadmin creates a user holding the global platform_superadmin role.
func (e *sweepEnv) platformSuperadmin(t *testing.T) string {
	t.Helper()
	u, err := e.q.InsertUser(context.Background(), "sweep-super-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	e.exec(t, `INSERT INTO user_roles (user_id, role_id, org_id)
	           SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, u.ID)
	t.Cleanup(func() {
		_, _ = e.pool.Exec(context.Background(), `DELETE FROM user_roles WHERE user_id = $1`, u.ID)
	})
	return e.jwtFor(t, u.ID)
}

// The bot's session cancel soft-deletes the session and leaves its tickets
// active. Support must still see the delivery status and the scans, resend the
// ticket, read its credential and cancel it; a foreign organization still
// gets the route's own 404.
func TestOrgIsolationFlatSweep_TicketOfCancelledSessionStaysReachable(t *testing.T) {
	e := newSweepEnv(t)
	super := e.platformSuperadmin(t)
	const reason = "support: cancelled session"
	for _, row := range flatSweepRows() {
		if row.name == "checkout tickets" {
			continue // keyed by the checkout session, not by the ticket's session
		}
		row := row
		for _, who := range []string{"own-owner", "superadmin", "foreign-owner"} {
			who := who
			t.Run(row.name+"/"+who, func(t *testing.T) {
				orgA := e.newOrg(t, "A")
				w := e.newWorld(t, orgA, e.orgC)
				id := e.flatIDs(t, w)
				if row.prepare != nil {
					row.prepare(t, e, id)
				}
				e.exec(t, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, w.s4)

				token, send, want := "", "", http.StatusOK
				switch who {
				case "own-owner":
					token = e.userIn(t, orgA, "org_admin")
				case "superadmin":
					token, send = super, reason
				default:
					token, want = e.owner, http.StatusNotFound
				}
				if row.controlStatus != 0 && want == http.StatusOK {
					want = row.controlStatus
				}
				if row.admin {
					send = reason // the support-console routes demand the header from every caller
				}
				status, out := e.doFlatReason(t, row, id, token, send)
				if status != want {
					t.Errorf("%s as %s on a cancelled session: status %d, want %d (%v)", row.name, who, status, want, out)
				}
			})
		}
	}
}

// Creating a credential is a write: a platform superadmin needs the reason for
// it, but reading one that already exists stays a plain read.
func TestOrgIsolationFlatSweep_CredentialCreateByPlatformSuperadminNeedsReason(t *testing.T) {
	e := newSweepEnv(t)
	super := e.platformSuperadmin(t)
	orgA := e.newOrg(t, "A")
	w := e.newWorld(t, orgA, e.orgC)
	id := e.flatIDs(t, w)
	row := flatSweepRows()[1]
	if row.name != "ticket credential" {
		t.Fatalf("row 1 is %q, want the credential row", row.name)
	}
	count := func() string { return e.flatSnapshot(t, w, id)["ticket credentials"] }

	if status, out := e.doFlatReason(t, row, id, super, ""); status != http.StatusBadRequest || count() != "0" {
		t.Fatalf("create without reason: status %d credentials %s, want 400 and 0 (%v)", status, count(), out)
	}
	if status, out := e.doFlatReason(t, row, id, super, "support: first look"); status != http.StatusOK || count() != "1" {
		t.Fatalf("create with reason: status %d credentials %s, want 200 and 1 (%v)", status, count(), out)
	}
	if status, out := e.doFlatReason(t, row, id, super, ""); status != http.StatusOK {
		t.Fatalf("read of an existing credential without reason: status %d, want 200 (%v)", status, out)
	}
}

// POST /v1/tickets/{id}/cancel finds the ticket's organization the same way.
// The world's ticket carries no sold-place bookkeeping, so the cancellation
// itself may answer an error of its own once the guard let the caller in:
// what is asserted is that the guard neither refuses the owner and the
// superadmin (404/403) nor lets the foreign organization through.
func TestOrgIsolationFlatSweep_CancelShimFindsTicketOfCancelledSession(t *testing.T) {
	e := newSweepEnv(t)
	super := e.platformSuperadmin(t)
	body := `{"reason":"sweep","refund_mode":"none"}`
	for _, who := range []string{"own-owner", "superadmin", "superadmin-no-reason", "foreign-owner"} {
		who := who
		t.Run(who, func(t *testing.T) {
			orgA := e.newOrg(t, "A")
			w := e.newWorld(t, orgA, e.orgC)
			id := e.flatIDs(t, w)
			e.exec(t, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, w.s4)

			token, reason := e.owner, ""
			switch who {
			case "own-owner":
				token = e.userIn(t, orgA, "org_admin")
			case "superadmin":
				token, reason = super, "support: cancelled session"
			case "superadmin-no-reason":
				token = super
			}
			row := flatSweepRow{route: "POST /v1/tickets/{id}/cancel",
				path: func(id flatIDs) string { return "/v1/tickets/" + id.Ticket.String() + "/cancel" },
				body: func(flatIDs) string { return body }}
			status, out := e.doFlatReason(t, row, id, token, reason)
			errObj, _ := out["error"].(map[string]any)
			code, _ := errObj["code"].(string)
			switch who {
			case "foreign-owner":
				if status != http.StatusNotFound || code != "ticket.not_found" {
					t.Errorf("foreign owner: status %d code %q, want the route's own 404 (%v)", status, code, out)
				}
			case "superadmin-no-reason":
				if status != http.StatusBadRequest {
					t.Errorf("superadmin without reason: status %d, want 400 (%v)", status, out)
				}
			default:
				if status == http.StatusNotFound || status == http.StatusForbidden || status == http.StatusBadRequest {
					t.Errorf("%s: status %d code %q, want the guard to let the caller in (%v)", who, status, code, out)
				}
			}
		})
	}
}
