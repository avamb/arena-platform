// Package stub provides a lightweight MACS stub receiver for testing.
//
// AB-50d (feature #440): round-trip integration test support.
//
// The StubReceiver accepts POST /_wh/tickets (the MACS webhook endpoint),
// parses incoming MACS envelopes, and stores them for assertion. It mirrors
// enough of the MACS importer's fabrication behaviour to make it visible when
// an incomplete export is passed — missing integer IDs etc. surface as
// obviously broken payloads.
//
// Usage in tests:
//
//	recv := stub.New()
//	defer recv.Close()
//	// Register recv.URL in webhook_subscribers.callback_url
//	// ... deliver events via MACSDispatcher ...
//	events := recv.Events()
//	// Assert events[0].Type == "order.paid", etc.
package stub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Envelope is the MACS webhook envelope shape received by the stub.
// It mirrors the macsEnvelope in dispatcher.go.
type Envelope struct {
	ID      int64          `json:"id"`
	Created string         `json:"created"`
	Type    string         `json:"type"`
	Data    map[string]any `json:"data"`
	// Raw is the unmodified JSON body for inspection.
	Raw []byte `json:"-"`
}

// Receiver is a test HTTP server that accepts MACS webhook calls and
// records them for assertion. It is safe for concurrent use.
type Receiver struct {
	srv *httptest.Server
	mu  sync.Mutex
	evs []Envelope
}

// New starts a new stub Receiver. Call Close() to release the server.
func New() *Receiver {
	r := &Receiver{}
	mux := http.NewServeMux()
	mux.HandleFunc("/_wh/tickets", r.handleWebhook)
	r.srv = httptest.NewServer(mux)
	return r
}

// URL returns the base URL of the stub server (e.g. "http://127.0.0.1:PORT").
func (r *Receiver) URL() string {
	return r.srv.URL
}

// WebhookURL returns the full webhook endpoint URL.
func (r *Receiver) WebhookURL() string {
	return r.srv.URL + "/_wh/tickets"
}

// Events returns a copy of all envelopes received so far, in order.
func (r *Receiver) Events() []Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Envelope, len(r.evs))
	copy(out, r.evs)
	return out
}

// EventsByType returns all received envelopes with the given MACS event type.
func (r *Receiver) EventsByType(macsType string) []Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Envelope
	for _, e := range r.evs {
		if e.Type == macsType {
			out = append(out, e)
		}
	}
	return out
}

// Reset clears all recorded envelopes.
func (r *Receiver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = nil
}

// Close shuts down the stub HTTP server.
func (r *Receiver) Close() {
	r.srv.Close()
}

// handleWebhook accepts every POST to /_wh/tickets, parses the JSON body,
// stores the envelope, and returns 200 OK. Non-POST requests return 405.
func (r *Receiver) handleWebhook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		// Store a best-effort envelope even when parse fails so tests can
		// detect malformed payloads (mirrors the importer's fabrication check).
		env = Envelope{Type: "parse_error", Raw: body}
	} else {
		env.Raw = body
	}

	r.mu.Lock()
	r.evs = append(r.evs, env)
	r.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}
