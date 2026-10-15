package eventbot

// serial_bot_test.go — the whole path an update takes: the Telegram library
// long-polls a stub Bot API, calls handleUpdate, and the update is handled in
// its sender's turn. The route is replaced by a small dialog that does what
// the real ones do (read the step, reply, write the next step), so the tests
// need neither a database nor arena-api.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// stubBotAPI answers getUpdates with one batch and every other method with a
// generic success, optionally after a delay (a slow Telegram widens the race
// these tests are about).
type stubBotAPI struct {
	srv       *httptest.Server
	mu        sync.Mutex
	batch     []string
	served    bool
	sendDelay time.Duration
	// onSend, when set, runs while a sendMessage/editMessageText request is
	// being answered.
	onSend func()
}

func newStubBotAPI(t *testing.T, updates ...string) *stubBotAPI {
	t.Helper()
	s := &stubBotAPI{batch: updates}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubBotAPI) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getUpdates":
		s.mu.Lock()
		first := !s.served
		s.served = true
		s.mu.Unlock()
		if first {
			_, _ = fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(s.batch, ","))
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	case "sendMessage", "editMessageText":
		if s.onSend != nil {
			s.onSend()
		}
		time.Sleep(s.sendDelay)
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
	default:
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}
}

// textUpdate is a private-chat message from account `from`.
func textUpdate(updateID int, from int64, text string) string {
	u := map[string]any{
		"update_id": updateID,
		"message": map[string]any{
			"message_id": updateID, "date": 0, "text": text,
			"chat": map[string]any{"id": from, "type": "private"},
			"from": map[string]any{"id": from, "is_bot": false, "first_name": "T"},
		},
	}
	raw, _ := json.Marshal(u)
	return string(raw)
}

// newStubbedBot builds a Bot that polls stub, with an in-memory dialog store.
func newStubbedBot(t *testing.T, stub *stubBotAPI) *Bot {
	t.Helper()
	b, err := New(Options{
		Token:             "123:test",
		Queries:           gen.New(nil),
		Arena:             NewArenaClient("http://127.0.0.1:1", "", nil),
		Minter:            NewTokenMinter("secret", "iss", "aud"),
		TelegramServerURL: stub.srv.URL,
		HTTPClient:        stub.srv.Client(),
		MessageRateLimit:  -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newMemDialogStore(&testClock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)})
	b.dialogs = store
	b.sessions = newSessionDialogs(store)
	return b
}

// runBot polls until the test ends.
func runBot(t *testing.T, b *Bot) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.tg.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// testDialogKind is the dialog the stand-in route keeps in the store.
const testDialogKind = "serial_test"

