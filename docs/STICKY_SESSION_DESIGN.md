# PairProxy Sticky Session 设计与演进规划

**版本**: v1.0
**创建日期**: 2026-03-31
**状态**: 规划中
**参考项目**: Sub2API (https://github.com/Wei-Shaw/sub2api)

---

## 背景

PairProxy 当前采用 **自动重试 + 负载均衡** 的架构，当请求失败时自动切换到下一个健康的 target，直到找到可用账号或全部遍历完毕。这种设计在企业内部场景表现良好，但随着用户规模增长和账号压力增大，存在优化空间。

本文档分析 Sub2API 的 **Sticky Session（粘性会话）** 机制，探讨其在 PairProxy 中的应用价值，并制定演进规划。

---

## 一、Sub2API vs PairProxy 对比分析

### 1.1 架构对比

```
┌─────────────────────────────────────────────────────────────────┐
│                      Sub2API 架构                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  用户请求 → 解析请求 → 生成 Session Hash                         │
│                    ↓                                            │
│              查询 Redis 绑定                                     │
│              ┌─────┴─────┐                                      │
│              ↓           ↓                                      │
│          [命中绑定]   [未命中]                                   │
│              ↓           ↓                                      │
│         使用绑定账号   正常调度                                   │
│              ↓           ↓                                      │
│              └─────┬─────┘                                      │
│                    ↓                                            │
│              转发请求 → 记录绑定 (TTL 1小时)                      │
│                                                                 │
│  存储: Redis (sticky_session:{groupID}:{hash} → accountID)      │
│  TTL: 默认 1 小时                                                │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│                      PairProxy 架构                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  用户请求 → 加权随机选择 Target                                  │
│                    ↓                                            │
│              发送请求                                            │
│              ┌─────┴─────┐                                      │
│              ↓           ↓                                      │
│          [成功]       [失败 5xx/429]                            │
│              ↓           ↓                                      │
│           返回响应    PickNext(tried)                           │
│                          ↓                                      │
│                    选择下一个 Target                             │
│                          ↓                                      │
│                    重试请求 (最多 max_retries 次)                │
│                                                                 │
│  存储: 无状态，内存 tried 列表                                   │
│  重试: 默认 2 次，可配置 retry_on_status: [429]                  │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 核心差异

| 维度 | Sub2API (Sticky Session) | PairProxy (Auto Retry) |
|------|--------------------------|------------------------|
| **核心理念** | 会话绑定，避免切换 | 失败重试，容错优先 |
| **状态存储** | Redis 持久化 | 无状态，请求级 |
| **切换时机** | 仅账号不可用时 | 每次请求失败时 |
| **外部依赖** | 必须 Redis | 可选 Redis |
| **复杂度** | 高（需维护绑定状态） | 低（无状态设计） |

### 1.3 账号管理对比

| 功能 | Sub2API | PairProxy |
|------|---------|-----------|
| **多平台支持** | Claude, Gemini, OpenAI, Antigravity, Bedrock | Anthropic, OpenAI, Ollama |
| **认证类型** | API Key, OAuth, Cookie | API Key, OAuth |
| **账号分组** | ✅ 账号分组 + 用户分组绑定 | ✅ Group-Target Set |
| **Sticky Session** | ✅ 核心功能 | ❌ 不支持 |
| **协议转换** | ❌ | ✅ OpenAI ↔ Anthropic |

### 1.4 调度机制对比

| 功能 | Sub2API | PairProxy |
|------|---------|-----------|
| **调度策略** | 优先级 + 负载感知 + Sticky Session | 加权随机 + 语义路由 |
| **Sticky Session** | ✅ 基于 Session Hash 绑定账号 | ❌ 不支持 |
| **健康检查** | ✅ 主动 + 被动 | ✅ 主动 GET + 被动熔断 |
| **故障转移** | ✅ 账号不可用时清除绑定 | ✅ RetryTransport 自动重试 |
| **语义路由** | ❌ | ✅ v2.18.0+ |

### 1.5 配额与计费对比

| 功能 | Sub2API | PairProxy |
|------|---------|-----------|
| **配额维度** | 用户余额 + API Key 配额 + 订阅套餐 | 分组日/月限额 + RPM |
| **计费模式** | ✅ Token 级精确计费，支持倍率 | 仅费用估算，无实际扣费 |
| **订阅系统** | ✅ 完整订阅管理 | ❌ 无 |
| **充值/支付** | ✅ 支持对接支付系统 | ❌ 无 |

### 1.6 技术栈对比

| 维度 | Sub2API | PairProxy |
|------|---------|-----------|
| **后端框架** | Gin + Ent ORM | net/http + GORM |
| **数据库** | PostgreSQL (必需) | SQLite (默认) / PostgreSQL |
| **缓存** | Redis (必需) | 无外部缓存 |
| **前端** | Vue 3 + Vite (需构建) | Go 模板 + Tailwind (内嵌) |
| **部署复杂度** | 高 (PG + Redis) | 低 (单二进制) |

---

## 二、为什么 PairProxy 当前不需要 Sticky Session

### 2.1 使用场景差异

| Sub2API | PairProxy |
|---------|-----------|
| SaaS 商业运营，账号成本高 | 企业内部，账号通常充足 |
| 每个账号服务多个外部用户 | 用户数量有限 |
| 需要精细化管理账号使用 | 简单可用即可 |

### 2.2 账号压力差异

```
Sub2API 典型场景：
├── 10 个 Claude Pro 账号
├── 1000 个用户共享
├── 每个账号被 ~100 人同时使用
└── 窗口限制压力大 → 需要 Sticky Session

PairProxy 典型场景：
├── 3-5 个 Claude Pro 账号
├── 20 个内部开发者
├── 每个账号被 ~4-5 人使用
└── 窗口限制压力小 → 自动重试足够
```

### 2.3 现有重试机制足够

PairProxy 的 `RetryTransport` 已经提供了良好的容错能力：

```go
// internal/lb/retry_transport.go
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    tried := []string{}
    
    for attempt := 0; ; attempt++ {
        resp, err := t.Inner.RoundTrip(req)
        
        // 成功 → 返回
        if err == nil && resp.StatusCode < 500 && !t.isRetriableStatus(resp.StatusCode) {
            return resp, nil
        }
        
        // 记录失败 target
        tried = append(tried, currentURL)
        
        // 达到最大重试次数
        if attempt >= t.MaxRetries {
            return nil, fmt.Errorf("all targets exhausted")
        }
        
        // 选下一个 target
        next, _ := t.PickNext(req.URL.Path, tried)
        req = cloneRequest(next)
    }
}
```

配置支持 429 状态码触发重试：
```yaml
llm:
  retry_on_status: [429]  # 配额耗尽时自动切换
