// Package main_test verifies the in-memory seed plan produced by
// BuildSeed without requiring a live PostgreSQL instance.
//
// The intent is to catch regressions to the fixture schema (typos in
// fixed UUIDs, duplicate IDs, mismatched foreign-key references, role
// names that violate memberships_role_check) at unit-test time so an
// operator who reseeds a clean database never sees a SQL error.
//
// A separate integration test (added with the `integration` build tag)
// exercises the full ApplySeed path against a real database, but that
// suite is only run inside docker-compose.
package main

import (
	"strings"
	"testing"
)

// TestBuildSeed_CountsMatchFeatureSpec pins the row counts to the
// "2-3 each" target stated in feature #247 so a future contributor who
// trims the fixture set sees a failing test before the seed lands.
func TestBuildSeed_CountsMatchFeatureSpec(t *testing.T) {
	t.Parallel()
	s := BuildSeed()

	if got := len(s.Organizations); got < 2 || got > 3 {
		t.Errorf("organizations count = %d; want 2-3", got)
	}
	if got := len(s.Users); got < 2 || got > 4 {
		t.Errorf("users count = %d; want 2-4 (one per role)", got)
	}
	if got := len(s.Channels); got < 2 || got > 3 {
		t.Errorf("channels count = %d; want 2-3", got)
	}
	if got := len(s.PaymentConfigs); got < 2 || got > 3 {
		t.Errorf("payment_configs count = %d; want 2-3", got)
	}
	// Spec asks for 2-3 venues per org — we currently produce 3 per org.
	venuesPerOrg := map[string]int{}
	for _, v := range s.Venues {
		venuesPerOrg[v.OrgID]++
	}
	for _, org := range s.Organizations {
		n := venuesPerOrg[org.ID]
		if n < 2 || n > 3 {
			t.Errorf("org %s has %d venues; want 2-3", org.ID, n)
		}
	}
}

// TestBuildSeed_AllIDsUnique guards against copy/paste mistakes in the
// hard-coded UUID list. Two rows with the same primary key would have
// the second silently dropped by ON CONFLICT (id) DO NOTHING — a subtle
// bug that wouldn't fail the seed run.
func TestBuildSeed_AllIDsUnique(t *testing.T) {
	t.Parallel()
	s := BuildSeed()
	seen := map[string]string{} // id -> "table"

	check := func(id, table string) {
		if prev, ok := seen[id]; ok {
			t.Errorf("duplicate seed UUID %s used by %s and %s", id, prev, table)
			return
		}
		seen[id] = table
	}
	for _, o := range s.Organizations {
		check(o.ID, "organizations")
	}
	for _, v := range s.Venues {
		check(v.ID, "venues")
	}
	for _, u := range s.Users {
		check(u.ID, "users")
	}
	for _, m := range s.Memberships {
		check(m.ID, "memberships")
	}
	for _, c := range s.Channels {
		check(c.ID, "sales_channels")
	}
	for _, pc := range s.PaymentConfigs {
		check(pc.ID, "payment_provider_configs")
	}
}

// TestBuildSeed_VenuesReferenceKnownOrgs ensures every venue points at a
// seeded organization (no dangling FK).
func TestBuildSeed_VenuesReferenceKnownOrgs(t *testing.T) {
	t.Parallel()
	s := BuildSeed()
	orgIDs := map[string]bool{}
	for _, o := range s.Organizations {
		orgIDs[o.ID] = true
	}
	for _, v := range s.Venues {
		if !orgIDs[v.OrgID] {
			t.Errorf("venue %q references unknown org_id %s", v.Name, v.OrgID)
		}
	}
}

// TestBuildSeed_ChannelsAndPaymentConfigsReferenceKnownOrgs guards the
// FK from sales_channels.org_id and payment_provider_configs.org_id.
func TestBuildSeed_ChannelsAndPaymentConfigsReferenceKnownOrgs(t *testing.T) {
	t.Parallel()
	s := BuildSeed()
	orgIDs := map[string]bool{}
	for _, o := range s.Organizations {
		orgIDs[o.ID] = true
	}
	for _, c := range s.Channels {
		if !orgIDs[c.OrgID] {
			t.Errorf("channel %q references unknown org_id %s", c.Name, c.OrgID)
		}
	}
	for _, pc := range s.PaymentConfigs {
		if !orgIDs[pc.OrgID] {
			t.Errorf("payment_config %s references unknown org_id %s", pc.ID, pc.OrgID)
		}
	}
}

