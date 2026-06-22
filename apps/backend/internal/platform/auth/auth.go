// Package auth implements the AuthBoundary placeholder used by the foundation
// milestone.
//
// The exported surface is split into three logical pieces:
//
//   - Actor — the value attached to a request context after successful
//     authentication. The real identity module (delivered in a later
//     milestone) will replace the StubProvider implementation but will reuse
//     this struct so call sites do not have to change.
//   - StubProvider — a dev-only HS256 JWT issuer/verifier. It mints tokens
//     signed by a shared secret (JWT_SIGNING_SECRET) and validates them on
//     incoming requests. Production swaps the StubProvider for an RS256
//     verifier wired to a real IdP; the Provider interface is the boundary.
//   - Middleware — chi/net-http middleware that extracts the Authorization
//     header, calls Provider.Verify, attaches the resulting Actor to the
//     request context, and emits the standard 401/403 error envelope on
//     failure.
//
// All public APIs are safe for concurrent use after construction.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// HeaderWWWAuthenticate is the standard challenge header for 401 responses.
// We always answer with the Bearer scheme and a fixed realm so clients can
// distinguish protected endpoints from accidentally-public ones.
const (
	HeaderWWWAuthenticate = "WWW-Authenticate"
	WWWAuthenticateBearer = `Bearer realm="arena"`
)

// -----------------------------------------------------------------------------
// Actor
// -----------------------------------------------------------------------------

// ActorType enumerates the broad kinds of authenticated principals the
// platform supports. Only "stub_user" is used in this milestone; the values
// match the strings stored in audit_events.actor_type so analytics queries
// stay stable when the real IdP lands.
type ActorType string

const (
	ActorTypeStubUser ActorType = "stub_user"
	ActorTypeUser     ActorType = "user"
	ActorTypeService  ActorType = "service"
	ActorTypeAnon     ActorType = "anonymous"
)

// Actor is the authenticated principal attached to a request context.
// All fields are immutable after construction.
type Actor struct {
	// ID is a UUID (any version) identifying the principal. Empty for
	// anonymous actors.
	ID string
	// Type discriminates the principal kind (stub_user / user / service / …).
	Type ActorType
	// Roles is an optional set of coarse-grained role labels; the real
	// PermissionBoundary uses these to make allow/deny decisions.
	Roles []string
	// Issuer is the "iss" claim from the validated JWT (or "" for anonymous).
	Issuer string
	// ExpiresAt is the "exp" claim (zero for anonymous).
	ExpiresAt time.Time
	// IssuedAt is the "iat" claim (zero for anonymous).
	IssuedAt time.Time
	// RawToken is the original token string. Kept for downstream forwarding
	// (e.g. service-to-service propagation) but never logged.
	RawToken string
}

// IsAuthenticated reports whether the actor has a non-empty ID and a
// recognised type other than anonymous.
func (a Actor) IsAuthenticated() bool {
	return a.ID != "" && a.Type != "" && a.Type != ActorTypeAnon
}

// -----------------------------------------------------------------------------
// Context plumbing
// -----------------------------------------------------------------------------

type ctxKey int

const actorKey ctxKey = iota

// WithActor returns a context carrying the supplied actor.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey, a)
}

// ActorFromContext returns the actor previously stored via WithActor.
// The second return value is false when the request was never authenticated.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	a, ok := ctx.Value(actorKey).(Actor)
	return a, ok
}

// -----------------------------------------------------------------------------
// Provider interface (the AuthBoundary)
// -----------------------------------------------------------------------------

// Provider is the contract every IdP adapter implements. The stub
// implementation in this package can be replaced by an RS256 verifier wired
// to a real IdP without touching the middleware or any call site.
type Provider interface {
	// Verify validates the supplied bearer token and returns the resulting
	// actor on success.
	Verify(ctx context.Context, token string) (Actor, error)
}

// TokenMinter is implemented by providers that can also issue tokens
// (development helpers, internal service-to-service token mints). The real
// production IdP will likely NOT implement this — it is delegated to the
// IdP itself.
type TokenMinter interface {
	IssueToken(ctx context.Context, req IssueRequest) (string, time.Time, error)
}

// IssueRequest carries the parameters for IssueToken. Zero-valued fields fall
// back to provider defaults (configured Issuer, configured TTL, ActorTypeStubUser).
type IssueRequest struct {
	ActorID   string
	ActorType ActorType
	Roles     []string
	Audience  string
	TTL       time.Duration
}