```

---

## 三、Sticky Session 核心原理（来自 Sub2API）

### 3.1 什么是 Sticky Session？

**核心作用**：确保同一对话的连续请求被路由到**同一个上游账号**。

```
用户 A 的对话：
  第1轮请求 → 账号 X
  第2轮请求 → 账号 X  ← Sticky Session 保证
  第3轮请求 → 账号 X  ← 即使其他账号空闲也不切换
```

### 3.2 为什么需要 Sticky Session？

Claude/Gemini 等 LLM 的 Pro/Plus 订阅账号有**使用限制**，这些限制是**绑定到具体账号**的：

```
Claude Pro 账号限制：
├── 5小时窗口内：最多 ~45 条消息
├── 计数器绑定到账号，不是用户
└── 每个账号独立计数
```

**没有 Sticky Session 的问题**：

```
用户发送 10 轮对话，没有 Sticky Session：

  轮 1-3: 账号 A（正常）
  轮 4-6: 账号 B（切换了！）
  轮 7-10: 账号 C（又切换！）

问题：
1. 账号 B/C 可能已经有累积使用量，容易触发限制
2. 用户可能收到 429 错误
3. 对话上下文缓存失效（Prompt Caching 不命中）
```

### 3.3 Session Hash 生成逻辑

Sub2API 的 Session Hash 生成优先级：

```go
func GenerateSessionHash(parsed *ParsedRequest) string {
    // 优先级 1: 从 metadata.user_id 提取 session_xxx
    if parsed.MetadataUserID != "" {
        if uid := ParseMetadataUserID(parsed.MetadataUserID); uid.SessionID != "" {
            return uid.SessionID
        }
    }
    
    // 优先级 2: 带 cache_control: {type: "ephemeral"} 的内容
    if cacheableContent := extractCacheableContent(parsed); cacheableContent != "" {
        return hashContent(cacheableContent)
    }
    
    // 优先级 3: SessionContext + System + Messages 完整摘要
    var combined strings.Builder
    if parsed.SessionContext != nil {
        combined.WriteString(parsed.SessionContext.ClientIP)
        combined.WriteString(parsed.SessionContext.UserAgent)
        combined.WriteString(parsed.SessionContext.APIKeyID)
    }
    combined.WriteString(parsed.System)
    for _, msg := range parsed.Messages {
        combined.WriteString(msg.Content)
    }
    return hashContent(combined.String())
}
```

**关键设计**：SessionContext 混入 `ClientIP` + `UserAgent` + `APIKeyID`，确保：
- 不同用户发送相同消息 → 不同 hash
- 同一用户的不同会话 → 不同 hash
- 同一会话的重试 → 相同 hash ✅

### 3.4 Redis 存储格式

```
Key 格式: sticky_session:{groupID}:{sessionHash}
Value: accountID
TTL: 默认 1 小时

