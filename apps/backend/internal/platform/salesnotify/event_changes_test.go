package salesnotify

import (
	"context"
	"errors"
	"testing"
)

// "Event published / changed" messages go to the operator's group only: an
// organization's own group never gets them, even when its row says so, and an
// operator row that did not switch them on gets none (owner decision
// 2026-10-07, migration 0124).
func TestRouteEventChanges_OnlyAnOperatorThatSwitchedItOn(t *testing.T) {
	subs := []Subscription{
		{ID: "operator-on", OrgID: "", ChatID: "-1", OnEventChanges: true},
		{ID: "operator-off", OrgID: "", ChatID: "-2"},
		{ID: "org-on", OrgID: orgVino, ChatID: "-3", OnEventChanges: true},
		{ID: "org-off", OrgID: orgLampyris, ChatID: "-4"},
	}
	got := RouteEventChanges(subs)
	if len(got) != 1 || got[0].ID != "operator-on" {
		t.Fatalf("want only operator-on, got %+v", got)
	}
}

func TestAnnounceEventChange_PostsToTheOperatorAndNobodyElse(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "operator", ChatID: "-100", OnEventChanges: true},
		Subscription{ID: "org", OrgID: orgVino, ChatID: "-200", OnEventChanges: true, OnOrderPaid: true},
		Subscription{ID: "plain-operator", ChatID: "-300", OnOrderPaid: true},
	)
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, nil)

	d.AnnounceEventChange(context.Background(), "🆕 New event published")

	if len(sender.sent) != 1 || sender.sent[0].chat != "-100" || sender.sent[0].text != "🆕 New event published" {
		t.Fatalf("want one message to -100, got %+v", sender.sent)
	}
	if store.lastErrs["operator"] != "" {
		t.Errorf("a successful delivery clears last_error, got %q", store.lastErrs["operator"])
	}
}

func TestAnnounceEventChange_WithoutABotTokenDoesNothing(_ *testing.T) {
	store := newFakeStore(Subscription{ID: "operator", ChatID: "-100", OnEventChanges: true})
	var d *Dispatcher
	d.AnnounceEventChange(context.Background(), "x") // nil receiver must not panic
	NewDispatcher(store, nil, nil).AnnounceEventChange(context.Background(), "x")
}

func TestAnnounceToOrg_PostsOnlyToThatOrganizationsChats(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "operator", ChatID: "-100", OnEventChanges: true, OnOrderPaid: true},
		Subscription{ID: "org", OrgID: orgVino, ChatID: "-200"},
		Subscription{ID: "other", OrgID: "org-other", ChatID: "-300", OnOrderPaid: true},
	)
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, nil)
	ctx := context.Background()

	if chats, delivered, err := d.AnnounceToOrg(ctx, orgVino, "✉️ Message"); err != nil || chats != 1 || delivered != 1 {
		t.Fatalf("chats=%d delivered=%d err=%v, want 1 1 nil", chats, delivered, err)
	}
	if len(sender.sent) != 1 || sender.sent[0].chat != "-200" {
		t.Fatalf("want one message to -200, got %+v", sender.sent)
	}
	if chats, _, _ := d.AnnounceToOrg(ctx, "", "x"); chats != 0 {
		t.Errorf("an empty organization reaches nobody, got %d", chats)
	}
	var nilD *Dispatcher
	if chats, _, err := nilD.AnnounceToOrg(ctx, orgVino, "x"); chats != 0 || err != nil {
		t.Errorf("nil dispatcher: got %d %v", chats, err)
	}

	// A chat that refuses the post is counted, not delivered: the caller retries.
	sender.failFor = map[string]error{"-200": errors.New("bot was kicked")}
	if chats, delivered, err := d.AnnounceToOrg(ctx, orgVino, "x"); err != nil || chats != 1 || delivered != 0 {
		t.Errorf("failed chat: chats=%d delivered=%d err=%v, want 1 0 nil", chats, delivered, err)
	}
}
