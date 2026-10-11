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

	"github.com/google/uuid"
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
	if row.admin {
		req.Header.Set("X-Admin-Reason", "org isolation sweep")
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
