package payplus

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubPayPlus is an in-process PayPlus: it checks the two key headers like
// the real API ("MISSING API_KEY" for a wrong pair), keeps a ledger of
// charges and refunds, and records every request it saw.
type stubPayPlus struct {
	server *httptest.Server
	apiKey string
	secret string

	mu       sync.Mutex
	requests []stubRequest
	charges  map[string]stubCharge // transaction uid → charge
	byPage   map[string]string     // page_request_uid → charge uid
	refunds  []stubRefund
	// failNextRefund makes the next refund answer 500 AFTER booking it (a
	// lost answer).
	loseNextRefund bool
	// refuseRefund makes refunds answer a results.status=error refusal.
	refuseRefund string
	seq          int
}

type stubRequest struct {
	Path string
	Body map[string]any
}

type stubCharge struct {
	UID, PageRequestUID, Currency string
	Amount                        int64
}

type stubRefund struct {
	UID, ChargeUID, MoreInfo string
	Amount                   int64
}

func newStubPayPlus(t *testing.T, apiKey, secret string) *stubPayPlus {
	t.Helper()
	s := &stubPayPlus{apiKey: apiKey, secret: secret, charges: map[string]stubCharge{}, byPage: map[string]string{}}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stubPayPlus) baseURL() string { return s.server.URL + "/api/v1.0" }

func (s *stubPayPlus) addCharge(uid, page string, amountMinor int64, currency string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.charges[uid] = stubCharge{UID: uid, PageRequestUID: page, Amount: amountMinor, Currency: currency}
	if page != "" {
		s.byPage[page] = uid
	}
}

func (s *stubPayPlus) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if strings.HasSuffix(r.Path, path) {
			n++
		}
	}
	return n
}

func (s *stubPayPlus) last(path string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if strings.HasSuffix(s.requests[i].Path, path) {
			return s.requests[i].Body
		}
	}
	return nil
}

func (s *stubPayPlus) refundCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.refunds)
}

func (s *stubPayPlus) refundAmountFor(marker string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.refunds {
		if r.MoreInfo == marker {
			return r.Amount
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func okEnv(data any) map[string]any {
	return map[string]any{"results": map[string]any{"status": "success", "code": 0, "description": "operation has been success"}, "data": data}
}

func (s *stubPayPlus) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	if len(raw) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&body)
	}
	s.mu.Lock()
	s.requests = append(s.requests, stubRequest{Path: r.URL.Path, Body: body})
	s.mu.Unlock()

	if r.Header.Get("api-key") == "" || r.Header.Get("secret-key") == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "AUTHORIZATION HEADER IS NOT VALID"})
		return
	}
	if r.Header.Get("api-key") != s.apiKey || r.Header.Get("secret-key") != s.secret {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "MISSING API_KEY"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1.0/")
	switch path {
	case "PaymentPages/generateLink":
		s.mu.Lock()
		s.seq++
		page := fmt.Sprintf("00000000-0000-4000-8000-%012d", s.seq)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, okEnv(map[string]any{
			"page_request_uid":  page,
			"payment_page_link": "https://paymentsdev.payplus.test/" + page,
		}))
	case "PaymentPages/ipn":
		page, _ := body["payment_request_uid"].(string)
		s.mu.Lock()
		uid, found := s.byPage[page]
		c := s.charges[uid]
		s.mu.Unlock()
		if !found {
			writeJSON(w, http.StatusOK, map[string]any{"results": map[string]any{"status": "error", "code": 1, "description": "payment request not found"}})
			return
		}
		writeJSON(w, http.StatusOK, okEnv(map[string]any{"transaction": map[string]any{
			"uid": c.UID, "payment_request_uid": c.PageRequestUID, "status_code": "000",
			"amount": json.Number(MinorToMajor(c.Amount)), "currency": c.Currency,
		}}))
	case "Transactions/View":
		mi, _ := body["more_info"].(string)
		s.mu.Lock()
		var out []any
		for _, rf := range s.refunds {
			if mi != "" && rf.MoreInfo == mi {
				out = append(out, map[string]any{"transaction": map[string]any{
					"transaction_uid": rf.UID, "transaction_type": "Refund", "status_code": "000",
					"amount": json.Number(MinorToMajor(rf.Amount)), "currency": "ILS", "more_info": rf.MoreInfo,
					"transaction_is_cancelled": false,
				}})
			}
		}
		s.mu.Unlock()
		if out == nil {
			out = []any{}
		}
		writeJSON(w, http.StatusOK, okEnv(out))
	case "Transactions/RefundByTransactionUID":
		if s.refuseRefund != "" {
			writeJSON(w, http.StatusOK, map[string]any{"results": map[string]any{"status": "error", "code": 1, "description": s.refuseRefund}})
			return
		}
		uid, _ := body["transaction_uid"].(string)
		amt, _ := MajorToMinor(fmt.Sprint(body["amount"]))
		mi, _ := body["more_info"].(string)
		s.mu.Lock()
		c, found := s.charges[uid]
		var refunded int64
		for _, rf := range s.refunds {
			if rf.ChargeUID == uid {
				refunded += rf.Amount
			}
		}
		if !found || amt <= 0 || refunded+amt > c.Amount {
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"results": map[string]any{"status": "error", "code": 1, "description": "can not refund this transaction"}})
			return
		}
		s.seq++
		rf := stubRefund{UID: fmt.Sprintf("refund-%04d", s.seq), ChargeUID: uid, MoreInfo: mi, Amount: amt}
		s.refunds = append(s.refunds, rf)
		lose := s.loseNextRefund
		s.loseNextRefund = false
		s.mu.Unlock()
		if lose {
			http.Error(w, "upstream timeout", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, okEnv(map[string]any{"transaction": map[string]any{
			"uid": rf.UID, "type": "internal_page", "status_code": "000",
			"amount": json.Number(MinorToMajor(amt)), "currency": c.Currency, "more_info": mi,
		}}))
	default:
		http.NotFound(w, r)
	}
}
