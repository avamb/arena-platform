// Package redissession implements Redis-backed session tracking for arena_new.
//
// It provides two key capabilities:
//
//  1. Revocation store — when a refresh token is revoked (e.g. on logout), a
//     key "arena:revoked:{token}" is written with TTL = remaining token lifetime.
//     The refresh endpoint checks this O(1) lookup before querying PostgreSQL.
//
//  2. Active-session tracker — each user's refresh tokens are stored in a Redis
//     sorted set "arena:sessions:{userID}" keyed by token string with score =
//     expiry Unix millisecond timestamp. On login, expired entries are pruned and
//     the oldest tokens are evicted when the user exceeds the configured maximum
//     concurrent sessions.
//
// The production implementation (RedisStore) wraps go-redis/v9. The MemStore
// in mem_store.go provides an in-memory replacement for unit tests.
//
// Feature #118 — Session management (Redis-backed).
package redissession

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyRevoked returns the Redis key used to mark a refresh token as revoked.
func keyRevoked(token string) string { return "arena:revoked:" + token }

// keySessions returns the Redis sorted-set key that tracks active refresh tokens
// for the given userID.
func keySessions(userID string) string { return "arena:sessions:" + userID }

// Store is the interface implemented by both the production RedisStore and the
// in-memory MemStore used in unit tests.
//
// All methods must be safe for concurrent use after construction.
type Store interface {
	// TrackSession registers a new refresh token in the user's active-sessions
	// sorted set. The score is expiresAt as a Unix millisecond timestamp so that
	// expired entries can be pruned in O(log N) by the next login.
	//
	// A no-op when the store is nil or Redis is unavailable.
	TrackSession(ctx context.Context, userID, token string, expiresAt time.Time) error

	// RevokeSession removes the token from the user's active-sessions set and
	// writes a "revoked" key with TTL = remaining token lifetime so that the
	// refresh flow can reject the token without a DB round-trip.
	//
	// Revocation is idempotent: revoking an already-revoked token does not return
	// an error.
	RevokeSession(ctx context.Context, userID, token string, expiresAt time.Time) error

	// IsRevoked returns true when the token has been explicitly revoked via
	// RevokeSession. A false result means "not found in revocation store" — the
	// caller should still verify expiry and DB revoked_at.
	//
	// When the store is unavailable, IsRevoked returns (false, err). The caller
	// may choose to fall back to the DB check on error.
	IsRevoked(ctx context.Context, token string) (bool, error)

	// PruneAndEvict removes entries whose expiry score is ≤ now from the user's
	// sessions set, then returns the tokens of any sessions that exceed maxSessions
	// (0 = unlimited). The caller is responsible for revoking those tokens in the
	// database via RevokeRefreshToken.
	//
	// PruneAndEvict is called on every login before the new token is tracked. It
	// enforces the concurrent-session policy configured per deployment.
	PruneAndEvict(ctx context.Context, userID string, maxSessions int, now time.Time) ([]string, error)

	// Ping checks that the backing store is reachable. Used by the readiness probe.
	Ping(ctx context.Context) error
}

// RedisStore is the production-ready Store backed by a go-redis/v9 client.
// It is safe for concurrent use.
type RedisStore struct {
	rdb *redis.Client
}

// NewRedisStore constructs a RedisStore from a parsed Redis URL.
// The client is configured with sensible defaults for arena_new:
//   - Pool size: 10 connections
//   - DialTimeout: 3 s
//   - ReadTimeout / WriteTimeout: 2 s
func NewRedisStore(redisURL string) (*RedisStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("redissession: parse URL: %w", err)
	}
	opts.PoolSize = 10
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second

	rdb := redis.NewClient(opts)
	return &RedisStore{rdb: rdb}, nil
}

// NewRedisStoreFromClient constructs a RedisStore from an existing *redis.Client.
// Useful for tests that want to supply a pre-configured client (e.g. via
// testcontainers).
func NewRedisStoreFromClient(rdb *redis.Client) *RedisStore {
	return &RedisStore{rdb: rdb}
}