// -----------------------------------------------------------------------------
// Errors
// -----------------------------------------------------------------------------

// Sentinel errors so callers can distinguish "missing header" from "expired"
// from "bad signature" without string matching.
var (
	ErrMissingToken    = errors.New("auth: missing bearer token")
	ErrMalformedToken  = errors.New("auth: malformed token")
	ErrInvalidSignature = errors.New("auth: invalid signature")
	ErrTokenExpired    = errors.New("auth: token expired")
	ErrUnknownIssuer   = errors.New("auth: unknown issuer")
	ErrUnknownAudience = errors.New("auth: unknown audience")
	ErrUnsupportedAlg  = errors.New("auth: unsupported jwt algorithm")
	ErrDisabled        = errors.New("auth: stub provider disabled (set ENABLE_DEV_AUTH=true)")
)

// -----------------------------------------------------------------------------
// StubProvider — HS256 token mint + verify
// -----------------------------------------------------------------------------

// StubProvider is a development-only Provider+TokenMinter that signs and
// verifies HS256 JWTs with a shared secret. It MUST NOT be deployed to
// production (config.Validate enforces this).
//
// The token shape is a standard 3-segment JWT:
//
//	header.payload.signature
//
// header  = {"alg":"HS256","typ":"JWT"}
// payload = {"sub","iss","aud","iat","exp","actor_type","roles"}
// signature = HMAC-SHA256(secret, "header.payload"), base64-url, no padding
type StubProvider struct {
	secret     []byte
	issuer     string
	audience   string
	defaultTTL time.Duration
	enabled    bool
	now        func() time.Time // injectable for tests; defaults to time.Now
}

// StubConfig configures a new StubProvider.
type StubConfig struct {
	Secret     string
	Issuer     string
	Audience   string
	DefaultTTL time.Duration
	Enabled    bool
}

// NewStubProvider constructs a StubProvider. Returns an error when Enabled is
// true but Secret is empty (mirrors config.Validate so callers fail fast).
func NewStubProvider(cfg StubConfig) (*StubProvider, error) {
	if cfg.Enabled && strings.TrimSpace(cfg.Secret) == "" {
		return nil, errors.New("auth: StubProvider requires a non-empty secret when enabled")
	}
	if cfg.DefaultTTL <= 0 {
		cfg.DefaultTTL = time.Hour
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "arena-dev"
	}
	if cfg.Audience == "" {
		cfg.Audience = "arena-api"
	}
	return &StubProvider{
		secret:     []byte(cfg.Secret),
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		defaultTTL: cfg.DefaultTTL,
		enabled:    cfg.Enabled,
		now:        time.Now,
	}, nil
}

// Enabled reports whether the provider will accept or mint tokens.
func (p *StubProvider) Enabled() bool { return p.enabled }

// Issuer returns the configured "iss" claim value.
func (p *StubProvider) Issuer() string { return p.issuer }

// Audience returns the configured "aud" claim value.
func (p *StubProvider) Audience() string { return p.audience }

// IssueToken mints an HS256 JWT for the supplied request. Returns the token
// string and its absolute expiry time.
func (p *StubProvider) IssueToken(_ context.Context, req IssueRequest) (string, time.Time, error) {
	if !p.enabled {
		return "", time.Time{}, ErrDisabled
	}
	if strings.TrimSpace(req.ActorID) == "" {
		return "", time.Time{}, errors.New("auth: IssueToken requires ActorID")
	}
	if req.ActorType == "" {
		req.ActorType = ActorTypeStubUser
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = p.defaultTTL
	}
	aud := req.Audience
	if aud == "" {
		aud = p.audience
	}

	now := p.now().UTC()
	exp := now.Add(ttl)

	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	payload := jwtClaims{
		Sub:       req.ActorID,
		Iss:       p.issuer,
		Aud:       aud,
		Iat:       now.Unix(),
		Exp:       exp.Unix(),
		ActorType: string(req.ActorType),
		Roles:     req.Roles,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: marshal payload: %w", err)
	}

	h := b64encode(headerJSON)
	pp := b64encode(payloadJSON)
	signingInput := h + "." + pp
	sig := signHS256(p.secret, []byte(signingInput))
	token := signingInput + "." + b64encode(sig)
	return token, exp, nil
}

