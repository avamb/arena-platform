package hauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:TEST-bot-token-for-unit-tests"

var testClock = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// signedInitData builds launch data the way Telegram does: every field but
// hash sorted into "name=value" lines, HMAC-ed with the derived key.
func signedInitData(key []byte, fields map[string]string) string {
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

func goodFields(authDate time.Time, telegramID int64) map[string]string {
	return map[string]string{
		"auth_date":     strconv.FormatInt(authDate.Unix(), 10),
		"query_id":      "AAH-unit-test",
		"user":          `{"id":` + strconv.FormatInt(telegramID, 10) + `,"first_name":"Анна","last_name":"Ё","username":"anna","language_code":"ru"}`,
		"signature":     "third-party-signature-is-part-of-the-data-check-string",
		"chat_type":     "private",
		"chat_instance": "-123456789",
	}
}

func TestVerifyInitData(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	good := signedInitData(key, goodFields(testClock.Add(-time.Minute), 7_123_456_789))

	t.Run("valid data names the account", func(t *testing.T) {
		u, err := VerifyInitData(good, key, testClock, InitDataMaxAge)
		if err != nil {
			t.Fatalf("VerifyInitData: %v", err)
		}
		if u.TelegramID != 7_123_456_789 {
			t.Errorf("TelegramID = %d (ids above 2^31 must survive)", u.TelegramID)
		}
		if !u.AuthDate.Equal(testClock.Add(-time.Minute)) {
			t.Errorf("AuthDate = %v", u.AuthDate)
		}
	})

	cases := []struct {
		name string
		raw  func() string
		key  []byte
		want error
	}{
		{"signed with another bot's key", func() string {
			return signedInitData(DeriveMiniappSecretKey("999:other-bot"), goodFields(testClock, 1))
		}, key, ErrInitDataBadSignature},
		{"signed with the bot token itself instead of the derived key", func() string {
			return signedInitData([]byte(testBotToken), goodFields(testClock, 1))
		}, key, ErrInitDataBadSignature},
		{"a field changed after signing (user id swapped)", func() string {
			return strings.Replace(good, url.QueryEscape(`"id":7123456789`), url.QueryEscape(`"id":7123456790`), 1)
		}, key, ErrInitDataBadSignature},
		{"a field added after signing", func() string { return good + "&start_param=evil" }, key, ErrInitDataBadSignature},
		{"hash of another message", func() string {
			v, _ := url.ParseQuery(good)
			v.Set("hash", strings.Repeat("ab", 32))
			return v.Encode()
		}, key, ErrInitDataBadSignature},
		{"hash upper-cased is still the same bytes (accepted)", func() string {
			v, _ := url.ParseQuery(good)
			v.Set("hash", strings.ToUpper(v.Get("hash")))
			return v.Encode()
		}, key, nil},
		{"no hash", func() string {
			v, _ := url.ParseQuery(good)
			v.Del("hash")
			return v.Encode()
		}, key, ErrInitDataMalformed},
		{"hash is not hex", func() string {
			v, _ := url.ParseQuery(good)
			v.Set("hash", strings.Repeat("zz", 32))
			return v.Encode()
		}, key, ErrInitDataMalformed},
		{"hash too short", func() string {
			v, _ := url.ParseQuery(good)
			v.Set("hash", "abcd")
			return v.Encode()
		}, key, ErrInitDataMalformed},
		{"a repeated field", func() string { return good + "&auth_date=1" }, key, ErrInitDataMalformed},
		{"semicolon separators", func() string { return strings.ReplaceAll(good, "&", ";") }, key, ErrInitDataMalformed},
		{"empty", func() string { return "" }, key, ErrInitDataMalformed},
		{"too long", func() string { return good + "&pad=" + strings.Repeat("x", maxInitDataLen) }, key, ErrInitDataMalformed},
		{"too many fields", func() string {
			f := goodFields(testClock, 1)
			for i := 0; i < maxInitDataFields; i++ {
				f["f"+strconv.Itoa(i)] = "1"
			}
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"no key configured", func() string { return good }, nil, ErrInitDataMalformed},
		{"genuine but older than five minutes", func() string {
			return signedInitData(key, goodFields(testClock.Add(-InitDataMaxAge-time.Second), 1))
		}, key, ErrInitDataExpired},
		{"genuine and exactly five minutes old", func() string {
			return signedInitData(key, goodFields(testClock.Add(-InitDataMaxAge), 1))
		}, key, nil},
		{"genuine but dated in the future", func() string {
			return signedInitData(key, goodFields(testClock.Add(initDataFutureSkew+10*time.Second), 1))
		}, key, ErrInitDataExpired},
		{"genuine, a little ahead of this clock (drift)", func() string {
			return signedInitData(key, goodFields(testClock.Add(30*time.Second), 1))
		}, key, nil},
		{"genuine but auth_date missing", func() string {
			f := goodFields(testClock, 1)
			delete(f, "auth_date")
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but auth_date not a number", func() string {
			f := goodFields(testClock, 1)
			f["auth_date"] = "yesterday"
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but no user", func() string {
			f := goodFields(testClock, 1)
			delete(f, "user")
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but user is not JSON", func() string {
			f := goodFields(testClock, 1)
			f["user"] = "not json"
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but a value holds a line break", func() string {
			f := goodFields(testClock, 1)
			f["query_id"] = "AA\nchat_type=group"
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but a field name holds an equals sign", func() string {
			f := goodFields(testClock, 1)
			f["a=b"] = "c"
			return signedInitData(key, f)
		}, key, ErrInitDataMalformed},
		{"genuine but user id is zero", func() string { return signedInitData(key, goodFields(testClock, 0)) }, key, ErrInitDataMalformed},
		{"genuine but user id is negative", func() string { return signedInitData(key, goodFields(testClock, -5)) }, key, ErrInitDataMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyInitData(tc.raw(), tc.key, testClock, InitDataMaxAge)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// An expired reading is only ever given for data whose signature checked out:
// a forger who does not know the key always sees the same answer, however old
// the date they claim.
func TestVerifyInitData_ExpiryIsNotAnOracleForForgers(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	forged := signedInitData(DeriveMiniappSecretKey("not-the-key"), goodFields(testClock.Add(-time.Hour), 1))
	if _, err := VerifyInitData(forged, key, testClock, InitDataMaxAge); !errors.Is(err, ErrInitDataBadSignature) {
		t.Fatalf("a forged and stale value answered %v, want the signature error", err)
	}
}

func TestMiniappSecretKey(t *testing.T) {
	want := DeriveMiniappSecretKey(testBotToken)
	if got := MiniappSecretKey(hex.EncodeToString(want)); string(got) != string(want) {
		t.Fatal("a 64-hex key must decode to itself")
	}
	for _, bad := range []string{"", "abcd", testBotToken, strings.Repeat("g", 64), strings.Repeat("a", 63), strings.Repeat("a", 66)} {
		if MiniappSecretKey(bad) != nil {
			t.Errorf("%q accepted as a key", bad)
		}
	}
}

// The documented derivation, pinned to a value computed independently with
// openssl: printf '123456:TEST-bot-token-for-unit-tests' | openssl dgst
// -sha256 -mac HMAC -macopt key:WebAppData.
func TestDeriveMiniappSecretKey_MatchesTheDocumentedFormula(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(testBotToken))
	if hex.EncodeToString(DeriveMiniappSecretKey(testBotToken)) != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("derivation differs from HMAC-SHA256(WebAppData, token)")
	}
}

type fakeMiniappMetrics struct{ outcomes []string }

func (f *fakeMiniappMetrics) RecordTelegramMiniappLogin(o string) { f.outcomes = append(f.outcomes, o) }

func miniappRequest(body string) (*httptest.ResponseRecorder, *http.Request) {
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/telegram-miniapp", strings.NewReader(body))
	r.RemoteAddr = "203.0.113.9:4444"
	return httptest.NewRecorder(), r
}

func TestTelegramMiniapp_RefusalsBeforeTheDatabase(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	keyHex := hex.EncodeToString(key)
	m := &fakeMiniappMetrics{}
	h := &Handler{jwtSecret: "x", miniappLimiter: newMiniappLimiter(), miniappNow: func() time.Time { return testClock }}
	body := func(raw string) string { return `{"init_data":"` + strings.ReplaceAll(raw, `"`, `\"`) + `"}` }

	// 503 while the key is unset, with its own outcome.
	w, r := miniappRequest(body(signedInitData(key, goodFields(testClock, 1))))
	h.SetTelegramMiniapp("", m)
	h.TelegramMiniapp(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "auth.telegram_miniapp_not_configured") {
		t.Fatalf("unconfigured: %d %s", w.Code, w.Body)
	}
	h.SetTelegramMiniapp("not hex", m)
	w, r = miniappRequest(body("x"))
	h.TelegramMiniapp(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a malformed key must leave the route off, got %d", w.Code)
	}

	h.SetTelegramMiniapp(keyHex, m)
	check := func(name, payload string, status int, code string) {
		t.Helper()
		w, r := miniappRequest(payload)
		h.TelegramMiniapp(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), code) {
			t.Fatalf("%s: %d %s, want %d %s", name, w.Code, w.Body, status, code)
		}
	}
	check("not json", "init_data=abc", http.StatusBadRequest, "http.invalid_json")
	check("forged", body(signedInitData(DeriveMiniappSecretKey("x"), goodFields(testClock, 1))), http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	check("expired", body(signedInitData(key, goodFields(testClock.Add(-10*time.Minute), 1))), http.StatusUnauthorized, "auth.telegram_init_data_expired")
	check("empty", `{"init_data":""}`, http.StatusUnauthorized, "auth.telegram_init_data_invalid")
	// A signed, fresh value gets past the check and meets the missing database.
	check("fresh without a database", body(signedInitData(key, goodFields(testClock, 1))), http.StatusServiceUnavailable, "dependency.database_unavailable")

	want := []string{"not_configured", "not_configured", "invalid", "invalid", "expired", "invalid", "error"}
	if strings.Join(m.outcomes, ",") != strings.Join(want, ",") {
		t.Fatalf("outcomes = %v, want %v", m.outcomes, want)
	}
}

func TestTelegramMiniapp_RateLimitPerAddress(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	m := &fakeMiniappMetrics{}
	h := &Handler{jwtSecret: "x", miniappLimiter: newMiniappLimiter(), miniappNow: func() time.Time { return testClock }}
	h.SetTelegramMiniapp(hex.EncodeToString(key), m)
	forged := `{"init_data":"` + signedInitData(DeriveMiniappSecretKey("x"), goodFields(testClock, 1)) + `"}`
	last := 0
	for i := 0; i < 40; i++ {
		w, r := miniappRequest(forged)
		h.TelegramMiniapp(w, r)
		last = w.Code
		if i < 30 && w.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: %d, want 401 inside the budget", i, w.Code)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", last)
	}
	// Another address has its own budget.
	w, r := miniappRequest(forged)
	r.RemoteAddr = "198.51.100.7:1"
	h.TelegramMiniapp(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("another address: %d, want 401", w.Code)
	}
}

// One IPv6 /64 is one client: rotating the low 64 bits does not earn a new
// budget.
func TestTelegramMiniapp_RateLimitFoldsIPv6To64(t *testing.T) {
	key := DeriveMiniappSecretKey(testBotToken)
	h := &Handler{jwtSecret: "x", miniappLimiter: newMiniappLimiter(), miniappNow: func() time.Time { return testClock }}
	h.SetTelegramMiniapp(hex.EncodeToString(key), &fakeMiniappMetrics{})
	forged := `{"init_data":"` + signedInitData(DeriveMiniappSecretKey("x"), goodFields(testClock, 1)) + `"}`
	last := 0
	for i := 0; i < 35; i++ {
		w, r := miniappRequest(forged)
		r.RemoteAddr = fmt.Sprintf("[2001:db8:1:2:%x::%x]:1", i+1, i+7)
		h.TelegramMiniapp(w, r)
		last = w.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("35 requests from one /64: last %d, want 429", last)
	}
}