// Close closes the underlying Redis connection pool.
func (s *RedisStore) Close() error { return s.rdb.Close() }

// Ping implements Store.
func (s *RedisStore) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.rdb.Ping(pingCtx).Err()
}

// TrackSession implements Store.
func (s *RedisStore) TrackSession(ctx context.Context, userID, token string, expiresAt time.Time) error {
	score := float64(expiresAt.UnixMilli())
	return s.rdb.ZAdd(ctx, keySessions(userID), redis.Z{
		Score:  score,
		Member: token,
	}).Err()
}

// RevokeSession implements Store.
func (s *RedisStore) RevokeSession(ctx context.Context, userID, token string, expiresAt time.Time) error {
	// Compute remaining TTL; floor at 1 second so the key is always written
	// (even if the token expired milliseconds ago — in which case Redis will
	// expire it almost immediately, which is correct).
	ttl := time.Until(expiresAt)
	if ttl < time.Second {
		ttl = time.Second
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, keyRevoked(token), "1", ttl)
	pipe.ZRem(ctx, keySessions(userID), token)
	_, err := pipe.Exec(ctx)
	return err
}

// IsRevoked implements Store.
func (s *RedisStore) IsRevoked(ctx context.Context, token string) (bool, error) {
	n, err := s.rdb.Exists(ctx, keyRevoked(token)).Result()
	if err != nil {
		return false, fmt.Errorf("redissession: is_revoked check: %w", err)
	}
	return n > 0, nil
}

// PruneAndEvict implements Store.
//
// maxSessions is the maximum number of active sessions to RETAIN after this
// call. Pass -1 (or any negative value) to disable eviction entirely.
// Passing 0 means "evict all active sessions" (used by the login handler to
// leave room for the incoming new session when MaxConcurrentSessionsPerUser=1).
//
// Algorithm:
//  1. ZREMRANGEBYSCORE — remove all entries with score ≤ now (expired tokens).
//  2. ZCARD — count remaining active sessions.
//  3. If count > maxSessions (and maxSessions ≥ 0): ZRANGE 0 excess-1 to get
//     the oldest tokens; remove them from the sorted set immediately so they
//     are not returned again on a subsequent call.
//  4. Return the evicted token strings; the caller calls RevokeRefreshToken(DB)
//     for each and then RevokeSession(Redis) to write the revocation key.
func (s *RedisStore) PruneAndEvict(ctx context.Context, userID string, maxSessions int, now time.Time) ([]string, error) {
	key := keySessions(userID)
	nowScore := fmt.Sprintf("%d", now.UnixMilli())

	// 1. Prune expired entries.
	if err := s.rdb.ZRemRangeByScore(ctx, key, "-inf", nowScore).Err(); err != nil {
		return nil, fmt.Errorf("redissession: prune expired: %w", err)
	}

	if maxSessions < 0 {
		return nil, nil // negative = unlimited, no eviction needed
	}

	// 2. Count remaining.
	count, err := s.rdb.ZCard(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redissession: zcard: %w", err)
	}

	if count <= int64(maxSessions) {
		return nil, nil // within limit — nothing to evict
	}

	// 3. Get oldest tokens to evict (lowest scores = earliest expiry).
	excess := count - int64(maxSessions)
	tokens, err := s.rdb.ZRange(ctx, key, 0, excess-1).Result()
	if err != nil {
		return nil, fmt.Errorf("redissession: zrange oldest: %w", err)
	}
	if len(tokens) == 0 {
		return nil, nil
	}

	// 4. Remove evicted tokens from the sorted set.
	members := make([]any, len(tokens))
	for i, t := range tokens {
		members[i] = t
	}
	if err := s.rdb.ZRem(ctx, key, members...).Err(); err != nil {
		return nil, fmt.Errorf("redissession: zrem evicted: %w", err)
	}

	return tokens, nil
}

// compile-time interface guard.
var _ Store = (*RedisStore)(nil)