// Verify validates the supplied bearer token. On success returns an Actor
// populated from the JWT claims. Returns one of the sentinel errors above
// on failure so middleware can map to the right HTTP status.
func (p *StubProvider) Verify(_ context.Context, token string) (Actor, error) {
	if !p.enabled {
		return Actor{}, ErrDisabled
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Actor{}, ErrMalformedToken
	}
	headerBytes, err := b64decode(parts[0])
	if err != nil {
		return Actor{}, fmt.Errorf("%w: header b64: %v", ErrMalformedToken, err)
	}
	payloadBytes, err := b64decode(parts[1])
	if err != nil {
		return Actor{}, fmt.Errorf("%w: payload b64: %v", ErrMalformedToken, err)
	}
	sigBytes, err := b64decode(parts[2])
	if err != nil {
		return Actor{}, fmt.Errorf("%w: signature b64: %v", ErrMalformedToken, err)
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Actor{}, fmt.Errorf("%w: header json: %v", ErrMalformedToken, err)
	}
	if header.Alg != "HS256" {
		return Actor{}, fmt.Errorf("%w: %q", ErrUnsupportedAlg, header.Alg)
	}

	expected := signHS256(p.secret, []byte(parts[0]+"."+parts[1]))
	if !hmac.Equal(expected, sigBytes) {
		return Actor{}, ErrInvalidSignature
	}

	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return Actor{}, fmt.Errorf("%w: payload json: %v", ErrMalformedToken, err)
	}

	if claims.Iss != "" && claims.Iss != p.issuer {
		return Actor{}, fmt.Errorf("%w: %q", ErrUnknownIssuer, claims.Iss)
	}
	if claims.Aud != "" && claims.Aud != p.audience {
		return Actor{}, fmt.Errorf("%w: %q", ErrUnknownAudience, claims.Aud)
	}
	now := p.now().UTC()
	if claims.Exp > 0 && now.Unix() >= claims.Exp {
		return Actor{}, ErrTokenExpired
	}

	actorType := ActorType(claims.ActorType)
	if actorType == "" {
		actorType = ActorTypeStubUser
	}

	return Actor{
		ID:        claims.Sub,
		Type:      actorType,
		Roles:     claims.Roles,
		Issuer:    claims.Iss,
		ExpiresAt: time.Unix(claims.Exp, 0).UTC(),
		IssuedAt:  time.Unix(claims.Iat, 0).UTC(),
		RawToken:  token,
	}, nil
}

// jwtClaims is the internal payload shape for stub JWTs.
type jwtClaims struct {
	Sub       string   `json:"sub"`
	Iss       string   `json:"iss"`
	Aud       string   `json:"aud,omitempty"`
	Iat       int64    `json:"iat"`
	Exp       int64    `json:"exp"`
	ActorType string   `json:"actor_type,omitempty"`
	Roles     []string `json:"roles,omitempty"`
}

func signHS256(secret, msg []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(msg)
	return mac.Sum(nil)
}

func b64encode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// MiddlewareOptions tunes the auth middleware.
type MiddlewareOptions struct {
	// Optional. Returns true if a request path should bypass auth entirely
	// (e.g. /healthz, /readyz, /metrics, /v1/info, /v1/dev/token). When nil,
	// every request inside the wrapped subtree must authenticate.
	Skip func(*http.Request) bool
}