示例:
sticky_session:1:a1b2c3d4e5f6... → 12345
```

---

## 四、PairProxy 演进规划

### 4.1 演进背景

随着企业规模扩大，可能出现以下情况：
1. 用户数量增长，账号压力增大
2. 多部门共享账号，需要更精细的会话管理
3. 需要保证对话上下文一致性（Prompt Caching 命中）

### 4.2 演进路线图

```
Phase 1: 评估与准备 (v2.19.0)
├── 分析现有账号使用模式
├── 评估是否需要 Sticky Session
└── 设计兼容现有架构的方案

Phase 2: 基础实现 (v3.0)
├── 增加 Sticky Session 存储层
├── 修改调度逻辑
└── 保持向后兼容

Phase 3: 增强 (v3.1+)
├── 结合语义路由
├── 配额感知调度
└── 智能绑定清理
```

### 4.3 Phase 1: 评估与准备

#### 4.3.1 触发条件评估

**何时需要引入 Sticky Session？**

| 指标 | 阈值 | 当前状态 | 建议 |
|------|------|----------|------|
| 用户/账号比 | > 10:1 | 评估中 | 超过阈值考虑引入 |
| 账号窗口限制触发率 | > 5% | 评估中 | 超过阈值考虑引入 |
| 429 错误率 | > 1% | 评估中 | 超过阈值考虑引入 |
| Prompt Caching 命中率 | < 50% | 评估中 | 需要提升时考虑引入 |

#### 4.3.2 存储选型

| 方案 | 优点 | 缺点 | 适用场景 |
|------|------|------|----------|
| **PostgreSQL 表** | 无新依赖，与现有架构一致 | 性能略低于 Redis | 中小规模，用户 < 100 |
| **Redis** | 高性能，TTL 原生支持 | 新增外部依赖 | 大规模，高并发 |
| **SQLite 本地缓存** | 无网络开销，最简单 | 跨节点不共享 | 单节点部署 |

**推荐方案**：
- SQLite 模式：使用 SQLite 本地表或内存缓存
- PostgreSQL 模式：使用 PG 表存储，共享于所有 Peer 节点

---

### 4.4 Phase 2: 基础实现

#### 4.4.1 数据库表设计（PostgreSQL 模式）

```sql
CREATE TABLE sticky_sessions (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL,                    -- 分组 ID
    session_hash VARCHAR(64) NOT NULL,           -- Session Hash
    target_url VARCHAR(512) NOT NULL,            -- 绑定的 Target URL
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NOT NULL,               -- 过期时间
    
    CONSTRAINT sticky_sessions_unique 
        UNIQUE (group_id, session_hash)
);

-- 索引
CREATE INDEX idx_sticky_sessions_expires 
    ON sticky_sessions(expires_at);
CREATE INDEX idx_sticky_sessions_group_hash 
    ON sticky_sessions(group_id, session_hash);
