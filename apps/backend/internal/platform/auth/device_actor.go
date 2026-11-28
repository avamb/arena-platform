// device_actor.go — the door-scanner phone as its own kind of subject
// (spec 69, MA-10).
//
// A device actor is NOT a user and NOT an organization API key. It carries a
// HARD-CODED permission list (DevicePermissions) and nothing else: it never
// inherits a user's roles or permissions, never gets the platform superadmin
// bypass, and its organization comes only from the actor itself (OrgID,
// later filled from the device row), never from org_memberships. Its ID is
// the device id, never a user id, so no code may parse it as one.
//
// Every legacy org guard (the ones written for users and API keys) refuses a
// device for every organization, its own included, through ServiceActorInOrg.
// The future scanner routes check a device's organization with
// DeviceActorInOrg plus their own scanner-pass scope checks.
package auth

import "context"

// devicePermissions is the complete, fixed set of permissions a device actor
// may ever hold. Never extend it from data: a device's reach is decided here.
var devicePermissions = []string{"scan.perform", "scan.read"}

// DevicePermissions returns a fresh copy of the fixed device permission list
// (scan.perform, scan.read). Callers may modify the returned slice freely.
func DevicePermissions() []string {
	return append([]string(nil), devicePermissions...)
}

// IsDevicePermission reports whether code is in the fixed device permission
// list. The checker allows a device only codes for which this is true.
func IsDevicePermission(code string) bool {
	for _, p := range devicePermissions {
		if p == code {
			return true
		}
	}
	return false
}

// IsDevice reports whether the actor is a door-scanner device.
func (a Actor) IsDevice() bool {
	return a.Type == ActorTypeDevice
}

// NewDeviceActor builds the actor of a door-scanner device: Type device, ID =
// the device id (never a user id), OrgID = the device's organization,
// Permissions = the fixed device list. It has no Roles and no RawToken.
// Code that writes audit rows for a device must set the actor type itself,
// because audit.ServiceActorWriter and many handlers treat every non-service
// actor as a user.
func NewDeviceActor(deviceID, orgID string) Actor {
	return Actor{
		ID:          deviceID,
		Type:        ActorTypeDevice,
		OrgID:       orgID,
		Permissions: DevicePermissions(),
	}
}

// DeviceActorInOrg reports whether the request is made by a device and, if
// so, whether the device belongs to orgID (exact match, an empty actor OrgID
// never matches). It is ONLY for the scanner routes, which add their own
// scanner-pass scope checks; the legacy org guards must keep using
// ServiceActorInOrg, which refuses a device for every organization.
func DeviceActorInOrg(ctx context.Context, orgID string) (isDevice, allowed bool) {
	actor, ok := ActorFromContext(ctx)
	if !ok || !actor.IsDevice() {
		return false, false
	}
	return true, actor.OrgID != "" && actor.OrgID == orgID
}
