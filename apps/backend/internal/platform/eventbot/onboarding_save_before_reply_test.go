package eventbot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// The applicant must never see a question whose step is not stored yet: a
// crash or a deploy between the reply and a deferred write-back would leave
// the next answer on the old step. Every message the bot sends while it walks
// the applicant to the first question finds that step already in the store.
func TestOnbApplyDirect_StoresTheStepBeforeItReplies(t *testing.T) {
	clock := &testClock{t: time.Now()}
	store := newMemDialogStore(clock)
	onb := newOnbDialogs(store)

	var mu sync.Mutex
	var stepsSeen []onbStep
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			mu.Lock()
			stepsSeen = append(stepsSeen, onb.get(context.Background(), 42).step)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":42,"type":"private"}}}`))
	}))
	t.Cleanup(tgSrv.Close)
	arenaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"onboarding.not_found","message":"no application"}}`))
	}))
	t.Cleanup(arenaSrv.Close)

	tg, err := tgbot.New("123:test", tgbot.WithServerURL(tgSrv.URL), tgbot.WithSkipGetMe(),
		tgbot.WithHTTPClient(15*time.Second, saveBeforeReplyClient{&http.Client{Timeout: 15 * time.Second}}))
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		dialogs: store,
		onb:     onb,
		tg:      tg,
		texts:   NewTexts(nil),
		arena:   NewArenaClient(arenaSrv.URL, "svc", nil),
	}
	b.onbApplyDirect(context.Background(), 42, &models.User{ID: 42, LanguageCode: "en"})

	mu.Lock()
	defer mu.Unlock()
	if len(stepsSeen) != 2 {
		t.Fatalf("want the intro and the first question, got %d messages", len(stepsSeen))
	}
	for i, s := range stepsSeen {
		if s != onbFirst {
			t.Fatalf("message %d went out while the stored step was %v, want the first question's step", i+1, s)
		}
	}
}
