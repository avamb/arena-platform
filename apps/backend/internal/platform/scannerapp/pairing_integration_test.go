//go:build integration

// pairing_integration_test.go — pairing codes, the code exchange, the
// attempt limit, token refresh, device and pass management of the scanner
// app (migration 0229, spec 69 MA-11 / MA-12) against a migrated database.
//
// Skips when DATABASE_URL is unset.
package scannerapp_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// organizer makes a person with an active organizer membership in orgID.
func organizer(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	u := newUser(t, pool)
	addMember(t, pool, u, orgID, "organizer")
	return u
}

func pairParams(code string) scannerapp.PairParams {
	return scannerapp.PairParams{Code: code, Platform: scannerapp.PlatformAndroid, DeviceLabel: "Pixel", ControllerName: "Anna"}
}

// pairOrganizer issues an organizer code and exchanges it.
func pairOrganizer(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, now time.Time) scannerapp.PairResult {
	t.Helper()
	ctx := context.Background()
	c, err := scannerapp.IssueOrganizerCode(ctx, pool, orgID, userID, now)
	if err != nil {
		t.Fatalf("IssueOrganizerCode: %v", err)
	}
	r, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), now)
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	return r
}

// standardPass makes a standard pass of f's first date with the window
// [from, until) and returns it with its pairing code.
func standardPass(t *testing.T, pool *pgxpool.Pool, f orgFix, label string, from, until time.Time, maxDevices int, now time.Time) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id, err := scannerapp.CreatePass(ctx, pool, scannerapp.CreatePassParams{
		OrgID: f.org, Label: label, Kind: scannerapp.PassKindStandard,
		ValidFrom: ptr(from), ValidUntil: ptr(until), MaxDevices: maxDevices,
		Sessions: []scannerapp.CreatePassSession{{SessionID: f.session}},
	}, now)
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	c, err := scannerapp.IssuePassCode(ctx, pool, f.org, id, nil, now)
	if err != nil {
		t.Fatalf("IssuePassCode: %v", err)
	}
	return id, c.Code
}

func wantRevoked(t *testing.T, err error, reason string, just bool) *scannerapp.RevokedError {
	t.Helper()
	var re *scannerapp.RevokedError
	if !errors.As(err, &re) || !errors.Is(err, scannerapp.ErrRevoked) {
		t.Fatalf("err = %v, want *RevokedError", err)
	}
	if re.Reason != reason || re.JustRevoked != just {
		t.Fatalf("RevokedError = %+v, want reason %q just %v", re, reason, just)
	}
	return re
}

func revokeReason(t *testing.T, pool *pgxpool.Pool, deviceID uuid.UUID) string {
	t.Helper()
	var r *string
	if err := pool.QueryRow(context.Background(), `SELECT revoke_reason FROM scanner_devices WHERE id = $1`, deviceID).Scan(&r); err != nil {
		t.Fatal(err)
	}
	if r == nil {
		return ""
	}
	return *r
}

