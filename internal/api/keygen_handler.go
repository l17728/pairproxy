package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/keygen"
)

// KeygenHandler 提供用户自助 API Key 生成功能。
//
// 路由：
//   - GET  /keygen/              — 静态 HTML 页面
//   - POST /keygen/api/login     — 用户名+密码登录，返回 key + session token
//   - POST /keygen/api/regenerate — 用 session token 重新生成 key
//
// 与 Dashboard 完全独立：使用普通用户密码，不使用管理员密码。
type KeygenHandler struct {
	logger       *zap.Logger
	userRepo     *db.UserRepo
	jwtMgr       *auth.Manager
	keygenSecret string
	isWorkerNode bool
}

// NewKeygenHandler 创建 KeygenHandler。
func NewKeygenHandler(logger *zap.Logger, userRepo *db.UserRepo, jwtMgr *auth.Manager, keygenSecret string) *KeygenHandler {
	return &KeygenHandler{
		logger:       logger.Named("keygen_handler"),
		userRepo:     userRepo,
		jwtMgr:       jwtMgr,
		keygenSecret: keygenSecret,
	}
}

// SetWorkerMode 设置 Worker 节点模式；Worker 节点不允许写操作（API Key 生成/重置）。
//
// 注意：必须在 RegisterRoutes 之前调用，因为封锁逻辑在路由注册时分支，
// 而不是运行时中间件判断。调用顺序颠倒会导致封锁静默失效。
func (h *KeygenHandler) SetWorkerMode(isWorker bool) {
	h.isWorkerNode = isWorker
}

// RegisterRoutes 注册 /keygen/ 相关路由。
func (h *KeygenHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /keygen/", h.handleStaticPage)
	if h.isWorkerNode {
		// Worker 节点：写端点返回 403，引导用户到 Primary 节点操作
		blockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.logger.Warn("blocked keygen write operation on worker node",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)
			writeKeygenError(w, http.StatusForbidden, "worker_read_only",
				"API key operations are not available on worker nodes; please use the primary node")
		})
		mux.Handle("POST /keygen/api/login", blockHandler)
		mux.Handle("POST /keygen/api/regenerate", blockHandler)
	} else {
		mux.HandleFunc("POST /keygen/api/login", h.handleLogin)
		mux.HandleFunc("POST /keygen/api/regenerate", h.handleRegenerate)
	}
}

// keygenLoginRequest 登录请求体
type keygenLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// keygenLoginResponse 登录响应体
type keygenLoginResponse struct {
	Username  string `json:"username"`
	Key       string `json:"key"`
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
}

// keygenRegenerateResponse 重新生成 key 响应体
type keygenRegenerateResponse struct {
	Username string `json:"username"`
	Key      string `json:"key"`
	Message  string `json:"message"`
}

func (h *KeygenHandler) handleStaticPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(keygenHTML))
}