// TestBot_SameAccountUpdatesSeeTheStepTheFirstOneSaved is the race the review
// found: two quick messages of one person. The first reads step 0, replies
// (slowly) and saves step 1; the second must read step 1. When the library
// ran each update on its own goroutine both read step 0.
func TestBot_SameAccountUpdatesSeeTheStepTheFirstOneSaved(t *testing.T) {
	stub := newStubBotAPI(t, textUpdate(1, 42, "a"), textUpdate(2, 42, "b"))
	stub.sendDelay = 50 * time.Millisecond
	b := newStubbedBot(t, stub)

	var (
		mu      sync.Mutex
		seen    []string
		running atomic.Int32
		overlap atomic.Bool
		wg      sync.WaitGroup
	)
	wg.Add(2)
	b.route = func(ctx context.Context, u *models.Update) {
		defer wg.Done()
		if running.Add(1) > 1 {
			overlap.Store(true)
		}
		defer running.Add(-1)
		id := u.Message.From.ID
		step, found, _, err := b.dialogs.Load(ctx, id, testDialogKind, nil)
		if err != nil {
			t.Error(err)
			return
		}
		if !found {
			step = "0"
		}
		mu.Lock()
		seen = append(seen, u.Message.Text+"@"+step)
		mu.Unlock()
		b.send(ctx, u.Message.Chat.ID, "question for step "+step, nil)
		next := map[string]string{"0": "1", "1": "2"}[step]
		if err := b.dialogs.Save(ctx, id, nil, testDialogKind, next, struct{}{}, dialogTTL); err != nil {
			t.Error(err)
		}
	}
	runBot(t, b)
	waitGroupOrFail(t, &wg)

	if overlap.Load() {
		t.Error("two updates of one account were handled at the same time")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(seen, ","); got != "a@0,b@1" {
		t.Fatalf("handled %q, want \"a@0,b@1\" (the second update in order, after the first one's step)", got)
	}
}

// TestBot_DifferentAccountsInterleave: one account's slow handler must not
// hold up another account. Account 1's handler waits until account 2's has
// run; with one global lock it would wait forever.
func TestBot_DifferentAccountsInterleave(t *testing.T) {
	stub := newStubBotAPI(t, textUpdate(1, 1, "slow"), textUpdate(2, 2, "fast"), textUpdate(3, 1, "after"))
	b := newStubbedBot(t, stub)

	secondRan := make(chan struct{})
	var (
		mu    sync.Mutex
		order []string
		wg    sync.WaitGroup
	)
	wg.Add(3)
	record := func(text string) {
		mu.Lock()
		order = append(order, text)
		mu.Unlock()
	}
	b.route = func(_ context.Context, u *models.Update) {
		defer wg.Done()
		switch u.Message.Text {
		case "slow":
			select {
			case <-secondRan:
			case <-time.After(5 * time.Second):
				t.Error("account 2 never ran while account 1 was busy")
			}
			record("slow")
		case "fast":
			// Record before releasing account 1, so "slow" can only be
			// recorded after "fast" whatever the scheduler does.
			record("fast")
			close(secondRan)
		default:
			record(u.Message.Text)
		}
	}
	runBot(t, b)
	waitGroupOrFail(t, &wg)

	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(order, ","); got != "fast,slow,after" {
		t.Fatalf("order %q, want \"fast,slow,after\"", got)
	}
}

// TestBot_PanicInAHandlerReleasesTheAccount: the person's next update is still
// handled, and no lane is left behind.
func TestBot_PanicInAHandlerReleasesTheAccount(t *testing.T) {
	stub := newStubBotAPI(t, textUpdate(1, 8, "boom"), textUpdate(2, 8, "next"))
	b := newStubbedBot(t, stub)

	next := make(chan struct{})
	b.route = func(_ context.Context, u *models.Update) {
		if u.Message.Text == "boom" {
			panic("handler failure")
		}
		close(next)
	}
	runBot(t, b)
	select {
	case <-next:
	case <-time.After(5 * time.Second):
		t.Fatal("the update after a panic was never handled")
	}
	waitIdle(t, b.serial)
}

// TestSessionsDialog_StepIsStoredBeforeTheReply: by the time Telegram receives
// the reply that asks for the time, the dialog in the store is already on the
// time step, although the handler only writes it back at the end.
func TestSessionsDialog_StepIsStoredBeforeTheReply(t *testing.T) {
	stub := newStubBotAPI(t)
	b := newStubbedBot(t, stub)
	ctx := context.Background()
	const id = int64(77)
	b.sessions.put(ctx, id, &sessionDialog{Step: sesStepDate})

	var atReply atomic.Value
	stub.onSend = func() {
		d, ok, _ := b.sessions.get(context.Background(), id)
		if ok {
			atReply.Store(d.Step)
		}
	}

	dlg, ok, _ := b.sessions.get(ctx, id)
	if !ok {
		t.Fatal("dialog not stored")
	}
	func() {
		defer b.sessions.save(ctx, id, dlg)
		hctx := b.sessions.saveBeforeReply(ctx, id, dlg)
		dlg.Step = sesStepTime
		b.reply(hctx, id, nil, "what time?", nil)
	}()
	if got, _ := atReply.Load().(string); got != sesStepTime {
		t.Fatalf("step stored when the reply went out = %q, want %q", got, sesStepTime)
	}
}

func waitGroupOrFail(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("updates were not all handled")
	}
}
