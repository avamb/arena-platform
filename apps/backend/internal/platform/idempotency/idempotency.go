// Package idempotency implements the PostgreSQL-backed Idempotency-Key
// middleware described in app_spec.txt §api / §boundaries.
//
// Flow on every mutating request that the middleware wraps:
//
//  1. Read Idempotency-Key header. Missing → 400.
//  2. Compute SHA-256 hash of the canonicalised request (method + path +
//     body). This is recorded with the stored response so a *different*
//     request reusing the same key can be rejected with 409.
//  3. SELECT response_status, response_body, request_hash FROM idempotency_keys
//     WHERE key=$1 AND scope=$2 AND expires_at > now().
//     - HIT with matching hash      → replay the stored response (status+body)
//                                     and short-circuit downstream handlers.
//     - HIT with mismatching hash   → 409 (key reused with different payload).
//     - MISS                        → invoke downstream handler with a
//                                     captured ResponseWriter, then INSERT
//                                     the captured (status, body) row.
//
// The middleware is intentionally agnostic about how the row is INSERTED in
// the success path: callers either let the middleware persist a "best-effort"
// row after the handler returns (default), or they call SaveTx from inside
// their own transaction to persist the idempotency row atomically with any
// business-domain writes (recommended for /v1/echo).
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HeaderName is the canonical HTTP header used to carry the idempotency key.
const HeaderName = "Idempotency-Key"

// MaxKeyLength matches IDEMPOTENCY_KEY_MAX_LENGTH from .env.example. Keys
// longer than this are rejected with 400 so a malicious client cannot stuff
// arbitrary blobs into the database.
const MaxKeyLength = 255

// Sentinel errors so handlers can distinguish "no key" from "key reused".
var (
	ErrMissingKey = errors.New("idempotency: missing Idempotency-Key header")
	ErrKeyTooLong = fmt.Errorf("idempotency: key exceeds %d bytes", MaxKeyLength)
	ErrConflict   = errors.New("idempotency: key reused with different request body")
	ErrInternalDB = errors.New("idempotency: database error")
)

// StoredResponse is the cached HTTP response replayed on a duplicate request.
type StoredResponse struct {
	Status      int
	ContentType string
	Body        []byte
	RequestHash string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Store is the contract the middleware needs from the persistence layer.
// The PG implementation below satisfies it; tests can stub it.
type Store interface {
	// Lookup returns (resp, true, nil) on a HIT, (zero, false, nil) on a
	// MISS, and (zero, false, err) on database failure.
	Lookup(ctx context.Context, key, scope string) (StoredResponse, bool, error)

	// Save persists the response under the given key+scope. ON CONFLICT it
	// becomes a no-op so the second writer in a race silently succeeds.
	Save(ctx context.Context, key, scope, actorID string, resp StoredResponse) error
}

// -----------------------------------------------------------------------------
// PostgreSQL store
// -----------------------------------------------------------------------------

// PGStore is the production Store backed by the idempotency_keys table.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore constructs a PGStore around a live pgx pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// Lookup implements Store.Lookup.
func (s *PGStore) Lookup(ctx context.Context, key, scope string) (StoredResponse, bool, error) {
	const q = `
		SELECT response_status, response_body, request_hash, created_at, expires_at
		  FROM idempotency_keys
		 WHERE key = $1
		   AND scope = $2
		   AND expires_at > now()
		 LIMIT 1
	`
	var (
		status    int
		bodyJSON  []byte
		reqHash   string
		createdAt time.Time
		expiresAt time.Time
	)
	row := s.pool.QueryRow(ctx, q, key, scope)
	err := row.Scan(&status, &bodyJSON, &reqHash, &createdAt, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredResponse{}, false, nil
		}
		return StoredResponse{}, false, fmt.Errorf("%w: %v", ErrInternalDB, err)
	}

	resp := StoredResponse{
		Status:      status,
		ContentType: "application/json; charset=utf-8",
		Body:        bodyJSON,
		RequestHash: reqHash,
		CreatedAt:   createdAt,
		ExpiresAt:   expiresAt,
	}
	return resp, true, nil
}

