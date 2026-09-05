// cmd_user.go — CREATE_USER plus the shared gateway-session guard
// (feature #481, W1-A4c, spec §7.3).
//
// CREATE_USER is the first command a WordPress site issues for a visitor.
// It takes an optional identity payload (email / firstName / lastName /
// phone), resolves it to a platform customer through the spec §12.2
// resolver, mints a fresh gateway_sessions row and returns the pair the
// site then echoes on every subsequent command:
//
//	{ "resultCode": 0, "description": "OK", "command": "CREATE_USER",
//	  "userId": 1000000001, "sessionId": "<43-char base64url>" }
//
// userId is customers.system_id — the bigint compatibility id, NOT a UUID —
// so the wire stays int64 end-to-end (spec §3.1). sessionId is 32 bytes of
// crypto/rand rendered as unpadded base64url, i.e. exactly 43 characters.
//
// requireGatewaySession is the mirror image: every command that carries a
// (userId, sessionId) pair runs it before touching platform state. A
// missing, unknown, expired or cross-org session yields resultCode=1
// ("session expired") — the one code the WordPress plugin special-cases by
// silently re-running CREATE_USER and retrying. Anything else would surface
// to the buyer as a hard error.

package hbil24

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/customers"
)

// DefaultGatewaySessionTTL is how long a gateway session stays valid
// (spec §7.3: `expires_at = now() + 30d`). The TTL is sliding — every
// command that passes requireGatewaySession pushes expires_at forward by
// another full TTL, so an actively-shopping visitor never loses the cookie
// mid-checkout.
const DefaultGatewaySessionTTL = 30 * 24 * time.Hour

// gatewaySessionTokenBytes is the entropy behind sessionId. 32 raw bytes
// render as 43 characters of unpadded base64url, which is the width the
// spec pins and the harness asserts.
const gatewaySessionTokenBytes = 32

// uniqueViolationCode is PostgreSQL's SQLSTATE for a unique-constraint
// breach. gateway_sessions.session_token is UNIQUE, so a 23505 on insert
// means the freshly-minted token collided and we should simply mint again.
const uniqueViolationCode = "23505"

// sessionTTLOrDefault resolves the configured TTL, falling back to the
// spec §7.3 default when the handler was built without WithSessionTTL.
func (h *Handler) sessionTTLOrDefault() time.Duration {
	if h.sessionTTL > 0 {
		return h.sessionTTL
	}
	return DefaultGatewaySessionTTL
}

