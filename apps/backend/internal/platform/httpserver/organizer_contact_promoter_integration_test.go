//go:build integration

package httpserver

// Owner decision 2026-10-14: a message from a promoter's page, or about an
// event of a promoter on the organization's own page, goes to the
// organization's owner only - never to the sales page's public e-mail, the
// organization's contact_email or the promoter's e-mail, and never to the
// owner of another organization. The page and the answer never carry the
// owner's address, and a promoter's page without an owner offers no form.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/contactform"
)

func TestOrganizerContact_PromoterPageGoesToTheOwner(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	srv := buildBotIntegrationServer(t, pool)
	cf := newContactFixture(t, srv, true)
	other := newContactFixture(t, srv, true)
	ctx := context.Background()
	const orgPrivate = "box-private@organizer.example"
	const promoterEmail = "promoter-hpform@lumen.example"
	owner := strings.ToLower(cf.f.emails[0])
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE organizations SET contact_email = $2 WHERE id = $1`, cf.f.orgID, orgPrivate)

	suffix := uuid.NewString()[:8]
	promoterSlug := "lumen-hpform-" + suffix
	promoterID, foreignPromoter := uuid.New(), uuid.New()
	eventID, plainEventID, foreignEventID := uuid.New(), uuid.New(), uuid.New()
	eventSlug, plainSlug, foreignSlug := "promoted-"+suffix, "plain-"+suffix, "foreign-"+suffix
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM event_promoters WHERE event_id = ANY($1)`, []uuid.UUID{eventID, foreignEventID})
		_, _ = pool.Exec(c, `DELETE FROM org_promoters WHERE id = ANY($1)`, []uuid.UUID{promoterID, foreignPromoter})
		_, _ = pool.Exec(c, `DELETE FROM events WHERE id = ANY($1)`, []uuid.UUID{eventID, plainEventID, foreignEventID})
	})
	exec(`INSERT INTO org_promoters (id, org_id, name, slug, email) VALUES ($1, $2, $3, $4, $5)`,
		promoterID, cf.f.orgID, "Lumen "+suffix, promoterSlug, promoterEmail)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, 'Promoted show', 'published', 'public', $3)`,
		eventID, cf.f.orgID, eventSlug)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, 'Own show', 'published', 'public', $3)`,
		plainEventID, cf.f.orgID, plainSlug)
	exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, eventID, cf.f.orgID, promoterID)
	// Another organization's event of its own promoter: naming its slug on
	// cf's page must not change cf's recipient, nor reach the other owner.
	exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`,
		foreignPromoter, other.f.orgID, "Foreign "+suffix, "foreign-hpform-"+suffix)
	exec(`INSERT INTO events (id, org_id, name, status, visibility, slug) VALUES ($1, $2, 'Foreign show', 'published', 'public', $3)`,
		foreignEventID, other.f.orgID, foreignSlug)
	exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, foreignEventID, other.f.orgID, foreignPromoter)

	mail := &captureMail{}
	h := contactform.New(contactform.Options{Pool: pool, Sender: mail})
	// send posts one message and runs its job the way the worker does: with
	// the payload the route queued. It answers the route's status and body
	// and, for a queued message, the address the letter went to.
	send := func(slug, evSlug, ip string) (int, string, string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{
			"name": "Anna Buyer", "email": "anna@example.com", "message": "Is the venue accessible by wheelchair?",
			"consent": true, "event_slug": evSlug, "locale": "es",
		})
		code, body := contactPost(t, srv.router, slug, string(b), ip)
		if code != http.StatusAccepted {
			return code, body, ""
		}
		var payload []byte
		if err := pool.QueryRow(ctx, `SELECT j.payload FROM worker_jobs j
		        JOIN organizer_contact_messages m ON j.payload->>'message_id' = m.id::text
		        WHERE m.org_id = $1 AND j.job_type = $2 ORDER BY m.created_at DESC LIMIT 1`, cf.f.orgID, contactform.JobType).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		before := len(mail.sent)
		if err := h.Handle(ctx, payload); err != nil {
			t.Fatal(err)
		}
		if len(mail.sent) != before+1 {
			t.Fatalf("one letter expected, got %d", len(mail.sent)-before)
		}
		return code, body, strings.ToLower(mail.sent[len(mail.sent)-1].To)
	}
	noAddress := func(name, body string) {
		t.Helper()
		for _, a := range []string{owner, contactOrgEmail, orgPrivate, promoterEmail, strings.ToLower(other.f.emails[0])} {
			if strings.Contains(strings.ToLower(body), a) {
				t.Fatalf("%s: the answer carries an address (%s): %s", name, a, body)
			}
		}
	}
	page := func(path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}

	// ── the promoter's page offers the form, and it goes to the owner ────────
	code, body := page("/v1/public/pages/" + promoterSlug)
	if code != http.StatusOK || !strings.Contains(body, `"contact_form":{"available":true`) {
		t.Fatalf("promoter page: %d %s", code, body)
	}
	noAddress("promoter page", body)
	code, body, to := send(promoterSlug, "", "198.51.100.60")
	noAddress("promoter send", body)
	if code != http.StatusAccepted || to != owner {
		t.Fatalf("promoter page message: %d %s, mailed to %q, want the owner", code, body, to)
	}
	// ── an event of the promoter on the organization's page: the owner too ───
	if code, body, to := send(cf.slug, eventSlug, "198.51.100.61"); code != http.StatusAccepted || to != owner {
		t.Fatalf("promoter's event on the organization's page: %d %s, mailed to %q", code, body, to)
	}
	// ── the organization's own pages keep the chain: the public e-mail ───────
	if code, body, to := send(cf.slug, plainSlug, "198.51.100.62"); code != http.StatusAccepted || to != contactOrgEmail {
		t.Fatalf("own event: %d %s, mailed to %q", code, body, to)
	}
	if code, body, to := send(cf.slug, "", "198.51.100.63"); code != http.StatusAccepted || to != contactOrgEmail {
		t.Fatalf("landing page: %d %s, mailed to %q", code, body, to)
	}
	// Another organization's promoted event named on cf's page changes nothing.
	if code, body, to := send(cf.slug, foreignSlug, "198.51.100.64"); code != http.StatusAccepted || to != contactOrgEmail {
		t.Fatalf("foreign event slug: %d %s, mailed to %q", code, body, to)
	}
	for _, m := range mail.sent {
		if strings.EqualFold(m.To, promoterEmail) || strings.EqualFold(m.To, other.f.emails[0]) || strings.EqualFold(m.To, orgPrivate) {
			t.Fatalf("a message reached %s", m.To)
		}
	}

	// ── no owner: the promoter's page offers no form and refuses with 404 ────
	exec(`UPDATE memberships SET status = 'suspended' WHERE org_id = $1 AND role = 'org_admin'`, cf.f.orgID)
	if code, body := page("/v1/public/pages/" + promoterSlug); code != http.StatusOK || strings.Contains(body, "contact_form") {
		t.Errorf("a promoter's page without an owner must not offer the form: %d %s", code, body)
	}
	for name, c := range map[string][2]string{
		"promoter page":        {promoterSlug, ""},
		"promoter's event":     {cf.slug, eventSlug},
		"promoter's own event": {promoterSlug, eventSlug},
	} {
		if code, body, _ := send(c[0], c[1], "198.51.100.65"); code != http.StatusNotFound || !strings.Contains(body, "contact.not_available") {
			t.Errorf("%s without an owner: %d %s", name, code, body)
		}
	}
	// The organization's own page still has its public e-mail.
	if code, body, to := send(cf.slug, plainSlug, "198.51.100.66"); code != http.StatusAccepted || to != contactOrgEmail {
		t.Errorf("own event without an owner: %d %s, mailed to %q", code, body, to)
	}
}
