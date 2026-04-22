# PairProxy 多协议直连设计文档

**版本**: v1.0  
**日期**: 2026-03-11  
**状态**: 设计阶段

---

## 1. 背景与动机

### 1.1 当前架构的局限

当前 PairProxy 采用 **cproxy + sproxy** 双代理架构：

```
Claude Code → cproxy (本地JWT管理) → sproxy → Anthropic
```

这种架构的优势：
- JWT 自动刷新，用户体验好
- 本地缓存，减少网络依赖

但这种架构也存在问题：
- **用户需安装 cproxy**：增加了分发和维护成本
- **配置复杂**：需要登录、启动守护进程
- **不适合 CI/CD 场景**：自动化流程难以处理 JWT 刷新

### 1.2 新需求

用户希望**无需安装 cproxy**，直接通过 API Key 接入 sproxy：

```
Claude Code/OpenCode → sproxy (直连) → Anthropic/OpenAI
```

**核心价值**：
- 零客户端安装
- 标准环境变量配置
- 支持多种协议（Anthropic/OpenAI）

---

## 2. 设计目标

### 2.1 主要目标

1. **零侵入接入**：用户仅需配置 `BASE_URL` 和 `API_KEY`
2. **多协议支持**：同时支持 Anthropic Messages API 和 OpenAI Chat Completions API
3. **路径隔离**：通过不同 URL 路径区分协议，避免冲突
4. **向后兼容**：现有 cproxy 模式完全保留

### 2.2 非目标

- 不替换现有 JWT 认证体系
- 不修改现有配额/审计逻辑
- 不强制用户迁移到新模式

---

## 3. 架构设计

