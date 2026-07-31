// plans_test.go — AB-28 unit coverage for the seating_plan org-membership
// bypass (superadmin path) introduced by the d199966 fix.
//
// Four cases are tested per the feature acceptance criteria:
//
//  1. Member without superadmin   → requireOrgMembership returns true
//  2. Non-member without superadmin → requireOrgMembership returns false (caller writes 403)
//  3. Superadmin WITH X-Admin-Reason → requireOrgMembership returns true (bypass is audited)
//  4. Superadmin WITHOUT X-Admin-Reason → requireOrgMembership returns false (reason mandatory)
//
// These tests exercise requireOrgMembership directly (same package) and are
// stdlib-only so they run in the Unit CI job without a live PostgreSQL.
//
// Implementation note: ListMembershipsByUser uses scanMembershipWithOrgRow
// which scans 8 columns (id, user_id, org_id, role, status, joined_at, org_name,
// org_display_number). The fakeMemRows.Scan implementation must reflect this.
package hseating

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake DBTX that returns a controlled set of membership rows.
// ─────────────────────────────────────────────────────────────────────────────

// membershipDBTX is a fake DBTX whose Query method returns a controlled rows
// set for the ListMembershipsByUser call. All other methods panic so
// unintended calls are caught immediately.
type membershipDBTX struct {
	rows []gen.MembershipRow
}

func (m *membershipDBTX) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	panic("membershipDBTX: Exec must not be called")
}

func (m *membershipDBTX) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	panic("membershipDBTX: QueryRow must not be called")
}

func (m *membershipDBTX) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return &fakeMemRows{rows: m.rows}, nil
}

// fakeMemRows implements pgx.Rows for a static slice of MembershipRow values.
// Next() advances the internal cursor (mirroring real pgx behaviour) so that
// Scan() can read the current row without advancing.
//
// IMPORTANT: ListMembershipsByUser calls scanMembershipWithOrgRow which scans
// 8 columns (the 6 memberships columns + org_name + org_display_number). The
// Scan implementation below must write all 8 destination pointers.
type fakeMemRows struct {
	rows    []gen.MembershipRow
	current *gen.MembershipRow
	index   int
}

func (f *fakeMemRows) Close()                                      {}
func (f *fakeMemRows) Err() error                                  { return nil }
func (f *fakeMemRows) CommandTag() pgconn.CommandTag               { return pgconn.CommandTag{} }
func (f *fakeMemRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeMemRows) Values() ([]any, error)                      { return nil, nil }
func (f *fakeMemRows) RawValues() [][]byte                         { return nil }
func (f *fakeMemRows) Conn() *pgx.Conn                             { return nil }

// Next advances the cursor and prepares the current row for Scan.
func (f *fakeMemRows) Next() bool {
	if f.index < len(f.rows) {
		row := f.rows[f.index]
		f.current = &row
		f.index++
		return true
	}
	f.current = nil
	return false
}

