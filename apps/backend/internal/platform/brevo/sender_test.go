package brevo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSenderReadsBrevoDNSRecords(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "test-key" {
			t.Fatal("missing Brevo API key")
		}
		if r.URL.Path != "/v3/senders/tickets@example.com" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"email":"tickets@example.com","active":true,"dkimRecord":{"hostName":"mail._domainkey","value":"dkim-value"},"spfRecord":{"hostName":"@","value":"v=spf1 include:spf.brevo.com ~all"}}`))
	}))
	defer s.Close()
	got, err := New("test-key", s.URL, s.Client()).GetSender(context.Background(), "tickets@example.com")
	if err != nil || !got.Active || len(got.DNSRecords) != 2 {
		t.Fatalf("sender=%+v err=%v", got, err)
	}
}
