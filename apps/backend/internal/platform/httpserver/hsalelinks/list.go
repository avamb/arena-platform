package hsalelinks

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

const (
	defaultListLimit = 50
	maxListLimit     = 100
)

// encodeCursor makes the opaque cursor of a page: base64url of
// "<created_at RFC3339Nano>|<id>" of its last row.
func encodeCursor(row gen.SalesLinkRow) string {
	raw := row.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + row.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (time.Time, uuid.UUID, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return t, u, true
}

type listJSON struct {
	Items      []salesLinkJSON `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

// HandleList serves GET /v1/organizations/{org_id}/sales-links.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.orgID(w, r)
	if !ok {
		return
	}
	eventID, ok := optionalUUID(w, r, "event_id")
	if !ok {
		return
	}
	sessionID, ok := optionalUUID(w, r, "session_id")
	if !ok {
		return
	}
	status := "active"
	if v := strings.TrimSpace(r.URL.Query().Get("status")); v != "" {
		status = v
	}
	switch status {
	case "active", "archived":
	case "all":
		status = ""
	default:
		writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_status", "status must be active, archived or all")
		return
	}
	limit := defaultListLimit
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxListLimit {
			writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_limit", "limit must be a number from 1 to 100")
			return
		}
		limit = n
	}
	params := gen.ListSalesLinksParams{
		OrgID: orgID, EventID: eventID, SessionID: sessionID, Status: status, Limit: int32(limit + 1), //nolint:gosec // limit is at most 100
	}
	if v := strings.TrimSpace(r.URL.Query().Get("cursor")); v != "" {
		at, id, valid := decodeCursor(v)
		if !valid {
			writeErr(w, r, http.StatusBadRequest, "sales_link.invalid_cursor", "cursor is not one this route returned")
			return
		}
		params.CursorCreatedAt, params.CursorID = &at, &id
	}
	ctx := r.Context()
	rows, err := h.queries.ListSalesLinks(ctx, params)
	if err != nil {
		h.failed(w, r, "list", err)
		return
	}
	out := listJSON{Items: []salesLinkJSON{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = encodeCursor(rows[limit-1])
	}
	facts := newFactsCache(h.queries)
	for _, row := range rows {
		out.Items = append(out.Items, h.present(ctx, facts, row))
	}
	writeJSON(w, http.StatusOK, out)
}
