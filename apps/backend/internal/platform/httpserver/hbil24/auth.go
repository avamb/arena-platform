// auth.go — Bil24 gateway authentication surface (feature #471, W1-A1b).
//
// Splits the credential-resolution and token-verification logic out of the
// monolithic bil24_compat.go. The gateway is a per-channel switch (spec §5):
//
//   - `fid` on the wire is the channel's numeric `display_number` (0072
//     migration; the WordPress plugins do `(int)$o['fid']` in
//     class-bil24-client.php:12, which would silently zero a UUID). This
//     package therefore parses `fid` as int64 (either a JSON number or a
//     string that contains a number) and resolves it via
//     GetSalesChannelByDisplayNumber. A UUID-shaped `fid` is still accepted
//     as a legacy fallback for the pre-W1 unit-test fixtures (bil24_374/390);
//     feature #476 (full split) will remove the UUID fallback.
//
//   - Per-channel enablement lives under `settings.gateway`:
//
//     { "settings": { "gateway": { "enabled": true,
//                                  "token_hash": "<bcrypt>",
//                                  "token_rotated_at": "<RFC3339>",
//                                  "default_locale": "cs" } } }
//
//     For backward compatibility with the shape written by feature #374/#390
//     admin plumbing (which stored the hash at the top level of `settings`
//     under `gateway_token_hash`), the parser reads the legacy key as a
//     fallback and treats such a channel as `enabled=true`. This fallback is
//     preserved for one more wave and will be removed once the admin
//     endpoint (`PUT /v1/organizations/{org_id}/channels/{id}/gateway-
//     credential`, spec §5.4) migrates every deployed channel to the new
//     shape.
//
// When `requireToken` is true (BIL24_REQUIRE_TOKEN=true in production),
// every command runs the credential check via authenticateCommand: any of
// the failure modes (channel deleted / gateway disabled / no hash stored /
// wrong token / missing token) surfaces as resultCode=-4 (Unauthorized).
// See spec §5 for the enumerated error surface.

package hbil24

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// settings.gateway parser (spec §5.1)
// ─────────────────────────────────────────────────────────────────────────────

// GatewaySettings is the strongly-typed projection of the
// sales_channels.settings.gateway JSON object (spec §5.1). All fields are
// optional; a channel with no gateway block AND no legacy top-level
// gateway_token_hash is considered "not configured for gateway access" and
// will be rejected under requireToken=true.
type GatewaySettings struct {
	// Enabled is the per-channel gateway on/off switch (spec §5.1). A
	// channel with `enabled=false` (or omitted, with no legacy fallback) is
	// rejected with resultCode=-4.
	Enabled bool
	// TokenHash is the bcrypt hash of the shared secret the WordPress
	// plugins send in the `token` field of every command. When empty and
	// requireToken=true the channel is treated as unconfigured (-4).
	TokenHash string
	// DefaultLocale is the fallback locale for description/error
	// localisation when the request's `locale` field is empty or unknown
	// (spec §5.1). Empty means fall through to "en" as the outer default.
	DefaultLocale string
	// LegacyOnly is true when only the deprecated top-level
	// gateway_token_hash was present (no `gateway` object). Callers log this
	// so the operator can be nudged to run the credential-rotation endpoint
	// (§5.4) and drop the legacy shape. Never affects authorization.
	LegacyOnly bool
}

// gatewaySettingsShape mirrors the two shapes we accept while decoding the
// raw sales_channels.settings JSONB blob. Keys are documented on
// GatewaySettings; the parser prefers the nested `gateway` object and falls
// back to the top-level `gateway_token_hash` only when the object is absent
// or empty.
type gatewaySettingsShape struct {
	Gateway *struct {
		Enabled       *bool  `json:"enabled"`
		TokenHash     string `json:"token_hash"`
		DefaultLocale string `json:"default_locale"`
	} `json:"gateway"`
	// Legacy top-level hash (feature #374/#390 admin shape). Preserved for
	// backward compat until #476 removes it.
	LegacyTokenHash string `json:"gateway_token_hash"`
}

