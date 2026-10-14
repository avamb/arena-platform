package authemail

// recipient.go decides where a password link of one user lands: the
// superadmin console (APP_PUBLIC_URL) or the organizers' web event center
// (EVENT_CENTER_PUBLIC_URL, spec 08_architecture/37_web_event_center_ru.md §4.2).
//
// The decision is made by arena-worker when the letter is sent, not when the
// job is queued: every enqueuer (self-service reset, admin user create, admin
// member add, onboarding approval) commits the membership in the same
// transaction as the job, so the worker always sees the final roles.

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PasswordRecipient is what the password e-mail needs to know about its
// recipient.
type PasswordRecipient struct {
	// Organizer is true for a member of an organization (owner, manager or
	// agent) who holds no platform role. Their links go to the event center.
	Organizer bool
	// Locale is users.preferred_locale.
	Locale string
}

// RecipientLookup reads a PasswordRecipient. A user that does not exist is
// the zero value with a nil error: the letter then keeps the console link.
type RecipientLookup interface {
	PasswordRecipient(ctx context.Context, userID uuid.UUID) (PasswordRecipient, error)
}

// RowQuerier is the one pgx method PGRecipients needs; *pgxpool.Pool has it.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGRecipients implements RecipientLookup over the users, memberships and
// user_roles tables.
type PGRecipients struct {
	DB RowQuerier
}

// passwordRecipientSQL: a person is an organizer when they hold an
// organization role through a membership and NO platform role anywhere. A
// global user_roles row of any other role (platform_superadmin,
// platform_operator, admin, support, ...) keeps the console link: those
// people work in admin-web. A global row of a membership role (an old way to
// hand out org_admin) is not a platform role; this is the same rule as the
// contact form's owner (gen.OwnerRecipientWhereSQL).
const passwordRecipientSQL = `
SELECT
    EXISTS (
        SELECT 1 FROM memberships m
        WHERE m.user_id = u.id AND m.role IN ('org_admin', 'organizer', 'agent')
    )
    AND NOT EXISTS (
        SELECT 1 FROM memberships m
        WHERE m.user_id = u.id AND m.role IN ('platform_superadmin', 'platform_operator')
    )
    AND NOT EXISTS (
        SELECT 1 FROM user_roles ur
        JOIN roles r ON r.id = ur.role_id
        WHERE ur.user_id = u.id
          AND ((ur.org_id IS NULL AND r.name NOT IN ('org_admin', 'organizer', 'agent'))
               OR r.name IN ('platform_superadmin', 'platform_operator'))
    ),
    u.preferred_locale
FROM users u
WHERE u.id = $1`

// PasswordRecipient implements RecipientLookup.
func (p PGRecipients) PasswordRecipient(ctx context.Context, userID uuid.UUID) (PasswordRecipient, error) {
	var rec PasswordRecipient
	err := p.DB.QueryRow(ctx, passwordRecipientSQL, userID).Scan(&rec.Organizer, &rec.Locale)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordRecipient{}, nil
	}
	if err != nil {
		return PasswordRecipient{}, err
	}
	return rec, nil
}
