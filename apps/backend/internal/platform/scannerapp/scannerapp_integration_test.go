//go:build integration

// scannerapp_integration_test.go — the scanner app store (migration 0227,
// spec 69 MA-10) against a migrated database: every Authenticate refusal,
// organization isolation of passes and devices, max_devices and Describe.
//
// Skips when DATABASE_URL is unset.
package scannerapp_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

func livePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping live DB integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// orgFix is an organization with one event and two dates, one category each.
type orgFix struct {
	org, venue, event uuid.UUID
	session, session2 uuid.UUID
	tier, tier2       uuid.UUID
}

func newOrg(t *testing.T, pool *pgxpool.Pool) orgFix {
	t.Helper()
	f := orgFix{
		org: uuid.New(), venue: uuid.New(), event: uuid.New(),
		session: uuid.New(), session2: uuid.New(), tier: uuid.New(), tier2: uuid.New(),
	}
	nonce := f.org.String()[:8]
	t.Cleanup(func() {
		ctx := context.Background()
		for _, s := range []string{
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM venues WHERE org_id = $1`,
			`DELETE FROM organizations WHERE id = $1`,
		} {
			if _, err := pool.Exec(ctx, s, f.org); err != nil {
				t.Logf("cleanup %q: %v", s, err)
			}
		}
	})
	mustExec(t, pool, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`, f.org, "Scanner Org "+nonce, "scanner-"+nonce)
	mustExec(t, pool, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, f.venue, f.org, "Scanner Hall "+nonce)
	mustExec(t, pool, `INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, f.event, f.org, "Scanner Event "+nonce)
	for i, ids := range [][2]uuid.UUID{{f.session, f.tier}, {f.session2, f.tier2}} {
		mustExec(t, pool, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
		      VALUES ($1, $2, $3, now() + make_interval(days => 10 + $4::int), now() + make_interval(days => 10 + $4::int, hours => 2), 100, 'EUR', 'override')`,
			ids[0], f.event, f.venue, i)
		mustExec(t, pool, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, currency) VALUES ($1, $2, 'General', 'fixed', 'EUR')`,
			ids[1], ids[0])
	}
	return f
}

func newUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, pool, `INSERT INTO users (id, email, password_hash, email_verified_at) VALUES ($1, $2, 'x', now())`,
		id, "scanner+"+id.String()[:12]+"@example.com")
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})
	return id
}

func addMember(t *testing.T, pool *pgxpool.Pool, userID, orgID uuid.UUID, role string) {
	t.Helper()
	mustExec(t, pool, `INSERT INTO memberships (user_id, org_id, role, status) VALUES ($1, $2, $3, 'active')`, userID, orgID, role)
}

// insertDevice writes a device row directly, bypassing the checks of the
// creation helpers, and returns its raw token.
func insertDevice(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, userID, passID *uuid.UUID, now time.Time) (uuid.UUID, string, error) {
	t.Helper()
	raw, hash, err := scannerapp.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	id, err := gen.New(pool).InsertScannerDevice(context.Background(), gen.InsertScannerDeviceParams{
		OrgID: orgID, UserID: userID, PassID: passID, Platform: scannerapp.PlatformAndroid,
		AccessTokenHash: hash, AccessExpiresAt: now.Add(scannerapp.AccessTokenTTL), CreatedAt: now,
	})
	return id, raw, err
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func testNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func TestAuthenticate_OrganizerDevice_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()
	a := newOrg(t, pool)
	b := newOrg(t, pool)

	// organizerDevice makes a person with an active organizer membership in
	// org A and a device of that person.
	organizerDevice := func(t *testing.T) (uuid.UUID, scannerapp.Device, string) {
		t.Helper()
		u := newUser(t, pool)
		addMember(t, pool, u, a.org, "organizer")
		d, raw, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: a.org, UserID: u, Label: "Door phone", Platform: scannerapp.PlatformIOS,
		}, now)
		if err != nil {
			t.Fatalf("CreateOrganizerDevice: %v", err)
		}
		return u, d, raw
	}
	expect := func(t *testing.T, raw string, at time.Time, want error) {
		t.Helper()
		_, err := scannerapp.Authenticate(ctx, pool, raw, at)
		if !errors.Is(err, want) {
			t.Fatalf("Authenticate err = %v, want %v", err, want)
		}
	}

	t.Run("ok", func(t *testing.T) {
		u, created, raw := organizerDevice(t)
		for _, role := range []string{"organizer", "org_admin"} {
			mustExec(t, pool, `UPDATE memberships SET role = $3 WHERE user_id = $1 AND org_id = $2`, u, a.org, role)
			d, err := scannerapp.Authenticate(ctx, pool, raw, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("%s: Authenticate: %v", role, err)
			}
			if d.ID != created.ID || d.OrgID != a.org || d.Kind != scannerapp.KindOrganizer ||
				d.UserID == nil || *d.UserID != u || d.PassID != nil || d.Label != "Door phone" || d.Platform != "ios" {
				t.Fatalf("%s: device = %+v", role, d)
			}
		}
	})
	t.Run("membership removed", func(t *testing.T) {
		u, _, raw := organizerDevice(t)
		mustExec(t, pool, `DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, u, a.org)
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("membership suspended", func(t *testing.T) {
		u, _, raw := organizerDevice(t)
		mustExec(t, pool, `UPDATE memberships SET status = 'suspended' WHERE user_id = $1 AND org_id = $2`, u, a.org)
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("membership role without scan.perform", func(t *testing.T) {
		u, _, raw := organizerDevice(t)
		mustExec(t, pool, `UPDATE memberships SET role = 'agent' WHERE user_id = $1 AND org_id = $2`, u, a.org)
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("user deactivated", func(t *testing.T) {
		u, _, raw := organizerDevice(t)
		mustExec(t, pool, `UPDATE users SET deactivated_at = now() WHERE id = $1`, u)
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("platform superadmin without membership", func(t *testing.T) {
		u := newUser(t, pool)
		mustExec(t, pool, `INSERT INTO user_roles (user_id, role_id, org_id)
		      SELECT $1, r.id, NULL FROM roles r WHERE r.name = 'platform_superadmin' AND r.org_id IS NULL`, u)
		_, _, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: a.org, UserID: u, Platform: scannerapp.PlatformOther,
		}, now)
		if !errors.Is(err, scannerapp.ErrAccessEnded) {
			t.Fatalf("CreateOrganizerDevice err = %v, want ErrAccessEnded", err)
		}
		_, raw, err := insertDevice(t, pool, a.org, &u, nil, now)
		if err != nil {
			t.Fatalf("insert device: %v", err)
		}
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("membership row with a platform role", func(t *testing.T) {
		// A platform role does not act inside an organization, even on a
		// membership row that names it (GetActiveRolesForUserInOrg rule).
		for _, role := range []string{"platform_superadmin", "platform_operator"} {
			u := newUser(t, pool)
			addMember(t, pool, u, a.org, role)
			_, _, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
				OrgID: a.org, UserID: u, Platform: scannerapp.PlatformOther,
			}, now)
			if !errors.Is(err, scannerapp.ErrAccessEnded) {
				t.Fatalf("%s: CreateOrganizerDevice err = %v, want ErrAccessEnded", role, err)
			}
		}
	})
	t.Run("membership role changed to a platform role", func(t *testing.T) {
		for _, role := range []string{"platform_superadmin", "platform_operator"} {
			u := newUser(t, pool)
			addMember(t, pool, u, a.org, "org_admin")
			_, raw, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
				OrgID: a.org, UserID: u, Platform: scannerapp.PlatformIOS,
			}, now)
			if err != nil {
				t.Fatalf("%s: CreateOrganizerDevice: %v", role, err)
			}
			expect(t, raw, now, nil)
			mustExec(t, pool, `UPDATE memberships SET role = $3 WHERE user_id = $1 AND org_id = $2`, u, a.org, role)
			expect(t, raw, now, scannerapp.ErrAccessEnded)
		}
	})
	t.Run("membership in another organization", func(t *testing.T) {
		u := newUser(t, pool)
		addMember(t, pool, u, b.org, "org_admin")
		_, raw, err := insertDevice(t, pool, a.org, &u, nil, now)
		if err != nil {
			t.Fatalf("insert device: %v", err)
		}
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("device revoked", func(t *testing.T) {
		u, d, raw := organizerDevice(t)
		ok, err := scannerapp.RevokeDevice(ctx, pool, a.org, d.ID, &u, "lost phone", now)
		if err != nil || !ok {
			t.Fatalf("RevokeDevice = %v, %v", ok, err)
		}
		if ok, _ := scannerapp.RevokeDevice(ctx, pool, a.org, d.ID, &u, "", now); ok {
			t.Error("second RevokeDevice reported a write")
		}
		expect(t, raw, now, scannerapp.ErrRevoked)
	})
	t.Run("revoke from another organization does nothing", func(t *testing.T) {
		_, d, raw := organizerDevice(t)
		if ok, err := scannerapp.RevokeDevice(ctx, pool, b.org, d.ID, nil, "", now); err != nil || ok {
			t.Fatalf("RevokeDevice from org B = %v, %v", ok, err)
		}
		expect(t, raw, now, nil)
	})
	t.Run("access expired", func(t *testing.T) {
		_, _, raw := organizerDevice(t)
		expect(t, raw, now.Add(scannerapp.AccessTokenTTL-time.Second), nil)
		expect(t, raw, now.Add(scannerapp.AccessTokenTTL), scannerapp.ErrExpired)
	})
	t.Run("organization archived", func(t *testing.T) {
		c := newOrg(t, pool)
		u := newUser(t, pool)
		addMember(t, pool, u, c.org, "organizer")
		_, raw, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: c.org, UserID: u, Platform: scannerapp.PlatformAndroid,
		}, now)
		if err != nil {
			t.Fatalf("CreateOrganizerDevice: %v", err)
		}
		expect(t, raw, now, nil)
		mustExec(t, pool, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, c.org)
		expect(t, raw, now, scannerapp.ErrAccessEnded)
	})
	t.Run("unknown token", func(t *testing.T) {
		raw, _, err := scannerapp.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		expect(t, raw, now, scannerapp.ErrUnknown)
	})
	t.Run("malformed token", func(t *testing.T) {
		expect(t, "ak_live_something", now, scannerapp.ErrMalformed)
		expect(t, "", now, scannerapp.ErrMalformed)
	})
	t.Run("touch last seen once a minute", func(t *testing.T) {
		_, d, raw := organizerDevice(t)
		for i, step := range []struct {
			at   time.Time
			want bool
		}{
			{now, true},
			{now.Add(30 * time.Second), false},
			{now.Add(61 * time.Second), true},
		} {
			got, err := scannerapp.TouchLastSeen(ctx, pool, d.ID, step.at)
			if err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			if got != step.want {
				t.Fatalf("step %d: wrote = %v, want %v", i, got, step.want)
			}
		}
		got, err := scannerapp.Authenticate(ctx, pool, raw, now.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if got.LastSeenAt == nil || !got.LastSeenAt.Equal(now.Add(61*time.Second)) {
			t.Fatalf("LastSeenAt = %v", got.LastSeenAt)
		}
	})
}

func TestPasses_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()
	a := newOrg(t, pool)
	b := newOrg(t, pool)
	admin := newUser(t, pool)
	addMember(t, pool, admin, a.org, "org_admin")

	standard := func(label string, from, until time.Time, maxDevices int, sessions ...scannerapp.CreatePassSession) scannerapp.CreatePassParams {
		return scannerapp.CreatePassParams{
			OrgID: a.org, Label: label, Kind: scannerapp.PassKindStandard,
			ValidFrom: ptr(from), ValidUntil: ptr(until), MaxDevices: maxDevices,
			CreatedBy: &admin, Sessions: sessions,
		}
	}
	passCount := func(t *testing.T, label string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM scanner_passes WHERE org_id = $1 AND label = $2`, a.org, label).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	newPassDevice := func(t *testing.T, passID uuid.UUID) (scannerapp.Device, string, error) {
		t.Helper()
		return scannerapp.CreatePassDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: a.org, PassID: passID, Label: "Gate 1", Platform: scannerapp.PlatformAndroid,
		}, now)
	}

	t.Run("pass cannot take a session of another organization", func(t *testing.T) {
		for name, sessions := range map[string][]scannerapp.CreatePassSession{
			"foreign only": {{SessionID: b.session}},
			"mixed":        {{SessionID: a.session}, {SessionID: b.session}},
		} {
			label := "Foreign " + name
			_, err := scannerapp.CreatePass(ctx, pool, standard(label, now, now.Add(time.Hour), 1, sessions...), now)
			if !errors.Is(err, scannerapp.ErrForeignSession) {
				t.Fatalf("%s: err = %v, want ErrForeignSession", name, err)
			}
			if n := passCount(t, label); n != 0 {
				t.Fatalf("%s: %d passes written", name, n)
			}
		}
	})
	t.Run("pass cannot take a category of another session", func(t *testing.T) {
		for name, s := range map[string]scannerapp.CreatePassSession{
			"tier of another organization": {SessionID: a.session, TierIDs: []uuid.UUID{b.tier}},
			"tier of another date":         {SessionID: a.session, TierIDs: []uuid.UUID{a.tier2}},
		} {
			label := "Foreign " + name
			_, err := scannerapp.CreatePass(ctx, pool, standard(label, now, now.Add(time.Hour), 1, s), now)
			if !errors.Is(err, scannerapp.ErrForeignTier) {
				t.Fatalf("%s: err = %v, want ErrForeignTier", name, err)
			}
			if n := passCount(t, label); n != 0 {
				t.Fatalf("%s: %d passes written", name, n)
			}
		}
	})
	t.Run("invalid pass", func(t *testing.T) {
		for name, p := range map[string]scannerapp.CreatePassParams{
			"blank label":        standard("  ", now, now.Add(time.Hour), 1),
			"window backwards":   standard("Backwards", now, now.Add(-time.Hour), 1),
			"zero devices":       standard("Zero", now, now.Add(time.Hour), 0),
			"demo with window":   {OrgID: a.org, Label: "Demo", Kind: scannerapp.PassKindDemo, ValidFrom: ptr(now), ValidUntil: ptr(now.Add(time.Hour)), MaxDevices: 1},
			"standard no window": {OrgID: a.org, Label: "Open", Kind: scannerapp.PassKindStandard, MaxDevices: 1},
			"unknown kind":       {OrgID: a.org, Label: "Kind", Kind: "vip", MaxDevices: 1},
		} {
			if _, err := scannerapp.CreatePass(ctx, pool, p, now); !errors.Is(err, scannerapp.ErrInvalidPass) {
				t.Errorf("%s: err = %v, want ErrInvalidPass", name, err)
			}
		}
	})
	t.Run("device cannot be inserted with a pass of another organization", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Cross org", now, now.Add(time.Hour), 5,
			scannerapp.CreatePassSession{SessionID: a.session}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		if _, _, err := insertDevice(t, pool, b.org, nil, &passID, now); pgCode(err) != "23503" {
			t.Fatalf("insert device of org B with pass of org A: err = %v, want 23503", err)
		}
		_, _, err = scannerapp.CreatePassDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: b.org, PassID: passID, Platform: scannerapp.PlatformIOS,
		}, now)
		if !errors.Is(err, scannerapp.ErrPassNotFound) {
			t.Fatalf("CreatePassDevice from org B: err = %v, want ErrPassNotFound", err)
		}
		_, hash, _ := scannerapp.NewToken()
		_, err = pool.Exec(ctx, `INSERT INTO scanner_pairing_codes (code_hash, org_id, pass_id, expires_at)
		      VALUES ($1, $2, $3, now() + interval '10 minutes')`, hash, b.org, passID)
		if pgCode(err) != "23503" {
			t.Fatalf("insert pairing code of org B with pass of org A: err = %v, want 23503", err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO scanner_pairing_codes (code_hash, org_id, pass_id, expires_at)
		      VALUES ($1, $2, $3, now() + interval '10 minutes')`, hash, a.org, passID)
		if err != nil {
			t.Fatalf("insert pairing code of the pass's own organization: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO scanner_pairing_codes (code_hash, org_id, pass_id, expires_at)
		      VALUES ('not-a-hash', $1, $2, now() + interval '10 minutes')`, a.org, passID); pgCode(err) != "23514" {
			t.Fatalf("insert pairing code with a raw code: err = %v, want 23514", err)
		}
		// Neither a person nor a pass, or both: refused by the CHECK.
		if _, _, err := insertDevice(t, pool, a.org, nil, nil, now); pgCode(err) != "23514" {
			t.Fatalf("device without owner: err = %v, want 23514", err)
		}
		if _, _, err := insertDevice(t, pool, a.org, &admin, &passID, now); pgCode(err) != "23514" {
			t.Fatalf("device with two owners: err = %v, want 23514", err)
		}
	})
	t.Run("max devices", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Two phones", now.Add(-time.Hour), now.Add(5*time.Hour), 2,
			scannerapp.CreatePassSession{SessionID: a.session}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		first, raw1, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("device 1: %v", err)
		}
		if _, _, err := newPassDevice(t, passID); err != nil {
			t.Fatalf("device 2: %v", err)
		}
		if _, _, err := newPassDevice(t, passID); !errors.Is(err, scannerapp.ErrTooManyDevices) {
			t.Fatalf("device 3: err = %v, want ErrTooManyDevices", err)
		}
		d, err := scannerapp.Authenticate(ctx, pool, raw1, now)
		if err != nil {
			t.Fatalf("Authenticate pass device: %v", err)
		}
		if d.Kind != scannerapp.KindPass || d.OrgID != a.org || d.PassID == nil || *d.PassID != passID || d.UserID != nil {
			t.Fatalf("device = %+v", d)
		}
		if ok, err := scannerapp.RevokeDevice(ctx, pool, a.org, first.ID, &admin, "", now); err != nil || !ok {
			t.Fatalf("RevokeDevice = %v, %v", ok, err)
		}
		if _, _, err := newPassDevice(t, passID); err != nil {
			t.Fatalf("device 3 after a revoke: %v", err)
		}
	})
	t.Run("pass before its window still authenticates", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Tomorrow", now.Add(time.Hour), now.Add(3*time.Hour), 1,
			scannerapp.CreatePassSession{SessionID: a.session}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		_, raw, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("CreatePassDevice: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, raw, now); err != nil {
			t.Fatalf("Authenticate before valid_from: %v", err)
		}
	})
	t.Run("pass window ended", func(t *testing.T) {
		until := now.Add(30 * time.Minute)
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Short", now.Add(-time.Hour), until, 1,
			scannerapp.CreatePassSession{SessionID: a.session}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		_, raw, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("CreatePassDevice: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, raw, until.Add(-time.Second)); err != nil {
			t.Fatalf("Authenticate inside the window: %v", err)
		}
		// The access token still has half an hour; the window decides.
		if _, err := scannerapp.Authenticate(ctx, pool, raw, until); !errors.Is(err, scannerapp.ErrAccessEnded) {
			t.Fatalf("Authenticate at valid_until: err = %v, want ErrAccessEnded", err)
		}
		_, _, err = scannerapp.CreatePassDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: a.org, PassID: passID, Platform: scannerapp.PlatformIOS,
		}, until)
		if !errors.Is(err, scannerapp.ErrAccessEnded) {
			t.Fatalf("CreatePassDevice after the window: err = %v, want ErrAccessEnded", err)
		}
	})
	t.Run("pass revoked", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Revoked", now.Add(-time.Hour), now.Add(5*time.Hour), 3,
			scannerapp.CreatePassSession{SessionID: a.session}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		_, raw, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("CreatePassDevice: %v", err)
		}
		if ok, err := scannerapp.RevokePass(ctx, pool, b.org, passID, nil, now); err != nil || ok {
			t.Fatalf("RevokePass from org B = %v, %v", ok, err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, raw, now); err != nil {
			t.Fatalf("Authenticate before the revoke: %v", err)
		}
		if ok, err := scannerapp.RevokePass(ctx, pool, a.org, passID, &admin, now); err != nil || !ok {
			t.Fatalf("RevokePass = %v, %v", ok, err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, raw, now); !errors.Is(err, scannerapp.ErrAccessEnded) {
			t.Fatalf("Authenticate after the revoke: err = %v, want ErrAccessEnded", err)
		}
		if _, _, err := newPassDevice(t, passID); !errors.Is(err, scannerapp.ErrPassRevoked) {
			t.Fatalf("CreatePassDevice of a revoked pass: err = %v, want ErrPassRevoked", err)
		}
	})
	t.Run("demo pass has no window", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, scannerapp.CreatePassParams{
			OrgID: a.org, Label: "Review", Kind: scannerapp.PassKindDemo, MaxDevices: 1,
			Sessions: []scannerapp.CreatePassSession{{SessionID: a.session}},
		}, now)
		if err != nil {
			t.Fatalf("CreatePass demo: %v", err)
		}
		_, raw, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("CreatePassDevice: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, raw, now.Add(59*time.Minute)); err != nil {
			t.Fatalf("Authenticate demo: %v", err)
		}
	})
	t.Run("describe never returns a foreign session", func(t *testing.T) {
		passID, err := scannerapp.CreatePass(ctx, pool, standard("Describe", now.Add(-time.Hour), now.Add(5*time.Hour), 1,
			scannerapp.CreatePassSession{SessionID: a.session, TierIDs: []uuid.UUID{a.tier}},
			scannerapp.CreatePassSession{SessionID: a.session2}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		// A row that names a date of another organization, written past the
		// store (a bug or a hand edit), must never reach the device.
		mustExec(t, pool, `INSERT INTO scanner_pass_sessions (pass_id, session_id) VALUES ($1, $2)`, passID, b.session)
		mustExec(t, pool, `INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)`, passID, b.session, b.tier)
		d, raw, err := newPassDevice(t, passID)
		if err != nil {
			t.Fatalf("CreatePassDevice: %v", err)
		}
		d, err = scannerapp.Authenticate(ctx, pool, raw, now)
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		me, err := scannerapp.Describe(ctx, pool, d)
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		if me.OrgID != a.org || me.Kind != scannerapp.KindPass || me.Pass == nil {
			t.Fatalf("me = %+v", me)
		}
		var orgName string
		if err := pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id = $1`, a.org).Scan(&orgName); err != nil {
			t.Fatal(err)
		}
		if me.OrgName != orgName {
			t.Errorf("OrgName = %q, want %q", me.OrgName, orgName)
		}
		p := me.Pass
		if p.ID != passID || p.Label != "Describe" || p.Kind != scannerapp.PassKindStandard ||
			p.ValidFrom == nil || !p.ValidFrom.Equal(now.Add(-time.Hour)) || p.ValidUntil == nil || !p.ValidUntil.Equal(now.Add(5*time.Hour)) {
			t.Errorf("pass = %+v", p)
		}
		if len(p.Sessions) != 2 {
			t.Fatalf("sessions = %+v, want 2", p.Sessions)
		}
		got := map[uuid.UUID]scannerapp.MePassSession{}
		for _, s := range p.Sessions {
			if s.SessionID == b.session {
				t.Fatalf("Describe returned the foreign session %s", b.session)
			}
			if s.EventID != a.event {
				t.Errorf("session %s event = %s, want %s", s.SessionID, s.EventID, a.event)
			}
			got[s.SessionID] = s
		}
		if s := got[a.session]; s.AllTiers || len(s.TierIDs) != 1 || s.TierIDs[0] != a.tier {
			t.Errorf("session 1 = %+v, want only [%s]", s, a.tier)
		}
		if s, ok := got[a.session2]; !ok || !s.AllTiers || s.TierIDs == nil || len(s.TierIDs) != 0 {
			t.Errorf("session 2 = %+v, want every category (all_tiers, empty non-nil)", s)
		}
	})

	// describeOf reads the dates of a pass as a device of orgID sees them.
	describeOf := func(t *testing.T, orgID, passID uuid.UUID) map[uuid.UUID]scannerapp.MePassSession {
		t.Helper()
		me, err := scannerapp.Describe(ctx, pool, scannerapp.Device{OrgID: orgID, PassID: &passID, Kind: scannerapp.KindPass})
		if err != nil {
			t.Fatalf("Describe: %v", err)
		}
		out := map[uuid.UUID]scannerapp.MePassSession{}
		for _, s := range me.Pass.Sessions {
			if s.TierIDs == nil {
				t.Fatalf("session %s: nil TierIDs", s.SessionID)
			}
			out[s.SessionID] = s
		}
		return out
	}
	passOf := func(orgID uuid.UUID, label string, sessions ...scannerapp.CreatePassSession) scannerapp.CreatePassParams {
		return scannerapp.CreatePassParams{
			OrgID: orgID, Label: label, Kind: scannerapp.PassKindStandard,
			ValidFrom: ptr(now.Add(-time.Hour)), ValidUntil: ptr(now.Add(5 * time.Hour)), MaxDevices: 1,
			Sessions: sessions,
		}
	}

	t.Run("restricted date never widens to every category", func(t *testing.T) {
		c := newOrg(t, pool)
		passID, err := scannerapp.CreatePass(ctx, pool, passOf(c.org, "Narrow",
			scannerapp.CreatePassSession{SessionID: c.session, TierIDs: []uuid.UUID{c.tier}},
			scannerapp.CreatePassSession{SessionID: c.session2}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		var allTiers1, allTiers2 bool
		if err := pool.QueryRow(ctx, `SELECT
		        (SELECT all_tiers FROM scanner_pass_sessions WHERE pass_id = $1 AND session_id = $2),
		        (SELECT all_tiers FROM scanner_pass_sessions WHERE pass_id = $1 AND session_id = $3)`,
			passID, c.session, c.session2).Scan(&allTiers1, &allTiers2); err != nil {
			t.Fatal(err)
		}
		if allTiers1 || !allTiers2 {
			t.Fatalf("stored all_tiers = %v, %v, want false, true", allTiers1, allTiers2)
		}
		// Rows written past the store: a category of ANOTHER date on the
		// restricted date, and a category on the every-category date. Neither
		// appears, neither flips all_tiers.
		mustExec(t, pool, `INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)`, passID, c.session, c.tier2)
		mustExec(t, pool, `INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)`, passID, c.session2, c.tier2)
		got := describeOf(t, c.org, passID)
		if s := got[c.session]; s.AllTiers || len(s.TierIDs) != 1 || s.TierIDs[0] != c.tier {
			t.Fatalf("restricted date with a planted row = %+v, want only [%s]", s, c.tier)
		}
		if s := got[c.session2]; !s.AllTiers || len(s.TierIDs) != 0 {
			t.Fatalf("every-category date with a planted row = %+v, want all_tiers and no ids", s)
		}
		// The only category of the restricted date is hard-deleted: the date
		// now checks nothing, it does not become "every category".
		mustExec(t, pool, `DELETE FROM ticket_tiers WHERE id = $1`, c.tier)
		got = describeOf(t, c.org, passID)
		if s, ok := got[c.session]; !ok || s.AllTiers || len(s.TierIDs) != 0 {
			t.Fatalf("restricted date after its category was deleted = %+v (present %v), want all_tiers false, no ids", s, ok)
		}
	})
	t.Run("pass cannot take a soft-deleted date, event or category", func(t *testing.T) {
		c := newOrg(t, pool)
		mustExec(t, pool, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, c.session2)
		if _, err := scannerapp.CreatePass(ctx, pool, passOf(c.org, "Deleted date",
			scannerapp.CreatePassSession{SessionID: c.session2}), now); !errors.Is(err, scannerapp.ErrForeignSession) {
			t.Fatalf("soft-deleted date: err = %v, want ErrForeignSession", err)
		}
		mustExec(t, pool, `UPDATE ticket_tiers SET deleted_at = now() WHERE id = $1`, c.tier)
		if _, err := scannerapp.CreatePass(ctx, pool, passOf(c.org, "Deleted category",
			scannerapp.CreatePassSession{SessionID: c.session, TierIDs: []uuid.UUID{c.tier}}), now); !errors.Is(err, scannerapp.ErrForeignTier) {
			t.Fatalf("soft-deleted category: err = %v, want ErrForeignTier", err)
		}
		e := newOrg(t, pool)
		mustExec(t, pool, `UPDATE events SET deleted_at = now() WHERE id = $1`, e.event)
		if _, err := scannerapp.CreatePass(ctx, pool, passOf(e.org, "Deleted event",
			scannerapp.CreatePassSession{SessionID: e.session}), now); !errors.Is(err, scannerapp.ErrForeignSession) {
			t.Fatalf("date of a soft-deleted event: err = %v, want ErrForeignSession", err)
		}
	})
	t.Run("soft-deleted after the pass was made", func(t *testing.T) {
		c := newOrg(t, pool)
		passID, err := scannerapp.CreatePass(ctx, pool, passOf(c.org, "Later delete",
			scannerapp.CreatePassSession{SessionID: c.session, TierIDs: []uuid.UUID{c.tier}},
			scannerapp.CreatePassSession{SessionID: c.session2}), now)
		if err != nil {
			t.Fatalf("CreatePass: %v", err)
		}
		if got := describeOf(t, c.org, passID); len(got) != 2 {
			t.Fatalf("sessions before = %+v, want 2", got)
		}
		mustExec(t, pool, `UPDATE sessions SET deleted_at = now() WHERE id = $1`, c.session2)
		mustExec(t, pool, `UPDATE ticket_tiers SET deleted_at = now() WHERE id = $1`, c.tier)
		got := describeOf(t, c.org, passID)
		if _, ok := got[c.session2]; ok || len(got) != 1 {
			t.Fatalf("sessions after the date was soft-deleted = %+v, want only %s", got, c.session)
		}
		if s := got[c.session]; s.AllTiers || len(s.TierIDs) != 0 {
			t.Fatalf("restricted date after its category was soft-deleted = %+v, want all_tiers false, no ids", s)
		}
		mustExec(t, pool, `UPDATE events SET deleted_at = now() WHERE id = $1`, c.event)
		if got := describeOf(t, c.org, passID); len(got) != 0 {
			t.Fatalf("sessions after the event was soft-deleted = %+v, want none", got)
		}
	})
	t.Run("pairing code cannot record a device of another organization", func(t *testing.T) {
		u := newUser(t, pool)
		devB, _, err := insertDevice(t, pool, b.org, &u, nil, now)
		if err != nil {
			t.Fatalf("insert device of org B: %v", err)
		}
		_, hashA, _ := scannerapp.NewToken()
		_, err = pool.Exec(ctx, `INSERT INTO scanner_pairing_codes (code_hash, org_id, user_id, expires_at, used_at, used_device_id)
		      VALUES ($1, $2, $3, now() + interval '10 minutes', now(), $4)`, hashA, a.org, u, devB)
		if pgCode(err) != "23503" {
			t.Fatalf("code of org A used by a device of org B: err = %v, want 23503", err)
		}
		_, hashB, _ := scannerapp.NewToken()
		_, err = pool.Exec(ctx, `INSERT INTO scanner_pairing_codes (code_hash, org_id, user_id, expires_at, used_at, used_device_id)
		      VALUES ($1, $2, $3, now() + interval '10 minutes', now(), $4)`, hashB, b.org, u, devB)
		if err != nil {
			t.Fatalf("code of org B used by its own device: %v", err)
		}
		// Deleting the device clears only used_device_id; the code keeps its org.
		mustExec(t, pool, `DELETE FROM scanner_devices WHERE id = $1`, devB)
		var orgID uuid.UUID
		var used *uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT org_id, used_device_id FROM scanner_pairing_codes WHERE code_hash = $1`, hashB).Scan(&orgID, &used); err != nil {
			t.Fatal(err)
		}
		if orgID != b.org || used != nil {
			t.Fatalf("after device delete: org %s, used_device_id %v, want %s and NULL", orgID, used, b.org)
		}
	})
	t.Run("describe organizer device", func(t *testing.T) {
		d, raw, err := scannerapp.CreateOrganizerDevice(ctx, pool, scannerapp.DeviceParams{
			OrgID: a.org, UserID: admin, Platform: scannerapp.PlatformIOS,
		}, now)
		if err != nil {
			t.Fatalf("CreateOrganizerDevice: %v", err)
		}
		if d, err = scannerapp.Authenticate(ctx, pool, raw, now); err != nil {
			t.Fatal(err)
		}
		me, err := scannerapp.Describe(ctx, pool, d)
		if err != nil {
			t.Fatal(err)
		}
		if me.OrgID != a.org || me.Kind != scannerapp.KindOrganizer || me.Pass != nil {
			t.Fatalf("me = %+v", me)
		}
	})
}