### 3.1 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         sproxy (端口 9000)                       │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────┐  │
│  │ 现有路径      │  │ 新增直连路径  │  │ 公共服务              │  │
│  ├──────────────┤  ├──────────────┤  ├──────────────────────┤  │
│  │ /v1/messages │  │ /v1/*        │  │ /health              │  │
│  │   JWT 认证   │  │   API Key    │  │ /metrics             │  │
│  │   (cproxy)   │  │   (OpenAI)   │  │ /dashboard           │  │
│  ├──────────────┤  ├──────────────┤  ├──────────────────────┤  │
│  │ /auth/*      │  │ /anthropic/* │  │ /api/admin/*         │  │
│  │ /api/user/*  │  │   API Key    │  │ /keygen/*            │  │
│  │              │  │   (Anthropic)│  │                      │  │
│  └──────────────┘  └──────────────┘  └──────────────────────┘  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 路由冲突解决方案

**问题**：`/v1/*` 路由会拦截所有 `/v1/` 开头的请求，与现有 cproxy 模式的 `/v1/messages` 冲突。

**解决方案**：通过 **认证头类型** 区分两种模式：

| 模式 | 请求路径 | 认证头 | 处理方式 |
|------|---------|--------|---------|
| **cproxy 模式** | `/v1/messages` | `X-PairProxy-Auth: <JWT>` | JWT 认证 → 复用现有逻辑 |
| **直连模式** | `/v1/chat/completions` | `Authorization: Bearer <key>` | Key 认证 → 直连处理器 |

**路由优先级**：
1. 精确路径优先（`/v1/messages` → cproxy 模式）
2. 前缀匹配其次（`/v1/*` → 直连模式，但需检查认证头）

```go
// 混合路由处理
func handleV1Routes(w http.ResponseWriter, r *http.Request) {
    // 1. 检查是否有 X-PairProxy-Auth 头（cproxy 模式）
    if r.Header.Get("X-PairProxy-Auth") != "" {
        // cproxy 模式：JWT 认证
        cproxyHandler.ServeHTTP(w, r)
        return
    }
    
    // 2. 检查是否有 Authorization: Bearer sk-pp- 头（直连模式）
    if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer sk-pp-") {
        // 直连模式：Key 认证
        directHandler.HandlerOpenAI().ServeHTTP(w, r)
        return
    }
    
    // 3. 无有效认证头
    writeError(w, 401, "missing_auth", "X-PairProxy-Auth or Authorization: Bearer required")
}
```

**最终路由注册**：

```go
// 公共服务
mux.HandleFunc("GET /health", sp.HealthHandler())
mux.HandleFunc("GET /metrics", metricsHandler.Handler())

// 管理服务
mux.Handle("/dashboard/", dashHandler)
mux.Handle("/api/admin/", adminHandler)
mux.Handle("/keygen/", keygenHandler)

// 用户认证（cproxy 登录用）
mux.Handle("/auth/", authHandler)

// 混合路由：同时支持 cproxy 和直连模式
mux.HandleFunc("/v1/", handleV1Routes)        // OpenAI 协议（根据认证头区分）
mux.HandleFunc("/anthropic/", directHandler.HandlerAnthropic())  // Anthropic 协议（仅直连）

// 其他所有请求（cproxy JWT 认证）
mux.Handle("/", cproxyHandler)
```

### 3.3 请求流程

#### Anthropic 协议直连

```
1. 用户请求（Claude Code 等客户端自动发送）
   POST https://sproxy.company.com/anthropic/v1/messages
   x-api-key: sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6
   anthropic-version: 2023-06-01

2. sproxy 处理
   ├─ 路由匹配: /anthropic/* → DirectAnthropicHandler
   ├─ 认证: APIKeyAuthMiddleware (从 x-api-key 提取 Key)
   ├─ 路径重写: /anthropic/v1/messages → /v1/messages
   ├─ 用户映射: API Key → UserID → GroupID
   ├─ 配额检查: 检查用户/分组配额
   ├─ 目标选择: 选择 anthropic provider 的 target
   └─ 代理转发: 替换 x-api-key 为真实 Anthropic Key

3. 上游请求
   POST https://api.anthropic.com/v1/messages
   x-api-key: sk-ant-api03-...
   anthropic-version: 2023-06-01

4. 响应处理
   ├─ Token 统计: 解析 usage 字段
   ├─ 用量记录: 写入 usage_logs
   └─ 返回用户: 原始响应
```

#### OpenAI 协议直连

```
1. 用户请求（OpenCode 等客户端自动发送）
   POST https://sproxy.company.com/v1/chat/completions
   Authorization: Bearer sk-pp-aB9cD2eF5gH8iJ1kL4mN7oP0qR3sT6uV9wX2yZ5

2. sproxy 处理
   ├─ 路由匹配: /v1/* → DirectOpenAIHandler
   ├─ 认证: APIKeyAuthMiddleware (从 Authorization: Bearer 提取 Key)
   ├─ 路径保持: /v1/chat/completions (无需重写)
   ├─ 用户映射: API Key → UserID → GroupID
   ├─ 配额检查: 检查用户/分组配额
   ├─ 自动注入: stream_options.include_usage=true
   ├─ 目标选择: 选择 openai provider 的 target
   └─ 代理转发: 替换 Authorization 为真实 OpenAI Key

3. 上游请求
   POST https://api.openai.com/v1/chat/completions
   Authorization: Bearer sk-...

4. 响应处理
   ├─ Token 统计: 解析 usage 字段 (流式/非流式)
   ├─ 用量记录: 写入 usage_logs
   └─ 返回用户: 原始响应
```

---

## 4. API Key 生成与验证机制

### 4.1 设计理念

**核心思想**：Key **无需存储**，通过**规则验证**识别用户身份。

| 对比项 | 传统方式 | 本设计 |
|--------|---------|--------|
| Key 存储 | 数据库存储 hash | ❌ 无需存储 |
| Key 验证 | 查询数据库比对 | ✅ 规则验证（从 Key 中提取用户名） |
| 优势 | 可随时吊销 | 无状态、可验证、无需数据库查询 |

### 4.2 Key 规则定义

#### 4.2.1 业界标准参考

| Provider | Key 格式 | 长度 | 字符集 |
|----------|---------|------|--------|
| OpenAI | `sk-<48字符>` | 51 | 字母 + 数字 |
| Anthropic | `sk-ant-<80字符>` | 87 | 字母 + 数字 |

**业界惯例**：
- **前缀**：以 `sk-` 开头（Secret Key 的缩写）
- **长度**：通常 50-90 字符
- **字符集**：字母（a-z, A-Z）和数字（0-9）
- **无特殊字符**：不含 `-` `_` 等符号（前缀中的 `-` 除外）

#### 4.2.2 PairProxy Key 规则

**重要**：此 Key **仅在网关处使用**，用于识别用户身份。真正连接 LLM 的 API Key 由管理员在 sproxy 配置中设置。

| 规则项 | 定义 | 说明 |
|--------|------|------|
| **前缀** | `sk-pp-` | 固定前缀，`pp` = PairProxy |
| **总长度** | 54 字符 | 前缀 6 字符 + 主体 48 字符 |
| **主体长度** | 48 字符 | 随机生成 |
| **字符集** | `a-z A-Z 0-9` | 仅字母和数字，无特殊字符 |
| **用户标识** | 内嵌用户名字符 | 用户名的字母和数字打散在 Key 中 |

**Key 结构**：
```
sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6
│  │   └─────────────────────────────────────────┘
│  │                    │
│  │                    └── 48 字符（用户名字符打散 + 随机填充）
│  └─────────────────────── "pp" = PairProxy 标识
└────────────────────────── 标准前缀 "sk-"
```

#### 4.2.3 验证规则

验证 Key 时，网关执行以下检查：

1. **前缀检查**：必须以 `sk-pp-` 开头
2. **长度检查**：总长度必须为 54 字符
3. **字符集检查**：主体部分仅包含 `a-z`, `A-Z`, `0-9`
4. **用户匹配**：Key 中必须包含某活跃用户的所有用户名字符（字母+数字）

#### 4.2.4 与真实 API Key 的区别

| 对比项 | PairProxy Key | 真实 LLM API Key |
|--------|--------------|------------------|
| **用途** | 网关身份识别 | 连接 LLM 服务 |
| **存储位置** | 无需存储（规则验证） | sproxy 配置文件（加密存储） |
| **生成方式** | 根据用户名自动生成 | 从 LLM 提供商获取 |
| **格式** | `sk-pp-<48字符>` | 由 LLM 提供商定义 |
| **验证方** | sproxy 网关 | LLM 提供商 |

**工作流程**：
```
用户请求 → 携带 PairProxy Key → sproxy 验证 → 替换为真实 LLM Key → 转发请求
                  ↓                                      ↓
           网关处验证                           由管理员配置
         （规则验证）                         （sproxy.yaml）
```

### 4.3 Key 生成算法（v2.15.0+，v2.24.7 更新）

v2.15.0 起采用 HMAC-SHA256 确定性生成；v2.24.7 起将派生密钥从共享 `keygen_secret` 改为**用户自己的 `PasswordHash`**。

```go
package keygen

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/base64"
    "fmt"
    "strings"
)

const (
    KeyPrefix  = "sk-pp-"
    KeyBodyLen = 48
    charset    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// GenerateKey 根据用户名和用户 PasswordHash 生成确定性 API Key。
// secret 为该用户的 bcrypt PasswordHash（[]byte），长度 ≥ 32 字节。
// 同一 username + 同一 PasswordHash → 同一 Key；改密码 → 新 PasswordHash → 新 Key。
func GenerateKey(username string, secret []byte) (string, error) {
    if len(secret) < 32 {
        return "", fmt.Errorf("keygen: secret must be at least 32 bytes")
    }
    mac := hmac.New(sha256.New, secret)
    mac.WriteString(username)
    raw := mac.Sum(nil) // 32 bytes

    // Base64URL → 取前 KeyBodyLen 字符，替换非 Base62 字符
    b64 := base64.RawURLEncoding.EncodeToString(raw) // 43 chars
    body := toBase62(b64, KeyBodyLen)
    return KeyPrefix + body, nil
}

// toBase62 将 Base64URL 字符串映射到 Base62 字符集（a-z A-Z 0-9）
func toBase62(s string, n int) string {
    var sb strings.Builder
    for _, c := range s {
        if strings.ContainsRune(charset, c) {
            sb.WriteRune(c)
        }
        if sb.Len() >= n {
            break
        }
    }
    // 若长度不足（极少数情况），补充确定性字符
    for sb.Len() < n {
        sb.WriteByte(charset[sb.Len()%len(charset)])
    }
    return sb.String()[:n]
}
```

> **设计说明**：每个用户使用自己的 bcrypt PasswordHash 作为 HMAC 密钥，实现 per-user 隔离。bcrypt 哈希固定 60 字节，满足 ≥32 字节要求。LDAP 用户 `PasswordHash == ""`，被 `ValidateAndGetUser` 静默跳过，无法持有 sk-pp- Key。

**历史算法（v2.9.0–v2.14.x，已废弃）**：将用户名字符打散到随机位置 + 随机填充，存在碰撞漏洞，已被 HMAC-SHA256 替代。

// 以下为历史代码占位，仅供回滚参考，实际代码已删除

// randomPositions (legacy)
func randomPositions(max, count int) []int {
    if count > max {
        count = max
    }
    
    positions := make(map[int]bool)
    result := make([]int, 0, count)
    
    for len(result) < count {
        n, _ := rand.Int(rand.Reader, big.NewInt(int64(max)))
        pos := int(n.Int64())
        if !positions[pos] {
            positions[pos] = true
            result = append(result, pos)
        }
    }
    
    return result
}
```

### 4.4 Key 验证算法（v2.24.7+）

```go
// IsValidFormat 检查 Key 格式是否有效（前缀、长度、字符集）
func IsValidFormat(key string) bool {
    if !strings.HasPrefix(key, KeyPrefix) {
        return false
    }
    body := key[len(KeyPrefix):]
    if len(body) != KeyBodyLen {
        return false
    }
    for _, c := range body {
        if !strings.ContainsRune(charset, c) {
            return false
        }
    }
    return true
}

// ValidateAndGetUser 验证 Key 并返回对应用户（v2.24.7+）
// 遍历所有活跃用户，用各自 PasswordHash 作为 HMAC 密钥重新派生 Key，比对输入。
// LDAP 用户（PasswordHash == ""）被静默跳过。
func ValidateAndGetUser(key string, users []UserEntry) (*UserEntry, error) {
    if !IsValidFormat(key) {
        return nil, nil
    }
    for i := range users {
        u := &users[i]
        if !u.IsActive || u.PasswordHash == "" {
            continue
        }
        expectedKey, err := GenerateKey(u.Username, []byte(u.PasswordHash))
        if err != nil {
            zap.L().Warn("keygen: skip user due to GenerateKey error",
                zap.String("username", u.Username), zap.Error(err))
            continue
        }
        if key == expectedKey {
            return u, nil
        }
    }
    return nil, nil
}
```

### 4.5 验证流程

```
用户请求: Authorization: Bearer sk-pp-xK8aLm2cNp9qRsTtUvWxYz...
                                    ↓
                           1. 先查 KeyCache（LRU + TTL）
                              命中 → 直接返回缓存用户，跳过 2-5
                                    ↓
                           2. 检查前缀 sk-pp-
                                    ↓
                           3. 检查长度 = 54 字符，字符集合法
                                    ↓
                           4. 从数据库加载所有活跃用户（含 PasswordHash）
                                    ↓
                           5. 遍历用户：GenerateKey(username, PasswordHash)
                              比对派生 Key 与输入 Key
                              LDAP 用户（PasswordHash==""）跳过
                                    ↓
                           6. 找到匹配用户 → 写入 KeyCache → 注入 context
                              未找到 → 返回 401
```

### 4.6 安全考虑

| 问题 | 解决方案 |
|------|---------|
| **共享密钥泄漏** | 已消除：每用户独立 PasswordHash 派生，无共享 secret |
| **Key 相互隔离** | 用户 A 改密码仅影响 A 的 Key，B 不受影响 |
| **暴力破解** | Key 长度 48 字符（Base62），熵 ≈ 287 位，暴力破解不可行 |
| **Key 轮换** | 用户自助改密码即可轮换，无需管理员介入 |
| **旧 Key 即时失效** | 改密码后 `KeyCache.InvalidateByUserID()` 主动清除缓存 |
| **LDAP 用户** | 无本地 PasswordHash，无法持有 sk-pp- Key（静默跳过）|

### 4.7 用户名约束

用户名必须满足：

| 约束 | 规则 |
|------|------|
| 最小长度 | ≥ 4 字符 |
| 字符要求 | 至少包含 1 个字母或数字 |
| 禁止用户名 | `admin`, `root`, `test`, `user` 等保留字 |

### 4.8 Key 缓存策略

为避免每次请求都遍历所有用户，实现 Key → User 的 LRU+TTL 缓存：

```go
// KeyCache 核心方法（v2.24.7+）
//
// Get(key)                    — 查缓存（命中且未过期则返回 CachedUser）
// Set(key, user)              — 写缓存（验证通过后调用）
// InvalidateUser(username)    — 按用户名清除（用于已知旧 key 字符串时）
// InvalidateByUserID(userID)  — 按 UserID 清除（改密码/重置密码后调用）
```

**缓存失效触发点**：

| 场景 | 调用方法 |
|------|---------|
| 用户自助改密码（`/keygen/api/change-password`） | `InvalidateByUserID(userID)` |
| 管理员重置密码（`/api/admin/users/{id}/reset-password`） | `InvalidateByUserID(userID)` |
| 用户被禁用 | 缓存 TTL 后自动失效 |

**推荐配置**：
- 缓存大小：1000-10000 条（根据用户数调整）
- TTL：5-15 分钟（0 = 永不过期，仅限测试）

---

## 5. 数据模型

### 5.1 用户表（现有，无需修改）

```go
// internal/db/models.go

type User struct {
    ID           string     `gorm:"primarykey"`
    Username     string     `gorm:"uniqueIndex;not null"`  // 用于生成 Key
    PasswordHash string     `gorm:"not null"`
    GroupID      *string    `gorm:"index"`
    Group        Group      `gorm:"foreignKey:GroupID"`
    IsActive     bool       `gorm:"default:true"`
    AuthProvider string     `gorm:"default:'local'"`
    ExternalID   string     `gorm:"index"`
    CreatedAt    time.Time
    LastLoginAt  *time.Time
}
```

### 5.2 无需 APIKey 表

由于 Key 通过规则验证，**无需存储 Key**。用户的 Key 可随时重新生成。

### 5.3 Key 缓存（已内置）

`KeyCache`（`internal/keygen/cache.go`）已内置于直连模式请求链路，默认大小 10000 条、TTL 15 分钟。Key 验证命中缓存时直接返回，无需遍历用户列表。密码变更后通过 `InvalidateByUserID()` 主动清除，保证旧 Key 即时失效。

---

## 6. WebUI 用户自助生成 Key

### 6.1 与管理员 Dashboard 的区别

PairProxy 有**两个独立的 WebUI 系统**，面向不同用户群体：

| 对比项 | 管理员 Dashboard | 用户 Key 生成页 |
|--------|-----------------|-----------------|
| **路径** | `/dashboard/` | `/keygen/` |
| **登录页面** | `/dashboard/login` | `/keygen/` |
| **登录凭证** | 管理员密码（无用户名） | 用户名 + 密码 |
| **目标用户** | 管理员 | 普通用户 |
| **主要功能** | 用户管理、分组管理、LLM 配置、用量统计、批量导入等 | 查看 API Key、**自助改密码并更新 Key** |
| **权限范围** | 全局管理权限 | 仅操作自己的 Key 和密码 |

**重要**：两个系统**完全独立**，互不影响：

- 管理员无法通过 `/keygen/` 登录（需要用户名）
- 普通用户无法通过 `/dashboard/` 登录（需要管理员密码）

---

### 6.2 访问地址

```
https://sproxy.company.com/keygen/
```

### 6.3 页面流程

```
┌─────────────────────────────────────────────────────────────────┐
│                    PairProxy Key 生成器                          │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  用户名: [alice        ]    密码: [********        ]      │   │
│  │                                                            │   │
│  │                    [ 登 录 ]                               │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  欢迎, alice!                                                    │
│                                                                  │
│  您的 API Key:                                                   │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6           │   │
│  └──────────────────────────────────────────────────────────┘   │
│                               [ 📋 复制 ]                        │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  修改密码（同步更新 API Key）：                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  当前密码: [**********  ]                                 │   │
│  │  新密码:   [**********  ]  (≥8位)                         │   │
│  │  确认密码: [**********  ]                                 │   │
│  │                   [ 修改密码 ]                             │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ─────────────────────────────────────────────────────────────  │
│                                                                  │
│  使用方法:                                                       │
│                                                                  │
│  Claude Code:                                                    │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  export ANTHROPIC_BASE_URL=https://sproxy.company.com/anthropic
│  │  export ANTHROPIC_API_KEY=sk-pp-xK8aLm2c...              │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  OpenCode:                                                       │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  export OPENAI_BASE_URL=https://sproxy.company.com/v1    │   │
│  │  export OPENAI_API_KEY=sk-pp-xK8aLm2c...                 │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### 6.4 API 端点

#### 登录并获取 Key

```http
POST /keygen/api/login
Content-Type: application/json

{
  "username": "alice",
  "password": "user-password"
}
```

**响应 200**:
```json
{
  "username": "alice",
  "key": "sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6",
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "expires_in": 3600
}
```

**响应 401**:
```json
{
  "error": "invalid_credentials",
  "message": "用户名或密码错误"
}
```

| 字段 | 说明 |
|------|------|
| `key` | 用户的 API Key，由当前 PasswordHash 派生（v2.24.7+） |
| `token` | Session Token（用于 change-password 等需认证操作） |
| `expires_in` | Session 有效期（秒），默认 1 小时 |

#### 自助修改密码并更新 Key（v2.24.7+）

```http
POST /keygen/api/change-password
Authorization: Bearer <session_token>
Content-Type: application/json

{
  "old_password": "current-password",
  "new_password": "new-strong-password"
}
```

**响应 200**:
```json
{
  "key": "sk-pp-<新Key>",
  "message": "密码已更新，新 API Key 已生成"
}
```

改密成功后：① 数据库密码哈希更新 ② 旧 Key 缓存立即清除 ③ 新 Key 直接返回，无需重新登录。

**错误响应**：

| Status | 原因 |
|--------|------|
| 401 | session_token 无效/过期，或旧密码错误 |
| 403 | LDAP 账户（不支持本地密码修改） |
| 503 | Worker 节点（写操作须转发至 Primary） |

#### 重新查看 Key（无需重新登录）

```http
POST /keygen/api/regenerate
Authorization: Bearer <session_token>
```

返回当前 PasswordHash 派生的 Key（与登录时相同，用于刷新显示）。

---

## 7. API 设计

### 7.1 用户接入端点

#### Anthropic 协议

| 端点 | 方法 | 描述 |
|------|------|------|
| `/anthropic/v1/messages` | POST | 创建消息 (非流式/流式) |
| `/anthropic/v1/models` | GET | 列出可用模型 |

**认证**: `x-api-key: sk-pp-<48字符>`（无 Bearer 前缀）

**示例请求**:
```bash
curl -X POST https://sproxy.company.com/anthropic/v1/messages \
  -H "x-api-key: sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "max_tokens": 1024,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

#### OpenAI 协议

| 端点 | 方法 | 描述 |
|------|------|------|
| `/v1/chat/completions` | POST | 创建聊天完成 |
| `/v1/models` | GET | 列出可用模型 |

**认证**: `Authorization: Bearer sk-pp-<48字符>`

**示例请求**:
```bash
curl -X POST https://sproxy.company.com/v1/chat/completions \
  -H "Authorization: Bearer sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

---

## 8. 实现细节

### 8.1 目录结构

```
internal/
├── keygen/
│   ├── generator.go          # 新增: Key 生成算法
│   ├── validator.go          # 新增: Key 验证算法
│   └── cache.go              # 新增: 可选的 Key 缓存
├── proxy/
│   ├── direct_handler.go     # 新增: 直连处理器
│   ├── keyauth_middleware.go # 新增: Key 认证中间件
│   └── sproxy.go             # 现有: 复用代理逻辑
├── api/
│   └── keygen_handler.go     # 新增: Key 生成 WebUI API
└── db/
    └── models.go             # 现有: 用户表（无需修改）
```

### 8.2 核心组件

#### 8.2.1 KeyAuthMiddleware

**重要**：OpenAI 和 Anthropic 协议使用不同的认证头格式：

| 协议 | 认证头 | 格式 |
|------|--------|------|
| OpenAI | `Authorization` | `Bearer <key>` |
| Anthropic | `x-api-key` | 直接 `<key>`（无 Bearer 前缀）|

```go
package proxy

import (
    "github.com/l17728/pairproxy/internal/keygen"
    "github.com/l17728/pairproxy/internal/db"
)

// KeyAuthMiddleware 验证 API Key 并将用户信息注入 context
// 支持两种认证头格式：OpenAI (Authorization: Bearer) 和 Anthropic (x-api-key)
func KeyAuthMiddleware(
    logger *zap.Logger, 
    userRepo *db.UserRepo,
    keyCache *keygen.KeyCache,  // 可选缓存
    next http.Handler,
) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 1. 提取 API Key（支持两种格式）
        token := extractAPIKey(r)
        if token == "" {
            writeError(w, 401, "missing_authorization", 
                "Authorization: Bearer <key> or x-api-key: <key> required")
            return
        }
        
        // 2. 验证 Key 格式（前缀 + 长度 + 字符集）
        if !keygen.IsValidFormat(token) {
            writeError(w, 401, "invalid_key_format", "")
            return
        }
        
        // 3. 检查缓存
        var user *db.User
        if keyCache != nil {
            user = keyCache.Get(token)
        }
        
        // 4. 缓存未命中，从数据库验证
        if user == nil {
            users, _ := userRepo.ListActive()
            user = keygen.ValidateAndGetUser(token, users)
            if user == nil {
                writeError(w, 401, "invalid_api_key", "")
                return
            }
            // 写入缓存
            if keyCache != nil {
                keyCache.Set(token, user)
            }
        }
        
        // 5. 构建 claims (复用现有 JWT claims 结构)
        claims := &auth.JWTClaims{
            UserID:   user.ID,
            Username: user.Username,
            GroupID:  user.GroupID,
        }
        
        // 6. 注入 context
        ctx := context.WithValue(r.Context(), ctxKeyClaims, claims)
        
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// extractAPIKey 从请求头提取 API Key，支持 OpenAI 和 Anthropic 两种格式
func extractAPIKey(r *http.Request) string {
    // OpenAI 格式: Authorization: Bearer <key>
    if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
        return strings.TrimPrefix(auth, "Bearer ")
    }
    
    // Anthropic 格式: x-api-key: <key>（无 Bearer 前缀）
    if key := r.Header.Get("x-api-key"); key != "" {
        return key
    }
    
    return ""
}
```

#### 6.2.2 DirectProxyHandler

```go
package proxy

// DirectProxyHandler 处理 API Key 直连请求
type DirectProxyHandler struct {
    sproxy       *SProxy
    userRepo     *db.UserRepo
    keyCache     *keygen.KeyCache
    quotaChecker *quota.Checker
    logger       *zap.Logger
}

// HandlerAnthropic 返回 Anthropic 协议处理器
func (h *DirectProxyHandler) HandlerAnthropic() http.Handler {
    core := http.HandlerFunc(h.handleAnthropic)
    
    // 中间件链 (与现有 SProxy.Handler() 一致)
    withQuota := quota.NewMiddleware(h.logger, h.quotaChecker, h.userIDFromContext)
    withAuth := KeyAuthMiddleware(h.logger, h.userRepo, h.keyCache, withQuota(core))
    withReqID := RequestIDMiddleware(h.logger, withAuth)
    
    return RecoveryMiddleware(h.logger, withReqID)
}

// HandlerOpenAI 返回 OpenAI 协议处理器
func (h *DirectProxyHandler) HandlerOpenAI() http.Handler {
    core := http.HandlerFunc(h.handleOpenAI)
    
    withQuota := quota.NewMiddleware(h.logger, h.quotaChecker, h.userIDFromContext)
    withAuth := KeyAuthMiddleware(h.logger, h.userRepo, h.keyCache, withQuota(core))
    withReqID := RequestIDMiddleware(h.logger, withAuth)
    
    return RecoveryMiddleware(h.logger, withReqID)
}

func (h *DirectProxyHandler) handleAnthropic(w http.ResponseWriter, r *http.Request) {
    // 1. 路径重写: /anthropic/v1/messages -> /v1/messages
    r.URL.Path = strings.TrimPrefix(r.URL.Path, "/anthropic")
    
    // 2. 获取 context 信息
    claims := ClaimsFromContext(r.Context())
    
    // 3. 复用 SProxy 代理逻辑
    h.sproxy.ServeDirect(w, r, claims, "anthropic")
}

func (h *DirectProxyHandler) handleOpenAI(w http.ResponseWriter, r *http.Request) {
    // 1. 路径无需重写 (已经是 /v1/...)
    
    // 2. 获取 context 信息
    claims := ClaimsFromContext(r.Context())
    
    // 3. 复用 SProxy 代理逻辑
    h.sproxy.ServeDirect(w, r, claims, "openai")
}
```

#### 8.2.3 SProxy.ServeDirect

```go
// ServeDirect 处理直连模式的代理请求
// 复用现有 serveProxy 的核心逻辑
func (sp *SProxy) ServeDirect(
    w http.ResponseWriter, 
    r *http.Request, 
    claims *auth.JWTClaims,
    userProtocol string,  // "openai" 或 "anthropic"
) {
    // 1. 选择 Target（根据用户绑定或负载均衡）
    target := sp.pickTarget(claims.UserID, claims.GroupID)
    
    // 2. 检测是否需要协议转换
    needsConversion := (userProtocol != target.Provider)
    
    // 3. 根据目标 Provider 确定认证头格式
    var authHeader, authValue string
    switch target.Provider {
    case "anthropic":
        authHeader = "x-api-key"
        authValue = target.APIKey
    case "openai", "ollama":
        authHeader = "Authorization"
        authValue = "Bearer " + target.APIKey
    default:
        authHeader = "Authorization"
        authValue = "Bearer " + target.APIKey
    }
    
    // 4. Director 函数
    proxy := &httputil.ReverseProxy{
        Director: func(req *http.Request) {
            // 删除用户的所有认证头
            req.Header.Del("Authorization")
            req.Header.Del("x-api-key")
            req.Header.Del("X-PairProxy-Auth")
            
            // 注入真实的 LLM API Key
            req.Header.Set(authHeader, authValue)
            
            // 路径重写 (anthropic 模式已在 DirectProxyHandler 中处理)
            
            // 设置目标 URL
            req.URL.Scheme = target.Scheme
            req.URL.Host = target.Host
            
            // 协议转换在 ModifyResponse 中处理
        },
        ModifyResponse: func(resp *http.Response) error {
            // 协议转换逻辑（复用现有代码）
            if needsConversion {
                // OpenAI ↔ Anthropic 协议转换
                return sp.handleProtocolConversion(resp, userProtocol, target.Provider)
            }
            return nil
        },
        ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
            sp.logger.Error("proxy error",
                zap.String("user_id", claims.UserID),
                zap.String("target", target.URL),
                zap.Error(err),
            )
            writeError(w, 502, "upstream_error", "upstream request failed")
        },
        FlushInterval: -1,  // SSE 必须立即刷新
    }
    
    proxy.ServeHTTP(w, r)
}
```

### 8.3 路由注册

```go
// cmd/sproxy/main.go

func runStart(cmd *cobra.Command, args []string) error {
    // ... 现有初始化代码 ...
    
    mux := http.NewServeMux()
    
    // 1. 公共服务 (无需认证)
    mux.HandleFunc("GET /health", sp.HealthHandler())
    metricsHandler.RegisterRoutes(mux)
    
    // 2. Key 生成 WebUI（用户登录后自助生成）
    keygenHandler := api.NewKeygenHandler(logger, userRepo, jwtMgr)
    keygenHandler.RegisterRoutes(mux)
    // 注册: /keygen/ (静态页面)
    // 注册: /keygen/api/login
    // 注册: /keygen/api/regenerate
    
    // 3. 管理 API (JWT 认证)
    adminHandler.RegisterRoutes(mux)
    
    // 4. 用户认证 API (登录/刷新/登出，cproxy 使用)
    authHandler.RegisterRoutes(mux)
    
    // 5. 用户自助 API (JWT 认证)
    userHandler.RegisterRoutes(mux)
    
    // 6. 集群 API (Shared Secret 认证)
    if clusterHandler != nil {
        clusterHandler.RegisterRoutes(mux)
    }
    
    // 7. Dashboard (管理员)
    if cfg.Dashboard.Enabled {
        dashHandler.RegisterRoutes(mux)
    }
    
    // 8. Anthropic 协议直连（仅 API Key 认证）
    //    路径: /anthropic/v1/messages
    //    认证: x-api-key: sk-pp-...
    mux.Handle("/anthropic/", directHandler.HandlerAnthropic())
    logger.Info("direct proxy registered",
        zap.String("path", "/anthropic/"),
        zap.String("protocol", "anthropic"),
        zap.String("auth", "apikey"))
    
    // 9. 混合路由：/v1/* 同时支持 cproxy 和直连模式
    //    根据认证头类型自动区分：
    //    - X-PairProxy-Auth: <JWT> → cproxy 模式
    //    - Authorization: Bearer sk-pp-... → 直连模式
    mux.HandleFunc("/v1/", handleV1Routes)
    logger.Info("hybrid route registered",
        zap.String("path", "/v1/"),
        zap.String("modes", "cproxy(JWT) + direct(Key)"))
    
    // 10. 其他所有请求 → cproxy 模式 (JWT 认证)
    mux.Handle("/", sp.Handler())
    
    // ... 启动服务器 ...
}

// handleV1Routes 混合路由处理器：根据认证头区分 cproxy 和直连模式
func handleV1Routes(w http.ResponseWriter, r *http.Request) {
    // 1. 检查 cproxy 模式认证头
    if r.Header.Get("X-PairProxy-Auth") != "" {
        cproxyHandler.ServeHTTP(w, r)
        return
    }
    
    // 2. 检查直连模式认证头
    auth := r.Header.Get("Authorization")
    if strings.HasPrefix(auth, "Bearer sk-pp-") {
        directHandler.HandlerOpenAI().ServeHTTP(w, r)
        return
    }
    
    // 3. 无有效认证头
    writeError(w, 401, "missing_auth",
        "X-PairProxy-Auth (cproxy) or Authorization: Bearer sk-pp-... (direct) required")
}
```

---

## 9. 安全考虑

### 9.1 API Key 安全

| 风险 | 缓解措施 |
|------|----------|
| **Key 泄露** | Key 无需存储，泄露后用户可重新生成新 Key |
| **Key 被滥用** | 配额限制、速率限制、用量监控 |
| **Key 传输** | 强制 HTTPS，传输层加密 |
| **暴力破解** | Key 长度 54 字符，熵约 287 位，暴力破解不可行 |

### 9.2 认证对比

| 特性 | JWT (cproxy) | API Key (直连) |
|------|-------------|---------------|
| 有效期 | 24小时，自动刷新 | 长期有效，用户可重新生成 |
| 存储 | 数据库存储 hash | **无需存储**，规则验证 |
| 撤销 | 实时（黑名单） | 重新生成即失效 |
| 适用场景 | 日常使用 | CI/CD、临时使用 |
| 泄露风险 | 低（短期） | 中（可重新生成） |

### 9.3 建议

1. **生产环境强制 HTTPS**
2. **Key 泄露后立即重新生成**
3. **监控异常用量**，自动告警
4. **定期重新生成 Key**（建议 90 天）

---

## 10. 用户文档

### 10.1 快速开始

#### 步骤1：获取 API Key

访问 Key 生成页面：

```
https://sproxy.company.com/keygen/
```

1. 输入用户名和密码登录
2. 系统自动生成一个 API Key
3. 点击"复制"保存 Key
4. 如需更换，点击"重新生成"

#### 步骤2：配置客户端

**认证头格式说明**：

| 客户端 | 环境变量 | 发送的认证头 |
|--------|---------|-------------|
| Claude Code | `ANTHROPIC_API_KEY` | `x-api-key: <key>` |
| OpenCode | `OPENAI_API_KEY` | `Authorization: Bearer <key>` |

sproxy 的认证中间件会自动识别两种格式，用户无需关心差异。

#### Claude Code 配置

```bash
# 1. 从 WebUI 获取 API Key (格式: sk-pp-...)

# 2. 设置环境变量
export ANTHROPIC_BASE_URL=https://sproxy.company.com/anthropic
export ANTHROPIC_API_KEY=sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6

# 3. 直接使用 Claude Code
# Claude Code 会自动发送: x-api-key: sk-pp-...
claude code
```

#### OpenCode 配置

```bash
# 1. 设置环境变量
export OPENAI_BASE_URL=https://sproxy.company.com/v1
export OPENAI_API_KEY=sk-pp-xK8aLm2cNp9qRsTtUvWxYz1A3B5C7D9E0F2H4J6

# 2. 使用 OpenCode
# OpenCode 会自动发送: Authorization: Bearer sk-pp-...
opencode
```

### 10.2 CI/CD 使用示例

```yaml
# .github/workflows/ai-review.yml
name: AI Code Review

jobs:
  review:
    runs-on: ubuntu-latest
    env:
      ANTHROPIC_BASE_URL: https://sproxy.company.com/anthropic
      ANTHROPIC_API_KEY: ${{ secrets.PAIRPROXY_API_KEY }}
    steps:
      - uses: actions/checkout@v4
      - name: Run AI Review
        run: |
          claude code --prompt "Review this PR for potential issues"
```

---

## 11. 测试计划

### 11.1 单元测试

| 测试文件 | 测试内容 |
|---------|---------|
| `keygen/generator_test.go` | Key 生成算法、长度、字符集 |
| `keygen/validator_test.go` | 格式验证、重复字符处理、碰撞检测 |
| `keygen/cache_test.go` | 缓存读写、TTL 过期、用户失效 |
| `keygen/username_test.go` | 用户名约束检查 |
| `keyauth_middleware_test.go` | 两种认证头格式、无效 Key、缓存命中/未命中 |
| `direct_handler_test.go` | 路径重写、协议转换 |
| `hybrid_routes_test.go` | 路由冲突解决、模式切换 |

### 11.2 集成测试

```bash
# 测试 Key 生成 WebUI
curl -X POST http://localhost:9000/keygen/api/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"password"}'

# 测试 Anthropic 直连（使用返回的 Key）
curl -X POST http://localhost:9000/anthropic/v1/messages \
  -H "x-api-key: sk-pp-..." \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}'

# 测试 OpenAI 直连
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer sk-pp-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}'

# 测试 cproxy 模式（确保不冲突）
curl -X POST http://localhost:9000/v1/messages \
  -H "X-PairProxy-Auth: <JWT>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}'

# 测试路由冲突解决
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "X-PairProxy-Auth: <JWT>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}'
# 预期：返回 401，因为 cproxy 模式不支持 OpenAI 协议
```

### 11.3 E2E 测试

| 测试场景 | 验证点 |
|---------|--------|
| WebUI 登录 → 生成 Key → 使用 Key 请求 | 完整链路、身份正确 |
| 用户名碰撞 | `alice` vs `alice2`，选择最长匹配 |
| 重复字符用户名 | `aaab` vs `ab`，正确区分 |
| 用户重新生成 Key | 旧 Key 失效、缓存清除 |
| 用户被禁用 | 请求被拒绝、缓存失效 |
| 流式响应 | Token 统计准确 |
| 协议转换 | OpenAI→Anthropic、Anthropic→OpenAI |
| 路由冲突 | 同一路径不同认证头正确路由 |
- 错误处理测试（无效 Key、超额、上游故障）

---

## 12. 迁移策略

### 阶段 1: 并行运行 (v2.9.0)
- 新功能默认关闭，通过配置开启
- 保持 cproxy 模式完全不变
- 内部测试直连模式

### 阶段 2: 公开 Beta (v2.10.0)
- 默认开启直连模式
- 文档更新
- 收集用户反馈

### 阶段 3: 全面推广 (v3.0.0)
- 推荐新用户使用直连模式
- cproxy 进入维护模式
- 长期仍保持兼容

---

## 13. 待办事项

### 13.1 高优先级（核心功能）

- [ ] 实现 `keygen.Generator` Key 生成算法
- [ ] 实现 `keygen.Validator` Key 验证算法（含重复字符处理）
- [ ] 实现 `keygen.KeyCache` 缓存机制
- [ ] 实现 `KeyAuthMiddleware` 认证中间件（支持两种认证头格式）
- [ ] 实现 `DirectProxyHandler` 直连处理器
- [ ] 实现 `handleV1Routes` 混合路由处理器（解决路由冲突）
- [ ] 实现 `SProxy.ServeDirect` 代理转发逻辑
- [ ] 实现 Key 生成 WebUI (`/keygen/`)
  - [ ] 登录 API（返回 key + token）
  - [ ] 重新生成 Key API
  - [ ] 前端页面

### 13.2 中优先级（完善功能）

- [ ] 实现用户名约束检查（最小长度、唯一字符数）
- [ ] 实现缓存失效机制（用户重新生成 Key、用户禁用）
- [ ] Dashboard 集成 Key 用量展示
- [ ] 编写用户文档
- [ ] 添加错误提示（碰撞时告知用户）

### 13.3 低优先级（增强功能）

- [ ] Key 使用统计
- [ ] 用量预测和告警
- [ ] 多因素认证
- [ ] IP 白名单限制

---

## 14. 附录

### 14.1 相关文档

- [API.md](./API.md) - REST API 参考
- [manual.md](./manual.md) - 用户手册
- [openai_protocol.md](./openai_protocol.md) - OpenAI 协议参考
- [anthropic_protocal.md](./anthropic_protocal.md) - Anthropic 协议参考

### 14.2 Key 格式对比

| Provider | Key 格式 | 长度 |
|----------|---------|------|
| OpenAI | `sk-<48字符>` | 51 |
| Anthropic | `sk-ant-<80字符>` | 87 |
| **PairProxy** | `sk-pp-<48字符>` | 54 |

---

**文档结束**
