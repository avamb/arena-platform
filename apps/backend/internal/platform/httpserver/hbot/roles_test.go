package hbot

import "testing"

func TestParseRole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"owner", RoleOwner, true},
		{" Manager ", RoleManager, true},
		{"OWNER", RoleOwner, true},
		{"org_admin", "", false},
		{"", "", false},
		{"admin", "", false},
	}
	for _, c := range cases {
		got, ok := ParseRole(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseRole(%q) = (%q, %v); want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestRoleMappingRoundTrip(t *testing.T) {
	t.Parallel()
	if MembershipRoleFor(RoleOwner) != "org_admin" {
		t.Errorf("owner must be created as org_admin, got %q", MembershipRoleFor(RoleOwner))
	}
	if MembershipRoleFor(RoleManager) != "organizer" {
		t.Errorf("manager must be created as organizer, got %q", MembershipRoleFor(RoleManager))
	}
	for _, role := range []string{RoleOwner, RoleManager} {
		if BotRoleFor(MembershipRoleFor(role)) != role {
			t.Errorf("round trip of %q broke: %q", role, BotRoleFor(MembershipRoleFor(role)))
		}
	}
	// An existing membership with any other role reads back as manager —
	// the bot never widens what a person already holds.
	for _, existing := range []string{"agent", "platform_operator", "network_operator"} {
		if BotRoleFor(existing) != RoleManager {
			t.Errorf("BotRoleFor(%q) = %q; want manager", existing, BotRoleFor(existing))
		}
	}
}

func TestNormalizeLocale(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"": "en", "en": "en", "EN": "en", "ru": "ru", "ru-RU": "ru", "ru_RU": "ru",
		"es": "es", "ES": "es", "es-ES": "es", "es_MX": "es",
		"cs": "en", "he": "en", "xx-YY": "en",
	}
	for in, want := range cases {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNewInvitationCodeFitsTelegramStartPayload(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		code, err := newInvitationCode()
		if err != nil {
			t.Fatalf("newInvitationCode: %v", err)
		}
		// Telegram's /start payload is at most 64 characters of [A-Za-z0-9_-];
		// "inv_" + 43 base64url characters = 47.
		if len(code) != 43 {
			t.Fatalf("code length = %d; want 43", len(code))
		}
		for _, r := range code {
			ok := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_'
			if !ok {
				t.Fatalf("code %q carries %q, outside Telegram's start-payload alphabet", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("code repeated: %q", code)
		}
		seen[code] = true
	}
}
