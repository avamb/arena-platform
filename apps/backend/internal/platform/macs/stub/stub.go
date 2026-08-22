// Package stub provides a lightweight MACS stub receiver for testing.
//
// AB-50d (feature #440): round-trip integration test support.
// AB-50e (feature #441): enhanced stub with ticket store, import endpoint,
// HMAC verification, and required-field validation.
//
// The StubReceiver accepts POST /_wh/tickets (the MACS webhook endpoint),
// parses incoming MACS envelopes, stores them for assertion, and optionally
// verifies HMAC-SHA256 signatures.  The /import/tickets endpoint accepts the
// full MACS Export JSON (array of orders), validates required fields, and
// stores per-ticket state so tests can assert holderStatus transitions.
//
// Usage in tests:
//
//	recv := stub.New()
//	defer recv.Close()
//	// Register recv.WebhookURL() in webhook_subscribers.callback_url
//	// ... deliver events via MACSDispatcher ...
//	events := recv.Events()
//	// Assert events[0].Type == "order.paid", etc.
//	tk := recv.TicketByID(sysID)
//	// Assert tk.HolderStatus == 3 (refunded)
//
// For HMAC-verified receivers:
//
//	recv := stub.NewWithSecret("my-signing-secret")
//	// Only requests signed with X-MACS-Signature: sha256=<hmac> are accepted
package stub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
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

// Ticket holds the per-ticket state tracked by the stub.
// It is populated from POST /import/tickets and updated by webhook deliveries.
type Ticket struct {
	// HolderStatus mirrors the MACS holderStatus field:
	// 0 = valid/not-used, 1 = checked-in, 2 = checked-out, 3 = refunded.
	HolderStatus int
	// Barcode is the ticket barcode value from the import payload.
	Barcode string
	// OrderID is the parent order id from the import payload.
	OrderID int64
	// ImportedAt is the time the ticket was stored via /import/tickets.
	ImportedAt time.Time
}

// importTicket is the minimal shape of a MACS ticket from the import JSON
// (enough to validate required fields and extract state).
type importTicket struct {
	ID           int64             `json:"id"`
	SeatID       int64             `json:"seatId"`
	OrderID      int64             `json:"orderId"`
	Barcode      string            `json:"barcode"`
	HolderStatus int               `json:"holderStatus"`
	ActionEvent  importActionEvent `json:"actionEvent"`
}

// importActionEvent holds the required fields of actionEvent.
type importActionEvent struct {
	ID               int64  `json:"id"`
	CityName         string `json:"cityName"`
	VenueName        string `json:"venueName"`
	ActionName       string `json:"actionName"`
	ActionLegalOwner string `json:"actionLegalOwner"`
	ShowTime         string `json:"showTime"`
}

// importOrder is one order from the MACS Export JSON.
type importOrder struct {
	ID         int64          `json:"id"`
	TicketList []importTicket `json:"ticketList"`
}

// Receiver is a test HTTP server that accepts MACS webhook calls and
// records them for assertion. It is safe for concurrent use.
type Receiver struct {
	srv           *httptest.Server
	mu            sync.Mutex
	evs           []Envelope
	tickets       map[int64]*Ticket // keyed by MACS ticket id (system_ticket_id)
	signingSecret string            // empty = no verification; non-empty = verify X-MACS-Signature
	// deliveryCount tracks delivery attempts per URL path for retry assertions.
	deliveryCount map[string]int
	// onceFailPaths is a set of paths that should return 503 exactly once.
	onceFailPaths map[string]bool
}

// New starts a new stub Receiver. Call Close() to release the server.
func New() *Receiver {
	return newReceiver("")
}

// NewWithSecret starts a stub Receiver that verifies X-MACS-Signature HMAC-SHA256
// on every POST /_wh/tickets call. Requests with a missing or invalid signature
// are rejected with 401. Call Close() to release the server.
func NewWithSecret(signingSecret string) *Receiver {
	return newReceiver(signingSecret)
}

