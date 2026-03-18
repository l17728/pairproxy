package proxy_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/keygen"
	"github.com/l17728/pairproxy/internal/proxy"
)

// 测试用 HMAC 密钥
var testKeygenSecret = "test-keygen-secret-must-be-at-least-32-bytes!!"

// fakeUserLookup 实现 ActiveUserLister 接口（测试用）
type fakeUserLookup struct {
	users         []keygen.UserEntry
	activeByID    map[string]bool // 可选：覆盖 IsUserActive 返回值
	isActiveErr   error           // 可选：模拟 IsUserActive 返回错误
}

func (f *fakeUserLookup) ListActive() ([]keygen.UserEntry, error) {
	return f.users, nil
}

func (f *fakeUserLookup) IsUserActive(userID string) (bool, error) {
	if f.isActiveErr != nil {
		return false, f.isActiveErr
	}
	if f.activeByID != nil {
		active, ok := f.activeByID[userID]
		if !ok {
			return false, nil // 未找到视为不活跃
		}
		return active, nil
	}
	// 默认：在 users 列表中查找
	for _, u := range f.users {
		if u.ID == userID {
			return u.IsActive, nil
		}
	}
	return false, nil
}

func makeAliceKey(t *testing.T) string {
	t.Helper()
	k, err := keygen.GenerateKey("alice", []byte(testKeygenSecret))
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

	mw := proxy.NewKeyAuthMiddleware(logger, users, cache, testKeygenSecret, next)
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

	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)
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
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

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
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

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
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestKeyAuthMiddleware_CacheHit(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{
		users:      []keygen.UserEntry{},
		activeByID: map[string]bool{"u1": true}, // 用户仍活跃
	}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	var gotClaims *auth.JWTClaims
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClaims = proxy.ClaimsFromContext(r.Context())
		w.WriteHeader(200)
	})
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)
	assert.Equal(t, 200, rr.Code)
	require.NotNil(t, gotClaims)
	assert.Equal(t, "alice", gotClaims.Username)
}

// errUserLookup 实现 ActiveUserLister 接口，ListActive 始终返回错误（测试用）
type errUserLookup struct{ err error }

func (e *errUserLookup) ListActive() ([]keygen.UserEntry, error) {
	return nil, e.err
}

func (e *errUserLookup) IsUserActive(_ string) (bool, error) {
	return false, e.err
}

func TestKeyAuthMiddleware_ListActiveError(t *testing.T) {
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)

	users := &errUserLookup{err: fmt.Errorf("db connection lost")}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	// 提供合法格式的 key，使其通过格式校验后触发 ListActive
	key := "sk-pp-" + strings.Repeat("a", 48)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestKeyAuthMiddleware_FabricatedKey_NoMatch(t *testing.T) {
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)

	// HMAC 算法下，伪造的合法格式 key 不会匹配任何用户
	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "abcd", IsActive: true},
		{ID: "u2", Username: "dcba", IsActive: true},
	}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	// 伪造的合法格式 key（6+48=54 字符），不是任何用户的 HMAC key
	key := "sk-pp-abcdabcdabcdabcdabcdabcdabcdabcdabcdabcdabcdabcd"
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
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
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	require.NotNil(t, gotClaims)
	assert.Equal(t, "g1", gotClaims.GroupID)
}

// ---------------------------------------------------------------------------
// 缓存命中后用户禁用的二次校验测试
// ---------------------------------------------------------------------------

// TestKeyAuthMiddleware_CacheHit_UserDisabled 验证：用户被禁用后，即使缓存中
// 存有其 Key，下一次请求也应立即返回 401，不等 TTL 自然过期。
func TestKeyAuthMiddleware_CacheHit_UserDisabled(t *testing.T) {
	key := makeAliceKey(t)
	// activeByID 控制 IsUserActive 返回值：alice 已被禁用
	users := &fakeUserLookup{
		users:      []keygen.UserEntry{},
		activeByID: map[string]bool{"u1": false},
	}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	// 预热缓存（模拟禁用前已缓存的状态）
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code, "禁用用户应立即返回 401，不等缓存 TTL 过期")
	assert.Contains(t, rr.Body.String(), "account_disabled")
}

// TestKeyAuthMiddleware_CacheHit_UserDisabled_CacheEvicted 验证：用户被禁用并
// 拒绝请求后，其缓存条目被驱逐（下次不会走 cache hit 路径）。
func TestKeyAuthMiddleware_CacheHit_UserDisabled_CacheEvicted(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{
		users:      []keygen.UserEntry{},
		activeByID: map[string]bool{"u1": false},
	}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	// 驱逐后缓存应为空
	assert.Nil(t, cache.Get(key), "禁用后缓存条目应被驱逐")
}

// TestKeyAuthMiddleware_CacheHit_ActiveUser_Passes 验证：活跃用户缓存命中后
// 二次校验通过，请求正常放行。
func TestKeyAuthMiddleware_CacheHit_ActiveUser_Passes(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{
		users:      []keygen.UserEntry{},
		activeByID: map[string]bool{"u1": true},
	}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	})
	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret, next)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	assert.True(t, reached, "活跃用户缓存命中应放行到下游")
}

// TestKeyAuthMiddleware_CacheHit_IsActiveError 验证：IsUserActive 查询失败时
// 返回 500，不允许请求通过（fail-closed 原则）。
func TestKeyAuthMiddleware_CacheHit_IsActiveError(t *testing.T) {
	key := makeAliceKey(t)
	users := &fakeUserLookup{
		users:       []keygen.UserEntry{},
		isActiveErr: fmt.Errorf("db connection lost"),
	}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(key, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	mw := proxy.NewKeyAuthMiddleware(zap.NewNop(), users, cache, testKeygenSecret,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code, "IsUserActive 失败应返回 500")
}
