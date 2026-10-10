package htickets

// complimentary_list.go — GET /v1/organizations/{org_id}/complimentary (EC-12,
// spec 08_architecture/35 §6.3). It used to return every issuance of the
// organization as bare rows; the Telegram bot's "Invitations" screen needs a
// page of them, newest first, each with the event and date it is for, its
// category, its tickets (number, guest name and e-mail, whether the door has
// scanned it) and a state:
//
//	valid    issued and not scanned          revoked  annulled
//	used     at least one ticket scanned at the door, or parked for manual review
//
// Query: limit (default 20, at most 100), offset, state (valid, revoked, used;
// absent = all) and session_id (one date). The answer keeps the old
// `issuances` key and adds `total` (all that match, not the page) and
// `has_more`. The guests' e-mails are in the answer: it is organization data
// behind complimentary.read and is never logged.

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

const (
	complimentaryListDefaultLimit = 20
	complimentaryListMaxLimit     = 100
)

// HandleListComplimentaryIssuances serves GET /v1/organizations/{org_id}/complimentary.
func (h *Handler) HandleListComplimentaryIssuances(w http.ResponseWriter, r *http.Request) {
	if h.complimentaryQueries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r,
		))
		return
	}

	orgID, err := uuid.Parse(chi.URLParam(r, "org_id"))
	if err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"complimentary.invalid_org_id", "org_id must be a valid UUID", r,
		))
		return
	}

	q := r.URL.Query()
	limit, offset := int32(complimentaryListDefaultLimit), int32(0)
	if v := q.Get("limit"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > complimentaryListMaxLimit {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"complimentary.invalid_limit", "limit must be a number from 1 to 100", r,
			))
			return
		}
		limit = int32(n) //nolint:gosec // bounded to 1..100 above
	}
	if v := q.Get("offset"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 0 || n > 1_000_000 {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"complimentary.invalid_offset", "offset must be a non-negative number", r,
			))
			return
		}
		offset = int32(n) //nolint:gosec // bounded above
	}
	state := q.Get("state")
	switch state {
	case "", gen.ComplimentaryStateValid, gen.ComplimentaryStateRevoked, gen.ComplimentaryStateUsed:
	default:
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"complimentary.invalid_state", "state must be valid, revoked or used", r,
		))
		return
	}
	var sessionID *uuid.UUID
	if v := q.Get("session_id"); v != "" {
		sid, parseErr := uuid.Parse(v)
		if parseErr != nil {
			httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
				"complimentary.invalid_session_id", "session_id must be a valid UUID", r,
			))
			return
		}
		sessionID = &sid
	}

	ctx := r.Context()
	total, err := h.complimentaryQueries.CountComplimentaryIssuances(ctx, orgID, sessionID, state)
	if err != nil {
		h.logger.Error("complimentary: count failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"complimentary.list_failed", "failed to list complimentary issuances", r,
		))
		return
	}
	rows, err := h.complimentaryQueries.ListComplimentaryIssuancesPage(ctx, orgID, sessionID, state, limit, offset)
	if err != nil {
		h.logger.Error("complimentary: list failed", "error", err.Error())
		httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
			"complimentary.list_failed", "failed to list complimentary issuances", r,
		))
		return
	}

	byIssuance := map[uuid.UUID][]map[string]any{}
	if len(rows) > 0 {
		ids := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		tickets, tErr := h.complimentaryQueries.ListComplimentaryTicketsForIssuances(ctx, ids)
		if tErr != nil {
			h.logger.Error("complimentary: list tickets failed", "error", tErr.Error())
			httputil.WriteJSON(w, http.StatusInternalServerError, httputil.ErrorEnvelope(
				"complimentary.list_failed", "failed to list complimentary issuances", r,
			))
			return
		}
		for _, t := range tickets {
			byIssuance[t.IssuanceID] = append(byIssuance[t.IssuanceID], map[string]any{
				"id":               t.ID,
				"system_ticket_id": t.SystemTicketID,
				"holder_email":     t.HolderEmail,
				"holder_name":      t.HolderName,
				"status":           t.Status,
				"used":             t.Used,
			})
		}
	}

	issuances := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := complimentaryIssuanceFromRow(row.ComplimentaryIssuanceRow)
		item["event_id"] = row.EventID
		item["event_name"] = row.EventName
		item["session_start_at"] = row.SessionStartAt
		item["venue_timezone"] = row.VenueTimezone
		item["tier_name"] = row.TierName
		item["ticket_count"] = row.TicketCount
		item["state"] = row.State
		tickets := byIssuance[row.ID]
		if tickets == nil {
			tickets = []map[string]any{}
		}
		item["tickets"] = tickets
		issuances = append(issuances, item)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"issuances": issuances,
		"total":     total,
		"has_more":  int64(offset)+int64(len(rows)) < total,
	})
}
