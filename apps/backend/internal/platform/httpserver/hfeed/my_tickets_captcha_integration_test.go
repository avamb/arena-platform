//go:build integration

// my_tickets_captcha_integration_test.go - the "my tickets" link request
// behind the site forms' Turnstile guard (hsite, action my_tickets): a
// channel that lists settings.site.origins must send a valid
// turnstile_token, any other channel keeps working on its rate limits only.
package hfeed

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hsite"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
)

// captchaStub accepts exactly one token.
type captchaStub struct{ calls int }

func (c *captchaStub) Verify(_ context.Context, token, _ string) (bool, error) {
	c.calls++
	return token == "good-token", nil
}

func TestSiteP7_MyTicketsLinkUsesTheSiteCaptcha(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	site := seedSiteOrder(t, pool, "captcha.site@example.com")
	plain := seedSiteOrder(t, pool, "captcha.plain@example.com")
	siteExec(t, pool, `UPDATE sales_channels SET settings = jsonb_set(settings, '{site}', '{"origins":["https://vinoandco.events"]}'::jsonb, true) WHERE id = $1`, site.channelID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1`, mytickets.JobTypeLinkEmail)
	})

	stub := &captchaStub{}
	guard := hsite.Guard{Verifier: stub}
	lookup := func(ctx context.Context, feedToken string) (hsite.Channel, error) {
		return hsite.ChannelByFeedToken(ctx, pool, feedToken)
	}
	h := siteHandler(pool).WithMyTicketsCaptcha(func(ctx context.Context, feedToken, captchaToken, ip string) hsite.CaptchaOutcome {
		return guard.CaptchaForFeed(ctx, lookup, feedToken, captchaToken, ip, hsite.ActionMyTickets)
	})
	request := func(feedToken, body string) (int, string) {
		w := siteDo(h.HandleMyTicketsRequestLink, http.MethodPost, "/x", body, map[string]string{"feed_token": feedToken}, nil)
		return w.Code, w.Body.String()
	}

	// A site channel without a token, or with a wrong one: 403, nothing queued.
	if c, b := request(site.feedToken, `{"email":"captcha.site@example.com"}`); c != http.StatusForbidden || !strings.Contains(b, "my_tickets.captcha_failed") {
		t.Fatalf("site channel without a token: %d %s", c, b)
	}
	if c, _ := request(site.feedToken, `{"email":"captcha.site@example.com","turnstile_token":"bad"}`); c != http.StatusForbidden {
		t.Fatalf("site channel with a wrong token: %d", c)
	}
	if r, _ := linkRequests(t, pool, site.orgID, "captcha.site@example.com"); r != 0 {
		t.Fatalf("a refused captcha recorded %d link requests", r)
	}
	// With the good token the letter is queued.
	if c, b := request(site.feedToken, `{"email":"captcha.site@example.com","turnstile_token":"good-token"}`); c != http.StatusAccepted {
		t.Fatalf("site channel with a good token: %d %s", c, b)
	}
	if r, j := linkRequests(t, pool, site.orgID, "captcha.site@example.com"); r != 1 || j != 1 {
		t.Fatalf("good token: requests=%d jobs=%d", r, j)
	}

	// A channel without site origins is not gated: no token, no verifier call.
	before := stub.calls
	if c, b := request(plain.feedToken, `{"email":"captcha.plain@example.com"}`); c != http.StatusAccepted {
		t.Fatalf("plain channel without a token: %d %s", c, b)
	}
	if stub.calls != before {
		t.Fatalf("the verifier was asked for a channel without site origins")
	}
	// An unknown feed token still answers its own 404.
	if c, _ := request("no-such-token", `{"email":"a@b.co"}`); c != http.StatusNotFound {
		t.Fatalf("unknown feed token: %d", c)
	}
}
