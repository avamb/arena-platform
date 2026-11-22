// Package metaconnect connects an organization's Meta business to the
// platform through "Facebook Login for Business" (spec 08_architecture/66,
// stage M1): the signed OAuth state, Meta's signed_request, a small Graph API
// client and the store of the connection with its token sealed by secretbox.
//
// No error, log line or audit row of this package carries an access token,
// the app secret or a code.
package metaconnect

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Login modes recorded in the state. The mode decides the redirect_uri of the
// code exchange: Meta wants the same address the login dialog used.
const (
	// ModeJS is a login through the JS SDK (FB.login): the exchange sends an
	// empty redirect_uri.
	ModeJS = "js"
	// ModeRedirect is a login through the dialog/oauth address: the exchange
	// sends META_OAUTH_REDIRECT_URL.
	ModeRedirect = "redirect"
)

// StateTTL is how long a signed state is accepted.
const StateTTL = 10 * time.Minute

// stateKeyLabel derives the state key from the app secret, so the raw secret
// never signs anything a browser carries.
const stateKeyLabel = "arena.metaconnect.oauth-state.v1"

// ErrStateInvalid is the only error of VerifyState: a malformed, tampered,
// expired or foreign state. The reason is never told to the client.
var ErrStateInvalid = errors.New("metaconnect: state is invalid or expired")

// State is what the signed state carries through the Meta login.
type State struct {
	OrgID  uuid.UUID `json:"o"`
	UserID uuid.UUID `json:"u"`
	Mode   string    `json:"m"`
	Exp    int64     `json:"e"`
	Nonce  string    `json:"n"`
}

func stateKey(appSecret string) []byte {
	m := hmac.New(sha256.New, []byte(appSecret))
	m.Write([]byte(stateKeyLabel))
	return m.Sum(nil)
}

// SignState builds the state for one login of userID into orgID. The value
// is base64url(json) + "." + base64url(HMAC-SHA256).
func SignState(appSecret string, orgID, userID uuid.UUID, mode string, now time.Time) (string, error) {
	if appSecret == "" {
		return "", errors.New("metaconnect: no app secret")
	}
	if mode != ModeJS && mode != ModeRedirect {
		return "", errors.New("metaconnect: unknown login mode")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("metaconnect: cannot read a random nonce")
	}
	st := State{OrgID: orgID, UserID: userID, Mode: mode, Exp: now.Add(StateTTL).Unix(), Nonce: hex.EncodeToString(nonce)}
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(payload)
	m := hmac.New(sha256.New, stateKey(appSecret))
	m.Write([]byte(p))
	return p + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

// VerifyState checks the signature (constant time), the expiry and the
// fields of a state made by SignState.
func VerifyState(appSecret, raw string, now time.Time) (State, error) {
	if appSecret == "" || len(raw) > 1024 {
		return State{}, ErrStateInvalid
	}
	p, sig, ok := strings.Cut(raw, ".")
	if !ok || p == "" || sig == "" {
		return State{}, ErrStateInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return State{}, ErrStateInvalid
	}
	m := hmac.New(sha256.New, stateKey(appSecret))
	m.Write([]byte(p))
	if !hmac.Equal(got, m.Sum(nil)) {
		return State{}, ErrStateInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return State{}, ErrStateInvalid
	}
	var st State
	if err := json.Unmarshal(payload, &st); err != nil {
		return State{}, ErrStateInvalid
	}
	if st.OrgID == uuid.Nil || st.UserID == uuid.Nil || (st.Mode != ModeJS && st.Mode != ModeRedirect) || st.Nonce == "" {
		return State{}, ErrStateInvalid
	}
	if now.Unix() >= st.Exp || st.Exp > now.Add(StateTTL+time.Minute).Unix() {
		return State{}, ErrStateInvalid
	}
	return st, nil
}
