//go:build integration

package provisioning

// Owner rule 2026-10-26 (UXH-08): an organizer created by an approval works
// only in the web event center, so approving an application must never hand
// out a platform-wide role. CreateWorkspace gives the owner a membership and
// nothing in user_roles; a role the person already held stays exactly as it
// was. Everything runs on a transaction that is rolled back.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	  ./apps/backend/internal/platform/provisioning/ -run TestCreateWorkspace_Never

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCreateWorkspace_NeverCreatesAGlobalRole(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	nonce := uuid.NewString()[:8]
	globalRows := func(userID uuid.UUID) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_roles WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	approve := func(name, ownerEmail string) Workspace {
		t.Helper()
		ws, err := CreateWorkspace(ctx, tx, WorkspaceInput{
			OrgName: fmt.Sprintf("Roles %s %s", name, nonce), Country: "ES", Locale: "en",
			OwnerEmail: ownerEmail, PaymentProvider: "stripe",
		})
		if err != nil {
			t.Fatalf("%s: create workspace: %v", name, err)
		}
		return ws
	}
	existingUser := func(email string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// A brand-new owner: membership yes, user_roles none.
	fresh := approve("fresh", fmt.Sprintf("fresh-%s@roles.example.test", nonce))
	if !fresh.OwnerCreated {
		t.Fatal("the new owner was not created")
	}
	if n := globalRows(fresh.OwnerID); n != 0 {
		t.Fatalf("new owner holds %d user_roles rows, want 0", n)
	}

	// A person who already has an account but no role: still none.
	plainEmail := fmt.Sprintf("plain-%s@roles.example.test", nonce)
	plainID := existingUser(plainEmail)
	plain := approve("plain", plainEmail)
	if plain.OwnerID != plainID || plain.OwnerCreated {
		t.Fatalf("existing account was not reused: %+v", plain)
	}
	if n := globalRows(plainID); n != 0 {
		t.Fatalf("existing owner holds %d user_roles rows, want 0", n)
	}

	// Platform staff who also receives an organization keep exactly the one
	// role they had.
	staffEmail := fmt.Sprintf("staff-%s@roles.example.test", nonce)
	staffID := existingUser(staffEmail)
	if err := grantGlobal(ctx, tx, staffID, "platform_superadmin"); err != nil {
		t.Fatal(err)
	}
	approve("staff", staffEmail)
	if n := globalRows(staffID); n != 1 {
		t.Fatalf("staff holds %d user_roles rows after an approval, want the 1 it had", n)
	}

	// Each one got the owner membership and no other.
	for name, id := range map[string]uuid.UUID{"fresh": fresh.OwnerID, "plain": plainID, "staff": staffID} {
		var roles []string
		rows, err := tx.Query(ctx, `SELECT role FROM memberships WHERE user_id = $1`, id)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			roles = append(roles, r)
		}
		rows.Close()
		if len(roles) != 1 || roles[0] != OwnerRole {
			t.Fatalf("%s memberships = %v, want [%s]", name, roles, OwnerRole)
		}
	}
}

func grantGlobal(ctx context.Context, tx pgx.Tx, userID uuid.UUID, role string) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
	        SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL`, userID, role)
	return err
}