// newGatewaySessionToken mints a fresh sessionId: 32 crypto/rand bytes in
// unpadded base64url (43 characters). crypto/rand.Read never returns a
// short read, but the error is propagated rather than ignored so a broken
// entropy source surfaces as -99 instead of a predictable token.
func newGatewaySessionToken() (string, error) {
	buf := make([]byte, gatewaySessionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// bil24DisplayName joins the optional firstName / lastName parts into the
// customers.display_name value (spec §7.3: `firstName + " " + lastName`).
// Either part may be missing; the result is trimmed so a lone lastName does
// not arrive with a leading space and an entirely empty payload yields ""
// (which the §12.2 resolver treats as "do not overwrite the stored name").
func bil24DisplayName(firstName, lastName string) string {
	return strings.TrimSpace(strings.TrimSpace(firstName) + " " + strings.TrimSpace(lastName))
}

// ─────────────────────────────────────────────────────────────────────────────
// CREATE_USER (spec §7.3)
// ─────────────────────────────────────────────────────────────────────────────

// handleBil24CreateUser resolves the request's optional identity payload to
// a platform customer and mints a gateway session for it.
//
// Every field of the payload is optional: a request with no email and no
// phone creates a brand-new anonymous customer, exactly as the spec
// prescribes ("без ключей — новый анонимный покупатель"). Repeating the
// call with the same email therefore returns the SAME userId but a NEW
// sessionId — the command is deliberately not idempotent on the session.
func (h *Handler) handleBil24CreateUser(w http.ResponseWriter, r *http.Request, req bil24Request) {
	ctx := r.Context()

	// CREATE_USER always needs a resolved channel: gateway_sessions.org_id
	// and .channel_id are NOT NULL, and the session is scoped to exactly
	// one (org, channel) pair. Unlike the read commands there is no
	// meaningful unauthenticated fallback, so an unresolved fid is -4 even
	// when requireToken is off.
	channel, ok := h.authenticateCommand(ctx, w, req)
	if !ok {
		if h.requireToken {
			// authenticateCommand already wrote the -4 envelope.
			return
		}
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			h.localizeDesc(req.Locale, "", "bil24.unauthorized",
				"unknown fid or channel disabled", nil),
		))
		return
	}

	gw := parseGatewaySettings(channel.Settings)

	if h.sessionQ == nil || h.customerStore == nil {
		h.logger.Warn("bil24_compat: CREATE_USER: customer/session surface not wired",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			h.localizeDesc(req.Locale, gw.DefaultLocale, "bil24.internal",
				"user service unavailable", nil),
		))
		return
	}

	// Spec §12.2: email and phone are strong keys, so an existing buyer is
	// found rather than duplicated. The channel scopes the weak-key lookup;
	// DefaultRegion stays empty because the WordPress sites send E.164.
	res, err := customers.Resolve(ctx, h.customerStore, customers.ResolveInput{
		Email:     strings.TrimSpace(req.Email),
		Phone:     strings.TrimSpace(req.Phone),
		Name:      bil24DisplayName(req.FirstName, req.LastName),
		ChannelID: channel.ID,
		Source:    customers.SourceLive,
	})
	if err != nil {
		h.logger.Error("bil24_compat: CREATE_USER: customer resolve failed",
			slog.String("fid", req.FID),
			slog.String("channel_id", channel.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient,
			h.localizeDesc(req.Locale, gw.DefaultLocale, "", // no dedicated bil24.* key: transient failures use the English text
				"failed to resolve customer", nil),
		))
		return
	}

	// The session records the locale the site negotiated so later commands
	// that carry no `locale` field still answer in the buyer's language.
	locale := bil24compat.NegotiateBil24Locale(req.Locale, gw.DefaultLocale)
	expiresAt := time.Now().UTC().Add(h.sessionTTLOrDefault())

	var token string
	for attempt := 0; attempt < 3; attempt++ {
		token, err = newGatewaySessionToken()
		if err != nil {
			break
		}
		_, err = h.sessionQ.InsertGatewaySession(ctx, token,
			res.Customer.ID, channel.OrgID, channel.ID, locale, []string{}, expiresAt)
		if err == nil {
			break
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolationCode {
			break
		}
		// Token collision (astronomically unlikely): mint another one.
	}
	if err != nil {
		h.logger.Error("bil24_compat: CREATE_USER: session insert failed",
			slog.String("customer_id", res.Customer.ID.String()),
			slog.String("channel_id", channel.ID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient,
			h.localizeDesc(req.Locale, gw.DefaultLocale, "", // no dedicated bil24.* key: transient failures use the English text
				"failed to create session", nil),
		))
		return
	}

	h.logger.Info("bil24_compat: CREATE_USER: session created",
		slog.Int64("user_id", res.Customer.SystemID),
		slog.Bool("customer_created", res.Created),
		slog.String("channel_id", channel.ID.String()),
		slog.String("locale", locale),
	)

	writeBil24JSON(w, http.StatusOK, bil24OK(req.Command, map[string]any{
		"userId":    res.Customer.SystemID,
		"sessionId": token,
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// requireGatewaySession — the shared (userId, sessionId) guard
// ─────────────────────────────────────────────────────────────────────────────

// requireGatewaySession validates the (userId, sessionId) pair a command
// carries and refreshes the session's clock on success.
//
// Failure modes, ALL of which surface as resultCode=1 (spec §7.4 —
// "userId/sessionId не найдены или истекли → 1"):
//
//   - sessionId or userId absent from the request,
//   - sessionId unknown to gateway_sessions,
//   - expires_at already in the past,
//   - the session belongs to a different organization than the fid's
//     channel (a cross-tenant replay attempt),
//   - the wire userId does not name the session's own customer.
//
// A genuine database failure is NOT a session problem, so it maps to
// resultCode=-1 (transient) instead — the site should retry rather than
// throw the visitor's cart away.
//
// On success expires_at is pushed forward by a full TTL (the query also
// bumps last_seen_at), implementing the sliding window from spec §7.3.
//
// When the session surface is not wired (h.sessionQ == nil — unit tests and
// deployments that have not migrated yet) the guard is a pass-through, which
// matches the nil-dependency self-gating convention used across this
// package.
func (h *Handler) requireGatewaySession(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	channel gen.SalesChannelRow,
	channelDefaultLocale string,
) bool {
	if h.sessionQ == nil {
		return true
	}

	reject := func(reason string) bool {
		h.logger.Warn("bil24_compat: gateway session rejected",
			slog.String("command", req.Command),
			slog.String("fid", req.FID),
			slog.Int64("user_id", req.UserID),
			slog.String("reason", reason),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeSessionExpired,
			h.localizeDesc(req.Locale, channelDefaultLocale,
				"bil24.session_expired", "session expired", nil),
		))
		return false
	}

	token := strings.TrimSpace(req.SessionID)
	if token == "" || req.UserID <= 0 {
		return reject("sessionId or userId missing")
	}

	row, err := h.sessionQ.GetGatewaySessionByToken(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject("unknown sessionId")
		}
		h.logger.Error("bil24_compat: gateway session lookup failed",
			slog.String("command", req.Command),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient,
			h.localizeDesc(req.Locale, channelDefaultLocale, "", // no dedicated bil24.* key: transient failures use the English text
				"failed to resolve session", nil),
		))
		return false
	}

	now := time.Now().UTC()
	if !row.ExpiresAt.After(now) {
		return reject("session expired")
	}

	// Cross-org replay guard: the session is bound to the (org, channel)
	// that minted it. Only compare when the caller resolved a channel —
	// the unauthenticated fallback passes the zero row.
	if channel.OrgID != uuid.Nil && row.OrgID != channel.OrgID {
		return reject("session belongs to another organization")
	}

	cust, err := h.sessionQ.GetCustomerBySystemID(ctx, req.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reject("unknown userId")
		}
		h.logger.Error("bil24_compat: gateway session customer lookup failed",
			slog.String("command", req.Command),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeTransient,
			h.localizeDesc(req.Locale, channelDefaultLocale, "", // no dedicated bil24.* key: transient failures use the English text
				"failed to resolve session", nil),
		))
		return false
	}
	if cust.ID != row.CustomerID {
		return reject("userId does not own this session")
	}

	// Sliding expiry (spec §7.3). A failure here is not fatal to the
	// command in flight — the session is valid right now — so it is logged
	// and swallowed.
	if err := h.sessionQ.ExtendGatewaySessionExpiry(ctx, row.ID,
		now.Add(h.sessionTTLOrDefault())); err != nil {
		h.logger.Warn("bil24_compat: gateway session refresh failed",
			slog.String("command", req.Command),
			slog.String("session_id", row.ID.String()),
			slog.String("error", err.Error()),
		)
	}
	return true
}
