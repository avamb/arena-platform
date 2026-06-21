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
// to retrieve it downstream). Failures emit a uniform error envelope:
//
//	{"error": {"code": "<code>", "message": "<msg>", "request_id": "..."}}
func Middleware(p Provider, opts MiddlewareOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if opts.Skip != nil && opts.Skip(r) {
				next.ServeHTTP(w, r)
				return
			}
			rawToken, err := bearerFromHeader(r)
			if err != nil {
				writeAuthError(w, r, http.StatusUnauthorized, "auth_missing_token", err.Error())
				return
			}
			actor, err := p.Verify(r.Context(), rawToken)
			if err != nil {
				status, code := mapAuthErrorToStatus(err)
				writeAuthError(w, r, status, code, err.Error())
				return
			}
			ctx := WithActor(r.Context(), actor)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerFromHeader(r *http.Request) (string, error) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return "", ErrMissingToken
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) && !strings.HasPrefix(h, strings.ToLower(prefix)) {
		return "", fmt.Errorf("%w: expected 'Bearer <token>'", ErrMalformedToken)
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", ErrMissingToken
	}
	return tok, nil
}

// mapAuthErrorToStatus converts an internal sentinel into HTTP status + a
// machine-readable error code that fits the uniform error envelope.
func mapAuthErrorToStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrMissingToken):
		return http.StatusUnauthorized, "auth_missing_token"
	case errors.Is(err, ErrMalformedToken):
		return http.StatusUnauthorized, "auth_malformed_token"
	case errors.Is(err, ErrInvalidSignature):
		return http.StatusUnauthorized, "auth_invalid_signature"
	case errors.Is(err, ErrTokenExpired):
		return http.StatusUnauthorized, "auth_token_expired"
	case errors.Is(err, ErrUnknownIssuer):
		return http.StatusUnauthorized, "auth_unknown_issuer"
	case errors.Is(err, ErrUnknownAudience):
		return http.StatusUnauthorized, "auth_unknown_audience"
	case errors.Is(err, ErrUnsupportedAlg):
		return http.StatusUnauthorized, "auth_unsupported_alg"
	case errors.Is(err, ErrDisabled):
		return http.StatusServiceUnavailable, "auth_disabled"
	default:
		return http.StatusUnauthorized, "auth_invalid_token"
	}
}

func writeAuthError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	payload := map[string]any{
		"error": map[string]any{
			"code":       code,
			"message":    msg,
			"request_id": r.Header.Get("X-Request-Id"),
		},
	}
	_ = json.NewEncoder(w).Encode(payload)
}