func TestPair_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()
	a := newOrg(t, pool)

	t.Run("organizer code: ok once, typed loosely", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		c, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatalf("IssueOrganizerCode: %v", err)
		}
		if !c.ExpiresAt.Equal(now.Add(scannerapp.OrganizerCodeTTL)) || len(c.Code) != scannerapp.CodeLen {
			t.Fatalf("code = %+v", c)
		}
		typed := strings.ToLower(c.Code[:4]) + "-" + c.Code[4:]
		r, err := scannerapp.Pair(ctx, pool, pairParams(typed), now)
		if err != nil {
			t.Fatalf("Pair: %v", err)
		}
		d := r.Device
		if d.Kind != scannerapp.KindOrganizer || d.OrgID != a.org || d.UserID == nil || *d.UserID != u || d.PassID != nil || d.ControllerName != "" {
			t.Fatalf("device = %+v", d)
		}
		if !strings.HasPrefix(r.AccessToken, scannerapp.TokenWirePrefix) || !strings.HasPrefix(r.RefreshToken, scannerapp.RefreshTokenWirePrefix) ||
			!r.AccessExpiresAt.Equal(now.Add(scannerapp.AccessTokenTTL)) || !r.RefreshExpiresAt.Equal(now.Add(scannerapp.RefreshTokenTTL)) {
			t.Fatalf("tokens = %+v", r)
		}
		got, err := scannerapp.Authenticate(ctx, pool, r.AccessToken, now)
		if err != nil || got.ID != d.ID || !got.PairConfirmed {
			t.Fatalf("Authenticate = %+v, %v", got, err)
		}
		var usedBy *uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT used_device_id FROM scanner_pairing_codes WHERE code_hash = $1`, scannerapp.HashCode(c.Code)).Scan(&usedBy); err != nil {
			t.Fatal(err)
		}
		if usedBy == nil || *usedBy != d.ID {
			t.Fatalf("used_device_id = %v, want %s", usedBy, d.ID)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("second Pair: err = %v, want ErrCodeNotFound", err)
		}
	})
	t.Run("a new organizer code deletes the open one", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		c1, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		c2, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c1.Code), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("old code: err = %v, want ErrCodeNotFound", err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c2.Code), now); err != nil {
			t.Fatalf("new code: %v", err)
		}
	})
	t.Run("organizer code expired", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		c, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), c.ExpiresAt); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("err = %v, want ErrCodeNotFound", err)
		}
	})
	t.Run("unknown and malformed codes", func(t *testing.T) {
		code, err := scannerapp.NewCode()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{code, "", "ABC", "ABCDEFG0", "ABCDEFGHJK"} {
			if _, err := scannerapp.Pair(ctx, pool, pairParams(c), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
				t.Fatalf("code %q: err = %v, want ErrCodeNotFound", c, err)
			}
		}
	})
	t.Run("bad body is not a code attempt", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		c, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		for name, p := range map[string]scannerapp.PairParams{
			"platform":        {Code: c.Code, Platform: "windows"},
			"long label":      {Code: c.Code, Platform: scannerapp.PlatformIOS, DeviceLabel: strings.Repeat("x", 81)},
			"long controller": {Code: c.Code, Platform: scannerapp.PlatformIOS, ControllerName: strings.Repeat("я", 81)},
		} {
			if _, err := scannerapp.Pair(ctx, pool, p, now); !errors.Is(err, scannerapp.ErrInvalidDevice) {
				t.Fatalf("%s: err = %v, want ErrInvalidDevice", name, err)
			}
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), now); err != nil {
			t.Fatalf("the code still works: %v", err)
		}
	})
	t.Run("membership required to issue", func(t *testing.T) {
		u := newUser(t, pool)
		if _, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now); !errors.Is(err, scannerapp.ErrMembershipRequired) {
			t.Fatalf("no membership: err = %v", err)
		}
		addMember(t, pool, u, a.org, "agent")
		if _, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now); !errors.Is(err, scannerapp.ErrMembershipRequired) {
			t.Fatalf("role without scan.perform: err = %v", err)
		}
	})
	t.Run("membership ended before the exchange", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		c, err := scannerapp.IssueOrganizerCode(ctx, pool, a.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, pool, `DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, u, a.org)
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("err = %v, want ErrCodeNotFound", err)
		}
	})
	t.Run("organization archived", func(t *testing.T) {
		c := newOrg(t, pool)
		u := organizer(t, pool, c.org)
		code, err := scannerapp.IssueOrganizerCode(ctx, pool, c.org, u, now)
		if err != nil {
			t.Fatal(err)
		}
		_, passCode := standardPass(t, pool, c, "Archived", now.Add(-time.Hour), now.Add(time.Hour), 3, now)
		mustExec(t, pool, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, c.org)
		for _, k := range []string{code.Code, passCode} {
			if _, err := scannerapp.Pair(ctx, pool, pairParams(k), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
				t.Fatalf("err = %v, want ErrCodeNotFound", err)
			}
		}
	})
	t.Run("pass code: many phones up to max_devices", func(t *testing.T) {
		passID, code := standardPass(t, pool, a, "Gate", now.Add(-time.Hour), now.Add(5*time.Hour), 2, now)
		r1, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatalf("phone 1: %v", err)
		}
		if d := r1.Device; d.Kind != scannerapp.KindPass || d.PassID == nil || *d.PassID != passID || d.UserID != nil || d.ControllerName != "Anna" {
			t.Fatalf("device = %+v", d)
		}
		got, err := scannerapp.Authenticate(ctx, pool, r1.AccessToken, now)
		if err != nil || got.ControllerName != "Anna" {
			t.Fatalf("Authenticate = %+v, %v", got, err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); err != nil {
			t.Fatalf("phone 2: %v", err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); !errors.Is(err, scannerapp.ErrPassFull) {
			t.Fatalf("phone 3: err = %v, want ErrPassFull", err)
		}
		if ok, err := scannerapp.RevokeSelf(ctx, pool, a.org, r1.Device.ID, now); err != nil || !ok {
			t.Fatalf("RevokeSelf = %v, %v", ok, err)
		}
		if ok, err := scannerapp.RevokeSelf(ctx, pool, a.org, r1.Device.ID, now); err != nil || ok {
			t.Fatalf("second RevokeSelf = %v, %v", ok, err)
		}
		if r := revokeReason(t, pool, r1.Device.ID); r != scannerapp.ReasonSelfUnpaired {
			t.Fatalf("revoke_reason = %q", r)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); err != nil {
			t.Fatalf("phone 3 after self unpair: %v", err)
		}
	})
	t.Run("pass revoked, then a new link turns it back on", func(t *testing.T) {
		passID, code := standardPass(t, pool, a, "Back on", now.Add(-time.Hour), now.Add(5*time.Hour), 2, now)
		if _, err := scannerapp.RevokePassWithDevices(ctx, pool, a.org, passID, nil, now); err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("revoked pass: err = %v, want ErrCodeNotFound", err)
		}
		c, err := scannerapp.IssuePassCode(ctx, pool, a.org, passID, nil, now)
		if err != nil || !c.PassReenabled {
			t.Fatalf("IssuePassCode = %+v, %v", c, err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("old code: err = %v, want ErrCodeNotFound", err)
		}
		if _, err := scannerapp.Pair(ctx, pool, pairParams(c.Code), now); err != nil {
			t.Fatalf("new code: %v", err)
		}
		c2, err := scannerapp.IssuePassCode(ctx, pool, a.org, passID, nil, now)
		if err != nil || c2.PassReenabled {
			t.Fatalf("IssuePassCode on a live pass = %+v, %v", c2, err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM scanner_pairing_codes WHERE pass_id = $1`, passID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("codes of the pass = %d, %v, want 1", n, err)
		}
	})
	t.Run("pass window ended", func(t *testing.T) {
		until := now.Add(time.Hour)
		passID, code := standardPass(t, pool, a, "Ended", now.Add(-time.Hour), until, 2, now)
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), until); !errors.Is(err, scannerapp.ErrCodeNotFound) {
			t.Fatalf("Pair: err = %v, want ErrCodeNotFound", err)
		}
		if _, err := scannerapp.IssuePassCode(ctx, pool, a.org, passID, nil, until); !errors.Is(err, scannerapp.ErrPassWindowEnded) {
			t.Fatalf("IssuePassCode: err = %v, want ErrPassWindowEnded", err)
		}
	})
	t.Run("pass of another organization", func(t *testing.T) {
		b := newOrg(t, pool)
		passID, _ := standardPass(t, pool, a, "Foreign", now.Add(-time.Hour), now.Add(time.Hour), 1, now)
		if _, err := scannerapp.IssuePassCode(ctx, pool, b.org, passID, nil, now); !errors.Is(err, scannerapp.ErrPassNotFound) {
			t.Fatalf("err = %v, want ErrPassNotFound", err)
		}
	})
	t.Run("two phones race for the last place", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			_, code := standardPass(t, pool, a, "Race", now.Add(-time.Hour), now.Add(5*time.Hour), 2, now)
			if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); err != nil {
				t.Fatalf("round %d phone 1: %v", round, err)
			}
			var wg sync.WaitGroup
			errs := make([]error, 2)
			start := make(chan struct{})
			for i := range errs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, errs[i] = scannerapp.Pair(ctx, pool, pairParams(code), now)
				}(i)
			}
			close(start)
			wg.Wait()
			ok, full := 0, 0
			for _, err := range errs {
				switch {
				case err == nil:
					ok++
				case errors.Is(err, scannerapp.ErrPassFull):
					full++
				default:
					t.Fatalf("round %d: unexpected err %v", round, err)
				}
			}
			if ok != 1 || full != 1 {
				t.Fatalf("round %d: ok %d full %d, want 1 and 1", round, ok, full)
			}
		}
	})
}

func TestRefresh_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()
	a := newOrg(t, pool)
	later := now.Add(time.Minute)

	t.Run("ordinary refresh and confirmation", func(t *testing.T) {
		r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
		r1, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		if r1.DeviceID != r0.Device.ID || r1.OrgID != a.org || r1.AccessToken == r0.AccessToken || r1.RefreshToken == r0.RefreshToken ||
			!r1.AccessExpiresAt.Equal(later.Add(scannerapp.AccessTokenTTL)) || !r1.RefreshExpiresAt.Equal(later.Add(scannerapp.RefreshTokenTTL)) {
			t.Fatalf("result = %+v", r1)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r0.AccessToken, later); !errors.Is(err, scannerapp.ErrUnknown) {
			t.Fatalf("old access token: err = %v, want ErrUnknown", err)
		}
		d, err := scannerapp.Authenticate(ctx, pool, r1.AccessToken, later)
		if err != nil || d.PairConfirmed {
			t.Fatalf("Authenticate new = %+v, %v, want unconfirmed", d, err)
		}
		if ok, err := scannerapp.ConfirmPair(ctx, pool, d.ID, r0.AccessToken, later); err != nil || ok {
			t.Fatalf("ConfirmPair with the old access token = %v, %v", ok, err)
		}
		if ok, err := scannerapp.ConfirmPair(ctx, pool, d.ID, r1.AccessToken, later); err != nil || !ok {
			t.Fatalf("ConfirmPair = %v, %v", ok, err)
		}
		if ok, err := scannerapp.ConfirmPair(ctx, pool, d.ID, r1.AccessToken, later); err != nil || ok {
			t.Fatalf("second ConfirmPair = %v, %v", ok, err)
		}
		d, err = scannerapp.Authenticate(ctx, pool, r1.AccessToken, later)
		if err != nil || !d.PairConfirmed {
			t.Fatalf("Authenticate after confirm = %+v, %v", d, err)
		}
		r2, err := scannerapp.Refresh(ctx, pool, r1.RefreshToken, later.Add(time.Minute))
		if err != nil {
			t.Fatalf("second Refresh: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r2.AccessToken, later.Add(time.Minute)); err != nil {
			t.Fatalf("Authenticate r2: %v", err)
		}
	})
	t.Run("previous token again before confirmation: a new pair", func(t *testing.T) {
		r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
		r1, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatalf("Refresh with the previous token: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r1.AccessToken, later); !errors.Is(err, scannerapp.ErrUnknown) {
			t.Fatalf("pair 1 access: err = %v, want ErrUnknown", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r2.AccessToken, later); err != nil {
			t.Fatalf("pair 2 access: %v", err)
		}
		// The previous token is still the fallback until pair 2 is used.
		r3, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatalf("third Refresh with the previous token: %v", err)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r3.AccessToken, later); err != nil {
			t.Fatalf("pair 3 access: %v", err)
		}
	})
	t.Run("previous token after confirmation: revoked", func(t *testing.T) {
		r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
		r1, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := scannerapp.ConfirmPair(ctx, pool, r1.DeviceID, r1.AccessToken, later); err != nil || !ok {
			t.Fatalf("ConfirmPair = %v, %v", ok, err)
		}
		_, err = scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		re := wantRevoked(t, err, scannerapp.ReasonRefreshTokenReuse, true)
		if re.DeviceID != r0.Device.ID || re.OrgID != a.org {
			t.Fatalf("RevokedError = %+v", re)
		}
		if _, err := scannerapp.Authenticate(ctx, pool, r1.AccessToken, later); !errors.Is(err, scannerapp.ErrRevoked) {
			t.Fatalf("Authenticate: err = %v, want ErrRevoked", err)
		}
		_, err = scannerapp.Refresh(ctx, pool, r1.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonRefreshTokenReuse, false)
		_, err = scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonRefreshTokenReuse, false)
	})
	t.Run("retired token presented: revoked", func(t *testing.T) {
		r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
		r1, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		if err != nil {
			t.Fatal(err)
		}
		// r0 comes back before r1 is used: r1's refresh token is retired.
		if _, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later); err != nil {
			t.Fatal(err)
		}
		_, err = scannerapp.Refresh(ctx, pool, r1.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonRefreshTokenReuse, true)
		if r := revokeReason(t, pool, r0.Device.ID); r != scannerapp.ReasonRefreshTokenReuse {
			t.Fatalf("revoke_reason = %q", r)
		}
	})
	t.Run("two refreshes at once: both ok, only the later pair works", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
			var wg sync.WaitGroup
			res := make([]scannerapp.RefreshResult, 2)
			errs := make([]error, 2)
			start := make(chan struct{})
			for i := range res {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					res[i], errs[i] = scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
				}(i)
			}
			close(start)
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("round %d refresh %d: %v", round, i, err)
				}
			}
			working := 0
			var live scannerapp.RefreshResult
			for _, r := range res {
				if _, err := scannerapp.Authenticate(ctx, pool, r.AccessToken, later); err == nil {
					working++
					live = r
				} else if !errors.Is(err, scannerapp.ErrUnknown) {
					t.Fatalf("round %d: Authenticate err = %v", round, err)
				}
			}
			if working != 1 {
				t.Fatalf("round %d: %d pairs work, want 1", round, working)
			}
			if r := revokeReason(t, pool, r0.Device.ID); r != "" {
				t.Fatalf("round %d: device revoked (%q)", round, r)
			}
			if _, err := scannerapp.Refresh(ctx, pool, live.RefreshToken, later); err != nil {
				t.Fatalf("round %d: refresh of the live pair: %v", round, err)
			}
		}
	})
	t.Run("unknown and malformed tokens", func(t *testing.T) {
		raw, _, err := scannerapp.NewRefreshToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.Refresh(ctx, pool, raw, now); !errors.Is(err, scannerapp.ErrUnknown) {
			t.Fatalf("unknown: err = %v", err)
		}
		access, _, _ := scannerapp.NewToken()
		for _, bad := range []string{"", access, "sdr_short"} {
			if _, err := scannerapp.Refresh(ctx, pool, bad, now); !errors.Is(err, scannerapp.ErrMalformed) {
				t.Fatalf("%q: err = %v, want ErrMalformed", bad, err)
			}
		}
	})
	t.Run("refresh token expired", func(t *testing.T) {
		r0 := pairOrganizer(t, pool, a.org, organizer(t, pool, a.org), now)
		_, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, r0.RefreshExpiresAt)
		wantRevoked(t, err, scannerapp.ReasonRefreshExpired, true)
	})
	t.Run("organizer left the organization", func(t *testing.T) {
		u := organizer(t, pool, a.org)
		r0 := pairOrganizer(t, pool, a.org, u, now)
		mustExec(t, pool, `DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, u, a.org)
		_, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonMembershipEnded, true)
		if r := revokeReason(t, pool, r0.Device.ID); r != scannerapp.ReasonMembershipEnded {
			t.Fatalf("revoke_reason = %q", r)
		}
		_, err = scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonMembershipEnded, false)
	})
	t.Run("organization archived", func(t *testing.T) {
		c := newOrg(t, pool)
		r0 := pairOrganizer(t, pool, c.org, organizer(t, pool, c.org), now)
		mustExec(t, pool, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, c.org)
		_, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonOrgArchived, true)
	})
	t.Run("pass window ended", func(t *testing.T) {
		until := now.Add(30 * time.Minute)
		_, code := standardPass(t, pool, a, "Short", now.Add(-time.Hour), until, 1, now)
		r0, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.Refresh(ctx, pool, r0.RefreshToken, until.Add(-time.Second)); err != nil {
			t.Fatalf("Refresh inside the window: %v", err)
		}
		// The previous token (r0) is still live: the new pair was not used.
		_, err = scannerapp.Refresh(ctx, pool, r0.RefreshToken, until)
		wantRevoked(t, err, scannerapp.ReasonPassEnded, true)
	})
	t.Run("revoked pass takes its devices back", func(t *testing.T) {
		admin := newUser(t, pool)
		passID, code := standardPass(t, pool, a, "Off", now.Add(-time.Hour), now.Add(5*time.Hour), 3, now)
		r1, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		res, err := scannerapp.RevokePassWithDevices(ctx, pool, a.org, passID, &admin, now)
		if err != nil || !res.JustRevoked || len(res.RevokedDeviceIDs) != 2 {
			t.Fatalf("RevokePassWithDevices = %+v, %v", res, err)
		}
		for _, r := range []scannerapp.PairResult{r1, r2} {
			if _, err := scannerapp.Authenticate(ctx, pool, r.AccessToken, later); !errors.Is(err, scannerapp.ErrRevoked) {
				t.Fatalf("Authenticate: err = %v, want ErrRevoked", err)
			}
			if rr := revokeReason(t, pool, r.Device.ID); rr != scannerapp.ReasonPassRevoked {
				t.Fatalf("revoke_reason = %q", rr)
			}
			_, err := scannerapp.Refresh(ctx, pool, r.RefreshToken, later)
			wantRevoked(t, err, scannerapp.ReasonPassRevoked, false)
		}
		var codes int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM scanner_pairing_codes WHERE pass_id = $1`, passID).Scan(&codes); err != nil || codes != 0 {
			t.Fatalf("codes left = %d, %v", codes, err)
		}
		again, err := scannerapp.RevokePassWithDevices(ctx, pool, a.org, passID, &admin, now)
		if err != nil || again.JustRevoked || len(again.RevokedDeviceIDs) != 0 {
			t.Fatalf("second RevokePassWithDevices = %+v, %v", again, err)
		}
		b := newOrg(t, pool)
		if _, err := scannerapp.RevokePassWithDevices(ctx, pool, b.org, passID, nil, now); !errors.Is(err, scannerapp.ErrPassNotFound) {
			t.Fatalf("from another organization: err = %v", err)
		}
	})
	t.Run("pass revoked outside the store: refresh revokes", func(t *testing.T) {
		passID, code := standardPass(t, pool, a, "Old off", now.Add(-time.Hour), now.Add(5*time.Hour), 1, now)
		r0, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := scannerapp.RevokePass(ctx, pool, a.org, passID, nil, now); err != nil || !ok {
			t.Fatalf("RevokePass = %v, %v", ok, err)
		}
		_, err = scannerapp.Refresh(ctx, pool, r0.RefreshToken, later)
		wantRevoked(t, err, scannerapp.ReasonPassRevoked, true)
	})
}

func TestPairLimit_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()

	fresh := func(t *testing.T) string {
		t.Helper()
		h := scannerapp.HashIP(uuid.NewString())
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM scanner_pair_attempts WHERE ip_hash = $1`, h)
		})
		return h
	}
	freshCode := func(t *testing.T) string {
		t.Helper()
		c, err := scannerapp.NewCode()
		if err != nil {
			t.Fatal(err)
		}
		h := scannerapp.HashCode(c)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM scanner_pair_attempts WHERE code_hash = $1`, h)
		})
		return h
	}
	record := func(t *testing.T, ip, code, outcome string, at time.Time) {
		t.Helper()
		if err := scannerapp.RecordPairAttempt(ctx, pool, ip, code, outcome, at); err != nil {
			t.Fatalf("RecordPairAttempt: %v", err)
		}
	}
	wantLimited := func(t *testing.T, err error, retry time.Duration) {
		t.Helper()
		var te *scannerapp.TooManyAttemptsError
		if !errors.As(err, &te) || !errors.Is(err, scannerapp.ErrTooManyAttempts) {
			t.Fatalf("err = %v, want *TooManyAttemptsError", err)
		}
		if te.RetryAfter != retry {
			t.Fatalf("RetryAfter = %v, want %v", te.RetryAfter, retry)
		}
	}

	t.Run("by address: failed attempts", func(t *testing.T) {
		ip := fresh(t)
		first := now.Add(-10 * time.Minute)
		for i := 0; i < scannerapp.MaxFailedPairsPerIP-1; i++ {
			record(t, ip, freshCode(t), scannerapp.OutcomeNotFound, first.Add(time.Duration(i)*time.Second))
		}
		if err := scannerapp.CheckPairLimit(ctx, pool, ip, freshCode(t), now); err != nil {
			t.Fatalf("9 failures: %v", err)
		}
		record(t, ip, freshCode(t), scannerapp.OutcomeNotFound, now.Add(-time.Minute))
		wantLimited(t, scannerapp.CheckPairLimit(ctx, pool, ip, freshCode(t), now), 5*time.Minute)
		// Once the oldest failure leaves the window, the address may try again.
		if err := scannerapp.CheckPairLimit(ctx, pool, ip, freshCode(t), first.Add(scannerapp.PairAttemptWindow)); err != nil {
			t.Fatalf("after the window: %v", err)
		}
	})
	t.Run("by address: successes do not count as failures", func(t *testing.T) {
		ip := fresh(t)
		for i := 0; i < scannerapp.MaxFailedPairsPerIP+5; i++ {
			record(t, ip, freshCode(t), scannerapp.OutcomeOK, now.Add(-time.Minute))
		}
		if err := scannerapp.CheckPairLimit(ctx, pool, ip, freshCode(t), now); err != nil {
			t.Fatalf("15 successes: %v", err)
		}
	})
	t.Run("by address: all attempts", func(t *testing.T) {
		ip := fresh(t)
		for i := 0; i < scannerapp.MaxPairsPerIP; i++ {
			record(t, ip, freshCode(t), scannerapp.OutcomeOK, now.Add(-14*time.Minute))
		}
		wantLimited(t, scannerapp.CheckPairLimit(ctx, pool, ip, freshCode(t), now), time.Minute)
	})
	t.Run("by code", func(t *testing.T) {
		code := freshCode(t)
		for i := 0; i < scannerapp.MaxFailedPairsPerCode; i++ {
			outcome := scannerapp.OutcomeNotFound
			if i%2 == 0 {
				outcome = scannerapp.OutcomePassFull
			}
			record(t, fresh(t), code, outcome, now.Add(-2*time.Minute))
		}
		wantLimited(t, scannerapp.CheckPairLimit(ctx, pool, fresh(t), code, now), 13*time.Minute)
		other := freshCode(t)
		for i := 0; i < scannerapp.MaxFailedPairsPerCode; i++ {
			record(t, fresh(t), other, scannerapp.OutcomeOK, now.Add(-2*time.Minute))
		}
		if err := scannerapp.CheckPairLimit(ctx, pool, fresh(t), other, now); err != nil {
			t.Fatalf("successful attempts of a code: %v", err)
		}
	})
	t.Run("old attempts are deleted on write", func(t *testing.T) {
		ip := fresh(t)
		mustExec(t, pool, `INSERT INTO scanner_pair_attempts (ip_hash, code_hash, outcome, created_at) VALUES ($1, $1, 'ok', $2)`,
			ip, now.Add(-scannerapp.PairAttemptRetention-time.Hour))
		record(t, ip, freshCode(t), scannerapp.OutcomeOK, now)
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM scanner_pair_attempts WHERE ip_hash = $1`, ip).Scan(&n); err != nil || n != 1 {
			t.Fatalf("rows = %d, %v, want 1", n, err)
		}
		if err := scannerapp.RecordPairAttempt(ctx, pool, ip, ip, "maybe", now); !errors.Is(err, scannerapp.ErrInvalidOutcome) {
			t.Fatalf("unknown outcome: err = %v", err)
		}
	})
}

