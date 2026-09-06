// cmd_promo.go — the spec §7.6 promo-code commands (feature #491, W1-B1a).
//
// Two commands live here plus the discount evaluation GET_CART reuses:
//
//   - ADD_PROMO_CODES accepts the UNION of the documented `promoCodeList`
//     and the `promoCodes` spelling the WordPress plugin actually emits,
//     deduplicates it case-insensitively, caps it at MaxPromoCodesPerRequest
//     and classifies every entry into new / exist / error. The accepted codes
//     are appended to gateway_sessions.promo_codes; resultCode stays 0 even
//     when some codes were refused (the per-code lists carry that signal),
//     and `description` names the FIRST refusal in the caller's locale.
//   - CHECK_KDP validates the SINGULAR `promoCode` without storing anything
//     and answers 0 (usable) or 101 (user-visible business refusal).
//
// Matching is case-insensitive on promo_codes.code within the CART's org —
// buyers type the code by hand in the checkout form, and a gateway session is
// scoped to exactly one org.
//
// Validation delegates to hcheckout.ValidatePromoForLines over the cart's
// TierLines so a tier-restricted code is refused for a cart that holds none of
// its tiers. An EMPTY cart cannot go through that path: with no lines a
// restricted code would report promo.tier_not_applicable and an unrestricted
// one would trip min_order_amount, neither of which is a statement about the
// code itself. Empty carts therefore get the status / valid_from / valid_until
// checks only, which is exactly what "is this code usable at all" means before
// the buyer has picked seats.
//
// Only ONE of the stored codes is ever applied to money — see
// tests/compat/bil24/BEHAVIOR_DIFFERENCES.md; checkout_sessions.promo_code_id
// is singular, so GET_CART applies the first stored code that yields a
// non-zero discount and ignores the rest.
package hbil24

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
)

// MaxPromoCodesPerRequest is the spec §7.6 cap on how many codes a single
// ADD_PROMO_CODES call may carry. Extra entries past the cap are dropped
// silently rather than failing the whole request, because the legacy plugin
// resends its full accumulated list on every keystroke.
const MaxPromoCodesPerRequest = 10

// PromoQuerier is the promo-code persistence surface the §7.6 commands need:
// a case-insensitive code lookup scoped to the cart's org, and the idempotent
// append of accepted codes onto the gateway session.
type PromoQuerier interface {
	GetPromoCodeByCodeCI(ctx context.Context, orgID uuid.UUID, code string) (gen.PromoCodeRow, error)
	AppendGatewaySessionPromoCode(ctx context.Context, id uuid.UUID, promoCodes []string) error
}

// WithPromoCodes wires the spec §7.6 promo surface used by ADD_PROMO_CODES,
// CHECK_KDP and the GET_CART discount. Callers that omit this setter keep the
// pre-#491 behaviour: both commands answer resultCode=-99 and GET_CART reports
// no discount. Returns the receiver for chaining.
func (h *Handler) WithPromoCodes(q PromoQuerier) *Handler {
	h.promoQ = q
	return h
}

// promoErrorKeys maps the hcheckout validation error codes onto the bil24.*
// message ids of spec §6. The WordPress checkout renders `description`
// verbatim next to the promo input, so each refusal names its own reason
// instead of collapsing into a generic "invalid code".
var promoErrorKeys = map[string]struct {
	key     string
	english string
}{
	"promo.not_active":           {"bil24.promo_invalid", "promo code is not valid"},
	"promo.not_yet_valid":        {"bil24.promo_not_yet_valid", "promo code is not valid yet"},
	"promo.expired":              {"bil24.promo_expired", "promo code has expired"},
	"promo.tier_not_applicable":  {"bil24.promo_not_applicable", "promo code does not apply to the tickets in your cart"},
	"promo.invalid_order_amount": {"bil24.promo_min_order", "the order total is below the minimum for this promo code"},
}

