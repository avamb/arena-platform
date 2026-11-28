package hscannerapp

// pair.go — the routes of the app itself that need no device token yet
// (spec 69 A.2, A.3) and the self-unpair (A.4), MA-11.

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

// Audit actor type of a scanner device (audit_events.actor_type). The audit
// writers rewrite only an API-key actor, so this value is kept.
const auditActorDevice = "device"

// PairRequest is the body of POST /v1/scanner-app/pair.
type PairRequest struct {
	Code           string `json:"code"`
	Platform       string `json:"platform"`
	DeviceLabel    string `json:"device_label"`
	ControllerName string `json:"controller_name"`
}

// PairResponse is the 201 body of POST /v1/scanner-app/pair.
type PairResponse struct {
	AccessToken      string     `json:"access_token"`
	AccessExpiresAt  time.Time  `json:"access_expires_at"`
	RefreshToken     string     `json:"refresh_token"`
	RefreshExpiresAt time.Time  `json:"refresh_expires_at"`
	Me               MeResponse `json:"me"`
}

// TokenPair is the 200 body of POST /v1/scanner-app/token/refresh.
type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

// HandlePair serves POST /v1/scanner-app/pair (no authorization): a pairing
// code becomes a device of the code's organization.
//
//	201 PairResponse
//	400 scanner_app.invalid_body / scanner_app.invalid_device (not counted)
//	404 scanner_app.code_not_found
//	409 scanner_app.pass_full
//	429 scanner_app.too_many_attempts + Retry-After
func (h *Handler) HandlePair(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	var req PairRequest
	if !readJSON(r, &req) {
		writeErr(w, r, http.StatusBadRequest, "scanner_app.invalid_body",
			"the request body is not a JSON object with code, platform, device_label and controller_name")
		return
	}
	ctx := r.Context()
	now := h.now()
	ipHash := scannerapp.HashIP(httputil.TrustedClientIP(r, h.TrustedProxies))
	codeHash := scannerapp.HashCode(req.Code)

	if err := scannerapp.CheckPairLimit(ctx, h.Pool, ipHash, codeHash, now); err != nil {
		var tm *scannerapp.TooManyAttemptsError
		if errors.As(err, &tm) {
			secs := int((tm.RetryAfter + time.Second - 1) / time.Second)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
				"scanner_app.too_many_attempts", "too many pairing attempts, please wait and try again", r,
				map[string]any{"retry_after_seconds": secs}))
			return
		}
		h.Logger.Error("hscannerapp: pair limit check failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner_app.pair_failed", "failed to pair the device")
		return
	}

	res, err := scannerapp.Pair(ctx, h.Pool, scannerapp.PairParams{
		Code: req.Code, Platform: req.Platform, DeviceLabel: req.DeviceLabel, ControllerName: req.ControllerName,
	}, now)
	if outcome, counted := scannerapp.PairOutcome(err); counted {
		if rerr := scannerapp.RecordPairAttempt(ctx, h.Pool, ipHash, codeHash, outcome, now); rerr != nil {
			h.Logger.Warn("hscannerapp: record pair attempt failed", "error", rerr.Error())
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, scannerapp.ErrInvalidDevice):
		writeErr(w, r, http.StatusBadRequest, "scanner_app.invalid_device",
			"platform must be ios, android or other; device_label and controller_name are at most 80 characters")
		return
	case errors.Is(err, scannerapp.ErrCodeNotFound):
		writeErr(w, r, http.StatusNotFound, "scanner_app.code_not_found",
			"the pairing code did not work, please ask for a new link")
		return
	case errors.Is(err, scannerapp.ErrPassFull):
		writeErr(w, r, http.StatusConflict, "scanner_app.pass_full",
			"the pass already has its number of devices, please ask the organizer to free a place")
		return
	default:
		h.Logger.Error("hscannerapp: pair failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner_app.pair_failed", "failed to pair the device")
		return
	}

	d := res.Device
	me, derr := scannerapp.Describe(ctx, h.Pool, d)
	if derr != nil {
		// The device exists and holds its place: the tokens must reach the
		// phone. The app reads the full description from GET /me later.
		h.Logger.Error("hscannerapp: describe paired device failed", "device_id", d.ID.String(), "error", derr.Error())
		me = scannerapp.Me{OrgID: d.OrgID, Kind: d.Kind}
	}
	meta := map[string]any{"org_id": d.OrgID.String(), "kind": d.Kind, "platform": d.Platform}
	if d.PassID != nil {
		meta["pass_id"] = d.PassID.String()
	}
	if d.UserID != nil {
		meta["user_id"] = d.UserID.String()
	}
	h.writeAudit(r, auditActorDevice, d.ID.String(), "v1.scanner_app.pair", "scanner_device", d.ID.String(), meta)
	httputil.WriteJSON(w, http.StatusCreated, PairResponse{
		AccessToken: res.AccessToken, AccessExpiresAt: res.AccessExpiresAt.UTC(),
		RefreshToken: res.RefreshToken, RefreshExpiresAt: res.RefreshExpiresAt.UTC(),
		Me: BuildMeResponse(d, me),
	})
}

