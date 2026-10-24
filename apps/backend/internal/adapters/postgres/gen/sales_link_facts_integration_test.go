//go:build integration

package gen_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salelinks"
)

// TestSalesLinkTargetFacts_LiveDB runs GetSalesLinkTargetFacts against a real
// schema and then the whole fall chain on the rows it reads (spec 43 4.3).
func TestSalesLinkTargetFacts_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set - skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 4))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var (
		orgA, orgB     = uuid.New(), uuid.New()
		venueID        = uuid.New()
		evtID, evtDraf = uuid.New(), uuid.New()
		sessLive       = uuid.New()
		sessCancelled  = uuid.New()
		sessDraftEvt   = uuid.New()
		promEvt, promL = uuid.New(), uuid.New()
		nonce          = orgA.String()[:8]
		slugA          = "ql2-a-" + nonce
		slugB          = "ql2-b-" + nonce
	)
	cleanup := func() {
		for _, step := range []struct {
			sql string
			arg uuid.UUID
		}{
			{`DELETE FROM event_promoters WHERE org_id = $1`, orgA},
			{`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`, orgA},
			{`DELETE FROM events WHERE org_id = $1`, orgA},
			{`DELETE FROM org_promoters WHERE org_id = $1`, orgA},
			{`DELETE FROM venues WHERE org_id = $1`, orgA},
			{`DELETE FROM organizations WHERE id = $1`, orgA},
			{`DELETE FROM organizations WHERE id = $1`, orgB},
		} {
			if _, err := pool.Exec(context.Background(), step.sql, step.arg); err != nil {
				t.Logf("cleanup %q: %v", step.sql, err)
			}
		}
	}
	defer cleanup()

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgA, "QL2 A "+nonce, slugA)
	exec(`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, orgB, "QL2 B "+nonce, slugB)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgA, "QL2 Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name, slug, status) VALUES ($1, $2, $3, $4, 'published')`, evtID, orgA, "QL2 Show "+nonce, "show-"+nonce)
	exec(`INSERT INTO events (id, org_id, name, slug, status) VALUES ($1, $2, $3, $4, 'draft')`, evtDraf, orgA, "QL2 Draft "+nonce, "draft-"+nonce)
	for _, s := range []struct {
		id, evt uuid.UUID
		status  string
	}{{sessLive, evtID, "scheduled"}, {sessCancelled, evtID, "cancelled"}, {sessDraftEvt, evtDraf, "scheduled"}} {
		exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source, status)
		      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'CZK', 'override', $4)`,
			s.id, s.evt, venueID, s.status)
	}
	exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, promEvt, orgA, "Stage "+nonce, "stage-"+nonce)
	exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, promL, orgA, "Club "+nonce, "club-"+nonce)
	exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, evtID, orgA, promEvt)

	q := gen.New(pool)
	get := func(org uuid.UUID, e, s, p *uuid.UUID) gen.SalesLinkTargetFactsRow {
		t.Helper()
		r, err := q.GetSalesLinkTargetFacts(ctx, org, e, s, p)
		if err != nil {
			t.Fatalf("GetSalesLinkTargetFacts: %v", err)
		}
		return r
	}

	t.Run("published event with a live session and promoters", func(t *testing.T) {
		r := get(orgA, &evtID, &sessLive, &promL)
		want := gen.SalesLinkTargetFactsRow{
			OrgSlug: slugA, EventSlug: "show-" + nonce, EventPublished: true,
			EventPromoterSlug: "stage-" + nonce, SessionLive: true, PromoterSlug: "club-" + nonce,
		}
		if r != want {
			t.Errorf("got %+v\nwant %+v", r, want)
		}
	})
	t.Run("event derived from the session when the event id is nil", func(t *testing.T) {
		r := get(orgA, nil, &sessLive, nil)
		if r.EventSlug != "show-"+nonce || !r.EventPublished || !r.SessionLive || r.EventPromoterSlug != "stage-"+nonce {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("cancelled session is not live", func(t *testing.T) {
		if r := get(orgA, &evtID, &sessCancelled, nil); r.SessionLive || !r.EventPublished {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("session soft-deleted is not live", func(t *testing.T) {
		exec(`UPDATE sessions SET deleted_at = now() WHERE id = $1`, sessLive)
		defer exec(`UPDATE sessions SET deleted_at = NULL WHERE id = $1`, sessLive)
		if r := get(orgA, &evtID, &sessLive, nil); r.SessionLive {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("draft event is not published", func(t *testing.T) {
		r := get(orgA, &evtDraf, &sessDraftEvt, nil)
		if r.EventPublished || r.EventSlug != "draft-"+nonce || !r.SessionLive {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("deleted event is not published", func(t *testing.T) {
		exec(`UPDATE events SET deleted_at = now() WHERE id = $1`, evtID)
		defer exec(`UPDATE events SET deleted_at = NULL WHERE id = $1`, evtID)
		if r := get(orgA, &evtID, nil, nil); r.EventPublished {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("archived promoters are reported archived", func(t *testing.T) {
		exec(`UPDATE org_promoters SET archived_at = now() WHERE id IN ($1, $2)`, promEvt, promL)
		defer exec(`UPDATE org_promoters SET archived_at = NULL WHERE id IN ($1, $2)`, promEvt, promL)
		r := get(orgA, &evtID, nil, &promL)
		if !r.EventPromoterArchived || !r.PromoterArchived || r.EventPromoterSlug == "" || r.PromoterSlug == "" {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("targets of another organization produce empty facts", func(t *testing.T) {
		r := get(orgB, &evtID, &sessLive, &promL)
		want := gen.SalesLinkTargetFactsRow{OrgSlug: slugB}
		if r != want {
			t.Errorf("got %+v, want only the organization slug", r)
		}
	})
	t.Run("no targets", func(t *testing.T) {
		if r := get(orgA, nil, nil, nil); r != (gen.SalesLinkTargetFactsRow{OrgSlug: slugA}) {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("unknown or deleted organization", func(t *testing.T) {
		if _, err := q.GetSalesLinkTargetFacts(ctx, uuid.New(), nil, nil, nil); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("unknown org: %v", err)
		}
		exec(`UPDATE organizations SET deleted_at = now() WHERE id = $1`, orgB)
		if _, err := q.GetSalesLinkTargetFacts(ctx, orgB, nil, nil, nil); !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("deleted org: %v", err)
		}
	})

	t.Run("the whole chain on real rows", func(t *testing.T) {
		const base = "https://tickets.example"
		link := func(kind string) salelinks.Link {
			l := salelinks.Link{OrgID: orgA, Code: "x7k2qpm", Target: salelinks.Target{Kind: kind}}
			switch kind {
			case salelinks.KindSession:
				l.Target.EventID, l.Target.SessionID = &evtID, &sessLive
			case salelinks.KindEvent:
				l.Target.EventID = &evtID
			case salelinks.KindPromoterPage:
				l.Target.PromoterID = &promL
			}
			return l
		}
		check := func(name string, l salelinks.Link, want string, wantDeg bool) {
			t.Helper()
			got, deg, err := salelinks.Resolve(ctx, q, base, l)
			if err != nil || got != want || deg != wantDeg {
				t.Errorf("%s: got (%q, %v, %v), want (%q, %v)", name, got, deg, err, want, wantDeg)
			}
		}
		check("session", link(salelinks.KindSession), base+"/stage-"+nonce+"/show-"+nonce+"?session="+sessLive.String(), false)
		check("event", link(salelinks.KindEvent), base+"/stage-"+nonce+"/show-"+nonce, false)
		check("promoter", link(salelinks.KindPromoterPage), base+"/club-"+nonce, false)
		check("org", link(salelinks.KindOrgPage), base+"/"+slugA, false)

		// The event goes back to draft: the chain falls to the promoter of the event.
		exec(`UPDATE events SET status = 'draft' WHERE id = $1`, evtID)
		check("session of a draft event", link(salelinks.KindSession), base+"/stage-"+nonce, true)
		check("event that is a draft", link(salelinks.KindEvent), base+"/stage-"+nonce, true)
		exec(`UPDATE org_promoters SET archived_at = now() WHERE id = $1`, promEvt)
		check("draft event, promoter archived", link(salelinks.KindEvent), base+"/"+slugA, true)
		exec(`UPDATE org_promoters SET archived_at = now() WHERE id = $1`, promL)
		check("archived promoter page", link(salelinks.KindPromoterPage), base+"/"+slugA, true)
		archived := link(salelinks.KindSession)
		archived.Archived = true
		check("archived link", archived, base+"/"+slugA, true)
	})
}
