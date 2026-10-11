//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// The venue catalog is shared for reading, the contacts of a venue are not.
func TestOrgIsolationCatalog_VenueContactsStayWithTheirOrganization(t *testing.T) {
	e := newSec1Env(t)
	orgA, orgB := e.org(t, "A"), e.org(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, orgB, "org_admin")
	tok := e.token(t, owner)
	_, superTok := e.superadmin(t)

	venueA, venueB := uuid.New(), uuid.New()
	for _, v := range []struct {
		id  uuid.UUID
		org uuid.UUID
	}{{venueA, orgA}, {venueB, orgB}} {
		if _, err := e.srv.pgxPool.Exec(context.Background(),
			`INSERT INTO venues (id, org_id, name, timezone, contact_phone, contact_email)
			 VALUES ($1, $2, $3, 'Europe/Prague', '+420111222333', 'hall@example.test')`,
			v.id, v.org, "Catalog Hall "+v.id.String()[:8]); err != nil {
			t.Fatalf("venue fixture: %v", err)
		}
		id := v.id
		t.Cleanup(func() { _, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM venues WHERE id = $1`, id) })
	}

	contacts := func(token string, id uuid.UUID) (phone, mail any, found bool) {
		status, body := e.do(t, "GET", "/v1/venues", token, "", "")
		if status != 200 {
			t.Fatalf("list venues: %d %s", status, body)
		}
		var out struct {
			Venues []map[string]any `json:"venues"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, v := range out.Venues {
			if v["id"] == id.String() {
				return v["contact_phone"], v["contact_email"], true
			}
		}
		return nil, nil, false
	}

	if p, m, ok := contacts(tok, venueA); !ok || p != nil || m != nil {
		t.Errorf("foreign venue in the list: found=%v phone=%v mail=%v, want listed without contacts", ok, p, m)
	}
	if p, m, ok := contacts(tok, venueB); !ok || p == nil || m == nil {
		t.Errorf("own venue in the list: found=%v phone=%v mail=%v, want contacts kept", ok, p, m)
	}
	if p, m, ok := contacts(superTok, venueA); !ok || p == nil || m == nil {
		t.Errorf("superadmin: found=%v phone=%v mail=%v, want contacts kept", ok, p, m)
	}

	_, body := e.do(t, "GET", "/v1/venues/"+venueA.String(), tok, "", "")
	var one struct {
		Venue map[string]any `json:"venue"`
	}
	if err := json.Unmarshal([]byte(body), &one); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if one.Venue["name"] == nil || one.Venue["contact_phone"] != nil || one.Venue["contact_email"] != nil {
		t.Errorf("foreign venue by id: %v, want the venue without contacts", one.Venue)
	}
}

// A session may only be bound to a plan version its organization can see.
func TestOrgIsolationCatalog_SeatingBindNeedsAVisiblePlan(t *testing.T) {
	e := newSweepEnv(t)
	orgA := e.newOrg(t, "A")
	wA := e.newWorld(t, orgA, e.orgC)
	wB := e.newWorld(t, e.orgB, e.orgB)

	var versionA uuid.UUID
	if err := e.pool.QueryRow(context.Background(),
		`SELECT id FROM seating_plan_versions WHERE seating_plan_id = $1`, wA.planID).Scan(&versionA); err != nil {
		t.Fatalf("version of A's plan: %v", err)
	}
	path := fmt.Sprintf("/v1/organizations/%s/events/%s/sessions/%s/seating", e.orgB, wB.event, wB.s1)
	body := fmt.Sprintf(`{"seating_plan_version_id":%q,"admission_mode":"general_admission","auto_create_tiers":true}`, versionA)
	bind := func() (int, string) {
		status, out := e.do(t, "POST", path, e.owner, body)
		code := ""
		if m, ok := out["error"].(map[string]any); ok {
			code, _ = m["code"].(string)
		}
		return status, code
	}

	for _, vis := range []string{"private", "shared_read"} {
		e.exec(t, `UPDATE seating_plans SET visibility = $2 WHERE id = $1`, wA.planID, vis)
		if status, code := bind(); status != 400 || code != "seating.version_not_found" {
			t.Errorf("%s plan of another organization: got %d %s, want 400 seating.version_not_found", vis, status, code)
		}
	}
	for _, vis := range []string{"public_template", "operator_verified"} {
		e.exec(t, `UPDATE seating_plans SET visibility = $2 WHERE id = $1`, wA.planID, vis)
		if _, code := bind(); code == "seating.version_not_found" {
			t.Errorf("%s plan of another organization was refused as unknown", vis)
		}
	}
}
