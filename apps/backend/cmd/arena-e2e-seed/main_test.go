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

// The mini app twin asks the bot route for a code in the name of each person's Telegram account: the accounts must
// be real (positive) and differ, or two people would share one link row.
func TestPeople_TelegramAccountsAreDistinct(t *testing.T) {
	seen := map[int64]string{}
	for _, p := range people() {
		if p.TelegramUserID <= 0 {
			t.Errorf("%s: telegram_user_id %d is not positive", p.Role, p.TelegramUserID)
		}
		if prev, dup := seen[p.TelegramUserID]; dup {
			t.Errorf("%s and %s share telegram_user_id %d", p.Role, prev, p.TelegramUserID)
		}
		seen[p.TelegramUserID] = p.Role
	}
}
