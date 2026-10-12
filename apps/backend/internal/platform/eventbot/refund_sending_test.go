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
