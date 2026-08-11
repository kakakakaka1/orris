package repository

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orris-inc/orris/internal/domain/user"
	apperrors "github.com/orris-inc/orris/internal/shared/errors"
	"github.com/orris-inc/orris/internal/shared/logger"
)

// nopLogger is a no-op logger for testing.
type nopLogger struct{}

func newNopLogger() logger.Interface { return &nopLogger{} }

func (l *nopLogger) Debug(msg string, args ...any)                   {}
func (l *nopLogger) Info(msg string, args ...any)                    {}
func (l *nopLogger) Warn(msg string, args ...any)                    {}
func (l *nopLogger) Error(msg string, args ...any)                   {}
func (l *nopLogger) Fatal(msg string, args ...any)                   {}
func (l *nopLogger) With(args ...any) logger.Interface               { return l }
func (l *nopLogger) Named(name string) logger.Interface              { return l }
func (l *nopLogger) Debugw(msg string, keysAndValues ...interface{}) {}
func (l *nopLogger) Infow(msg string, keysAndValues ...interface{})  {}
func (l *nopLogger) Warnw(msg string, keysAndValues ...interface{})  {}
func (l *nopLogger) Errorw(msg string, keysAndValues ...interface{}) {}
func (l *nopLogger) Fatalw(msg string, keysAndValues ...interface{}) {}

// fakeSessionRepo is an in-memory user.SessionRepository that counts reads, so a
// test can tell a cache hit from a database round trip.
type fakeSessionRepo struct {
	sessions   map[string]*user.Session
	getByIDHit int
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{sessions: make(map[string]*user.Session)}
}

func (f *fakeSessionRepo) Create(session *user.Session) error {
	f.sessions[session.ID] = session
	return nil
}

func (f *fakeSessionRepo) GetByID(sessionID string) (*user.Session, error) {
	f.getByIDHit++
	session, ok := f.sessions[sessionID]
	if !ok {
		return nil, apperrors.NewNotFoundError("session not found")
	}
	return session, nil
}

