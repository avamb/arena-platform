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
)

// TestSessionExternalRefs_W1E1a_LiveDB exercises migration 0099 and the
// three hand-written wrappers in session_external_refs.sql.go end to end
// (feature #523, event-bundle spec §6):
//
//   - a fresh session has no ref (pgx.ErrNoRows from both lookups);
//   - InsertSessionExternalRef binds a ref, and both lookups then agree;
//   - GetSessionByExternalRef also returns the session's event_id;
//   - the composite PK (org_id, external_ref) rejects the same ref twice in
//     one organization, even pointing at a different session;
//   - session_external_refs_session_uq rejects a second ref for a session
//     already bound;
//   - the SAME ref string is accepted in a DIFFERENT organization;
//   - a ref from another org is invisible to this org's lookup (no leak);
//   - the length CHECK rejects an empty ref and a 201-character ref while
//     accepting a 200-character one;
//   - deleting the session cascades the ref row away.
func TestSessionExternalRefs_W1E1a_LiveDB(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, mustPoolConfig(t, dsn, 8))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	// Two independent org fixtures so the cross-org uniqueness assertions
	// are real, plus a second session in org A for the PK collision case.
	a := createSessionExternalRefFixture(t, ctx, pool)
	defer a.cleanup()
	b := createSessionExternalRefFixture(t, ctx, pool)
	defer b.cleanup()

	q := gen.New(pool)

	// Randomised per run: this table has no org-global unique index, but the
	// dev stand is shared and leftovers from an interrupted run would still
	// collide on (org_id, external_ref) if the ref were a fixed literal and
	// the org were reused. Deriving it from the fixture UUID keeps runs
	// independent.
	ref := "wp:test-" + a.orgID.String() + ":product:4711"

	// Step 1: nothing bound yet.
	if _, err := q.GetExternalRefBySession(ctx, a.sessionID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetExternalRefBySession(unbound): got %v, want pgx.ErrNoRows", err)
	}
	if _, _, err := q.GetSessionByExternalRef(ctx, a.orgID, ref); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetSessionByExternalRef(unknown): got %v, want pgx.ErrNoRows", err)
	}

	// Step 2: bind it, then both lookups agree.
	if err := q.InsertSessionExternalRef(ctx, a.orgID, ref, a.sessionID); err != nil {
		t.Fatalf("InsertSessionExternalRef: %v", err)
	}
	gotSession, gotEvent, err := q.GetSessionByExternalRef(ctx, a.orgID, ref)
	if err != nil {
		t.Fatalf("GetSessionByExternalRef: %v", err)
	}
	if gotSession != a.sessionID {
		t.Errorf("GetSessionByExternalRef session: got %s, want %s", gotSession, a.sessionID)
	}
	if gotEvent != a.eventID {
		t.Errorf("GetSessionByExternalRef event: got %s, want %s", gotEvent, a.eventID)
	}
	gotRef, err := q.GetExternalRefBySession(ctx, a.sessionID)
	if err != nil {
		t.Fatalf("GetExternalRefBySession: %v", err)
	}
	if gotRef != ref {
		t.Errorf("GetExternalRefBySession: got %q, want %q", gotRef, ref)
	}

	// Step 3: the composite PK refuses the same ref twice in this org, even
	// when it points at a different session of the same org.
	if err := q.InsertSessionExternalRef(ctx, a.orgID, ref, a.sessionID); err == nil {
		t.Errorf("duplicate (org_id, external_ref) for the same session was accepted")
	}
	if err := q.InsertSessionExternalRef(ctx, a.orgID, ref, a.session2ID); err == nil {
		t.Errorf("duplicate (org_id, external_ref) for another session was accepted")
	}

	// Step 4: one session may carry only one ref.
	if err := q.InsertSessionExternalRef(ctx, a.orgID, ref+":alt", a.sessionID); err == nil {
		t.Errorf("session_external_refs_session_uq: second ref for one session was accepted")
	}

	// Step 5: the very same ref string is free in another organization.
	if err := q.InsertSessionExternalRef(ctx, b.orgID, ref, b.sessionID); err != nil {
		t.Fatalf("same ref in another org was rejected: %v", err)
	}
	otherSession, otherEvent, err := q.GetSessionByExternalRef(ctx, b.orgID, ref)
	if err != nil {
		t.Fatalf("GetSessionByExternalRef(org B): %v", err)
	}
	if otherSession != b.sessionID || otherEvent != b.eventID {
		t.Errorf("org B lookup: got (%s, %s), want (%s, %s)",
			otherSession, otherEvent, b.sessionID, b.eventID)
	}
	// … and org A still resolves to its own session, not org B's.
	gotSession, _, err = q.GetSessionByExternalRef(ctx, a.orgID, ref)
	if err != nil {
		t.Fatalf("GetSessionByExternalRef(org A after org B insert): %v", err)
	}
	if gotSession != a.sessionID {
		t.Errorf("cross-tenant leak: org A ref resolved to %s, want %s", gotSession, a.sessionID)
	}
	// A ref that only org B knows is invisible to a third org id.
	if _, _, err := q.GetSessionByExternalRef(ctx, uuid.New(), ref); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetSessionByExternalRef(foreign org): got %v, want pgx.ErrNoRows", err)
	}

	// Step 6: the length CHECK (1..200).
	if err := q.InsertSessionExternalRef(ctx, a.orgID, "", a.session2ID); err == nil {
		t.Errorf("length CHECK: empty external_ref was accepted")
	}
	tooLong := make([]byte, 201)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	if err := q.InsertSessionExternalRef(ctx, a.orgID, string(tooLong), a.session2ID); err == nil {
		t.Errorf("length CHECK: 201-character external_ref was accepted")
	}
	if err := q.InsertSessionExternalRef(ctx, a.orgID, string(tooLong[:200]), a.session2ID); err != nil {
		t.Errorf("length CHECK: 200-character external_ref was rejected: %v", err)
	}

	// Step 7: ON DELETE CASCADE from sessions.
	if _, err := pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, a.sessionID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	a.sessionDeleted = true
	if _, _, err := q.GetSessionByExternalRef(ctx, a.orgID, ref); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("ON DELETE CASCADE: ref survived the session delete (err=%v)", err)
	}
}

