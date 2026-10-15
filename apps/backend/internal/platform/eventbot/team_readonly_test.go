package eventbot

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// V-11 (owner decision 2026-10-16): the manager reads the Team screen but gets
// no action button; the owner keeps every one of them.
func TestTeamMemberControls_ByRole(t *testing.T) {
	t.Parallel()
	member := TeamMember{
		UserID: uuid.New(), Email: "a@example.com", MembershipRole: "organizer",
		InvitationState: invStateWaiting, InvitationID: uuid.New(), InvitationChannel: "telegram",
	}
	accepted := TeamMember{UserID: uuid.New(), Email: "b@example.com", InvitationState: invStateAccepted}

	if got := teamMemberControls(member, false, true); len(got) != 0 {
		t.Errorf("a read-only viewer must get no controls, got %+v", got)
	}
	if got := teamMemberControls(member, true, false); len(got) != 0 {
		t.Errorf("nobody gets controls for themself, got %+v", got)
	}
	owner := teamMemberControls(member, false, false)
	if len(owner) != 3 || !strings.HasPrefix(owner[0].Data, "team:rs:") || !strings.HasPrefix(owner[1].Data, "team:ri:") || !strings.HasPrefix(owner[2].Data, "team:rm:") {
		t.Errorf("the owner must see resend, revoke and remove for a waiting invitation: %+v", owner)
	}
	if got := teamMemberControls(accepted, false, false); len(got) != 1 || !strings.HasPrefix(got[0].Data, "team:rm:") {
		t.Errorf("the owner must see only remove for an accepted member: %+v", got)
	}
}

func TestTeamFooterControls_ByRole(t *testing.T) {
	t.Parallel()
	got := teamFooterControls(true)
	if len(got) != 1 || got[0].Data != "home" {
		t.Errorf("read-only footer = %+v, want only the way back", got)
	}
	owner := teamFooterControls(false)
	if len(owner) != 2 || owner[0].Data != "team:invite" {
		t.Errorf("owner footer = %+v", owner)
	}
}

// The main menu has the Team row for the owner and the manager, and for no
// other role. The manager's row opens the screen ("team"), never the invite
// dialog.
func TestHomeKeyboard_TeamRowByRole(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	row := func(role string) string {
		id := &Identity{Current: &Membership{Role: role}}
		for _, r := range b.homeKeyboard(context.Background(), id, "").InlineKeyboard {
			for _, btn := range r {
				if btn.CallbackData == "team" || btn.CallbackData == "team:invite" {
					return btn.CallbackData
				}
			}
		}
		return ""
	}
	if got := row("organizer"); got != "team" {
		t.Errorf("manager Team row = %q, want %q", got, "team")
	}
	if got := row("org_admin"); got == "" {
		t.Error("the owner must keep the Team row")
	}
	if got := row("agent"); got != "" {
		t.Errorf("an agent must not see Team, got %q", got)
	}
}
