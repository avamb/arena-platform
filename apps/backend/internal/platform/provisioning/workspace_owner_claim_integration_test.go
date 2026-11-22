//go:build integration

package provisioning

// An approval whose owner e-mail belongs to an existing account with an
// unconfirmed address replaces that account's password, revokes its refresh
// tokens and queues the set-your-password letter; an account with a confirmed
// address is left as it was. Everything runs on a transaction that is rolled
// back.
//
//	DATABASE_URL=... go.exe test -tags integration \
//	  ./apps/backend/internal/platform/provisioning/ -run TestCreateWorkspace_UnconfirmedOwner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/authemail"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

func TestCreateWorkspace_UnconfirmedOwnerIsReset(t *testing.T) {
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
	account := func(email string, verified bool) (uuid.UUID, string) {
		t.Helper()
		hash, err := users.HashPassword("known-password-123")
		if err != nil {
			t.Fatal(err)
		}
		var id uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, email_verified_at)
VALUES ($1, $2, CASE WHEN $3 THEN now() END) RETURNING id`, email, hash, verified).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (token, user_id, expires_at) VALUES ($1, $2, $3)`,
			users.TokenHash(uuid.NewString()), id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		return id, hash
	}
	approve := func(name, email string) Workspace {
		t.Helper()
		ws, err := CreateWorkspace(ctx, tx, WorkspaceInput{
			OrgName: fmt.Sprintf("Claim %s %s", name, nonce), Country: "ES", Locale: "en",
			OwnerEmail: email, PaymentProvider: "stripe",
		})
		if err != nil {
			t.Fatalf("%s: create workspace: %v", name, err)
		}
		return ws
	}
	state := func(id uuid.UUID, email string) (hash string, active, tokens int, letters []authemail.PasswordResetEmailPayload) {
		t.Helper()
		if err := tx.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, id).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, id).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id = $1`, id).Scan(&tokens); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, `SELECT payload FROM worker_jobs WHERE job_type = $1 AND payload->>'email' = $2`,
			authemail.JobTypePasswordResetEmail, email)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var p authemail.PasswordResetEmailPayload
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatal(err)
			}
			letters = append(letters, p)
		}
		return hash, active, tokens, letters
	}

	unconfirmedEmail := fmt.Sprintf("claim-owner-%s@claim.example.test", nonce)
	unconfirmedID, oldHash := account(unconfirmedEmail, false)
	ws := approve("unconfirmed", unconfirmedEmail)
	if ws.OwnerID != unconfirmedID || !ws.OwnerCreated {
		t.Fatalf("workspace = %+v, want the existing owner with a setup letter", ws)
	}
	hash, active, tokens, letters := state(unconfirmedID, unconfirmedEmail)
	if hash == oldHash {
		t.Error("the unconfirmed owner's password was not replaced")
	}
	if active != 0 || tokens != 1 {
		t.Errorf("active refresh tokens = %d, setup tokens = %d; want 0 and 1", active, tokens)
	}
	if len(letters) != 1 || letters[0].Purpose != authemail.PurposeOrgInvitation || letters[0].OrgName == "" {
		t.Fatalf("setup letters = %+v, want one org invitation", letters)
	}

	confirmedEmail := fmt.Sprintf("claim-owner-ok-%s@claim.example.test", nonce)
	confirmedID, keptHash := account(confirmedEmail, true)
	ws = approve("confirmed", confirmedEmail)
	if ws.OwnerID != confirmedID || ws.OwnerCreated {
		t.Fatalf("workspace = %+v, want the existing owner without a letter", ws)
	}
	hash, active, tokens, letters = state(confirmedID, confirmedEmail)
	if hash != keptHash || active != 1 || tokens != 0 || len(letters) != 0 {
		t.Fatalf("a confirmed owner was changed: hash kept=%v active=%d tokens=%d letters=%d", hash == keptHash, active, tokens, len(letters))
	}
}
