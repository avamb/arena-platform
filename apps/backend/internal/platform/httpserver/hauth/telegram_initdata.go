package hauth

// telegram_initdata.go — checking the signed launch data (initData) of a
// Telegram Mini App (task S3b). The rule is Telegram's own
// (https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app):
//
//	secret_key        = HMAC_SHA256(key = "WebAppData", message = bot token)
//	data_check_string = every received field except "hash", sorted by name,
//	                    "name=value" lines joined by "\n" (values decoded)
//	hash              = hex(HMAC_SHA256(key = secret_key, message = data_check_string))
//
// arena-api holds only secret_key (TELEGRAM_MINIAPP_SECRET_KEY), never the bot
// token. The check is a pure function so it is tested without a database.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// InitDataMaxAge is how old signed launch data may be: five minutes.
	InitDataMaxAge = 5 * time.Minute
	// initDataFutureSkew is how far ahead of this server's clock auth_date may
	// be (clock drift between Telegram and this host).
	initDataFutureSkew = time.Minute
	// maxInitDataLen and maxInitDataFields bound what is parsed. Real launch
	// data is under 2 KiB with about ten fields.
	maxInitDataLen    = 4096
	maxInitDataFields = 24
)

// Why launch data was refused. Callers map them to one 401 answer each; the
// distinction between ErrInitDataExpired and the others is only ever made
// AFTER the signature checked out, so it tells a forger nothing.
var (
	ErrInitDataMalformed    = errors.New("init data is malformed")
	ErrInitDataBadSignature = errors.New("init data signature does not match")
	ErrInitDataExpired      = errors.New("init data is older than the allowed age")
)

// InitDataUser is the Telegram account named by verified launch data.
type InitDataUser struct {
	// TelegramID is the account's id (the key of bot_telegram_links).
	TelegramID int64
	// AuthDate is when Telegram signed the data.
	AuthDate time.Time
}

// VerifyInitData checks raw launch data against the derived key secretKey and
// returns the account it names. A malformed value, a wrong or missing hash, or
// a user that does not parse is ErrInitDataMalformed or ErrInitDataBadSignature;
// signed data older than maxAge (or dated more than a minute ahead of now) is
// ErrInitDataExpired. The signature is always checked before the age, and the
// hash is compared in constant time.
func VerifyInitData(raw string, secretKey []byte, now time.Time, maxAge time.Duration) (InitDataUser, error) {
	var zero InitDataUser
	if raw == "" || len(raw) > maxInitDataLen || len(secretKey) == 0 {
		return zero, ErrInitDataMalformed
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) == 0 || len(values) > maxInitDataFields {
		return zero, ErrInitDataMalformed
	}
	for _, v := range values {
		if len(v) != 1 { // a repeated field would make "the" value ambiguous
			return zero, ErrInitDataMalformed
		}
	}
	// The check string is "name=value" lines joined by a line break: a decoded
	// name or value that holds a line break (or a name that holds "=") could be
	// read as a different set of fields with the same bytes.
	for k, v := range values {
		if strings.ContainsAny(k, "\n=") || strings.Contains(v[0], "\n") {
			return zero, ErrInitDataMalformed
		}
	}
	gotHex := values.Get("hash")
	got, err := hex.DecodeString(gotHex)
	if err != nil || len(got) != sha256.Size {
		return zero, ErrInitDataMalformed
	}

	names := make([]string, 0, len(values))
	for k := range values {
		if k != "hash" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	lines := make([]string, len(names))
	for i, k := range names {
		lines[i] = k + "=" + values.Get(k)
	}
	mac := hmac.New(sha256.New, secretKey)
	mac.Write([]byte(strings.Join(lines, "\n")))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return zero, ErrInitDataBadSignature
	}

	// From here on the data is Telegram's own.
	sec, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || sec <= 0 {
		return zero, ErrInitDataMalformed
	}
	authDate := time.Unix(sec, 0).UTC()
	if age := now.Sub(authDate); age > maxAge || age < -initDataFutureSkew {
		return zero, ErrInitDataExpired
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal([]byte(values.Get("user")), &user) != nil || user.ID <= 0 {
		return zero, ErrInitDataMalformed
	}
	return InitDataUser{TelegramID: user.ID, AuthDate: authDate}, nil
}

// MiniappSecretKey decodes TELEGRAM_MINIAPP_SECRET_KEY (64 hex characters)
// into the derived HMAC key; nil when it is empty or not that shape.
func MiniappSecretKey(configured string) []byte {
	key, err := hex.DecodeString(strings.TrimSpace(configured))
	if err != nil || len(key) != sha256.Size {
		return nil
	}
	return key
}

// DeriveMiniappSecretKey is HMAC_SHA256("WebAppData", botToken), the key
// Telegram signs with. Production never calls it (arena-api must not hold the
// token); tests and the documented derivation use it.
func DeriveMiniappSecretKey(botToken string) []byte {
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))
	return mac.Sum(nil)
}
