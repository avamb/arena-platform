//go:build integration

// telegram_miniapp_integration_test.go — POST /v1/auth/telegram-miniapp (task
// S3b): the signed launch data of the Telegram Mini App becomes a 30-minute
// session of channel event_center_tg, for a Telegram account the bot has
// linked to a person and nobody else.

package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hauth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

const miniappTestBotToken = "5551234567:AA-miniapp-integration-test-token"

// miniappInitData signs launch data the way Telegram does.
func miniappInitData(t *testing.T, key []byte, telegramID int64, authDate time.Time) string {
	t.Helper()
	return miniappInitDataWith(t, key, telegramID, authDate, nil)
}

// miniappInitDataWith signs launch data that also carries the extra fields
// (for example the signed start_param).
func miniappInitDataWith(t *testing.T, key []byte, telegramID int64, authDate time.Time, extra map[string]string) string {
	t.Helper()
	fields := map[string]string{
		"auth_date":     strconv.FormatInt(authDate.Unix(), 10),
		"query_id":      "AAH" + uuid.NewString()[:8],
		"user":          fmt.Sprintf(`{"id":%d,"first_name":"Тест","username":"t%d","language_code":"ru"}`, telegramID, telegramID),
		"chat_type":     "private",
		"chat_instance": "-1234567890",
	}
	for k, v := range extra {
		fields[k] = v
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	lines := make([]string, len(names))
	for i, k := range names {
		lines[i] = k + "=" + fields[k]
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strings.Join(lines, "\n")))
	v := url.Values{}
	for k, val := range fields {
		v.Set(k, val)
	}
	v.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return v.Encode()
}

func miniappBody(initData string) string {
	return fmt.Sprintf(`{"init_data":%q}`, initData)
}