func (f *fakeSessionRepo) GetByUserID(userID uint) ([]*user.Session, error) {
	var out []*user.Session
	for _, s := range f.sessions {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeSessionRepo) GetByTokenHash(string) (*user.Session, error) { return nil, nil }

func (f *fakeSessionRepo) GetByRefreshTokenHash(string) (*user.Session, error) { return nil, nil }

func (f *fakeSessionRepo) Update(session *user.Session) error {
	f.sessions[session.ID] = session
	return nil
}

func (f *fakeSessionRepo) Delete(sessionID string) error {
	delete(f.sessions, sessionID)
	return nil
}

func (f *fakeSessionRepo) DeleteByUserID(userID uint) error {
	for id, s := range f.sessions {
		if s.UserID == userID {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeSessionRepo) DeleteExpired() error { return nil }

func setupCachedSessionRepo(t *testing.T) (*CachedSessionRepository, *fakeSessionRepo, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	inner := newFakeSessionRepo()
	repo := NewCachedSessionRepository(inner, client, newNopLogger()).(*CachedSessionRepository)

	return repo, inner, mr
}

func newTestSession(id string, userID uint) *user.Session {
	return &user.Session{
		ID:        id,
		UserID:    userID,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

// TestCachedSessionRepository_GetByID_ServesFromCache verifies the second read
// avoids the database.
func TestCachedSessionRepository_GetByID_ServesFromCache(t *testing.T) {
	repo, inner, _ := setupCachedSessionRepo(t)
	require.NoError(t, repo.Create(newTestSession("sess-1", 7)))

	first, err := repo.GetByID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, uint(7), first.UserID)
	assert.Equal(t, 1, inner.getByIDHit)

	second, err := repo.GetByID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, uint(7), second.UserID)
	assert.Equal(t, 1, inner.getByIDHit, "second read should be served from cache")
}

// TestCachedSessionRepository_Delete_RevokesImmediately is the core guarantee:
// logging out must not leave a usable cached session behind.
func TestCachedSessionRepository_Delete_RevokesImmediately(t *testing.T) {
	repo, _, _ := setupCachedSessionRepo(t)
	require.NoError(t, repo.Create(newTestSession("sess-1", 7)))

	_, err := repo.GetByID("sess-1")
	require.NoError(t, err, "session should be readable before revocation")

	require.NoError(t, repo.Delete("sess-1"))

	_, err = repo.GetByID("sess-1")
	require.Error(t, err)
	assert.True(t, apperrors.IsNotFoundError(err), "revoked session must not be served from cache")
}

// TestCachedSessionRepository_DeleteByUserID_RevokesEverySession covers password
// change, password reset and admin reset, which revoke by user rather than by ID.
func TestCachedSessionRepository_DeleteByUserID_RevokesEverySession(t *testing.T) {
	repo, _, _ := setupCachedSessionRepo(t)
	require.NoError(t, repo.Create(newTestSession("sess-1", 7)))
	require.NoError(t, repo.Create(newTestSession("sess-2", 7)))
	require.NoError(t, repo.Create(newTestSession("sess-3", 8)))

	// Warm the cache for all three.
	for _, id := range []string{"sess-1", "sess-2", "sess-3"} {
		_, err := repo.GetByID(id)
		require.NoError(t, err)
	}

	require.NoError(t, repo.DeleteByUserID(7))

	for _, id := range []string{"sess-1", "sess-2"} {
		_, err := repo.GetByID(id)
		require.Error(t, err, "session %s should be revoked", id)
		assert.True(t, apperrors.IsNotFoundError(err))
	}

	// Another user's session must survive.
	survivor, err := repo.GetByID("sess-3")
	require.NoError(t, err)
	assert.Equal(t, uint(8), survivor.UserID)
}

// TestCachedSessionRepository_NegativeCache verifies a revoked token stops
// generating database queries on every request.
func TestCachedSessionRepository_NegativeCache(t *testing.T) {
	repo, inner, _ := setupCachedSessionRepo(t)

	_, err := repo.GetByID("missing")
	require.Error(t, err)
	assert.Equal(t, 1, inner.getByIDHit)

	_, err = repo.GetByID("missing")
	require.Error(t, err)
	assert.True(t, apperrors.IsNotFoundError(err))
	assert.Equal(t, 1, inner.getByIDHit, "repeated probes should be absorbed by the negative entry")
}

// TestCachedSessionRepository_CreateClearsNegativeEntry ensures a negative entry
// cannot outlive the session it denied.
func TestCachedSessionRepository_CreateClearsNegativeEntry(t *testing.T) {
	repo, _, _ := setupCachedSessionRepo(t)

	_, err := repo.GetByID("sess-1")
	require.Error(t, err)

	require.NoError(t, repo.Create(newTestSession("sess-1", 7)))

	session, err := repo.GetByID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, uint(7), session.UserID)
}

// TestCachedSessionRepository_UpdateEvicts ensures a refreshed session is re-read
// rather than served from a stale entry.
func TestCachedSessionRepository_UpdateEvicts(t *testing.T) {
	repo, inner, _ := setupCachedSessionRepo(t)
	require.NoError(t, repo.Create(newTestSession("sess-1", 7)))

	_, err := repo.GetByID("sess-1")
	require.NoError(t, err)
	hitsBefore := inner.getByIDHit

	extended := newTestSession("sess-1", 7)
	extended.ExpiresAt = time.Now().Add(72 * time.Hour)
	require.NoError(t, repo.Update(extended))

	_, err = repo.GetByID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, hitsBefore+1, inner.getByIDHit, "update should force a re-read")
}

// TestCachedSessionRepository_TTLCappedBySessionLifetime ensures a cache entry can
// never outlive the session itself.
func TestCachedSessionRepository_TTLCappedBySessionLifetime(t *testing.T) {
	repo, _, mr := setupCachedSessionRepo(t)

	shortLived := newTestSession("sess-short", 7)
	shortLived.ExpiresAt = time.Now().Add(5 * time.Second)
	require.NoError(t, repo.Create(shortLived))

	_, err := repo.GetByID("sess-short")
	require.NoError(t, err)

	ttl := mr.TTL(sessionCachePrefix + "sess-short")
	assert.Greater(t, ttl, time.Duration(0))
	assert.LessOrEqual(t, ttl, 5*time.Second, "entry must not outlive the session")
}

// TestCachedSessionRepository_AlreadyExpiredSessionNotCached guards against
// caching a session that is already past its expiry.
func TestCachedSessionRepository_AlreadyExpiredSessionNotCached(t *testing.T) {
	repo, inner, _ := setupCachedSessionRepo(t)

	expired := newTestSession("sess-expired", 7)
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	require.NoError(t, inner.Create(expired))

	_, err := repo.GetByID("sess-expired")
	require.NoError(t, err)
	_, err = repo.GetByID("sess-expired")
	require.NoError(t, err)

	assert.Equal(t, 2, inner.getByIDHit, "an expired session must not be cached")
}

// TestCachedSessionRepository_RedisDownFallsBackToDatabase ensures a cache outage
// never decides whether a request is authenticated.
func TestCachedSessionRepository_RedisDownFallsBackToDatabase(t *testing.T) {
	repo, inner, mr := setupCachedSessionRepo(t)
	require.NoError(t, inner.Create(newTestSession("sess-1", 7)))

	mr.Close()

	session, err := repo.GetByID("sess-1")
	require.NoError(t, err, "a Redis outage must fall back to the database")
	assert.Equal(t, uint(7), session.UserID)
}

// TestCachedSessionRepository_CorruptEntryFallsBackToDatabase ensures a malformed
// cache entry cannot stand in for a real lookup.
func TestCachedSessionRepository_CorruptEntryFallsBackToDatabase(t *testing.T) {
	repo, inner, mr := setupCachedSessionRepo(t)
	require.NoError(t, inner.Create(newTestSession("sess-1", 7)))
	require.NoError(t, mr.Set(sessionCachePrefix+"sess-1", "{not-json"))

	session, err := repo.GetByID("sess-1")
	require.NoError(t, err)
	assert.Equal(t, uint(7), session.UserID)
	assert.Equal(t, 1, inner.getByIDHit)
}

// TestNewCachedSessionRepository_NilClientReturnsInner keeps deployments without
// Redis working.
func TestNewCachedSessionRepository_NilClientReturnsInner(t *testing.T) {
	inner := newFakeSessionRepo()
	repo := NewCachedSessionRepository(inner, nil, newNopLogger())
	assert.Same(t, inner, repo)
}
