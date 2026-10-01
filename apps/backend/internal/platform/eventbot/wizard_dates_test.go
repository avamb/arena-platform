package eventbot

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testToday = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestDateCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// What the first organizer typed on 2026-10-01: a comma, a trailing dot.
		{"20.10,2026", []string{"2026-10-20"}},
		{"20.10.2026.", []string{"2026-10-20"}},
		{"20 10 2026", []string{"2026-10-20"}},
		{"20/10/26", []string{"2026-10-20"}},
		// Day and month that can be swapped are both offered, day first.
		{"04.05.2026", []string{"2026-05-04", "2026-04-05"}},
		{"040526", []string{"2026-05-04", "2026-04-05"}},
		{"4-5-2026", []string{"2026-05-04", "2026-04-05"}},
		{"05.05.2026", []string{"2026-05-05"}},
		{"31.12.2026", []string{"2026-12-31"}},
		// No year: the next such day.
		{"20.10", []string{"2026-10-20"}},
		{"5 апреля", []string{"2027-04-05"}},
		{"20 октября", []string{"2026-10-20"}},
		{"20 окт.", []string{"2026-10-20"}},
		{"October 20", []string{"2026-10-20"}},
		{"20 October 2027", []string{"2027-10-20"}},
		{"2026-10-20", []string{"2026-10-20"}},
		{"сегодня", []string{"2026-10-01"}},
		{"Завтра", []string{"2026-10-02"}},
		// Not dates.
		{"31.02.2026", nil},
		{"13.13.2026", nil},
		{"banana", nil},
		{"", nil},
		{"50", nil},
	}
	for _, c := range cases {
		if got := DateCandidates(c.in, testToday); !reflect.DeepEqual(got, c.want) {
			t.Errorf("DateCandidates(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDateWords(t *testing.T) {
	if got := DateWords("ru", "2026-10-20"); got != "вторник, 20 октября 2026" {
		t.Errorf("ru = %q", got)
	}
	if got := DateWords("en", "2026-10-20"); got != "Tuesday, 20 October 2026" {
		t.Errorf("en = %q", got)
	}
}

// atSessionDate gets a draft to the first date question.
func atSessionDate(t *testing.T) *wizardRun {
	t.Helper()
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	return r
}

func buttonData(s Screen) []string {
	var out []string
	for _, row := range s.Buttons {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	return out
}

// The question shows a calendar; a day that has passed is a dead cell and
// pressing it anyway is refused.
func TestDateQuestion_CalendarHasNoPastDays(t *testing.T) {
	r := atSessionDate(t)
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(screen.Buttons), "Октябрь 2026") {
		t.Fatalf("the calendar should open on October 2026:\n%v", screen.Buttons)
	}
	data := buttonData(screen)
	if !contains(data, "d:2026-10-01") || !contains(data, "d:2026-10-31") {
		t.Errorf("today and the end of the month must be pickable: %v", data)
	}
	for _, past := range []string{"d:2026-09-30", "d:2026-09-01"} {
		if contains(data, past) {
			t.Errorf("%s is in the past and must not be a button", past)
		}
	}
	if contains(data, "cal:2026-09") {
		t.Error("there is nothing to pick in September: the left arrow must be dead")
	}
	if !contains(data, "cal:2026-11") {
		t.Error("the right arrow must page to November")
	}
	// A shortcut for tomorrow exists and today is offered.
	if !contains(data, "d:2026-10-02") {
		t.Error("tomorrow should be pickable")
	}

	note := r.step(WizInput{Data: "d:2026-09-30"}, stSDate)
	if !strings.Contains(note, "уже прошла") {
		t.Errorf("a past day pressed anyway: %q", note)
	}
	// Paging moves the calendar, the question stays.
	r.press("cal:2026-11", stSDate)
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	if !strings.Contains(fmt.Sprint(screen.Buttons), "Ноябрь 2026") {
		t.Errorf("did not page to November:\n%v", screen.Buttons)
	}
	// A pressed day is the answer.
	r.press("d:2026-11-14", stSTime)
	if r.d.Sessions[0].Date != "2026-11-14" {
		t.Errorf("date = %q", r.d.Sessions[0].Date)
	}
}

// A typed date is confirmed in words; a swappable one offers both readings,
// and nothing is taken until one is chosen.
func TestDateQuestion_TypedDateIsConfirmed(t *testing.T) {
	r := atSessionDate(t)
	// 04.05.2027: both readings are in the future.
	r.step(WizInput{Text: "04.05.2027"}, stSDate)
	if got := r.d.Scratch.DatePending; !reflect.DeepEqual(got, []string{"2027-05-04", "2027-04-05"}) {
		t.Fatalf("pending = %v", got)
	}
	if r.d.Sessions != nil && r.d.Sessions[0].Date != "" {
		t.Fatalf("a typed date must not be taken before it is confirmed: %q", r.d.Sessions[0].Date)
	}
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	for _, want := range []string{"04.05.2027", "вторник, 4 мая 2027", "понедельник, 5 апреля 2027"} {
		if !strings.Contains(screen.Text+fmt.Sprint(screen.Buttons), want) {
			t.Errorf("confirmation lacks %q:\n%s\n%v", want, screen.Text, screen.Buttons)
		}
	}
	// "Another date" goes back to the calendar.
	r.press("dc:retry", stSDate)
	if len(r.d.Scratch.DatePending) != 0 {
		t.Fatal("retry must drop the pending dates")
	}
	r.step(WizInput{Text: "04.05.2027"}, stSDate)
	// Only an offered reading can be confirmed.
	r.step(WizInput{Data: "dc:2027-06-06"}, stSDate)
	r.press("dc:2027-04-05", stSTime)
	if r.d.Sessions[0].Date != "2027-04-05" {
		t.Errorf("date = %q, want the second reading", r.d.Sessions[0].Date)
	}

	// An unambiguous one is confirmed too, with a single reading.
	r2 := atSessionDate(t)
	r2.step(WizInput{Text: "20.10,2026"}, stSDate)
	if got := r2.d.Scratch.DatePending; !reflect.DeepEqual(got, []string{"2026-10-20"}) {
		t.Fatalf("pending = %v", got)
	}
	screen, _ = r2.w.Render(context.Background(), r2.ws, r2.d)
	if !strings.Contains(screen.Text, "вторник, 20 октября 2026") {
		t.Errorf("single confirmation:\n%s", screen.Text)
	}

	// Text that is not a date keeps the old hint.
	r3 := atSessionDate(t)
	if note := r3.step(WizInput{Text: "banana"}, stSDate); !strings.Contains(note, "Не понял дату") {
		t.Errorf("note = %q", note)
	}
	// A past date typed in is refused with the reason, nothing pending.
	if note := r3.step(WizInput{Text: "15.09.2026"}, stSDate); !strings.Contains(note, "уже прошла") || len(r3.d.Scratch.DatePending) != 0 {
		t.Errorf("past date: note=%q pending=%v", note, r3.d.Scratch.DatePending)
	}
}

// Of two readings, the one that has passed is not offered.
func TestDateQuestion_PastReadingIsNotOffered(t *testing.T) {
	r := atSessionDate(t)
	// 02.11.2026 = 2 Nov, or 11 Feb (2026, past): only the first is offered.
	r.step(WizInput{Text: "02.11.2026"}, stSDate)
	if got := r.d.Scratch.DatePending; !reflect.DeepEqual(got, []string{"2026-11-02"}) {
		t.Fatalf("pending = %v", got)
	}
}

// The prices live between today and the day of the event.
func TestDateQuestion_PriceDatesStayWithinTheEvent(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate() // the event is on 15.12.2026
	r.press("next", stTMode)
	r.press("mode:single", stTName)
	r.text("Вход", stTPrice)
	r.text("20", stTChanges)
	r.press("yes", stTChangeDate)

	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	data := buttonData(screen)
	if !contains(data, "d:2026-10-01") || contains(data, "d:2026-09-30") {
		t.Errorf("the range starts today: %v", data)
	}
	r.press("cal:2026-12", stTChangeDate)
	screenDec, _ := r.w.Render(context.Background(), r.ws, r.d)
	dec := buttonData(screenDec)
	if !contains(dec, "d:2026-12-15") || contains(dec, "d:2026-12-16") || contains(dec, "cal:2027-01") {
		t.Errorf("the range ends on the day of the event: %v", dec)
	}
	r.press("cal:2026-10", stTChangeDate)
	if !strings.Contains(screen.Text, "01.10.2026") || !strings.Contains(screen.Text, "15.12.2026") {
		t.Errorf("the range should be said in words:\n%s", screen.Text)
	}
	if note := r.text("16.12.2026", stTChangeDate); !strings.Contains(note, "Позже 15.12.2026") {
		t.Errorf("after the event: %q", note)
	}
	r.press("d:2026-11-01", stTChangePrice)
	r.text("25", stTChangeMore)
	r.press("more", stTChangeDate)
	// The next change cannot be on or before the previous one.
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	data = buttonData(screen)
	if contains(data, "d:2026-11-01") || !contains(data, "d:2026-11-02") {
		t.Errorf("the second change starts the day after the first: %v", data)
	}
	if note := r.text("01.11.2026", stTChangeDate); !strings.Contains(note, "Раньше 02.11.2026") {
		t.Errorf("not after the previous change: %q", note)
	}
}

// A category's end is held to the same range, a count still works, and a date
// with a count is confirmed together.
func TestDateQuestion_CategoryEnd(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:sequence", stTCatName)
	r.text("Early", stTCatPrice)
	r.text("20", stTCatUntil)

	// A date after the event is refused; the range is said.
	if note := r.text("20.12.2026", stTCatUntil); !strings.Contains(note, "Позже 15.12.2026") {
		t.Errorf("after the event: %q", note)
	}
	// "date count" is confirmed with the count shown.
	r.step(WizInput{Text: "31.10.2026 50"}, stTCatUntil)
	if r.d.Scratch.DateLimit != 50 || !reflect.DeepEqual(r.d.Scratch.DatePending, []string{"2026-10-31"}) {
		t.Fatalf("pending = %v limit %d", r.d.Scratch.DatePending, r.d.Scratch.DateLimit)
	}
	screen, _ := r.w.Render(context.Background(), r.ws, r.d)
	if !strings.Contains(screen.Text, "50") {
		t.Errorf("the confirmation should show the count:\n%s", screen.Text)
	}
	r.press("dc:2026-10-31", stTCatName)
	c := r.d.Tickets.Categories[0]
	if c.SellUntil != "2026-10-31" || c.SellLimit != 50 {
		t.Errorf("category = %+v", c)
	}
	r.text("Regular", stTCatPrice)
	r.text("30", stTCatLast)
	r.press("next", stTCatUntil)
	// The next end cannot be before the previous one.
	screen, _ = r.w.Render(context.Background(), r.ws, r.d)
	data := buttonData(screen)
	if contains(data, "d:2026-10-30") || !contains(data, "d:2026-10-31") {
		t.Errorf("the second end starts at the previous one: %v", data)
	}
	// A count alone is not a date at all.
	r.text("100", stTCatName)
}

// A day picked on the calendar answers the category question without a limit.
func TestDateQuestion_PickedDayEndsACategory(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate()
	r.press("next", stTMode)
	r.press("mode:multi", stTKind)
	r.press("kind:sequence", stTCatName)
	r.text("Early", stTCatPrice)
	r.text("20", stTCatUntil)
	r.press("d:2026-11-30", stTCatName)
	if c := r.d.Tickets.Categories[0]; c.SellUntil != "2026-11-30" || c.SellLimit != 0 {
		t.Errorf("category = %+v", c)
	}
}

// With no room between today and the event the question says so instead of
// showing an empty calendar.
func TestDateQuestion_NoRoomLeft(t *testing.T) {
	r := newWizardRun(t, newFakeRefs())
	r.eventHead()
	r.firstDate()
	r.d.Sessions[0].Date = "2026-09-20" // the event is already behind us
	r.d.Step = stTChangeDate
	screen, err := r.w.Render(context.Background(), r.ws, r.d)
	if err != nil || !strings.Contains(screen.Text, "Выбрать нечего") {
		t.Fatalf("screen = %+v %v", screen, err)
	}
}

// Both languages render the calendar and the confirmation.
func TestDateQuestion_RendersInBothLocales(t *testing.T) {
	for _, loc := range SupportedLocales {
		r := atSessionDate(t)
		r.ws.Locale = loc
		for _, pending := range [][]string{nil, {"2026-10-20"}, {"2026-05-04", "2026-04-05"}} {
			r.d.Scratch.DatePending, r.d.Scratch.DateRaw = pending, "04.05.2026"
			screen, err := r.w.Render(context.Background(), r.ws, r.d)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(screen.Text, "bot.wz.") || strings.Contains(screen.Text, "<no value>") {
				t.Errorf("%s: %q", loc, screen.Text)
			}
			for _, row := range screen.Buttons {
				if len(row) > 8 {
					t.Errorf("%s: a button row of %d (Telegram allows 8)", loc, len(row))
				}
				for _, b := range row {
					if b.Label == "" || b.Data == "" || strings.Contains(b.Label, "bot.") {
						t.Errorf("%s: button %+v", loc, b)
					}
				}
			}
		}
	}
}
