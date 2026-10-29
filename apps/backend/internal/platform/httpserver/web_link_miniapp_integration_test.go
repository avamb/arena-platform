//go:build integration

// web_link_miniapp_integration_test.go — the sign-in code of purpose
// "miniapp" (spec 44, RT-12a, migration 0169): the Telegram mini app editor.
// Its session is 30 minutes of channel event_center_tg, the chat gets no
// "signed in" notice, and it neither takes back nor is taken back by a code of
// another purpose. A platform role gets no code.

package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

func TestWebLinkMiniapp_ThirtyMinuteSessionNoNoticeOtherCodesUntouched(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.BotServiceToken = webLinkServiceToken
		c.EventCenterPublicURL = "https://events.example.test"
		c.WebSessionIdleHours = 72
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()
	service := map[string]string{"Authorization": "Bearer " + webLinkServiceToken}

	nonce := uuid.NewString()[:8]
	hash, err := users.HashPassword("a long phrase 1")
	if err != nil {
		t.Fatal(err)
	}
	newUser := func(label string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
			label+"-"+nonce+"@example.test", hash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID, adminID := newUser("miniapp"), newUser("miniapp-sa")
	tg, tgAdmin := 7_000_000_000+rand.Int64N(1_000_000_000), 8_000_000_000+rand.Int64N(1_000_000_000)
	q := gen.New(pool)
	if _, err := q.UpsertBotTelegramLink(ctx, tg, userID, nil, "ru", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertBotTelegramLink(ctx, tgAdmin, adminID, nil, "en", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{userID, adminID} {
			_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM web_login_codes WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		}
	})

	// The purpose is a CHECK of the table, not only a Go string (migration 0169).
	if _, err := pool.Exec(ctx, `INSERT INTO web_login_codes (code_hash, user_id, telegram_user_id, expires_at, purpose)
		VALUES ($1, $2, $3, now() + interval '5 minutes', 'bogus')`, users.TokenHash("bogus-"+nonce), userID, tg); err == nil || !strings.Contains(err.Error(), "web_login_codes_purpose_check") {
		t.Fatalf("a code of an unknown purpose was stored: %v", err)
	}

	event := uuid.NewString()
	issue := func(telegramID int64, extra string) (int, map[string]any) {
		return c.do(http.MethodPost, "/v1/bot/web-login-codes", "", fmt.Sprintf(`{"telegram_user_id":%d%s}`, telegramID, extra), service)
	}
	issueMiniapp := func(kind string) (int, map[string]any) {
		return issue(tg, fmt.Sprintf(`,"purpose":"miniapp","kind":%q,"event_id":%q`, kind, event))
	}
	codeFrom := func(out map[string]any, prefix string) string {
		u, _ := out["url"].(string)
		if !strings.HasPrefix(u, prefix) {
			t.Fatalf("link %q does not start with %q", u, prefix)
		}
		code, _, _ := strings.Cut(strings.TrimPrefix(u, prefix), "&") // the fragment carries kind and event after the code
		return code
	}
	exchange := func(code string) (int, map[string]any) {
		return c.do(http.MethodPost, "/v1/auth/web/link", "", fmt.Sprintf(`{"code":%q}`, code), nil)
	}
	purposeOf := func(code string) string {
		var p string
		if err := pool.QueryRow(ctx, `SELECT purpose FROM web_login_codes WHERE code_hash = $1`, users.TokenHash(code)).Scan(&p); err != nil {
			t.Fatalf("read purpose: %v", err)
		}
		return p
	}

	// A code of an unknown purpose, and a miniapp code without a target, are
	// refused before anything is stored.
	if code, out := issue(tg, `,"purpose":"admin"`); code != http.StatusBadRequest || errCode(out) != "bot.invalid_purpose" {
		t.Fatalf("unknown purpose: %d %v", code, out)
	}
	for _, extra := range []string{
		`,"purpose":"miniapp"`,
		fmt.Sprintf(`,"purpose":"miniapp","kind":"poster","event_id":%q`, event),
		`,"purpose":"miniapp","kind":"description","event_id":"nope"`,
		// INV-05: the invitations screen names a session as well as the event
		fmt.Sprintf(`,"purpose":"miniapp","kind":"invitations","event_id":%q`, event),
		fmt.Sprintf(`,"purpose":"miniapp","kind":"invitations","event_id":%q,"session_id":"nope"`, event),
	} {
		if code, out := issue(tg, extra); code != http.StatusBadRequest || errCode(out) != "bot.invalid_miniapp_target" {
			t.Fatalf("miniapp target %s: %d %v", extra, code, out)
		}
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE user_id = $1`, userID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("a refused request stored %d codes (%v)", stored, err)
	}

	// A platform role gets no miniapp code, and a code drawn before the role
	// was granted is void at the exchange.
	if code, out := issue(tgAdmin, fmt.Sprintf(`,"purpose":"miniapp","kind":"description","event_id":%q`, event)); code != http.StatusForbidden || errCode(out) != "bot.web_login_superadmin" {
		t.Fatalf("superadmin miniapp code: %d %v", code, out)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE user_id = $1`, adminID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("a superadmin has %d codes (%v)", stored, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO web_login_codes (code_hash, user_id, telegram_user_id, expires_at, purpose)
		VALUES ($1, $2, $3, now() + interval '5 minutes', 'miniapp')`, users.TokenHash("sa-code-"+nonce), adminID, tgAdmin); err != nil {
		t.Fatal(err)
	}
	if code, out := exchange("sa-code-" + nonce); code != http.StatusForbidden || errCode(out) != "auth.web_link_superadmin" {
		t.Fatalf("a superadmin's miniapp code at the exchange: %d %v", code, out)
	}

	// An open web code is not touched by a miniapp code, and the other way round.
	_, webOut := issue(tg, "")
	webCode := codeFrom(webOut, "https://events.example.test/login/link#code=")
	if purposeOf(webCode) != "web" {
		t.Fatalf("the default purpose is %q", purposeOf(webCode))
	}
	code, out := issueMiniapp("description")
	if code != http.StatusCreated {
		t.Fatalf("miniapp code: %d %v", code, out)
	}
	prefix := "https://events.example.test/tg/edit#code="
	first := codeFrom(out, prefix)
	if want := prefix + first + "&kind=description&event=" + event; out["url"] != want {
		t.Fatalf("link %v, want %s", out["url"], want)
	}
	if exp, _ := out["expires_at"].(string); exp == "" {
		t.Fatalf("no expires_at: %v", out)
	}
	if purposeOf(first) != "miniapp" {
		t.Fatalf("purpose of the miniapp code is %q", purposeOf(first))
	}
	// INV-05: the staff invitations screen of one session. The sign-in code
	// is in the fragment, the session and the event in the query.
	sessionID := uuid.NewString()
	icode, iout := issue(tg, fmt.Sprintf(`,"purpose":"miniapp","kind":"invitations","event_id":%q,"session_id":%q`, event, sessionID))
	if icode != http.StatusCreated {
		t.Fatalf("invitations code: %d %v", icode, iout)
	}
	iurl, _ := iout["url"].(string)
	ipre := "https://events.example.test/tg/invitations?session=" + sessionID + "&event=" + event + "#code="
	if !strings.HasPrefix(iurl, ipre) || purposeOf(strings.TrimPrefix(iurl, ipre)) != "miniapp" {
		t.Fatalf("invitations link %q, want %s<code> of purpose miniapp", iurl, ipre)
	}
	var revoked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE code_hash = $1 AND revoked_at IS NOT NULL`, users.TokenHash(webCode)).Scan(&revoked); err != nil || revoked != 0 {
		t.Fatalf("issuing a miniapp code took back the web code (%d, %v)", revoked, err)
	}
	// A newer miniapp code replaces the older miniapp code only.
	_, out2 := issueMiniapp("email_note")
	second := codeFrom(out2, prefix)
	if !strings.HasSuffix(out2["url"].(string), "&kind=email_note&event="+event) {
		t.Fatalf("note link %v", out2["url"])
	}
	if code, o := exchange(first); code != http.StatusBadRequest || errCode(o) != "auth.web_link_invalid" {
		t.Fatalf("replaced miniapp code: %d %v", code, o)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE code_hash = $1 AND revoked_at IS NOT NULL`, users.TokenHash(webCode)).Scan(&revoked); err != nil || revoked != 0 {
		t.Fatalf("a second miniapp code took back the web code (%d, %v)", revoked, err)
	}
	// A new web code does not take back the open miniapp code either.
	_, webOut2 := issue(tg, `,"purpose":"web"`)
	webCode2 := codeFrom(webOut2, "https://events.example.test/login/link#code=")
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE code_hash = $1 AND revoked_at IS NOT NULL`, users.TokenHash(second)).Scan(&revoked); err != nil || revoked != 0 {
		t.Fatalf("a web code took back the miniapp code (%d, %v)", revoked, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web_login_codes WHERE code_hash = $1 AND revoked_at IS NOT NULL`, users.TokenHash(webCode)).Scan(&revoked); err != nil || revoked != 1 {
		t.Fatalf("a new web code must take back the older web code (%d, %v)", revoked, err)
	}

	// The exchange: a 30-minute session of channel event_center_tg.
	code, signedIn := exchange(second)
	if code != http.StatusOK {
		t.Fatalf("miniapp exchange: %d %v", code, signedIn)
	}
	refresh := signedIn["refresh_token"].(string)
	s := readTokenSession(t, pool, refresh)
	if s.channel == nil || *s.channel != "event_center_tg" || s.idle == nil || *s.idle != 30*60 || s.started == nil || s.sessionID == nil {
		t.Fatalf("session of a miniapp code: %+v", s)
	}
	if got := s.expiresAt.Sub(*s.started); got != 30*time.Minute {
		t.Fatalf("session length %v, want 30m", got)
	}
	if !near(s.expiresAt, time.Now().Add(30*time.Minute)) {
		t.Fatalf("session ends %v", s.expiresAt)
	}
	if chn, sid := accessSessionClaims(t, signedIn["access_token"].(string)); chn != "event_center_tg" || sid != s.sessionID.String() {
		t.Fatalf("access token: chn=%q sid=%q", chn, sid)
	}
	// It works once.
	if code, o := exchange(second); code != http.StatusBadRequest || errCode(o) != "auth.web_link_invalid" {
		t.Fatalf("second exchange: %d %v", code, o)
	}
	// A refresh rotates the token and never moves the end of the session.
	code, refreshed := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, refresh), nil)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %v", code, refreshed)
	}
	s2 := readTokenSession(t, pool, refreshed["refresh_token"].(string))
	if s2.channel == nil || *s2.channel != "event_center_tg" || s2.expiresAt.After(s.expiresAt) || s2.sessionID == nil || *s2.sessionID != *s.sessionID {
		t.Fatalf("refreshed session %+v, first %+v", s2, s)
	}
	if chn, _ := accessSessionClaims(t, refreshed["access_token"].(string)); chn != "event_center_tg" {
		t.Fatalf("refreshed access token chn=%q", chn)
	}
	// The money routes still ask this session for the password.
	stepUp := fmt.Sprintf(`{"password":"a long phrase 1","org_id":%q}`, uuid.NewString())
	if code, o := c.do(http.MethodPost, "/v1/auth/step-up", refreshed["access_token"].(string), stepUp, nil); code != http.StatusOK {
		t.Fatalf("step-up in a miniapp session: %d %v", code, o)
	}

	// No notice for the miniapp sign-in. The web code, used next, has one.
	claim := func() []any {
		code, o := c.do(http.MethodPost, "/v1/bot/web-login-notices/claim", "", `{}`, service)
		if code != http.StatusOK {
			t.Fatalf("claim: %d %v", code, o)
		}
		list, _ := o["notices"].([]any)
		var mine []any
		for _, n := range list {
			if m, _ := n.(map[string]any); m != nil && int64(m["telegram_user_id"].(float64)) == tg {
				mine = append(mine, n)
			}
		}
		return mine
	}
	if got := claim(); len(got) != 0 {
		t.Fatalf("a miniapp sign-in produced a notice: %v", got)
	}
	// The web code was still open: it signs in as a web session with a notice.
	code, webIn := exchange(webCode2)
	if code != http.StatusOK {
		t.Fatalf("web exchange after a miniapp sign-in: %d %v", code, webIn)
	}
	ws := readTokenSession(t, pool, webIn["refresh_token"].(string))
	if ws.channel == nil || *ws.channel != "event_center_web" || ws.idle == nil || *ws.idle != 72*3600 {
		t.Fatalf("web session: %+v", ws)
	}
	if got := claim(); len(got) != 1 {
		t.Fatalf("notices after the web sign-in: %v", got)
	}

	// "Sign out everywhere" ends the miniapp session too and takes back an
	// open miniapp code.
	_, out3 := issueMiniapp("description")
	third := codeFrom(out3, prefix)
	code, rev := c.do(http.MethodPost, "/v1/auth/web-sessions/revoke", webIn["access_token"].(string), "", nil)
	if code != http.StatusOK || rev["revoked_sessions"].(float64) < 2 {
		t.Fatalf("revoke: %d %v", code, rev)
	}
	if code, o := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, refreshed["refresh_token"]), nil); code != http.StatusUnauthorized || errCode(o) != "auth.refresh_token_expired" {
		t.Fatalf("a miniapp session after sign out everywhere: %d %v", code, o)
	}
	if code, o := exchange(third); code != http.StatusBadRequest || errCode(o) != "auth.web_link_invalid" {
		t.Fatalf("an open miniapp code after sign out everywhere: %d %v", code, o)
	}

	// The audit row names the channel and the purpose.
	var via, purpose string
	if err := pool.QueryRow(ctx, `SELECT metadata->>'via', metadata->>'purpose' FROM audit_events
		WHERE actor_id = $1 AND action = 'v1.auth.web_link_login' AND metadata->>'purpose' = 'miniapp'`, userID).Scan(&via, &purpose); err != nil {
		t.Fatalf("audit row of the miniapp sign-in: %v", err)
	}
	if via != "event_center_tg" || purpose != "miniapp" {
		t.Fatalf("audit via=%q purpose=%q", via, purpose)
	}
}

// accessExp is the "exp" claim of an access token.
func accessExp(t *testing.T, token string) time.Time {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		t.Fatalf("no exp in %s (%v)", raw, err)
	}
	return time.Unix(claims.Exp, 0)
}

// B1: the access token of a mini app session never outlives the session. A
// refresh at minute 29 gives a token that ends at minute 30 at the latest (not
// at minute 44), and a refresh after the end is refused.
func TestWebLinkMiniapp_AccessTokenNeverOutlivesTheSession(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.BotServiceToken = webLinkServiceToken
		c.EventCenterPublicURL = "https://events.example.test"
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()
	service := map[string]string{"Authorization": "Bearer " + webLinkServiceToken}

	hash, err := users.HashPassword("a long phrase 1")
	if err != nil {
		t.Fatal(err)
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		"miniapp-ttl-"+uuid.NewString()[:8]+"@example.test", hash).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	tg := 6_000_000_000 + rand.Int64N(1_000_000_000)
	if _, err := gen.New(pool).UpsertBotTelegramLink(ctx, tg, userID, nil, "ru", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM web_login_codes WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})

	code, out := c.do(http.MethodPost, "/v1/bot/web-login-codes", "",
		fmt.Sprintf(`{"telegram_user_id":%d,"purpose":"miniapp","kind":"description","event_id":%q}`, tg, uuid.NewString()), service)
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %v", code, out)
	}
	link, _ := out["url"].(string)
	miniCode, _, _ := strings.Cut(strings.TrimPrefix(link, "https://events.example.test/tg/edit#code="), "&")
	code, signedIn := c.do(http.MethodPost, "/v1/auth/web/link", "", fmt.Sprintf(`{"code":%q}`, miniCode), nil)
	if code != http.StatusOK {
		t.Fatalf("exchange: %d %v", code, signedIn)
	}
	refresh := signedIn["refresh_token"].(string)
	first := readTokenSession(t, pool, refresh)
	// At the exchange the usual 15 minutes fit into the 30 of the session.
	if exp := accessExp(t, signedIn["access_token"].(string)); exp.After(first.expiresAt) || exp.Before(time.Now().Add(13*time.Minute)) {
		t.Fatalf("access token at the exchange ends %v, session ends %v", exp, first.expiresAt)
	}

	// Minute 29 of the session: move its start (and the end of the token) back.
	ageSession := func(raw string, by string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE refresh_tokens
			SET session_started_at = session_started_at - $2::interval, expires_at = expires_at - $2::interval
			WHERE token = $1`, users.TokenHash(raw), by); err != nil {
			t.Fatal(err)
		}
	}
	ageSession(refresh, "29 minutes")
	aged := readTokenSession(t, pool, refresh)
	code, refreshed := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, refresh), nil)
	if code != http.StatusOK {
		t.Fatalf("refresh at minute 29: %d %v", code, refreshed)
	}
	next := readTokenSession(t, pool, refreshed["refresh_token"].(string))
	end := aged.started.Add(30 * time.Minute)
	if next.expiresAt.After(end) {
		t.Fatalf("the refreshed session ends %v, after the end of the session %v", next.expiresAt, end)
	}
	exp := accessExp(t, refreshed["access_token"].(string))
	if exp.After(end.Add(time.Second)) {
		t.Fatalf("access token ends %v, after the end of the session %v (it used to live to minute 44)", exp, end)
	}
	if exp.After(time.Now().Add(2 * time.Minute)) {
		t.Fatalf("access token ends %v: about a minute was left of the session", exp)
	}
	if got, _ := refreshed["expires_at"].(string); got == "" {
		t.Fatalf("no expires_at in the answer: %v", refreshed)
	}

	// After the end the refresh token is refused.
	ageSession(refreshed["refresh_token"].(string), "2 minutes")
	if code, o := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, refreshed["refresh_token"]), nil); code != http.StatusUnauthorized || errCode(o) != "auth.refresh_token_expired" {
		t.Fatalf("refresh after the end of the session: %d %v", code, o)
	}
}
