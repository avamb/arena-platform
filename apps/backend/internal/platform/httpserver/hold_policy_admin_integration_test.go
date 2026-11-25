//go:build integration

// hold_policy_admin_integration_test.go - the superadmin screen "Seat hold"
// (spec 67 section 7.2): /v1/admin/hold-policy and the event hold profile.
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/holdpolicy"
)

func holdJSON(sel, pay int) string {
	b, _ := json.Marshal(map[string]int{
		"select_seconds": sel, "payment_seconds": pay, "extend_seconds": 240, "extend_count": 2,
		"paying_seconds": 500, "max_seats_per_hold": 8, "max_active_per_device": 3, "max_active_per_ip": 7,
		"rehold_pause_seconds": 200, "poll_interval_ms": 2500,
	})
	return string(b)
}

func TestHoldPolicyAdmin_AccessEditAuditAndEventOverride(t *testing.T) {
	e := newSec1Env(t)
	ctx := context.Background()
	superID, super := e.superadmin(t)
	org := e.org(t, "HP")
	ownerID := e.user(t, "hpowner")
	e.member(t, ownerID, org, "org_admin")
	owner := e.token(t, ownerID)
	apiKey := e.key(t, org, "org.read", "event.read")

	q := gen.New(e.srv.pgxPool)
	orig, err := q.GetHoldPolicyProfile(ctx, holdpolicy.ProfileStandard)
	if err != nil {
		t.Fatalf("read standard: %v", err)
	}

	// Fixture: an event with one session.
	nonce := uuid.NewString()[:8]
	venueID, eventID, sessID := uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.srv.pgxPool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, org, "HP Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, eventID, org, "HP Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'ILS', 'override')`,
		sessID, eventID, venueID)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = q.UpdateHoldPolicyProfile(c, gen.UpdateHoldPolicyProfileParams{
			Name: orig.Name, SelectSeconds: orig.SelectSeconds, PaymentSeconds: orig.PaymentSeconds,
			ExtendSeconds: orig.ExtendSeconds, ExtendCount: orig.ExtendCount, PayingSeconds: orig.PayingSeconds,
			MaxSeatsPerHold: orig.MaxSeatsPerHold, MaxActivePerDevice: orig.MaxActivePerDevice,
			MaxActivePerIP: orig.MaxActivePerIP, ReholdPauseSeconds: orig.ReholdPauseSeconds,
			PollIntervalMs: orig.PollIntervalMs, UpdatedBy: orig.UpdatedBy,
		})
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM audit_events WHERE resource_id = ANY($1)`, []string{"standard", eventID.String()})
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM sessions WHERE id = $1`, sessID)
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM events WHERE id = $1`, eventID)
		_, _ = e.srv.pgxPool.Exec(c, `DELETE FROM venues WHERE id = $1`, venueID)
	})

	putStd := "/v1/admin/hold-policy/standard"
	evPath := "/v1/admin/events/" + eventID.String() + "/hold-profile"

	// Not a superadmin: 403 everywhere, nothing changes. No token: 401.
	for _, tok := range []string{owner, apiKey} {
		e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/admin/hold-policy", tok, "hp", "")
		e.expect(t, http.StatusForbidden, http.MethodPut, putStd, tok, "hp", holdJSON(300, 600))
		e.expect(t, http.StatusForbidden, http.MethodPut, evPath, tok, "hp", `{"profile":"high_demand"}`)
	}
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/v1/admin/hold-policy", "", "", "")

	// Superadmin reads both profiles.
	out := e.expect(t, http.StatusOK, http.MethodGet, "/v1/admin/hold-policy", super, "", "")
	var list struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Profiles) != 2 {
		t.Fatalf("list = %s (%v), want 2 profiles", out, err)
	}

	// No reason: 400, nothing changes.
	e.expect(t, http.StatusBadRequest, http.MethodPut, putStd, super, "", holdJSON(300, 600))
	e.expect(t, http.StatusBadRequest, http.MethodPut, evPath, super, "", `{"profile":"high_demand"}`)

	// Invalid values and shapes: 422 (and 404 for an unknown profile).
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPut, putStd, super, "hp", holdJSON(60, 600))   // select < 120
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPut, putStd, super, "hp", holdJSON(300, 9000)) // payment > 1800
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPut, putStd, super, "hp", `{"select_seconds":300}`)
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPut, evPath, super, "hp", `{"profile":"turbo"}`)
	e.expect(t, http.StatusUnprocessableEntity, http.MethodPut, evPath, super, "hp", `{}`)
	e.expect(t, http.StatusNotFound, http.MethodPut, "/v1/admin/hold-policy/turbo", super, "hp", holdJSON(300, 600))
	e.expect(t, http.StatusNotFound, http.MethodPut, "/v1/admin/events/"+uuid.NewString()+"/hold-profile", super, "hp", `{"profile":null}`)
	if got, _ := q.GetHoldPolicyProfile(ctx, holdpolicy.ProfileStandard); got.SelectSeconds != orig.SelectSeconds {
		t.Fatalf("a refused request changed the profile: %+v", got)
	}

	// A valid edit: stored, audited with before and after and the reason.
	e.expect(t, http.StatusOK, http.MethodPut, putStd, super, "tune the window", holdJSON(300, 700))
	got, err := q.GetHoldPolicyProfile(ctx, holdpolicy.ProfileStandard)
	if err != nil || got.SelectSeconds != 300 || got.PaymentSeconds != 700 || got.ExtendCount != 2 ||
		got.PollIntervalMs != 2500 || got.UpdatedBy == nil || *got.UpdatedBy != superID {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	var actor, reason string
	var before, after int
	if err := e.srv.pgxPool.QueryRow(ctx, `
SELECT COALESCE(actor_id::text, ''), COALESCE(metadata->>'reason', ''),
       (metadata->'before'->>'select_seconds')::int, (metadata->'after'->>'select_seconds')::int
FROM audit_events WHERE action = 'v1.admin.hold_policy.update' AND resource_id = 'standard'
ORDER BY occurred_at DESC LIMIT 1`).Scan(&actor, &reason, &before, &after); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if actor != superID.String() || reason != "tune the window" || before != int(orig.SelectSeconds) || after != 300 {
		t.Errorf("audit actor=%q reason=%q before=%d after=%d", actor, reason, before, after)
	}

	// The event override is read back by ForSession.
	p, err := holdpolicy.ForSession(ctx, q, sessID)
	if err != nil || p.Profile != holdpolicy.ProfileStandard || p.Select.Seconds() != 300 {
		t.Fatalf("ForSession before override = %+v, %v", p, err)
	}
	e.expect(t, http.StatusOK, http.MethodPut, evPath, super, "busy sale", `{"profile":"high_demand"}`)
	p, err = holdpolicy.ForSession(ctx, q, sessID)
	if err != nil || p.Profile != holdpolicy.ProfileHighDemand {
		t.Fatalf("ForSession with override = %+v, %v", p, err)
	}
	var n int
	if err := e.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'v1.admin.event.hold_profile.set'
	  AND resource_id = $1 AND metadata->>'reason' = 'busy sale' AND metadata->>'after' = 'high_demand'`, eventID.String()).Scan(&n); err != nil || n != 1 {
		t.Errorf("event audit rows = %d, %v", n, err)
	}
	e.expect(t, http.StatusOK, http.MethodPut, evPath, super, "back to normal", `{"profile":null}`)
	p, err = holdpolicy.ForSession(ctx, q, sessID)
	if err != nil || p.Profile != holdpolicy.ProfileStandard {
		t.Fatalf("ForSession after reset = %+v, %v", p, err)
	}
}
