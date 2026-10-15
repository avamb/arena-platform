package main

// Fixture of the web twin test of task 4-5 (the team screen,
// apps/event-center/tests/smoke/live/team.spec.ts).
//
// One person the owner can remove (a manager with fixed ids, so a rerun finds
// him again) and four small operations the spec calls through the seed binary,
// because the test process has no SQL driver:
//
//	--team-op reset    puts the team back: the removable manager is a member
//	                   again, and every account the test invited (e-mails
//	                   e2e-team-invite-*@test.arena.local) is gone with its
//	                   invitations, setup tokens and queued letters;
//	--team-op age      moves the last letter of --team-email's invitation 11
//	                   minutes into the past, so a resend is allowed;
//	--team-op expire   makes that invitation expired (and aged);
//	--team-op inspect  prints what the database holds for --team-email.
//
// Kept apart from main.go so the parallel tasks that extend the seed do not
// collide with it. Nothing here touches the three people of the main fixture.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	// The manager the owner removes in the twin.
	teamRemovableUserID       = "fe0000e5-0000-7000-8000-0000000004a1"
	teamRemovableMembershipID = "fe0000e5-0000-7000-8000-0000000004a2"
	teamRemovableEmail        = "e2e-team-removable@test.arena.local"

	// Every address the twin invites matches this pattern (LIKE).
	teamInvitePattern = "e2e-team-invite-%@test.arena.local"
)

// applyTeamFixture is idempotent AND resetting.
func applyTeamFixture(ctx context.Context, tx pgx.Tx) error {
	run := func(what, sql string, args ...any) error {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("team fixture %s: %w", what, err)
		}
		return nil
	}
	// Everything the twin's invitations created, children first.
	if err := run("invited letters", `DELETE FROM worker_jobs WHERE payload->>'email' LIKE $1`, teamInvitePattern); err != nil {
		return err
	}
	for _, q := range []struct{ what, sql string }{
		{"invitations", `DELETE FROM bot_invitations WHERE email LIKE $1`},
		{"setup tokens", `DELETE FROM password_reset_tokens WHERE user_id IN (SELECT id FROM users WHERE email LIKE $1)`},
		{"memberships", `DELETE FROM memberships WHERE user_id IN (SELECT id FROM users WHERE email LIKE $1)`},
		{"users", `DELETE FROM users WHERE email LIKE $1`},
	} {
		if err := run(q.what, q.sql, teamInvitePattern); err != nil {
			return err
		}
	}
	// The removable manager, back in the team.
	hash, err := bcrypt.GenerateFromPassword([]byte(seedPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := run("removable user", `
		INSERT INTO users (id, email, password_hash, preferred_locale, email_verified_at)
		VALUES ($1, $2, $3, 'en', now())
		ON CONFLICT (id) DO NOTHING`, teamRemovableUserID, teamRemovableEmail, string(hash)); err != nil {
		return err
	}
	return run("removable membership", `
		INSERT INTO memberships (id, user_id, org_id, role, status)
		VALUES ($1, $2, $3, 'organizer', 'active')
		ON CONFLICT (id) DO UPDATE SET status = 'active'`,
		teamRemovableMembershipID, teamRemovableUserID, orgID)
}

// runTeamOp serves --team-op. It prints one JSON line.
func runTeamOp(op, email string) error {
	cfg, err := config.LoadFor(config.RoleMigrate)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.DBDSN())
	if err != nil {
		return fmt.Errorf("open pgx pool: %w", err)
	}
	defer pool.Close()

	switch op {
	case "reset":
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := applyTeamFixture(ctx, tx); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return printTeamJSON(map[string]string{"ok": "reset"})
	case "age", "expire":
		if email == "" {
			return fmt.Errorf("--team-op %s needs --team-email", op)
		}
		sql := `UPDATE bot_invitations SET last_sent_at = now() - interval '11 minutes'
		        WHERE org_id = $1 AND email = $2 AND revoked_at IS NULL`
		if op == "expire" {
			sql = `UPDATE bot_invitations SET last_sent_at = now() - interval '11 minutes', expires_at = now() - interval '1 hour'
			       WHERE org_id = $1 AND email = $2 AND revoked_at IS NULL`
		}
		tag, err := pool.Exec(ctx, sql, orgID, email)
		if err != nil {
			return fmt.Errorf("%s invitation: %w", op, err)
		}
		return printTeamJSON(map[string]int64{"rows": tag.RowsAffected()})
	case "inspect":
		if email == "" {
			return fmt.Errorf("--team-op inspect needs --team-email")
		}
		out := map[string]any{"membership_roles": []string{}, "preferred_locale": "", "invitations": 0, "revoked_invitations": 0, "jobs": []string{}}
		roles := []string{}
		rows, err := pool.Query(ctx, `SELECT m.role FROM memberships m JOIN users u ON u.id = m.user_id
		                              WHERE m.org_id = $1 AND u.email = $2 AND m.status = 'active' ORDER BY m.role`, orgID, email)
		if err != nil {
			return fmt.Errorf("inspect memberships: %w", err)
		}
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				rows.Close()
				return err
			}
			roles = append(roles, r)
		}
		rows.Close()
		out["membership_roles"] = roles
		var locale string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(max(preferred_locale), '') FROM users WHERE email = $1`, email).Scan(&locale); err != nil {
			return fmt.Errorf("inspect user: %w", err)
		}
		out["preferred_locale"] = locale
		var live, revoked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FILTER (WHERE revoked_at IS NOT NULL)
		                              FROM bot_invitations WHERE org_id = $1 AND email = $2`, orgID, email).Scan(&live, &revoked); err != nil {
			return fmt.Errorf("inspect invitations: %w", err)
		}
		out["invitations"], out["revoked_invitations"] = live, revoked
		jobs := []string{}
		jr, err := pool.Query(ctx, `SELECT job_type FROM worker_jobs WHERE payload->>'email' = $1 ORDER BY created_at`, email)
		if err != nil {
			return fmt.Errorf("inspect jobs: %w", err)
		}
		for jr.Next() {
			var j string
			if err := jr.Scan(&j); err != nil {
				jr.Close()
				return err
			}
			jobs = append(jobs, j)
		}
		jr.Close()
		out["jobs"] = jobs
		return printTeamJSON(out)
	default:
		return fmt.Errorf("unknown --team-op %q (reset, age, expire, inspect)", op)
	}
}

func printTeamJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(body, '\n'))
	return err
}
