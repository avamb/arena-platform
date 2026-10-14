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
	sid := uuid.NewString()
	org := uuid.NewString()
	good, _, err := IssueStepUp(secret, issuer, user, sid, org)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyStepUp(secret, issuer, good, user.String(), sid)
	if err != nil {
		t.Fatalf("a fresh step-up token: %v", err)
	}
	if got != org {
		t.Fatalf("organization = %q, want %q", got, org)
	}
	access, _, _ := IssueJWT(secret, user, nil, nil, issuer, "arena-api", time.Hour)
	// A token of the old shape (no session, no organization) proves nothing.
	unbound, _, _ := IssueJWT(secret, user, nil, nil, issuer, StepUpAudience, StepUpTTL)
	otherIssuer, _, _ := IssueStepUp(secret, "someone-else", user, sid, org)
	cases := map[string]struct{ secret, raw, user, sid string }{
		"another person":      {secret, good, uuid.NewString(), sid},
		"another session":     {secret, good, user.String(), uuid.NewString()},
		"no session":          {secret, good, user.String(), ""},
		"an access token":     {secret, access, user.String(), sid},
		"an unbound token":    {secret, unbound, user.String(), sid},
		"another issuer":      {secret, otherIssuer, user.String(), sid},
		"another secret":      {"another-secret-of-32-bytes-length!!", good, user.String(), sid},
		"empty":               {secret, "", user.String(), sid},
		"no secret on server": {"", good, user.String(), sid},
	}
	for name, c := range cases {
		if _, err := VerifyStepUp(c.secret, issuer, c.raw, c.user, c.sid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := IssueStepUp(secret, issuer, user, "", org); err == nil {
		t.Error("a step-up token without a session was issued")
	}
	if _, _, err := IssueStepUp(secret, issuer, user, sid, ""); err == nil {
		t.Error("a step-up token without an organization was issued")
	}
	// A step-up token is never an access token.
	v, _ := NewJWTVerifier(secret, issuer, "arena-api")
	if _, err := v.Verify(t.Context(), good); err == nil {
		t.Error("the access token verifier accepted a step-up token")
	}
}

func TestIssueSessionJWT_CarriesSessionClaims(t *testing.T) {
	t.Parallel()
	const secret, issuer = "session-test-secret-32-bytes-long!", "arena-api"
	user := uuid.New()
	tok, _, err := IssueSessionJWT(secret, user, issuer, "arena-api", time.Hour,
		SessionClaims{SessionID: "sid-1", Channel: "event_center_web"})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := NewJWTVerifier(secret, issuer, "arena-api")
	a, err := v.Verify(t.Context(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != "sid-1" || a.Channel != "event_center_web" {
		t.Fatalf("verifier: sid=%q chn=%q", a.SessionID, a.Channel)
	}
	stub, err := NewStubProvider(StubConfig{Secret: secret, Issuer: issuer, Audience: "arena-api", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err = stub.Verify(t.Context(), tok)
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != "sid-1" || a.Channel != "event_center_web" {
		t.Fatalf("stub: sid=%q chn=%q", a.SessionID, a.Channel)
	}
	plain, _, _ := IssueJWT(secret, user, nil, nil, issuer, "arena-api", time.Hour)
	if a, _ := v.Verify(t.Context(), plain); a.SessionID != "" || a.Channel != "" {
		t.Fatalf("a token without a sign-in carries sid=%q chn=%q", a.SessionID, a.Channel)
	}
}
