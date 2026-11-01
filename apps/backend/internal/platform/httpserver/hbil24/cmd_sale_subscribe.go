package hbil24

// cmd_sale_subscribe.go — SUBSCRIBE_SALE, an arena extension of the gateway
// (not part of the Bil24 protocol): a WordPress site's "notify me when sales
// open" form on a "coming soon" session (package salesubscribe, migration
// 0183). The site sends the buyer's address, its own page and the consent
// the buyer ticked; arena keeps it, mails the confirmation (double opt-in)
// and, when the session opens, the letter with the link to that page.
//
//	{"command":"SUBSCRIBE_SALE","fid":7,"token":"…","actionEventId":1000000848,
//	 "email":"a@b.c","locale":"he","pageUrl":"https://site/event/",
//	 "consent":true,"consentVersion":"sale-notify-2026-11-01"}
//	→ {"resultCode":0,"description":"OK","command":"SUBSCRIBE_SALE","status":"check_email"}
//
// The site is authenticated by its gateway token, so its page is kept as it
// is (an absolute http(s) URL). A bad address or a missing consent is -2;
// open sales, a session that is not "coming soon" and too many attempts are
// 101 with a description the site shows the buyer (open sales also carry
// salesState "open", so the site can switch the page to the buy button).

import (
	"errors"
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesubscribe"
)

// WithSaleSubscriptions wires the "notify me" service. Returns the receiver.
func (h *Handler) WithSaleSubscriptions(svc *salesubscribe.Service) *Handler {
	h.saleSubscribe = svc
	return h
}

func (h *Handler) handleBil24SubscribeSale(w http.ResponseWriter, r *http.Request, req bil24Request) {
	if h.saleSubscribe == nil || h.compatDB == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(req.Command, ResultCodeInternalError, "subscription service unavailable"))
		return
	}
	ctx := r.Context()
	sessionID, err := h.resolveActionEventID(ctx, req.ActionEventID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(req.Command, ResultCodeInvalidRequest,
			"actionEventId must be a valid session identifier"))
		return
	}
	channel, authed := h.authenticateCommand(ctx, w, req)
	if !authed {
		if !h.requireToken {
			writeBil24JSON(w, http.StatusOK, bil24Error(req.Command, ResultCodeUnauthorized, "token required"))
		}
		return
	}
	if !h.enforceSessionOrg(ctx, w, req, sessionID, channel.OrgID) {
		return
	}
	eventID, orgID, err := gen.New(h.compatDB).GetSalesStateSessionScope(ctx, sessionID)
	if err != nil || orgID != channel.OrgID {
		writeBil24JSON(w, http.StatusOK, bil24Error(req.Command, ResultCodeNotFound, "session not found"))
		return
	}
	channelID := channel.ID
	err = h.saleSubscribe.Subscribe(ctx, salesubscribe.Input{
		Target:         salesubscribe.Target{SessionID: sessionID, EventID: eventID, OrgID: orgID, ChannelID: &channelID},
		Email:          req.Email,
		Locale:         req.Locale,
		PageURL:        req.PageURL,
		Source:         salesubscribe.SourceSite,
		Consent:        req.Consent,
		ConsentVersion: req.ConsentVersion,
	})
	if err == nil {
		writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{"status": "check_email"}))
		return
	}
	switch {
	case errors.Is(err, salesubscribe.ErrInvalidEmail):
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, "", "bil24.subscription_invalid_email", "Check the e-mail address and send it again.", nil)))
	case errors.Is(err, salesubscribe.ErrConsentRequired), errors.Is(err, salesubscribe.ErrConsentOutdated):
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, "", "bil24.subscription_consent", "Tick the consent box to get the letter.", nil)))
	case errors.Is(err, salesubscribe.ErrSalesOpen):
		resp := bil24ErrorWithUserMessage(req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, "", "bil24.subscription_sales_open", "Sales are open now. Reload the page to buy tickets.", nil))
		resp.Data["salesState"] = "open"
		writeBil24JSON(w, http.StatusOK, resp)
	case errors.Is(err, salesubscribe.ErrNotComingSoon):
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, "", "bil24.subscription_not_soon", "This session is not waiting for sales to open.", nil)))
	case errors.Is(err, salesubscribe.ErrRateLimited):
		writeBil24JSON(w, http.StatusOK, bil24ErrorWithUserMessage(req.Command, ResultCodeUserVisible,
			h.localizeDesc(req.Locale, "", "bil24.subscription_rate_limited", "Too many attempts. Try again in an hour.", nil)))
	default:
		h.logger.Error("bil24_compat: SUBSCRIBE_SALE failed", "error", err.Error())
		writeBil24JSON(w, http.StatusOK, bil24Error(req.Command, ResultCodeTransient,
			h.localizeDesc(req.Locale, "", "bil24.internal", "service temporarily unavailable", nil)))
	}
}
