//go:build integration

// scanner_app_ma11_integration_test.go — spec 69, MA-11 / MA-12 through the
// real router: pairing, token refresh, self-unpair, the organizer's pairing
// code and devices, and controller passes.
//
// Every pairing request carries its own X-Forwarded-For (the server trusts
// one proxy hop here), so the attempt limit of one test never meets the
// attempts of another test on the shared database.
//
// Run against a migrated database:
//
//	DATABASE_URL=... go test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestScannerAppMA11_
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/clock"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

type ma11Env struct {
	*ma10Env
}

func newMA11Env(t *testing.T) *ma11Env {
	t.Helper()
	e := &ma11Env{&ma10Env{newSec1EnvCfg(t, func(c *config.Config) { c.TrustedProxyCount = 1 })}}
	// Registered after newSec1EnvCfg, so it runs BEFORE its organization and
	// user cleanup (t.Cleanup is LIFO).
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM scanner_pairing_codes WHERE org_id = ANY($1)`,
			`DELETE FROM scanner_devices WHERE org_id = ANY($1)`,
			`DELETE FROM scanner_pass_tiers WHERE pass_id IN (SELECT id FROM scanner_passes WHERE org_id = ANY($1))`,
			`DELETE FROM scanner_pass_sessions WHERE pass_id IN (SELECT id FROM scanner_passes WHERE org_id = ANY($1))`,
			`DELETE FROM scanner_passes WHERE org_id = ANY($1)`,
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT s.id FROM sessions s JOIN events ev ON ev.id = s.event_id WHERE ev.org_id = ANY($1))`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = ANY($1))`,
			`DELETE FROM events WHERE org_id = ANY($1)`,
			`DELETE FROM venues WHERE org_id = ANY($1)`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, e.orgs); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return e
}

// ma11IP is a random private address, one per call.
func ma11IP() string {
	return fmt.Sprintf("10.%d.%d.%d", rand.Intn(250)+1, rand.Intn(250)+1, rand.Intn(250)+1) //nolint:gosec // test address
}

// raw sends one request without failing the test (safe in goroutines).
func (e *ma11Env) raw(method, path, bearer, ip, reason, body string) (int, http.Header, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		return 0, nil, "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, resp.Header, buf.String(), nil
}

func (e *ma11Env) call(t *testing.T, method, path, bearer, ip, body string) (int, http.Header, string) {
	t.Helper()
	st, h, out, err := e.raw(method, path, bearer, ip, "", body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return st, h, out
}

// want fails unless the request answered status (and code, when not "").
func (e *ma11Env) want(t *testing.T, status int, code, method, path, bearer, body string) string {
	t.Helper()
	st, _, out := e.call(t, method, path, bearer, "", body)
	if st != status || (code != "" && sec1Code(out) != code) {
		t.Fatalf("%s %s: %d %s, want %d %s", method, path, st, out, status, code)
	}
	return out
}

type ma11Pairing struct {
	Code      string     `json:"code"`
	Link      string     `json:"link"`
	ExpiresAt *time.Time `json:"expires_at"`
	QRPNG     *string    `json:"qr_png"`
}

type ma11Tokens struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Me               *ma10Me   `json:"me"`
}

type ma11Pass struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Kind        string     `json:"kind"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidUntil  *time.Time `json:"valid_until"`
	MaxDevices  int        `json:"max_devices"`
	Accepted    int        `json:"accepted"`
	RevokedAt   *time.Time `json:"revoked_at"`
	State       string     `json:"state"`
	ClosedAt    *time.Time `json:"closed_at"`
	UploadUntil *time.Time `json:"upload_until"`
	Sessions    []struct {
		SessionID string   `json:"session_id"`
		EventID   string   `json:"event_id"`
		AllTiers  bool     `json:"all_tiers"`
		TierIDs   []string `json:"tier_ids"`
	} `json:"sessions"`
	Devices []struct {
		ID             string `json:"id"`
		ControllerName string `json:"controller_name"`
	} `json:"devices"`
}

func ma11Decode(t *testing.T, body string, dst any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), dst); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func orgScanner(org uuid.UUID) string { return "/v1/organizations/" + org.String() + "/scanner" }

// organizerCode issues the caller's own pairing code in org.
func (e *ma11Env) organizerCode(t *testing.T, org uuid.UUID, tok string) ma11Pairing {
	t.Helper()
	var p ma11Pairing
	ma11Decode(t, e.want(t, http.StatusCreated, "", http.MethodPost, orgScanner(org)+"/pairing-codes", tok, ""), &p)
	return p
}

func pairBody(code, controller string) string {
	return `{"code":"` + code + `","platform":"android","device_label":"Gate","controller_name":"` + controller + `"}`
}

// pair exchanges code from a fresh address and expects 201.
func (e *ma11Env) pair(t *testing.T, code, controller string) ma11Tokens {
	t.Helper()
	st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(code, controller))
	if st != http.StatusCreated {
		t.Fatalf("pair %s: %d %s, want 201", code, st, out)
	}
	var tk ma11Tokens
	ma11Decode(t, out, &tk)
	return tk
}

func (e *ma11Env) refresh(t *testing.T, refresh string) (int, ma11Tokens, string) {
	t.Helper()
	st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/token/refresh", "", "", `{"refresh_token":"`+refresh+`"}`)
	var tk ma11Tokens
	if st == http.StatusOK {
		ma11Decode(t, out, &tk)
	}
	return st, tk, out
}

func (e *ma11Env) meStatus(t *testing.T, access string) int {
	t.Helper()
	st, _, _ := e.call(t, http.MethodGet, "/v1/scanner-app/me", access, "", "")
	return st
}

