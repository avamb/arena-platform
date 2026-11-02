package analytics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func mustDay(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, 11, 2, 15, 30, 0, 0, time.UTC)
	p, err := ParsePeriod("", "", now)
	if err != nil || !p.To.Equal(mustDay("2026-11-02")) || !p.From.Equal(mustDay("2026-10-27")) || p.Days() != 7 {
		t.Fatalf("default period = %+v, %v", p, err)
	}
	prev := p.Previous()
	if !prev.From.Equal(mustDay("2026-10-20")) || !prev.To.Equal(mustDay("2026-10-26")) {
		t.Errorf("previous = %+v", prev)
	}
	if !p.End().Equal(mustDay("2026-11-03")) {
		t.Errorf("end = %v", p.End())
	}
	p, err = ParsePeriod("2026-10-01", "2026-10-31", now)
	if err != nil || p.Days() != 31 {
		t.Fatalf("explicit = %+v, %v", p, err)
	}
	for _, bad := range [][2]string{{"2026-11-02", "2026-11-01"}, {"x", ""}, {"", "2026-13-01"}, {"2024-01-01", "2026-01-01"}} {
		if _, err := ParsePeriod(bad[0], bad[1], now); !errors.Is(err, ErrInvalidPeriod) {
			t.Errorf("ParsePeriod(%q,%q) = %v, want ErrInvalidPeriod", bad[0], bad[1], err)
		}
	}
}

func TestChangeAndDropOff(t *testing.T) {
	if ChangePct(5, 0) != nil {
		t.Error("change from zero has no percentage")
	}
	if v := ChangePct(15, 10); v == nil || *v != 50 {
		t.Errorf("ChangePct(15,10) = %v", v)
	}
	if v := ChangePct(5, 10); v == nil || *v != -50 {
		t.Errorf("ChangePct(5,10) = %v", v)
	}
	if (FlowStep{}).DropOffPct() != nil {
		t.Error("no entries: no drop-off")
	}
	if v := (FlowStep{Entered: 8, LeftHere: 2}).DropOffPct(); v == nil || *v != 25 {
		t.Errorf("drop-off = %v", v)
	}
	if v := (FlowStep{Entered: 1, LeftHere: 3}).DropOffPct(); v == nil || *v != 100 {
		t.Errorf("drop-off must be capped at 100, got %v", v)
	}
}

func TestSortFlowSteps(t *testing.T) {
	in := []FlowStepCompare{
		{Current: FlowStep{Surface: "bot", Flow: "wizard", Step: "b", Entered: 5}},
		{Current: FlowStep{Surface: "bot", Flow: "wizard", Step: "a", Entered: 9}},
		{Current: FlowStep{Surface: "bot", Flow: "wizard", Step: "gone"}, Previous: FlowStep{Entered: 3}},
		{Current: FlowStep{Surface: "bot", Flow: "wizard", Step: "z"}},
		{Current: FlowStep{Surface: "bot", Flow: "alpha", Step: "s", Entered: 1}},
		{Current: FlowStep{Surface: "event_center", Flow: "a", Step: "s", Entered: 1}},
	}
	SortFlowSteps(in)
	var got []string
	for _, c := range in {
		got = append(got, c.Current.Surface+"/"+c.Current.Flow+"/"+c.Current.Step)
	}
	want := "bot/alpha/s bot/wizard/a bot/wizard/b bot/wizard/gone bot/wizard/z event_center/a/s"
	if strings.Join(got, " ") != want {
		t.Errorf("order = %v", got)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 2, 10, 0, 0, 123456000, time.UTC)
	c := EncodeCursor(at, 42)
	gotAt, gotID, err := DecodeCursor(c)
	if err != nil || !gotAt.Equal(at) || gotID != 42 {
		t.Fatalf("decode(%q) = %v %d %v", c, gotAt, gotID, err)
	}
	for _, bad := range []string{"", "1", "a.b", "1.2.3", "1.x"} {
		if _, _, err := DecodeCursor(bad); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) = %v", bad, err)
		}
	}
}

func TestCheckSurface(t *testing.T) {
	for _, ok := range []string{"", "site", "bot", "event_center"} {
		if CheckSurface(ok) != nil {
			t.Errorf("%q must be accepted", ok)
		}
	}
	if !errors.Is(CheckSurface("widget"), ErrInvalidSurface) {
		t.Error("widget is not an analytics surface")
	}
}

func TestNextDigestTime(t *testing.T) {
	madrid := DigestLocation()
	cases := []struct {
		now  time.Time
		want time.Time
	}{
		// Sunday 1 Nov 2026: the Madrid clock is already on winter time.
		{time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 9, 0, 0, 0, madrid)},
		// Monday before 09:00 Madrid -> the same day.
		{time.Date(2026, 11, 2, 7, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 9, 0, 0, 0, madrid)},
		// Monday after 09:00 Madrid -> next week.
		{time.Date(2026, 11, 2, 8, 30, 0, 0, time.UTC), time.Date(2026, 11, 9, 9, 0, 0, 0, madrid)},
		// Exactly 09:00 -> strictly after.
		{time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 9, 0, 0, 0, madrid)},
		// Summer time: 09:00 CEST is 07:00 UTC.
		{time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 7, 6, 9, 0, 0, 0, madrid)},
	}
	for _, c := range cases {
		got := NextDigestTime(c.now)
		if !got.Equal(c.want) {
			t.Errorf("NextDigestTime(%v) = %v, want %v", c.now, got, c.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("NextDigestTime must return UTC")
		}
	}
	if got := NextDigestTime(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)); got.Hour() != 7 {
		t.Errorf("summer digest at %v UTC, want 07:00", got.Hour())
	}
}

