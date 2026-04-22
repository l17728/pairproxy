package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/keygen"
	"github.com/l17728/pairproxy/internal/proxy"
)

// mockSProxy 用于测试 DirectProxyHandler，避免真实代理依赖
type mockSProxy struct {
	receivedPath string
	receivedUser string
	response     string
}

func (m *mockSProxy) ServeDirect(w http.ResponseWriter, r *http.Request) {
	m.receivedPath = r.URL.Path
	if claims := proxy.ClaimsFromContext(r.Context()); claims != nil {
		m.receivedUser = claims.Username
	}
	w.WriteHeader(200)
	_, _ = io.WriteString(w, m.response)
}

func TestDirectHandler_AnthropicPathRewrite(t *testing.T) {
	mock := &mockSProxy{response: "ok"}
	aliceKey, _ := keygen.GenerateKey("alice", []byte(testPasswordHash))
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(aliceKey, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true},
	}}

	dh := proxy.NewDirectProxyHandler(zap.NewNop(), mock, users, cache, nil, nil)
	handler := dh.HandlerAnthropic()

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", aliceKey)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "/v1/messages", mock.receivedPath, "path must be rewritten")
	assert.Equal(t, "alice", mock.receivedUser)
}

func TestDirectHandler_OpenAIPathUnchanged(t *testing.T) {
	mock := &mockSProxy{response: "ok"}
	aliceKey, _ := keygen.GenerateKey("alice", []byte(testPasswordHash))
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	cache.Set(aliceKey, &keygen.CachedUser{UserID: "u1", Username: "alice"})

	users := &fakeUserLookup{users: []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: testPasswordHash, IsActive: true},
	}}

	dh := proxy.NewDirectProxyHandler(zap.NewNop(), mock, users, cache, nil, nil)
	handler := dh.HandlerOpenAI()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+aliceKey)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	assert.Equal(t, "/v1/chat/completions", mock.receivedPath, "OpenAI path must not be rewritten")
}

func TestDirectHandler_HandlerBuiltOnce(t *testing.T) {
	mock := &mockSProxy{}
	users := &fakeUserLookup{}
	cache, err := keygen.NewKeyCache(10, time.Minute)
	require.NoError(t, err)
	dh := proxy.NewDirectProxyHandler(zap.NewNop(), mock, users, cache, nil, nil)

	// HandlerOpenAI/HandlerAnthropic 返回指针类型，可通过 assert.Same 验证同一实例
	h1 := dh.HandlerOpenAI()
	h2 := dh.HandlerOpenAI()
	assert.Same(t, h1, h2, "HandlerOpenAI must return the same pre-built handler")

	h3 := dh.HandlerAnthropic()
	h4 := dh.HandlerAnthropic()
	assert.Same(t, h3, h4, "HandlerAnthropic must return the same pre-built handler")
}

