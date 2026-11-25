package stripe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

type pmdStub struct {
	mu    sync.Mutex
	calls []string
}

func (s *pmdStub) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r.Method+" "+r.URL.Path)
}

func newPMDAdapter(t *testing.T, h http.HandlerFunc) (*Adapter, *pmdStub) {
	t.Helper()
	st := &pmdStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.record(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Config{SecretKey: "sk_test_stub", BaseURL: srv.URL + "/v1"}), st
}

const pmdActive = `{"id":"pmd_1","domain_name":"tickets.example.test","enabled":true,` +
	`"apple_pay":{"status":"active"},"google_pay":{"status":"active"},"link":{"status":"active"}}`

func TestEnsurePaymentMethodDomain_FoundActive(t *testing.T) {
	a, st := newPMDAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("domain_name"); got != "tickets.example.test" {
			t.Errorf("domain_name = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[` + pmdActive + `]}`))
	})
	d, err := a.EnsurePaymentMethodDomain(context.Background(), "tickets.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if d.ProviderID != "pmd_1" || !d.Enabled || d.ApplePay != "active" || d.GooglePay != "active" {
		t.Fatalf("got %+v", d)
	}
	if len(st.calls) != 1 || st.calls[0] != "GET /v1/payment_method_domains" {
		t.Fatalf("calls = %v; want one read-only list", st.calls)
	}
}

func TestEnsurePaymentMethodDomain_InactiveIsValidated(t *testing.T) {
	a, st := newPMDAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"pmd_1","domain_name":"tickets.example.test","enabled":true,` +
				`"apple_pay":{"status":"inactive","status_details":{"error_message":"no file"}},"google_pay":{"status":"active"},"link":{"status":"active"}}]}`))
			return
		}
		_, _ = w.Write([]byte(pmdActive))
	})
	d, err := a.EnsurePaymentMethodDomain(context.Background(), "tickets.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if d.ApplePay != "active" {
		t.Fatalf("validated object not used: %+v", d)
	}
	if len(st.calls) != 2 || st.calls[1] != "POST /v1/payment_method_domains/pmd_1/validate" {
		t.Fatalf("calls = %v", st.calls)
	}
}

func TestEnsurePaymentMethodDomain_ValidateFailureKeepsListObject(t *testing.T) {
	a, _ := newPMDAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"pmd_1","domain_name":"tickets.example.test","enabled":true,` +
				`"apple_pay":{"status":"inactive","status_details":{"error_message":"no file"}}}]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"boom"}}`))
	})
	d, err := a.EnsurePaymentMethodDomain(context.Background(), "tickets.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if d.ApplePay != "inactive" || d.Detail != "no file" {
		t.Fatalf("got %+v", d)
	}
}

func TestEnsurePaymentMethodDomain_NotFoundCreates(t *testing.T) {
	a, st := newPMDAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("domain_name") != "tickets.example.test" {
			t.Errorf("create form = %v err=%v", r.PostForm, err)
		}
		_, _ = w.Write([]byte(pmdActive))
	})
	d, err := a.EnsurePaymentMethodDomain(context.Background(), "tickets.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if d.ProviderID != "pmd_1" {
		t.Fatalf("got %+v", d)
	}
	if len(st.calls) != 2 || st.calls[1] != "POST /v1/payment_method_domains" {
		t.Fatalf("calls = %v", st.calls)
	}
}

func TestEnsurePaymentMethodDomain_CredentialRefused(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		a, _ := newPMDAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Invalid API Key provided"}}`))
		})
		_, err := a.EnsurePaymentMethodDomain(context.Background(), "tickets.example.test")
		if !errors.Is(err, payments.ErrCredentialRefused) {
			t.Fatalf("status %d: err = %v; want ErrCredentialRefused", code, err)
		}
		if strings.Contains(err.Error(), "sk_test_stub") {
			t.Fatalf("error carries the key: %v", err)
		}
	}
}
