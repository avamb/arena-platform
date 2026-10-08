package honboarding

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/onboarding"
)

// The routes below are the Telegram bot's side of the application flow
// (08_architecture/34_onboarding_applications_ru.md §10). They sit behind
// hbot.RequireServiceToken and name the Telegram account in every call: an
// application is only reachable by the account it was started from.

type botIdentity struct {
	TelegramUserID int64 `json:"telegram_user_id"`
}

// tgFromBody checks that a body named a Telegram account.
func (h *Handler) tgOK(w http.ResponseWriter, r *http.Request, tg int64) bool {
	if tg <= 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelopeWithDetails(
			"onboarding.invalid_field", "telegram_user_id is required", r,
			map[string]any{"fields": map[string]string{"telegram_user_id": "required"}}))
		return false
	}
	return true
}

// HandleBotSchema serves GET /v1/bot/onboarding/form-schema.
func (h *Handler) HandleBotSchema(w http.ResponseWriter, r *http.Request) {
	schema, err := h.svc.Schema(r.Context(), r.URL.Query().Get("locale"))
	if err != nil {
		h.internal(w, r, "bot schema", err)
		return
	}
	terms, privacy := h.svc.SiteLinks()
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"schema": schema, "terms_url": terms, "privacy_url": privacy})
}

// HandleBotCurrent serves GET /v1/bot/onboarding/application?telegram_user_id=.
func (h *Handler) HandleBotCurrent(w http.ResponseWriter, r *http.Request) {
	tg, _ := strconv.ParseInt(r.URL.Query().Get("telegram_user_id"), 10, 64)
	if !h.tgOK(w, r, tg) {
		return
	}
	app, err := h.svc.BotOpen(r.Context(), tg)
	if err != nil {
		h.fail(w, r, "bot current", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

type botStartRequest struct {
	botIdentity
	TelegramUsername string `json:"telegram_username"`
	FirstName        string `json:"first_name"`
	LastName         string `json:"last_name"`
	Phone            string `json:"phone"`
	Email            string `json:"email"`
	Locale           string `json:"locale"`
}

// HandleBotStart serves POST /v1/bot/onboarding/applications.
func (h *Handler) HandleBotStart(w http.ResponseWriter, r *http.Request) {
	var req botStartRequest
	if !h.decode(w, r, &req) || !h.tgOK(w, r, req.TelegramUserID) {
		return
	}
	app, created, err := h.svc.BotStart(r.Context(), onboarding.BotStartInput{
		TelegramUserID: req.TelegramUserID, TelegramUsername: req.TelegramUsername,
		FirstName: req.FirstName, LastName: req.LastName, Phone: req.Phone, Email: req.Email, Locale: req.Locale,
	})
	if err != nil {
		h.fail(w, r, "bot start", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httputil.WriteJSON(w, status, map[string]any{"application": toPublic(app), "created": created})
}

type botAnswersRequest struct {
	botIdentity
	Answers map[string]any `json:"answers"`
}

// HandleBotSaveAnswers serves PUT /v1/bot/onboarding/applications/{id}/answers.
func (h *Handler) HandleBotSaveAnswers(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var req botAnswersRequest
	if !h.decode(w, r, &req) || !h.tgOK(w, r, req.TelegramUserID) {
		return
	}
	if len(req.Answers) == 0 {
		httputil.WriteJSON(w, http.StatusBadRequest, httputil.ErrorEnvelope("onboarding.empty_body", "answers are required", r))
		return
	}
	app, err := h.svc.BotSaveAnswers(r.Context(), id, req.TelegramUserID, req.Answers)
	if err != nil {
		h.fail(w, r, "bot save", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

// botByID runs an action on an application of the calling account.
func (h *Handler) botByID(w http.ResponseWriter, r *http.Request, op string, do func(id uuid.UUID, tg int64, body botCodeRequest) (*onboarding.Application, error)) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var req botCodeRequest
	if !h.decode(w, r, &req) || !h.tgOK(w, r, req.TelegramUserID) {
		return
	}
	app, err := do(id, req.TelegramUserID, req)
	if err != nil {
		h.fail(w, r, op, err)
		return
	}
	if app == nil {
		httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"application": toPublic(app)})
}

type botCodeRequest struct {
	botIdentity
	Code string `json:"code"`
}

// HandleBotEmailCode serves POST …/{id}/email-code.
func (h *Handler) HandleBotEmailCode(w http.ResponseWriter, r *http.Request) {
	h.botByID(w, r, "bot email code", func(id uuid.UUID, tg int64, _ botCodeRequest) (*onboarding.Application, error) {
		return nil, h.svc.BotSendCode(r.Context(), id, tg)
	})
}

// HandleBotConfirmEmail serves POST …/{id}/confirm-email.
func (h *Handler) HandleBotConfirmEmail(w http.ResponseWriter, r *http.Request) {
	h.botByID(w, r, "bot confirm email", func(id uuid.UUID, tg int64, b botCodeRequest) (*onboarding.Application, error) {
		return h.svc.BotConfirmEmail(r.Context(), id, tg, b.Code)
	})
}

// HandleBotSubmit serves POST …/{id}/submit.
func (h *Handler) HandleBotSubmit(w http.ResponseWriter, r *http.Request) {
	h.botByID(w, r, "bot submit", func(id uuid.UUID, tg int64, _ botCodeRequest) (*onboarding.Application, error) {
		return h.svc.BotSubmit(r.Context(), id, tg)
	})
}

// HandleBotSiteLink serves POST …/{id}/site-link.
func (h *Handler) HandleBotSiteLink(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.UUIDPathParam(w, r, "id")
	if !ok {
		return
	}
	var req botIdentity
	if !h.decode(w, r, &req) || !h.tgOK(w, r, req.TelegramUserID) {
		return
	}
	link, err := h.svc.BotSiteLink(r.Context(), id, req.TelegramUserID)
	if err != nil {
		h.fail(w, r, "bot site link", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"url": link})
}

// HandleBotClaimNotices serves POST /v1/bot/onboarding/notifications/claim.
func (h *Handler) HandleBotClaimNotices(w http.ResponseWriter, r *http.Request) {
	notices, err := h.svc.BotClaimNotices(r.Context(), 50)
	if err != nil {
		h.internal(w, r, "bot notices", err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"notices": notices})
}
