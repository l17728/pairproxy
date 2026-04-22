package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/keygen"
)

func setupKeygenTest(t *testing.T) (*api.KeygenHandler, *db.UserRepo) {
	t.Helper()
	logger := zap.NewNop()
	gdb, err := db.Open(logger, ":memory:")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(logger, gdb))
	userRepo := db.NewUserRepo(gdb, logger)
	jwtMgr, err := auth.NewManager(logger, "test-secret-key-for-testing-only")
	require.NoError(t, err)
	h := api.NewKeygenHandler(logger, userRepo, jwtMgr)
	return h, userRepo
}

func TestKeygenLogin_Success(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "testpass")
	require.NoError(t, err)
	u := &db.User{Username: "alice", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "testpass"})
	req := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)

	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, "alice", resp["username"])
	assert.Contains(t, resp["key"].(string), "sk-pp-")
	assert.NotEmpty(t, resp["token"])
}

func TestKeygenLogin_WrongPassword(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "correct")
	require.NoError(t, err)
	u := &db.User{Username: "bob", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	body, _ := json.Marshal(map[string]string{"username": "bob", "password": "wrong"})
	req := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(rr, req)

	assert.Equal(t, 401, rr.Code)
}

func TestKeygenLogin_DisabledUser(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "pass")
	require.NoError(t, err)
	u := &db.User{Username: "disabled", PasswordHash: pass}
	require.NoError(t, userRepo.Create(u))
	require.NoError(t, userRepo.SetActive(u.ID, false))

	body, _ := json.Marshal(map[string]string{"username": "disabled", "password": "pass"})
	req := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(rr, req)

	assert.Equal(t, 401, rr.Code)
}

func TestKeygenRegenerate_Success(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "pass")
	require.NoError(t, err)
	u := &db.User{Username: "charlie", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	// 先登录
	loginBody, _ := json.Marshal(map[string]string{"username": "charlie", "password": "pass"})
	loginReq := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(loginRR, loginReq)
	require.Equal(t, 200, loginRR.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(loginRR.Body.Bytes(), &loginResp))
	token := loginResp["token"].(string)
	firstKey := loginResp["key"].(string)

	// 重新生成
	regenReq := httptest.NewRequest("POST", "/keygen/api/regenerate", nil)
	regenReq.Header.Set("Authorization", "Bearer "+token)
	regenRR := httptest.NewRecorder()
	mux.ServeHTTP(regenRR, regenReq)

	assert.Equal(t, 200, regenRR.Code)
	var regenResp map[string]interface{}
	require.NoError(t, json.Unmarshal(regenRR.Body.Bytes(), &regenResp))
	newKey := regenResp["key"].(string)
	assert.Contains(t, newKey, "sk-pp-")
	assert.Equal(t, firstKey, newKey, "HMAC-based regenerate should return same key (deterministic)")
}

func TestKeygenRegenerate_Unauthorized(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/keygen/api/regenerate", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, 401, rr.Code)
}

