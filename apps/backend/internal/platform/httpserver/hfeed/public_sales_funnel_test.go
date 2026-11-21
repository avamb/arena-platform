package hfeed

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseFunnelRequest(t *testing.T) {
	ev := uuid.New().String()
	sess := uuid.New().String()
	cases := []struct {
		name string
		req  funnelRequest
		ok   bool
	}{
		{"widget view", funnelRequest{FeedToken: "tok", EventID: ev, Step: "view", Surface: "widget"}, true},
		{"widget with session", funnelRequest{FeedToken: "tok", EventID: ev, SessionID: sess, Step: "seats_chosen", Surface: "widget"}, true},
		{"page", funnelRequest{Page: &funnelPage{Slug: "acme", EventSlug: "gala"}, Step: "view", Surface: "hosted_page"}, true},
		{"unknown step", funnelRequest{FeedToken: "tok", EventID: ev, Step: "paid", Surface: "widget"}, false},
		{"widget surface needs a feed token", funnelRequest{Page: &funnelPage{Slug: "a", EventSlug: "b"}, Step: "view", Surface: "widget"}, false},
		{"page surface needs a page", funnelRequest{FeedToken: "tok", EventID: ev, Step: "view", Surface: "hosted_page"}, false},
		{"tickets page surface with a token", funnelRequest{FeedToken: "tok", EventID: ev, Step: "view", Surface: "tickets_page"}, false},
		{"tickets page", funnelRequest{Page: &funnelPage{Slug: "a", EventSlug: "b"}, Step: "view", Surface: "tickets_page"}, true},
		{"external site with a token", funnelRequest{FeedToken: "tok", EventID: ev, Step: "view", Surface: "external_site"}, true},
		{"external site with a page", funnelRequest{Page: &funnelPage{Slug: "a", EventSlug: "b"}, Step: "session_open", Surface: "external_site"}, true},
		{"unknown surface", funnelRequest{FeedToken: "tok", EventID: ev, Step: "view", Surface: "app"}, false},
		{"both contexts", funnelRequest{FeedToken: "tok", EventID: ev, Page: &funnelPage{Slug: "a", EventSlug: "b"}, Step: "view", Surface: "widget"}, false},
		{"neither context", funnelRequest{Step: "view", Surface: "widget"}, false},
		{"token without event", funnelRequest{FeedToken: "tok", Step: "view", Surface: "widget"}, false},
		{"bad event id", funnelRequest{FeedToken: "tok", EventID: "nope", Step: "view", Surface: "widget"}, false},
		{"bad session id", funnelRequest{FeedToken: "tok", EventID: ev, SessionID: "x", Step: "view", Surface: "widget"}, false},
		{"empty slug", funnelRequest{Page: &funnelPage{Slug: " ", EventSlug: "b"}, Step: "view", Surface: "hosted_page"}, false},
		{"page with event id", funnelRequest{EventID: ev, Page: &funnelPage{Slug: "a", EventSlug: "b"}, Step: "view", Surface: "hosted_page"}, false},
		{"huge token", funnelRequest{FeedToken: strings.Repeat("a", 500), EventID: ev, Step: "view", Surface: "widget"}, false},
	}
	for _, c := range cases {
		_, _, _, ok := parseFunnelRequest(c.req)
		if ok != c.ok {
			t.Errorf("%s: ok = %v; want %v", c.name, ok, c.ok)
		}
	}
}

func TestParseFunnelRequest_ReturnsTheContext(t *testing.T) {
	ev, sess := uuid.New(), uuid.New()
	fc, step, surface, ok := parseFunnelRequest(funnelRequest{
		FeedToken: " tok ", EventID: ev.String(), SessionID: sess.String(), Step: "session_open", Surface: "widget",
	})
	if !ok || !fc.byToken() || fc.feedToken != "tok" || fc.eventID != ev || fc.sessionID == nil || *fc.sessionID != sess {
		t.Fatalf("context = %+v ok=%v", fc, ok)
	}
	if step != "session_open" || surface != "widget" {
		t.Errorf("step/surface = %s/%s", step, surface)
	}
}
