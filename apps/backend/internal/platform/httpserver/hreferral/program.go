package hreferral

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// programAnswer is the body of the programme routes.
type programAnswer struct {
	Program    programJSON    `json:"program"`
	Capability capabilityJSON `json:"capability"`
	Warnings   []warningJSON  `json:"warnings"`
}

func (h *Handler) answer(w http.ResponseWriter, _ *http.Request, status int, p gen.ReferralProgramRow, cap capabilityJSON, warnings []warningJSON) {
	httputil.WriteJSON(w, status, programAnswer{Program: toProgramJSON(p), Capability: cap, Warnings: warnings})
}

// HandleGet serves GET .../events/{event_id}/referral-program. The 404 carries
// the capability in error.details so a screen can say, before the organizer
// fills anything in, whether the event's channels can pay the cashback.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.eventTarget(w, r); ok {
		h.get(w, r, t)
	}
}

// HandleShopGet serves GET .../shop/referral-program.
func (h *Handler) HandleShopGet(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.shopTarget(w, r); ok {
		h.get(w, r, t)
	}
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, t target) {
	ctx := r.Context()
	cap, warnings, err := h.capability(ctx, t)
	if err != nil {
		h.failed(w, r, "capability", err)
		return
	}
	p, err := t.latest(ctx, h.queries)
	if errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteJSON(w, http.StatusNotFound, httputil.ErrorEnvelopeWithDetails(
			"referral.program_not_found", "this "+t.noun()+" has no friend programme", r,
			map[string]any{"capability": cap, "warnings": warnings}))
		return
	}
	if err != nil {
		h.failed(w, r, "programme read", err)
		return
	}
	h.answer(w, r, http.StatusOK, p, cap, warnings)
}

// putBody is the full replacement a PUT sends: an absent max_friends or
// ends_at means "no limit", an absent cashback_cap_pct means 100.
type putBody struct {
	FriendDiscountPct *int    `json:"friend_discount_pct"`
	CashbackPct       *int    `json:"cashback_pct"`
	CashbackCapPct    *int    `json:"cashback_cap_pct"`
	MaxFriends        *int    `json:"max_friends"`
	EndsAt            *string `json:"ends_at"`
	AcceptTerms       bool    `json:"accept_terms"`
}

type settings struct {
	discount, cashback, cap int16
	maxFriends              *int16
	endsAt                  *time.Time
}

func invalid(w http.ResponseWriter, r *http.Request, field, msg string) {
	httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
		"referral.invalid_value", msg, r, map[string]any{"field": field}))
}

// parse validates a PUT body; false = the answer is written.
func (h *Handler) parse(w http.ResponseWriter, r *http.Request, b putBody) (settings, bool) {
	var s settings
	pct := func(v *int, field string, required bool, def int16) (int16, bool) {
		if v == nil {
			if required {
				invalid(w, r, field, field+" is required")
				return 0, false
			}
			return def, true
		}
		if *v < 1 || *v > 100 {
			invalid(w, r, field, field+" must be a whole number from 1 to 100")
			return 0, false
		}
		return int16(*v), true
	}
	var ok bool
	if s.discount, ok = pct(b.FriendDiscountPct, "friend_discount_pct", true, 0); !ok {
		return s, false
	}
	if s.cashback, ok = pct(b.CashbackPct, "cashback_pct", true, 0); !ok {
		return s, false
	}
	if s.cap, ok = pct(b.CashbackCapPct, "cashback_cap_pct", false, 100); !ok {
		return s, false
	}
	if b.MaxFriends != nil {
		if *b.MaxFriends < 1 || *b.MaxFriends > 32767 {
			invalid(w, r, "max_friends", "max_friends must be a whole number from 1 to 32767, or null for no limit")
			return s, false
		}
		v := int16(*b.MaxFriends)
		s.maxFriends = &v
	}
	if b.EndsAt != nil {
		t, err := time.Parse(time.RFC3339, *b.EndsAt)
		if err != nil {
			invalid(w, r, "ends_at", "ends_at must be an RFC 3339 timestamp, or null to run until the sales end")
			return s, false
		}
		if !t.After(h.now()) {
			invalid(w, r, "ends_at", "ends_at must be in the future")
			return s, false
		}
		t = t.UTC()
		s.endsAt = &t
	}
	return s, true
}

// HandlePut serves PUT .../events/{event_id}/referral-program: it starts the
// event's programme (201) or replaces the settings of its live one (200).
// Starting needs accept_terms: true; the programme's channels must be able to
// return a part of a payment (decision B-15), else 422 referral.provider_unsupported.
func (h *Handler) HandlePut(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.eventTarget(w, r); ok {
		h.put(w, r, t)
	}
}

