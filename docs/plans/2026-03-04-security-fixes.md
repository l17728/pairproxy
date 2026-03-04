# Security Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 修复 TEST_REPORT.md 中确认的 8 个安全/质量问题，补充对应测试用例，刷新文档。

**Architecture:**
- IP 欺骗修复引入可信代理 CIDR 配置（`auth.trusted_proxies`），`realIP()` 仅在请求来自可信代理时才读取 XFF/X-Real-IP，否则直接用 `RemoteAddr`。
- Reporter 上下文传播：`sendHeartbeat(ctx)` / `ReportUsage(ctx, records)` 从 `loop(ctx)` 透传，无需存字段。
- 其余修复均为局部改动（错误处理、移除无效调用、统一 logger）。

**Tech Stack:** Go 1.24, `net` (CIDR parsing), `go.uber.org/zap`, Go testing

---

## Task 1: Config — trusted_proxies + worker shared_secret 校验

**Files:**
- Modify: `internal/config/config.go`

**Step 1: 在 `SProxyAuth` 添加 `TrustedProxies` 字段**

在 `SProxyAuth` 结构体中（`JWTSecret` 字段之后）新增：

```go
TrustedProxies []string `yaml:"trusted_proxies"` // CIDR 列表，仅来自这些代理的请求才信任 XFF；空=不信任任何代理
```

**Step 2: 在 `Validate()` 补充 worker shared_secret 校验**

找到现有的 worker 角色校验（约 215 行）：
```go
if c.Cluster.Role == "worker" && c.Cluster.Primary == "" {
    errs = append(errs, "cluster.primary is required when cluster.role is \"worker\"")
}
```
在其后立即添加：
```go
if c.Cluster.Role == "worker" && c.Cluster.SharedSecret == "" {
    errs = append(errs, "cluster.shared_secret is required when cluster.role is \"worker\"")
}
```

**Step 3: 运行现有配置测试，确认不破坏已有用例**

```bash
"C:/Program Files/Go/bin/go.exe" test ./internal/config/... -v -count=1
```
Expected: 全部 PASS

**Step 4: 为 shared_secret 校验写测试**

在 `internal/config/` 找到或新建测试文件（如 `config_validate_test.go`，若已有 validate 测试则在其中追加）。

添加测试：
```go
func TestValidate_WorkerRequiresSharedSecret(t *testing.T) {
    cfg := &SProxyFullConfig{}
    cfg.Listen.Port = 9000
    cfg.Auth.JWTSecret = "secret"
    cfg.Database.Path = "/tmp/test.db"
    cfg.LLM.Targets = []LLMTarget{{URL: "http://llm", APIKey: "key"}}
    cfg.Cluster.Role = "worker"
    cfg.Cluster.Primary = "http://primary:9000"
    cfg.Cluster.SharedSecret = "" // 故意留空

    err := cfg.Validate()
    if err == nil {
        t.Fatal("expected validation error for missing shared_secret")
    }
    if !strings.Contains(err.Error(), "shared_secret") {
        t.Errorf("error should mention shared_secret, got: %v", err)
    }
}

func TestValidate_WorkerWithSharedSecret_OK(t *testing.T) {
    cfg := &SProxyFullConfig{}
    cfg.Listen.Port = 9000
    cfg.Auth.JWTSecret = "secret"
    cfg.Database.Path = "/tmp/test.db"
    cfg.LLM.Targets = []LLMTarget{{URL: "http://llm", APIKey: "key"}}
    cfg.Cluster.Role = "worker"
    cfg.Cluster.Primary = "http://primary:9000"
    cfg.Cluster.SharedSecret = "my-secret"

    if err := cfg.Validate(); err != nil {
        t.Errorf("unexpected error: %v", err)
    }
}
```

**Step 5: 运行新测试**
```bash
"C:/Program Files/Go/bin/go.exe" test ./internal/config/... -v -run TestValidate_Worker -count=1
```
Expected: 2 个测试 PASS

**Step 6: Commit**
```bash
git add internal/config/config.go
git add internal/config/
git commit -m "feat(config): add trusted_proxies to SProxyAuth; require shared_secret for worker role"
```

---

## Task 2: IP 欺骗修复 — realIP() + AuthConfig + main.go 接线

**Files:**
- Modify: `internal/api/login_limiter.go`
- Modify: `internal/api/auth_handler.go`
- Modify: `cmd/sproxy/main.go`

