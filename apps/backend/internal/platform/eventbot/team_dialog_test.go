package eventbot

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTeamDialogBot(clock *testClock) *Bot {
	return &Bot{dialogs: newMemDialogStore(clock), logger: slog.Default()}
}

// The invite dialog walks email -> role in the store and keeps the message it
// lives in, so the role screen edits the same prompt.
func TestTeamDialog_StepsAreKept(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	b := newTeamDialogBot(clock)
	org := uuid.New()

	if err := b.startTeamDialog(ctx, 42, org, 5); err != nil {
		t.Fatalf("start: %v", err)
	}
	dlg, step, found, expired := b.loadTeamDialog(ctx, 42)
	if !found || expired || step != teamStepEmail || dlg != (teamDialog{MsgID: 5}) {
		t.Fatalf("after start: %+v step=%q found=%v expired=%v", dlg, step, found, expired)
	}
	dlg.Email = "colleague@example.test"
	if err := b.setTeamDialogEmail(ctx, 42, org, dlg); err != nil {
		t.Fatalf("set e-mail: %v", err)
	}
	dlg, step, found, _ = b.loadTeamDialog(ctx, 42)
	if !found || step != teamStepRole || dlg != (teamDialog{Email: "colleague@example.test", MsgID: 5}) {
		t.Fatalf("after the e-mail: %+v step=%q found=%v", dlg, step, found)
	}
	b.clearTeamDialog(ctx, 42)
	if _, _, found, expired := b.loadTeamDialog(ctx, 42); found || expired {
		t.Fatal("a cleared dialog is gone, not expired")
	}
}

// A dialog that ran out is reported once, to the person it belonged to, and a
// fresh start forgets the lapse.
func TestTeamDialog_LapseIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	b := newTeamDialogBot(clock)
	org := uuid.New()

	_ = b.startTeamDialog(ctx, 42, org, 5)
	_ = b.startTeamDialog(ctx, 7, org, 6)
	clock.advance(dialogTTL + time.Second)

	if _, _, found, expired := b.loadTeamDialog(ctx, 42); found || !expired {
		t.Fatalf("the lapse must be reported: found=%v expired=%v", found, expired)
	}
	if _, _, found, expired := b.loadTeamDialog(ctx, 42); found || expired {
		t.Fatal("the lapse must be reported once")
	}

	// Someone else's lapse is not ours, and a new start wipes it.
	if err := b.startTeamDialog(ctx, 7, org, 9); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if dlg, _, found, expired := b.loadTeamDialog(ctx, 7); !found || expired || dlg.MsgID != 9 {
		t.Fatalf("a restarted dialog is live: %+v found=%v expired=%v", dlg, found, expired)
	}
}

// Answering slides the 30 minutes: the e-mail saved at minute 25 keeps the
// dialog alive at minute 50.
func TestTeamDialog_AnswerSlidesTheExpiry(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	b := newTeamDialogBot(clock)
	org := uuid.New()

	_ = b.startTeamDialog(ctx, 42, org, 5)
	clock.advance(25 * time.Minute)
	if err := b.setTeamDialogEmail(ctx, 42, org, teamDialog{Email: "x@example.test", MsgID: 5}); err != nil {
		t.Fatalf("set e-mail: %v", err)
	}
	clock.advance(25 * time.Minute)
	if _, step, found, _ := b.loadTeamDialog(ctx, 42); !found || step != teamStepRole {
		t.Fatalf("the answer must have moved the expiry: found=%v step=%q", found, step)
	}
}
