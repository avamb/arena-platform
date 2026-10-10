package eventbot

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

func TestTeamMember_RevokableOnlyForAnOpenInvitation(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	cases := []struct {
		name string
		m    TeamMember
		want bool
	}{
		{"waiting", TeamMember{InvitationState: invStateWaiting, InvitationID: id}, true},
		{"expired", TeamMember{InvitationState: invStateExpired, InvitationID: id}, true},
		{"accepted", TeamMember{InvitationState: invStateAccepted, InvitationID: id}, false},
		{"none", TeamMember{}, false},
		{"state without id", TeamMember{InvitationState: invStateWaiting}, false},
	}
	for _, c := range cases {
		if got := c.m.revokable(); got != c.want {
			t.Errorf("%s: revokable = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every kept_reason the server can answer has a text in every language, and
// the removed and kept answers differ.
func TestRevokedText_EveryReasonIsSpoken(t *testing.T) {
	t.Parallel()
	bundle, err := i18n.NewBundle()
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{texts: NewTexts(bundle)}
	for _, loc := range SupportedLocales {
		removed := b.revokedText(loc, "a@b.test", RevokeResult{MembershipRemoved: true})
		if !strings.Contains(removed, "a@b.test") {
			t.Errorf("%s: removed text lacks the address: %q", loc, removed)
		}
		for reason := range keptReasons {
			got := b.revokedText(loc, "a@b.test", RevokeResult{KeptReason: reason})
			if got == removed || strings.Contains(got, "bot.team.") || strings.Contains(got, "<no value>") {
				t.Errorf("%s/%s: %q", loc, reason, got)
			}
		}
	}
}