### 2a: 修改 `login_limiter.go`

**Step 1: 修改 `realIP` 签名，添加辅助函数**

将整个 `realIP` 函数（121-139 行）替换为：

```go
// realIP 从请求中提取真实客户端 IP。
// 当且仅当请求来自 trustedProxies 中的某个 CIDR 时，才信任 X-Forwarded-For / X-Real-IP。
// trustedProxies 为空时永不信任代理头（最安全的默认行为）。
func realIP(r *http.Request, trustedProxies []net.IPNet) string {
	remote := extractRemoteHost(r.RemoteAddr)
	if len(trustedProxies) > 0 {
		if ip := net.ParseIP(remote); ip != nil && inTrustedProxies(ip, trustedProxies) {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				parts := strings.SplitN(xff, ",", 2)
				if client := strings.TrimSpace(parts[0]); client != "" {
					return client
				}
			}
			if xri := r.Header.Get("X-Real-IP"); xri != "" {
				return strings.TrimSpace(xri)
			}
		}
	}
	return remote
}

// extractRemoteHost 从 "IP:port" 形式的地址中提取 IP 部分。
func extractRemoteHost(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[:idx]
	}
	return addr
}

// inTrustedProxies 检查 ip 是否属于任意一个 CIDR。
func inTrustedProxies(ip net.IP, proxies []net.IPNet) bool {
	for i := range proxies {
		if proxies[i].Contains(ip) {
			return true
		}
	}
	return false
}
```

同时在文件顶部 import 中添加 `"net"`（与 `"net/http"` 同组）。

### 2b: 修改 `auth_handler.go`

**Step 2: 在 `AuthConfig` 加入已解析的 CIDR 列表**

```go
type AuthConfig struct {
    AccessTokenTTL  time.Duration
    RefreshTokenTTL time.Duration
    TrustedProxies  []net.IPNet // pre-parsed from config.SProxyAuth.TrustedProxies
}
```

同时在 `auth_handler.go` 顶部 import 添加 `"net"`（与 `"net/http"` 同组）。

**Step 3: 更新 `handleLogin` 中的 `realIP` 调用**

```go
clientIP := realIP(r, h.cfg.TrustedProxies)
```

### 2c: 修改 `main.go` 接线（约 414-419 行）

**Step 4: 解析 CIDR 并注入 AuthConfig**

找到以下代码段（约 414-419 行）：
```go
authCfg := api.AuthConfig{
    AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
    RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
}
```

替换为：
```go
var trustedProxies []net.IPNet
for _, cidr := range cfg.Auth.TrustedProxies {
    _, ipNet, err := net.ParseCIDR(cidr)
    if err != nil {
        logger.Warn("invalid trusted_proxy CIDR, skipping",
            zap.String("cidr", cidr), zap.Error(err))
        continue
    }
    trustedProxies = append(trustedProxies, *ipNet)
}
authCfg := api.AuthConfig{
    AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
    RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
    TrustedProxies:  trustedProxies,
}
```

同时在 `main.go` 顶部 import 列表确认已有 `"net"` 包（若无则添加）。

**Step 5: 编译检查**
```bash
"C:/Program Files/Go/bin/go.exe" build ./...
```
Expected: 无编译错误

**Step 6: Commit**
```bash
git add internal/api/login_limiter.go internal/api/auth_handler.go cmd/sproxy/main.go
git commit -m "fix(security): realIP() only trusts XFF from configured trusted proxies"
```

---

## Task 3: 更新登录限流测试

**Files:**
- Modify: `internal/api/login_limiter_test.go`

**Step 1: 删除三个漏洞确认测试，替换为正向安全测试**

删除以下测试函数（约 159-273 行）：
- `TestIPSpoofing_Vulnerability`
- `TestIPSpoofing_BypassRateLimit`
- `TestIPSpoofing_LockoutBypass`

同时更新现有的两个 `TestRealIP_*` 测试以传入新参数：

