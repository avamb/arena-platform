package auth

// step_up.go — the short proof that the person at the keyboard typed their
// password a moment ago (spec 08_architecture/37, owner decision 2026-10-14:
// the web event center asks for the password again before a money action).
//
// It is an HS256 JWT signed with the API's own secret but with its OWN
// audience, so it can never stand in for an access token (the access token
// verifier refuses any other audience), and an access token can never stand
// in for it. It names the sign-in it was made in ("sid") and the
// organization it was made for ("org_id"): it is worthless in another
// session, after a sign-out, and in another organization.

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// StepUpAudience is the "aud" claim of a step-up token.
const StepUpAudience = "arena-step-up"

// StepUpTTL is how long one password confirmation covers money actions.
const StepUpTTL = 5 * time.Minute

// ErrStepUpInvalid is returned for any step-up token that does not prove a
// recent password confirmation of the given user in the given session.
var ErrStepUpInvalid = errors.New("auth: step-up token invalid")

// stepUpClaims is the payload of a step-up token.
type stepUpClaims struct {
	jwt.RegisteredClaims
	SessionID string `json:"sid"`
	OrgID     string `json:"org_id"`
}

// IssueStepUp mints a step-up token for userID, valid StepUpTTL, inside the
// sign-in sessionID and for the organization orgID. Both are required.
func IssueStepUp(secret, issuer string, userID uuid.UUID, sessionID, orgID string) (string, time.Time, error) {
	if strings.TrimSpace(secret) == "" || sessionID == "" || orgID == "" {
		return "", time.Time{}, errors.New("auth: IssueStepUp needs a secret, a session and an organization")
	}
	now := time.Now().UTC()
	exp := now.Add(StepUpTTL)
	claims := stepUpClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   userID.String(),
			Audience:  jwt.ClaimStrings{StepUpAudience},
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ID:        uuid.New().String(),
		},
		SessionID: sessionID,
		OrgID:     orgID,
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign step-up token: %w", err)
	}
	return signed, exp, nil
}

// VerifyStepUp checks a step-up token: signature, expiry, issuer, the
// step-up audience, that it was issued to userID inside the sign-in
// sessionID, and that it names an organization. It returns that
// organization; the caller compares it with the organization the action is
// in.
func VerifyStepUp(secret, issuer, raw, userID, sessionID string) (string, error) {
	if secret == "" || raw == "" || userID == "" || sessionID == "" {
		return "", ErrStepUpInvalid
	}
	var claims stepUpClaims
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
		return "", ErrStepUpInvalid
	}
	if issuer != "" && claims.Issuer != issuer {
		return "", ErrStepUpInvalid
	}
	if claims.Subject != userID || claims.SessionID != sessionID {
		return "", ErrStepUpInvalid
	}
	org, err := uuid.Parse(claims.OrgID)
	if err != nil {
		return "", ErrStepUpInvalid
	}
	return org.String(), nil
}
