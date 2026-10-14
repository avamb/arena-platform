//go:build integration

// recipient_integration_test.go — PGRecipients on a live database: who is an
// organizer (their password link goes to the web event center) and who keeps
// the console link. Needs DATABASE_URL on a migrated database; skips without it.

package authemail

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGRecipients_OrganizerOrPlatformUser(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	nonce := uuid.NewString()[:8]
	orgID := uuid.New()
	var users []uuid.UUID
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, orgID)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "Letters Org "+nonce, "letters-"+nonce); err != nil {
		t.Fatal(err)
	}
	user := func(name, locale string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, preferred_locale)
		        VALUES ($1, 'x', $2) RETURNING id`, name+"-"+nonce+"@letters.example", locale).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		return id
	}
	member := func(id uuid.UUID, role string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, $3)`, id, orgID, role); err != nil {
			t.Fatal(err)
		}
	}
	globalRole := func(id uuid.UUID, role string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		        SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL`, id, role); err != nil {
			t.Fatal(err)
		}
	}

	owner := user("owner", "ru")
	member(owner, "org_admin")
	manager := user("manager", "es")
	member(manager, "organizer")
	agent := user("agent", "en")
	member(agent, "agent")
	// An old global row of a membership role does not make a platform user.
	oldStyle := user("old", "en")
	member(oldStyle, "org_admin")
	globalRole(oldStyle, "org_admin")

	superadmin := user("super", "ru")
	member(superadmin, "org_admin")
	globalRole(superadmin, "platform_superadmin")
	support := user("support", "en")
	member(support, "organizer")
	globalRole(support, "support")
	nobody := user("nobody", "en")

	lookup := PGRecipients{DB: pool}
	cases := []struct {
		name      string
		id        uuid.UUID
		organizer bool
		locale    string
	}{
		{"owner", owner, true, "ru"},
		{"manager", manager, true, "es"},
		{"agent", agent, true, "en"},
		{"org_admin with an old global org_admin row", oldStyle, true, "en"},
		{"superadmin who is also an owner", superadmin, false, "ru"},
		{"support who is also a manager", support, false, "en"},
		{"user without a membership", nobody, false, "en"},
		{"unknown user", uuid.New(), false, ""},
	}
	for _, tc := range cases {
		rec, err := lookup.PasswordRecipient(ctx, tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if rec.Organizer != tc.organizer || rec.Locale != tc.locale {
			t.Errorf("%s: got %+v, want organizer=%v locale=%q", tc.name, rec, tc.organizer, tc.locale)
		}
	}
}
