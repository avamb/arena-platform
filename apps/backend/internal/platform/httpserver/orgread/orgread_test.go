package orgread

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/tests/orgauthcases"
)

// The shared API-key table: a key reads its own organization and no other.
// A nil *gen.Queries proves no case reaches the database.
func TestReader_ServiceActorCases(t *testing.T) {
	for _, tc := range orgauthcases.Cases() {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := New(tc.Ctx, nil).Can(tc.OrgID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.WantMember {
				t.Fatalf("can = %v, want %v", got, tc.WantMember)
			}
		})
	}
}

func TestReader_SuperadminReadsEveryOrganization(t *testing.T) {
	ctx := auth.WithSuperadminOrgAccess(orgauthcases.ServiceCtx(orgauthcases.KeyOrgID))
	got, err := New(ctx, nil).Can(orgauthcases.OtherOrgID)
	if err != nil || !got {
		t.Fatalf("superadmin can = %v (err %v), want true", got, err)
	}
}