```

#### 4.4.2 内存缓存设计（SQLite 模式）

```go
// internal/lb/sticky_session.go
package lb

import (
    "sync"
    "time"
)

type StickySession struct {
    TargetURL string
    ExpiresAt time.Time
}

type StickySessionCache struct {
    mu    sync.RWMutex
    store map[string]*StickySession  // key: "{groupID}:{sessionHash}"
    ttl   time.Duration
}

func NewStickySessionCache(ttl time.Duration) *StickySessionCache {
    c := &StickySessionCache{
        store: make(map[string]*StickySession),
        ttl:   ttl,
    }
    go c.cleanup()
    return c
}

func (c *StickySessionCache) Get(groupID int64, sessionHash string) (string, bool) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    key := fmt.Sprintf("%d:%s", groupID, sessionHash)
    session, ok := c.store[key]
    if !ok || time.Now().After(session.ExpiresAt) {
        return "", false
    }
    return session.TargetURL, true
}

func (c *StickySessionCache) Set(groupID int64, sessionHash, targetURL string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    key := fmt.Sprintf("%d:%s", groupID, sessionHash)
    c.store[key] = &StickySession{
        TargetURL: targetURL,
        ExpiresAt: time.Now().Add(c.ttl),
    }
}

func (c *StickySessionCache) Delete(groupID int64, sessionHash string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    
    key := fmt.Sprintf("%d:%s", groupID, sessionHash)
    delete(c.store, key)
}

func (c *StickySessionCache) cleanup() {
    ticker := time.NewTicker(5 * time.Minute)
    for range ticker.C {
        c.mu.Lock()
        now := time.Now()
        for k, v := range c.store {
            if now.After(v.ExpiresAt) {
                delete(c.store, k)
            }
        }
        c.mu.Unlock()
    }
}
```

#### 4.4.3 Session Hash 生成

```go
// internal/lb/session_hash.go
package lb

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "strings"
    
    "github.com/cespare/xxhash/v2"
)

// SessionHashInput 用于生成 Session Hash 的输入
type SessionHashInput struct {
    // 最高优先级：客户端提供的 session_id
    SessionID string
    
    // 次优先级：请求内容摘要
    SystemPrompt string
    Messages     []string
    
    // 上下文因子（避免碰撞）
    ClientIP  string
    UserAgent string
    UserID    string
    GroupID   int64
}

// GenerateSessionHash 生成 Session Hash
func GenerateSessionHash(input *SessionHashInput) string {
    if input == nil {
        return ""
    }
    
    // 优先级 1: 客户端提供的 session_id
    if input.SessionID != "" {
        return normalizeSessionID(input.SessionID)
    }
    
    // 优先级 2: 上下文 + 内容摘要
    var combined strings.Builder
    
    // 混入上下文因子
    combined.WriteString(input.ClientIP)
    combined.WriteString(":")
    combined.WriteString(normalizeUserAgent(input.UserAgent))
    combined.WriteString(":")
    combined.WriteString(input.UserID)
    combined.WriteString("|")
    
    // 混入 System Prompt
    combined.WriteString(input.SystemPrompt)
    
    // 混入 Messages
    for _, msg := range input.Messages {
        combined.WriteString(msg)
    }
    
    // 使用 xxhash (更快) 或 sha256
    if combined.Len() == 0 {
        return ""
    }
    
    return fmt.Sprintf("%016x", xxhash.Sum64String(combined.String()))
}

// normalizeSessionID 标准化 session ID
func normalizeSessionID(id string) string {
    id = strings.TrimSpace(id)
    // 提取 UUID 格式
    if strings.Contains(id, "session_") {
        parts := strings.Split(id, "session_")
        if len(parts) > 1 {
            return strings.TrimSpace(parts[len(parts)-1])
        }
    }
    return id
}

// normalizeUserAgent 标准化 User-Agent（忽略版本号变化）
func normalizeUserAgent(ua string) string {
    // 移除版本号，避免版本升级导致 hash 变化
    re := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)
    return re.ReplaceAllString(ua, "x.x.x")
}
```

#### 4.4.4 调度逻辑修改

```go
// internal/proxy/sproxy.go 修改