```go
func TestRealIP_TrustedProxy_UsesXFF(t *testing.T) {
    _, cidr, _ := net.ParseCIDR("10.0.0.0/8")
    proxies := []net.IPNet{*cidr}

    req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
    req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.1")
    req.RemoteAddr = "10.0.0.2:12345" // 来自可信代理

    ip := realIP(req, proxies)
    if ip != "203.0.113.1" {
        t.Errorf("realIP = %q, want %q (should use XFF from trusted proxy)", ip, "203.0.113.1")
    }
}

func TestRealIP_UntrustedProxy_IgnoresXFF(t *testing.T) {
    _, cidr, _ := net.ParseCIDR("10.0.0.0/8")
    proxies := []net.IPNet{*cidr}

    req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
    req.Header.Set("X-Forwarded-For", "1.2.3.4") // 攻击者伪造的 XFF
    req.RemoteAddr = "203.0.113.99:12345"          // 非可信代理 IP

    ip := realIP(req, proxies)
    if ip != "203.0.113.99" {
        t.Errorf("realIP = %q, want %q (should ignore XFF from untrusted proxy)", ip, "203.0.113.99")
    }
}

func TestRealIP_EmptyTrustedProxies_AlwaysRemoteAddr(t *testing.T) {
    req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
    req.Header.Set("X-Forwarded-For", "1.2.3.4")
    req.RemoteAddr = "5.6.7.8:12345"

    ip := realIP(req, nil) // 空列表：永不信任 XFF
    if ip != "5.6.7.8" {
        t.Errorf("realIP = %q, want %q (empty proxies should always use RemoteAddr)", ip, "5.6.7.8")
    }
}

func TestRealIP_RemoteAddr_NoHeader(t *testing.T) {
    req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
    req.RemoteAddr = "172.16.0.5:54321"

    ip := realIP(req, nil)
    if ip != "172.16.0.5" {
        t.Errorf("realIP = %q, want %q", ip, "172.16.0.5")
    }
}

// TestRateLimiter_SpoofedXFF_BlocksRealIP 验证修复后：
// 攻击者无论如何伪造 XFF，限流器始终基于真实 RemoteAddr 锁定。
func TestRateLimiter_SpoofedXFF_BlocksRealIP(t *testing.T) {
    l := NewLoginLimiter(3, time.Minute, 5*time.Minute)
    // 无可信代理配置（空列表），realIP 始终返回 RemoteAddr
    var proxies []net.IPNet

    attackerRealIP := "1.2.3.4"

    // 攻击者每次换一个伪造 XFF，但 RemoteAddr 不变
    spoofHeaders := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
    for _, xff := range spoofHeaders {
        req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
        req.Header.Set("X-Forwarded-For", xff)
        req.RemoteAddr = attackerRealIP + ":12345"

        ip := realIP(req, proxies)
        l.RecordFailure(ip) // 每次都基于真实 IP 记录失败
    }

    // 3 次失败后，真实 IP 应被锁定
    allowed, _ := l.Check(attackerRealIP)
    if allowed {
        t.Error("real IP should be locked after 3 failures regardless of spoofed XFF headers")
    }
}
```

在文件顶部 import 添加 `"net"`。

**Step 2: 运行全量 api 包测试**
```bash
"C:/Program Files/Go/bin/go.exe" test ./internal/api/... -v -count=1
```
Expected: 全部 PASS（含新增 5 个测试）

**Step 3: Commit**
```bash
git add internal/api/login_limiter_test.go
git commit -m "test(security): replace vulnerability-documenting tests with fix-verification tests"
```

---

## Task 4: Reporter — 透传 context

**Files:**
- Modify: `internal/cluster/reporter.go`
- Modify: `internal/cluster/reporter_test.go`

### 4a: 修改 reporter.go

**Step 1: 更新 `sendHeartbeat` 签名**

将 `func (r *Reporter) sendHeartbeat()` 改为 `func (r *Reporter) sendHeartbeat(ctx context.Context)`。

在函数体内第 129 行：
```go
// 旧代码：
req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(data))
// 新代码：
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
```

**Step 2: 更新 `loop` 中的调用**

```go
// 旧代码（启动时立即注册）：
r.sendHeartbeat()
// ticker 处：
r.sendHeartbeat()

// 新代码：
r.sendHeartbeat(ctx)
// ticker 处：
r.sendHeartbeat(ctx)
```

**Step 3: 更新 `ReportUsage` 签名**

将 `func (r *Reporter) ReportUsage(records []db.UsageRecord) error` 改为：
```go
func (r *Reporter) ReportUsage(ctx context.Context, records []db.UsageRecord) error
```

在函数体内第 188 行：
```go
// 旧代码：
req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(data))
// 新代码：
req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
```

