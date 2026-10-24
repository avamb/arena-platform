package bil24wire

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
)

// A subscriber row stored before the policy existed may point anywhere. The
// dispatcher's client must refuse to dial it, and the error that lands in
// outbox_events.last_error must not repeat the callback's path or query.
func TestDeliver_StoredPrivateCallbackIsNeverDialed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	d := &Dispatcher{client: egress.Policy{Private: egress.PrivateDeny}.Client(5 * time.Second)}
	sub := Subscriber{CallbackURL: srv.URL + "/wp-json/hook?key=s3cr3t", SigningSecret: "x"}
	err := d.deliver(context.Background(), sub, Envelope{ID: 1, Type: SiteEventTest})
	if err == nil {
		t.Fatal("expected the delivery to be refused")
	}
	if !errors.Is(err, egress.ErrDestinationNotAllowed) {
		t.Fatalf("err = %v, want ErrDestinationNotAllowed", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("receiver got %d requests, want 0", hits.Load())
	}
	if msg := err.Error(); strings.Contains(msg, "s3cr3t") || strings.Contains(msg, "/wp-json") {
		t.Fatalf("error repeats the callback path or query: %s", msg)
	}
}

// A redirect is a failed delivery: the signed body is never re-sent elsewhere.
func TestDeliver_RedirectIsAFailedDelivery(t *testing.T) {
	var target atomic.Int32
	dst := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { target.Add(1) }))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL, http.StatusTemporaryRedirect)
	}))
	defer src.Close()

	d := &Dispatcher{client: egress.Policy{Private: egress.PrivateAllow}.Client(5 * time.Second)}
	err := d.deliver(context.Background(), Subscriber{CallbackURL: src.URL}, Envelope{ID: 1, Type: SiteEventTest})
	if err == nil {
		t.Fatal("a 307 must not count as delivered")
	}
	if target.Load() != 0 {
		t.Fatal("the redirect target must not be called")
	}
}
