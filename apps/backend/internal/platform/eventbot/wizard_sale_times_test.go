package eventbot

import (
	"context"
	"strings"
	"testing"
)

// A typed sales end counts on the event's day; one more than twelve hours
// before the start is the next day (a 22:00 show selling until 01:00).
// Doors open at or before the start, at most twelve hours earlier.
func TestSaleTimes_TypedOffsets(t *testing.T) {
	for _, c := range []struct {
		start, typed string
		want         int
	}{
		{"20:00", "20:00", 0},
		{"20:00", "21:30", 90},
		{"20:00", "19:00", -60},
		{"22:00", "01:00", 180},
	} {
		if got, ok := salesEndOffset(c.start, c.typed); !ok || got != c.want {
			t.Errorf("salesEndOffset(%s, %s) = %d, %v; want %d", c.start, c.typed, got, ok, c.want)
		}
	}
	for _, c := range []struct {
		start, typed string
		want         int
		ok           bool
	}{
		{"20:00", "19:30", 30, true},
		{"00:30", "23:30", 60, true},
		{"20:00", "20:00", 0, false},
		{"20:00", "20:30", 0, false},
		{"20:00", "07:00", 0, false},
	} {
		got, ok := doorsOffset(c.start, c.typed)
		if ok != c.ok || got != c.want {
			t.Errorf("doorsOffset(%s, %s) = %d, %v; want %d, %v", c.start, c.typed, got, ok, c.want, c.ok)
		}
	}
}

// The start time is followed by the sales end (the start offered as the one
// button to keep) and the optional doors time, in every locale.
func TestWizard_SalesEndAndDoorsQuestions(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.d.Sessions = []DraftSession{{Date: "2026-12-15"}}
	r.d.Step = stSTime
	r.text("22:00", stSSalesEnd)

	for _, loc := range SupportedLocales {
		r.ws.Locale = loc
		for _, st := range []string{stSSalesEnd, stSSalesEndTime, stSDoors} {
			r.d.Step = st
			screen, err := r.w.Render(context.Background(), r.ws, r.d)
			if err != nil {
				t.Fatalf("%s/%s: %v", loc, st, err)
			}
			if strings.Contains(screen.Text, "bot.wz.") || strings.Contains(screen.Text, "<no value>") {
				t.Errorf("%s/%s: unrendered text %q", loc, st, screen.Text)
			}
		}
	}
	r.ws.Locale = "ru"
	r.d.Step = stSSalesEnd
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	if got := screen.Buttons[0][0]; got.Data != "se:0" || !strings.Contains(got.Label, "22:00") {
		t.Errorf("first button = %+v, want keep the start 22:00", got)
	}

	if note := r.text("25:00", stSSalesEnd); note == "" {
		t.Error("a bad time must explain itself")
	}
	r.text("01:00", stSDoors)
	if got := r.d.Sessions[0].SalesEndMin; got != 180 {
		t.Errorf("sales end = %d min after the start, want 180", got)
	}
	if note := r.text("23:00", stSDoors); note == "" {
		t.Error("doors after the start must be refused")
	}
	r.text("21:30", stSCountry)
	if got := r.d.Sessions[0].DoorsMin; got != 30 {
		t.Errorf("doors = %d min before, want 30", got)
	}
}

// The offsets reach the bundle as instants in the venue's zone; a date read
// back from an existing session without a doors time clears it.
func TestBuildBundle_SaleTimes(t *testing.T) {
	d := NewDraft()
	d.Sessions = []DraftSession{{Date: "2026-12-15", Time: "22:00", Timezone: "Europe/Madrid", SalesEndMin: 180, DoorsMin: 30}}
	req := BuildBundle(d, 0, 0, "", "ref")
	if got := req.ActionEvent.SellEndTime; got != "2026-12-16T01:00:00+01:00" {
		t.Errorf("sellEndTime = %q", got)
	}
	if got := req.ActionEvent.DoorsOpenTime; got == nil || *got != "2026-12-15T21:30:00+01:00" {
		t.Errorf("doorsOpenTime = %v", got)
	}

	d.Sessions[0] = DraftSession{Date: "2026-12-15", Time: "22:00", Timezone: "Europe/Madrid", SessionID: "s1"}
	req = BuildBundle(d, 0, 0, "", "ref")
	if got := req.ActionEvent.SellEndTime; got != "2026-12-15T22:00:00+01:00" {
		t.Errorf("default sellEndTime = %q, want the start", got)
	}
	if got := req.ActionEvent.DoorsOpenTime; got == nil || *got != "" {
		t.Errorf("an existing session without doors must clear them, got %v", got)
	}
}
