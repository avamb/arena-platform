package eventbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func contactOf(name, email, phone string, hidden bool) openapi.OrganizerContact {
	return openapi.OrganizerContact{Name: name, Email: email, Phone: phone, PhoneHidden: hidden}
}

func TestSessionDialogs_StateSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	store := newMemDialogStore(clock)
	const id = int64(9)
	org, ev, ses := uuid.New(), uuid.New(), uuid.New()
	start := time.Date(2026, 12, 19, 18, 0, 0, 0, time.UTC)

	first := newSessionDialogs(store)
	first.put(ctx, id, &sessionDialog{
		OrgID: org, EventID: ev, EventName: "Gala", Step: sesStepConfirm, Mode: sesModeMove, Cur: 1,
		Items:   []sesItem{{ID: uuid.New(), Start: start, Status: "scheduled", Tz: "Europe/Madrid", Active: 3}, {ID: ses, Start: start.Add(24 * time.Hour), Status: "scheduled", Tz: "Europe/Madrid", Active: -1}},
		Date:    "2026-12-20",
		Time:    "19:30",
		Message: "New time", MessageSet: true,
		Impact: &sesImpact{Kinds: []string{"date", "time"}, Orders: 4, Tickets: 6, Old: "19 Dec", New: "20 Dec"},
		Cal:    &Draft{},
		MsgID:  42, CanSales: true,
	})

	// A new process: nothing but the store is shared.
	second := newSessionDialogs(store)
	got, ok, expired := second.get(ctx, id)
	if !ok || expired {
		t.Fatalf("get after a restart: ok=%v expired=%v; want the dialog", ok, expired)
	}
	if got.OrgID != org || got.EventID != ev || got.Step != sesStepConfirm || got.Mode != sesModeMove || got.Cur != 1 ||
		got.Date != "2026-12-20" || got.Time != "19:30" || got.Message != "New time" || !got.MessageSet ||
		got.MsgID != 42 || !got.CanSales || got.Cal == nil {
		t.Fatalf("state lost: %+v", got)
	}
	if len(got.Items) != 2 || got.Items[1].ID != ses || !got.Items[0].Start.Equal(start) || got.Items[1].Active != -1 {
		t.Fatalf("items lost: %+v", got.Items)
	}
	if got.Impact == nil || got.Impact.Orders != 4 || got.Impact.New != "20 Dec" {
		t.Fatalf("impact lost: %+v", got.Impact)
	}

	// Changes made by a handler are written back by save.
	got.Step = sesStepType
	second.save(ctx, id, got)
	again, _, _ := newSessionDialogs(store).get(ctx, id)
	if again.Step != sesStepType {
		t.Fatalf("step after save = %q; want %q", again.Step, sesStepType)
	}
}

func TestSessionDialogs_LapseIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	d := newSessionDialogs(newMemDialogStore(clock))
	const id = int64(9)
	d.put(ctx, id, &sessionDialog{OrgID: uuid.New(), EventID: uuid.New(), Step: sesStepCard})
	if _, ok, _ := d.get(ctx, id); !ok {
		t.Fatal("a fresh dialog must be live")
	}
	clock.advance(dialogTTL + time.Minute)
	if _, ok, expired := d.get(ctx, id); ok || !expired {
		t.Fatalf("an expired dialog: ok=%v expired=%v; want not live, expired once", ok, expired)
	}
	if _, ok, expired := d.get(ctx, id); ok || expired {
		t.Fatalf("the lapse must be reported exactly once: ok=%v expired=%v", ok, expired)
	}
}

func TestSessionDialogs_DroppedDialogIsNotWrittenBack(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	d := newSessionDialogs(newMemDialogStore(clock))
	const id = int64(9)
	d.put(ctx, id, &sessionDialog{OrgID: uuid.New(), Step: sesStepCard})
	dlg, _, _ := d.get(ctx, id)
	d.drop(ctx, id, dlg)
	d.save(ctx, id, dlg) // the deferred write-back of the entry point
	if _, ok, expired := d.get(ctx, id); ok || expired {
		t.Fatalf("a dropped dialog came back: ok=%v expired=%v", ok, expired)
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
