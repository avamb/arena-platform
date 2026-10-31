package hinvitations

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	orgListDefaultLimit = 50
	orgListMaxLimit     = 200

	stateActive    = "active"
	stateCancelled = "cancelled"
	stateAll       = "all"
)

// sessionBrief is the session an organization-wide list item belongs to.
type sessionBrief struct {
	ID        string    `json:"id"`
	StartAt   time.Time `json:"start_at"`
	EventID   string    `json:"event_id"`
	EventName string    `json:"event_name"`
	VenueName *string   `json:"venue_name"`
	Timezone  string    `json:"timezone"`
}

// orgListItem is a session list item plus its session.
type orgListItem struct {
	listItem
	Session sessionBrief `json:"session"`
}

type orgListResponse struct {
	Items      []orgListItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// HandleOrgList serves GET /v1/organizations/{org_id}/invitations: every
// staff invitation of the organization, whoever issued it, newest first
// (spec 46 section 6.1). The bot and the web screen list all sessions of an
// organization through it. Filters: session_id, event_id, state (active |
// cancelled | all, default active), limit (default 50, at most 200) and an
// opaque keyset cursor.
func (h *Handler) HandleOrgList(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		unavailable(w, r)
		return
	}
	orgID, ok := httputil.UUIDPathParam(w, r, "org_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	f := gen.OrgInvitationFilter{State: stateActive, Limit: orgListDefaultLimit}
	for _, p := range []struct {
		key string
		dst **uuid.UUID
	}{{"session_id", &f.SessionID}, {"event_id", &f.EventID}} {
		if v := q.Get(p.key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				writeErr(w, r, http.StatusBadRequest, "invitation.invalid_filter", p.key+" is not a UUID", nil)
				return
			}
			*p.dst = &id
		}
	}
	if v := q.Get("state"); v != "" {
		if v != stateActive && v != stateCancelled && v != stateAll {
			writeErr(w, r, http.StatusBadRequest, "invitation.invalid_state", "state is active, cancelled or all", nil)
			return
		}
		f.State = v
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > orgListMaxLimit {
			writeErr(w, r, http.StatusBadRequest, "invitation.invalid_limit", "limit is a number from 1 to 200", nil)
			return
		}
		f.Limit = int32(n) //nolint:gosec // bounded above
	}
	if v := q.Get("cursor"); v != "" {
		at, id, ok := decodeOrgCursor(v)
		if !ok {
			writeErr(w, r, http.StatusBadRequest, "invitation.invalid_cursor", "cursor is not one this list returned", nil)
			return
		}
		f.CursorAt, f.CursorID = &at, &id
	}
	ctx := r.Context()
	asked := f.Limit
	f.Limit++ // one extra row tells whether another page exists
	rows, err := h.queries.ListOrgInvitations(ctx, orgID, f)
	if err != nil {
		h.internal(w, r, "org list", err)
		return
	}
	var next *string
	if int32(len(rows)) > asked { //nolint:gosec // bounded by limit
		rows = rows[:asked]
		last := rows[len(rows)-1]
		c := encodeOrgCursor(last.CreatedAt, last.ID)
		next = &c
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	ex, err := h.loadPageExtras(ctx, ids)
	if err != nil {
		h.internal(w, r, "org list: places and letters", err)
		return
	}
	items := make([]orgListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, orgListItem{
			listItem: newListItem(row.SessionInvitationRow, ex.places[row.ID], ex.letters[row.ID]),
			Session: sessionBrief{
				ID: row.SessionID.String(), StartAt: row.SessionStartAt, EventID: row.EventID.String(),
				EventName: row.EventName, VenueName: row.VenueName, Timezone: row.SessionTimezone,
			},
		})
	}
	httputil.WriteJSON(w, http.StatusOK, orgListResponse{Items: items, NextCursor: next})
}

// encodeOrgCursor packs the position after the last row of a page.
func encodeOrgCursor(at time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + "|" + id.String()))
}

// decodeOrgCursor is the inverse; ok is false for anything encodeOrgCursor
// did not produce.
func decodeOrgCursor(s string) (at time.Time, id uuid.UUID, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	micro, rest, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, false
	}
	us, err := strconv.ParseInt(micro, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	id, err = uuid.Parse(rest)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	return time.UnixMicro(us).UTC(), id, true
}
