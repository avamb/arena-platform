package macs

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
)

// A MACS subscriber stored before the policy existed may point anywhere. The
// client must refuse to dial it, and the error must not repeat the URL's path
// or query (it becomes outbox_events.last_error and an ops alert).
func TestPost_StoredPrivateCallbackIsNeverDialed(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	d := &Dispatcher{client: egress.Policy{Private: egress.PrivateDeny}.Client(5 * time.Second), logger: slog.Default()}
	err := d.post(context.Background(), srv.URL+"/x?k=s3cr3t#/api/_wh/tickets", "secret", macsEnvelope{ID: 1, Type: "order.paid"})
	if err == nil {
		t.Fatal("expected the delivery to be refused")
	}
	if !errors.Is(err, egress.ErrDestinationNotAllowed) {
		t.Fatalf("err = %v, want ErrDestinationNotAllowed", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("receiver got %d requests, want 0", hits.Load())
	}
	if msg := err.Error(); strings.Contains(msg, "s3cr3t") || strings.Contains(msg, "/x") {
		t.Fatalf("error repeats the callback path or query: %s", msg)
	}
}

// The receiver's answer is never copied into the error.
func TestCheckAck_DoesNotEchoTheReceiversBody(t *testing.T) {
	body := []byte("<html>internal admin page: token=abc123</html>")
	err := checkAck("https://site.example", body)
	if err == nil {
		t.Fatal("a non-JSON body must fail")
	}
	if strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), "admin page") {
		t.Fatalf("error echoes the body: %v", err)
	}
	err = checkAck("https://site.example", []byte(`{"status":"Error","detail":"token=abc123"}`))
	if err == nil || strings.Contains(err.Error(), "abc123") {
		t.Fatalf("status error must name the status only: %v", err)
	}
	if !strings.Contains(err.Error(), `"Error"`) {
		t.Fatalf("status error should name the status: %v", err)
	}
}
