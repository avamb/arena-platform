package main

import (
	"testing"

	"github.com/google/uuid"
)

// The harness and the event center's three-role rule depend on exactly
// these three membership roles being present, with parseable ids.
func TestPeople_ThreeRolesWithValidIDs(t *testing.T) {
	want := map[string]bool{"org_admin": false, "organizer": false, "agent": false}
	for _, p := range people() {
		if _, ok := want[p.Role]; !ok {
			t.Fatalf("unexpected role %q", p.Role)
		}
		want[p.Role] = true
		for _, id := range []string{p.UserID, p.MembershipID} {
			if _, err := uuid.Parse(id); err != nil {
				t.Errorf("%s: bad id %q: %v", p.Role, id, err)
			}
		}
	}
	for role, seen := range want {
		if !seen {
			t.Errorf("role %q missing", role)
		}
	}
}
