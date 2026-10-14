package eventbot

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

func followupBot(t *testing.T) *Bot {
	t.Helper()
	clock := &testClock{t: time.Now()}
	return &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dialogs: newMemDialogStore(clock)}
}

// A text goes to the NEWEST open dialog that takes it, wherever that dialog
// sits in the fixed table order: a fixed order of handlers would hand the
// word of a just-opened orders prompt to an older sessions dialog.
func TestRouteDialogText_NewestThatTakesIt(t *testing.T) {
	t.Parallel()
	b := followupBot(t)
	ctx := context.Background()
	org := uuid.New()
	var got []string
	takes := map[string]bool{}
	record := func(kind string) dialogTextHandler {
		return func(_ *Bot, _ context.Context, _ int64, _ *models.User, _ string) bool {
			got = append(got, kind)
			return takes[kind]
		}
	}
	b.textRoutes = map[string]dialogTextHandler{}
	for _, kind := range dialogOwnerOrder {
		b.textRoutes[kind] = record(kind)
	}
	save := func(kind string) {
		if err := b.dialogs.Save(ctx, 7, &org, kind, "x", map[string]any{}, dialogTTL); err != nil {
			t.Fatal(err)
		}
	}
	from := &models.User{ID: 7}
	// sessions (late in the table) opened LAST: it is the newest.
	save(ordersDialogKind)
	save(sesDialogKind)
	takes[ordersDialogKind], takes[sesDialogKind] = true, true
	if !b.routeDialogText(ctx, 7, from, "word") || !reflect.DeepEqual(got, []string{sesDialogKind}) {
		t.Fatalf("the newest dialog must take the text alone, offered to %v", got)
	}
	// orders (early in the table) touched again: now it is the newest.
	got = nil
	save(ordersDialogKind)
	if !b.routeDialogText(ctx, 7, from, "word") || !reflect.DeepEqual(got, []string{ordersDialogKind}) {
		t.Fatalf("after a new save the orders dialog is the newest, offered to %v", got)
	}
	// The newest declines (it waits for buttons): the next newest gets it.
	got = nil
	takes[ordersDialogKind] = false
	if !b.routeDialogText(ctx, 7, from, "word") || !reflect.DeepEqual(got, []string{ordersDialogKind, sesDialogKind}) {
		t.Fatalf("a declining dialog passes the text on, offered to %v", got)
	}
	// Nobody takes it.
	got = nil
	takes[sesDialogKind] = false
	if b.routeDialogText(ctx, 7, from, "word") {
		t.Fatal("no dialog took the text")
	}
}

// A background write (SaveQuiet) changes the dialog but never makes it the
// newest one, and never brings back a dialog that is gone.
func TestDialogStore_SaveQuietKeepsRecency(t *testing.T) {
	t.Parallel()
	b := followupBot(t)
	ctx := context.Background()
	org := uuid.New()
	for _, kind := range []string{refundDialogKind, sesDialogKind} {
		if err := b.dialogs.Save(ctx, 7, &org, kind, "x", map[string]any{}, dialogTTL); err != nil {
			t.Fatal(err)
		}
	}
	found, err := b.dialogs.SaveQuiet(ctx, 7, &org, refundDialogKind, refStepFinal, refundDialog{Key: "tg-k"}, dialogTTL)
	if err != nil || !found {
		t.Fatalf("quiet save: found=%v err=%v", found, err)
	}
	kinds, _ := b.dialogs.Recent(ctx, 7)
	if kinds[0] != sesDialogKind {
		t.Fatalf("a quiet save made the refund the newest dialog: %v", kinds)
	}
	var st refundDialog
	if step, _, _, _ := b.dialogs.Load(ctx, 7, refundDialogKind, &st); step != refStepFinal || st.Key != "tg-k" {
		t.Fatalf("the quiet save was not stored: step=%q key=%q", step, st.Key)
	}
	if found, _ := b.dialogs.SaveQuiet(ctx, 8, &org, refundDialogKind, refStepFinal, refundDialog{}, dialogTTL); found {
		t.Fatal("a quiet save created a dialog that did not exist")
	}
}

// "Cancel" on a refund: one that may have started is parked with its key,
// anything else ends.
func TestCancelRefund(t *testing.T) {
	t.Parallel()
	b := followupBot(t)
	ctx := context.Background()
	org := uuid.New()
	cases := []struct {
		name   string
		st     refundDialog
		step   string
		want   string
		parked bool
	}{
		{"maybe started", refundDialog{OrgID: org, Key: "tg-a", Attempted: true}, refStepFinal, "bot.ref.cancel_maybe_started", true},
		{"lost to a restart", refundDialog{OrgID: org, Key: "tg-b"}, refStepSending, "bot.ref.cancel_maybe_started", true},
		{"never sent", refundDialog{OrgID: org, Key: "tg-c"}, refStepFinal, "bot.ref.cancelled", false},
		{"still picking", refundDialog{OrgID: org}, refStepPick, "bot.ref.cancelled", false},
	}
	for i, c := range cases {
		tg := int64(100 + i)
		if err := b.dialogs.Save(ctx, tg, &org, refundDialogKind, c.step, c.st, dialogTTL); err != nil {
			t.Fatal(err)
		}
		if got := b.cancelRefund(ctx, tg, c.st, c.step); got != c.want {
			t.Errorf("%s: text %q, want %q", c.name, got, c.want)
		}
		var st refundDialog
		step, found, _, _ := b.dialogs.Load(ctx, tg, refundDialogKind, &st)
		if found != c.parked {
			t.Errorf("%s: dialog kept=%v, want %v", c.name, found, c.parked)
		}
		if c.parked && (!st.Parked || st.Key != c.st.Key || step != c.step) {
			t.Errorf("%s: parked=%v key=%q step=%q", c.name, st.Parked, st.Key, step)
		}
	}
}

// An operation that may have started keeps its key well past the usual
// dialog lifetime.
func TestRefundTTL(t *testing.T) {
	t.Parallel()
	if refundTTL(refundDialog{}) != dialogTTL {
		t.Error("a refund that was never sent lives as long as any dialog")
	}
	if refundTTL(refundDialog{Attempted: true}) < 24*time.Hour {
		t.Error("the key of a refund that may have started must outlive a day")
	}
}

// The order's cancel-word step ends on every press but its own, while the
// list and the card stay; a press of the cancel button keeps it.
func TestNarrowOrderCancel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	org := uuid.New()
	order := uuid.New()
	for _, c := range []struct {
		data string
		want string
	}{
		{"or:f:s:" + order.String(), ordStepCard},
		{"or:r:s:" + order.String(), ordStepCard},
		{"or:v:" + order.String(), ordStepCard},
		{"or:c:" + order.String(), ordStepCancel},
		{"noop", ordStepCancel},
	} {
		b := followupBot(t)
		st := newOrdersDialog(org)
		st.CardID = &order
		if err := b.dialogs.Save(ctx, 7, &org, ordersDialogKind, ordStepCancel, st, dialogTTL); err != nil {
			t.Fatal(err)
		}
		b.leaveForeignDialogs(ctx, 7, c.data)
		var got ordersDialog
		step, found, _, _ := b.dialogs.Load(ctx, 7, ordersDialogKind, &got)
		if !found || step != c.want {
			t.Errorf("press %q: orders dialog found=%v step=%q, want %q", c.data, found, step, c.want)
		}
	}
}
