package onboarding

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/adminnotify"
)

func strp(s string) *string { return &s }

func TestAdminMessageAndTelegramText(t *testing.T) {
	app := &Application{ID: uuid.New(), OrgName: strp("A <b> & Co"), Country: strp("ES"), Email: "a@example.com", Source: "site"}
	title, details := adminMessage(app, true, true)
	if !strings.Contains(title, "повторно") {
		t.Fatalf("title = %q", title)
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{"Организация: A <b> & Co", "Страна: ES", "Заявитель: —", "E-mail: a@example.com", "Телефон: —", "Источник: site", "Система одобрила бы: да"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details lack %q: %s", want, joined)
		}
	}
	text := adminnotify.TelegramText(adminnotify.Message{Title: title, Details: details, Link: "https://admin.example.test/onboarding/x", WhatToDo: adminWhatToDo})
	if strings.Contains(text, "A <b> &") || !strings.Contains(text, "A &lt;b&gt; &amp; Co") {
		t.Errorf("telegram text is not escaped: %s", text)
	}
	if !strings.Contains(text, "Что делать") || !strings.HasSuffix(text, "https://admin.example.test/onboarding/x") {
		t.Errorf("telegram text = %s", text)
	}
}
