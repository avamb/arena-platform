package eventbot

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

func newRefundSendingBot(t *testing.T) (*Bot, *testClock) {
	t.Helper()
	clock := &testClock{t: time.Now()}
	return &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dialogs: newMemDialogStore(clock)}, clock
}

// While a refund is with the server, a typed text is not the refund dialog's:
// it reaches whatever other dialog is waiting for it.
func TestRefundText_LeavesTextAloneWhileSending(t *testing.T) {
	b, _ := newRefundSendingBot(t)
	ctx := context.Background()
	st := refundDialog{OrgID: uuid.New(), Key: "tg-k", SendingAt: time.Now().UTC()}
	if err := b.dialogs.Save(ctx, 42, &st.OrgID, refundDialogKind, refStepSending, st, dialogTTL); err != nil {
		t.Fatal(err)
	}
	if b.refundText(ctx, 42, &models.User{ID: 42}, "PROMO10") {
		t.Fatal("the refund dialog took a typed text while its refund was being sent")
	}
}

// A send started by an earlier bot process (it was restarted mid-call) is not
// "being sent" any more: its dialog does not hold the person for the rest of
// the send window, and leaving the screen ends it.
func TestRefundSending_AnEarlierProcessDoesNotHoldTheDialog(t *testing.T) {
	b, _ := newRefundSendingBot(t)
	ctx := context.Background()
	org := uuid.New()
	state := map[string]any{"org_id": org, "key": "tg-k", "sending_at": time.Now().UTC(), "run_id": "an-earlier-run"}
	if err := b.dialogs.Save(ctx, 42, &org, refundDialogKind, refStepSending, state, dialogTTL); err != nil {
		t.Fatal(err)
	}
	b.leaveIdleRefund(ctx, 42)
	var st refundDialog
	if _, found, _, err := b.dialogs.Load(ctx, 42, refundDialogKind, &st); err != nil || found {
		t.Fatalf("a send of an earlier process still holds the dialog (found=%v, err=%v)", found, err)
	}
}
