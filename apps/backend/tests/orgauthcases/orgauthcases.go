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

// Cases returns the shared expectation table. Every case is answerable
// WITHOUT a database: a service actor is resolved from the context alone, and
// the anonymous case short-circuits before any query, so guards may be called
// with a nil *gen.Queries.
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
	}
}
