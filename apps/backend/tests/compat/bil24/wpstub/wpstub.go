// Package wpstub replays the behaviour of the legacy Vino&Co
// bil24-notification-receiver.php endpoint so that arena→WP webhooks can be
// exercised end-to-end from Go tests without booting the real WordPress
// installation.
//
// The receiver contract (from 08_architecture/18_bil24_compat_wave1_specification_ru.md §7
// and §9) is intentionally small:
//
//   - HTTP POST application/json.
//   - Body MUST contain non-empty "type" (string) and "data" (object) fields —
//     otherwise the receiver returns 400 with body {"ok":false,"error":"..."}.
//   - Any other well-formed body returns 200 {"ok":true}.
//   - When "type" == "ticket.refunded", the receiver deduplicates by
//     data.id — a repeated payload for the same id still returns 200 {"ok":true}
//     but is NOT appended to the stored bil24_tickets state (matches the
//     "REPEAT" branch of the PHP script).
//   - All accepted payloads are appended to an in-memory bil24_tickets log
//     that tests can inspect via Received().
//
// The stub deliberately avoids any dependency on internal/platform/httpserver
// or on a database. It is safe to import from any test package.
package wpstub

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Event is one accepted receiver payload, preserved in the order it was
// stored to bil24_tickets. Repeated ticket.refunded events for the same
// data.id are recorded once.
type Event struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

// Server is an in-memory replay of bil24-notification-receiver.php.
type Server struct {
	mu           sync.Mutex
	received     []Event
	seenRefundID map[string]struct{}
	deliveries   int
	failOnce     bool
	http         *httptest.Server
}

// New starts a new httptest server and returns the stub. Call Close when
// finished.
func New() *Server {
	s := &Server{seenRefundID: map[string]struct{}{}}
	s.http = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the base URL of the underlying httptest.Server. POST payloads here.
func (s *Server) URL() string { return s.http.URL }

// Close shuts down the httptest server.
func (s *Server) Close() { s.http.Close() }

// Received returns a snapshot of the stored bil24_tickets log.
func (s *Server) Received() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.received))
	copy(out, s.received)
	return out
}

// Reset clears the stored bil24_tickets log, the dedup index and the delivery
// counter. An armed SetOnceFail survives a Reset (same precedent as
// internal/platform/macs/stub): the retry scenario arms the failure and then
// clears the counters it is about to assert on.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = nil
	s.seenRefundID = map[string]struct{}{}
	s.deliveries = 0
}

// Deliveries is the number of POSTs the stub has answered since the last
// Reset, counting the rejected ones. It is the only signal a test has that a
// dispatcher actually attempted a delivery, as opposed to skipping the row —
// Received() cannot distinguish "not delivered yet" from "delivered and
// deduplicated".
func (s *Server) Deliveries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliveries
}

// SetOnceFail arms the receiver to answer the NEXT delivery with 503 and
// disarm itself, so the attempt after it succeeds. This models the transient
// WordPress outage of spec §9.2: the outbox must retry rather than
// dead-letter, and the retried envelope must still be accepted.
func (s *Server) SetOnceFail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOnce = true
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]interface{}{"ok": false, "error": "method_not_allowed"})
		return
	}
	// Count the attempt BEFORE any rejection: a 503 is still a delivery the
	// dispatcher made, and the retry assertions key off exactly that.
	s.mu.Lock()
	s.deliveries++
	failing := s.failOnce
	s.failOnce = false
	s.mu.Unlock()
	if failing {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "transient_unavailable"})
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "read_body"})
		return
	}
	var env struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "malformed_json"})
		return
	}
	if env.Type == "" || env.Data == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "type_and_data_required"})
		return
	}
	if err := s.store(env.Type, env.Data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// store is separated from serve to keep the dedup rule testable without HTTP.
func (s *Server) store(evtType string, data map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if evtType == "ticket.refunded" {
		key, err := refundKey(data)
		if err != nil {
			return err
		}
		if _, seen := s.seenRefundID[key]; seen {
			// Dedup: accept (200 ok) but do NOT append to bil24_tickets.
			return nil
		}
		s.seenRefundID[key] = struct{}{}
	}
	s.received = append(s.received, Event{Type: evtType, Data: cloneMap(data)})
	return nil
}

// refundKey extracts data.id as a stable string. The PHP script accepts any
// scalar id (int64 in production); we normalize to string for the map key.
func refundKey(data map[string]interface{}) (string, error) {
	raw, ok := data["id"]
	if !ok {
		return "", errors.New("ticket.refunded.data.id_required")
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return "", errors.New("ticket.refunded.data.id_empty")
		}
		return "s:" + v, nil
	case float64:
		return "n:" + jsonNumber(v), nil
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return "", errors.New("ticket.refunded.data.id_bad_type")
		}
		return "j:" + string(b), nil
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func cloneMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