### 4b: 更新 reporter_test.go 调用方

**Step 4: 更新测试中的 `ReportUsage` 调用**

在 `reporter_test.go` 中找到 `reporter.ReportUsage(records)` 调用，改为：
```go
reporter.ReportUsage(context.Background(), records)
```
并在测试文件 import 中添加 `"context"`（若未有）。

**Step 5: 编译 + 测试**
```bash
"C:/Program Files/Go/bin/go.exe" build ./...
"C:/Program Files/Go/bin/go.exe" test ./internal/cluster/... -v -count=1
```
Expected: 编译通过，全部 PASS

**Step 6: Commit**
```bash
git add internal/cluster/reporter.go internal/cluster/reporter_test.go
git commit -m "fix(cluster): propagate context in Reporter HTTP requests instead of context.Background()"
```

---

## Task 5: admin_llm_handler — 修复错误信息泄露

**Files:**
- Modify: `internal/api/admin_llm_handler.go`

**Step 1: 替换 4 处 `err.Error()` 暴露**

在 `admin_llm_handler.go` 中，全局搜索：
```
writeJSONError(w, http.StatusInternalServerError, "db_error", err.Error())
```

每处替换为（在对应的 `h.logger.Error(...)` 之后）：
```go
writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
```

共 4 处（约行 99、137、177、223），每处已有前置的 `h.logger.Error(...)` 记录详细错误，只需修改最后一个参数。

**Step 2: 确认 `h.logger.Error` 记录了足够信息**

确认每处被修改行的前一行是类似：
```go
h.logger.Error("...", zap.String("..."), zap.Error(err))
```
若有遗漏，补充 logger 调用。

**Step 3: 编译 + 测试**
```bash
"C:/Program Files/Go/bin/go.exe" build ./internal/api/...
"C:/Program Files/Go/bin/go.exe" test ./internal/api/... -v -count=1
```
Expected: 编译通过，全部 PASS

**Step 4: Commit**
```bash
git add internal/api/admin_llm_handler.go
git commit -m "fix(security): do not expose raw db errors to API clients in LLM handler"
```

---

## Task 6: main.go — 资源泄漏 + JSON解码 + 无效 AddCommand

**Files:**
- Modify: `cmd/sproxy/main.go`

### 6a: 修复资源泄漏（约 1752-1760 行）

**Step 1: 用正确错误处理替换忽略写法**

找到（约 1752-1760 行）：
```go
// 创建当前 DB 的安全备份
safeBak := dst + ".pre-restore"
if _, err := os.Stat(dst); err == nil {
    in, _ := os.Open(dst)
    out, _ := os.Create(safeBak)
    io.Copy(out, in) //nolint:errcheck
    in.Close()
    out.Close()
    logger.Info("pre-restore backup saved", zap.String("path", safeBak))
}
```

替换为：
```go
// 创建当前 DB 的安全备份
safeBak := dst + ".pre-restore"
if _, err := os.Stat(dst); err == nil {
    in, err := os.Open(dst)
    if err != nil {
        return fmt.Errorf("open current database for backup: %w", err)
    }
    out, err := os.Create(safeBak)
    if err != nil {
        in.Close()
        return fmt.Errorf("create pre-restore backup file: %w", err)
    }
    if _, err := io.Copy(out, in); err != nil {
        out.Close()
        in.Close()
        os.Remove(safeBak)
        return fmt.Errorf("copy database to backup: %w", err)
    }
    if err := out.Close(); err != nil {
        in.Close()
        return fmt.Errorf("close backup file: %w", err)
    }
    in.Close()
    logger.Info("pre-restore backup saved", zap.String("path", safeBak))
}
```

### 6b: 修复 JSON 解码错误（约 2830 行）

**Step 2: 找到以下代码**
```go
json.NewDecoder(resp.Body).Decode(&status)
resp.Body.Close()
```

替换为：
```go
if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
    resp.Body.Close()
    fmt.Printf("Warning: failed to decode drain status response: %v\n", err)
    continue
}
resp.Body.Close()
```

### 6c: 删除无效 AddCommand（约 2135 行）

**Step 3: 找到**
```go
adminAuditCmd.AddCommand() // 顶层命令，直接执行
```

替换为（仅保留有意义的行）：
```go
adminAuditCmd.Flags().IntVar(&adminAuditLimit, "limit", 100, "max number of records to show")
```