func newReceiver(secret string) *Receiver {
	r := &Receiver{
		tickets:       make(map[int64]*Ticket),
		signingSecret: secret,
		deliveryCount: make(map[string]int),
		onceFailPaths: make(map[string]bool),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_wh/tickets", r.handleWebhook)
	mux.HandleFunc("/import/tickets", r.handleImport)
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

// ImportURL returns the URL of the /import/tickets endpoint.
func (r *Receiver) ImportURL() string {
	return r.srv.URL + "/import/tickets"
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

// TicketByID returns the stub's view of the given MACS ticket id, or nil if
// the ticket was never imported via /import/tickets.
func (r *Receiver) TicketByID(id int64) *Ticket {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tickets[id]
	if !ok {
		return nil
	}
	// Return a copy so callers can't mutate internal state.
	cp := *t
	return &cp
}

// DeliveryCount returns the number of POST requests received on the given path.
func (r *Receiver) DeliveryCount(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deliveryCount[path]
}

// SetOnceFailPath configures path to return HTTP 503 on the very next request
// (once only). Subsequent requests to the same path succeed normally.
func (r *Receiver) SetOnceFailPath(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onceFailPaths[path] = true
}

// Reset clears all recorded envelopes and resets delivery counters.
// The imported ticket store and onceFailPaths are NOT cleared — tickets survive
// a Reset so holderStatus transitions are visible across event boundaries.
func (r *Receiver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = nil
	r.deliveryCount = make(map[string]int)
}

// Close shuts down the stub HTTP server.
func (r *Receiver) Close() {
	r.srv.Close()
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /_wh/tickets — MACS webhook receiver
// ─────────────────────────────────────────────────────────────────────────────

// handleWebhook accepts every POST to /_wh/tickets, parses the JSON body,
// stores the envelope, and returns 200 OK.
//
// When a signing secret is configured, the X-MACS-Signature header must
// match sha256=HMAC(secret, body); mismatched signatures return 401.
//
// When the path is in onceFailPaths, the first request returns 503 (simulating
// a transient delivery failure for retry testing).
//
// After a successful delivery, if the envelope is "ticket.refunded" or
// "order.paid", the ticket store is updated to reflect the new holderStatus.
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

	r.mu.Lock()
	path := req.URL.Path
	r.deliveryCount[path]++

	// Once-fail guard: return 503 on the first call, then clear.
	if r.onceFailPaths[path] {
		delete(r.onceFailPaths, path)
		r.mu.Unlock()
		http.Error(w, "transient error (retry test)", http.StatusServiceUnavailable)
		return
	}
	r.mu.Unlock()

	// HMAC verification.
	if r.signingSecret != "" {
		sig := req.Header.Get("X-MACS-Signature")
		if !verifyHMAC(r.signingSecret, body, sig) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
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
	// Update ticket store holderStatus based on envelope data.
	if env.Type == "ticket.refunded" || env.Type == "order.paid" {
		if idF, ok := env.Data["id"].(float64); ok {
			ticketID := int64(idF)
			if hs, ok2 := env.Data["holderStatus"].(float64); ok2 {
				if tk, exists := r.tickets[ticketID]; exists {
					tk.HolderStatus = int(hs)
				} else {
					// Ticket not imported via /import/tickets but still track it.
					r.tickets[ticketID] = &Ticket{
						HolderStatus: int(hs),
						ImportedAt:   time.Now().UTC(),
					}
				}
			}
		}
	}
	r.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /import/tickets — MACS import receiver
// ─────────────────────────────────────────────────────────────────────────────

// handleImport accepts a MACS Export JSON array ([]Order), validates that
// every ticket carries the required MACS Pydantic fields, and stores the
// tickets in the internal ticket store.
//
// Required fields per ticket:
//
//	id, seatId, barcode, actionEvent.id, actionEvent.cityName,
//	actionEvent.venueName, actionEvent.actionName, actionEvent.actionLegalOwner,
//	actionEvent.showTime
//
// Returns 422 with a JSON body listing the first validation error when any
// required field is missing or zero. Returns 200 on success.
func (r *Receiver) handleImport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	var orders []importOrder
	if err := json.Unmarshal(body, &orders); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprintf(w, `{"error":"invalid JSON: %s"}`, jsonEscapeString(err.Error()))
		return
	}

	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	for oi, order := range orders {
		for ti, t := range order.TicketList {
			if verr := validateImportTicket(oi, ti, t); verr != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprintf(w, `{"error":%q}`, verr)
				return
			}
			r.tickets[t.ID] = &Ticket{
				HolderStatus: t.HolderStatus,
				Barcode:      t.Barcode,
				OrderID:      t.OrderID,
				ImportedAt:   now,
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	imported := 0
	for _, o := range orders {
		imported += len(o.TicketList)
	}
	fmt.Fprintf(w, `{"imported":%d}`, imported)
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// validateImportTicket returns a non-empty error string when the ticket is
// missing any MACS-required field.
//
// All of the following fields are strictly required per the MACS Pydantic
// model (AB-50g: restored to strict validation):
//
//	id, seatId (> 0), barcode,
//	actionEvent.id, actionEvent.cityName, actionEvent.venueName,
//	actionEvent.actionName, actionEvent.actionLegalOwner, actionEvent.showTime.
func validateImportTicket(orderIdx, ticketIdx int, t importTicket) string {
	prefix := fmt.Sprintf("order[%d].ticketList[%d]", orderIdx, ticketIdx)
	if t.ID == 0 {
		return prefix + ": id is required and must be non-zero"
	}
	if t.SeatID <= 0 {
		return prefix + ": seatId is required and must be greater than zero"
	}
	if t.Barcode == "" {
		return prefix + ": barcode is required"
	}
	ae := t.ActionEvent
	if ae.ID == 0 {
		return prefix + ": actionEvent.id is required and must be non-zero"
	}
	if ae.CityName == "" {
		return prefix + ": actionEvent.cityName is required"
	}
	if ae.VenueName == "" {
		return prefix + ": actionEvent.venueName is required"
	}
	if ae.ActionName == "" {
		return prefix + ": actionEvent.actionName is required"
	}
	if ae.ActionLegalOwner == "" {
		return prefix + ": actionEvent.actionLegalOwner is required"
	}
	if ae.ShowTime == "" {
		return prefix + ": actionEvent.showTime is required"
	}
	return ""
}

// verifyHMAC returns true when sig matches "sha256=" + HMAC-SHA256(secret, body).
func verifyHMAC(secret string, body []byte, sig string) bool {
	if !strings.HasPrefix(sig, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sig), []byte(expected))
}

// jsonEscapeString escapes a string for embedding in JSON error messages.
func jsonEscapeString(s string) string {
	b, _ := json.Marshal(s)
	// Marshal returns a quoted JSON string; strip the surrounding quotes.
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}