func TestTelegramMiniappLogin_SessionBindingAndRefusals(t *testing.T) {
	key := hauth.DeriveMiniappSecretKey(miniappTestBotToken)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.TelegramMiniappSecretKey = hex.EncodeToString(key)
		c.WebSessionIdleHours = 72
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	pool := srv.pgxPool
	ctx := context.Background()
	const path = "/v1/auth/telegram-miniapp"

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
	person, other, admin, gone, erased := newUser("tgmini"), newUser("tgmini-other"), newUser("tgmini-sa"), newUser("tgmini-off"), newUser("tgmini-erased")
	tgID := func() int64 { return 9_000_000_000 + rand.Int64N(900_000_000) }
	tgPerson, tgOther, tgAdmin, tgGone, tgRevoked, tgStranger, tgErased := tgID(), tgID(), tgID(), tgID(), tgID(), tgID(), tgID()
	q := gen.New(pool)
	for _, l := range []struct {
		tg   int64
		user uuid.UUID
	}{{tgPerson, person}, {tgOther, other}, {tgAdmin, admin}, {tgGone, gone}, {tgRevoked, other}, {tgErased, erased}} {
		if _, err := q.UpsertBotTelegramLink(ctx, l.tg, l.user, nil, "ru", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE bot_telegram_links SET revoked_at = now() WHERE telegram_user_id = $1`, tgRevoked); err != nil {
		t.Fatal(err)
	}
	// A GDPR-erased account keeps its row with the placeholder address.
	if _, err := pool.Exec(ctx, `UPDATE users SET email = 'deleted-' || id::text || '@arena.invalid', anonymized_at = now() WHERE id = $1`, erased); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET deactivated_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{person, other, admin, gone, erased} {
			_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE actor_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM bot_telegram_links WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
		}
	})
	refreshCount := func(id uuid.UUID) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expect := func(name, body string, status int, code string) {
		t.Helper()
		got, out := c.do(http.MethodPost, path, "", body, nil)
		if got != status || (code != "" && errCode(out) != code) {
			t.Fatalf("%s: %d %v, want %d %s", name, got, out, status, code)
		}
	}
	now := time.Now()

	// Forged: a signature made with the bot token itself, another bot's key,
	// a tampered field, a missing hash.
	expect("signed with the bot token as the key", miniappBody(miniappInitData(t, []byte(miniappTestBotToken), tgPerson, now)),
		http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	expect("signed with another bot's key", miniappBody(miniappInitData(t, hauth.DeriveMiniappSecretKey("1:other"), tgPerson, now)),
		http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	good := miniappInitData(t, key, tgOther, now)
	swapped := strings.Replace(good, url.QueryEscape(fmt.Sprintf(`"id":%d`, tgOther)), url.QueryEscape(fmt.Sprintf(`"id":%d`, tgPerson)), 1)
	if swapped == good {
		t.Fatal("test bug: the user id was not swapped")
	}
	expect("another person's id swapped into signed data", miniappBody(swapped), http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	expect("hash removed", miniappBody(strings.Join(filterParams(good, "hash"), "&")), http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	expect("garbage", miniappBody("hello"), http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	expect("not json", "init_data=x", http.StatusBadRequest, "http.invalid_json")
	for _, id := range []uuid.UUID{person, other} {
		if n := refreshCount(id); n != 0 {
			t.Fatalf("a refused request left %d refresh tokens", n)
		}
	}

	// Expired: genuinely signed, 6 minutes old; and dated far ahead.
	expect("expired", miniappBody(miniappInitData(t, key, tgPerson, now.Add(-6*time.Minute))), http.StatusUnauthorized, "auth.telegram_init_data_expired")
	expect("from the future", miniappBody(miniappInitData(t, key, tgPerson, now.Add(10*time.Minute))), http.StatusUnauthorized, "auth.telegram_init_data_expired")

	// Genuine data of an account the bot never linked, or whose link was
	// revoked: refused, and no session of anyone is issued.
	expect("stranger", miniappBody(miniappInitData(t, key, tgStranger, now)), http.StatusForbidden, "auth.telegram_not_linked")
	expect("revoked link", miniappBody(miniappInitData(t, key, tgRevoked, now)), http.StatusForbidden, "auth.telegram_not_linked")
	// Deactivated and platform superadmin.
	expect("deactivated", miniappBody(miniappInitData(t, key, tgGone, now)), http.StatusForbidden, "auth.user_deactivated")
	expect("erased account", miniappBody(miniappInitData(t, key, tgErased, now)), http.StatusForbidden, "auth.user_deactivated")
	expect("superadmin", miniappBody(miniappInitData(t, key, tgAdmin, now)), http.StatusForbidden, "auth.web_link_superadmin")
	for _, id := range []uuid.UUID{other, admin, gone, erased} {
		if n := refreshCount(id); n != 0 {
			t.Fatalf("a refused sign-in left %d refresh tokens for %s", n, id)
		}
	}

	// A valid launch: the linked person gets a 30-minute session of the mini
	// app's own channel.
	code, signedIn := c.do(http.MethodPost, path, "", miniappBody(miniappInitData(t, key, tgPerson, now.Add(-time.Minute))), nil)
	if code != http.StatusOK {
		t.Fatalf("valid launch: %d %v", code, signedIn)
	}
	if signedIn["user_id"] != person.String() {
		t.Fatalf("signed in as %v, want %s", signedIn["user_id"], person)
	}
	refresh, _ := signedIn["refresh_token"].(string)
	access, _ := signedIn["access_token"].(string)
	if refresh == "" || access == "" {
		t.Fatalf("answer without tokens: %v", signedIn)
	}
	s := readTokenSession(t, pool, refresh)
	if s.channel == nil || *s.channel != "event_center_tg" || s.idle == nil || *s.idle != 30*60 || s.started == nil || s.sessionID == nil {
		t.Fatalf("session: %+v", s)
	}
	if got := s.expiresAt.Sub(*s.started); got != 30*time.Minute {
		t.Fatalf("session length %v, want 30m", got)
	}
	if chn, sid := accessSessionClaims(t, access); chn != "event_center_tg" || sid != s.sessionID.String() {
		t.Fatalf("access token: chn=%q sid=%q", chn, sid)
	}
	if exp := accessExp(t, access); exp.After(s.expiresAt.Add(2 * time.Second)) {
		t.Fatalf("access token outlives the session: %v > %v", exp, s.expiresAt)
	}
	// The same launch data works again inside its five minutes (a page reload
	// reuses it) and the new session is independent.
	if code, again := c.do(http.MethodPost, path, "", miniappBody(miniappInitData(t, key, tgPerson, now)), nil); code != http.StatusOK || again["refresh_token"] == signedIn["refresh_token"] {
		t.Fatalf("second launch: %d %v", code, again)
	}
	// The bearer works for the person, and the money routes still ask for the password.
	if code, me := c.do(http.MethodGet, "/v1/me", access, "", nil); code != http.StatusOK {
		t.Fatalf("/v1/me with the mini app token: %d %v", code, me)
	}
	stepUp := fmt.Sprintf(`{"password":"a long phrase 1","org_id":%q}`, uuid.NewString())
	if code, o := c.do(http.MethodPost, "/v1/auth/step-up", access, stepUp, nil); code != http.StatusOK {
		t.Fatalf("step-up in the mini app session: %d %v", code, o)
	}
	// A refresh rotates the token and never moves the end of the session.
	code, refreshed := c.do(http.MethodPost, "/v1/auth/refresh", "", fmt.Sprintf(`{"refresh_token":%q}`, refresh), nil)
	if code != http.StatusOK {
		t.Fatalf("refresh: %d %v", code, refreshed)
	}
	s2 := readTokenSession(t, pool, refreshed["refresh_token"].(string))
	if s2.channel == nil || *s2.channel != "event_center_tg" || s2.expiresAt.After(s.expiresAt) {
		t.Fatalf("refreshed session %+v, first %+v", s2, s)
	}

	// It is audited with the channel, never with the launch data.
	var via string
	var tg float64
	if err := pool.QueryRow(ctx, `SELECT metadata->>'via', (metadata->>'telegram_user_id')::float8 FROM audit_events
		WHERE actor_id = $1 AND action = 'v1.auth.telegram_miniapp_login' ORDER BY occurred_at LIMIT 1`, person).Scan(&via, &tg); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if via != "event_center_tg" || int64(tg) != tgPerson {
		t.Fatalf("audit via=%q telegram_user_id=%v", via, tg)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE actor_id = $1 AND metadata::text LIKE '%hash%'`, person).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("launch data leaked into audit metadata (%d, %v)", leaked, err)
	}
}