// parseGatewaySettings decodes a sales_channels.settings JSONB blob into the
// strongly-typed GatewaySettings projection. A nil / empty / malformed blob
// yields the zero value (Enabled=false, TokenHash="") which callers must
// treat as "not configured".
//
// Precedence (spec §5.1, transition rule):
//  1. settings.gateway.{enabled,token_hash,default_locale} — the target
//     shape, written by the admin gateway-credential endpoint (§5.4).
//  2. settings.gateway_token_hash — legacy shape written by pre-W1 code;
//     treated as `enabled=true` when present so existing deployments keep
//     working through one wave.
func parseGatewaySettings(raw json.RawMessage) GatewaySettings {
	if len(raw) == 0 {
		return GatewaySettings{}
	}
	var shape gatewaySettingsShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		// Best-effort: a malformed blob is indistinguishable from an unset
		// one for authorization purposes.
		return GatewaySettings{}
	}
	if shape.Gateway != nil && (shape.Gateway.TokenHash != "" || shape.Gateway.Enabled != nil || shape.Gateway.DefaultLocale != "") {
		out := GatewaySettings{
			TokenHash:     shape.Gateway.TokenHash,
			DefaultLocale: shape.Gateway.DefaultLocale,
		}
		if shape.Gateway.Enabled != nil {
			out.Enabled = *shape.Gateway.Enabled
		}
		return out
	}
	if shape.LegacyTokenHash != "" {
		return GatewaySettings{
			Enabled:    true, // legacy shape carried no flag; presence = enabled
			TokenHash:  shape.LegacyTokenHash,
			LegacyOnly: true,
		}
	}
	return GatewaySettings{}
}

// ─────────────────────────────────────────────────────────────────────────────
// fid parsing (spec §5.2 / §4)
// ─────────────────────────────────────────────────────────────────────────────

// parseFIDInt64 accepts the wire representation of a `fid` field — either a
// JSON number or a string that decodes to one — and returns the int64 value
// used to look up sales_channels.display_number (0072). Empty input, a
// non-numeric string, or a value ≤ 0 all yield ok=false; callers must
// respond with resultCode=-4 in that case (spec §5.2).
//
// The wire keeps `fid` as `string` on the arena side because the WP client
// serialises it inconsistently (sometimes number, sometimes string —
// class-bil24-client.php lines 12/14). Trim and parse; reject the pathological
// zero and negative values (0072 sequence starts at 1).
func parseFIDInt64(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// parseFIDUUID is the legacy pre-W1 form: the fid was the channel's UUID PK
// (bil24_374_test.go / bil24_390_test.go). Preserved as a fallback so the
// existing unit tests keep passing during the #471 → #476 transition.
// Callers try parseFIDInt64 first; only when both parses fail is the fid
// truly unresolvable.
func parseFIDUUID(raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

// ─────────────────────────────────────────────────────────────────────────────
// channel resolver (spec §5.2 / §7.1 / §7.2)
// ─────────────────────────────────────────────────────────────────────────────

// ChannelLookupQuerier is the narrow read surface used by the auth path to
// resolve a wire `fid` to a sales_channels row. The primary path
// (GetSalesChannelByDisplayNumber, feature #471) supersedes the legacy
// GetSalesChannelByIDGlobal (feature #390); the latter stays on the
// interface so pre-W1 tests that inject a UUID fid keep resolving until
// #476 sunsets it. *gen.Queries satisfies this interface.
type ChannelLookupQuerier interface {
	GetSalesChannelByDisplayNumber(ctx context.Context, displayNumber int64) (gen.SalesChannelRow, error)
	GetSalesChannelByIDGlobal(ctx context.Context, id uuid.UUID) (gen.SalesChannelRow, error)
}

// resolveChannelByFID resolves the wire `fid` to a sales_channels row.
// Returns (channel, ok, wireErr) — when ok=false the caller must NOT touch
// the response writer (writeErr=false) OR the Bil24 error envelope has
// already been written (writeErr=true).
//
// The resolution order is (spec §5.2):
//  1. int64 parse → GetSalesChannelByDisplayNumber (target shape).
//  2. UUID parse → GetSalesChannelByIDGlobal (legacy fallback; #476 removes).
//
// A deleted/missing channel yields resultCode=-4 (Unauthorized) per spec §5.2
// ("канал удалён / не включён / нет хэша → -4"). The upstream `-3` used by
// the pre-#471 code path was reserved for "not found in catalog" — the fid
// credential is authentication surface, so we surface a `-4` for both the
// wrong-fid and disabled-channel branches.
func (h *Handler) resolveChannelByFID(
	ctx context.Context,
	req bil24Request,
) (channel gen.SalesChannelRow, ok bool) {
	if h.channelQ == nil {
		return gen.SalesChannelRow{}, false
	}
	if dn, dnOK := parseFIDInt64(req.FID); dnOK {
		ch, err := h.channelQ.GetSalesChannelByDisplayNumber(ctx, dn)
		if err == nil {
			return ch, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: channel lookup by display_number failed",
				slog.String("command", req.Command),
				slog.String("fid", req.FID),
				slog.Int64("display_number", dn),
				slog.String("error", err.Error()),
			)
		}
		// Fall through and try UUID legacy path so the pre-W1 fixtures
		// (bil24_374/390) that pass a UUID fid still resolve.
	}
	if legacyID, uuOK := parseFIDUUID(req.FID); uuOK {
		ch, err := h.channelQ.GetSalesChannelByIDGlobal(ctx, legacyID)
		if err == nil {
			return ch, true
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: channel lookup by legacy UUID fid failed",
				slog.String("command", req.Command),
				slog.String("fid", req.FID),
				slog.String("error", err.Error()),
			)
		}
	}
	return gen.SalesChannelRow{}, false
}