// TestBuildSeed_MembershipsAndUserRolesReferenceKnownUsers guards the
// FKs to users.id from both memberships and user_roles.
func TestBuildSeed_MembershipsAndUserRolesReferenceKnownUsers(t *testing.T) {
	t.Parallel()
	s := BuildSeed()
	userIDs := map[string]bool{}
	for _, u := range s.Users {
		userIDs[u.ID] = true
	}
	orgIDs := map[string]bool{}
	for _, o := range s.Organizations {
		orgIDs[o.ID] = true
	}
	for _, m := range s.Memberships {
		if !userIDs[m.UserID] {
			t.Errorf("membership %s references unknown user_id %s", m.ID, m.UserID)
		}
		if !orgIDs[m.OrgID] {
			t.Errorf("membership %s references unknown org_id %s", m.ID, m.OrgID)
		}
	}
	for _, ur := range s.UserRoles {
		if !userIDs[ur.UserID] {
			t.Errorf("user_role for %s/%s references unknown user_id", ur.UserID, ur.RoleName)
		}
		if ur.OrgID != "" && !orgIDs[ur.OrgID] {
			t.Errorf("user_role for %s/%s references unknown org_id %s", ur.UserID, ur.RoleName, ur.OrgID)
		}
	}
}

// TestBuildSeed_MembershipRolesPassCheckConstraint ensures every
// memberships.role value is one of the strings accepted by
// memberships_role_check (defined in 0011_memberships.sql). Without
// this guard a typo in BuildSeed would surface as a SQL constraint
// failure on the very first arena-seed run.
func TestBuildSeed_MembershipRolesPassCheckConstraint(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"organizer":                   true,
		"agent":                       true,
		"platform_operator":           true,
		"external_ticketing_operator": true,
		"platform_superadmin":         true,
	}
	for _, m := range BuildSeed().Memberships {
		if !allowed[m.Role] {
			t.Errorf("membership %s uses role %q not allowed by memberships_role_check", m.ID, m.Role)
		}
	}
}

// TestBuildSeed_ChannelEnumsValid pins payment_mode and provider values
// to the strings accepted by the sales_channels CHECK constraints.
func TestBuildSeed_ChannelEnumsValid(t *testing.T) {
	t.Parallel()
	modes := map[string]bool{"direct_merchant": true, "merchant_of_record": true}
	providers := map[string]bool{"stripe": true, "allpay": true}
	for _, c := range BuildSeed().Channels {
		if !modes[c.PaymentMode] {
			t.Errorf("channel %q payment_mode=%q invalid", c.Name, c.PaymentMode)
		}
		if !providers[c.Provider] {
			t.Errorf("channel %q provider=%q invalid", c.Name, c.Provider)
		}
		if c.PaymentMode == "direct_merchant" && c.ProviderAccountID == "" {
			t.Errorf("channel %q is direct_merchant but ProviderAccountID is empty", c.Name)
		}
	}
}

// TestBuildSeed_PaymentConfigsAreTestOnly guarantees the seed never
// inserts a 'live' payment provider config. Real live credentials must
// always be entered through the admin UI by an operator who knows what
// they're doing.
func TestBuildSeed_PaymentConfigsAreTestOnly(t *testing.T) {
	t.Parallel()
	for _, pc := range BuildSeed().PaymentConfigs {
		if pc.Mode != "test" {
			t.Errorf("payment_config %s mode=%q; seed must only use test credentials", pc.ID, pc.Mode)
		}
		if !strings.Contains(pc.SecretsJSON, "TEST") {
			t.Errorf("payment_config %s secrets %q missing 'TEST' marker — secrets must be obviously fake", pc.ID, pc.SecretsJSON)
		}
	}
}

// TestBuildSeed_UsersAreObviouslyTestData makes sure the seed never
// uses a real-looking email domain. Anything that escapes to a real
// inbox is a privacy bug.
func TestBuildSeed_UsersAreObviouslyTestData(t *testing.T) {
	t.Parallel()
	for _, u := range BuildSeed().Users {
		if !strings.HasSuffix(u.Email, "@test.arena.local") {
			t.Errorf("user email %q must use the @test.arena.local sentinel domain", u.Email)
		}
	}
}

// TestBuildSeed_OrgFieldsValid pins the country/locale/TTL fields to
// values matching the admin-orgs input validation contract.
func TestBuildSeed_OrgFieldsValid(t *testing.T) {
	t.Parallel()
	for _, o := range BuildSeed().Organizations {
		if len(o.Country) != 2 {
			t.Errorf("org %q country %q must be ISO 3166-1 alpha-2", o.Slug, o.Country)
		}
		if o.DefaultLocale == "" {
			t.Errorf("org %q default_locale must not be empty", o.Slug)
		}
		if o.ReservationTTLSeconds <= 0 || o.ReservationTTLSeconds > 86400 {
			t.Errorf("org %q reservation_ttl_seconds=%d out of (0, 86400]", o.Slug, o.ReservationTTLSeconds)
		}
		if !strings.HasPrefix(o.Name, "TEST ") {
			t.Errorf("org %q name must start with TEST so dashboards can spot fixture data", o.Slug)
		}
	}
}