// createPass creates a pass of org through the route.
func (e *ma11Env) createPass(t *testing.T, org uuid.UUID, tok, body string) (ma11Pass, ma11Pairing) {
	t.Helper()
	var out struct {
		Pass    ma11Pass    `json:"pass"`
		Pairing ma11Pairing `json:"pairing"`
	}
	ma11Decode(t, e.want(t, http.StatusCreated, "", http.MethodPost, orgScanner(org)+"/passes", tok, body), &out)
	return out.Pass, out.Pairing
}

func (e *ma11Env) deviceRevoke(t *testing.T, device string) (revoked bool, reason string) {
	t.Helper()
	var r *string
	var at *time.Time
	if err := e.srv.pgxPool.QueryRow(context.Background(),
		`SELECT revoked_at, revoke_reason FROM scanner_devices WHERE id = $1`, device).Scan(&at, &r); err != nil {
		t.Fatalf("read device %s: %v", device, err)
	}
	if r != nil {
		reason = *r
	}
	return at != nil, reason
}

type ma11Audit struct {
	ActorType, ActorID, Reason string
}

func (e *ma11Env) audits(t *testing.T, action, resource string) []ma11Audit {
	t.Helper()
	rows, err := e.srv.pgxPool.Query(context.Background(),
		`SELECT actor_type, COALESCE(actor_id::text, ''), COALESCE(metadata->>'reason', '')
		 FROM audit_events WHERE action = $1 AND resource_id = $2 ORDER BY occurred_at`, action, resource)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []ma11Audit
	for rows.Next() {
		var a ma11Audit
		if err := rows.Scan(&a.ActorType, &a.ActorID, &a.Reason); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		out = append(out, a)
	}
	return out
}

func ma11PassBody(label string, maxDevices int, sessions ...string) string {
	return fmt.Sprintf(`{"label":%q,"max_devices":%d,"sessions":[%s]}`, label, maxDevices, strings.Join(sessions, ","))
}

func ma11Session(id uuid.UUID, tiers ...uuid.UUID) string {
	if len(tiers) == 0 {
		return `{"session_id":"` + id.String() + `"}`
	}
	ts := make([]string, 0, len(tiers))
	for _, tr := range tiers {
		ts = append(ts, `"`+tr.String()+`"`)
	}
	return `{"session_id":"` + id.String() + `","tier_ids":[` + strings.Join(ts, ",") + `]}`
}

