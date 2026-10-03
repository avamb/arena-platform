package sessionchange

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	venueA = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	venueB = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

func at(day, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, time.UTC)
}

func state(start time.Time, venue uuid.UUID) State {
	return State{StartAt: start, EndAt: start.Add(2 * time.Hour), VenueID: venue,
		VenueName: "V", Timezone: "Europe/Madrid", Status: "scheduled"}
}

func TestKinds(t *testing.T) {
	base := state(at(28, 18, 0), venueA)

	cancelled := base
	cancelled.Status = "cancelled"
	deleted := base
	deleted.Deleted = true

	endOnly := base
	endOnly.EndAt = base.EndAt.Add(time.Hour)

	otherVenueSameTime := state(at(28, 18, 0), venueB)
	otherDay := state(at(31, 18, 0), venueA)
	otherTime := state(at(28, 20, 30), venueA)
	both := state(at(31, 18, 0), venueB)

	// After the clocks went back (UTC+1), 23:30 Madrid on the 28th is 22:30
	// UTC and 00:30 Madrid on the 29th is 23:30 UTC: the UTC day is the same,
	// the venue's day is not.
	lateOld := state(at(28, 22, 30), venueA)
	lateNew := state(at(28, 23, 30), venueA)

	tests := []struct {
		name     string
		old, new State
		want     []string
	}{
		{"identical", base, base, nil},
		{"end time alone is not buyer-visible", base, endOnly, nil},
		{"other day is a date change", base, otherDay, []string{KindDate}},
		{"same day other time is a time change", base, otherTime, []string{KindTime}},
		{"venue only", base, otherVenueSameTime, []string{KindVenue}},
		{"date and venue travel together", base, both, []string{KindDate, KindVenue}},
		{"day is judged in the venue zone, not UTC", lateOld, lateNew, []string{KindDate}},
		{"cancelled by status", base, cancelled, []string{KindCancelled}},
		{"cancelled by soft delete", base, deleted, []string{KindCancelled}},
		{"cancelled wins over a simultaneous move", base,
			func() State { s := otherDay; s.Status = "cancelled"; return s }(), []string{KindCancelled}},
		{"already cancelled stays quiet", cancelled, cancelled, nil},
		{"already cancelled and then deleted stays quiet", cancelled,
			func() State { s := cancelled; s.Deleted = true; return s }(), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Kinds(tc.old, tc.new)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Kinds() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMovesBuyers(t *testing.T) {
	if MovesBuyers(nil) {
		t.Error("no kinds must not move buyers")
	}
	if !MovesBuyers([]string{KindDate, KindVenue}) {
		t.Error("a date change moves buyers")
	}
	if MovesBuyers([]string{KindCancelled}) {
		t.Error("a cancellation needs no new ticket")
	}
}

func TestNormalizeMessage(t *testing.T) {
	got, err := NormalizeMessage("  line one\r\nline two\x00\x07 \n\n ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "line one\nline two" {
		t.Fatalf("NormalizeMessage = %q", got)
	}
	if got, _ := NormalizeMessage(""); got != "" {
		t.Fatalf("empty message must stay empty, got %q", got)
	}
	// 1000 runes pass (multi-byte counted as runes), 1001 do not.
	if _, err := NormalizeMessage(strings.Repeat("я", MaxMessageRunes)); err != nil {
		t.Fatalf("exactly the limit must pass: %v", err)
	}
	if _, err := NormalizeMessage(strings.Repeat("я", MaxMessageRunes+1)); !errors.Is(err, ErrMessageTooLong) {
		t.Fatalf("over the limit: err = %v, want ErrMessageTooLong", err)
	}
}

func TestScrubError(t *testing.T) {
	got := ScrubError("550 rejected: buyer.name@example.com mailbox full\n" + strings.Repeat("x", 400))
	if strings.Contains(got, "@") || strings.Contains(got, "example.com") {
		t.Fatalf("address leaked: %q", got)
	}
	if !strings.Contains(got, "[address]") {
		t.Fatalf("address was not replaced: %q", got)
	}
	if n := len([]rune(got)); n > maxStoredError {
		t.Fatalf("not truncated: %d runes", n)
	}
	if ScrubError("   ") != "" {
		t.Fatal("blank error must stay blank")
	}
}

func TestContactPublicPhone(t *testing.T) {
	c := Contact{Email: "o@example.com", Phone: " +34 600 000 000 "}
	if c.PublicPhone() != "+34 600 000 000" {
		t.Fatalf("PublicPhone = %q", c.PublicPhone())
	}
	c.PhoneHidden = true
	if c.PublicPhone() != "" {
		t.Fatal("a hidden phone must never be printed")
	}
	if (Contact{Email: "  "}).Complete() {
		t.Fatal("a blank e-mail is not a contact")
	}
}

func TestDefaultMessage(t *testing.T) {
	for _, loc := range []string{"en", "ru", "cs", "de", "es", "fr", "he", "RU", "ru-RU", "pt", ""} {
		for _, kind := range []string{"change", "cancel"} {
			if DefaultMessage(kind, loc) == "" {
				t.Errorf("empty default message for %s/%s", kind, loc)
			}
		}
	}
	if DefaultMessage("change", "ru") == DefaultMessage("cancel", "ru") {
		t.Error("move and cancel must have different default texts")
	}
	if DefaultMessage("change", "klingon") != DefaultMessage("change", "en") {
		t.Error("unknown locale must fall back to English")
	}
	// No default text may promise or mention money: refunds are a separate
	// conversation between organizer and buyer.
	for loc, pair := range defaultMessages {
		for _, text := range pair {
			low := strings.ToLower(text)
			for _, bad := range []string{"refund", "reembols", "rembours", "erstatt", "возврат", "vrácen", "החזר", "money", "деньг"} {
				if strings.Contains(low, bad) {
					t.Errorf("%s default message mentions %q: %s", loc, bad, text)
				}
			}
		}
	}
	if KindForDefault([]string{KindCancelled}) != "cancel" || KindForDefault([]string{KindDate}) != "change" {
		t.Error("KindForDefault mapping is wrong")
	}
}