// authenticateCommand runs the full spec §5 gate for a single incoming
// command: parse fid, resolve the channel, verify the token when
// requireToken=true. When authentication fails the Bil24 error envelope has
// already been written; the caller returns immediately.
//
// When requireToken=false (dev/staging with BIL24_REQUIRE_TOKEN unset) the
// channel is still resolved so read commands can org-scope; a missing / bad
// fid in that mode short-circuits with (nil, false) and the caller falls
// back to the pre-W1 unauthenticated behaviour (needed only until every
// deployment migrates). If the channel lookup surface is nil (unit tests
// that don't wire it) auth is treated as pass-through — matches the
// nil-query precedent of the other command handlers.
func (h *Handler) authenticateCommand(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
) (channel gen.SalesChannelRow, ok bool) {
	// Unit tests that don't wire the channel lookup surface fall through to
	// the pre-W1 branch. Production wiring (bil24_shims.go) always sets
	// channelQ = *gen.Queries.
	if h.channelQ == nil {
		if h.requireToken {
			h.logger.Warn("bil24_compat: requireToken=true but channelQ is nil; rejecting",
				slog.String("command", req.Command),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeUnauthorized,
				"authentication surface unavailable",
			))
			return gen.SalesChannelRow{}, false
		}
		return gen.SalesChannelRow{}, false
	}

	if strings.TrimSpace(req.FID) == "" {
		if !h.requireToken {
			return gen.SalesChannelRow{}, false
		}
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"fid is required",
		))
		return gen.SalesChannelRow{}, false
	}

	ch, resolved := h.resolveChannelByFID(ctx, req)
	if !resolved {
		if !h.requireToken {
			return gen.SalesChannelRow{}, false
		}
		h.logger.Warn("bil24_compat: fid did not resolve to a channel; rejecting",
			slog.String("command", req.Command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"unknown fid or channel disabled",
		))
		return gen.SalesChannelRow{}, false
	}

	if !h.requireToken {
		return ch, true
	}

	gw := parseGatewaySettings(ch.Settings)
	if !gw.Enabled || gw.TokenHash == "" {
		h.logger.Warn("bil24_compat: channel gateway disabled or unconfigured; rejecting",
			slog.String("command", req.Command),
			slog.String("channel_id", ch.ID.String()),
			slog.Int64("display_number", ch.DisplayNumber),
			slog.Bool("enabled", gw.Enabled),
			slog.Bool("has_hash", gw.TokenHash != ""),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"channel is not configured for gateway access",
		))
		return gen.SalesChannelRow{}, false
	}

	if strings.TrimSpace(req.Token) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is missing",
		))
		return gen.SalesChannelRow{}, false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(gw.TokenHash), []byte(req.Token)); err != nil {
		h.logger.Warn("bil24_compat: token validation failed",
			slog.String("command", req.Command),
			slog.String("channel_id", ch.ID.String()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication failed: invalid token",
		))
		return gen.SalesChannelRow{}, false
	}
	return ch, true
}

