package hbot

// guest_list.go — the bot's side of the automatic guest list (owner decision
// 2026-11-02, package guestlist, migration 0188):
//
//	POST /v1/bot/guest-list-deliveries/claim
//	POST /v1/bot/guest-list-deliveries/{id}/finish
//
// Called by the bot PROCESS under BOT_SERVICE_TOKEN. The claim hands out the
// lists whose moment came (24 h, 3 h before the start, doors open) for people
// linked to the bot, each taken for 10 minutes. The bot then fetches
// guest-list.csv AS that person (so the API decides whether they may still
// read it) and sends it to their private chat, then reports the outcome. The
// answer carries no data of the file.

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// guestListBatch caps one claim.
const guestListBatch = 20

// guestListReason is the shape of a reported reason: a short code.
var guestListReason = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)

// HandleClaimGuestListDeliveries serves POST /v1/bot/guest-list-deliveries/claim.
func (h *Handler) HandleClaimGuestListDeliveries(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	ds, err := h.queries.ClaimGuestListTelegramDeliveries(r.Context(), guestListBatch)
	if err != nil {
		h.logger.Error("hbot: claim guest list deliveries failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]any{
			"id":               d.ID.String(),
			"org_id":           d.OrgID.String(),
			"session_id":       d.SessionID.String(),
			"slot":             d.Slot,
			"attempt":          d.Attempt,
			"telegram_user_id": d.TelegramUserID,
			"event_name":       d.EventName,
			"session_start_at": d.SessionStartAt.UTC().Format(time.RFC3339),
			"session_timezone": d.SessionTimezone,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": out})
}

// HandleFinishGuestListDelivery serves
// POST /v1/bot/guest-list-deliveries/{id}/finish with
// {"attempt": <from the claim>, "outcome": "sent"|"failed"|"skipped"|"email",
// "reason": "<short code>"}. A report naming an older claim than the row has
// now answers 404 and changes nothing.
func (h *Handler) HandleFinishGuestListDelivery(w http.ResponseWriter, r *http.Request) {
	if h.queries == nil {
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Attempt int32  `json:"attempt"`
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Attempt < 1 ||
		(body.Outcome != gen.GuestListSent && body.Outcome != gen.GuestListFailed &&
			body.Outcome != gen.GuestListSkipped && body.Outcome != gen.GuestListToEmail) ||
		(body.Reason != "" && !guestListReason.MatchString(body.Reason)) {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope(
			"bot.invalid_guest_list_outcome", "attempt must name the claim; outcome must be sent, failed, skipped or email; reason a short code", r))
		return
	}
	var reason *string
	if body.Reason != "" {
		reason = &body.Reason
	}
	n, err := h.queries.FinishGuestListDelivery(r.Context(), id, body.Attempt, body.Outcome, reason)
	if err != nil {
		h.logger.Error("hbot: finish guest list delivery failed", slog.String("error", err.Error()))
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			"dependency.database_unavailable", "database is not available", r))
		return
	}
	if n == 0 {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelope(
			"bot.guest_list_delivery_not_found", "no open guest list delivery with this id and attempt", r))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
