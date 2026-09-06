package hpayments

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/tests/orgauthcases"
)

// TestActorIsMemberOfOrg_ServiceActor runs the shared org-auth table
// (apps/backend/tests/orgauthcases) against this package's guard, so all six
// guards answer the API-key rule identically (spec §13.1, feature #513).
// A nil *gen.Queries proves no case reaches the database.
func TestActorIsMemberOfOrg_ServiceActor(t *testing.T) {
	for _, tc := range orgauthcases.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := actorIsMemberOfOrg(tc.Ctx, nil, tc.OrgID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.WantMember {
				t.Fatalf("member = %v, want %v", got, tc.WantMember)
			}
		})
	}
}
