package hsite

import (
	"context"
	"errors"
	"testing"
)

// fakeVerifier accepts exactly one token.
type fakeVerifier struct {
	good  string
	calls int
}

func (f *fakeVerifier) Verify(_ context.Context, token, _ string) (bool, error) {
	f.calls++
	return token != "" && token == f.good, nil
}

// TestCaptchaForFeed_RequiredOnlyForASiteChannel: the my-tickets link request
// needs a Turnstile token only when the feed token's channel lists site
// origins; otherwise it is not gated (the route works as before).
func TestCaptchaForFeed_RequiredOnlyForASiteChannel(t *testing.T) {
	site := func(context.Context, string) (Channel, error) {
		return Channel{Origins: []string{"https://vinoandco.events"}}, nil
	}
	noSite := func(context.Context, string) (Channel, error) { return Channel{}, ErrNoChannel }
	broken := func(context.Context, string) (Channel, error) { return Channel{}, errors.New("db down") }

	for _, tc := range []struct {
		name   string
		lookup ChannelLookup
		token  string
		want   CaptchaOutcome
		calls  int
	}{
		{"site channel, no token", site, "", CaptchaFailed, 1},
		{"site channel, wrong token", site, "nope", CaptchaFailed, 1},
		{"site channel, good token", site, "good", CaptchaOK, 1},
		{"no site origins, no token", noSite, "", CaptchaOK, 0},
		{"lookup failed", broken, "good", CaptchaUnavailable, 0},
	} {
		v := &fakeVerifier{good: "good"}
		g := Guard{Verifier: v}
		got := g.CaptchaForFeed(context.Background(), tc.lookup, "feed", tc.token, "198.51.100.1", ActionMyTickets)
		if got != tc.want {
			t.Errorf("%s: outcome %v, want %v", tc.name, got, tc.want)
		}
		if v.calls != tc.calls {
			t.Errorf("%s: verifier called %d times, want %d", tc.name, v.calls, tc.calls)
		}
	}
}