// promoNotFoundKey is the refusal used when no promo_codes row matches the
// typed code in the cart's org — the single most common case in production.
const (
	promoNotFoundKey     = "bil24.promo_not_found"
	promoNotFoundEnglish = "promo code was not found"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared preamble
// ─────────────────────────────────────────────────────────────────────────────

// promoCtx bundles what both §7.6 commands resolve before touching a code.
type promoCtx struct {
	cc   cartCtx
	snap cartSnapshot
}

// resolvePromoContext runs the credential / gateway-session / cart preamble
// shared by ADD_PROMO_CODES and CHECK_KDP. It mirrors handleBil24GetCart
// exactly — the three commands address the same cart and must reject the same
// requests the same way. A false second return means the envelope was already
// written.
func (h *Handler) resolvePromoContext(w http.ResponseWriter, r *http.Request, req bil24Request) (promoCtx, bool) {
	ctx := r.Context()

	// Without the cart surface or the promo surface there is nothing to
	// validate a code against; -99 keeps degraded compositions honest instead
	// of accepting codes that will never be applied.
	if !h.cartDeps.wired() || h.promoQ == nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "promo service unavailable",
		))
		return promoCtx{}, false
	}

	channel, authed := h.authenticateCommand(ctx, w, req)
	if !authed {
		if h.requireToken {
			// authenticateCommand already wrote the -4 envelope.
			return promoCtx{}, false
		}
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"unknown fid or channel disabled",
		))
		return promoCtx{}, false
	}

	locale := parseGatewaySettings(channel.Settings).DefaultLocale

	gw, ok := h.resolveGatewaySession(ctx, w, req, channel, locale)
	if !ok {
		return promoCtx{}, false
	}
	if gw.ID == uuid.Nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "promo service unavailable",
		))
		return promoCtx{}, false
	}

	cc := cartCtx{
		gw:      gw,
		channel: channel,
		locale:  locale,
		ttl:     cartHoldTTL(channel),
		orgID:   gw.OrgID,
	}

	snap, err := h.collectCart(ctx, cc)
	if err != nil {
		h.writeCartTransient(w, req, cc)
		return promoCtx{}, false
	}
	return promoCtx{cc: cc, snap: snap}, true
}

// ─────────────────────────────────────────────────────────────────────────────
// Code normalization
// ─────────────────────────────────────────────────────────────────────────────

// mergePromoCodes builds the spec §7.6 union of the two request spellings:
// trimmed, empties dropped, deduplicated case-insensitively with the FIRST
// spelling winning (that is the spelling echoed back in the response lists),
// truncated to MaxPromoCodesPerRequest.
func mergePromoCodes(lists ...[]string) []string {
	out := make([]string, 0, MaxPromoCodesPerRequest)
	seen := make(map[string]struct{}, MaxPromoCodesPerRequest)
	for _, list := range lists {
		for _, raw := range list {
			code := strings.TrimSpace(raw)
			if code == "" {
				continue
			}
			k := strings.ToLower(code)
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, code)
			if len(out) == MaxPromoCodesPerRequest {
				return out
			}
		}
	}
	return out
}

// containsFold reports whether list holds code, compared case-insensitively.
func containsFold(list []string, code string) bool {
	for _, c := range list {
		if strings.EqualFold(strings.TrimSpace(c), code) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Evaluation
// ─────────────────────────────────────────────────────────────────────────────

// promoVerdict is the outcome of evaluating one code against one cart.
type promoVerdict struct {
	row      gen.PromoCodeRow
	discount int64
	// errCode is "" when the code is usable; otherwise it is either an
	// hcheckout "promo.*" code or promoNotFound.
	errCode string
}

// promoNotFound is the sentinel errCode for "no such code in this org". It is
// deliberately not an hcheckout code: hcheckout never looks codes up.
const promoNotFound = "promo.not_found"

// evaluatePromoCode resolves the code in the cart's org and validates it
// against the cart. See the file header for why an empty cart takes the
// status/window-only path.
func (h *Handler) evaluatePromoCode(ctx context.Context, pc promoCtx, code string, now time.Time) (promoVerdict, error) {
	row, err := h.promoQ.GetPromoCodeByCodeCI(ctx, pc.cc.orgID, code)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return promoVerdict{errCode: promoNotFound}, nil
		}
		h.logger.Error("bil24_compat: promo code lookup failed",
			slog.String("org_id", pc.cc.orgID.String()),
			slog.String("error", err.Error()),
		)
		return promoVerdict{}, err
	}

	if len(pc.snap.lines) == 0 {
		if code := promoWindowError(row, now); code != "" {
			return promoVerdict{row: row, errCode: code}, nil
		}
		return promoVerdict{row: row}, nil
	}

	discount, errCode := hcheckout.ValidatePromoForLines(row, promoTierLines(pc.snap), now)
	return promoVerdict{row: row, discount: discount, errCode: errCode}, nil
}

