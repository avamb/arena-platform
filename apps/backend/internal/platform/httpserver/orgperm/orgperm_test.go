package orgperm

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestHasMembershipRow_PlatformRoleIsNotAMembership(t *testing.T) {
	org, other := uuid.New(), uuid.New()
	row := func(o uuid.UUID, role, status string) gen.MembershipRow {
		return gen.MembershipRow{OrgID: o, Role: role, Status: status}
	}
	cases := []struct {
		name string
		rows []gen.MembershipRow
		want bool
	}{
		{"agent", []gen.MembershipRow{row(org, "agent", "active")}, true},
		{"owner", []gen.MembershipRow{row(org, "org_admin", "active")}, true},
		{"platform superadmin row only", []gen.MembershipRow{row(org, "platform_superadmin", "active")}, false},
		{"platform operator row only", []gen.MembershipRow{row(org, "platform_operator", "active")}, false},
		{"platform row plus a real role", []gen.MembershipRow{row(org, "platform_operator", "active"), row(org, "organizer", "active")}, true},
		{"suspended", []gen.MembershipRow{row(org, "org_admin", "suspended")}, false},
		{"another organization", []gen.MembershipRow{row(other, "org_admin", "active")}, false},
		{"no rows", nil, false},
	}
	for _, c := range cases {
		if got := HasMembershipRow(c.rows, org); got != c.want {
			t.Errorf("%s: %v; want %v", c.name, got, c.want)
		}
	}
	if !IsPlatformRole("platform_operator") || IsPlatformRole("org_admin") {
		t.Error("IsPlatformRole disagrees with the membership rule")
	}
}
