package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestVerifyStepUp(t *testing.T) {
	t.Parallel()
	const secret, issuer = "step-up-test-secret-32-bytes-long!", "arena-api"
	user := uuid.New()
	good, _, err := IssueJWT(secret, user, nil, nil, issuer, StepUpAudience, StepUpTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyStepUp(secret, issuer, good, user.String()); err != nil {
		t.Fatalf("a fresh step-up token: %v", err)
	}
	access, _, _ := IssueJWT(secret, user, nil, nil, issuer, "arena-api", time.Hour)
	expired, _, _ := IssueJWT(secret, user, nil, nil, issuer, StepUpAudience, -time.Minute)
	otherIssuer, _, _ := IssueJWT(secret, user, nil, nil, "someone-else", StepUpAudience, StepUpTTL)
	cases := map[string]struct{ secret, raw, user string }{
		"another person":      {secret, good, uuid.NewString()},
		"an access token":     {secret, access, user.String()},
		"expired":             {secret, expired, user.String()},
		"another issuer":      {secret, otherIssuer, user.String()},
		"another secret":      {"another-secret-of-32-bytes-length!!", good, user.String()},
		"empty":               {secret, "", user.String()},
		"no secret on server": {"", good, user.String()},
	}
	for name, c := range cases {
		if err := VerifyStepUp(c.secret, issuer, c.raw, c.user); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A step-up token is never an access token.
	v, _ := NewJWTVerifier(secret, issuer, "arena-api")
	if _, err := v.Verify(t.Context(), good); err == nil {
		t.Error("the access token verifier accepted a step-up token")
	}
}
