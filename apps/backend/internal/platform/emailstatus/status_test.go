package emailstatus

import (
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		class Class
	}{
		{"request", EventSent, ClassStep},
		{"delivered", EventDelivered, ClassStep},
		{"unique_opened", EventOpened, ClassStep},
		{"uniqueOpened", EventOpened, ClassStep},
		{"opened", EventOpened, ClassStep},
		{"proxy_open", EventProxyOpen, ClassStep},
		{"unique_proxy_open", EventProxyOpen, ClassStep},
		{"loadedByProxy", EventProxyOpen, ClassStep},
		{"hard_bounce", EventHardBounce, ClassFailure},
		{"softBounce", EventSoftBounce, ClassFailure},
		{"invalid_email", EventInvalidEmail, ClassFailure},
		{"blocked", EventBlocked, ClassFailure},
		{"error", EventError, ClassFailure},
		{"click", EventClick, ClassStep},
		{"deferred", "deferred", ClassIgnored},
		{"unsubscribed", "unsubscribed", ClassIgnored},
		{"spam", "spam", ClassIgnored},
		{"listAddition", "list_addition", ClassIgnored},
		{"somethingNew", "something_new", ClassUnknown},
		{"", "", ClassUnknown},
	}
	for _, c := range cases {
		got, class := Normalize(c.in)
		if got != c.want || class != c.class {
			t.Errorf("Normalize(%q) = %q/%d, want %q/%d", c.in, got, class, c.want, c.class)
		}
	}
}

var t0 = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func ev(name string, first, last int) EventTimes {
	return EventTimes{Event: name, First: at(first), Last: at(last)}
}

