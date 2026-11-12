package adminnotify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	emailadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesnotify"
)

func TestTelegramText_Escapes(t *testing.T) {
	text := TelegramText(Message{
		Title: "Новая <заявка>", Details: []string{"Организация: A <b> & Co"},
		Link: "https://admin.example.test/x", WhatToDo: "Что делать: проверьте.",
	})
	if strings.Contains(text, "A <b> &") || !strings.Contains(text, "A &lt;b&gt; &amp; Co") {
		t.Errorf("details not escaped: %s", text)
	}
	if !strings.Contains(text, "<b>Новая &lt;заявка&gt;</b>") {
		t.Errorf("title not escaped: %s", text)
	}
	if !strings.Contains(text, "Что делать") || !strings.HasSuffix(text, "https://admin.example.test/x") {
		t.Errorf("text = %s", text)
	}
	if strings.Contains(TelegramText(Message{Title: "x"}), "Что делать") {
		t.Error("empty WhatToDo must not print a line")
	}
}

func TestTelegramHandler(t *testing.T) {
	var got map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/botTOKEN/sendMessage") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer stub.Close()
	payload, _ := json.Marshal(TelegramPayload{ChatID: 4242, Text: "hello"})
	if err := NewTelegramHandler(salesnotify.NewTelegramSender("TOKEN", stub.URL), nil)(context.Background(), payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got["chat_id"] != "4242" || got["text"] != "hello" {
		t.Fatalf("telegram got %v", got)
	}
	if err := NewTelegramHandler(nil, nil)(context.Background(), payload); err != nil {
		t.Fatalf("nil sender: %v", err)
	}
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`))
	}))
	defer refuse.Close()
	if err := NewTelegramHandler(salesnotify.NewTelegramSender("TOKEN", refuse.URL), nil)(context.Background(), payload); err != nil {
		t.Fatalf("blocked chat must not be retried: %v", err)
	}
	if err := NewTelegramHandler(nil, nil)(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("empty payload accepted")
	}
	if err := NewTelegramHandler(nil, nil)(context.Background(), []byte(`not json`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

type captureSender struct {
	msgs []emailadapter.Message
	err  error
}

func (c *captureSender) Send(_ context.Context, m emailadapter.Message) error {
	c.msgs = append(c.msgs, m)
	return c.err
}

func TestRenderEmail(t *testing.T) {
	subject, htmlBody, textBody := RenderEmail(EmailPayload{
		Email: "a@example.com", Title: "Заголовок <x>", Details: []string{"Строка & один", "Строка два"},
		Link: "https://admin.example.test/a?b=1&c=2", ButtonLabel: "Открыть заявку", WhatToDo: "Что делать: проверьте.",
	})
	if subject != "Заголовок <x>" {
		t.Errorf("subject = %q", subject)
	}
	for _, want := range []string{"Заголовок &lt;x&gt;", "Строка &amp; один<br>Строка два", "Что делать: проверьте.", ">Открыть заявку</a>", "b=1&amp;c=2", "Arena Platform"} {
		if !strings.Contains(htmlBody, want) {
			t.Errorf("html lacks %q: %s", want, htmlBody)
		}
	}
	for _, want := range []string{"Заголовок <x>", "Строка & один", "Открыть заявку: https://admin.example.test/a?b=1&c=2"} {
		if !strings.Contains(textBody, want) {
			t.Errorf("text lacks %q: %s", want, textBody)
		}
	}
	_, h2, _ := RenderEmail(EmailPayload{Title: "T", Link: "https://x.test"})
	if !strings.Contains(h2, ">Открыть</a>") {
		t.Errorf("default button missing: %s", h2)
	}
}

func TestEmailHandler(t *testing.T) {
	ctx := context.Background()
	payload, _ := json.Marshal(EmailPayload{Email: "a@example.com", Title: "T", Details: []string{"d"}})
	s := &captureSender{}
	if err := NewEmailHandler(s, nil)(ctx, payload); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(s.msgs) != 1 || s.msgs[0].To != "a@example.com" || s.msgs[0].Subject != "T" || s.msgs[0].HTMLBody == "" || s.msgs[0].TextBody == "" {
		t.Fatalf("sent = %+v", s.msgs)
	}
	if err := NewEmailHandler(nil, nil)(ctx, payload); err != nil {
		t.Fatalf("nil sender: %v", err)
	}
	for _, bad := range []string{`{}`, `{"email":"a@example.com"}`, `{"title":"T"}`, `nope`} {
		if err := NewEmailHandler(s, nil)(ctx, []byte(bad)); err == nil {
			t.Errorf("payload %s accepted", bad)
		}
	}
	if err := NewEmailHandler(&captureSender{err: errors.New("smtp down")}, nil)(ctx, payload); err == nil {
		t.Error("a send error must be returned for retry")
	}
}