// Save implements Store.Save.
func (s *PGStore) Save(ctx context.Context, key, scope, actorID string, resp StoredResponse) error {
	const q = `
		INSERT INTO idempotency_keys
		    (key, scope, actor_id, request_hash, response_status, response_body, created_at, expires_at)
		VALUES
		    ($1, $2, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8)
		ON CONFLICT (key, scope) DO NOTHING
	`
	if resp.Body == nil {
		resp.Body = []byte("{}")
	}
	if !json.Valid(resp.Body) {
		wrapped, _ := json.Marshal(map[string]string{"raw": string(resp.Body)})
		resp.Body = wrapped
	}
	if resp.CreatedAt.IsZero() {
		resp.CreatedAt = time.Now().UTC()
	}
	if resp.ExpiresAt.IsZero() {
		resp.ExpiresAt = resp.CreatedAt.Add(24 * time.Hour)
	}
	tag, err := s.pool.Exec(ctx, q,
		key, scope, actorID, resp.RequestHash,
		resp.Status, resp.Body, resp.CreatedAt, resp.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInternalDB, err)
	}
	_ = tag
	return nil
}

// -----------------------------------------------------------------------------
// Middleware
// -----------------------------------------------------------------------------

// Options tunes the middleware.
type Options struct {
	// Scope is the value stored in idempotency_keys.scope. Typically the
	// endpoint name, e.g. "POST /v1/echo". Required.
	Scope string
	// TTL is how long stored responses are honoured for. Defaults to 24h.
	TTL time.Duration
	// ActorID is a function that extracts the actor UUID (or "" for anon)
	// from the request context. Defaults to "" (no actor).
	ActorID func(ctx context.Context) string
	// OnReplay is an optional hook invoked exactly once when the middleware
	// short-circuits a duplicate request by replaying a stored response.
	// It is NOT called when the key is missing, malformed, or reused with a
	// different body (those paths are errors, not replays). Typical use: increment
	// a Prometheus counter (e.g. observability.Metrics.IdempotencyReplaysTotal).
	OnReplay func()
}

// Middleware returns net/http middleware enforcing Idempotency-Key semantics
// against the supplied Store. The middleware:
//
//   - rejects requests missing or with malformed Idempotency-Key header,
//   - replays stored responses on a HIT,
//   - returns 409 when the same key is reused with a different request body,
//   - otherwise invokes downstream handler, captures the response, and best-
//     effort INSERTs it after handler returns.
//
// Routes that need to persist the idempotency row atomically with their
// business writes should call SaveTx from inside their own pgx.Tx; SaveTx
// marks the response so the middleware skips its own auto-save.
func Middleware(store Store, opts Options) func(http.Handler) http.Handler {
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.Scope == "" {
		opts.Scope = "default"
	}
	if opts.ActorID == nil {
		opts.ActorID = func(context.Context) string { return "" }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimSpace(r.Header.Get(HeaderName))
			if key == "" {
				writeIdempError(w, r, http.StatusBadRequest, "idempotency_missing_key", ErrMissingKey.Error())
				return
			}
			if len(key) > MaxKeyLength {
				writeIdempError(w, r, http.StatusBadRequest, "idempotency_key_too_long", ErrKeyTooLong.Error())
				return
			}

			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				writeIdempError(w, r, http.StatusBadRequest, "idempotency_read_body", err.Error())
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			reqHash := hashRequest(r.Method, r.URL.Path, bodyBytes)

			stored, hit, err := store.Lookup(r.Context(), key, opts.Scope)
			if err != nil {
				writeIdempError(w, r, http.StatusInternalServerError, "idempotency_lookup_failed", err.Error())
				return
			}
			if hit {
				if stored.RequestHash != "" && stored.RequestHash != reqHash {
					logging.FromContext(r.Context()).Warn("idempotency: body mismatch detected",
						"code", "idempotency.body_mismatch",
						"scope", opts.Scope,
						"key", key,
					)
					writeIdempError(w, r, http.StatusConflict, "idempotency.body_mismatch", ErrConflict.Error())
					return
				}
				if opts.OnReplay != nil {
					opts.OnReplay()
				}
				replayStored(w, stored)
				return
			}

			rec := &capturingWriter{ResponseWriter: w, status: http.StatusOK, body: &bytes.Buffer{}}
			ctx := contextWithMiddlewareState(r.Context(), &state{
				key:     key,
				scope:   opts.Scope,
				ttl:     opts.TTL,
				reqHash: reqHash,
				actor:   opts.ActorID(r.Context()),
				store:   store,
			})
			next.ServeHTTP(rec, r.WithContext(ctx))

			if rec.Header().Get(persistedHeader) == "true" {
				rec.Header().Del(persistedHeader)
				return
			}
			if rec.status < 200 || rec.status >= 300 {
				return
			}
			now := time.Now().UTC()
			saveErr := store.Save(r.Context(), key, opts.Scope, opts.ActorID(r.Context()), StoredResponse{
				Status:      rec.status,
				ContentType: rec.Header().Get("Content-Type"),
				Body:        rec.body.Bytes(),
				RequestHash: reqHash,
				CreatedAt:   now,
				ExpiresAt:   now.Add(opts.TTL),
			})
			if saveErr != nil {
				rec.Header().Set("X-Idempotency-Save-Error", saveErr.Error())
			}
		})
	}
}

