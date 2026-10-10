package eventbot

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The Orders button is on the main menu of a role that may read orders, and
// on no other menu.
func TestHomeKeyboard_OrdersButtonOnlyForRolesThatReadOrders(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	has := func(role string, super bool) bool {
		id := &Identity{Current: &Membership{Role: role}, Superadmin: super}
		for _, row := range b.homeKeyboard(context.Background(), id, "").InlineKeyboard {
			for _, btn := range row {
				if btn.CallbackData == "or:new" {
					return true
				}
			}
		}
		return false
	}
	if !has("organizer", false) || !has("agent", true) {
		t.Error("a manager and the operator must see Orders")
	}
	if has("agent", false) || has("", false) {
		t.Error("an agent must not see Orders")
	}
}

// The event card's row carries Orders beside Sessions for a role that may
// read orders, and Sessions alone for another.
func TestSessionsOrdersRow(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	event := uuid.New()
	manager := &Identity{Current: &Membership{Role: "organizer"}}
	agent := &Identity{Current: &Membership{Role: "agent"}}
	row := b.sessionsOrdersRow("en", manager, event)
	if len(row) != 2 || row[0].CallbackData != "ses:list:"+event.String() || row[1].CallbackData != "or:e:"+event.String() {
		t.Errorf("manager row = %+v", row)
	}
	if row := b.sessionsOrdersRow("en", agent, event); len(row) != 1 {
		t.Errorf("agent row = %+v", row)
	}
}