// filterParams drops the named query parameters from an encoded query.
func filterParams(raw string, names ...string) []string {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	var out []string
	for _, kv := range strings.Split(raw, "&") {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	return out
}

// Without TELEGRAM_MINIAPP_SECRET_KEY the route is off, never open.
func TestTelegramMiniappLogin_NotConfiguredAnswers503(t *testing.T) {
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) { c.TelegramMiniappSecretKey = "" })
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	key := hauth.DeriveMiniappSecretKey(miniappTestBotToken)
	code, out := c.do(http.MethodPost, "/v1/auth/telegram-miniapp", "", miniappBody(miniappInitData(t, key, 1, time.Now())), nil)
	if code != http.StatusServiceUnavailable || errCode(out) != "auth.telegram_miniapp_not_configured" {
		t.Fatalf("unconfigured: %d %v", code, out)
	}
}

// A flood of forged requests from one address is cut off.
func TestTelegramMiniappLogin_RateLimitedPerAddress(t *testing.T) {
	key := hauth.DeriveMiniappSecretKey(miniappTestBotToken)
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.TelegramMiniappSecretKey = hex.EncodeToString(key)
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	c := webAuthClient{t: t, ts: ts}
	forged := miniappBody(miniappInitData(t, hauth.DeriveMiniappSecretKey("0:x"), 1, time.Now()))
	var last int
	for i := 0; i < 35; i++ {
		last, _ = c.do(http.MethodPost, "/v1/auth/telegram-miniapp", "", forged, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after 35 forged requests: %d, want 429", last)
	}
}