// HandleShopPut serves PUT .../shop/referral-program: the same as HandlePut for
// the organization's shop (one live programme per organization).
func (h *Handler) HandleShopPut(w http.ResponseWriter, r *http.Request) {
	if t, ok := h.shopTarget(w, r); ok {
		h.put(w, r, t)
	}
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, t target) {
	var body putBody
	if !readBody(w, r, &body) {
		return
	}
	s, ok := h.parse(w, r, body)
	if !ok {
		return
	}
	ctx := r.Context()
	cap, warnings, err := h.capability(ctx, t)
	if err != nil {
		h.failed(w, r, "capability", err)
		return
	}
	if !cap.Supported {
		httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelopeWithDetails(
			"referral.provider_unsupported",
			"the payment provider of this "+t.noun()+" cannot return part of a payment, so the cashback could not be paid; no friend programme can be started on it",
			r, map[string]any{"capability": cap}))
		return
	}

	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.failed(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	if err := t.lock(ctx, q); err != nil {
		h.failed(w, r, "lock", err)
		return
	}
	cur, err := t.latest(ctx, q)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		h.failed(w, r, "programme read", err)
		return
	}
	live := err == nil && cur.Status != StatusEnded

	var row gen.ReferralProgramRow
	status, action := http.StatusOK, "v1.referral_program.update"
	if live {
		row, err = q.UpdateReferralProgram(ctx, gen.UpdateReferralProgramParams{
			ID: cur.ID, FriendDiscountPct: s.discount, CashbackPct: s.cashback, CashbackCapPct: s.cap,
			MaxFriends: s.maxFriends, EndsAt: s.endsAt,
		})
	} else {
		if !body.AcceptTerms {
			httputil.WriteJSON(w, http.StatusUnprocessableEntity, httputil.ErrorEnvelope(
				"referral.terms_not_accepted",
				"to start the programme you accept its terms: the cashback is paid at once and is not taken back if the friend returns the ticket", r))
			return
		}
		status, action = http.StatusCreated, "v1.referral_program.create"
		row, err = t.insert(ctx, q, s, actorLabel(ctx), createdVia(ctx))
	}
	if err != nil {
		h.failed(w, r, "programme write", err)
		return
	}
	meta := map[string]any{
		"friend_discount_pct": s.discount, "cashback_pct": s.cashback, "cashback_cap_pct": s.cap,
	}
	if live {
		meta["previous"] = map[string]any{
			"friend_discount_pct": cur.FriendDiscountPct, "cashback_pct": cur.CashbackPct, "cashback_cap_pct": cur.CashbackCapPct,
		}
	}
	if err := h.writeAudit(ctx, tx, r, action, row, meta); err != nil {
		h.failed(w, r, "audit", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		h.failed(w, r, "commit", err)
		return
	}
	h.answer(w, r, status, row, cap, warnings)
}

// transition applies pause, resume or end to the event's live programme.
func (h *Handler) transition(w http.ResponseWriter, r *http.Request, to, action string) {
	if t, ok := h.eventTarget(w, r); ok {
		h.transitionOf(w, r, t, to, action)
	}
}

func (h *Handler) shopTransition(w http.ResponseWriter, r *http.Request, to, action string) {
	if t, ok := h.shopTarget(w, r); ok {
		h.transitionOf(w, r, t, to, action)
	}
}

func (h *Handler) transitionOf(w http.ResponseWriter, r *http.Request, t target, to, action string) {
	ctx := r.Context()
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		h.failed(w, r, "begin", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := h.queries.WithTx(tx)
	if err := t.lock(ctx, q); err != nil {
		h.failed(w, r, "lock", err)
		return
	}
	cur, err := t.latest(ctx, q)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, r, http.StatusNotFound, "referral.program_not_found", "this "+t.noun()+" has no friend programme")
		return
	}
	if err != nil {
		h.failed(w, r, "programme read", err)
		return
	}
	// Repeating the state the programme is already in is a no-op; ending a
	// programme that is over is too. Anything else out of "ended" is refused:
	// ended is final, a new programme is started with PUT.
	row := cur
	switch cur.Status {
	case to:
	case StatusEnded:
		writeErr(w, r, http.StatusConflict, "referral.program_ended",
			"this friend programme has ended; start a new one instead")
		return
	default:
		row, err = q.SetReferralProgramStatus(ctx, cur.ID, to)
		if err != nil {
			h.failed(w, r, "status write", err)
			return
		}
		if err := h.writeAudit(ctx, tx, r, action, row, map[string]any{"from": cur.Status, "to": to}); err != nil {
			h.failed(w, r, "audit", err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		h.failed(w, r, "commit", err)
		return
	}
	cap, warnings, err := h.capability(ctx, t)
	if err != nil {
		h.failed(w, r, "capability", err)
		return
	}
	h.answer(w, r, http.StatusOK, row, cap, warnings)
}

// HandlePause serves POST .../referral-program/pause.
func (h *Handler) HandlePause(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusPaused, "v1.referral_program.pause")
}

// HandleResume serves POST .../referral-program/resume.
func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusActive, "v1.referral_program.resume")
}

// HandleEnd serves POST .../referral-program/end.
func (h *Handler) HandleEnd(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, StatusEnded, "v1.referral_program.end")
}

// HandleShopPause serves POST .../shop/referral-program/pause.
func (h *Handler) HandleShopPause(w http.ResponseWriter, r *http.Request) {
	h.shopTransition(w, r, StatusPaused, "v1.referral_program.pause")
}

// HandleShopResume serves POST .../shop/referral-program/resume.
func (h *Handler) HandleShopResume(w http.ResponseWriter, r *http.Request) {
	h.shopTransition(w, r, StatusActive, "v1.referral_program.resume")
}

// HandleShopEnd serves POST .../shop/referral-program/end.
func (h *Handler) HandleShopEnd(w http.ResponseWriter, r *http.Request) {
	h.shopTransition(w, r, StatusEnded, "v1.referral_program.end")
}
