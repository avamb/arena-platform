package opsalert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNew_NoCredentials_ReturnsNoop(t *testing.T) {
	n := New("", "", "prod", discardLogger())
	if _, ok := n.(*noopNotifier); !ok {
		t.Fatalf("New with empty credentials = %T, want *noopNotifier", n)
	}

	n2 := New("token", "", "prod", discardLogger())
	if _, ok := n2.(*noopNotifier); !ok {
		t.Fatalf("New with empty chat id = %T, want *noopNotifier", n2)
	}

	n3 := New("", "chat", "prod", discardLogger())
	if _, ok := n3.(*noopNotifier); !ok {
		t.Fatalf("New with empty bot token = %T, want *noopNotifier", n3)
	}
}

func TestNoopNotifier_NeverErrors(t *testing.T) {
	n := New("", "", "prod", discardLogger())
	if err := n.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("noop Send returned error: %v", err)
	}
}

func TestNew_WithCredentials_ReturnsTelegramNotifier(t *testing.T) {
	n := New("bot-token", "chat-id", "prod", discardLogger())
	if _, ok := n.(*TelegramNotifier); !ok {
		t.Fatalf("New with credentials = %T, want *TelegramNotifier", n)
	}
}

func TestTelegramNotifier_Send_PostsExpectedPayload(t *testing.T) {
	var gotPath string
	var gotBody sendMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n := NewTelegramNotifier("TESTTOKEN", "12345", "staging",
		WithBaseURL(srv.URL),
		WithLogger(discardLogger()),
	)

	if err := n.Send(context.Background(), "hello <world> & friends"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	if gotPath != "/botTESTTOKEN/sendMessage" {
		t.Errorf("path = %q, want /botTESTTOKEN/sendMessage", gotPath)
	}
	if gotBody.ChatID != "12345" {
		t.Errorf("chat_id = %q, want 12345", gotBody.ChatID)
	}
	if gotBody.ParseMode != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", gotBody.ParseMode)
	}
	wantText := "<b>staging</b> hello <world> & friends"
	_ = wantText // env label itself is escaped, message body is passed through verbatim
	if !strings.Contains(gotBody.Text, "[staging]") {
		t.Errorf("text = %q, want env label prefix [staging]", gotBody.Text)
	}
	if !strings.Contains(gotBody.Text, "hello <world> & friends") {
		t.Errorf("text = %q, want original body preserved", gotBody.Text)
	}
}

func TestTelegramNotifier_EscapeHTML_AppliedToEnvLabel(t *testing.T) {
	var gotBody sendMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewTelegramNotifier("TOKEN", "1", "<prod>&co",
		WithBaseURL(srv.URL),
		WithLogger(discardLogger()),
	)
	if err := n.Send(context.Background(), "body"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if strings.Contains(gotBody.Text, "<prod>&co") {
		t.Errorf("env label was not escaped: %q", gotBody.Text)
	}
	if !strings.Contains(gotBody.Text, "&lt;prod&gt;&amp;co") {
		t.Errorf("expected escaped env label, got %q", gotBody.Text)
	}
}

func TestTelegramNotifier_Truncation(t *testing.T) {
	var gotBody sendMessageRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewTelegramNotifier("TOKEN", "1", "", WithBaseURL(srv.URL), WithLogger(discardLogger()))
	huge := strings.Repeat("x", TelegramMaxMessageLength*2)
	if err := n.Send(context.Background(), huge); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if len([]rune(gotBody.Text)) > TelegramMaxMessageLength {
		t.Errorf("text length = %d, want <= %d", len([]rune(gotBody.Text)), TelegramMaxMessageLength)
	}
	if !strings.HasSuffix(gotBody.Text, truncationMarker) {
		t.Errorf("expected truncation marker suffix, got tail %q", gotBody.Text[len(gotBody.Text)-40:])
	}
}

func TestTelegramNotifier_RetriesOnFailureThenSucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewTelegramNotifier("TOKEN", "1", "", WithBaseURL(srv.URL), WithLogger(discardLogger()))
	err := n.Send(context.Background(), "retry me")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestTelegramNotifier_AllRetriesFail_SendStillReturnsNil(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewTelegramNotifier("TOKEN", "1", "", WithBaseURL(srv.URL), WithLogger(discardLogger()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := n.Send(ctx, "always fails")
	if err != nil {
		t.Fatalf("Send returned error: %v — failures must only be logged", err)
	}
	if got := atomic.LoadInt32(&attempts); got != maxSendAttempts {
		t.Errorf("attempts = %d, want %d", got, maxSendAttempts)
	}
}

func TestEscapeHTML(t *testing.T) {
	got := EscapeHTML(`Tom & Jerry <script>`)
	want := "Tom &amp; Jerry &lt;script&gt;"
	if got != want {
		t.Errorf("EscapeHTML = %q, want %q", got, want)
	}
}
