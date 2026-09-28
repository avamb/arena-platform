package hbot

import "strings"

// The bot knows two roles (spec 28 §3.2). They map onto membership roles:
// owner is org_admin (every org.* permission, seeded in 0009, allowed as a
// membership role since 0115), manager is organizer (the wizard's own
// permission set, granted in 0115). Any other membership role a person
// already holds reads back as manager — the bot never widens what an
// existing membership grants.
const (
	RoleOwner   = "owner"
	RoleManager = "manager"

	membershipRoleOwner   = "org_admin"
	membershipRoleManager = "organizer"
)

// ParseRole normalizes a bot role from a request body. ok is false for
// anything but owner / manager.
func ParseRole(raw string) (role string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RoleOwner:
		return RoleOwner, true
	case RoleManager:
		return RoleManager, true
	default:
		return "", false
	}
}

// MembershipRoleFor returns the memberships.role a bot role is created with.
func MembershipRoleFor(role string) string {
	if role == RoleOwner {
		return membershipRoleOwner
	}
	return membershipRoleManager
}

// BotRoleFor reads a bot role back from an existing membership role.
func BotRoleFor(membershipRole string) string {
	if membershipRole == membershipRoleOwner {
		return RoleOwner
	}
	return RoleManager
}

// NormalizeLocale keeps the locales the bot speaks (en, ru); anything else
// falls back to en. Region subtags are dropped (ru-RU -> ru).
func NormalizeLocale(raw string) string {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	switch tag {
	case "ru":
		return "ru"
	default:
		return "en"
	}
}
