package eventbot

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// textRecorder is a stub Bot API that keeps the text of every message the bot
// sends or edits.
type textRecorder struct {
	mu    sync.Mutex
	texts []string
}

func (rec *textRecorder) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if method == "sendMessage" || method == "editMessageText" {
		text := ""
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); strings.HasPrefix(mt, "multipart/") {
			if r.ParseMultipartForm(1<<20) == nil {
				text = r.FormValue("text")
			}
		} else {
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			if json.Unmarshal(raw, &body) == nil {
				text, _ = body["text"].(string)
			}
		}
		rec.mu.Lock()
		rec.texts = append(rec.texts, text)
		rec.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"}}}`))
}

func (rec *textRecorder) has(want string) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, s := range rec.texts {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func newTurnWaitBot(t *testing.T, arenaCalls *atomic.Int32) (*Bot, *textRecorder) {
	t.Helper()
	rec := &textRecorder{}
	tgSrv := httptest.NewServer(http.HandlerFunc(rec.serve))
	t.Cleanup(tgSrv.Close)
	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	arenaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arenaCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"refunds":[]}`))
	}))
	t.Cleanup(arenaSrv.Close)
	b, _ := newRefundSendingBot(t)
	b.tg = tg
	b.texts = NewTexts(nil)
	b.minter = NewTokenMinter("test-secret", "arena", "arena-api")
	b.arena = NewArenaClient(arenaSrv.URL, "", nil)
	b.serial = newUserSerial(b.logger)
	return b, rec
}

func turnWaitDialog() (refundDialog, *Identity) {
	return refundDialog{OrgID: uuid.New(), OrderID: uuid.New(), Key: "tg-turnwait", Reason: "r",
			Tickets: []refundTicket{{ID: uuid.New(), Refundable: 100, Sel: true}}},
		&Identity{Link: gen.BotTelegramLinkRow{UserID: uuid.New()}}
}

// M1: the person's lane is busy longer than the background send waits for
// it (a poster being read, a long save). The answer still reaches the person
// at once, and once the lane frees the dialog does not stay in sending.
func TestSendRefund_BusyLaneStillGetsTheAnswerAndLeavesSending(t *testing.T) {
	saved := refundTurnWait
	refundTurnWait = 100 * time.Millisecond
	t.Cleanup(func() { refundTurnWait = saved })
	var calls atomic.Int32
	b, rec := newTurnWaitBot(t, &calls)
	ctx := context.Background()
	st, id := turnWaitDialog()

	hold := make(chan struct{})
	if err := b.serial.submit(42, func() { <-hold }); err != nil {
		t.Fatal(err)
	}
	b.startSendRefund(withTurn(ctx, 42), 42, 7, &models.User{ID: 42}, id, st)
	result := b.refundResultText("en", st, openapi.CreateOrderRefundsResponse{})
	deadline := time.Now().Add(5 * time.Second)
	for !rec.has(result) {
		if time.Now().After(deadline) {
			close(hold)
			t.Fatalf("the person's lane was busy and the answer never reached them; sent %q", rec.texts)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var mid refundDialog
	if step, found, _, _ := b.dialogs.Load(ctx, 42, refundDialogKind, &mid); !found || step != refStepSending {
		t.Fatalf("while the lane is busy the dialog changed outside the turn: step=%q found=%v", step, found)
	}
	close(hold)
	for {
		var cur refundDialog
		step, found, _, err := b.dialogs.Load(ctx, 42, refundDialogKind, &cur)
		if err == nil && (!found || step != refStepSending) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the lane freed the dialog stayed in sending: step=%q", step)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b.bg.close()
	b.bg.wait()
	if calls.Load() != 1 {
		t.Fatalf("arena was called %d times; want 1", calls.Load())
	}
}

// A send pressed while the bot shuts down is not started in the background
// (it would wait for the person's own lane): nothing is sent, the person is
// told, and the dialog is back on the final screen with the same key.
func TestSendRefund_DuringShutdownSendsNothing(t *testing.T) {
	var calls atomic.Int32
	b, rec := newTurnWaitBot(t, &calls)
	ctx := context.Background()
	st, id := turnWaitDialog()
	b.bg.close()
	b.startSendRefund(withTurn(ctx, 42), 42, 7, &models.User{ID: 42}, id, st)
	b.bg.wait()
	if calls.Load() != 0 {
		t.Fatalf("arena was called %d times during shutdown", calls.Load())
	}
	if !rec.has("bot.ref.err_unavailable") {
		t.Fatalf("the person was not told; sent %q", rec.texts)
	}
	var got refundDialog
	if step, found, _, err := b.dialogs.Load(ctx, 42, refundDialogKind, &got); err != nil || !found || step != refStepFinal || got.Key != st.Key {
		t.Fatalf("dialog after a refused start: step=%q found=%v key=%q err=%v; want the final step with the same key", step, found, got.Key, err)
	}
}
