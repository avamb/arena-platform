//go:build integration

// contact_route_integration_test.go — GetTicketContactRoute on a live
// database: which owner may receive the contact form's messages (never a
// platform superadmin, never a deactivated or anonymized account), the event
// page is linked only while it answers, and "hosted page enabled" is read
// the way the page reads it.

package delivery

import (
	"context"
	"strings"
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
	feedToken := uuid.New()
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM event_publications WHERE feed_token_id = $1`, feedToken)
		_, _ = pool.Exec(c, `DELETE FROM agent_feed_tokens WHERE id = $1`, feedToken)
		_, _ = pool.Exec(c, `DELETE FROM user_roles WHERE user_id = ANY($1)`, users)
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
	// Published but on no feed token: the event page answers 404.
	if r.EventSlug != nil {
		t.Errorf("an event with no publication must not be linked, got %q", *r.EventSlug)
	}
	publish := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`, eventID, feedToken); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label, is_active)
	        VALUES ($1, $2, $3, 'contact route', true)`, feedToken, "tok-"+uuid.NewString(), channelID); err != nil {
		t.Fatal(err)
	}
	publish()
	if r := route(); r.EventSlug == nil || *r.EventSlug != evSlug {
		t.Errorf("a published event links its own page, got %v", r.EventSlug)
	}
	// Unpublished the way HandleUnpublishEvent does it: the status stays
	// 'published' but the page has nothing to show.
	if _, err := pool.Exec(ctx, `DELETE FROM event_publications WHERE event_id = $1 AND feed_token_id = $2`, eventID, feedToken); err != nil {
		t.Fatal(err)
	}
	if r := route(); r.EventSlug != nil {
		t.Errorf("an unpublished event must not be linked, got %q", *r.EventSlug)
	}
	publish()
	if _, err := pool.Exec(ctx, `UPDATE agent_feed_tokens SET is_active = false WHERE id = $1`, feedToken); err != nil {
		t.Fatal(err)
	}
	if r := route(); r.EventSlug != nil {
		t.Errorf("an event published only on a revoked token must not be linked, got %q", *r.EventSlug)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_feed_tokens SET is_active = true WHERE id = $1`, feedToken); err != nil {
		t.Fatal(err)
	}
	if r.OwnerReachable {
		t.Fatal("no owner yet, but owner_reachable is true")
	}

	// A person with any platform-wide role who is an org_admin here is not the
	// owner: the superadmin, the operator, the admin, support.
	nonce := uuid.NewString()[:8]
	for _, role := range []string{"platform_superadmin", "platform_operator", "admin", "support"} {
		staff := addOwner(role + "-" + nonce + "@platform.example")
		if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		        SELECT $1, id, NULL FROM roles WHERE name = $2 AND org_id IS NULL`, staff, role); err != nil {
			t.Fatal(err)
		}
		if route().OwnerReachable {
			t.Errorf("a %s must never receive a buyer's message", role)
		}
	}
	client := addOwner("client-" + nonce + "@organizer.example")
	// A global row of a membership role (an old way to hand out org_admin) is
	// not a platform role: that owner stays reachable.
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
	        SELECT $1, id, NULL FROM roles WHERE name = 'org_admin' AND org_id IS NULL`, client); err != nil {
		t.Fatal(err)
	}
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

// Owner decision 2026-10-14: the form's messages about an event of a
// promoter go to the organization's owner only. to_owner follows the same
// rule as the page and the send route (gen.EventPromotedSQL): an active
// promoter of the SAME organization; an archived one no longer counts.
func TestTicketContactRoute_PromoterEventGoesToTheOwner(t *testing.T) {
	pool := presentationPool(t)
	ctx := context.Background()
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	defer cleanup()
	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT org_id FROM events WHERE id = $1`, seed.EventID).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	q := gen.New(pool)
	toOwner := func() bool {
		t.Helper()
		r, err := q.GetTicketContactRoute(ctx, seed.TicketID)
		if err != nil {
			t.Fatal(err)
		}
		return r.ToOwner
	}
	if toOwner() {
		t.Fatal("an event without a promoter must keep the organization's chain")
	}
	var promoterID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO org_promoters (org_id, name, slug, email) VALUES ($1, $2, $3, 'promoter@lumen.example') RETURNING id`,
		orgID, "Lumen "+uuid.NewString()[:8], "lumen-"+uuid.NewString()[:8]).Scan(&promoterID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM event_promoters WHERE event_id = $1`, seed.EventID)
		_, _ = pool.Exec(c, `DELETE FROM org_promoters WHERE id = $1`, promoterID)
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, seed.EventID, orgID, promoterID); err != nil {
		t.Fatal(err)
	}
	if !toOwner() {
		t.Error("an event of a promoter must send the form's messages to the owner")
	}
	slug := "ev-promo-" + uuid.NewString()[:8]
	if _, err := pool.Exec(ctx, `UPDATE events SET slug = $2 WHERE id = $1`, seed.EventID, slug); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.GetContactEventPromoted(ctx, orgID, strings.ToUpper(slug)); err != nil || !ok {
		t.Errorf("the send route's rule must agree with the letters' (slug matched case-insensitively): %v %v", ok, err)
	}
	if ok, err := q.GetContactEventPromoted(ctx, uuid.New(), slug); err != nil || ok {
		t.Errorf("another organization never sees this event as its own: %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE org_promoters SET archived_at = now() WHERE id = $1`, promoterID); err != nil {
		t.Fatal(err)
	}
	if toOwner() {
		t.Error("an archived promoter no longer sends the messages to the owner")
	}
	if ok, err := q.GetContactEventPromoted(ctx, orgID, slug); err != nil || ok {
		t.Errorf("the send route must agree on an archived promoter: %v %v", ok, err)
	}
}
