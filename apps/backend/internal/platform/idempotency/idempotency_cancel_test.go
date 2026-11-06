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
