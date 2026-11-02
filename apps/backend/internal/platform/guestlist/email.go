package guestlist

import (
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/i18n"
)

// Locales the guest list speaks: the CSV headers and the letter.
var Locales = []string{"en", "ru", "es"}

// NormalizeLocale keeps a supported language subtag, else English.
func NormalizeLocale(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	for _, s := range Locales {
		if l == s {
			return l
		}
	}
	return "en"
}

// Letter is a rendered guest list e-mail.
type Letter struct {
	Subject string
	HTML    string
	Text    string
}

var (
	bundleOnce sync.Once
	bundle     *i18n.Bundle
)

func localizer(locale string) *goi18n.Localizer {
	bundleOnce.Do(func() {
		if b, err := i18n.NewBundle(); err == nil {
			bundle = b
		}
	})
	if bundle == nil {
		return nil
	}
	return bundle.LocalizerFor(locale)
}

func say(loc *goi18n.Localizer, id string, data map[string]string) string {
	if loc == nil {
		return id
	}
	out, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil {
		return id
	}
	return out
}

// When renders the session's start on the venue's clock, "02.01.2006 19:30".
func When(start time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	// allow:timeformat: a date for people in a letter, on the venue's clock.
	return start.In(loc).Format("02.01.2006 15:04")
}

// RenderEmail builds the letter that carries an automatic guest list. The
// file itself is the attachment; the letter names the event, the date, the
// moment and how many tickets the list holds.
func RenderEmail(locale, slot, eventName string, start time.Time, loc *time.Location, rows int) Letter {
	l := localizer(NormalizeLocale(locale))
	data := map[string]string{
		"Event": strings.Join(strings.Fields(eventName), " "),
		"When":  When(start, loc),
		"Rows":  strconv.Itoa(rows),
	}
	subject := say(l, "guestlist.email.subject", data)
	lines := []string{
		say(l, "guestlist.email.greeting", nil),
		say(l, "guestlist.email.attached", data),
		say(l, "guestlist.email.slot_"+slotKey(slot), nil),
		say(l, "guestlist.email.why", nil),
		say(l, "guestlist.email.stop", nil),
	}
	var hb strings.Builder
	hb.WriteString(`<!doctype html><html><body style="font-family:Arial,sans-serif;font-size:15px;line-height:1.5;color:#111">`)
	for _, line := range lines {
		hb.WriteString("<p>")
		hb.WriteString(html.EscapeString(line))
		hb.WriteString("</p>")
	}
	hb.WriteString("</body></html>")
	return Letter{Subject: subject, HTML: hb.String(), Text: strings.Join(lines, "\n\n")}
}

func slotKey(slot string) string {
	switch slot {
	case SlotH24, SlotH3, SlotDoors:
		return slot
	}
	return "manual"
}
