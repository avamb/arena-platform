package onboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

func strp(s string) *string { return &s }

func TestAdminMessageAndTelegramText(t *testing.T) {
	app := &Application{ID: uuid.New(), OrgName: strp("A <b> & Co"), Country: strp("ES"), Email: "a@example.com", Source: "site"}
	title, details := adminMessage(app, true, true)
	if !strings.Contains(title, "повторно") {
		t.Fatalf("title = %q", title)
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{"Организация: A <b> & Co", "Страна: ES", "Заявитель: —", "E-mail: a@example.com", "Телефон: —", "Источник: site", "Система одобрила бы: да"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details lack %q: %s", want, joined)
		}
	}
	text := adminTelegramText(title, details, "https://admin.example.test/onboarding/x")
	if strings.Contains(text, "A <b> &") || !strings.Contains(text, "A &lt;b&gt; &amp; Co") {
		t.Errorf("telegram text is not escaped: %s", text)
	}
	if !strings.Contains(text, "Что делать") || !strings.HasSuffix(text, "https://admin.example.test/onboarding/x") {
		t.Errorf("telegram text = %s", text)
	}
}

func TestAdminTelegramHandler(t *testing.T) {
	var got map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/botTOKEN/sendMessage") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer stub.Close()
	h := NewAdminTelegramHandler(salesnotify.NewTelegramSender("TOKEN", stub.URL), nil)
	payload, _ := json.Marshal(AdminTelegramPayload{ChatID: 4242, Text: "hello"})
	if err := h(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got["chat_id"] != "4242" || got["text"] != "hello" {
		t.Fatalf("telegram got %v", got)
	}

	// No sender: dropped without an error.
	if err := NewAdminTelegramHandler(nil, nil)(context.Background(), payload); err != nil {
		t.Fatalf("nil sender: %v", err)
	}
	// A refused chat is not retried.
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer refuse.Close()
	if err := NewAdminTelegramHandler(salesnotify.NewTelegramSender("TOKEN", refuse.URL), nil)(context.Background(), payload); err != nil {
		t.Fatalf("blocked chat must not be retried: %v", err)
	}
	if err := NewAdminTelegramHandler(nil, nil)(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("empty payload accepted")
	}
}
