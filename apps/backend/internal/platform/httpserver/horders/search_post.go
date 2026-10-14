package horders

// search_post.go — POST /v1/organizations/{org_id}/orders/search (owner
// decision 2026-10-14, spec 08_architecture/37 §13 item 5): the same search
// as GET .../orders, with the filters in the request BODY. A buyer's e-mail or
// phone in a query string is written by every proxy and by Cloudflare; a body
// is not. The web event center and the bot use this route.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// maxSearchBody caps the body: every field together is far below it.
const maxSearchBody = 4 << 10

// searchBody is the request body. Every field is optional and means exactly
// what the query parameter of the same name means on GET .../orders.
type searchBody struct {
	Q         string `json:"q"`
	Tab       string `json:"tab"`
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	EventID   string `json:"event_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Limit     *int   `json:"limit"`
	Offset    *int   `json:"offset"`
}

// values turns the body into the query parameters HandleList reads, so the
// two routes share every rule and every error code.
func (b searchBody) values() url.Values {
	v := url.Values{}
	set := func(name, value string) {
		if value != "" {
			v.Set(name, value)
		}
	}
	set("q", b.Q)
	set("tab", b.Tab)
	set("status", b.Status)
	set("session_id", b.SessionID)
	set("event_id", b.EventID)
	set("from", b.From)
	set("to", b.To)
	if b.Limit != nil {
		v.Set("limit", strconv.Itoa(*b.Limit))
	}
	if b.Offset != nil {
		v.Set("offset", strconv.Itoa(*b.Offset))
	}
	return v
}

// HandleSearch serves POST /v1/organizations/{org_id}/orders/search. A body
// that is not one JSON object of the known fields is 400
// orders.invalid_body; a query string on this route is refused too (400
// orders.query_in_url), so a client cannot fall back to leaking the search.
func (h *Handler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"orders.query_in_url", "send the search in the request body, not in the URL", r))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxSearchBody+1))
	if err != nil || len(raw) > maxSearchBody {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"orders.invalid_body", "the body must be a JSON object of at most 4 KiB", r))
		return
	}
	var body searchBody
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil || dec.More() || !isEOF(dec) {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"orders.invalid_body", "the body must be one JSON object of the search fields", r))
			return
		}
	}
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = body.values().Encode()
	r2.URL = &u
	h.HandleList(w, r2)
}

// isEOF reports whether nothing but whitespace follows the decoded object.
func isEOF(dec *json.Decoder) bool {
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}
