package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func contactOf(name, email, phone string, hidden bool) openapi.OrganizerContact {
	return openapi.OrganizerContact{Name: name, Email: email, Phone: phone, PhoneHidden: hidden}
}

func TestSessionDialogs_LapseIsReportedOnce(t *testing.T) {
	d := newSessionDialogs()
	const id = int64(9)
	d.put(id, &sessionDialog{EventID: uuid.New()})
	if _, ok := d.get(id); !ok {
		t.Fatal("a fresh dialog must be live")
	}
	d.mu.Lock()
	d.byID[id].expires = time.Now().Add(-time.Minute)
	d.mu.Unlock()
	if _, ok := d.get(id); ok {
		t.Fatal("an expired dialog must not be returned")
	}
	if !d.takeLapsed(id) || d.takeLapsed(id) {
		t.Fatal("the lapse must be reported exactly once")
	}
	d.put(id, &sessionDialog{})
	if d.takeLapsed(id) {
		t.Fatal("a new dialog clears the lapse")
	}
}

// The chosen local day and time become the instant in the venue's zone — and
// on the day the clocks change, the zone's own offset applies.
func TestSesStart_UsesTheVenueZone(t *testing.T) {
	got, err := sesStart("2099-12-15", "20:00", "Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2099, 12, 15, 19, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("winter 20:00 Madrid = %s, want %s", got.UTC(), want)
	}
	got, _ = sesStart("2099-07-15", "20:00", "Europe/Madrid")
	if want := time.Date(2099, 7, 15, 18, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("summer 20:00 Madrid = %s, want %s", got.UTC(), want)
	}
	if _, err := sesStart("2099-07-15", "20:00", "Nowhere/Land"); err == nil {
		t.Fatal("an unknown zone must be an error, never silently UTC")
	}
	if got, _ := sesStart("2099-07-15", "20:00", ""); !got.Equal(time.Date(2099, 7, 15, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("no zone = UTC, got %s", got)
	}
}

// Telegram refuses a callback_data over 64 bytes: every button of the dialog,
// including the calendar's, must fit with the "ses:" prefix.
func TestSesKeyboard_CallbacksFitTelegram(t *testing.T) {
	rows := [][]Button{
		{{Label: "x", Data: "list:" + uuid.NewString()}},
		{{Label: "x", Data: "dc:2099-12-31"}, {Label: "x", Data: "cal:2099-12"}, {Label: "x", Data: "d:2099-12-31"}},
		{{Label: "x", Data: "t:19:30"}, {Label: "x", Data: "hide:1"}},
	}
	kb := sesKeyboard(rows)
	for _, line := range kb.InlineKeyboard {
		for _, b := range line {
			if !strings.HasPrefix(b.CallbackData, "ses:") {
				t.Errorf("callback %q lacks the ses: prefix", b.CallbackData)
			}
			if len(b.CallbackData) > 64 {
				t.Errorf("callback %q is %d bytes", b.CallbackData, len(b.CallbackData))
			}
		}
	}
}

func TestContactLabel_HidesAHiddenPhone(t *testing.T) {
	c := contactOf("Anna <b>", "a@b.c", "+34 600", true)
	got := contactLabel(c)
	if strings.Contains(got, "600") {
		t.Errorf("a hidden phone leaked: %q", got)
	}
	if !strings.Contains(got, "a@b.c") || strings.Contains(got, "<b>") {
		t.Errorf("label must carry the e-mail and escape the name: %q", got)
	}
	if got := contactLabel(contactOf("", "a@b.c", "+34 600", false)); !strings.Contains(got, "+34 600") {
		t.Errorf("a shown phone must appear: %q", got)
	}
}