func (h *KeygenHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req keygenLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("keygen login: invalid request body", zap.Error(err))
		writeKeygenError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		h.logger.Warn("keygen login: missing required fields",
			zap.Bool("has_username", req.Username != ""),
			zap.Bool("has_password", req.Password != ""),
		)
		writeKeygenError(w, http.StatusBadRequest, "missing_fields", "username and password required")
		return
	}

	// 查询用户（仅本地账户）
	user, err := h.userRepo.GetByUsernameAndProvider(req.Username, "local")
	if err != nil {
		h.logger.Error("keygen login: db error", zap.String("username", req.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "internal_error", "user lookup failed")
		return
	}
	if user == nil || !user.IsActive {
		h.logger.Warn("keygen login: user not found or inactive", zap.String("username", req.Username))
		writeKeygenError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}

	// 验证密码
	if !auth.VerifyPassword(h.logger, user.PasswordHash, req.Password) {
		h.logger.Warn("keygen login: wrong password", zap.String("username", req.Username))
		writeKeygenError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}

	// 生成 API Key
	apiKey, err := keygen.GenerateKey(req.Username, []byte(h.keygenSecret))
	if err != nil {
		h.logger.Error("keygen login: key generation failed",
			zap.String("username", req.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate API key")
		return
	}

	// 生成 session token（1小时有效）
	groupIDStr := ""
	if user.GroupID != nil {
		groupIDStr = *user.GroupID
	}
	token, err := h.jwtMgr.Sign(auth.JWTClaims{
		UserID:   user.ID,
		Username: req.Username,
		GroupID:  groupIDStr,
		Role:     "user",
	}, time.Hour)
	if err != nil {
		h.logger.Error("keygen login: token issue failed", zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "token_error", "session token generation failed")
		return
	}

	h.logger.Info("keygen login: success",
		zap.String("username", req.Username),
		zap.String("user_id", user.ID),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keygenLoginResponse{
		Username:  req.Username,
		Key:       apiKey,
		Token:     token,
		ExpiresIn: 3600,
	})
}

func (h *KeygenHandler) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		h.logger.Warn("keygen regenerate: missing or malformed Authorization header",
			zap.String("remote_addr", r.RemoteAddr),
		)
		writeKeygenError(w, http.StatusUnauthorized, "missing_token", "Authorization: Bearer <token> required")
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := h.jwtMgr.Parse(tokenStr)
	if err != nil {
		h.logger.Warn("keygen regenerate: invalid session token", zap.Error(err))
		writeKeygenError(w, http.StatusUnauthorized, "session_expired", "会话已过期，请重新登录")
		return
	}

	apiKey, err := keygen.GenerateKey(claims.Username, []byte(h.keygenSecret))
	if err != nil {
		h.logger.Error("keygen regenerate: key generation failed",
			zap.String("username", claims.Username), zap.Error(err))
		writeKeygenError(w, http.StatusInternalServerError, "key_gen_error", "failed to generate API key")
		return
	}

	h.logger.Info("keygen regenerate: new key generated",
		zap.String("username", claims.Username),
		zap.String("user_id", claims.UserID),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(keygenRegenerateResponse{
		Username: claims.Username,
		Key:      apiKey,
		Message:  "新 Key 已生成",
	})
}

func writeKeygenError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

// keygenHTML 是内嵌的 Key 生成页面（避免静态文件依赖）。
const keygenHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>PairProxy Key Generator</title>
<style>
  body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;max-width:640px;margin:60px auto;padding:0 20px;color:#333}
  h1{color:#1a1a2e;font-size:1.6rem}
  label{display:block;margin:10px 0 4px;font-weight:500}
  input{width:100%;padding:8px 12px;border:1px solid #ddd;border-radius:6px;font-size:14px;box-sizing:border-box}
  button{padding:10px 24px;background:#4f46e5;color:#fff;border:none;border-radius:6px;cursor:pointer;font-size:14px;margin:4px 4px 4px 0}
  button:hover{background:#4338ca}
  button.secondary{background:#6b7280}
  button.secondary:hover{background:#4b5563}
  .key-box{font-family:monospace;background:#f8f9fa;border:1px solid #e9ecef;padding:14px;border-radius:6px;word-break:break-all;font-size:13px;margin:8px 0}
  .section{border-top:1px solid #eee;margin-top:24px;padding-top:16px}
  pre{background:#1e1e2e;color:#cdd6f4;padding:14px;border-radius:6px;font-size:12px;overflow-x:auto}
  .hidden{display:none}
  .error{color:#dc2626;font-size:13px;margin-top:6px}
</style>
</head>
<body>
<h1>PairProxy Key Generator</h1>

<div id="login-section">
  <label>用户名</label><input type="text" id="username" autocomplete="username">
  <label>密码</label><input type="password" id="password" autocomplete="current-password">
  <br><br>
  <button onclick="login()">登 录</button>
  <div class="error" id="login-error"></div>
</div>

<div id="key-section" class="hidden">
  <p>欢迎，<strong id="welcome-name"></strong>！</p>
  <p>您的 API Key：</p>
  <div class="key-box" id="api-key-display"></div>
  <button onclick="copyKey()">复制</button>
  <button class="secondary" onclick="regenerate()">重新生成</button>

  <div class="section">
    <h3>Claude Code 配置</h3>
    <pre id="cc-snippet"></pre>
    <h3>OpenCode 配置</h3>
    <pre id="oc-snippet"></pre>
  </div>

  <button class="secondary" style="margin-top:16px" onclick="logout()">退出登录</button>
</div>

<script>
const BASE = window.location.origin;
let sessionToken = '';
let currentKey = '';

async function login() {
  const username = document.getElementById('username').value.trim();
  const password = document.getElementById('password').value;
  document.getElementById('login-error').textContent = '';
  try {
    const r = await fetch('/keygen/api/login', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({username, password})
    });
    const data = await r.json();
    if (!r.ok) { document.getElementById('login-error').textContent = data.message || '登录失败'; return; }
    sessionToken = data.token;
    showKey(data.username, data.key);
  } catch(e) { document.getElementById('login-error').textContent = '网络错误'; }
}

function showKey(username, key) {
  currentKey = key;
  document.getElementById('login-section').classList.add('hidden');
  document.getElementById('key-section').classList.remove('hidden');
  document.getElementById('welcome-name').textContent = username;
  document.getElementById('api-key-display').textContent = key;
  document.getElementById('cc-snippet').textContent =
    'export ANTHROPIC_BASE_URL=' + BASE + '/anthropic\nexport ANTHROPIC_API_KEY=' + key;
  document.getElementById('oc-snippet').textContent =
    'export OPENAI_BASE_URL=' + BASE + '/v1\nexport OPENAI_API_KEY=' + key;
}

function copyKey() {
  navigator.clipboard.writeText(currentKey).then(() => alert('已复制到剪贴板'));
}

async function regenerate() {
  if (!confirm('重新生成后旧 Key 将失效。继续？')) return;
  const r = await fetch('/keygen/api/regenerate', {
    method: 'POST',
    headers: {'Authorization': 'Bearer ' + sessionToken}
  });
  const data = await r.json();
  if (!r.ok) { alert(data.message || '重新生成失败'); return; }
  showKey(document.getElementById('welcome-name').textContent, data.key);
  alert('新 Key 已生成');
}

function logout() {
  sessionToken = ''; currentKey = '';
  document.getElementById('key-section').classList.add('hidden');
  document.getElementById('login-section').classList.remove('hidden');
  document.getElementById('password').value = '';
}
</script>
</body>
</html>`
