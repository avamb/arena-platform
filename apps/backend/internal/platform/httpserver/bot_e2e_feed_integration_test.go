//go:build integration

package httpserver

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The chat is a chronological feed (owner decision 2026-10-13): a press is
// answered by a NEW message at the bottom, the pressed message loses its
// keyboard, and a press on that old message afterwards does nothing but say
// the message is out of date.
func TestBotE2E_FeedNewMessagePerPressAndStalePressesRefused(t *testing.T) {
	pool := onboardingIntegrationPool(t)
	f := newBotInviteFixture(t, pool)
	srv := buildBotIntegrationServer(t, pool)
	api := httptest.NewServer(srv.router)
	defer api.Close()
	linkECBotUser(t, f, srv, ecOwnerTG, "owner")
	tg := newStubTelegram(t)
	stop := startDialogTestBot(t, pool, api, tg)
	defer func() { stop() }()
	d := ecDriver{t, tg, ecOwnerTG}

	d.say("/start", "Что будем делать?")
	home, _, homeKB := tg.lastOf(ecOwnerTG)
	if !hasPrefixData(homeKB, "help") {
		t.Fatalf("the menu has no help button: %v", homeKB)
	}

	// A press: a new message, and the menu above loses its buttons.
	help := d.press("help", "Что умеет бот")
	helpID, helpText, _ := tg.lastOf(ecOwnerTG)
	if helpID == home || !strings.Contains(helpText, "Что умеет бот") || helpText != help {
		t.Fatalf("the answer was not a new message at the bottom (menu %d, last %d)", home, helpID)
	}
	if kb := tg.keyboardOf(home); len(kb) != 0 {
		t.Fatalf("the pressed menu kept its buttons: %v", kb)
	}

	// A press on the old menu (a second tap, a screen whose keyboard could
	// not be removed) does nothing: the spinner stops with a note.
	sends := tg.callCount("sendMessage")
	answers := tg.callCount("answerCallbackQuery")
	tg.push(fmt.Sprintf(`"callback_query":{"id":"cb-stale","from":{"id":%d,"is_bot":false,"first_name":"U","language_code":"ru"},"chat_instance":"x","data":"help","message":{"message_id":%d,"date":%d,"chat":{"id":%d,"type":"private"},"text":"menu"}}`,
		ecOwnerTG, home, time.Now().Unix(), ecOwnerTG))
	tg.waitCall(t, "answerCallbackQuery", answers)
	time.Sleep(300 * time.Millisecond)
	tg.mu.Lock()
	note := tg.answers[len(tg.answers)-1]
	tg.mu.Unlock()
	if !strings.Contains(note, "устарело") {
		t.Errorf("the stale press was not told to carry on below: %q", note)
	}
	if got := tg.callCount("sendMessage"); got != sends {
		t.Fatalf("a press on an old message sent %d message(s)", got-sends)
	}
}
