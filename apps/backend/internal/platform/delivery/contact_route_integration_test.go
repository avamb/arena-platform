//go:build integration

// contact_route_integration_test.go — GetTicketContactRoute on a live
// database: which owner may receive the contact form's messages (never a
// platform superadmin, never a deactivated or anonymized account), the event
// page is linked only while it answers, and "hosted page enabled" is read
// the way the page reads it.

package delivery

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestTicketContactRoute_OwnersEventPageAndHostedFlag(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()

	var orgID, channelID, eventID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT cs.org_id, cs.channel_id, s.event_id FROM tickets t
	        JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
	        JOIN sessions s ON s.id = t.session_id WHERE t.id = $1`, seed.TicketID).Scan(&orgID, &channelID, &eventID); err != nil {
		t.Fatal(err)
	}
	evSlug := "ev-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'published', slug = $2 WHERE id = $1`, eventID, evSlug); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = NULL WHERE id = $1`, orgID); err != nil {
		t.Fatal(err)
	}
	// The channel's flag stored as a STRING: the page reads it with #>>, so
	// the letters must too (an organizer can type it through the free-form
	// settings field).
	if _, err := pool.Exec(ctx, `UPDATE sales_channels SET settings = '{"hosted_page": {"enabled": "true"}}'::jsonb WHERE id = $1`, channelID); err != nil {
		t.Fatal(err)
	}

	// Deferred after the seed's cleanup, so it runs first (LIFO): the
	// memberships go before the organization does.
	var users []uuid.UUID
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE user_id = ANY($1)`, users)
		_, _ = pool.Exec(c, `DELETE FROM users WHERE id = ANY($1)`, users)
	}()
	addOwner := func(email string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		users = append(users, id)
		if _, err := pool.Exec(ctx, `INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'org_admin')`, id, orgID); err != nil {
			t.Fatal(err)
		}
		return id
	}
	q := gen.New(pool)
	route := func() gen.TicketContactRoute {
		t.Helper()
		r, err := q.GetTicketContactRoute(ctx, seed.TicketID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	r := route()
	if !r.HostedPage {
		t.Fatal(`"enabled": "true" (a string) must count as a hosted page, as the page itself reads it`)
	}
	if r.EventSlug == nil || *r.EventSlug != evSlug {
		t.Errorf("a published event links its own page, got %v", r.EventSlug)
	}
	if r.OwnerReachable {
		t.Fatal("no owner yet, but owner_reachable is true")
	}

	// An operator of the platform who is an org_admin here is not the owner.
	nonce := uuid.NewString()[:8]
	operator := addOwner("operator-" + nonce + "@platform.example")
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
	        SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, operator); err != nil {
		t.Fatal(err)
	}
	if route().OwnerReachable {
		t.Error("a platform superadmin must never receive a buyer's message")
	}
	client := addOwner("client-" + nonce + "@organizer.example")
	if !route().OwnerReachable {
		t.Error("an ordinary owner next to the operator is reachable")
	}
	assertOwnerAddress(ctx, t, pool, orgID, "client-"+nonce+"@organizer.example")
	if _, err := pool.Exec(ctx, `UPDATE users SET deactivated_at = now() WHERE id = $1`, client); err != nil {
		t.Fatal(err)
	}
	if route().OwnerReachable {
		t.Error("a deactivated owner is not reachable")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET deactivated_at = NULL, anonymized_at = now() WHERE id = $1`, client); err != nil {
		t.Fatal(err)
	}
	if route().OwnerReachable {
		t.Error("an anonymized owner is not reachable")
	}

	// Taken off sale or deleted: the event page answers 404, so the letter
	// links the organization's (or promoter's) page instead.
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'draft' WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
	if r := route(); r.EventSlug != nil {
		t.Errorf("an event taken off sale must not be linked, got %q", *r.EventSlug)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'published', deleted_at = now() WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
	if r := route(); r.EventSlug != nil {
		t.Errorf("a deleted event must not be linked, got %q", *r.EventSlug)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET deleted_at = NULL WHERE id = $1`, eventID); err != nil {
		t.Fatal(err)
	}
}

// assertOwnerAddress checks the worker's recipient query picks that owner.
func assertOwnerAddress(ctx context.Context, t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, want string) {
	t.Helper()
	var got *string
	if err := pool.QueryRow(ctx, `SELECT `+gen.OwnerRecipientSQL+` FROM organizations o WHERE o.id = $1`, orgID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != want {
		t.Errorf("owner recipient = %v, want %s", got, want)
	}
}
