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
	assert.NotEqual(t, firstKey, newKey, "regenerated key should differ from original")
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
