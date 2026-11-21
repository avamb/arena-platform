package brevo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func stub(t *testing.T, domainsStatus int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.Header.Get("api-key") != "test-key" {
			t.Error("missing Brevo API key")
		}
		switch r.URL.Path {
		case "/v3/senders":
			_, _ = w.Write([]byte(`{"senders":[{"id":1,"name":"A","email":"other@example.org","active":true},{"id":2,"name":"T","email":"Tickets@Example.com","active":true,"ips":[]}]}`))
		case "/v3/senders/domains/example.com":
			if domainsStatus != http.StatusOK {
				w.WriteHeader(domainsStatus)
				return
			}
			_, _ = w.Write([]byte(`{"domain":"example.com","verified":true,"authenticated":true,"dns_records":{"dkim_record":null,"dkim1Record":{"type":"CNAME","value":"b1.dkim.brevo.com","host_name":"brevo1._domainkey","status":true},"dkim2Record":{"type":"CNAME","value":"b2.dkim.brevo.com","host_name":"brevo2._domainkey"},"brevo_code":{"type":"TXT","value":"brevo-code:abc","host_name":"@"},"dmarc_record":{"type":"TXT","value":"v=DMARC1","host_name":"_dmarc"}}}`))
		case "/v3/senders/domains":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(s.Close)
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func TestGetSenderListsSendersAndReadsDomainRecords(t *testing.T) {
	s, paths := stub(t, http.StatusOK)
	got, err := New("test-key", s.URL, s.Client()).GetSender(context.Background(), "tickets@example.com")
	if err != nil || !got.Active || !got.DomainVerified || len(got.DNSRecords) != 4 {
		t.Fatalf("sender=%+v err=%v", got, err)
	}
	if got.DNSRecords[0].Type != "DKIM" || got.DNSRecords[0].Host != "brevo1._domainkey" {
		t.Fatalf("records=%+v", got.DNSRecords)
	}
	for _, p := range paths() {
		if p != "/v3/senders" && p != "/v3/senders/domains/example.com" {
			t.Fatalf("unexpected path requested: %q", p)
		}
	}
}

func TestGetSenderNotFound(t *testing.T) {
	s, paths := stub(t, http.StatusOK)
	_, err := New("test-key", s.URL, s.Client()).GetSender(context.Background(), "nobody@example.com")
	if !errors.Is(err, ErrSenderNotFound) {
		t.Fatalf("err=%v", err)
	}
	for _, p := range paths() {
		if p == "/v3/senders/nobody@example.com" {
			t.Fatal("old per-address route was requested")
		}
	}
}

func TestGetSenderToleratesDomainsFailure(t *testing.T) {
	s, _ := stub(t, http.StatusInternalServerError)
	got, err := New("test-key", s.URL, s.Client()).GetSender(context.Background(), "tickets@example.com")
	if err != nil || !got.Active || len(got.DNSRecords) != 0 {
		t.Fatalf("sender=%+v err=%v", got, err)
	}
}