// enforceSessionOrg verifies that the given session belongs to the channel's
// organization. Returns ok=false and writes the Bil24 error envelope when
// the session belongs to a different org or does not exist. Used by the
// read/hold commands (GET_SEAT_LIST, GET_SCHEMA, RESERVATION, UN_RESERVE)
// to prevent cross-tenant reads and holds through the compat surface
// (spec §5.3, §7.1/7.2).
//
// The lookup path uses ReservationContextQuerier.GetSessionOrgContext when
// available; when the querier is not wired (unit tests) the check is
// skipped so the existing test suites keep passing. Production wiring
// always provides *gen.Queries.
func (h *Handler) enforceSessionOrg(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	sessionID uuid.UUID,
	channelOrgID uuid.UUID,
) bool {
	if h.resDeps.CtxQ == nil {
		return true
	}
	row, err := h.resDeps.CtxQ.GetSessionOrgContext(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "session not found",
			))
			return false
		}
		h.logger.Error("bil24_compat: session org lookup failed",
			slog.String("command", req.Command),
			slog.String("session_id", sessionID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve session",
		))
		return false
	}
	if row.OrgID != channelOrgID {
		h.logger.Warn("bil24_compat: cross-tenant session access rejected",
			slog.String("command", req.Command),
			slog.String("session_id", sessionID.String()),
			slog.String("channel_org", channelOrgID.String()),
			slog.String("session_org", row.OrgID.String()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeNotFound,
			"session not found in this channel's organization",
		))
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Legacy per-command token validators (features #374, #381, #390)
//
// These predate the unified authenticateCommand() path (feature #471) and
// are still used by RESERVATION (reservationContext), UN_RESERVE
// (validateUnReserveToken) and SCAN_TICKET (validateScanTicketToken) where
// the credential resolution needs to look at the settings blob of a
// specific channel derived from the request payload (reservation lookup,
// SCAN_TICKET's fid-only path) rather than the fid-first
// authenticateCommand() flow. Extracted from bil24_compat.go by feature
// #476 so the reservation / scan files stay well under 700 lines.
// ─────────────────────────────────────────────────────────────────────────────

// validateGatewayToken reads gateway_token_hash from the channel's settings
// JSON and compares it against the token in the request using bcrypt.
// Returns true on success; on failure writes the Bil24 error response and
// returns false. Feature #374.
func (h *Handler) validateGatewayToken(
	w http.ResponseWriter,
	req bil24Request,
	settings json.RawMessage,
) bool {
	// Parse the stored gateway_token_hash from the channel settings.
	var cfg struct {
		GatewayTokenHash string `json:"gateway_token_hash"`
	}
	if len(settings) > 0 {
		_ = json.Unmarshal(settings, &cfg) // ignore decode errors — empty cfg means no hash
	}

	if cfg.GatewayTokenHash == "" {
		// No hash configured: this channel has not been set up for gateway
		// access. Reject rather than allow unauthenticated access.
		h.logger.Warn("bil24_compat: gateway_token_hash not configured on channel; rejecting",
			slog.String("command", req.Command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"channel is not configured for gateway access; set gateway_token_hash in channel settings",
		))
		return false
	}

	if strings.TrimSpace(req.Token) == "" {
		h.logger.Warn("bil24_compat: token missing in request",
			slog.String("command", req.Command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication required: token field is missing",
		))
		return false
	}

	if err := bcrypt.CompareHashAndPassword([]byte(cfg.GatewayTokenHash), []byte(req.Token)); err != nil {
		h.logger.Warn("bil24_compat: token validation failed",
			slog.String("command", req.Command),
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"authentication failed: invalid token",
		))
		return false
	}

	return true
}

// validateUnReserveToken validates the token field for UN_RESERVE by resolving
// the reservation's owning sales channel and comparing the supplied token
// against the channel's stored gateway_token_hash (bcrypt). Feature #381,
// PR2-25 variant A.
//
// Precondition: req.Token is non-empty (caller guards).
//
// On failure the Bil24 error envelope has already been written and false is
// returned.
func (h *Handler) validateUnReserveToken(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
	reservationID uuid.UUID,
) bool {
	// Fail closed: cannot validate without the context querier.
	if h.resDeps.CtxQ == nil {
		h.logger.Warn("bil24_compat: UN_RESERVE: requireToken=true but CtxQ is nil; rejecting",
			slog.String("reservation_id", reservationID.String()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			"authentication service unavailable",
		))
		return false
	}

	res, err := h.resDeps.CtxQ.GetReservationByID(ctx, reservationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeNotFound, "reservation not found",
			))
		} else {
			h.logger.Error("bil24_compat: UN_RESERVE: reservation lookup for auth failed",
				slog.String("reservation_id", reservationID.String()),
				slog.String("error", err.Error()),
			)
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeInternalError, "failed to resolve reservation",
			))
		}
		return false
	}

	channel, err := h.resDeps.CtxQ.GetSalesChannelByID(ctx, res.ChannelID, res.OrgID)
	if err != nil {
		h.logger.Error("bil24_compat: UN_RESERVE: channel lookup for auth failed",
			slog.String("reservation_id", reservationID.String()),
			slog.String("channel_id", res.ChannelID.String()),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve sales channel",
		))
		return false
	}

	return h.validateGatewayToken(w, req, channel.Settings)
}

