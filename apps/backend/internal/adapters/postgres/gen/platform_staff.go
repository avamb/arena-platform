package gen

import (
	"context"

	"github.com/google/uuid"
)

// PlatformRoleRowSQL is the ONE definition of "this user_roles row makes its
// holder platform staff" (user_roles alias ur, roles alias r): a role granted
// platform-wide (no organization) that is not one of the membership roles
// org_admin, organizer, agent. A global row of a membership role is an old way
// to hand out the owner role and is not staff. OwnerRecipientWhereSQL (the
// contact form's owner) and IsPlatformStaff (GET /v1/me platform_staff, the
// admin console's sign-in gate) both build on it, and authemail/recipient.go
// mirrors it for the password letters; the admin console is for staff only,
// organizers work in the web event center.
const PlatformRoleRowSQL = `ur.org_id IS NULL
                                   AND r.org_id IS NULL AND r.name NOT IN ('org_admin', 'organizer', 'agent')`

const isPlatformStaff = `SELECT EXISTS (
    SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
    WHERE ur.user_id = $1 AND ` + PlatformRoleRowSQL + `
)`

// IsPlatformStaff reports whether userID holds a platform-wide role other than
// the membership roles (see PlatformRoleRowSQL).
func (q *Queries) IsPlatformStaff(ctx context.Context, userID uuid.UUID) (bool, error) {
	var v bool
	err := q.db.QueryRow(ctx, isPlatformStaff, userID).Scan(&v)
	return v, err
}
