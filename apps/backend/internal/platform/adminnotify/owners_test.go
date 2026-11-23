package adminnotify

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMetaConnectionBrokenMessage(t *testing.T) {
	org := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	msg := MetaConnectionBrokenMessage("Театр <Колыбель>", "https://events.example.test/", org)
	if msg.Kind != KindMetaConnectionBroken {
		t.Errorf("kind = %q", msg.Kind)
	}
	wantLink := "https://events.example.test/o/" + org.String() + "/ads/meta"
	if msg.Link != wantLink {
		t.Errorf("link = %q, want %q", msg.Link, wantLink)
	}
	tg := TelegramText(msg)
	for _, want := range []string{
		"Подключение Meta организации Театр &lt;Колыбель&gt; перестало работать",
		"Покупки больше не уходят в Meta с сервера.",
		"Подключите Meta заново:",
		wantLink,
	} {
		if !strings.Contains(tg, want) {
			t.Errorf("Telegram text has no %q:\n%s", want, tg)
		}
	}
	if !strings.HasPrefix(tg, "⚠️ ") {
		t.Errorf("Telegram text icon: %q", tg)
	}
	subject, htmlBody, textBody := RenderEmail(EmailPayload{
		Kind: msg.Kind, Email: "o@example.test", Title: msg.Title, Details: msg.Details,
		Link: msg.Link, ButtonLabel: msg.ButtonLabel, WhatToDo: msg.WhatToDo,
	})
	if subject != "Подключение Meta организации Театр <Колыбель> перестало работать" {
		t.Errorf("subject = %q", subject)
	}
	if !strings.Contains(textBody, "Подключить Meta: "+wantLink) || !strings.Contains(htmlBody, wantLink) {
		t.Errorf("letter has no link:\n%s", textBody)
	}
	// A letter to an organizer signs with ArenaSoldOut.com, never "Arena Platform".
	if strings.Contains(htmlBody, "Arena Platform") || strings.Contains(textBody, "Arena Platform") ||
		!strings.HasSuffix(textBody, "ArenaSoldOut.com") {
		t.Errorf("letter brand:\n%s", textBody)
	}
	// No token, no Graph message: the notice is built from the name and the
	// id only, so nothing like a token or an OAuth text can appear.
	for _, s := range []string{tg, htmlBody, textBody} {
		for _, bad := range []string{"EAAB", "OAuth", "190", "access_token", "token"} {
			if strings.Contains(s, bad) {
				t.Errorf("notice carries %q", bad)
			}
		}
	}
	// Without the event center address there is no link, only the section.
	noLink := MetaConnectionBrokenMessage("A", "", org)
	if noLink.Link != "" || !strings.Contains(noLink.WhatToDo, "«Реклама»") {
		t.Errorf("no-link message = %+v", noLink)
	}
	// The default icon of the platform-admin notices is unchanged.
	if !strings.HasPrefix(TelegramText(Message{Title: "x"}), "🆕 ") {
		t.Error("default icon changed")
	}
}