func hashRequest(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(strings.ToUpper(method)))
	h.Write([]byte{'\n'})
	h.Write([]byte(path))
	h.Write([]byte{'\n'})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func replayStored(w http.ResponseWriter, stored StoredResponse) {
	ct := stored.ContentType
	if ct == "" {
		ct = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Idempotent-Replay", "true")
	w.WriteHeader(stored.Status)
	_, _ = w.Write(stored.Body)
}

func writeIdempError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
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

// -----------------------------------------------------------------------------
// capturingWriter — wraps http.ResponseWriter to capture status + body
// -----------------------------------------------------------------------------

type capturingWriter struct {
	http.ResponseWriter
	status      int
	body        *bytes.Buffer
	wroteHeader bool
}

func (c *capturingWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.status = status
	c.wroteHeader = true
	c.ResponseWriter.WriteHeader(status)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	c.body.Write(b)
	return c.ResponseWriter.Write(b)
}

// -----------------------------------------------------------------------------
// Context helpers for handlers that want to persist atomically inside a tx
// -----------------------------------------------------------------------------

const persistedHeader = "X-Idempotency-Persisted"

type state struct {
	key     string
	scope   string
	ttl     time.Duration
	reqHash string
	actor   string
	store   Store
}

type ctxKey int

const stateKey ctxKey = iota

func contextWithMiddlewareState(ctx context.Context, s *state) context.Context {
	return context.WithValue(ctx, stateKey, s)
}

// FromContext returns the middleware-stored key/scope/hash for the current
// request, or zero values when the middleware is not on the chain.
func FromContext(ctx context.Context) (key, scope, reqHash string, ttl time.Duration, ok bool) {
	if ctx == nil {
		return "", "", "", 0, false
	}
	s, ok := ctx.Value(stateKey).(*state)
	if !ok || s == nil {
		return "", "", "", 0, false
	}
	return s.key, s.scope, s.reqHash, s.ttl, true
}

// SaveTx persists the idempotency row inside an existing pgx.Tx. Handlers
// that perform other DB writes (audit, outbox) inside the same transaction
// should call this rather than letting the middleware auto-save, so the
// idempotency row is rolled back together with everything else on failure.
func SaveTx(
	ctx context.Context,
	tx pgx.Tx,
	w http.ResponseWriter,
	resp StoredResponse,
) error {
	key, scope, reqHash, ttl, ok := FromContext(ctx)
	if !ok {
		return errors.New("idempotency: middleware state missing from context")
	}
	if resp.RequestHash == "" {
		resp.RequestHash = reqHash
	}
	if resp.CreatedAt.IsZero() {
		resp.CreatedAt = time.Now().UTC()
	}
	if resp.ExpiresAt.IsZero() {
		resp.ExpiresAt = resp.CreatedAt.Add(ttl)
	}
	if resp.Body == nil {
		resp.Body = []byte("{}")
	}
	if !json.Valid(resp.Body) {
		wrapped, _ := json.Marshal(map[string]string{"raw": string(resp.Body)})
		resp.Body = wrapped
	}

	s, _ := ctx.Value(stateKey).(*state)
	actorID := ""
	if s != nil {
		actorID = s.actor
	}

	const q = `
		INSERT INTO idempotency_keys
		    (key, scope, actor_id, request_hash, response_status, response_body, created_at, expires_at)
		VALUES
		    ($1, $2, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8)
		ON CONFLICT (key, scope) DO NOTHING
	`
	if _, err := tx.Exec(ctx, q,
		key, scope, actorID, resp.RequestHash,
		resp.Status, resp.Body, resp.CreatedAt, resp.ExpiresAt,
	); err != nil {
		return fmt.Errorf("%w: %v", ErrInternalDB, err)
	}
	MarkPersisted(w)
	return nil
}

// MarkPersisted is the lower-level companion to SaveTx for handlers that
// perform the INSERT themselves. Setting the header tells the middleware to
// skip its own auto-save.
func MarkPersisted(w http.ResponseWriter) {
	w.Header().Set(persistedHeader, "true")
}
