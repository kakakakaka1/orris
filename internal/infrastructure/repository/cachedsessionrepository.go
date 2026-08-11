package repository

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/orris-inc/orris/internal/domain/user"
	apperrors "github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

const (
	// sessionCachePrefix namespaces cached session records.
	sessionCachePrefix = "session:id:"
	// sessionUserIndexPrefix namespaces the per-user set of cached session IDs.
	// DeleteByUserID needs it because it only knows the user, not which session IDs
	// it just removed from the database.
	sessionUserIndexPrefix = "session:user:"

	// sessionCacheTTL bounds how long a revoked session could still be served if an
	// eviction is lost (Redis restart, network blip between the delete and the next
	// read). Writes evict eagerly, so this is a backstop, not the normal path - it is
	// also the worst-case revocation delay for logout and password changes.
	sessionCacheTTL = 60 * time.Second
	// sessionMissCacheTTL is deliberately shorter than sessionCacheTTL: a negative
	// entry exists only to absorb repeated probes carrying an already-revoked token.
	sessionMissCacheTTL = 10 * time.Second
	// sessionUserIndexTTL outlives the longest configurable session so the index does
	// not disappear while the sessions it tracks are still live.
	sessionUserIndexTTL = 32 * 24 * time.Hour

	// sessionMissMarker is the payload stored for a negative cache entry. It is not
	// valid JSON for a session, so it can never be mistaken for a cached record.
	sessionMissMarker = "-"
)

// CachedSessionRepository adds a short-lived Redis cache in front of a
// user.SessionRepository. The auth middleware reads a session on every
// authenticated request, so this keeps that check off the database hot path.
//
// Correctness rests on eager eviction rather than on the TTL: every write path
// (create, update, delete, delete-by-user) invalidates the affected entries, so
// logging out or resetting a password takes effect immediately. The TTL only
// caps the damage when an eviction is lost, and the per-entry TTL is additionally
// capped at the session's own remaining lifetime.
//
// Redis failures degrade to the database. A cache problem must never decide
// whether a request is authenticated.
type CachedSessionRepository struct {
	inner  user.SessionRepository
	client *redis.Client
	logger logger.Interface
}

// NewCachedSessionRepository wraps inner with a Redis cache. A nil client returns
// inner unchanged, so deployments without Redis keep working.
func NewCachedSessionRepository(inner user.SessionRepository, client *redis.Client, log logger.Interface) user.SessionRepository {
	if client == nil {
		return inner
	}

	return &CachedSessionRepository{
		inner:  inner,
		client: client,
		logger: log,
	}
}

// GetByID returns the session, preferring the cache and falling back to the
// database on a miss, a decode failure or any Redis error.
func (r *CachedSessionRepository) GetByID(sessionID string) (*user.Session, error) {
	ctx := context.Background()
	key := sessionCachePrefix + sessionID

	if session, found := r.readCache(ctx, key, sessionID); found {
		if session == nil {
			return nil, apperrors.NewNotFoundError("session not found")
		}
		return session, nil
	}

	session, err := r.inner.GetByID(sessionID)
	if err != nil {
		if apperrors.IsNotFoundError(err) {
			r.cacheMiss(ctx, key)
		}
		return nil, err
	}

	r.cacheSession(ctx, key, session)

	return session, nil
}

// readCache reports whether the cache answered. A found entry with a nil session
// is a negative entry, meaning the session is known not to exist.
func (r *CachedSessionRepository) readCache(ctx context.Context, key, sessionID string) (*user.Session, bool) {
	payload, err := r.client.Get(ctx, key).Result()
	if err != nil {
		if err != redis.Nil {
			r.logger.Warnw("failed to read session cache, falling back to database",
				"session_id", sessionID,
				"error", err,
			)
		}
		return nil, false
	}

	if payload == sessionMissMarker {
		return nil, true
	}

	var session user.Session
	if err := json.Unmarshal([]byte(payload), &session); err != nil {
		// A corrupt entry must never stand in for the database.
		r.logger.Warnw("failed to decode cached session, falling back to database",
			"session_id", sessionID,
			"error", err,
		)
		return nil, false
	}

	return &session, true
}

