package salesnotify

import (
	"context"
	"testing"
)

// The daily Meta ad summary (spec 66 M2) is the kind 'ads'.
func TestNormalizeKinds_AcceptsAds(t *testing.T) {
	got, err := NormalizeKinds([]string{"ads", "tickets"})
	if err != nil || len(got) != 2 || got[0] != KindTickets || got[1] != KindAds {
		t.Fatalf("NormalizeKinds = %v, %v", got, err)
	}
	if _, err := NormalizeKinds([]string{"ad"}); err == nil {
		t.Error("an unknown kind must be refused")
	}
}

// AnnounceKindToOrg reaches only the organization's own chats that hear the
// kind, and a key goes out once.
func TestDispatcher_AnnounceKindToOrg(t *testing.T) {
	store := newFakeStore(
		Subscription{ID: "op", Name: "operator", ChatID: "-100", Kinds: []string{KindTickets, KindAds}},
		Subscription{ID: "ads", OrgID: orgVino, Name: "ads", ChatID: "-200", Kinds: []string{KindTickets, KindAds}},
		Subscription{ID: "tickets", OrgID: orgVino, Name: "tickets", ChatID: "-300", Kinds: []string{KindTickets}},
		Subscription{ID: "other", OrgID: orgLampyris, Name: "other", ChatID: "-400", Kinds: []string{KindAds}},
	)
	snd := &fakeSender{}
	d := NewDispatcher(store, snd, nil)
	ctx := context.Background()
	chats, delivered, err := d.AnnounceKindToOrg(ctx, orgVino, KindAds, "ads:vino:2026-11-22", "summary")
	if err != nil || chats != 1 || delivered != 1 {
		t.Fatalf("first: %d %d %v", chats, delivered, err)
	}
	if len(snd.sent) != 1 || snd.sent[0].chat != "-200" {
		t.Fatalf("sent = %+v", snd.sent)
	}
	if _, delivered, _ := d.AnnounceKindToOrg(ctx, orgVino, KindAds, "ads:vino:2026-11-22", "summary"); delivered != 0 || len(snd.sent) != 1 {
		t.Fatalf("a claimed key went out again: %+v", snd.sent)
	}
	// No chat hears the kind: the key is not claimed.
	if chats, _, _ := d.AnnounceKindToOrg(ctx, orgVino, KindShopDelivery, "k2", "x"); chats != 0 || store.claimed["k2"] {
		t.Fatalf("claimed without a chat")
	}
	if c, dl, err := NewDispatcher(store, nil, nil).AnnounceKindToOrg(ctx, orgVino, KindAds, "k3", "x"); c != 0 || dl != 0 || err != nil {
		t.Fatalf("no sender: %d %d %v", c, dl, err)
	}
}