func TestScannerAppMA11_Pair(t *testing.T) {
	e := newMA11Env(t)
	a := e.fixture(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")
	tok := e.token(t, owner)

	t.Run("organizer code and exchange", func(t *testing.T) {
		before := time.Now()
		p := e.organizerCode(t, a.org, tok)
		if len(p.Code) != scannerapp.CodeLen || !strings.HasSuffix(p.Link, "/scan/pair#"+p.Code) || p.QRPNG == nil || *p.QRPNG == "" {
			t.Fatalf("pairing = %+v", p)
		}
		if p.ExpiresAt == nil || p.ExpiresAt.Before(before.Add(14*time.Minute)) || p.ExpiresAt.After(time.Now().Add(16*time.Minute)) {
			t.Fatalf("expires_at = %v, want about 15 minutes", p.ExpiresAt)
		}
		// Case, spaces and hyphens do not matter.
		typed := strings.ToLower(p.Code[:4]) + "-" + p.Code[4:]
		tk := e.pair(t, typed, "")
		if !strings.HasPrefix(tk.AccessToken, scannerapp.TokenWirePrefix) || !strings.HasPrefix(tk.RefreshToken, scannerapp.RefreshTokenWirePrefix) {
			t.Fatalf("tokens = %+v", tk)
		}
		if tk.Me == nil || tk.Me.Kind != scannerapp.KindOrganizer || tk.Me.Organization.ID != a.org.String() || tk.Me.Organization.Name == "" {
			t.Fatalf("me = %+v", tk.Me)
		}
		if st := e.meStatus(t, tk.AccessToken); st != http.StatusOK {
			t.Fatalf("/me with the new token: %d", st)
		}
		// The refresh token is never a Bearer token.
		if st := e.meStatus(t, tk.RefreshToken); st != http.StatusUnauthorized {
			t.Fatalf("/me with the refresh token: %d, want 401", st)
		}
		got := e.audits(t, "v1.scanner_app.pair", tk.Me.Device.ID)
		if len(got) != 1 || got[0].ActorType != "device" || got[0].ActorID != tk.Me.Device.ID {
			t.Fatalf("pair audit = %+v, want one row with actor device %s", got, tk.Me.Device.ID)
		}
		issue := e.audits(t, "v1.scanner.pairing_code_issue", a.org.String())
		if len(issue) == 0 || issue[len(issue)-1].ActorType != "user" || issue[len(issue)-1].ActorID != owner.String() {
			t.Fatalf("code issue audit = %+v", issue)
		}

		// Used: the same code again.
		st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(p.Code, ""))
		if st != http.StatusNotFound || sec1Code(out) != "scanner_app.code_not_found" {
			t.Fatalf("used code: %d %s, want 404 scanner_app.code_not_found", st, out)
		}
	})

	t.Run("unknown, expired and malformed codes are one 404", func(t *testing.T) {
		unknown, err := scannerapp.NewCode()
		if err != nil {
			t.Fatal(err)
		}
		expired := e.organizerCode(t, a.org, tok)
		e.exec(t, `UPDATE scanner_pairing_codes SET created_at = now() - interval '20 minutes', expires_at = now() - interval '1 second' WHERE code_hash = $1`, scannerapp.HashCode(expired.Code))
		for name, code := range map[string]string{"unknown": unknown, "expired": expired.Code, "malformed": "0OIL"} {
			st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(code, ""))
			if st != http.StatusNotFound || sec1Code(out) != "scanner_app.code_not_found" {
				t.Errorf("%s code: %d %s, want 404 scanner_app.code_not_found", name, st, out)
			}
		}
	})

	t.Run("bad body is 400 and not an attempt", func(t *testing.T) {
		ip := ma11IP()
		for _, body := range []string{`not json`, `{"code":"ABCDEFGH","platform":"windows"}`} {
			st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ip, body)
			if st != http.StatusBadRequest {
				t.Errorf("body %s: %d %s, want 400", body, st, out)
			}
		}
		var n int
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM scanner_pair_attempts WHERE ip_hash = $1`,
			scannerapp.HashIP(ip)).Scan(&n); err != nil || n != 0 {
			t.Fatalf("attempts of a bad body = %d, %v; want 0", n, err)
		}
	})

	t.Run("429 with Retry-After", func(t *testing.T) {
		ip := ma11IP()
		for i := 0; i < scannerapp.MaxFailedPairsPerIP; i++ {
			c, _ := scannerapp.NewCode()
			if st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ip, pairBody(c, "")); st != http.StatusNotFound {
				t.Fatalf("attempt %d: %d %s, want 404", i+1, st, out)
			}
		}
		c, _ := scannerapp.NewCode()
		st, h, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ip, pairBody(c, ""))
		if st != http.StatusTooManyRequests || sec1Code(out) != "scanner_app.too_many_attempts" {
			t.Fatalf("over the limit: %d %s, want 429 scanner_app.too_many_attempts", st, out)
		}
		if secs, err := strconv.Atoi(h.Get("Retry-After")); err != nil || secs < 1 || secs > int(scannerapp.PairAttemptWindow.Seconds()) {
			t.Fatalf("Retry-After = %q", h.Get("Retry-After"))
		}
		// Another address is not limited.
		if st, _, _ := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(c, "")); st != http.StatusNotFound {
			t.Fatalf("another address: %d, want 404", st)
		}
	})

	t.Run("pass full is 409", func(t *testing.T) {
		_, link := e.createPass(t, a.org, tok, ma11PassBody("One phone", 1, ma11Session(a.session)))
		tk := e.pair(t, link.Code, "Anna")
		if tk.Me == nil || tk.Me.Kind != scannerapp.KindPass || tk.Me.Pass == nil {
			t.Fatalf("pass device me = %+v", tk.Me)
		}
		st, _, out := e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(link.Code, "Boris"))
		if st != http.StatusConflict || sec1Code(out) != "scanner_app.pass_full" {
			t.Fatalf("second phone: %d %s, want 409 scanner_app.pass_full", st, out)
		}
	})

	t.Run("two phones race for the last place", func(t *testing.T) {
		_, link := e.createPass(t, a.org, tok, ma11PassBody("Race", 2, ma11Session(a.session)))
		e.pair(t, link.Code, "First")
		var wg sync.WaitGroup
		statuses := make([]int, 2)
		errs := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				statuses[i], _, _, errs[i] = e.raw(http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), "", pairBody(link.Code, fmt.Sprintf("Racer %d", i)))
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		created, full := 0, 0
		for _, st := range statuses {
			switch st {
			case http.StatusCreated:
				created++
			case http.StatusConflict:
				full++
			}
		}
		if created != 1 || full != 1 {
			t.Fatalf("statuses = %v, want one 201 and one 409", statuses)
		}
	})
}

func TestScannerAppMA11_Refresh(t *testing.T) {
	e := newMA11Env(t)
	a := e.fixture(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")
	tok := e.token(t, owner)

	t.Run("rotation, retry before confirmation, reuse after it", func(t *testing.T) {
		t1 := e.pair(t, e.organizerCode(t, a.org, tok).Code, "")
		device := t1.Me.Device.ID

		st, t2, out := e.refresh(t, t1.RefreshToken)
		if st != http.StatusOK || t2.AccessToken == t1.AccessToken || t2.RefreshToken == t1.RefreshToken {
			t.Fatalf("refresh: %d %s", st, out)
		}
		if st := e.meStatus(t, t1.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("old access token: %d, want 401", st)
		}
		// The answer was lost: the previous token still works before the new
		// pair is used, and gives a pair instead of the unused one.
		st, t3, out := e.refresh(t, t1.RefreshToken)
		if st != http.StatusOK {
			t.Fatalf("retry with the previous token: %d %s, want 200", st, out)
		}
		if st := e.meStatus(t, t2.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("dropped pair still works: %d", st)
		}
		// First use of the new access token confirms the pair.
		if st := e.meStatus(t, t3.AccessToken); st != http.StatusOK {
			t.Fatalf("new access token: %d, want 200", st)
		}
		if rev, _ := e.deviceRevoke(t, device); rev {
			t.Fatal("device revoked by a normal retry")
		}
		// The previous token after confirmation is a copied token.
		st, _, out = e.refresh(t, t1.RefreshToken)
		if st != http.StatusUnauthorized || sec1Code(out) != "scanner_app.device_revoked" {
			t.Fatalf("reuse after confirmation: %d %s, want 401 scanner_app.device_revoked", st, out)
		}
		if rev, reason := e.deviceRevoke(t, device); !rev || reason != scannerapp.ReasonRefreshTokenReuse {
			t.Fatalf("device revoked = %v (%q), want refresh_token_reuse", rev, reason)
		}
		if st := e.meStatus(t, t3.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("access token of a revoked device: %d", st)
		}
		if st, _, _ := e.refresh(t, t3.RefreshToken); st != http.StatusUnauthorized {
			t.Fatalf("current token of a revoked device: %d", st)
		}
		got := e.audits(t, "v1.scanner_app.device_revoke", device)
		if len(got) != 1 || got[0].ActorType != "device" || got[0].ActorID != device || got[0].Reason != scannerapp.ReasonRefreshTokenReuse {
			t.Fatalf("revoke audit = %+v", got)
		}
	})

	t.Run("two refreshes at once", func(t *testing.T) {
		t1 := e.pair(t, e.organizerCode(t, a.org, tok).Code, "")
		var wg sync.WaitGroup
		res := make([]ma11Tokens, 2)
		sts := make([]int, 2)
		errs := make([]error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				var out string
				sts[i], _, out, errs[i] = e.raw(http.MethodPost, "/v1/scanner-app/token/refresh", "", "", "", `{"refresh_token":"`+t1.RefreshToken+`"}`)
				if errs[i] == nil && sts[i] == http.StatusOK {
					errs[i] = json.Unmarshal([]byte(out), &res[i])
				}
			}(i)
		}
		wg.Wait()
		for i := range sts {
			if errs[i] != nil || sts[i] != http.StatusOK {
				t.Fatalf("refresh %d: %d %v, want 200", i, sts[i], errs[i])
			}
		}
		working := 0
		for i := range res {
			if e.meStatus(t, res[i].AccessToken) == http.StatusOK {
				working++
			}
		}
		if working != 1 {
			t.Fatalf("%d pairs work, want exactly one", working)
		}
		if rev, reason := e.deviceRevoke(t, t1.Me.Device.ID); rev {
			t.Fatalf("device revoked (%s) by two refreshes at once", reason)
		}
	})

	t.Run("bad tokens", func(t *testing.T) {
		unknown, _, err := scannerapp.NewRefreshToken()
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{unknown, "sdr_short", "", "sdt_" + unknown[4:]} {
			if st, _, out := e.refresh(t, raw); st != http.StatusUnauthorized || sec1Code(out) != "scanner_app.device_revoked" {
				t.Errorf("refresh %q: %d %s, want 401", raw, st, out)
			}
		}
		if st, _, _ := e.call(t, http.MethodPost, "/v1/scanner-app/token/refresh", "", "", "nope"); st != http.StatusBadRequest {
			t.Errorf("bad body: %d, want 400", st)
		}
	})

	t.Run("fired organizer", func(t *testing.T) {
		u := e.user(t, "gate")
		e.member(t, u, a.org, "organizer")
		t1 := e.pair(t, e.organizerCode(t, a.org, e.token(t, u)).Code, "")
		e.exec(t, `UPDATE memberships SET status = 'revoked' WHERE user_id = $1 AND org_id = $2`, u, a.org)
		st, _, out := e.refresh(t, t1.RefreshToken)
		if st != http.StatusUnauthorized || sec1Code(out) != "scanner_app.device_revoked" {
			t.Fatalf("refresh of a fired organizer: %d %s, want 401", st, out)
		}
		device := t1.Me.Device.ID
		if rev, reason := e.deviceRevoke(t, device); !rev || reason != scannerapp.ReasonMembershipEnded {
			t.Fatalf("device revoked = %v (%q), want membership_ended", rev, reason)
		}
		got := e.audits(t, "v1.scanner_app.device_revoke", device)
		if len(got) != 1 || got[0].ActorType != "device" || got[0].Reason != scannerapp.ReasonMembershipEnded {
			t.Fatalf("revoke audit = %+v", got)
		}
	})

	t.Run("self unpair", func(t *testing.T) {
		t1 := e.pair(t, e.organizerCode(t, a.org, tok).Code, "")
		if st, _, out := e.call(t, http.MethodDelete, "/v1/scanner-app/device", t1.AccessToken, "", ""); st != http.StatusNoContent {
			t.Fatalf("unpair: %d %s, want 204", st, out)
		}
		if st := e.meStatus(t, t1.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("/me after unpair: %d", st)
		}
		if st, _, _ := e.call(t, http.MethodDelete, "/v1/scanner-app/device", t1.AccessToken, "", ""); st != http.StatusUnauthorized {
			t.Fatalf("second unpair: %d, want 401", st)
		}
		got := e.audits(t, "v1.scanner_app.device_revoke", t1.Me.Device.ID)
		if len(got) != 1 || got[0].ActorType != "device" || got[0].Reason != scannerapp.ReasonSelfUnpaired {
			t.Fatalf("unpair audit = %+v", got)
		}
	})
}

func TestScannerAppMA11_Passes(t *testing.T) {
	e := newMA11Env(t)
	a := e.fixture(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")
	tok := e.token(t, owner)
	base := orgScanner(a.org)

	t.Run("default window and devices", func(t *testing.T) {
		p, link := e.createPass(t, a.org, tok, fmt.Sprintf(`{"label":"Main door","sessions":[%s,%s]}`,
			ma11Session(a.session, a.tier), ma11Session(a.session2)))
		var s1, s2 time.Time
		if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT (SELECT start_at FROM sessions WHERE id = $1), (SELECT start_at FROM sessions WHERE id = $2)`,
			a.session, a.session2).Scan(&s1, &s2); err != nil {
			t.Fatal(err)
		}
		if p.MaxDevices != scannerapp.DefaultPassDevices || p.Kind != scannerapp.PassKindStandard || p.Accepted != 0 || len(p.Sessions) != 2 {
			t.Fatalf("pass = %+v", p)
		}
		if p.ValidFrom == nil || !p.ValidFrom.Equal(s1.Add(-scannerapp.PassWindowBefore)) || p.ValidUntil == nil || !p.ValidUntil.Equal(s2.Add(scannerapp.PassWindowAfter)) {
			t.Fatalf("window = %v .. %v, want %v .. %v", p.ValidFrom, p.ValidUntil, s1.Add(-scannerapp.PassWindowBefore), s2.Add(scannerapp.PassWindowAfter))
		}
		if link.Code == "" || link.QRPNG == nil || link.ExpiresAt != nil {
			t.Fatalf("pass link = %+v", link)
		}
		e.pair(t, link.Code, "Anna")
		var got ma11Pass
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes/"+p.ID, tok, ""), &got)
		if got.Accepted != 1 || len(got.Devices) != 1 || got.Devices[0].ControllerName != "Anna" {
			t.Fatalf("pass after pairing = %+v", got)
		}
		var list struct {
			Passes []ma11Pass `json:"passes"`
		}
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes?event_id="+a.event.String(), tok, ""), &list)
		if len(list.Passes) == 0 || list.Passes[0].ID != p.ID {
			t.Fatalf("list = %+v", list)
		}
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes?event_id="+uuid.NewString(), tok, ""), &list)
		if len(list.Passes) != 0 {
			t.Fatalf("list of another event = %+v", list)
		}
		e.want(t, http.StatusBadRequest, "scanner_pass.invalid_filter", http.MethodGet, base+"/passes?event_id=nope", tok, "")

		// PATCH: below accepted, then a valid change.
		e.pair(t, link.Code, "Boris")
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.max_devices_below_accepted", http.MethodPatch, base+"/passes/"+p.ID, tok, `{"max_devices":1}`)
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid", http.MethodPatch, base+"/passes/"+p.ID, tok, `{"max_devices":51}`)
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_window", http.MethodPatch, base+"/passes/"+p.ID, tok,
			`{"valid_from":"2030-01-02T00:00:00Z","valid_until":"2030-01-01T00:00:00Z"}`)
		e.want(t, http.StatusBadRequest, "scanner_pass.invalid_body", http.MethodPatch, base+"/passes/"+p.ID, tok, `{}`)
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodPatch, base+"/passes/"+p.ID, tok, `{"label":"Side door","max_devices":5}`), &got)
		if got.Label != "Side door" || got.MaxDevices != 5 {
			t.Fatalf("patched = %+v", got)
		}
	})

	t.Run("validation", func(t *testing.T) {
		for name, body := range map[string]string{
			"max_devices 0":  ma11PassBody("X", 0, ma11Session(a.session)),
			"max_devices 51": ma11PassBody("X", 51, ma11Session(a.session)),
			"no sessions":    `{"label":"X","sessions":[]}`,
			"no label":       `{"label":" ","sessions":[` + ma11Session(a.session) + `]}`,
			"bad kind":       `{"label":"X","kind":"vip","sessions":[` + ma11Session(a.session) + `]}`,
		} {
			if st, _, out := e.call(t, http.MethodPost, base+"/passes", tok, "", body); st != http.StatusUnprocessableEntity || sec1Code(out) != "scanner_pass.invalid" {
				t.Errorf("%s: %d %s, want 422 scanner_pass.invalid", name, st, out)
			}
		}
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_window", http.MethodPost, base+"/passes", tok,
			`{"label":"X","valid_from":"2030-01-01T00:00:00Z","sessions":[`+ma11Session(a.session)+`]}`)
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.window_ended", http.MethodPost, base+"/passes", tok,
			`{"label":"X","valid_from":"2020-01-01T00:00:00Z","valid_until":"2020-01-02T00:00:00Z","sessions":[`+ma11Session(a.session)+`]}`)
		// A category of the other date of the same event.
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_tier", http.MethodPost, base+"/passes", tok,
			ma11PassBody("X", 2, ma11Session(a.session, a.tier2)))
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_session", http.MethodPost, base+"/passes", tok,
			ma11PassBody("X", 2, ma11Session(uuid.New())))
	})

	t.Run("demo pass", func(t *testing.T) {
		demo := `{"label":"Store review","kind":"demo","sessions":[` + ma11Session(a.session) + `]}`
		e.want(t, http.StatusForbidden, "scanner_pass.demo_forbidden", http.MethodPost, base+"/passes", tok, demo)
		_, superTok := e.superadmin(t)
		st, _, out, err := e.raw(http.MethodPost, base+"/passes", superTok, "", "MA-11 demo pass test", demo)
		if err != nil || st != http.StatusCreated {
			t.Fatalf("superadmin demo pass: %d %s %v, want 201", st, out, err)
		}
		var res struct {
			Pass ma11Pass `json:"pass"`
		}
		ma11Decode(t, out, &res)
		if res.Pass.Kind != scannerapp.PassKindDemo || res.Pass.ValidFrom != nil || res.Pass.ValidUntil != nil {
			t.Fatalf("demo pass = %+v", res.Pass)
		}
	})

	t.Run("delete leaves its devices upload only, a new link turns it on", func(t *testing.T) {
		p, link := e.createPass(t, a.org, tok, ma11PassBody("Night", 3, ma11Session(a.session)))
		if p.State != scannerapp.PassStateActive || p.ClosedAt != nil || p.UploadUntil != nil {
			t.Fatalf("new pass state = %+v", p)
		}
		d1, d2 := e.pair(t, link.Code, "One"), e.pair(t, link.Code, "Two")
		for _, d := range []ma11Tokens{d1, d2} {
			if st := e.meStatus(t, d.AccessToken); st != http.StatusOK {
				t.Fatalf("/me before delete: %d", st)
			}
		}
		e.want(t, http.StatusNoContent, "", http.MethodDelete, base+"/passes/"+p.ID, tok, "")
		var closed ma11Pass
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes/"+p.ID, tok, ""), &closed)
		if closed.State != scannerapp.PassStateClosed || closed.RevokedAt == nil || closed.ClosedAt == nil || !closed.ClosedAt.Equal(*closed.RevokedAt) ||
			closed.UploadUntil == nil || !closed.UploadUntil.Equal(closed.ClosedAt.Add(scannerapp.PassUploadGrace)) || closed.Accepted != 2 {
			t.Fatalf("turned-off pass = %+v", closed)
		}
		for _, d := range []ma11Tokens{d1, d2} {
			me, body := e.me(t, d.AccessToken, "")
			if me.Pass == nil || me.Pass.State != scannerapp.PassStateClosed || me.Pass.ClosedAt == nil || !me.Pass.ClosedAt.Equal(*closed.ClosedAt) ||
				me.Pass.UploadUntil == nil || !me.Pass.UploadUntil.Equal(*closed.UploadUntil) {
				t.Fatalf("/me after delete = %s, want pass closed", body)
			}
			if rev, _ := e.deviceRevoke(t, d.Me.Device.ID); rev {
				t.Fatalf("device %s revoked by the delete", d.Me.Device.ID)
			}
		}
		st, r1, out := e.refresh(t, d1.RefreshToken)
		if st != http.StatusOK || r1.AccessExpiresAt.After(*closed.UploadUntil) {
			t.Fatalf("refresh inside the upload term: %d %s", st, out)
		}
		e.want(t, http.StatusNoContent, "", http.MethodDelete, base+"/passes/"+p.ID, tok, "")
		st, _, out = e.call(t, http.MethodPost, "/v1/scanner-app/pair", "", ma11IP(), pairBody(link.Code, "Late"))
		if st != http.StatusNotFound {
			t.Fatalf("old link after delete: %d %s, want 404", st, out)
		}
		// A turned-off pass is changed and stays off; only a new link turns it on.
		var renamed ma11Pass
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodPatch, base+"/passes/"+p.ID, tok, `{"label":"Again"}`), &renamed)
		if renamed.Label != "Again" || renamed.RevokedAt == nil || renamed.State != scannerapp.PassStateClosed {
			t.Fatalf("turned-off pass after PATCH = %+v", renamed)
		}
		var fresh ma11Pairing
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodPost, base+"/passes/"+p.ID+"/link", tok, ""), &fresh)
		if fresh.Code == "" || fresh.Code == link.Code {
			t.Fatalf("new link = %+v", fresh)
		}
		var got ma11Pass
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes/"+p.ID, tok, ""), &got)
		if got.RevokedAt != nil || got.Accepted != 2 || got.State != scannerapp.PassStateActive || got.ClosedAt != nil || got.UploadUntil != nil {
			t.Fatalf("pass after a new link = %+v", got)
		}
		// Inside the upload term the devices work in full again.
		if me, body := e.me(t, d2.AccessToken, ""); me.Pass == nil || me.Pass.State != scannerapp.PassStateActive || me.Pass.ClosedAt != nil {
			t.Fatalf("/me after a new link = %s", body)
		}
		e.pair(t, fresh.Code, "Three")
		if len(e.audits(t, "v1.scanner_pass.revoke", p.ID)) != 1 || len(e.audits(t, "v1.scanner_pass.link", p.ID)) != 1 {
			t.Fatal("pass revoke/link audit missing")
		}
		// The window is over: a new link is refused until it is moved.
		e.want(t, http.StatusOK, "", http.MethodPatch, base+"/passes/"+p.ID, tok, `{"valid_from":"2020-01-01T00:00:00Z","valid_until":"2020-01-02T00:00:00Z"}`)
		e.want(t, http.StatusUnprocessableEntity, "scanner_pass.window_ended", http.MethodPost, base+"/passes/"+p.ID+"/link", tok, "")
	})

	t.Run("after the upload term: 401, revoked; a new link revokes the rest", func(t *testing.T) {
		start := time.Now().UTC().Truncate(time.Second)
		body := fmt.Sprintf(`{"label":"Late","max_devices":3,"valid_from":%q,"valid_until":%q,"sessions":[%s]}`,
			start.Add(-time.Hour).Format(time.RFC3339), start.Add(72*time.Hour).Format(time.RFC3339), ma11Session(a.session))
		p, link := e.createPass(t, a.org, tok, body)
		d1, d2 := e.pair(t, link.Code, "One"), e.pair(t, link.Code, "Two")
		e.want(t, http.StatusNoContent, "", http.MethodDelete, base+"/passes/"+p.ID, tok, "")
		// A second pass whose window ends in 30 seconds, with one phone.
		short, shortLink := e.createPass(t, a.org, tok, fmt.Sprintf(`{"label":"Short","max_devices":2,"valid_from":%q,"valid_until":%q,"sessions":[%s]}`,
			start.Add(-time.Hour).Format(time.RFC3339), start.Add(30*time.Second).Format(time.RFC3339), ma11Session(a.session)))
		d3 := e.pair(t, shortLink.Code, "Three")

		// The scanner routes read the server clock; move it past the term.
		old := e.srv.clk
		t.Cleanup(func() { e.srv.clk = old })
		moved := time.Now().Add(scannerapp.PassUploadGrace + time.Minute)
		e.srv.clk = clock.NewFake(moved)

		// A PATCH that leaves the short pass closed revokes nothing; one that
		// opens it revokes the phone past its term, audited by the person.
		e.want(t, http.StatusOK, "", http.MethodPatch, base+"/passes/"+short.ID, tok, `{"label":"Short, renamed"}`)
		if rev, _ := e.deviceRevoke(t, d3.Me.Device.ID); rev {
			t.Fatal("a PATCH that leaves the pass closed revoked its phone")
		}
		e.want(t, http.StatusOK, "", http.MethodPatch, base+"/passes/"+short.ID, tok,
			fmt.Sprintf(`{"valid_until":%q}`, moved.Add(5*time.Hour).UTC().Format(time.RFC3339)))
		if rev, reason := e.deviceRevoke(t, d3.Me.Device.ID); !rev || reason != scannerapp.ReasonPassEnded {
			t.Fatalf("device of the reopened pass = %v %q, want pass_ended", rev, reason)
		}
		if got := e.audits(t, "v1.scanner_app.device_revoke", d3.Me.Device.ID); len(got) != 1 || got[0].ActorType != "user" ||
			got[0].ActorID != owner.String() || got[0].Reason != scannerapp.ReasonPassEnded {
			t.Fatalf("device_revoke audit of the PATCH = %+v", got)
		}

		if st := e.meStatus(t, d1.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("/me after the upload term: %d, want 401", st)
		}
		st, _, out := e.refresh(t, d1.RefreshToken)
		if st != http.StatusUnauthorized || sec1Code(out) != "scanner_app.device_revoked" {
			t.Fatalf("refresh after the upload term: %d %s, want 401 scanner_app.device_revoked", st, out)
		}
		if rev, reason := e.deviceRevoke(t, d1.Me.Device.ID); !rev || reason != scannerapp.ReasonPassRevoked {
			t.Fatalf("device = %v %q, want pass_revoked", rev, reason)
		}
		if got := e.audits(t, "v1.scanner_app.device_revoke", d1.Me.Device.ID); len(got) != 1 || got[0].ActorType != "device" || got[0].Reason != scannerapp.ReasonPassRevoked {
			t.Fatalf("device_revoke audit = %+v", got)
		}
		// d2 never refreshed; turning the pass on again revokes it.
		e.want(t, http.StatusOK, "", http.MethodPost, base+"/passes/"+p.ID+"/link", tok, "")
		if rev, reason := e.deviceRevoke(t, d2.Me.Device.ID); !rev || reason != scannerapp.ReasonPassEnded {
			t.Fatalf("device = %v %q, want pass_ended", rev, reason)
		}
		if got := e.audits(t, "v1.scanner_app.device_revoke", d2.Me.Device.ID); len(got) != 1 || got[0].ActorType != "user" ||
			got[0].ActorID != owner.String() || got[0].Reason != scannerapp.ReasonPassEnded {
			t.Fatalf("device_revoke audit of the new link = %+v", got)
		}
		var got ma11Pass
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/passes/"+p.ID, tok, ""), &got)
		if got.State != scannerapp.PassStateActive || got.Accepted != 0 {
			t.Fatalf("pass after a late new link = %+v", got)
		}
	})

	t.Run("organizer devices", func(t *testing.T) {
		d := e.pair(t, e.organizerCode(t, a.org, tok).Code, "")
		var list struct {
			Devices []struct {
				ID     string  `json:"id"`
				Kind   string  `json:"kind"`
				UserID *string `json:"user_id"`
			} `json:"devices"`
		}
		ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, base+"/devices", tok, ""), &list)
		found := false
		for _, x := range list.Devices {
			if x.ID == d.Me.Device.ID && x.Kind == scannerapp.KindOrganizer && x.UserID != nil && *x.UserID == owner.String() {
				found = true
			}
		}
		if !found {
			t.Fatalf("device %s missing from %+v", d.Me.Device.ID, list)
		}
		e.want(t, http.StatusNoContent, "", http.MethodDelete, base+"/devices/"+d.Me.Device.ID, tok, "")
		e.want(t, http.StatusNoContent, "", http.MethodDelete, base+"/devices/"+d.Me.Device.ID, tok, "")
		e.want(t, http.StatusNotFound, "scanner.device_not_found", http.MethodDelete, base+"/devices/"+uuid.NewString(), tok, "")
		if st := e.meStatus(t, d.AccessToken); st != http.StatusUnauthorized {
			t.Fatalf("/me of a revoked device: %d", st)
		}
		got := e.audits(t, "v1.scanner_app.device_revoke", d.Me.Device.ID)
		if len(got) != 1 || got[0].ActorType != "user" || got[0].ActorID != owner.String() || got[0].Reason != scannerapp.ReasonRevokedByUser {
			t.Fatalf("manual revoke audit = %+v", got)
		}
	})

	t.Run("superadmin without own membership gets no organizer code", func(t *testing.T) {
		_, superTok := e.superadmin(t)
		st, _, out, err := e.raw(http.MethodPost, base+"/pairing-codes", superTok, "", "MA-11 code test", "")
		if err != nil || st != http.StatusForbidden || sec1Code(out) != "scanner.membership_required" {
			t.Fatalf("superadmin code: %d %s %v, want 403 scanner.membership_required", st, out, err)
		}
	})
}