即删除 `adminAuditCmd.AddCommand()` 这一行，不改动后面的 `Flags()` 调用。

**Step 4: 编译检查**
```bash
"C:/Program Files/Go/bin/go.exe" build ./cmd/sproxy/...
```
Expected: 无编译错误

**Step 5: Commit**
```bash
git add cmd/sproxy/main.go
git commit -m "fix: proper error handling in restore backup; fix json decode; remove no-op AddCommand"
```

---

## Task 7: main.go — closeGormDB 使用真实 logger

**Files:**
- Modify: `cmd/sproxy/main.go`

**Step 1: 替换全部 `closeGormDB(zap.NewNop(), ...)` 调用**

全局搜索（共 10 处，行号约 873, 940, 1116, 1161, 1215, 1325, 1487, 1535, 1809, 1869）：
```go
defer closeGormDB(zap.NewNop(), database)
```

全部替换为：
```go
defer closeGormDB(logger, database)
```

每处的 `logger` 变量均在同一函数内 defer 语句之前已定义（可通过 grep 确认：若某处 `logger` 在 defer 之后才赋值，则使用闭包 `defer func() { closeGormDB(logger, database) }()`）。

**Step 2: 编译确认无误**
```bash
"C:/Program Files/Go/bin/go.exe" build ./cmd/sproxy/...
```
Expected: 无编译错误

**Step 3: Commit**
```bash
git add cmd/sproxy/main.go
git commit -m "fix: use real logger in closeGormDB defers instead of zap.NewNop()"
```

---

## Task 8: 全量测试 + TEST_REPORT.md 更新

**Files:**
- Modify: `docs/TEST_REPORT.md`

**Step 1: 运行全量测试（带竞态检测）**

```bash
"C:/Program Files/Go/bin/go.exe" test ./... -race -count=1 -timeout 120s
```
Expected: 全部 PASS，无竞态告警

**Step 2: 更新 TEST_REPORT.md 执行摘要**

将报告顶部的执行摘要表改为：

```markdown
| 类别 | 数量 | 状态 |
|------|------|------|
| 严重问题 (Critical) | 3 | 全部已修复 |
| 高危问题 (High) | 3 | 2个已修复，1个(事务)待修复 |
| 中危问题 (Medium) | 2 | 全部已修复 |
| 低危问题 (Low) | 2 | 全部已修复 |
| **总计** | **10** | **8个已修复，1个已验证安全，1个待修复** |
```

（删除 Low #11 这一行，因为其在当前代码中不存在）

**Step 3: 更新各问题的状态标记**

逐一更新每个问题的状态说明：
- Critical #1 死锁：标注 `✅ 设计已安全（goroutine 异步 + 锁外调用），无需修复`
- Critical #2 IP欺骗：标注 `✅ 已修复 — realIP() 引入可信代理校验`
- Critical #3 资源泄漏：标注 `✅ 已修复 — restore 备份步骤完整错误处理`
- High #4 Context：标注 `✅ 已修复 — sendHeartbeat(ctx) / ReportUsage(ctx, records)`
- High #5 事务缺失：保留原状，标注 `⏸️ 待后续 Sprint 处理（影响有限，审计写入失败仅记录 warn 日志）`
- High #6 错误泄露：标注 `✅ 已修复 — admin_llm_handler 返回通用错误消息`；并更正位置为 `admin_llm_handler.go`
- Medium #7 JSON解码：标注 `✅ 已修复 — drain wait 命令检查解码错误`
- Medium #8 配置验证：标注 `✅ 已修复 — worker 模式验证 shared_secret 非空`
- Low #9 空 AddCommand：标注 `✅ 已修复 — 删除无参调用`
- Low #10 DB关闭日志：标注 `✅ 已修复 — 使用真实 logger`
- Low #11：**整条删除**（当前代码中不存在此问题）

**Step 4: 更新报告元数据**

```markdown
| 属性 | 值 |
|------|-----|
| 报告版本 | v2.0 |
| 生成时间 | 2026-03-04 |
| 代码版本 | main@HEAD |
| Go版本 | 1.24.0 |
| 测试框架 | Go testing |
```

**Step 5: Final commit**
```bash
git add docs/TEST_REPORT.md docs/plans/2026-03-04-security-fixes.md
git commit -m "docs: update TEST_REPORT.md to reflect all security fixes; add implementation plan"
```
