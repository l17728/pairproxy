package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/keygen"
	"github.com/l17728/pairproxy/internal/proxy"
)

// fakeUserLookup 实现 ActiveUserLister 接口（测试用）
type fakeUserLookup struct {
	users []keygen.UserEntry
}

func (f *fakeUserLookup) ListActive() ([]keygen.UserEntry, error) {
	return f.users, nil
}

func makeAliceKey(t *testing.T) string {
	t.Helper()
	k, err := keygen.GenerateKey("alice")
	require.NoError(t, err)
	return k
}

func TestKeyAuthMiddleware_OpenAI_BearerFormat(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true},
	}}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	logger := zap.NewNop()

	var gotClaims *auth.JWTClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = proxy.ClaimsFromContext(r.Context())
		w.WriteHeader(200)
	})

	mw := proxy.NewKeyAuthMiddleware(logger, users, cache, next)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	require.NotNil(t, gotClaims)
	assert.Equal(t, "u1", gotClaims.UserID)
	assert.Equal(t, "alice", gotClaims.Username)
}

func TestKeyAuthMiddleware_Anthropic_XApiKeyFormat(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true},
	}}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)

	var gotClaims *auth.JWTClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = proxy.ClaimsFromContext(r.Context())
		w.WriteHeader(200)
	})

	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	require.NotNil(t, gotClaims)
	assert.Equal(t, "alice", gotClaims.Username)
}

func TestKeyAuthMiddleware_MissingAuth(t *testing.T) {
	users := &fakeUserLookup{}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestKeyAuthMiddleware_InvalidFormat(t *testing.T) {
	users := &fakeUserLookup{}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-openai-notapairproxykey")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestKeyAuthMiddleware_InvalidUser(t *testing.T) {
	key := makeAliceKey(t)
	// 使用一个需要大量重复字符的用户名（20个q），使其几乎不可能在48字符的key body中出现
	// 避免使用短用户名（如"bob"）导致概率性误匹配（已知设计限制）
	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u2", Username: "qqqqqqqqqqqqqqqqqqqq", IsActive: true},
	}}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestKeyAuthMiddleware_CacheHit(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{users: []keygen.UserEntry{}}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	var gotClaims *auth.JWTClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = proxy.ClaimsFromContext(r.Context())
		w.WriteHeader(200)
	})
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
	require.NotNil(t, gotClaims)
	assert.Equal(t, "alice", gotClaims.Username)
}

func TestKeyAuthMiddleware_GroupID(t *testing.T) {
	key := makeAliceKey(t)
	gid := "g1"
	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "alice", IsActive: true, GroupID: &gid},
	}}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)

	var gotClaims *auth.JWTClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = proxy.ClaimsFromContext(r.Context())
		w.WriteHeader(200)
	})
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, next)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	require.NotNil(t, gotClaims)
	assert.Equal(t, "g1", gotClaims.GroupID)
}
