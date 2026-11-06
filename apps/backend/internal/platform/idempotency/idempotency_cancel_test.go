package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ctxCheckingStore refuses a Save on a cancelled context, like a pgx pool does.
type ctxCheckingStore struct{ *inMemoryStore }

func (s ctxCheckingStore) Save(ctx context.Context, key, scope, actorID string, resp StoredResponse) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.inMemoryStore.Save(ctx, key, scope, actorID, resp)
}

// A client that drops the connection after the handler answered is the
// retry case idempotency exists for: the response must still be stored.
func TestMiddleware_SavesWhenClientContextCancelled(t *testing.T) {
	store := ctxCheckingStore{newInMemoryStore()}
	ctx, cancel := context.WithCancel(context.Background())
	h := Middleware(store, Options{Scope: testScope})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
		cancel() // the client goes away before the middleware saves
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set(HeaderName, "cancel-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Idempotency-Save-Error"); got != "" {
		t.Fatalf("save failed: %s", got)
	}
	e, ok := store.get("cancel-key", testScope)
	if !ok {
		t.Fatal("no stored row after the client context was cancelled")
	}
	if e.resp.Status != http.StatusCreated || string(e.resp.Body) != `{"ok":true}` {
		t.Fatalf("stored %d %q", e.resp.Status, e.resp.Body)
	}
}

// A 409 (checkout.price_changed and the like) is never the key's final
// answer: the same key runs the handler again.
func TestMiddleware_ConflictIsNotStored(t *testing.T) {
	store := newInMemoryStore()
	calls := 0
	h := Middleware(store, Options{Scope: testScope})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"checkout.price_changed"}}`))
	}))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{}`))
		req.Header.Set(HeaderName, "conflict-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || rec.Header().Get("Idempotent-Replay") != "" {
			t.Fatalf("call %d: %d replay=%q", i, rec.Code, rec.Header().Get("Idempotent-Replay"))
		}
	}
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2", calls)
	}
	if _, ok := store.get("conflict-key", testScope); ok {
		t.Fatal("a 409 was stored for the key")
	}
}
