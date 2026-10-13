package eventbot

import (
	"context"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"
)

// A text from another chat than the person's private one is never an answer
// of the resend dialog: it must not consume the dialog (an expired one is
// reported once, in the private chat) nor be answered there.
func TestResendText_IgnoresAnotherChat(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	b := newTeamDialogBot(clock)
	org := uuid.New()
	if err := b.dialogs.Save(ctx, 42, &org, resendDialogKind, resStepEmail, resendDialog{OrgID: org}, dialogTTL); err != nil {
		t.Fatal(err)
	}
	clock.t = clock.t.Add(dialogTTL + time.Minute)
	if b.resendText(ctx, -100123, &models.User{ID: 42}, "buyer@example.test") {
		t.Fatal("a group message was taken for the resend dialog's answer")
	}
	if _, found, expired, err := b.dialogs.Load(ctx, 42, resendDialogKind, nil); err != nil || found || !expired {
		t.Fatalf("after the group message: found=%v expired=%v err=%v; want the expiry still owed to the private chat", found, expired, err)
	}
}