// validateScanTicketToken performs the full SCAN_TICKET credential check
// (feature #390, PR2-32): the fid identifies the sales channel whose
// gateway_token_hash authenticates the caller. Unlike RESERVATION, a scan
// carries no session to derive the org from, so the channel is resolved by
// primary key alone (GetSalesChannelByIDGlobal). On failure the Bil24 error
// envelope has already been written and false is returned.
func (h *Handler) validateScanTicketToken(
	ctx context.Context,
	w http.ResponseWriter,
	req bil24Request,
) bool {
	// Fail closed: cannot validate without the context querier.
	if h.resDeps.CtxQ == nil {
		h.logger.Warn("bil24_compat: SCAN_TICKET: requireToken=true but CtxQ is nil; rejecting",
			slog.String("fid", req.FID),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError,
			"authentication service unavailable",
		))
		return false
	}

	if strings.TrimSpace(req.FID) == "" {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"fid is required for SCAN_TICKET (sales channel credential)",
		))
		return false
	}
	// Feature #471 (W1-A1b, spec §5.2): prefer the int64 display_number
	// path via the channel-lookup surface; fall back to the legacy UUID
	// resolution so pre-W1 SCAN_TICKET fixtures keep resolving.
	var (
		channel gen.SalesChannelRow
		err     error
	)
	if dn, dnOK := parseFIDInt64(req.FID); dnOK && h.channelQ != nil {
		ch, lookupErr := h.channelQ.GetSalesChannelByDisplayNumber(ctx, dn)
		if lookupErr == nil {
			return h.validateGatewayToken(w, req, ch.Settings)
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			h.logger.Error("bil24_compat: SCAN_TICKET: display_number lookup failed",
				slog.String("fid", req.FID),
				slog.Int64("display_number", dn),
				slog.String("error", lookupErr.Error()),
			)
		}
	}
	chID, err := TranslateLegacyID(req.FID)
	if err != nil {
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeUnauthorized,
			"fid must be a valid sales channel identifier",
		))
		return false
	}

	channel, err = h.resDeps.CtxQ.GetSalesChannelByIDGlobal(ctx, chID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeBil24JSON(w, http.StatusOK, bil24Error(
				req.Command, ResultCodeUnauthorized,
				"sales channel not found for fid",
			))
			return false
		}
		h.logger.Error("bil24_compat: SCAN_TICKET: channel lookup for auth failed",
			slog.String("fid", req.FID),
			slog.String("error", err.Error()),
		)
		writeBil24JSON(w, http.StatusOK, bil24Error(
			req.Command, ResultCodeInternalError, "failed to resolve sales channel",
		))
		return false
	}

	return h.validateGatewayToken(w, req, channel.Settings)
}