// HandleRefresh serves POST /v1/scanner-app/token/refresh (no
// authorization header): a refresh token becomes a new token pair.
//
//	200 TokenPair
//	400 scanner_app.invalid_body
//	401 scanner_app.device_revoked (every refusal, an unknown token included)
func (h *Handler) HandleRefresh(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !readJSON(r, &req) {
		writeErr(w, r, http.StatusBadRequest, "scanner_app.invalid_body",
			"the request body is not a JSON object with refresh_token")
		return
	}
	res, err := scannerapp.Refresh(r.Context(), h.Pool, req.RefreshToken, h.now())
	if err != nil {
		var rev *scannerapp.RevokedError
		if errors.As(err, &rev) && rev.JustRevoked {
			h.writeAudit(r, auditActorDevice, rev.DeviceID.String(), "v1.scanner_app.device_revoke", "scanner_device",
				rev.DeviceID.String(), map[string]any{"org_id": rev.OrgID.String(), "reason": rev.Reason})
		}
		if errors.Is(err, scannerapp.ErrMalformed) || errors.Is(err, scannerapp.ErrUnknown) || errors.Is(err, scannerapp.ErrRevoked) {
			h.Logger.Warn("hscannerapp: refresh refused", "reason", err.Error())
			writeErr(w, r, http.StatusUnauthorized, "scanner_app.device_revoked",
				"the device is no longer paired, please pair it again")
			return
		}
		h.Logger.Error("hscannerapp: refresh failed", "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner_app.refresh_failed", "failed to refresh the device token")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, TokenPair{
		AccessToken: res.AccessToken, AccessExpiresAt: res.AccessExpiresAt.UTC(),
		RefreshToken: res.RefreshToken, RefreshExpiresAt: res.RefreshExpiresAt.UTC(),
	})
}

// HandleUnpair serves DELETE /v1/scanner-app/device: the device takes
// itself back (self_unpaired); a pass device frees its place. 204.
func (h *Handler) HandleUnpair(w http.ResponseWriter, r *http.Request) {
	if h.Pool == nil {
		writeNoDB(w, r)
		return
	}
	d, ok := scannerapp.DeviceFromContext(r.Context())
	if !ok {
		writeUnauthorized(w, r)
		return
	}
	just, err := scannerapp.RevokeSelf(r.Context(), h.Pool, d.OrgID, d.ID, h.now())
	if err != nil {
		h.Logger.Error("hscannerapp: unpair failed", "device_id", d.ID.String(), "error", err.Error())
		writeErr(w, r, http.StatusInternalServerError, "scanner_app.unpair_failed", "failed to unpair the device")
		return
	}
	if just {
		h.writeAudit(r, auditActorDevice, d.ID.String(), "v1.scanner_app.device_revoke", "scanner_device", d.ID.String(),
			map[string]any{"org_id": d.OrgID.String(), "reason": scannerapp.ReasonSelfUnpaired})
	}
	w.WriteHeader(http.StatusNoContent)
}