// A manager (role organizer) passes every new organizer route, an agent is
// refused by the permission gate, an API key too.
func TestScannerAppMA11_RolesAndKeys(t *testing.T) {
	e := newMA11Env(t)
	a := e.fixture(t, "A")
	manager, agent := e.user(t, "manager"), e.user(t, "agent")
	e.member(t, manager, a.org, "organizer")
	e.member(t, agent, a.org, "agent")
	mTok, aTok := e.token(t, manager), e.token(t, agent)
	key := e.key(t, a.org, "event.read")
	base := orgScanner(a.org)

	p, _ := e.createPass(t, a.org, mTok, ma11PassBody("Manager pass", 2, ma11Session(a.session)))
	routes := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPost, base + "/pairing-codes", "", http.StatusCreated},
		{http.MethodGet, base + "/devices", "", http.StatusOK},
		{http.MethodDelete, base + "/devices/" + uuid.NewString(), "", http.StatusNotFound},
		{http.MethodPost, base + "/passes", ma11PassBody("Manager pass 2", 2, ma11Session(a.session)), http.StatusCreated},
		{http.MethodGet, base + "/passes", "", http.StatusOK},
		{http.MethodGet, base + "/passes/" + p.ID, "", http.StatusOK},
		{http.MethodPatch, base + "/passes/" + p.ID, `{"label":"Renamed"}`, http.StatusOK},
		{http.MethodPost, base + "/passes/" + p.ID + "/link", "", http.StatusOK},
		{http.MethodDelete, base + "/passes/" + p.ID, "", http.StatusNoContent},
	}
	for _, rt := range routes {
		if st, _, out := e.call(t, rt.method, rt.path, mTok, "", rt.body); st != rt.want {
			t.Errorf("manager %s %s: %d %s, want %d", rt.method, rt.path, st, out, rt.want)
		}
	}
	for _, rt := range routes {
		if st, _, out := e.call(t, rt.method, rt.path, aTok, "", rt.body); st != http.StatusForbidden {
			t.Errorf("agent %s %s: %d %s, want 403", rt.method, rt.path, st, out)
		}
		if st, _, out := e.call(t, rt.method, rt.path, key, "", rt.body); st != http.StatusForbidden {
			t.Errorf("API key %s %s: %d %s, want 403", rt.method, rt.path, st, out)
		}
	}
}

