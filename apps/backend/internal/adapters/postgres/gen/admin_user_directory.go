// Hand-maintained typed query wrapper for the superadmin user directory.
package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AdminUserDirectoryRow is the read-only projection used by GET /v1/admin/users.
type AdminUserDirectoryRow struct {
	ID              uuid.UUID
	DisplayNumber   int64
	Email           string
	CreatedAt       time.Time
	EmailVerifiedAt *time.Time
	DeactivatedAt   *time.Time
	GlobalRoles     []byte
	Memberships     []byte
}

const listAdminUsers = `
SELECT u.id, u.display_number, u.email, u.created_at, u.email_verified_at, u.deactivated_at,
       COALESCE((SELECT jsonb_agg(x.role ORDER BY x.role)
                 FROM (SELECT DISTINCT r.name AS role FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                       WHERE ur.user_id = u.id AND ur.org_id IS NULL) x), '[]'::jsonb),
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', m.id, 'org_id', o.id, 'name', o.name, 'slug', o.slug, 'role', m.role)
                                  ORDER BY o.name, m.role)
                 FROM memberships m JOIN organizations o ON o.id = m.org_id
                 WHERE m.user_id = u.id AND m.status = 'active'), '[]'::jsonb)
FROM users u
WHERE lower(u.email) LIKE '%' || lower($1) || '%'
ORDER BY u.created_at DESC, u.id DESC
LIMIT $2 OFFSET $3`

// ListAdminUsers returns one page with current global roles and active memberships.
func (q *Queries) ListAdminUsers(ctx context.Context, emailSearch string, limit, offset int32) ([]AdminUserDirectoryRow, error) {
	rows, err := q.db.Query(ctx, listAdminUsers, emailSearch, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdminUserDirectoryRow, 0)
	for rows.Next() {
		var item AdminUserDirectoryRow
		if err := rows.Scan(&item.ID, &item.DisplayNumber, &item.Email, &item.CreatedAt, &item.EmailVerifiedAt, &item.DeactivatedAt, &item.GlobalRoles, &item.Memberships); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const countAdminUsers = `SELECT count(*) FROM users WHERE lower(email) LIKE '%' || lower($1) || '%'`

func (q *Queries) CountAdminUsers(ctx context.Context, emailSearch string) (int64, error) {
	var total int64
	err := q.db.QueryRow(ctx, countAdminUsers, emailSearch).Scan(&total)
	return total, err
}