// promoWindowError applies only the code-intrinsic checks — status and the
// validity window — in the same order and with the same error codes
// hcheckout.ValidatePromoForLines uses, so an empty-cart refusal and a
// full-cart refusal are worded identically.
func promoWindowError(pc gen.PromoCodeRow, now time.Time) string {
	if pc.Status != "active" {
		return "promo.not_active"
	}
	if pc.ValidFrom != nil && now.Before(*pc.ValidFrom) {
		return "promo.not_yet_valid"
	}
	if pc.ValidUntil != nil && now.After(*pc.ValidUntil) {
		return "promo.expired"
	}
	return ""
}

// promoTierLines projects the cart snapshot onto the tier-aware promo input.
// One TierLine per held unit: the per-line price is already the unit total.
func promoTierLines(snap cartSnapshot) []hcheckout.TierLine {
	lines := make([]hcheckout.TierLine, 0, len(snap.lines))
	for _, l := range snap.lines {
		tid := ""
		if l.tierID != uuid.Nil {
			tid = l.tierID.String()
		}
		lines = append(lines, hcheckout.TierLine{TierID: tid, Amount: l.price})
	}
	return lines
}

// promoRefusalDescription localizes a verdict's error code for the wire.
func (h *Handler) promoRefusalDescription(req bil24Request, cc cartCtx, errCode string) string {
	if errCode == promoNotFound {
		return h.localizeDesc(req.Locale, cc.locale, promoNotFoundKey, promoNotFoundEnglish, nil)
	}
	if m, ok := promoErrorKeys[errCode]; ok {
		return h.localizeDesc(req.Locale, cc.locale, m.key, m.english, nil)
	}
	return h.localizeDesc(req.Locale, cc.locale, "bil24.promo_invalid", "promo code is not valid", nil)
}

// ─────────────────────────────────────────────────────────────────────────────
// ADD_PROMO_CODES
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24AddPromoCodes serves spec §7.6. Per-code classification never
// fails the envelope: resultCode is 0 whenever the request itself was
// well-formed, and the caller reads newPromoCodeList / existPromoCodeList /
// errorPromoCodeList to learn what happened to each entry.
func (h *Handler) handleBil24AddPromoCodes(w http.ResponseWriter, r *http.Request, req bil24Request) {
	pc, ok := h.resolvePromoContext(w, r, req)
	if !ok {
		return
	}
	ctx := r.Context()

	codes := mergePromoCodes(req.PromoCodeList, req.PromoCodes)
	if len(codes) == 0 {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, pc.cc.locale, "bil24.invalid_request",
				"promoCodeList must contain at least one code", nil),
		))
		return
	}

	now := time.Now().UTC()
	newList := make([]string, 0, len(codes))
	existList := make([]string, 0, len(codes))
	errList := make([]string, 0, len(codes))
	accepted := make([]string, 0, len(codes))
	firstErr := ""

	for _, code := range codes {
		// Already on the session: "exist", without re-validating. The plugin
		// resends the whole accumulated list on every checkout render, so this
		// is the hot path and must stay a no-op.
		if containsFold(pc.cc.gw.PromoCodes, code) {
			existList = append(existList, code)
			continue
		}

		v, err := h.evaluatePromoCode(ctx, pc, code, now)
		if err != nil {
			h.writeCartTransient(w, req, pc.cc)
			return
		}
		if v.errCode != "" {
			errList = append(errList, code)
			if firstErr == "" {
				firstErr = v.errCode
			}
			continue
		}
		newList = append(newList, code)
		// Persist the CANONICAL spelling from the database so later lookups
		// (GET_CART, PAY_ORDER) never depend on how the buyer typed it.
		accepted = append(accepted, v.row.Code)
	}

	if len(accepted) > 0 {
		if err := h.promoQ.AppendGatewaySessionPromoCode(ctx, pc.cc.gw.ID, accepted); err != nil {
			h.logger.Error("bil24_compat: promo code persistence failed",
				slog.String("gateway_session_id", pc.cc.gw.ID.String()),
				slog.String("error", err.Error()),
			)
			h.writeCartTransient(w, req, pc.cc)
			return
		}
	}

	resp := bil24OK(req.Command, map[string]any{
		"newPromoCodeList":   newList,
		"existPromoCodeList": existList,
		"errorPromoCodeList": errList,
	})
	if firstErr != "" {
		resp.Description = h.promoRefusalDescription(req, pc.cc, firstErr)
	} else {
		resp.Description = h.localizeDesc(req.Locale, pc.cc.locale, "bil24.ok", "OK", nil)
	}
	writeBil24JSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// CHECK_KDP
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24CheckKDP serves the spec §7.6 single-code probe: it answers 0
// when the code would be accepted for this cart and 101 (user-visible business
// error) with a localized reason when it would not. Nothing is persisted, so
// the plugin can call it on every keystroke.
func (h *Handler) handleBil24CheckKDP(w http.ResponseWriter, r *http.Request, req bil24Request) {
	pc, ok := h.resolvePromoContext(w, r, req)
	if !ok {
		return
	}
	ctx := r.Context()

	code := strings.TrimSpace(req.PromoCode)
	if code == "" {
		// Tolerate the plural spellings so a plugin build that reuses the
		// ADD_PROMO_CODES body for the probe still gets an answer.
		if merged := mergePromoCodes(req.PromoCodeList, req.PromoCodes); len(merged) > 0 {
			code = merged[0]
		}
	}
	if code == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInvalidRequest,
			h.localizeDesc(req.Locale, pc.cc.locale, "bil24.invalid_request",
				"promoCode is required", nil),
		))
		return
	}

	v, err := h.evaluatePromoCode(ctx, pc, code, time.Now().UTC())
	if err != nil {
		h.writeCartTransient(w, req, pc.cc)
		return
	}
	if v.errCode != "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUserVisible,
			h.promoRefusalDescription(req, pc.cc, v.errCode),
		))
		return
	}

	resp := bil24OK(req.Command, nil)
	resp.Description = h.localizeDesc(req.Locale, pc.cc.locale, "bil24.ok", "OK", nil)
	writeBil24JSON(w, http.StatusOK, resp)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_CART discount
