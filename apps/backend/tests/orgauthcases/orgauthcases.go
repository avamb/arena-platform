// Package orgauthcases holds the ONE shared table of org-membership
// expectations for organization API keys (spec §13.1, feature #513 /
// epic #466 W1-C1b).
//
// The rule under test — "a service actor is a member of api_keys.org_id and of
// no other organization" — is enforced by six guards that live in six
// different packages (httpserver plus the hcatalog / hiam / hpayments /
// hbankaccounts / hseating twins), each with an unexported
// actorIsMemberOfOrg. A single test file cannot reach all six, and six
// independently written tables would drift apart — which is exactly how the
// permissive `membershipQueries == nil` branches in hpayments and
// hbankaccounts came to differ from the fail-closed ones elsewhere.
//
// So the table is exported from here and each guard package contributes a
// four-line test that iterates it. Add a case once; all six twins are
// re-checked.
package orgauthcases

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// KeyOrgID is the organization the API key in every case belongs to.
var KeyOrgID = uuid.MustParse("11111111-1111-4111-8111-111111111111")

// OtherOrgID is a different organization the same key must never reach.
var OtherOrgID = uuid.MustParse("22222222-2222-4222-8222-222222222222")

// KeyID is the api_keys.id used as the service actor's subject.
var KeyID = uuid.MustParse("33333333-3333-4333-8333-333333333333")

// Case is one expectation for a guard's actorIsMemberOfOrg helper.
type Case struct {
	// Name identifies the scenario in test output.
	Name string
	// Ctx is the request context carrying (or not carrying) an actor.
	Ctx context.Context
	// OrgID is the organization the guard is asked about.
	OrgID uuid.UUID
	// WantMember is the decision every guard must reach.
	WantMember bool
}

// ServiceCtx returns a context carrying a service actor bound to orgID, as the
// applyAuth middleware builds it after a successful API-key authentication.
func ServiceCtx(orgID uuid.UUID) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{
		ID:          KeyID.String(),
		Type:        auth.ActorTypeService,
		Permissions: []string{"event.read", "session.read"},
		OrgID:       orgID.String(),
	})
}

// DeviceID is the scanner_devices id used as a device actor's subject.
var DeviceID = uuid.MustParse("44444444-4444-4444-8444-444444444444")

// SuperadminUserID stands for a platform superadmin's users.id: a device
// actor wrongly built with it as ID must still be refused.
var SuperadminUserID = uuid.MustParse("55555555-5555-4555-8555-555555555555")

// DeviceCtx returns a context carrying a door-scanner device actor bound to
// orgID (spec 69, MA-10).
func DeviceCtx(orgID uuid.UUID) context.Context {
	return auth.WithActor(context.Background(), auth.NewDeviceActor(DeviceID.String(), orgID.String()))
}

// Cases returns the shared expectation table. Every case is answerable
// WITHOUT a database: service and device actors are resolved from the context
// alone, and the anonymous case short-circuits before any query, so guards may
// be called with a nil *gen.Queries.
//
// A device is refused by every legacy guard for every organization, its own
// included: the scanner routes have their own organization and pass checks.
func Cases() []Case {
	return []Case{
		{
			Name:       "service actor reaches its own org",
			Ctx:        ServiceCtx(KeyOrgID),
			OrgID:      KeyOrgID,
			WantMember: true,
		},
		{
			Name:       "service actor is denied another org",
			Ctx:        ServiceCtx(KeyOrgID),
			OrgID:      OtherOrgID,
			WantMember: false,
		},
		{
			Name: "service actor with no org reaches nothing",
			Ctx: auth.WithActor(context.Background(), auth.Actor{
				ID:   KeyID.String(),
				Type: auth.ActorTypeService,
			}),
			OrgID:      KeyOrgID,
			WantMember: false,
		},
		{
			Name:       "request with no actor is not a member",
			Ctx:        context.Background(),
			OrgID:      KeyOrgID,
			WantMember: false,
		},
		{
			Name:       "device is refused its own org",
			Ctx:        DeviceCtx(KeyOrgID),
			OrgID:      KeyOrgID,
			WantMember: false,
		},
		{
			Name:       "device is refused another org",
			Ctx:        DeviceCtx(KeyOrgID),
			OrgID:      OtherOrgID,
			WantMember: false,
		},
		{
			Name: "device with a superadmin's roles is refused",
			Ctx: auth.WithActor(context.Background(), auth.Actor{
				ID:          SuperadminUserID.String(),
				Type:        auth.ActorTypeDevice,
				Roles:       []string{"platform_superadmin", "org_admin"},
				Permissions: []string{"scan.perform", "scan.read", "superadmin.read", "event.read"},
				OrgID:       KeyOrgID.String(),
			}),
			OrgID:      KeyOrgID,
			WantMember: false,
		},
	}
}
