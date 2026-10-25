package posterread

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A reply without the poster_facts call is an error, not an empty result.
func TestAnthropic_Read_NoToolCallIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"I cannot read it."}]}`))
	}))
	defer srv.Close()
	_, err := NewAnthropic("k", "", srv.URL, srv.Client()).Read(context.Background(), []byte("x"), "image/png")
	if err == nil || !strings.Contains(err.Error(), "no poster_facts tool call") {
		t.Fatalf("err = %v", err)
	}
}

// The usage line holds names and numbers only.
func TestAnthropic_Read_LogsUsageWithoutText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1500,"output_tokens":90},"content":[{"type":"tool_use","name":"poster_facts","input":{"name":"SECRET TITLE"}}]}`))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := NewAnthropic("k", "", srv.URL, srv.Client()).WithLogger(l).Read(context.Background(), []byte("x"), "image/png"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "input_tokens=1500") || !strings.Contains(out, "output_tokens=90") || !strings.Contains(out, "provider=anthropic") {
		t.Errorf("log = %s", out)
	}
	if strings.Contains(out, "SECRET TITLE") {
		t.Errorf("the log must not hold poster text: %s", out)
	}
}