// cacheSession stores a session and registers it in its user's index.
func (r *CachedSessionRepository) cacheSession(ctx context.Context, key string, session *user.Session) {
	payload, err := json.Marshal(session)
	if err != nil {
		r.logger.Warnw("failed to encode session for cache", "session_id", session.ID, "error", err)
		return
	}

	// Never let an entry outlive the session itself, otherwise an expired session
	// could still be served after the row stopped being valid.
	ttl := sessionCacheTTL
	remaining := time.Until(session.ExpiresAt)
	if remaining <= 0 {
		return
	}
	if remaining < ttl {
		ttl = remaining
	}

	pipe := r.client.Pipeline()
	pipe.Set(ctx, key, payload, ttl)
	indexKey := userIndexKey(session.UserID)
	pipe.SAdd(ctx, indexKey, session.ID)
	pipe.Expire(ctx, indexKey, sessionUserIndexTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		r.logger.Warnw("failed to populate session cache", "session_id", session.ID, "error", err)
	}
}

// cacheMiss records that a session does not exist, so a revoked token cannot be
// replayed into a database query on every request.
func (r *CachedSessionRepository) cacheMiss(ctx context.Context, key string) {
	if err := r.client.Set(ctx, key, sessionMissMarker, sessionMissCacheTTL).Err(); err != nil {
		r.logger.Warnw("failed to cache session miss", "key", key, "error", err)
	}
}

func (r *CachedSessionRepository) Create(session *user.Session) error {
	if err := r.inner.Create(session); err != nil {
		return err
	}

	// Clear any negative entry left by a lookup that raced this create, and index
	// the session so DeleteByUserID can find it later.
	ctx := context.Background()
	pipe := r.client.Pipeline()
	pipe.Del(ctx, sessionCachePrefix+session.ID)
	indexKey := userIndexKey(session.UserID)
	pipe.SAdd(ctx, indexKey, session.ID)
	pipe.Expire(ctx, indexKey, sessionUserIndexTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		r.logger.Warnw("failed to prime session cache after create", "session_id", session.ID, "error", err)
	}

	return nil
}

func (r *CachedSessionRepository) Update(session *user.Session) error {
	if err := r.inner.Update(session); err != nil {
		return err
	}

	r.evict(session.ID)

	return nil
}

func (r *CachedSessionRepository) Delete(sessionID string) error {
	err := r.inner.Delete(sessionID)

	// Evict even when the delete failed or matched no rows: a cache entry must
	// never outlive the row it mirrors.
	r.evict(sessionID)

	return err
}

func (r *CachedSessionRepository) DeleteByUserID(userID uint) error {
	err := r.inner.DeleteByUserID(userID)

	r.evictUser(userID)

	return err
}

// DeleteExpired needs no eviction: entries are already capped at the session's
// remaining lifetime, and callers reject an expired session regardless of where
// it was read from.
func (r *CachedSessionRepository) DeleteExpired() error {
	return r.inner.DeleteExpired()
}

func (r *CachedSessionRepository) GetByUserID(userID uint) ([]*user.Session, error) {
	return r.inner.GetByUserID(userID)
}

func (r *CachedSessionRepository) GetByTokenHash(tokenHash string) (*user.Session, error) {
	return r.inner.GetByTokenHash(tokenHash)
}

func (r *CachedSessionRepository) GetByRefreshTokenHash(refreshTokenHash string) (*user.Session, error) {
	return r.inner.GetByRefreshTokenHash(refreshTokenHash)
}

// evict removes a single cached session.
func (r *CachedSessionRepository) evict(sessionID string) {
	ctx := context.Background()
	if err := r.client.Del(ctx, sessionCachePrefix+sessionID).Err(); err != nil {
		// The entry now lingers until sessionCacheTTL expires.
		r.logger.Warnw("failed to evict cached session",
			"session_id", sessionID,
			"error", err,
			"max_stale_seconds", int(sessionCacheTTL.Seconds()),
		)
	}
}

// evictUser removes every cached session belonging to a user, using the index
// written by cacheSession and Create.
func (r *CachedSessionRepository) evictUser(userID uint) {
	ctx := context.Background()
	indexKey := userIndexKey(userID)

	sessionIDs, err := r.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		r.logger.Warnw("failed to read session index during eviction",
			"user_id", userID,
			"error", err,
			"max_stale_seconds", int(sessionCacheTTL.Seconds()),
		)
		return
	}

	keys := make([]string, 0, len(sessionIDs)+1)
	for _, sessionID := range sessionIDs {
		keys = append(keys, sessionCachePrefix+sessionID)
	}
	keys = append(keys, indexKey)

	if err := r.client.Del(ctx, keys...).Err(); err != nil {
		r.logger.Warnw("failed to evict user sessions from cache",
			"user_id", userID,
			"error", err,
			"max_stale_seconds", int(sessionCacheTTL.Seconds()),
		)
	}
}

func userIndexKey(userID uint) string {
	return sessionUserIndexPrefix + strconv.FormatUint(uint64(userID), 10)
}
