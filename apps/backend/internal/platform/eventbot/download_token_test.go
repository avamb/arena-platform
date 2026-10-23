package eventbot

// download_token_test.go — the Telegram file link carries the bot token in its
// path, and net/http repeats the URL in the errors it returns. The error of a
// failed poster download is logged, so it must not carry the token.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const downloadTestToken = "123456:SECRET-token-value"

func newDownloadTestBot(t *testing.T, fileHandler http.HandlerFunc) *Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.Contains(r.URL.Path, "/file/") {
			fileHandler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"f1","file_path":"photos/p.jpg"}}`)
	}))
	t.Cleanup(srv.Close)
	b, err := New(Options{
		Token:             downloadTestToken,
		Queries:           gen.New(nil),
		Arena:             NewArenaClient("http://127.0.0.1:1", "", nil),
		Minter:            NewTokenMinter("secret", "iss", "aud"),
		TelegramServerURL: srv.URL,
		HTTPClient:        srv.Client(),
		MessageRateLimit:  -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.fileClient = &http.Client{Timeout: 5 * time.Second}
	return b
}

func TestDownloadTelegramFile_ConnectionErrorDoesNotCarryTheToken(t *testing.T) {
	b := newDownloadTestBot(t, func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler) // the server drops the connection
	})
	_, err := b.downloadTelegramFile(context.Background(), "f1")
	if err == nil {
		t.Fatal("expected an error from a dropped connection")
	}
	for _, leak := range []string{downloadTestToken, "SECRET-token-value", "/file/bot"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}

func TestDownloadTelegramFile_StatusErrorDoesNotCarryTheToken(t *testing.T) {
	b := newDownloadTestBot(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	_, err := b.downloadTelegramFile(context.Background(), "f1")
	if err == nil || strings.Contains(err.Error(), "SECRET-token-value") {
		t.Fatalf("want a status error without the token, got %v", err)
	}
}

func TestDownloadTelegramFile_ReturnsTheBytes(t *testing.T) {
	b := newDownloadTestBot(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "image-bytes")
	})
	data, err := b.downloadTelegramFile(context.Background(), "f1")
	if err != nil || string(data) != "image-bytes" {
		t.Fatalf("got %q, %v", data, err)
	}
}