func TestDigestWeek(t *testing.T) {
	// Monday 2 Nov 09:00 Madrid -> week 26 Oct .. 1 Nov.
	w := DigestWeek(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC))
	if !w.From.Equal(mustDay("2026-10-26")) || !w.To.Equal(mustDay("2026-11-01")) {
		t.Errorf("week = %+v", w)
	}
	// A retry on Wednesday still reports the same full week.
	w = DigestWeek(time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC))
	if !w.From.Equal(mustDay("2026-10-26")) || !w.To.Equal(mustDay("2026-11-01")) {
		t.Errorf("retry week = %+v", w)
	}
	// Sunday belongs to the week that is still running.
	w = DigestWeek(time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC))
	if !w.From.Equal(mustDay("2026-10-19")) || !w.To.Equal(mustDay("2026-10-25")) {
		t.Errorf("sunday week = %+v", w)
	}
}

func TestRankLossesAndFormat(t *testing.T) {
	mk := func(surface, flow, step string, entered, left, prevLeft int) FlowStepCompare {
		return FlowStepCompare{
			Current:  FlowStep{Surface: surface, Flow: flow, Step: step, Entered: entered, LeftHere: left},
			Previous: FlowStep{Surface: surface, Flow: flow, Step: step, LeftHere: prevLeft},
		}
	}
	in := []FlowStepCompare{
		mk("bot", "wizard", "venue", 40, 14, 9),
		mk("bot", "wizard", "price", 30, 14, 0),
		mk("event_center", "event_create", "venue", 10, 9, 9),
		mk("event_center", FlowScreens, "events", 0, 5, 0), // pseudo flow: skipped
		mk("bot", "wizard", "done", 20, 0, 4),              // nobody left: skipped
		mk("bot", "wizard", "a", 5, 1, 1),
		mk("bot", "wizard", "b", 5, 2, 1),
		mk("bot", "wizard", "c", 5, 3, 1),
	}
	got := RankLosses(in, 5)
	if len(got) != 5 {
		t.Fatalf("got %d steps", len(got))
	}
	// 14 and 14: the higher rate (price 14/30 vs venue 14/40) comes first.
	if got[0].Step != "price" || got[1].Step != "venue" || got[1].Surface != "bot" || got[2].Step != "venue" || got[2].Surface != "event_center" {
		t.Errorf("order = %+v", got)
	}
	for _, s := range got {
		if s.Flow == FlowScreens || s.LeftHere == 0 {
			t.Errorf("unexpected step %+v", s)
		}
	}

	d := UXDigest{
		Week:  Period{From: mustDay("2026-10-26"), To: mustDay("2026-11-01")},
		Steps: got[:2],
		Errors: []ErrorStat{
			{Kind: ErrKindUI, Surface: "event_center", Key: "venue.not_found", Place: "/events/:id", Count: 12, Previous: 8},
			{Kind: ErrKindRageClick, Surface: "event_center", Key: "/events/:id", Count: 3, Previous: 0},
			{Kind: ErrKindWizard, Surface: "bot", Key: "mail me@example.com", Count: 1, Previous: 4},
		},
	}
	text := FormatUXDigest(d)
	for _, want := range []string{
		"UX digest, 26 Oct - 1 Nov 2026",
		"1. bot / wizard / price: 14 left (47% of 30), was 0, new",
		"2. bot / wizard / venue: 14 left (35% of 40), was 9, +56%",
		"ui_error venue.not_found at /events/:id [event_center]: 12, was 8, +50%",
		"rage_click /events/:id [event_center]: 3, was 0, new",
		"wizard_input_error mail [redacted] [bot]: 1, was 4, -75%",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("digest lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "@") || strings.Contains(text, "example.com") {
		t.Errorf("digest must not carry an e-mail address:\n%s", text)
	}
	if empty := FormatUXDigest(UXDigest{Week: d.Week}); !strings.Contains(empty, "No exits recorded.") || !strings.Contains(empty, "No errors recorded.") {
		t.Errorf("empty digest = %s", empty)
	}
	if len(text) > 4000 {
		t.Errorf("digest is %d chars", len(text))
	}
}

type captureScheduler struct{ at []time.Time }

func (c *captureScheduler) ScheduleNext(_ context.Context, at time.Time) error {
	c.at = append(c.at, at)
	return nil
}

type uxFailingDB struct{}

func (uxFailingDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("db down")
}
func (uxFailingDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type captureNotifier struct{ sent []string }

func (c *captureNotifier) Send(_ context.Context, text string) error {
	c.sent = append(c.sent, text)
	return nil
}

func TestUXDigestHandlerSchedulesBeforeTheWork(t *testing.T) {
	sched := &captureScheduler{}
	n := &captureNotifier{}
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	h := NewUXDigestHandler(UXDigestHandlerOptions{
		DB: uxFailingDB{}, Notifier: n, Scheduler: sched, Now: func() time.Time { return now },
	})
	// The database is down: the run fails and may be retried, but the next
	// Monday is already queued so the chain survives.
	if err := h(context.Background(), nil); err == nil {
		t.Fatal("a failed read must fail the run")
	}
	if len(sched.at) != 1 || !sched.at[0].Equal(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("scheduled %v", sched.at)
	}
	if len(n.sent) != 0 {
		t.Errorf("nothing may be sent without data: %v", n.sent)
	}
}