// sessionExternalRefFixture seeds organization → venue → event → two
// sessions, the minimum row graph session_external_refs references.
type sessionExternalRefFixture struct {
	t              *testing.T
	pool           *pgxpool.Pool
	orgID          uuid.UUID
	venueID        uuid.UUID
	eventID        uuid.UUID
	sessionID      uuid.UUID
	session2ID     uuid.UUID
	sessionDeleted bool
}

func createSessionExternalRefFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *sessionExternalRefFixture {
	f := &sessionExternalRefFixture{
		t: t, pool: pool,
		orgID:      uuid.New(),
		venueID:    uuid.New(),
		eventID:    uuid.New(),
		sessionID:  uuid.New(),
		session2ID: uuid.New(),
	}
	suffix := f.orgID.String()[:8]
	const insertSession = `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at,
	    capacity_total, status, admission_mode, currency, currency_source)
	  VALUES ($1, $2, $3, now() + interval '30 days',
	    now() + interval '30 days 3 hours', 100, 'draft',
	    'general_admission', 'EUR', 'override')`
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
			[]any{f.orgID, "SER Org " + suffix, "ser-" + suffix}},
		{`INSERT INTO venues (id, org_id, name) VALUES ($1, $2, $3)`,
			[]any{f.venueID, f.orgID, "SER Venue " + suffix}},
		{`INSERT INTO events (id, org_id, name, status, visibility)
		  VALUES ($1, $2, $3, 'draft', 'private')`,
			[]any{f.eventID, f.orgID, "SER Event " + suffix}},
		{insertSession, []any{f.sessionID, f.eventID, f.venueID}},
		{insertSession, []any{f.session2ID, f.eventID, f.venueID}},
	}
	for i, s := range steps {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			f.cleanup()
			t.Fatalf("session_external_refs fixture step %d failed: %v", i, err)
		}
	}
	return f
}

func (f *sessionExternalRefFixture) cleanup() {
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM session_external_refs WHERE org_id = $1`, f.orgID); err != nil {
		f.t.Logf("cleanup session_external_refs: %v", err)
	}
	if !f.sessionDeleted {
		if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, f.sessionID); err != nil {
			f.t.Logf("cleanup sessions: %v", err)
		}
	}
	for _, step := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM sessions WHERE id = $1`, f.session2ID},
		{`DELETE FROM events WHERE id = $1`, f.eventID},
		{`DELETE FROM venues WHERE id = $1`, f.venueID},
		{`DELETE FROM organizations WHERE id = $1`, f.orgID},
	} {
		if _, err := f.pool.Exec(ctx, step.sql, step.arg); err != nil {
			f.t.Logf("cleanup: %v", err)
		}
	}
}
