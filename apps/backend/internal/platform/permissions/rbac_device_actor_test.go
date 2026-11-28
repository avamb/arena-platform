package permissions

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// Spec 69, MA-10: a door-scanner device is decided from the hard-coded device
// list only. The fakes below answer like a real platform superadmin would, so
// any fall-through to roles or memberships would ALLOW and fail the tests.

var (
	devOrg       = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	devOtherOrg  = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	devSuperUser = uuid.MustParse("55555555-5555-4555-8555-555555555555")
)

// devEverythingRBAC grants every permission name to any role set.
type devEverythingRBAC struct{ calls int }

func (f *devEverythingRBAC) GetPermissionsForRoles(context.Context, []string) ([]string, error) {
	f.calls++
	return []string{"superadmin.read", "event.read", "scanner_pass.manage", "scan.read", "scan.perform", "org.update"}, nil
}

// devSuperadminMemberships says every user is a platform superadmin.
type devSuperadminMemberships struct{ calls int }

func (m *devSuperadminMemberships) GetActiveRolesForUser(context.Context, uuid.UUID) ([]string, error) {
	m.calls++
	return []string{"platform_superadmin", "org_admin"}, nil
}

func (m *devSuperadminMemberships) GetActiveRolesForUserInOrg(context.Context, uuid.UUID, uuid.UUID) ([]string, error) {
	m.calls++
	return []string{"platform_superadmin", "org_admin"}, nil
}

// worstDevice is a mis-built device actor: a real superadmin's user id, the
// superadmin role and extra permissions. It must still reach only the list.
func worstDevice() auth.Actor {
	return auth.Actor{
		ID:          devSuperUser.String(),
		Type:        auth.ActorTypeDevice,
		Roles:       []string{"platform_superadmin", "org_admin"},
		Permissions: []string{"scan.perform", "scan.read", "superadmin.read", "event.read", "scanner_pass.manage"},
		OrgID:       devOrg.String(),
	}
}

func newDeviceTestChecker() (*DBChecker, *devEverythingRBAC, *devSuperadminMemberships) {
	rbac := &devEverythingRBAC{}
	mq := &devSuperadminMemberships{}
	return NewDBChecker(rbac).WithMembershipQuerier(mq), rbac, mq
}

func TestDBChecker_DeviceActorHardList(t *testing.T) {
	denied := []string{"superadmin.read", "event.read", "scanner_pass.manage", "org.update", "scan.write", ""}
	actors := map[string]auth.Actor{
		"device":       auth.NewDeviceActor(uuid.NewString(), devOrg.String()),
		"worst device": worstDevice(),
	}
	for name, actor := range actors {
		t.Run(name, func(t *testing.T) {
			c, rbac, mq := newDeviceTestChecker()
			ctx := auth.WithActor(context.Background(), actor)
			for _, action := range []string{"scan.read", "scan.perform"} {
				if err := c.Check(ctx, action, "scan"); err != nil {
					t.Fatalf("Check(%q) = %v, want nil", action, err)
				}
				if err := c.Check(WithOrgScope(ctx, devOrg), action, "scan"); err != nil {
					t.Fatalf("Check(%q) in its own org = %v, want nil", action, err)
				}
			}
			for _, action := range denied {
				err := c.Check(ctx, action, "x")
				var pd *PermissionDeniedError
				if !errors.As(err, &pd) {
					t.Fatalf("Check(%q) = %v, want *PermissionDeniedError", action, err)
				}
			}
			if rbac.calls != 0 || mq.calls != 0 {
				t.Fatalf("a device touched roles/memberships: rbac=%d memberships=%d", rbac.calls, mq.calls)
			}
		})
	}
}

func TestDBChecker_DeviceActorWithoutPermissionField(t *testing.T) {
	c, _, _ := newDeviceTestChecker()
	// A device actor that does not carry the permission on itself gets nothing,
	// even for a code on the list (e.g. a JWT claiming actor_type device).
	ctx := auth.WithActor(context.Background(), auth.Actor{
		ID: devSuperUser.String(), Type: auth.ActorTypeDevice, Roles: []string{"platform_superadmin"}, OrgID: devOrg.String(),
	})
	if err := c.Check(ctx, "scan.read", "scan"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("Check = %v, want denied", err)
	}
}

func TestDBChecker_DeviceActorOtherOrgScopeDenied(t *testing.T) {
	c, _, _ := newDeviceTestChecker()
	ctx := auth.WithActor(context.Background(), auth.NewDeviceActor(uuid.NewString(), devOrg.String()))
	if err := c.CheckInOrg(ctx, "scan.read", devOtherOrg); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("CheckInOrg(other org) = %v, want denied", err)
	}
	noOrg := auth.WithActor(context.Background(), auth.NewDeviceActor(uuid.NewString(), ""))
	if err := c.CheckInOrg(noOrg, "scan.read", devOrg); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("CheckInOrg(device with no org) = %v, want denied", err)
	}
}

func TestDBChecker_SuperadminOrgBypass_DeviceNever(t *testing.T) {
	c, rbac, mq := newDeviceTestChecker()
	if c.SuperadminOrgBypass(context.Background(), worstDevice()) {
		t.Fatal("a device actor must never get the superadmin bypass")
	}
	if c.SuperadminOrgBypass(context.Background(), auth.NewDeviceActor(devSuperUser.String(), devOrg.String())) {
		t.Fatal("a device actor must never get the superadmin bypass")
	}
	if rbac.calls != 0 || mq.calls != 0 {
		t.Fatalf("bypass touched roles/memberships for a device: rbac=%d memberships=%d", rbac.calls, mq.calls)
	}
	// Control: the same fakes DO grant the bypass to a user, so the false
	// above comes from the device branch.
	user := auth.Actor{ID: devSuperUser.String(), Type: auth.ActorTypeUser}
	if !c.SuperadminOrgBypass(context.Background(), user) {
		t.Fatal("control: the fakes must grant the bypass to a user")
	}
}

func TestDBChecker_EffectiveInOrg_Device(t *testing.T) {
	c, rbac, mq := newDeviceTestChecker()
	want := []string{"scan.perform", "scan.read"}

	got, err := c.EffectiveInOrg(context.Background(), worstDevice(), devOrg)
	if err != nil {
		t.Fatalf("EffectiveInOrg: %v", err)
	}
	if len(got.Roles) != 0 || got.Roles == nil {
		t.Fatalf("Roles = %#v, want empty non-nil", got.Roles)
	}
	if !reflect.DeepEqual(got.Permissions, want) {
		t.Fatalf("Permissions = %v, want %v", got.Permissions, want)
	}

	got, err = c.EffectiveInOrg(context.Background(), worstDevice(), devOtherOrg)
	if err != nil {
		t.Fatalf("EffectiveInOrg(other): %v", err)
	}
	if len(got.Roles) != 0 || len(got.Permissions) != 0 || got.Permissions == nil {
		t.Fatalf("another org: %#v, want empty roles and permissions", got)
	}
	if rbac.calls != 0 || mq.calls != 0 {
		t.Fatalf("EffectiveInOrg touched roles/memberships for a device: rbac=%d memberships=%d", rbac.calls, mq.calls)
	}
}