func TestKeygenLogin_MissingFields(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// 缺少密码
	body := `{"username":"alice","password":""}`
	req := httptest.NewRequest(http.MethodPost, "/keygen/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "missing_fields")

	// 缺少用户名
	body = `{"username":"","password":"pass"}`
	req = httptest.NewRequest(http.MethodPost, "/keygen/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "missing_fields")
}

func TestKeygenLogin_InvalidJSON(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/keygen/api/login", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_request")
}

func TestKeygenLogin_UserNotFound(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// "nobody" 不在测试数据库中
	body := `{"username":"nobody","password":"anypass"}`
	req := httptest.NewRequest(http.MethodPost, "/keygen/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid_credentials")
}

// ---------------------------------------------------------------------------
// change-password
// ---------------------------------------------------------------------------

func TestKeygenChangePassword_Success(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	// 创建缓存并注入（验证改密后缓存被踢出）
	cache, err := keygen.NewKeyCache(10, 0)
	require.NoError(t, err)
	h.SetKeyCache(cache)

	pass, err := auth.HashPassword(zap.NewNop(), "oldpass123")
	require.NoError(t, err)
	u := &db.User{Username: "eve", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	// 登录获取 session token 和旧 key
	loginBody, _ := json.Marshal(map[string]string{"username": "eve", "password": "oldpass123"})
	loginReq := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(loginRR, loginReq)
	require.Equal(t, 200, loginRR.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(loginRR.Body.Bytes(), &loginResp))
	token := loginResp["token"].(string)
	oldKey := loginResp["key"].(string)

	// 预热缓存（模拟已有缓存命中）
	cache.Set(oldKey, &keygen.CachedUser{UserID: u.ID, Username: u.Username})

	// 修改密码
	cpBody, _ := json.Marshal(map[string]string{"old_password": "oldpass123", "new_password": "newpass456"})
	cpReq := httptest.NewRequest("POST", "/keygen/api/change-password", bytes.NewReader(cpBody))
	cpReq.Header.Set("Content-Type", "application/json")
	cpReq.Header.Set("Authorization", "Bearer "+token)
	cpRR := httptest.NewRecorder()
	mux.ServeHTTP(cpRR, cpReq)

	require.Equal(t, 200, cpRR.Code)
	var cpResp map[string]interface{}
	require.NoError(t, json.Unmarshal(cpRR.Body.Bytes(), &cpResp))
	newKey := cpResp["key"].(string)

	assert.Contains(t, newKey, "sk-pp-", "new key must have correct prefix")
	assert.NotEqual(t, oldKey, newKey, "new key must differ from old key after password change")

	// 旧 Key 缓存应被踢出
	assert.Nil(t, cache.Get(oldKey), "old key cache must be invalidated after password change")
}

func TestKeygenChangePassword_WrongOldPassword(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "correct123")
	require.NoError(t, err)
	u := &db.User{Username: "frank", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	loginBody, _ := json.Marshal(map[string]string{"username": "frank", "password": "correct123"})
	loginReq := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(loginRR, loginReq)
	require.Equal(t, 200, loginRR.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(loginRR.Body.Bytes(), &loginResp))
	token := loginResp["token"].(string)

	cpBody, _ := json.Marshal(map[string]string{"old_password": "wrong_old", "new_password": "newpass456"})
	cpReq := httptest.NewRequest("POST", "/keygen/api/change-password", bytes.NewReader(cpBody))
	cpReq.Header.Set("Content-Type", "application/json")
	cpReq.Header.Set("Authorization", "Bearer "+token)
	cpRR := httptest.NewRecorder()
	mux.ServeHTTP(cpRR, cpReq)

	assert.Equal(t, http.StatusUnauthorized, cpRR.Code)
	assert.Contains(t, cpRR.Body.String(), "wrong_password")
}

func TestKeygenChangePassword_SamePassword(t *testing.T) {
	h, userRepo := setupKeygenTest(t)

	pass, err := auth.HashPassword(zap.NewNop(), "samepass123")
	require.NoError(t, err)
	u := &db.User{Username: "grace", PasswordHash: pass, IsActive: true}
	require.NoError(t, userRepo.Create(u))

	loginBody, _ := json.Marshal(map[string]string{"username": "grace", "password": "samepass123"})
	loginReq := httptest.NewRequest("POST", "/keygen/api/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRR := httptest.NewRecorder()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.ServeHTTP(loginRR, loginReq)
	require.Equal(t, 200, loginRR.Code)

	var loginResp map[string]interface{}
	require.NoError(t, json.Unmarshal(loginRR.Body.Bytes(), &loginResp))
	token := loginResp["token"].(string)

	// old == new → 应被拒绝
	cpBody, _ := json.Marshal(map[string]string{"old_password": "samepass123", "new_password": "samepass123"})
	cpReq := httptest.NewRequest("POST", "/keygen/api/change-password", bytes.NewReader(cpBody))
	cpReq.Header.Set("Content-Type", "application/json")
	cpReq.Header.Set("Authorization", "Bearer "+token)
	cpRR := httptest.NewRecorder()
	mux.ServeHTTP(cpRR, cpReq)

	assert.Equal(t, http.StatusBadRequest, cpRR.Code)
	assert.Contains(t, cpRR.Body.String(), "same_password")
}

func TestKeygenChangePassword_Unauthorized(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cpBody, _ := json.Marshal(map[string]string{"old_password": "old", "new_password": "new"})
	req := httptest.NewRequest("POST", "/keygen/api/change-password", bytes.NewReader(cpBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestKeygenRegenerate_MissingAuthHeader(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/keygen/api/regenerate", nil)
	// 不设置 Authorization 头
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), "missing_token")
}

func TestKeygenStaticPage(t *testing.T) {
	h, _ := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/keygen/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, 200, rr.Code)
	assert.Contains(t, rr.Body.String(), "PairProxy")
}

// TestKeygenWorkerBlocked 验证 Worker 节点拒绝 keygen 写操作（POST login/regenerate），返回 403。
func TestKeygenWorkerBlocked(t *testing.T) {
	h, _ := setupKeygenTest(t)
	h.SetWorkerMode(true)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	writePaths := []string{
		"/keygen/api/login",
		"/keygen/api/regenerate",
	}
	for _, path := range writePaths {
		t.Run("POST "+path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code,
				"Worker node should return 403 for POST %s", path)
			assert.Contains(t, rr.Body.String(), "worker_read_only",
				"response body should contain 'worker_read_only'")
		})
	}
}

// TestKeygenWorkerAllowsStaticPage 验证 Worker 节点允许访问静态页面（GET /keygen/）。
func TestKeygenWorkerAllowsStaticPage(t *testing.T) {
	h, _ := setupKeygenTest(t)
	h.SetWorkerMode(true)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/keygen/", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// ---------------------------------------------------------------------------
// Fix 2: keygen login uses GetByUsernameAndProvider("local") — mixed-auth regression
// ---------------------------------------------------------------------------

// TestKeygenLogin_MixedAuth_LocalAndLDAP_LocalWins verifies that when both a local
// and LDAP user share the same username, keygen login with local password succeeds
// and matches the local account only.
func TestKeygenLogin_MixedAuth_LocalAndLDAP_LocalWins(t *testing.T) {
	h, userRepo := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	localPass, err := auth.HashPassword(zap.NewNop(), "localpass")
	require.NoError(t, err)

	// Create local "alice"
	require.NoError(t, userRepo.Create(&db.User{
		Username: "alice", PasswordHash: localPass, AuthProvider: "local", IsActive: true,
	}))
	// Create LDAP "alice" (no password hash — LDAP auth doesn't use it)
	extID := "ldap-alice-uid"
	require.NoError(t, userRepo.Create(&db.User{
		Username: "alice", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID, IsActive: true,
	}))

	body, _ := json.Marshal(map[string]string{"username": "alice", "password": "localpass"})
	req := httptest.NewRequest(http.MethodPost, "/keygen/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code,
		"keygen login should succeed with local password when both local and ldap 'alice' exist; body: "+rr.Body.String())
}

// TestKeygenLogin_MixedAuth_LDAPOnlyUser_Returns401 verifies that an LDAP-only user
// (no local account) cannot login via keygen (which uses local password auth).
func TestKeygenLogin_MixedAuth_LDAPOnlyUser_Returns401(t *testing.T) {
	h, userRepo := setupKeygenTest(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	extID := "ldap-bob-uid"
	require.NoError(t, userRepo.Create(&db.User{
		Username: "bob", PasswordHash: "", AuthProvider: "ldap", ExternalID: &extID, IsActive: true,
	}))

	body, _ := json.Marshal(map[string]string{"username": "bob", "password": "anypassword"})
	req := httptest.NewRequest(http.MethodPost, "/keygen/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code,
		"LDAP-only user should not be able to login via keygen (no local account); body: "+rr.Body.String())
}