// Middleware returns net/http middleware that verifies the Authorization
// Bearer header on every request that is NOT excluded by opts.Skip.
//
// Successful auth puts the Actor on the request context (use ActorFromContext
// to retrieve it downstream). Failures emit a uniform error envelope shaped
// per app_spec.txt §api_response_envelope:
//
//	{"error": {"code": "<dotted.code>", "message": "<localized>",
//	           "request_id": "...", "trace_id": "..."}}
//
// 401 responses additionally carry a `WWW-Authenticate: Bearer realm="arena"`
// header so generic HTTP clients (curl --user, browsers, OpenAPI clients)
// detect the challenge correctly.
func Middleware(p Provider, opts MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts.Skip != nil && opts.Skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			rawToken, err := bearerFromHeader(r)
			if err != nil {
				// All three "no usable bearer token presented" cases
				// (missing header, wrong scheme, empty bearer value)
				// collapse into auth.missing_token per feature spec — the
				// caller did not actually present a bearer token.
				code := "auth.missing_token"
				if errors.Is(err, ErrMalformedToken) {
					// Truly malformed token (e.g. "Bearer not.a.jwt"
					// fails parsing inside Verify, not here). Treat
					// pre-verify malformations the same way so the
					// authentication challenge stays uniform.
					code = "auth.missing_token"
				}
				writeAuthError(w, r, http.StatusUnauthorized, code, err)
				return
			}
			actor, err := p.Verify(r.Context(), rawToken)
			if err != nil {
				status, code := mapAuthErrorToStatus(err)
				writeAuthError(w, r, status, code, err)
				return
			}
			ctx := WithActor(r.Context(), actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerFromHeader extracts the bearer token from an Authorization header.
//
// Returns ErrMissingToken when:
//
//   - the Authorization header is absent or contains only whitespace,
//   - the scheme is not "Bearer" (e.g. Basic, Digest, custom),
//   - the scheme is "Bearer" but no token value follows the space.
//
// All three cases are treated the same per feature #6 — the caller did not
// present a usable bearer token. Later phases (signature/expiry/issuer
// validation) live inside Provider.Verify and surface their own sentinels.
func bearerFromHeader(r *http.Request) (string, error) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", fmt.Errorf("%w: Authorization header is absent", ErrMissingToken)
	}
	// Split scheme from credentials. Anything other than "Bearer" (case
	// insensitive) is treated as no bearer token presented — RFC 7235 allows
	// multiple challenges, but for this milestone the only supported scheme
	// is Bearer.
	parts := strings.SplitN(h, " ", 2)
	scheme := strings.ToLower(strings.TrimSpace(parts[0]))
	if scheme != "bearer" {
		return "", fmt.Errorf("%w: Authorization scheme %q is not supported, expected 'Bearer'", ErrMissingToken, parts[0])
	}
	if len(parts) < 2 {
		return "", fmt.Errorf("%w: Authorization header is missing the bearer token value", ErrMissingToken)
	}
	tok := strings.TrimSpace(parts[1])
	if tok == "" {
		return "", fmt.Errorf("%w: bearer token value is empty", ErrMissingToken)
	}
	return tok, nil
}

// mapAuthErrorToStatus converts an internal sentinel into HTTP status + a
// machine-readable, dotted error code suitable for the uniform error envelope.
func mapAuthErrorToStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrMissingToken):
		return http.StatusUnauthorized, "auth.missing_token"
	case errors.Is(err, ErrMalformedToken):
		return http.StatusUnauthorized, "auth.malformed_token"
	case errors.Is(err, ErrInvalidSignature):
		return http.StatusUnauthorized, "auth.invalid_signature"
	case errors.Is(err, ErrTokenExpired):
		return http.StatusUnauthorized, "auth.token_expired"
	case errors.Is(err, ErrUnknownIssuer):
		return http.StatusUnauthorized, "auth.unknown_issuer"
	case errors.Is(err, ErrUnknownAudience):
		return http.StatusUnauthorized, "auth.unknown_audience"
	case errors.Is(err, ErrUnsupportedAlg):
		return http.StatusUnauthorized, "auth.unsupported_alg"
	case errors.Is(err, ErrDisabled):
		return http.StatusServiceUnavailable, "auth.disabled"
	default:
		return http.StatusUnauthorized, "auth.invalid_token"
	}
}

// writeAuthError renders the uniform error envelope, attaches the bearer
// challenge header on 401 responses, and resolves request_id / trace_id from
// the chi context so the response body matches the response headers.
//
// The message is translated using the Accept-Language header (English and
// Russian are wired in this milestone). The English string serves as both
// the EN translation and the developer-facing diagnostic embedded in
// `details` so non-localized debugging is still possible.
func writeAuthError(w http.ResponseWriter, r *http.Request, status int, code string, cause error) {
	requestID := resolveRequestID(r, w)
	traceID := logging.TraceID(r.Context())

	if status == http.StatusUnauthorized {
		w.Header().Set(HeaderWWWAuthenticate, WWWAuthenticateBearer)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	locale := negotiateLocale(r.Header.Get("Accept-Language"))
	message := translateAuthCode(code, locale)

	body := map[string]any{
		"error": map[string]any{
			"code":       code,
			"message":    message,
			"request_id": requestID,
			"trace_id":   traceID,
		},
	}
	if cause != nil {
		body["error"].(map[string]any)["details"] = map[string]any{
			"reason": cause.Error(),
		}
	}
	_ = json.NewEncoder(w).Encode(body)
}

// resolveRequestID returns the request identifier in this priority order:
//  1. The response header X-Request-Id (set by upstream middleware).
//  2. The chi RequestID stored on the request context.
//  3. The incoming X-Request-Id header (in case the client asserted one).
//
// Empty string is returned only when none of the three sources has a value.
func resolveRequestID(r *http.Request, w http.ResponseWriter) string {
	if w != nil {
		if v := strings.TrimSpace(w.Header().Get("X-Request-Id")); v != "" {
			return v
		}
	}
	if id := chimw.GetReqID(r.Context()); id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get("X-Request-Id"))
}

