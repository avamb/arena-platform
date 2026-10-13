package eventbot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
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

// failingSaveStore refuses to store a dialog at the given step.
type failingSaveStore struct {
	DialogStore
	step string
}

func (s failingSaveStore) Save(ctx context.Context, tg int64, orgID *uuid.UUID, kind, step string, state any, ttl time.Duration) error {
	if step == s.step {
		return errors.New("database down")
	}
	return s.DialogStore.Save(ctx, tg, orgID, kind, step, state, ttl)
}

// A send whose "being sent" state could not be stored is not started: with no
// record of it, nothing would stop a second press from sending another one.
func TestStartSendRefund_DoesNotSendWhenTheStateCannotBeStored(t *testing.T) {
	var arenaCalls atomic.Int32
	arenaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arenaCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer arenaSrv.Close()
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer tgSrv.Close()
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newRefundSendingBot(t)
	b.dialogs = failingSaveStore{DialogStore: b.dialogs, step: refStepSending}
	b.tg = tg
	b.texts = NewTexts(nil)
	b.arena = NewArenaClient(arenaSrv.URL, "", nil)
	b.minter = NewTokenMinter("secret", "arena-api", "arena-api")
	id := &Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New()}}
	st := refundDialog{OrgID: uuid.New(), OrderID: uuid.New(), Key: "tg-k", Reason: "r",
		Tickets: []refundTicket{{ID: uuid.New(), Refundable: 100, Sel: true}}}

	b.startSendRefund(context.Background(), 42, 7, &models.User{ID: 42}, id, st)
	b.bg.close()
	b.bg.wait()
	if n := arenaCalls.Load(); n != 0 {
		t.Fatalf("the refund was sent %d time(s) although its state could not be stored", n)
	}
}

// newPanickingSendBot is a bot whose token minter is missing, so the first
// thing a send does panics inside the real send code.
func newPanickingSendBot(t *testing.T) *Bot {
	t.Helper()
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"}}}`))
	}))
	t.Cleanup(tgSrv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newRefundSendingBot(t)
	b.tg = tg
	b.texts = NewTexts(nil)
	b.arena = NewArenaClient("http://127.0.0.1:1", "", nil)
	b.minter = nil
	return b
}

// A panic inside sendRefund itself (not a stand-in function) is recovered:
// the bot stays up and the dialog goes back to the final screen with the
// same key, so confirming again replays rather than repeats.
func TestSendRefund_APanicPutsTheDialogBackAtTheFinalScreen(t *testing.T) {
	b := newPanickingSendBot(t)
	id := &Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New()}}
	st := refundDialog{OrgID: uuid.New(), OrderID: uuid.New(), Key: "tg-panic", Reason: "r",
		Tickets: []refundTicket{{ID: uuid.New(), Refundable: 100, Sel: true}}}
	b.startSendRefund(context.Background(), 42, 7, &models.User{ID: 42}, id, st)
	b.bg.close()
	b.bg.wait()
	var got refundDialog
	step, found, _, err := b.dialogs.Load(context.Background(), 42, refundDialogKind, &got)
	if err != nil || !found || step != refStepFinal || got.Key != st.Key {
		t.Fatalf("after a panic in sendRefund: step=%q found=%v key=%q err=%v; want the final step with key %q", step, found, got.Key, err, st.Key)
	}
}

// A panic inside doQueueRetry itself is recovered and releases the retry, so
// a second press is accepted instead of "still sending" forever.
func TestQueueRetry_APanicReleasesTheRetry(t *testing.T) {
	b := newPanickingSendBot(t)
	id := &Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New()}}
	refundID := uuid.New()
	b.startQueueRetry(context.Background(), 42, 7, id, refundID)
	b.bg.close()
	b.bg.wait()
	if _, busy := b.retrying.Load(refundID); busy {
		t.Fatal("a panic in doQueueRetry left the retry marked as running")
	}
}