// Scan copies the current row (set by Next) into dest.
// scanMembershipWithOrgRow — used by ListMembershipsByUser — passes 8 pointers:
//
//	dest[0] *uuid.UUID  (id)
//	dest[1] *uuid.UUID  (user_id)
//	dest[2] *uuid.UUID  (org_id)
//	dest[3] *string     (role)
//	dest[4] *string     (status)
//	dest[5] *time.Time  (joined_at)
//	dest[6] *string     (org_name)
//	dest[7] *int64      (org_display_number)
func (f *fakeMemRows) Scan(dest ...any) error {
	if f.current == nil {
		return pgx.ErrNoRows
	}
	if len(dest) < 6 {
		return nil
	}
	*dest[0].(*uuid.UUID) = f.current.ID
	*dest[1].(*uuid.UUID) = f.current.UserID
	*dest[2].(*uuid.UUID) = f.current.OrgID
	*dest[3].(*string) = f.current.Role
	*dest[4].(*string) = f.current.Status
	*dest[5].(*time.Time) = f.current.JoinedAt
	// Optional org columns (only when len(dest) == 8).
	if len(dest) >= 7 {
		*dest[6].(*string) = f.current.OrgName
	}
	if len(dest) >= 8 {
		*dest[7].(*int64) = f.current.OrgDisplayNumber
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helper: request with optional actor/superadmin marker.
// ─────────────────────────────────────────────────────────────────────────────

// newAuthzRequest builds a GET request whose context carries the supplied
// actor (and optionally the superadmin org-access marker). X-Admin-Reason is
// set when adminReason is non-empty.
func newAuthzRequest(t *testing.T, actor *auth.Actor, withSuperadmin bool, adminReason string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := r.Context()
	if actor != nil {
		ctx = auth.WithActor(ctx, *actor)
	}
	if withSuperadmin {
		ctx = auth.WithSuperadminOrgAccess(ctx)
	}
	r = r.WithContext(ctx)
	if adminReason != "" {
		r.Header.Set("X-Admin-Reason", adminReason)
	}
	return r
}

// ─────────────────────────────────────────────────────────────────────────────
// AB-28: requireOrgMembership — four acceptance cases
// ─────────────────────────────────────────────────────────────────────────────

// TestAB28_RequireOrgMembership_Member_Allowed verifies that an actor holding
// an active membership in the target org is permitted.
func TestAB28_RequireOrgMembership_Member_Allowed(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	orgID := uuid.New()

	q := gen.New(&membershipDBTX{
		rows: []gen.MembershipRow{
			{
				ID:       uuid.New(),
				UserID:   userID,
				OrgID:    orgID,
				Role:     "admin",
				Status:   "active",
				JoinedAt: time.Now(),
			},
		},
	})

	actor := &auth.Actor{ID: userID.String(), Type: auth.ActorTypeUser}
	r := newAuthzRequest(t, actor, false, "")
	w := httptest.NewRecorder()

	got, err := requireOrgMembership(w, r, q, orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("member should be allowed (expected true)")
	}
	// requireOrgMembership must NOT have written an error response.
	if w.Code != http.StatusOK {
		t.Fatalf("expected no error response (200 recorder default), got %d", w.Code)
	}
}

// TestAB28_RequireOrgMembership_NonMember_Rejected verifies that an actor with
// no membership in the target org is rejected. requireOrgMembership itself
// returns (false, nil); the CALLER is responsible for writing the 403.
func TestAB28_RequireOrgMembership_NonMember_Rejected(t *testing.T) {
	t.Parallel()
	userID := uuid.New()
	orgID := uuid.New()

	// Actor holds a membership in a DIFFERENT org — this lookup finds no match.
	q := gen.New(&membershipDBTX{
		rows: []gen.MembershipRow{
			{
				ID:       uuid.New(),
				UserID:   userID,
				OrgID:    uuid.New(), // different org
				Role:     "admin",
				Status:   "active",
				JoinedAt: time.Now(),
			},
		},
	})

	actor := &auth.Actor{ID: userID.String(), Type: auth.ActorTypeUser}
	r := newAuthzRequest(t, actor, false, "")
	w := httptest.NewRecorder()

	got, err := requireOrgMembership(w, r, q, orgID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("non-member without superadmin must be rejected (expected false)")
	}
	// requireOrgMembership does not write the 403 directly — it just returns
	// false so the handler can decide the status code (403 for create/fork,
	// 404 for update to avoid leaking plan existence). Verify no response was
	// written here.
	if w.Code != http.StatusOK {
		t.Fatalf("requireOrgMembership must not write a response for non-members (got %d)", w.Code)
	}
}

// TestAB28_RequireOrgMembership_SuperadminWithReason_Allowed verifies that a
// platform_superadmin with X-Admin-Reason bypasses org membership. The DB must
// NOT be queried (unusedDBTX panics if called).
func TestAB28_RequireOrgMembership_SuperadminWithReason_Allowed(t *testing.T) {
	t.Parallel()
	q := gen.New(unusedDBTX{t: t})

	actor := &auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"}}
	r := newAuthzRequest(t, actor, true /*withSuperadmin*/, "bootstrap org for customer")
	w := httptest.NewRecorder()

	got, err := requireOrgMembership(w, r, q, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("superadmin with X-Admin-Reason must be allowed (expected true)")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected no error response (200 default), got %d", w.Code)
	}
}

// TestAB28_RequireOrgMembership_SuperadminWithoutReason_Rejected verifies that
// the X-Admin-Reason header is mandatory even for superadmins. The DB must NOT
// be queried.
func TestAB28_RequireOrgMembership_SuperadminWithoutReason_Rejected(t *testing.T) {
	t.Parallel()
	q := gen.New(unusedDBTX{t: t})

	actor := &auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser, Roles: []string{"platform_superadmin"}}
	r := newAuthzRequest(t, actor, true /*withSuperadmin*/, "" /*no reason*/)
	w := httptest.NewRecorder()

	got, err := requireOrgMembership(w, r, q, uuid.New())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("superadmin without X-Admin-Reason must be rejected (expected false)")
	}
	// RequireAdminReason writes a non-2xx response.
	if w.Code == http.StatusOK {
		t.Fatalf("expected an error response when no X-Admin-Reason provided, got 200")
	}
}