// PickTargetWithSticky 结合 Sticky Session 的 target 选择
func (sp *SProxy) PickTargetWithSticky(
    ctx context.Context,
    groupID int64,
    sessionHash string,
    tried []string,
) (*LLMTarget, error) {
    
    // 1. 如果有 Sticky Session，优先使用绑定的 target
    if sessionHash != "" && sp.stickyCache != nil {
        targetURL, ok := sp.stickyCache.Get(groupID, sessionHash)
        if ok {
            // 检查绑定的 target 是否可用
            target := sp.findTargetByURL(targetURL)
            if target != nil && sp.isTargetHealthy(target.URL) {
                // 检查是否已在 tried 列表中
                if !contains(tried, target.URL) {
                    return target, nil
                }
            }
            // 绑定的 target 不可用，清除绑定
            sp.stickyCache.Delete(groupID, sessionHash)
        }
    }
    
    // 2. 正常负载均衡选择
    target, err := sp.llmBalancer.Pick()
    if err != nil {
        return nil, err
    }
    
    // 3. 建立 Sticky Session 绑定
    if sessionHash != "" && sp.stickyCache != nil {
        sp.stickyCache.Set(groupID, sessionHash, target.URL)
    }
    
    return target, nil
}

// shouldClearStickySession 判断是否需要清除 Sticky Session
func (sp *SProxy) shouldClearStickySession(targetURL string, statusCode int) bool {
    // 429: 账号配额耗尽
    if statusCode == 429 {
        return true
    }
    
    // 5xx: 服务端错误
    if statusCode >= 500 {
        return true
    }
    
    // 连接错误
    // ...
    
    return false
}
```

#### 4.4.5 配置设计

```yaml
# sproxy.yaml 新增配置

sticky_session:
  enabled: true                    # 是否启用 Sticky Session
  ttl: 1h                          # 绑定 TTL
  storage: "auto"                  # auto | memory | postgres | redis
  clear_on_status: [429, 500, 502, 503]  # 触发清除绑定的状态码
```

---

### 4.5 Phase 3: 增强

#### 4.5.1 结合语义路由

```go
// 语义路由 + Sticky Session 结合
func (sp *SProxy) PickTargetWithSemantic(
    ctx context.Context,
    groupID int64,
    sessionHash string,
    messages []Message,
    tried []string,
) (*LLMTarget, error) {
    
    // 1. 检查 Sticky Session
    if sessionHash != "" {
        if targetURL, ok := sp.stickyCache.Get(groupID, sessionHash); ok {
            if target := sp.findTargetByURL(targetURL); target != nil && sp.isHealthy(target) {
                return target, nil
            }
            sp.stickyCache.Delete(groupID, sessionHash)
        }
    }
    
    // 2. 语义路由缩窄候选池
    candidatePool := sp.allTargets
    if sp.semanticRouter != nil {
        if route := sp.semanticRouter.Classify(messages); route != nil {
            candidatePool = sp.filterTargetsByRoute(candidatePool, route)
        }
    }
    
    // 3. 从候选池中选择
    target := sp.llmBalancer.PickFromPool(candidatePool, tried)
    
    // 4. 建立绑定
    if sessionHash != "" && target != nil {
        sp.stickyCache.Set(groupID, sessionHash, target.URL)
    }
    
    return target, nil
}
```

#### 4.5.2 配额感知调度

```go
// 结合用户配额状态决定是否启用 Sticky Session
func (sp *SProxy) shouldUseStickySession(userID, groupID string) bool {
    if sp.stickyCache == nil {
        return false
    }
    
    // 查询用户配额状态
    quota, err := sp.quotaChecker.GetQuotaStatus(userID, groupID)
    if err != nil {
        return false  // 查询失败，降级为不使用
    }
    
    // 配额紧张时，不启用 Sticky Session，允许切换到其他账号
    if quota.RemainingPercent < 20 {
        return false
    }
    
    return true
}
```

#### 4.5.3 智能绑定清理

```go
// 后台任务：清理过期的绑定或无效账号的绑定
func (sp *SProxy) cleanupStickySessions() {
    ticker := time.NewTicker(5 * time.Minute)
    for range ticker.C {
        // 1. 清理过期绑定（由 TTL 自动处理）
        
        // 2. 清理不健康账号的绑定
        targets := sp.llmBalancer.Targets()
        for _, t := range targets {
            if !t.Healthy {
                sp.stickyCache.DeleteByTargetURL(t.ID)
            }
        }
    }
}
```

---

## 五、实现优先级

```
Phase 1: 评估与准备
├── 账号使用模式分析                    ★★★☆☆
├── 429 错误率监控                      ★★★★☆
└── Prompt Caching 命中率分析           ★★★☆☆