// negotiateLocale picks the most-preferred supported locale from the
// Accept-Language header. The supported set for this milestone is en and ru;
// the default is en. Quality factors and complex tags are honoured at the
// "language" granularity only (e.g. "ru-RU" maps to "ru").
func negotiateLocale(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return "en"
	}
	// Parse "lang;q=0.9, lang2;q=0.8" — we don't need full RFC 7231 here.
	best := ""
	bestQ := -1.0
	for _, raw := range strings.Split(header, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		segs := strings.Split(raw, ";")
		tag := strings.ToLower(strings.TrimSpace(segs[0]))
		// Reduce to primary language subtag.
		if dash := strings.Index(tag, "-"); dash > 0 {
			tag = tag[:dash]
		}
		q := 1.0
		for _, p := range segs[1:] {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "q=") {
				if v, err := parseQuality(p[2:]); err == nil {
					q = v
				}
			}
		}
		if tag != "en" && tag != "ru" {
			continue
		}
		if q > bestQ {
			best = tag
			bestQ = q
		}
	}
	if best == "" {
		return "en"
	}
	return best
}

func parseQuality(s string) (float64, error) {
	// Accept-Language quality factors are in [0,1] with at most 3 decimals.
	// strconv would pull in another import for one call site; do it inline.
	var whole, frac int
	var scale = 1
	dot := strings.Index(s, ".")
	if dot < 0 {
		// "q=1" or "q=0"
		switch s {
		case "0":
			return 0, nil
		case "1":
			return 1, nil
		}
		return 0, fmt.Errorf("invalid quality %q", s)
	}
	for _, r := range s[:dot] {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid quality %q", s)
		}
		whole = whole*10 + int(r-'0')
	}
	for _, r := range s[dot+1:] {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid quality %q", s)
		}
		frac = frac*10 + int(r-'0')
		scale *= 10
	}
	return float64(whole) + float64(frac)/float64(scale), nil
}

// translateAuthCode resolves the localized human-readable message for the
// given error code. The map below is the inline message catalog for this
// milestone; later milestones will replace it with go-i18n/v2 TOML catalogs
// (the lookup signature stays the same).
func translateAuthCode(code, locale string) string {
	if msgs, ok := authMessages[code]; ok {
		if m, ok := msgs[locale]; ok {
			return m
		}
		if m, ok := msgs["en"]; ok {
			return m
		}
	}
	// Fallback so we never return an empty message — the code is at least
	// informative even if the catalog is missing an entry.
	return code
}

// authMessages is the inline localization table for auth-domain error codes.
// Keys are dotted codes; values are locale -> human message.
var authMessages = map[string]map[string]string{
	"auth.missing_token": {
		"en": "Authentication required: provide an Authorization header with a Bearer token.",
		"ru": "Требуется аутентификация: укажите заголовок Authorization со схемой Bearer.",
	},
	"auth.malformed_token": {
		"en": "The provided bearer token is malformed.",
		"ru": "Предоставленный токен Bearer имеет некорректный формат.",
	},
	"auth.invalid_signature": {
		"en": "The bearer token signature is invalid.",
		"ru": "Подпись токена Bearer недействительна.",
	},
	"auth.token_expired": {
		"en": "The bearer token has expired; request a fresh one.",
		"ru": "Срок действия токена Bearer истёк, получите новый токен.",
	},
	"auth.unknown_issuer": {
		"en": "The bearer token was issued by an unknown issuer.",
		"ru": "Токен Bearer выпущен неизвестным эмитентом.",
	},
	"auth.unknown_audience": {
		"en": "The bearer token is not intended for this audience.",
		"ru": "Токен Bearer выпущен не для этой аудитории.",
	},
	"auth.unsupported_alg": {
		"en": "The bearer token uses an unsupported signing algorithm.",
		"ru": "Токен Bearer использует неподдерживаемый алгоритм подписи.",
	},
	"auth.invalid_token": {
		"en": "The bearer token is invalid.",
		"ru": "Токен Bearer недействителен.",
	},
	"auth.disabled": {
		"en": "Authentication is disabled for this deployment.",
		"ru": "Аутентификация отключена в этой среде.",
	},
}