func TestDerive(t *testing.T) {
	cases := []struct {
		name   string
		events []EventTimes
		want   string
	}{
		{"nothing yet", nil, StatusSent},
		{"request only", []EventTimes{ev(EventSent, 0, 0)}, StatusSent},
		{"delivered", []EventTimes{ev(EventSent, 0, 0), ev(EventDelivered, 1, 1)}, StatusDelivered},
		{"proxy open is delivery, not reading", []EventTimes{ev(EventProxyOpen, 2, 2)}, StatusDelivered},
		{"opened", []EventTimes{ev(EventDelivered, 1, 1), ev(EventOpened, 5, 9)}, StatusOpened},
		{"hard bounce", []EventTimes{ev(EventSent, 0, 0), ev(EventHardBounce, 1, 1)}, StatusFailed},
		{"soft bounce then delivered", []EventTimes{ev(EventSoftBounce, 1, 1), ev(EventDelivered, 30, 30)}, StatusDelivered},
		{"delivered then bounce report", []EventTimes{ev(EventDelivered, 1, 1), ev(EventHardBounce, 3, 3)}, StatusFailed},
		{"failure after open still opened", []EventTimes{ev(EventOpened, 2, 2), ev(EventBlocked, 4, 4)}, StatusOpened},
		{"failure at the same instant as delivered", []EventTimes{ev(EventDelivered, 1, 1), ev(EventError, 1, 1)}, StatusDelivered},
	}
	for _, c := range cases {
		if got := Derive(c.events).Status; got != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDerive_OrderIndependent: the result depends on the set of events, so
// every permutation of the same rows gives the same answer.
func TestDerive_OrderIndependent(t *testing.T) {
	base := []EventTimes{
		ev(EventSent, 0, 0), ev(EventSoftBounce, 1, 2), ev(EventDelivered, 3, 3),
		ev(EventProxyOpen, 4, 4), ev(EventHardBounce, 5, 5),
	}
	base[4].Reason = "550 user unknown"
	want := Derive(base)
	if want.Status != StatusFailed || want.FailureEvent != EventHardBounce || want.FailureReason != "550 user unknown" {
		t.Fatalf("baseline = %+v", want)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		evs := append([]EventTimes(nil), base...)
		r.Shuffle(len(evs), func(a, b int) { evs[a], evs[b] = evs[b], evs[a] })
		got := Derive(evs)
		if got.Status != want.Status || got.FailureEvent != want.FailureEvent || !got.FailedAt.Equal(*want.FailedAt) ||
			!got.DeliveredAt.Equal(*want.DeliveredAt) || !got.ProxyOpenedAt.Equal(*want.ProxyOpenedAt) {
			t.Fatalf("permutation %d gave %+v, want %+v", i, got, want)
		}
	}
}

func TestDerive_TimesAreFirstSeen(t *testing.T) {
	d := Derive([]EventTimes{ev(EventDelivered, 2, 7), ev(EventOpened, 10, 40)})
	if !d.DeliveredAt.Equal(at(2)) || !d.OpenedAt.Equal(at(10)) {
		t.Fatalf("times = %v / %v", d.DeliveredAt, d.OpenedAt)
	}
	if d.FailedAt != nil || d.FailureEvent != "" {
		t.Fatalf("no failure expected: %+v", d)
	}
}

func TestDerive_HarderFailureWinsATie(t *testing.T) {
	d := Derive([]EventTimes{ev(EventSoftBounce, 1, 1), ev(EventHardBounce, 1, 1)})
	if d.FailureEvent != EventHardBounce {
		t.Fatalf("failure event = %q", d.FailureEvent)
	}
}

func TestParseBrevo_SingleEvent(t *testing.T) {
	letter := uuid.MustParse("0192f3c4-5b6a-7c8d-9e0f-112233445566")
	body := `{"event":"hard_bounce","email":"buyer@example.com","id":12,"date":"2026-10-13 12:00:00",
	  "ts":1791885600,"message-id":"<202610131055.1@smtp-relay.mailin.fr>","ts_event":1791885600,
	  "subject":"Tickets","X-Mailin-custom":"arena-letter:` + letter.String() + `","ts_epoch":1791878400123,
	  "reason":"550 5.1.1 <buyer@example.com>: Recipient address rejected"}`
	evs, err := ParseBrevo([]byte(body), t0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("parse: %v %v", evs, err)
	}
	e := evs[0]
	if e.Event != EventHardBounce || e.Class != ClassFailure {
		t.Fatalf("event = %q class %d", e.Event, e.Class)
	}
	if e.MessageID != "202610131055.1@smtp-relay.mailin.fr" || e.LetterID != letter {
		t.Fatalf("ids = %q %v", e.MessageID, e.LetterID)
	}
	if !e.At.Equal(time.UnixMilli(1791878400123).UTC()) {
		t.Fatalf("time = %v (ts_epoch must win)", e.At)
	}
	if e.Reason != "550 5.1.1 <[redacted]>: Recipient address rejected" {
		t.Fatalf("reason = %q", e.Reason)
	}
	for k := range e.Payload {
		if k != "event" && k != "ts_epoch" {
			t.Fatalf("payload keeps %q; only event and ts_epoch may be kept", k)
		}
	}
}

func TestParseBrevo_BatchAndLimits(t *testing.T) {
	evs, err := ParseBrevo([]byte(`[{"event":"delivered","message-id":"<a@b>"},{"event":"deferred","message-id":"<a@b>"}]`), t0)
	if err != nil || len(evs) != 2 || evs[0].Event != EventDelivered || evs[1].Class != ClassIgnored {
		t.Fatalf("batch: %+v %v", evs, err)
	}
	if !evs[0].At.Equal(t0) {
		t.Fatalf("an event without a timestamp takes now: %v", evs[0].At)
	}
	for _, bad := range []string{``, `null`, `"x"`, `{"event":`, `[null]`, `{} {}`} {
		if _, err := ParseBrevo([]byte(bad), t0); err == nil {
			t.Errorf("ParseBrevo(%q) must fail", bad)
		}
	}
	big := "["
	for i := 0; i <= MaxBatch; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"event":"delivered"}`
	}
	if _, err := ParseBrevo([]byte(big+"]"), t0); err == nil {
		t.Fatal("a batch above MaxBatch must fail")
	}
}

func TestParseBrevo_FutureTimeClamped(t *testing.T) {
	evs, _ := ParseBrevo([]byte(`{"event":"delivered","ts_epoch":4102444800000}`), t0)
	if !evs[0].At.Equal(t0) {
		t.Fatalf("a time far in the future must become now, got %v", evs[0].At)
	}
}

func TestTrackingRoundTrip(t *testing.T) {
	id := uuid.New()
	if parseTracking(TrackingID(id)) != id {
		t.Fatal("tracking id does not round-trip")
	}
	if parseTracking("other-site:123") != uuid.Nil || parseTracking("arena-letter:nope") != uuid.Nil {
		t.Fatal("foreign custom headers must not match a letter")
	}
}

func TestDerive_ClickIsDeliveryNotReading(t *testing.T) {
	d := Derive([]EventTimes{ev(EventSent, 0, 0), ev(EventClick, 3, 8)})
	if d.Status != StatusDelivered || d.ClickedAt == nil || !d.ClickedAt.Equal(at(3)) || d.OpenedAt != nil {
		t.Fatalf("click only = %+v", d)
	}
	// A click after a soft bounce proves the letter arrived.
	if got := Derive([]EventTimes{ev(EventSoftBounce, 1, 1), ev(EventClick, 4, 4)}).Status; got != StatusDelivered {
		t.Fatalf("soft bounce then click = %q", got)
	}
}

func TestCleanLink(t *testing.T) {
	cases := map[string]string{
		"https://Tickets.ArenaSoldOut.com/o/abcdefghijklmnopqrstuvwxyz012345/pdf?utm=x#top": "https://tickets.arenasoldout.com/o/{token}/pdf",
		"https://example.com/events/summer-gala":                                            "https://example.com/events/summer-gala",
		"mailto:someone@example.com":                                                        "",
		"javascript:alert(1)":                                                               "",
		"not a url":                                                                         "",
	}
	for in, want := range cases {
		if got := CleanLink(in); got != want {
			t.Errorf("CleanLink(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBrevo_ClickKeepsTheCleanLinkOnly(t *testing.T) {
	evs, err := ParseBrevo([]byte(`{"event":"click","message-id":"<a@b>","ts_epoch":1791878400123,
		"link":"https://api.example.com/v1/public/checkout/0123456789abcdefghijKLMNOP/pdf?sig=secret"}`), t0)
	if err != nil {
		t.Fatal(err)
	}
	e := evs[0]
	if e.Event != EventClick || e.Class != ClassStep {
		t.Fatalf("click = %q / %d", e.Event, e.Class)
	}
	if e.Link != "https://api.example.com/v1/public/checkout/{token}/pdf" {
		t.Fatalf("link = %q", e.Link)
	}
	for k := range e.Payload {
		if k != "event" && k != "ts_epoch" {
			t.Fatalf("payload keeps %q", k)
		}
	}
}

func TestBodyDigest_SeparatesHTMLFromText(t *testing.T) {
	if BodyDigest("ab", "c") == BodyDigest("a", "bc") {
		t.Fatal("moving bytes between the HTML and the text body must change the digest")
	}
	if len(BodyDigest("x", "y")) != 64 || len(FileDigest([]byte("pdf"))) != 64 {
		t.Fatal("digests are hex SHA-256")
	}
}