Phase 2: 基础实现 (v3.0)
├── StickySessionCache 内存实现        ★★★★★  核心功能
├── Session Hash 生成逻辑               ★★★★★  核心功能
├── 调度逻辑修改                         ★★★★★  核心功能
├── 配置支持                             ★★★★☆
└── PostgreSQL 表存储 (可选)            ★★★☆☆  分布式场景

Phase 3: 增强 (v3.1+)
├── 结合语义路由                         ★★★★☆  已有语义路由基础
├── 配额感知调度                         ★★★☆☆
└── 智能绑定清理                         ★★★☆☆
```

---

## 六、向后兼容策略

所有新增功能均保持**向后兼容**：

1. **默认关闭**：`sticky_session.enabled: false`，现有行为不变
2. **渐进启用**：可按分组配置是否启用
3. **降级机制**：Sticky Session 失败时自动回退到现有重试逻辑
4. **无强制依赖**：不强制要求 Redis，SQLite 模式可用内存缓存

---

## 七、相关文档

- 现有架构：`docs/ARCHITECTURE.md`
- 路由设计：`docs/ROADMAP.md`
- 故障容错：`docs/FAULT_TOLERANCE_ANALYSIS.md`
- 语义路由：`docs/superpowers/specs/2026-03-21-semantic-router-design.md`

---

## 附录：Sub2API 参考代码

### A.1 Session Hash 生成 (Sub2API)

```go
// Sub2API: backend/internal/service/gateway_service.go
func (s *GatewayService) GenerateSessionHash(parsed *ParsedRequest) string {
    // 1. 最高优先级：从 metadata.user_id 提取 session_xxx
    if parsed.MetadataUserID != "" {
        if uid := ParseMetadataUserID(parsed.MetadataUserID); uid != nil && uid.SessionID != "" {
            return uid.SessionID
        }
    }
    
    // 2. 提取带 cache_control: {type: "ephemeral"} 的内容
    cacheableContent := s.extractCacheableContent(parsed)
    if cacheableContent != "" {
        return s.hashContent(cacheableContent)
    }
    
    // 3. 最后 fallback: 使用 session上下文 + system + 所有消息的完整摘要串
    var combined strings.Builder
    if parsed.SessionContext != nil {
        combined.WriteString(parsed.SessionContext.ClientIP)
        combined.WriteString(":")
        combined.WriteString(NormalizeSessionUserAgent(parsed.SessionContext.UserAgent))
        combined.WriteString(":")
        combined.WriteString(strconv.FormatInt(parsed.SessionContext.APIKeyID, 10))
        combined.WriteString("|")
    }
    // ... system and messages
    return s.hashContent(combined.String())
}
```

### A.2 Redis 存储 (Sub2API)

```go
// Sub2API: backend/internal/repository/gateway_cache.go
const stickySessionPrefix = "sticky_session:"

func buildSessionKey(groupID int64, sessionHash string) string {
    return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
    key := buildSessionKey(groupID, sessionHash)
    return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
    key := buildSessionKey(groupID, sessionHash)
    return c.rdb.Set(ctx, key, accountID, ttl).Err()
}
```

### A.3 清理判断 (Sub2API)

```go
// Sub2API: backend/internal/service/sticky_session_test.go
func shouldClearStickySession(account *Account, requestedModel string) bool {
    if account == nil {
        return false
    }
    // 账号状态错误或禁用
    if account.Status == StatusError || account.Status == StatusDisabled {
        return true
    }
    // 账号不可调度
    if !account.Schedulable {
        return true
    }
    // 临时不可调度且未过期
    if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(time.Now()) {
        return true
    }
    // 模型限流
    if account.IsModelRateLimited(requestedModel) {
        return true
    }
    return false
}
```