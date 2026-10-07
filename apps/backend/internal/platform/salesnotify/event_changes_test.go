package salesnotify

import (
	"context"
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
