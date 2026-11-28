package auth

import (
	"context"
	"reflect"
	"testing"
)

const deviceID = "44444444-4444-4444-8444-444444444444"

func TestDevicePermissions_FixedListAndFreshCopy(t *testing.T) {
	want := []string{"scan.perform", "scan.read"}
	got := DevicePermissions()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DevicePermissions() = %v, want %v", got, want)
	}
	got[0] = "superadmin.read"
	if again := DevicePermissions(); !reflect.DeepEqual(again, want) {
		t.Fatalf("mutating the returned slice changed the list: %v", again)
	}
}

func TestIsDevicePermission(t *testing.T) {
	for _, code := range []string{"scan.perform", "scan.read"} {
		if !IsDevicePermission(code) {
			t.Fatalf("IsDevicePermission(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"", "superadmin.read", "event.read", "scanner_pass.manage", "scan.write", "SCAN.READ"} {
		if IsDevicePermission(code) {
			t.Fatalf("IsDevicePermission(%q) = true, want false", code)
		}
	}
}

func TestNewDeviceActor(t *testing.T) {
	a := NewDeviceActor(deviceID, keyOrg)
	if ActorTypeDevice != "device" || a.Type != ActorTypeDevice {
		t.Fatalf("Type = %q, want device", a.Type)
	}
	if a.ID != deviceID || a.OrgID != keyOrg {
		t.Fatalf("ID/OrgID = %q/%q", a.ID, a.OrgID)
	}
	if !reflect.DeepEqual(a.Permissions, []string{"scan.perform", "scan.read"}) {
		t.Fatalf("Permissions = %v", a.Permissions)
	}
	if len(a.Roles) != 0 || a.RawToken != "" {
		t.Fatalf("a device carries no roles and no raw token: %v %q", a.Roles, a.RawToken)
	}
	if !a.IsDevice() || a.IsService() {
		t.Fatal("a device actor must report IsDevice and not IsService")
	}
	if !a.IsAuthenticated() {
		t.Fatal("a device actor is authenticated")
	}
}

func TestActorIsDevice(t *testing.T) {
	for _, tp := range []ActorType{ActorTypeUser, ActorTypeStubUser, ActorTypeService, ActorTypeAnon} {
		if (Actor{Type: tp}).IsDevice() {
			t.Fatalf("actor type %q must not report IsDevice", tp)
		}
	}
}

// TestServiceActorInOrg_DeviceRefusedEverywhere pins that the shared authority
// of the legacy org guards answers "not a user, refused" for a device, for its
// own organization too, and whatever a mis-built device actor carries.
func TestServiceActorInOrg_DeviceRefusedEverywhere(t *testing.T) {
	ctxs := map[string]context.Context{
		"device": WithActor(context.Background(), NewDeviceActor(deviceID, keyOrg)),
		"device with superadmin roles": WithActor(context.Background(), Actor{
			ID: deviceID, Type: ActorTypeDevice, OrgID: keyOrg,
			Roles:       []string{"platform_superadmin"},
			Permissions: []string{"scan.read", "superadmin.read"},
		}),
		"device with no org": WithActor(context.Background(), Actor{ID: deviceID, Type: ActorTypeDevice}),
	}
	for name, ctx := range ctxs {
		for _, org := range []string{keyOrg, otherOrg, ""} {
			notUser, allowed := ServiceActorInOrg(ctx, org)
			if !notUser || allowed {
				t.Fatalf("%s, org %q: (notUser, allowed) = (%v, %v), want (true, false)", name, org, notUser, allowed)
			}
		}
	}
}

func TestDeviceActorInOrg(t *testing.T) {
	tests := []struct {
		name         string
		ctx          context.Context
		orgID        string
		wantIsDevice bool
		wantAllowed  bool
	}{
		{"device in its own org", WithActor(context.Background(), NewDeviceActor(deviceID, keyOrg)), keyOrg, true, true},
		{"device in another org", WithActor(context.Background(), NewDeviceActor(deviceID, keyOrg)), otherOrg, true, false},
		{"device with no org", WithActor(context.Background(), NewDeviceActor(deviceID, "")), "", true, false},
		{"service actor is not a device", WithActor(context.Background(), Actor{Type: ActorTypeService, OrgID: keyOrg}), keyOrg, false, false},
		{"user is not a device", WithActor(context.Background(), Actor{Type: ActorTypeUser, ID: "u1"}), keyOrg, false, false},
		{"no actor", context.Background(), keyOrg, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isDevice, allowed := DeviceActorInOrg(tc.ctx, tc.orgID)
			if isDevice != tc.wantIsDevice || allowed != tc.wantAllowed {
				t.Fatalf("(isDevice, allowed) = (%v, %v), want (%v, %v)", isDevice, allowed, tc.wantIsDevice, tc.wantAllowed)
			}
		})
	}
}