// Another organization's devices and passes are the route's own 404, its
// dates a 422.
func TestScannerAppMA11_OtherOrganization(t *testing.T) {
	e := newMA11Env(t)
	a, b := e.fixture(t, "A"), e.fixture(t, "B")
	ownerA, ownerB := e.user(t, "ownerA"), e.user(t, "ownerB")
	e.member(t, ownerA, a.org, "org_admin")
	e.member(t, ownerB, b.org, "org_admin")
	tokA, tokB := e.token(t, ownerA), e.token(t, ownerB)

	pass, link := e.createPass(t, a.org, tokA, ma11PassBody("A pass", 2, ma11Session(a.session)))
	dev := e.pair(t, link.Code, "A controller")
	orgDev := e.pair(t, e.organizerCode(t, a.org, tokA).Code, "")
	baseB := orgScanner(b.org)

	e.want(t, http.StatusNotFound, "scanner.device_not_found", http.MethodDelete, baseB+"/devices/"+dev.Me.Device.ID, tokB, "")
	e.want(t, http.StatusNotFound, "scanner.device_not_found", http.MethodDelete, baseB+"/devices/"+orgDev.Me.Device.ID, tokB, "")
	e.want(t, http.StatusNotFound, "scanner_pass.not_found", http.MethodGet, baseB+"/passes/"+pass.ID, tokB, "")
	e.want(t, http.StatusNotFound, "scanner_pass.not_found", http.MethodPatch, baseB+"/passes/"+pass.ID, tokB, `{"label":"Mine"}`)
	e.want(t, http.StatusNotFound, "scanner_pass.not_found", http.MethodDelete, baseB+"/passes/"+pass.ID, tokB, "")
	e.want(t, http.StatusNotFound, "scanner_pass.not_found", http.MethodPost, baseB+"/passes/"+pass.ID+"/link", tokB, "")
	e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_session", http.MethodPost, baseB+"/passes", tokB,
		ma11PassBody("Stolen", 2, ma11Session(a.session)))
	own, _ := e.createPass(t, b.org, tokB, ma11PassBody("B pass", 2, ma11Session(b.session)))
	e.want(t, http.StatusUnprocessableEntity, "scanner_pass.invalid_session", http.MethodPatch, baseB+"/passes/"+own.ID, tokB,
		`{"sessions":[`+ma11Session(a.session)+`]}`)
	// The owner of B is not a member of A.
	e.want(t, http.StatusForbidden, "", http.MethodGet, orgScanner(a.org)+"/passes/"+pass.ID, tokB, "")

	var list struct {
		Devices []struct {
			ID string `json:"id"`
		} `json:"devices"`
	}
	ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, baseB+"/devices", tokB, ""), &list)
	if len(list.Devices) != 0 {
		t.Fatalf("org B sees devices %+v", list.Devices)
	}

	// Nothing of A moved.
	for _, d := range []ma11Tokens{dev, orgDev} {
		if st := e.meStatus(t, d.AccessToken); st != http.StatusOK {
			t.Fatalf("device of A after the attack: %d", st)
		}
	}
	var got ma11Pass
	ma11Decode(t, e.want(t, http.StatusOK, "", http.MethodGet, orgScanner(a.org)+"/passes/"+pass.ID, tokA, ""), &got)
	if got.Label != "A pass" || got.RevokedAt != nil || got.Accepted != 1 {
		t.Fatalf("pass of A after the attack = %+v", got)
	}
}
