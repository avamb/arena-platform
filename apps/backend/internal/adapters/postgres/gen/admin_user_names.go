// Hand-maintained typed query wrappers for the optional user name (migration
// 0123) and the admin member list that shows it next to the e-mail.
package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AdminMemberRow is one active membership of an organization together with
// the member's e-mail and optional name, for GET /v1/admin/organizations/{id}/members.
type AdminMemberRow struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	OrgID     uuid.UUID
	Role      string
	Status    string
	JoinedAt  time.Time
	Email     string
	FirstName *string
	LastName  *string
}

const listAdminMembersByOrg = `
SELECT m.id, m.user_id, m.org_id, m.role, m.status, m.joined_at, u.email, u.first_name, u.last_name
FROM   memberships m
JOIN   users u ON u.id = m.user_id
WHERE  m.org_id = $1
  AND  m.status = 'active'
ORDER  BY m.joined_at ASC, m.id ASC`

// ListAdminMembersByOrg returns the active memberships of an organization with
// each member's e-mail and name, oldest first.
func (q *Queries) ListAdminMembersByOrg(ctx context.Context, orgID uuid.UUID) ([]AdminMemberRow, error) {
	rows, err := q.db.Query(ctx, listAdminMembersByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminMemberRow
	for rows.Next() {
		var m AdminMemberRow
		if err := rows.Scan(&m.ID, &m.UserID, &m.OrgID, &m.Role, &m.Status, &m.JoinedAt, &m.Email, &m.FirstName, &m.LastName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const setUserName = `
UPDATE users SET first_name = $2, last_name = $3
WHERE  id = $1
RETURNING id`

// SetUserName replaces a user's first and last name; nil clears a field. It
// reports pgx.ErrNoRows for an unknown user.
func (q *Queries) SetUserName(ctx context.Context, userID uuid.UUID, firstName, lastName *string) error {
	var id uuid.UUID
	return q.db.QueryRow(ctx, setUserName, userID, firstName, lastName).Scan(&id)
}