func TestDevicesAndPasses_LiveDB(t *testing.T) {
	pool := livePool(t)
	ctx := context.Background()
	now := testNow()
	a := newOrg(t, pool)
	b := newOrg(t, pool)
	admin := newUser(t, pool)
	addMember(t, pool, admin, a.org, "org_admin")

	t.Run("list and revoke devices", func(t *testing.T) {
		c := newOrg(t, pool)
		u1, u2 := organizer(t, pool, c.org), organizer(t, pool, c.org)
		d1 := pairOrganizer(t, pool, c.org, u1, now).Device
		d2 := pairOrganizer(t, pool, c.org, u2, now.Add(time.Second)).Device
		passID, code := standardPass(t, pool, c, "Door", now.Add(-time.Hour), now.Add(5*time.Hour), 2, now)
		rp, err := scannerapp.Pair(ctx, pool, pairParams(code), now.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		own, err := scannerapp.ListDevices(ctx, pool, c.org, &u1)
		if err != nil || len(own) != 1 || own[0].ID != d1.ID || own[0].Kind != scannerapp.KindOrganizer {
			t.Fatalf("own devices = %+v, %v", own, err)
		}
		all, err := scannerapp.ListDevices(ctx, pool, c.org, nil)
		if err != nil || len(all) != 3 {
			t.Fatalf("all devices = %+v, %v", all, err)
		}
		if p := all[0]; p.ID != rp.Device.ID || p.Kind != scannerapp.KindPass || p.PassID == nil || *p.PassID != passID ||
			p.PassLabel == nil || *p.PassLabel != "Door" || p.ControllerName != "Anna" || p.UserID != nil {
			t.Fatalf("newest device = %+v", p)
		}
		if _, err := scannerapp.RevokeDeviceOf(ctx, pool, c.org, d2.ID, &u1, &u1, now); !errors.Is(err, scannerapp.ErrDeviceNotFound) {
			t.Fatalf("another person's device: err = %v", err)
		}
		if _, err := scannerapp.RevokeDeviceOf(ctx, pool, b.org, d1.ID, nil, &admin, now); !errors.Is(err, scannerapp.ErrDeviceNotFound) {
			t.Fatalf("device of another organization: err = %v", err)
		}
		if _, err := scannerapp.RevokeDeviceOf(ctx, pool, c.org, uuid.New(), nil, &admin, now); !errors.Is(err, scannerapp.ErrDeviceNotFound) {
			t.Fatalf("unknown device: err = %v", err)
		}
		if just, err := scannerapp.RevokeDeviceOf(ctx, pool, c.org, d1.ID, &u1, &u1, now); err != nil || !just {
			t.Fatalf("own device = %v, %v", just, err)
		}
		if just, err := scannerapp.RevokeDeviceOf(ctx, pool, c.org, d1.ID, &u1, &u1, now); err != nil || just {
			t.Fatalf("own device again = %v, %v", just, err)
		}
		if r := revokeReason(t, pool, d1.ID); r != scannerapp.ReasonRevokedByUser {
			t.Fatalf("revoke_reason = %q", r)
		}
		if just, err := scannerapp.RevokeDeviceOf(ctx, pool, c.org, rp.Device.ID, nil, &admin, now); err != nil || !just {
			t.Fatalf("any device by a manager = %v, %v", just, err)
		}
		all, err = scannerapp.ListDevices(ctx, pool, c.org, nil)
		if err != nil || len(all) != 1 || all[0].ID != d2.ID {
			t.Fatalf("devices after revokes = %+v, %v", all, err)
		}
	})
	t.Run("get, list and update passes", func(t *testing.T) {
		c := newOrg(t, pool)
		// A second event of the same organization.
		event2, session3 := uuid.New(), uuid.New()
		mustExec(t, pool, `INSERT INTO events (id, org_id, name) VALUES ($1, $2, 'Second event')`, event2, c.org)
		mustExec(t, pool, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
		      VALUES ($1, $2, $3, now() + interval '20 days', now() + interval '20 days 2 hours', 100, 'EUR', 'override')`, session3, event2, c.venue)

		p1, code := standardPass(t, pool, c, "First", now.Add(-time.Hour), now.Add(5*time.Hour), 3, now)
		p2, err := scannerapp.CreatePass(ctx, pool, scannerapp.CreatePassParams{
			OrgID: c.org, Label: "Second", Kind: scannerapp.PassKindStandard,
			ValidFrom: ptr(now.Add(-time.Hour)), ValidUntil: ptr(now.Add(5 * time.Hour)), MaxDevices: 2,
			Sessions: []scannerapp.CreatePassSession{{SessionID: session3}},
		}, now.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		r1, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		r2, err := scannerapp.Pair(ctx, pool, pairParams(code), now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := scannerapp.RevokeSelf(ctx, pool, c.org, r2.Device.ID, now); err != nil {
			t.Fatal(err)
		}

		got, err := scannerapp.GetPass(ctx, pool, c.org, p1)
		if err != nil {
			t.Fatalf("GetPass: %v", err)
		}
		if got.Label != "First" || got.Kind != scannerapp.PassKindStandard || got.MaxDevices != 3 || got.Accepted != 1 ||
			len(got.Devices) != 1 || got.Devices[0].ID != r1.Device.ID || got.Devices[0].ControllerName != "Anna" ||
			len(got.Sessions) != 1 || got.Sessions[0].SessionID != c.session || got.Sessions[0].EventID != c.event || !got.Sessions[0].AllTiers {
			t.Fatalf("pass = %+v", got)
		}
		if _, err := scannerapp.GetPass(ctx, pool, b.org, p1); !errors.Is(err, scannerapp.ErrPassNotFound) {
			t.Fatalf("GetPass from another organization: err = %v", err)
		}
		list, err := scannerapp.ListPasses(ctx, pool, c.org, nil)
		if err != nil || len(list) != 2 || list[0].ID != p2 || list[1].ID != p1 || list[0].Devices == nil {
			t.Fatalf("ListPasses = %+v, %v", list, err)
		}
		list, err = scannerapp.ListPasses(ctx, pool, c.org, &event2)
		if err != nil || len(list) != 1 || list[0].ID != p2 {
			t.Fatalf("ListPasses of event 2 = %+v, %v", list, err)
		}
		list, err = scannerapp.ListPasses(ctx, pool, b.org, nil)
		if err != nil || len(list) != 0 {
			t.Fatalf("ListPasses of another organization = %+v, %v", list, err)
		}

		upd := func(p scannerapp.UpdatePassParams) error {
			p.OrgID, p.PassID = c.org, p1
			return scannerapp.UpdatePass(ctx, pool, p)
		}
		if err := upd(scannerapp.UpdatePassParams{MaxDevices: ptr(0)}); !errors.Is(err, scannerapp.ErrInvalidPass) {
			t.Fatalf("max 0: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{MaxDevices: ptr(51)}); !errors.Is(err, scannerapp.ErrInvalidPass) {
			t.Fatalf("max 51: err = %v", err)
		}
		mustExec(t, pool, `UPDATE scanner_passes SET max_devices = 5 WHERE id = $1`, p1)
		if _, err := scannerapp.Pair(ctx, pool, pairParams(code), now); err != nil {
			t.Fatal(err)
		}
		if err := upd(scannerapp.UpdatePassParams{MaxDevices: ptr(1)}); !errors.Is(err, scannerapp.ErrMaxDevicesBelowAccepted) {
			t.Fatalf("max below accepted: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{ValidUntil: ptr(now.Add(-2 * time.Hour))}); !errors.Is(err, scannerapp.ErrInvalidWindow) {
			t.Fatalf("window backwards: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{Label: ptr("  ")}); !errors.Is(err, scannerapp.ErrInvalidPass) {
			t.Fatalf("blank label: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{Sessions: &[]scannerapp.CreatePassSession{}}); !errors.Is(err, scannerapp.ErrInvalidPass) {
			t.Fatalf("no dates: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{Sessions: &[]scannerapp.CreatePassSession{{SessionID: b.session}}}); !errors.Is(err, scannerapp.ErrForeignSession) {
			t.Fatalf("foreign date: err = %v", err)
		}
		if err := upd(scannerapp.UpdatePassParams{Sessions: &[]scannerapp.CreatePassSession{{SessionID: c.session, TierIDs: []uuid.UUID{c.tier2}}}}); !errors.Is(err, scannerapp.ErrForeignTier) {
			t.Fatalf("category of another date: err = %v", err)
		}
		if err := scannerapp.UpdatePass(ctx, pool, scannerapp.UpdatePassParams{OrgID: b.org, PassID: p1, Label: ptr("X")}); !errors.Is(err, scannerapp.ErrPassNotFound) {
			t.Fatalf("from another organization: err = %v", err)
		}
		until := now.Add(48 * time.Hour)
		if err := upd(scannerapp.UpdatePassParams{
			Label: ptr(" Renamed "), ValidUntil: &until, MaxDevices: ptr(2),
			Sessions: &[]scannerapp.CreatePassSession{{SessionID: c.session2, TierIDs: []uuid.UUID{c.tier2}}, {SessionID: session3}},
		}); err != nil {
			t.Fatalf("UpdatePass: %v", err)
		}
		got, err = scannerapp.GetPass(ctx, pool, c.org, p1)
		if err != nil {
			t.Fatal(err)
		}
		if got.Label != "Renamed" || got.MaxDevices != 2 || got.ValidUntil == nil || !got.ValidUntil.Equal(until) ||
			got.ValidFrom == nil || !got.ValidFrom.Equal(now.Add(-time.Hour)) || len(got.Sessions) != 2 {
			t.Fatalf("pass after update = %+v", got)
		}
		for _, s := range got.Sessions {
			switch s.SessionID {
			case c.session2:
				if s.AllTiers || len(s.TierIDs) != 1 || s.TierIDs[0] != c.tier2 {
					t.Fatalf("restricted date = %+v", s)
				}
			case session3:
				if !s.AllTiers || s.EventID != event2 {
					t.Fatalf("every-category date = %+v", s)
				}
			default:
				t.Fatalf("unexpected date %+v", s)
			}
		}
		if _, err := scannerapp.RevokePassWithDevices(ctx, pool, c.org, p1, &admin, now); err != nil {
			t.Fatal(err)
		}
		if err := upd(scannerapp.UpdatePassParams{Label: ptr("Late")}); !errors.Is(err, scannerapp.ErrPassRevoked) {
			t.Fatalf("revoked pass: err = %v", err)
		}
		got, err = scannerapp.GetPass(ctx, pool, c.org, p1)
		if err != nil || got.RevokedAt == nil || got.Accepted != 0 || len(got.Devices) != 0 {
			t.Fatalf("revoked pass = %+v, %v", got, err)
		}
	})
	t.Run("demo pass refuses a window", func(t *testing.T) {
		id, err := scannerapp.CreatePass(ctx, pool, scannerapp.CreatePassParams{
			OrgID: a.org, Label: "Review", Kind: scannerapp.PassKindDemo, MaxDevices: 1,
			Sessions: []scannerapp.CreatePassSession{{SessionID: a.session}},
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := scannerapp.UpdatePass(ctx, pool, scannerapp.UpdatePassParams{OrgID: a.org, PassID: id, ValidUntil: ptr(now)}); !errors.Is(err, scannerapp.ErrInvalidWindow) {
			t.Fatalf("err = %v, want ErrInvalidWindow", err)
		}
		// A demo pass has no window, so a new link never says it ended.
		if _, err := scannerapp.IssuePassCode(ctx, pool, a.org, id, nil, now.Add(10000*time.Hour)); err != nil {
			t.Fatalf("IssuePassCode demo: %v", err)
		}
	})
	t.Run("default pass window", func(t *testing.T) {
		c := newOrg(t, pool)
		var start1, start2 time.Time
		if err := pool.QueryRow(ctx, `SELECT (SELECT start_at FROM sessions WHERE id = $1), (SELECT start_at FROM sessions WHERE id = $2)`,
			c.session, c.session2).Scan(&start1, &start2); err != nil {
			t.Fatal(err)
		}
		from, until, err := scannerapp.DefaultPassWindow(ctx, pool, c.org, []uuid.UUID{c.session, c.session2, c.session})
		if err != nil {
			t.Fatal(err)
		}
		if !from.Equal(start1.Add(-4*time.Hour)) || !until.Equal(start2.Add(6*time.Hour)) {
			t.Fatalf("window = %v .. %v, starts %v %v", from, until, start1, start2)
		}
		mustExec(t, pool, `UPDATE sessions SET doors_open_at = start_at - interval '1 hour' WHERE id = $1`, c.session)
		from, _, err = scannerapp.DefaultPassWindow(ctx, pool, c.org, []uuid.UUID{c.session})
		if err != nil || !from.Equal(start1.Add(-5*time.Hour)) {
			t.Fatalf("from with doors = %v, %v", from, err)
		}
		if _, _, err := scannerapp.DefaultPassWindow(ctx, pool, c.org, []uuid.UUID{c.session, b.session}); !errors.Is(err, scannerapp.ErrForeignSession) {
			t.Fatalf("foreign date: err = %v", err)
		}
		if _, _, err := scannerapp.DefaultPassWindow(ctx, pool, c.org, nil); !errors.Is(err, scannerapp.ErrInvalidPass) {
			t.Fatalf("no dates: err = %v", err)
		}
	})
}