// ─────────────────────────────────────────────────────────────────────────────

// cartDiscount is the per-cart result of applying the session's promo codes.
type cartDiscount struct {
	total   int64
	perLine []int64 // parallel to cartSnapshot.lines
}

// sessionCartDiscount applies the gateway session's stored promo codes to the
// snapshot. The codes are tried in stored order and the FIRST one that yields
// a non-zero discount wins — checkout_sessions.promo_code_id is singular, so
// only one code can survive into the order (BEHAVIOR_DIFFERENCES.md).
//
// The total is prorated across the cart rows in proportion to their price,
// with the rounding remainder placed on the LAST row, so the per-row discounts
// always sum back to the total the money block reports.
func (h *Handler) sessionCartDiscount(ctx context.Context, pc promoCtx) cartDiscount {
	empty := cartDiscount{perLine: make([]int64, len(pc.snap.lines))}
	if h.promoQ == nil || len(pc.cc.gw.PromoCodes) == 0 || len(pc.snap.lines) == 0 || pc.snap.sum <= 0 {
		return empty
	}

	now := time.Now().UTC()
	for _, code := range pc.cc.gw.PromoCodes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		v, err := h.evaluatePromoCode(ctx, pc, code, now)
		if err != nil {
			// A lookup failure must not fail GET_CART: the cart itself is
			// readable, so report it without a discount rather than -1.
			return empty
		}
		if v.errCode != "" || v.discount <= 0 {
			continue
		}
		return prorateDiscount(pc.snap, v.discount)
	}
	return empty
}

// prorateDiscount spreads total across the snapshot's lines proportionally to
// their price. The discount is clamped to the cart sum, and the remainder left
// by integer division lands on the last line.
func prorateDiscount(snap cartSnapshot, total int64) cartDiscount {
	out := cartDiscount{total: total, perLine: make([]int64, len(snap.lines))}
	if total <= 0 || snap.sum <= 0 || len(snap.lines) == 0 {
		out.total = 0
		return out
	}
	if total > snap.sum {
		total = snap.sum
		out.total = total
	}
	var assigned int64
	for i, l := range snap.lines[:len(snap.lines)-1] {
		share := total * l.price / snap.sum
		out.perLine[i] = share
		assigned += share
	}
	out.perLine[len(snap.lines)-1] = total - assigned
	return out
}
