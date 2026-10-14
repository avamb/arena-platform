package auth

// step_up.go — the short proof that the person at the keyboard typed their
// password a moment ago (spec 08_architecture/37, owner decision 2026-10-14:
// the web event center asks for the password again before a money action).
//
// It is an HS256 JWT signed with the API's own secret but with its OWN
// audience, so it can never stand in for an access token (the access token
// verifier refuses any other audience), and an access token can never stand
// in for it.

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// StepUpAudience is the "aud" claim of a step-up token.
const StepUpAudience = "arena-step-up"

// StepUpTTL is how long one password confirmation covers money actions.
const StepUpTTL = 5 * time.Minute

// ErrStepUpInvalid is returned for any step-up token that does not prove a
// recent password confirmation of the given user.
var ErrStepUpInvalid = errors.New("auth: step-up token invalid")

// VerifyStepUp checks a step-up token: signature, expiry, issuer, the
// step-up audience and that it was issued to userID.
func VerifyStepUp(secret, issuer, raw, userID string) error {
	if secret == "" || raw == "" || userID == "" {
		return ErrStepUpInvalid
	}
	var claims jwt.RegisteredClaims
	tok, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected alg %v", t.Header["alg"])
		}
		return []byte(secret), nil
	},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
		jwt.WithAudience(StepUpAudience),
		jwt.WithLeeway(5*time.Second),
	)
	if err != nil || !tok.Valid {
		return ErrStepUpInvalid
	}
	if issuer != "" && claims.Issuer != issuer {
		return ErrStepUpInvalid
	}
	if claims.Subject != userID {
		return ErrStepUpInvalid
	}
	return nil
}
