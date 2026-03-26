# Group-Target Set Binding with Pooling and Alerting Design

**日期**: 2026-03-25
**项目**: PairProxy Gateway
**状态**: 待实现
**作者**: Claude Sonnet 4.6

---

## 1. 背景与目标

### 1.1 当前问题

现有 PairProxy 架构存在以下限制：

1. **Group 只能绑定单个 Target**: `LLMBinding` 模型只能指定一个 `TargetURL`，无法支持一组 targets 的池化和负载均衡
2. **绑定 Target 故障时无自动路由**: 当绑定的 target 不可用时，请求直接返回 403 错误，不会自动路由到组内其他可用 target
3. **缺少 Target 级别的告警**: 当某个 LLM 端点返回错误（如 429、503、连接超时）时，没有机制在 WebUI 上实时展示告警信息
4. **未分组用户处理不明确**: 当前未分组用户没有默认的 target 集合，导致配置复杂

### 1.2 目标

1. **Group-Target Set 绑定**: 支持为每个 Group 配置一组 LLM targets，组内自动负载均衡和故障转移
2. **组内池化与故障转移**: 当组内某个 target 不可用时（配额耗尽、网络故障、5xx 错误），自动路由到组内其他可用 target
3. **智能路由模式**: 基于 SemanticRouter 实现二分类路由（小模型 vs 大模型）
   - 小模型：用于代码理解等简单任务
   - 大模型：用于复杂任务
   - 小模型失败可 fallback 到大模型池
   - 大模型失败只在同类内重试
4. **实时告警**: 当 target 返回错误时，在 WebUI Dashboard 上实时显示告警信息，包括错误类型、时间、影响的用户组
5. **两类群组支持**:
   - 第一类：正常创建的群组（如 engineering、research），可配置专属的 target set
   - 第二类：默认组：未分组用户自动归属的组，使用全局 target pool
6. **用户单一归属**: 用户同一时间只能属于一个组（符合现有设计）

### 1.3 两类群组说明

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Group 分类体系                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ 第一类：普通组 (Normal Groups)                                        │ │
│  │ ───────────────────────────────────────────────────────────────────── │ │
│  │                                                                       │ │
│  │  由管理员显式创建，用户通过 --group 参数指定加入                        │ │
│  │                                                                       │ │
│  │  示例:                                                                │ │
│  │    ./sproxy admin group add engineering --daily-limit 1000000         │ │
│  │    ./sproxy admin user add alice --group engineering                  │ │
│  │                                                                       │ │
│  │  特点:                                                                │ │
│  │    • 可配置专属的 target set（一组 LLM 端点）                         │ │
│  │    • 有自己独立的配额限制                                             │ │
│  │    • 请求仅在组内的 target set 中路由                                 │ │
│  │                                                                       │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ 第二类：默认组 (Default Group)                                        │ │
│  │ ───────────────────────────────────────────────────────────────────── │ │
│  │                                                                       │ │
│  │  系统隐式存在，不指定 --group 的用户自动归属                            │ │
│  │                                                                       │ │
│  │  示例:                                                                │ │
│  │    ./sproxy admin user add bob   # 不指定 --group                      │ │
│  │    → bob 自动归属到默认组 (group_id = "default" 或 NULL)               │ │
│  │                                                                       │ │
│  │  特点:                                                                │ │
│  │    • 使用全局 target pool（所有未分配给特定组的 targets）               │ │
│  │    • 可配置默认配额（可选）                                           │ │
│  │    • 向后兼容：未分组用户可继续使用所有 targets                        │ │
│  │                                                                       │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  约束：用户同一时间只能属于一个组                                           │
│  ──────────────────────────────────────────────────────────────────────── │
│  • 用户加入新组时自动从旧组移除                                             │
│  • 不能同时属于普通组和默认组（互斥）                                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. 核心设计决策

| 决策项 | 选择 | 理由 |
|--------|------|------|
| Group-Target 关系 | 多对多（Group ↔ Target Set） | 灵活配置，支持不同组使用不同 target 池 |
| 组内选择策略 | 加权随机 + 健康检查 | 与现有 LB 策略一致，无需额外学习成本 |
| 故障转移范围 | 限制在 Group 内 | 隔离性，避免跨组资源抢占 |
| 默认组行为 | 使用全局所有 targets | 向后兼容，简化未分组用户配置 |
| 告警触发 | Target 返回错误 + 健康检查失败 | 覆盖主要故障场景 |
| 告警展示 | Dashboard SSE 实时推送 + 历史告警页面 | 即时感知 + 事后追溯 |
| 数据存储 | 新增 `group_target_sets` 表 | 与现有 LLMBinding 并存，逐步迁移 |
| 配置方式 | Admin CLI + Dashboard API | 与现有管理体系一致 |

---

## 3. 架构位置

```
请求进入
  ↓
AuthMiddleware (获取 userID, groupID)
  ↓
QuotaMiddleware (检查配额)
  ↓
[修改] pickLLMTarget 逻辑
  ├─ 如果 user 有绑定 (legacy LLMBinding) → 使用绑定的单个 target
  ├─ 如果 group 有 TargetSet 绑定 → 在组内 pool 中选择
  │   ├─ 获取 Group 的所有可用 targets
  │   ├─ 过滤不健康/已尝试的 targets
  │   ├─ 加权随机选择
  │   └─ 失败时在同一组内重试（try-next）
  └─ 如果无绑定且 Group 无 TargetSet → 使用全局 LB（默认组行为）
  ↓
RetryTransport (跨组级别的故障转移)
  ↓
目标 LLM

[新增] TargetHealthMonitor (后台 goroutine)
  ├─ 定期检查各 target 健康状态
  ├─ 记录 target 错误事件
  └─ 推送告警到 AlertHub

[新增] AlertHub (Dashboard SSE)
  ├─ 接收 target 错误/恢复事件
  ├─ 维护告警列表（内存 + 持久化）
  └─ SSE 推送实时更新到 WebUI
```

---

## 4. 数据模型设计

### 4.1 新增表: `group_target_sets`

```sql
-- Group 与 Target Set 的绑定关系
-- 支持两类群组：
--   1. 普通组: group_id 指向具体的 groups.id
--   2. 默认组: group_id = NULL 且 is_default = 1
CREATE TABLE group_target_sets (
    id              TEXT PRIMARY KEY,           -- UUID
    group_id        TEXT,                       -- 关联 groups.id（NULL=默认组）
    name            TEXT NOT NULL,              -- 显示名称（如 "production-pool"）
    strategy        TEXT NOT NULL DEFAULT 'weighted_random', -- 选择策略
    retry_policy    TEXT NOT NULL DEFAULT 'try_next',        -- 重试策略
    is_default      INTEGER NOT NULL DEFAULT 0, -- 是否默认组（1=默认组，0=普通组）
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);

-- 创建索引
CREATE INDEX idx_group_target_sets_group_id ON group_target_sets(group_id);
CREATE INDEX idx_group_target_sets_is_default ON group_target_sets(is_default);
```

### 4.2 新增表: `group_target_set_members`

```sql
-- Target Set 的成员（多对多关系）
CREATE TABLE group_target_set_members (
    id                  TEXT PRIMARY KEY,       -- UUID
    target_set_id       TEXT NOT NULL,          -- 关联 group_target_sets.id
    target_url          TEXT NOT NULL,          -- LLM target URL
    weight              INTEGER NOT NULL DEFAULT 1,  -- 权重
    priority            INTEGER NOT NULL DEFAULT 0,  -- 优先级（高优先先选）
    is_active           INTEGER NOT NULL DEFAULT 1,  -- 是否启用
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id) ON DELETE CASCADE,
    FOREIGN KEY (target_url) REFERENCES llm_targets(url) ON DELETE CASCADE,
    UNIQUE(target_set_id, target_url)  -- 同一 target_set 内 URL 唯一
);

-- 创建索引
CREATE INDEX idx_gt_members_target_set_id ON group_target_set_members(target_set_id);
CREATE INDEX idx_gt_members_target_url ON group_target_set_members(target_url);
```

### 4.3 新增表: `target_alerts`

```sql
-- Target 告警事件（持久化存储，用于历史查询）
CREATE TABLE target_alerts (
    id              TEXT PRIMARY KEY,           -- UUID
    target_url      TEXT NOT NULL,              -- 关联的 target
    alert_type      TEXT NOT NULL,              -- 告警类型: error, degraded, recovered
    severity        TEXT NOT NULL,              -- 严重级别: warning, error, critical
    status_code     INTEGER,                    -- HTTP 状态码（如果有）
    error_message   TEXT,                       -- 错误信息
    affected_groups TEXT,                       -- 受影响的分组 JSON 数组
    resolved_at     DATETIME,                   -- 恢复时间（NULL=未恢复）
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (target_url) REFERENCES llm_targets(url)
);

-- 创建索引
CREATE INDEX idx_target_alerts_target_url ON target_alerts(target_url);
CREATE INDEX idx_target_alerts_created_at ON target_alerts(created_at);
CREATE INDEX idx_target_alerts_unresolved ON target_alerts(resolved_at) WHERE resolved_at IS NULL;
```

### 4.4 扩展现有表: `groups`

```sql
-- 扩展现有表: `groups`
-- 支持两类群组：普通组(is_default=0) 和 默认组(is_default=1)

-- 扩展现有表: `groups`
-- 支持两类群组：普通组(is_default=0) 和 默认组(is_default=1)

-- 添加 target_set_id 外键（可选，用于快速查询）
ALTER TABLE groups ADD COLUMN target_set_id TEXT 
    REFERENCES group_target_sets(id) ON DELETE SET NULL;

-- 添加默认组标记（0=普通组，1=默认组）
ALTER TABLE groups ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;
```

### 4.5 模型关系图（两类群组）

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           两类群组模型关系                                  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │  第一类：普通组 (is_default=0)                                        │ │
│  │  ─────────────────────────────────────────────────────────────────    │ │
│  │                                                                       │ │
│  │   groups (id=eng) ──▶ group_target_sets (group_id=eng)                │ │
│  │      │                     │                                          │ │
│  │      │                     ▼                                          │ │
│  │   users (group_id=eng)   group_target_set_members                     │ │
│  │                              │                                        │ │
│  │                              ▼                                        │ │
│  │                          llm_targets (专属 pool)                      │ │
│  │                                                                       │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │  第二类：默认组 (is_default=1)                                        │ │
│  │  ─────────────────────────────────────────────────────────────────    │ │
│  │                                                                       │ │
│  │   groups (id=default, is_default=1)                                   │ │
│  │      │                                                                │ │
│  │      │         group_target_sets (group_id=NULL, is_default=1)        │ │
│  │      │                     │                                          │ │
│  │      │                     ▼                                          │ │
│  │   users (group_id=NULL   group_target_set_members                     │ │
│  │   or group_id=default)       │                                        │ │
│  │                              ▼                                        │ │
│  │                          llm_targets (全局 pool)                      │ │
│  │                                                                       │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

详细表结构:
┌─────────────────┐     ┌──────────────────────┐     ┌─────────────────────┐
│     groups      │────▶│  group_target_sets   │◄────┤ group_target_set_   │
├─────────────────┤     ├──────────────────────┤     │ _members            │
│ id (PK)         │     │ id (PK)              │     ├─────────────────────┤
│ name            │     │ group_id (FK, NULL)  │     │ id (PK)             │
│ daily_limit     │     │                      │     │ target_set_id (FK)  │
│ monthly_limit   │     │ is_default:          │     │ target_url (FK)     │
│ target_set_id   │────▶│   0=普通组           │     │ weight              │
│ is_default      │     │   1=默认组           │     │ priority            │
└─────────────────┘     └──────────────────────┘     └─────────────────────┘
                                                               │
                                                               ▼
                                                      ┌─────────────────────┐
                                                      │    llm_targets      │
                                                      ├─────────────────────┤
                                                      │ url (PK)            │
                                                      │ api_key_id          │
                                                      │ provider            │
                                                      │ health_check_path   │
                                                      └─────────────────────┘
                                                               │
                                                               ▼
                                                      ┌─────────────────────┐
                                                      │    target_alerts    │
                                                      ├─────────────────────┤
                                                      │ id (PK)             │
                                                      │ target_url (FK)     │
                                                      │ alert_type          │
                                                      │ severity            │
                                                      │ resolved_at         │
                                                      └─────────────────────┘
```

---

## 5. 配置格式

### 5.1 sproxy.yaml 新增配置段

```yaml
# -----------------------------------------------------------------------------
# group_target_sets — 分组 Target Set 配置
# 支持两类群组：
#   1. 普通组: 通过 group_name 关联到具体群组
#   2. 默认组: 通过 is_default: true 标记（无 group_name）
# -----------------------------------------------------------------------------
group_target_sets:
  # 第二类：默认组的 target set（未分组用户使用）
  - name: "default-global-pool"
    is_default: true              # 标记为默认组
    strategy: "weighted_random"
    retry_policy: "try_next"      # 组内故障转移策略
    targets:
      - url: "https://api.anthropic.com"
        weight: 2
        priority: 1
      - url: "https://api.openai.com"
        weight: 1
        priority: 2
      - url: "http://localhost:11434"  # Ollama fallback
        weight: 1
        priority: 3

  # 第一类：普通组 - Group A: 高优先级 Anthropic 池
  - name: "group-a-premium"
    group_name: "engineering"     # 关联到 groups.name
    strategy: "weighted_random"
    retry_policy: "try_next"
    targets:
      - url: "https://api.anthropic.com"
        weight: 1
      - url: "https://bedrock-proxy.company.com"
        weight: 1

  # 第一类：普通组 - Group B: OpenAI + Ollama 混合池
  - name: "group-b-standard"
    group_name: "standard-users"
    strategy: "round_robin"
    retry_policy: "try_next"
    targets:
      - url: "https://api.openai.com"
        weight: 2
      - url: "http://ollama.local:11434"
        weight: 1

# -----------------------------------------------------------------------------
# target_alerts — Target 告警配置
# -----------------------------------------------------------------------------
target_alerts:
  enabled: true
  
  # 告警触发条件
  triggers:
    # HTTP 5xx 错误
    - type: "http_error"
      status_codes: [500, 502, 503, 504]
      severity: "error"
      min_occurrences: 3           # 连续 3 次触发告警
      window: "5m"                 # 在 5 分钟窗口内
    
    # 配额耗尽 (429)
    - type: "rate_limited"
      status_codes: [429]
      severity: "warning"
      min_occurrences: 2
      window: "2m"
    
    # 连接错误
    - type: "connection_error"
      severity: "critical"
      min_occurrences: 1
      window: "1m"
    
    # 健康检查失败
    - type: "health_check_failed"
      severity: "error"
      min_occurrences: 2
      window: "3m"
  
  # 告警恢复条件
  recovery:
    consecutive_successes: 3       # 连续 3 次成功视为恢复
    window: "5m"
  
  # 告警展示配置
  dashboard:
    max_active_alerts: 50          # 最多显示多少条活跃告警
    retention: "7d"                # 历史告警保留时间
    auto_refresh: true             # 自动刷新
```

---

## 6. Admin CLI 命令

```bash
# =============================================================================
# Group Target Set 管理
# =============================================================================

# 列出所有 target sets
./sproxy admin targetset list

# 创建新的 target set
./sproxy admin targetset create <name> \
  --group <group_name> \
  --strategy weighted_random \
  --retry-policy try_next

# 删除 target set
./sproxy admin targetset delete <name>

# 将 target 添加到 set
./sproxy admin targetset add-target <set_name> \
  --url "https://api.anthropic.com" \
  --weight 2 \
  --priority 1

# 从 set 移除 target
./sproxy admin targetset remove-target <set_name> \
  --url "https://api.anthropic.com"

# 更新 target 权重
./sproxy admin targetset set-weight <set_name> \
  --url "https://api.anthropic.com" \
  --weight 3

# 查看 target set 详情
./sproxy admin targetset show <name>

# =============================================================================
# Target 告警管理
# =============================================================================

# 查看活跃告警
./sproxy admin alert list [--target <url>] [--severity <warning|error|critical>]

# 查看告警历史
./sproxy admin alert history [--days 7] [--target <url>]

# 手动解决告警（标记为已恢复）
./sproxy admin alert resolve <alert_id>

# 查看告警统计
./sproxy admin alert stats [--target <url>] [--days 30]
```

---

## 7. REST API 设计

### 7.1 Group Target Set API

```http
# 列出所有 target sets
GET /api/admin/group-target-sets
Response: {
  "sets": [
    {
      "id": "uuid",
      "name": "group-a-premium",
      "group_id": "uuid",
      "group_name": "engineering",
      "strategy": "weighted_random",
      "is_default": false,
      "targets": [
        {"url": "https://api.anthropic.com", "weight": 1, "priority": 1, "healthy": true}
      ],
      "created_at": "2026-03-25T10:00:00Z"
    }
  ]
}

# 创建 target set
POST /api/admin/group-target-sets
Body: {
  "name": "group-a-premium",
  "group_id": "uuid",
  "strategy": "weighted_random",
  "retry_policy": "try_next",
  "targets": [
    {"url": "https://api.anthropic.com", "weight": 1, "priority": 1}
  ]
}

# 更新 target set
PUT /api/admin/group-target-sets/{id}
Body: { /* 同上 */ }

# 删除 target set
DELETE /api/admin/group-target-sets/{id}

# 添加 target 到 set
POST /api/admin/group-target-sets/{id}/targets
Body: {
  "url": "https://api.anthropic.com",
  "weight": 1,
  "priority": 1
}

# 从 set 移除 target
DELETE /api/admin/group-target-sets/{id}/targets/{target_url}

# 更新 target 权重
PATCH /api/admin/group-target-sets/{id}/targets/{target_url}
Body: {
  "weight": 2,
  "priority": 1,
  "is_active": true
}
```

### 7.2 Target Alert API

```http
# 获取活跃告警列表
GET /api/admin/target-alerts?status=active&severity=error&target=<url>
Response: {
  "alerts": [
    {
      "id": "uuid",
      "target_url": "https://api.anthropic.com",
      "alert_type": "rate_limited",
      "severity": "warning",
      "status_code": 429,
      "error_message": "Rate limit exceeded",
      "affected_groups": ["engineering", "research"],
      "created_at": "2026-03-25T10:30:00Z",
      "duration_seconds": 300
    }
  ],
  "summary": {
    "total_active": 5,
    "by_severity": {"warning": 3, "error": 1, "critical": 1}
  }
}

# 获取告警历史
GET /api/admin/target-alerts/history?days=7&target=<url>&page=1&page_size=50
Response: {
  "alerts": [ /* ... */ ],
  "pagination": {"page": 1, "page_size": 50, "total": 123}
}

# 手动解决告警
POST /api/admin/target-alerts/{id}/resolve
Body: {"reason": "manual resolution by admin"}

# 获取告警统计
GET /api/admin/target-alerts/stats?days=30
Response: {
  "by_target": {
    "https://api.anthropic.com": {
      "total_alerts": 45,
      "error_rate": 0.02,
      "avg_duration_seconds": 120
    }
  },
  "by_group": {
    "engineering": {"affected_alerts": 30}
  }
}

# SSE 实时告警流
GET /api/admin/target-alerts/stream
Content-Type: text/event-stream

# 事件格式:
event: alert_created
data: {"id": "uuid", "target_url": "...", "severity": "error", ...}

event: alert_resolved
data: {"id": "uuid", "resolved_at": "..."}

event: target_health_changed
data: {"target_url": "...", "healthy": false, "reason": "..."}
```

---

## 8. 核心接口设计

### 8.1 GroupTargetSetRepo

```go
package db

// GroupTargetSet 代表一个 target 集合
type GroupTargetSet struct {
    ID           string
    GroupID      *string           // nil 表示默认组
    Name         string
    Strategy     string            // "weighted_random" | "round_robin"
    RetryPolicy  string            // "try_next" | "fail_fast"
    IsDefault    bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
    
    // 关联数据（非 DB 字段）
    Members      []GroupTargetSetMember
}

// GroupTargetSetMember target set 成员
type GroupTargetSetMember struct {
    ID           string
    TargetSetID  string
    TargetURL    string
    Weight       int
    Priority     int
    IsActive     bool
    CreatedAt    time.Time
    
    // 关联数据
    Target       *LLMTarget
}

// GroupTargetSetRepo 管理 Group-Target Set 关系
type GroupTargetSetRepo struct {
    db     *gorm.DB
    logger *zap.Logger
}

func NewGroupTargetSetRepo(db *gorm.DB, logger *zap.Logger) *GroupTargetSetRepo

// 基础 CRUD
func (r *GroupTargetSetRepo) Create(set *GroupTargetSet) error
func (r *GroupTargetSetRepo) GetByID(id string) (*GroupTargetSet, error)
func (r *GroupTargetSetRepo) GetByName(name string) (*GroupTargetSet, error)
func (r *GroupTargetSetRepo) GetByGroupID(groupID string) (*GroupTargetSet, error)
func (r *GroupTargetSetRepo) GetDefault() (*GroupTargetSet, error)
func (r *GroupTargetSetRepo) Update(set *GroupTargetSet) error
func (r *GroupTargetSetRepo) Delete(id string) error
func (r *GroupTargetSetRepo) ListAll() ([]GroupTargetSet, error)

// Member 管理
func (r *GroupTargetSetRepo) AddMember(setID string, member *GroupTargetSetMember) error
func (r *GroupTargetSetRepo) RemoveMember(setID string, targetURL string) error
func (r *GroupTargetSetRepo) UpdateMember(setID string, targetURL string, weight, priority int) error
func (r *GroupTargetSetRepo) ListMembers(setID string) ([]GroupTargetSetMember, error)

// 获取 Group 可用的 targets（用于路由选择）
func (r *GroupTargetSetRepo) GetAvailableTargetsForGroup(groupID string) ([]TargetWithWeight, error)
```

### 8.2 TargetAlertRepo

```go
package db

// TargetAlert Target 告警事件
type TargetAlert struct {
    ID             string
    TargetURL      string
    AlertType      string            // "error" | "degraded" | "recovered"
    Severity       string            // "warning" | "error" | "critical"
    StatusCode     *int
    ErrorMessage   string
    AffectedGroups string            // JSON 数组
    ResolvedAt     *time.Time
    CreatedAt      time.Time
}

// TargetAlertRepo 管理告警事件
type TargetAlertRepo struct {
    db     *gorm.DB
    logger *zap.Logger
}

func NewTargetAlertRepo(db *gorm.DB, logger *zap.Logger) *TargetAlertRepo

// 创建告警
func (r *TargetAlertRepo) Create(alert *TargetAlert) error

// 查询活跃告警
func (r *TargetAlertRepo) ListActive(filters AlertFilters) ([]TargetAlert, error)

// 查询历史告警
func (r *TargetAlertRepo) ListHistory(days int, page, pageSize int) ([]TargetAlert, error)

// 标记为已解决
func (r *TargetAlertRepo) Resolve(alertID string) error

// 获取统计信息
func (r *TargetAlertRepo) GetStats(days int) (*AlertStats, error)

// 清理旧数据
func (r *TargetAlertRepo) Cleanup(olderThan time.Duration) (int, error)
```

### 8.3 GroupTargetSelector（核心选择器）

```go
package proxy

// GroupTargetSelector 在 Group 内选择 target
type GroupTargetSelector struct {
    repo       *db.GroupTargetSetRepo
    balancer   lb.Balancer
    healthChecker *lb.HealthChecker
    logger     *zap.Logger
}

// NewGroupTargetSelector 创建选择器
func NewGroupTargetSelector(
    repo *db.GroupTargetSetRepo,
    healthChecker *lb.HealthChecker,
    logger *zap.Logger,
) *GroupTargetSelector

// SelectTarget 为指定 Group 选择 target
// 逻辑：
// 1. 查询 Group 的 target set
// 2. 获取所有 active members
// 3. 过滤不健康的 targets
// 4. 过滤已尝试的 targets（tried 参数）
// 5. 按 strategy 选择
// 6. 返回选中的 target 和是否还有可用备选
func (s *GroupTargetSelector) SelectTarget(
    ctx context.Context,
    groupID string,
    tried []string,
) (*SelectedTarget, bool, error)

type SelectedTarget struct {
    URL        string
    APIKey     string
    Provider   string
    Weight     int
    IsBound    bool  // 是否来自 group binding
}
```

### 8.4 TargetAlertManager（告警管理器）

```go
package alert

// TargetAlertManager 管理 target 告警
type TargetAlertManager struct {
    repo       *db.TargetAlertRepo
    config     TargetAlertConfig
    logger     *zap.Logger
    
    // 内存状态
    activeAlerts map[string]*ActiveAlert  // target_url -> alert
    mu           sync.RWMutex
    
    // 事件通道
    eventCh      chan AlertEvent
}

// NewTargetAlertManager 创建告警管理器
func NewTargetAlertManager(
    repo *db.TargetAlertRepo,
    config TargetAlertConfig,
    logger *zap.Logger,
) *TargetAlertManager

// Start 启动告警管理器（启动后台 goroutine）
func (m *TargetAlertManager) Start(ctx context.Context)

// Stop 停止告警管理器
func (m *TargetAlertManager) Stop()

// RecordError 记录 target 错误（由 RetryTransport 调用）
func (m *TargetAlertManager) RecordError(
    targetURL string,
    statusCode int,
    err error,
    affectedGroups []string,
)

// RecordHealthCheckFail 记录健康检查失败
func (m *TargetAlertManager) RecordHealthCheckFail(targetURL string, reason string)

// RecordSuccess 记录成功（用于恢复检测）
func (m *TargetAlertManager) RecordSuccess(targetURL string)

// GetActiveAlerts 获取当前活跃告警
func (m *TargetAlertManager) GetActiveAlerts() []ActiveAlert

// SubscribeEvents 订阅告警事件（Dashboard SSE 使用）
func (m *TargetAlertManager) SubscribeEvents() <-chan AlertEvent
```

---

## 9. 修改现有代码

### 9.1 pickLLMTarget 修改

```go
// internal/proxy/sproxy.go

// pickLLMTarget 选择下一个 LLM target，支持多种策略
func (sp *SProxy) pickLLMTarget(
    path, userID, groupID string,
    tried []string,
    candidateFilter []string,
) (*lb.LLMTargetInfo, error) {
    
    triedSet := make(map[string]bool, len(tried))
    for _, u := range tried {
        triedSet[u] = true
    }

    // 1. 优先级最高：用户级 LLMBinding（向后兼容）
    if sp.bindingResolver != nil {
        boundURL, found := sp.bindingResolver(userID, "")
        if found {
            // ... 原有逻辑
            return sp.llmTargetInfoForURL(boundURL), nil
        }
    }

    // 2. [新增] Group Target Set 选择
    if sp.groupTargetSelector != nil && groupID != "" {
        target, hasMore, err := sp.groupTargetSelector.SelectTarget(
            context.Background(),
            groupID,
            tried,
        )
        if err == nil && target != nil {
            sp.logger.Debug("selected target from group set",
                zap.String("group_id", groupID),
                zap.String("target_url", target.URL),
                zap.Bool("has_more", hasMore),
            )
            return &lb.LLMTargetInfo{
                URL:      target.URL,
                APIKey:   target.APIKey,
                Provider: target.Provider,
            }, nil
        }
        
        // Group set 选择失败但还有其他 target → 继续到全局选择
        if hasMore {
            sp.logger.Warn("group target set exhausted, falling back to global pool",
                zap.String("group_id", groupID),
                zap.Int("tried_count", len(tried)),
            )
        }
    }

    // 3. [新增] 默认组（未分组用户）使用全局 targets
    if (groupID == "" || sp.isDefaultGroup(groupID)) && sp.groupTargetSelector != nil {
        target, _, err := sp.groupTargetSelector.SelectTarget(
            context.Background(),
            "",  // 空 groupID 表示默认组
            tried,
        )
        if err == nil && target != nil {
            return &lb.LLMTargetInfo{
                URL:      target.URL,
                APIKey:   target.APIKey,
                Provider: target.Provider,
            }, nil
        }
    }

    // 4. 回退到原有全局负载均衡逻辑
    // ... 原有代码
}
```

### 9.2 RetryTransport 集成告警

```go
// internal/lb/retry_transport.go

func (rt *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    var lastErr error
    tried := make([]string, 0, rt.MaxRetries+1)
    
    for attempt := 0; attempt <= rt.MaxRetries; attempt++ {
        target, err := rt.PickNext(req.URL.Path, tried)
        if err != nil {
            return nil, err
        }
        
        tried = append(tried, target.URL)
        resp, err := rt.doAttempt(req, target)
        
        if err != nil {
            lastErr = err
            
            // [新增] 记录 target 错误到告警管理器
            if rt.alertManager != nil {
                rt.alertManager.RecordError(
                    target.URL,
                    0,  // 连接错误无状态码
                    err,
                    []string{},  // TODO: 获取 affected groups
                )
            }
            
            rt.OnFailure(target.URL)
            continue  // 重试
        }
        
        // [新增] 记录成功（用于恢复检测）
        if rt.alertManager != nil {
            rt.alertManager.RecordSuccess(target.URL)
        }
        
        // 检查是否需要重试
        if rt.shouldRetry(resp.StatusCode) {
            lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
            
            // [新增] 记录 HTTP 错误
            if rt.alertManager != nil {
                rt.alertManager.RecordError(
                    target.URL,
                    resp.StatusCode,
                    fmt.Errorf("HTTP error %d", resp.StatusCode),
                    []string{},
                )
            }
            
            rt.OnFailure(target.URL)
            continue
        }
        
        rt.OnSuccess(target.URL)
        return resp, nil
    }
    
    return nil, lastErr
}
```

---

## 10. WebUI Dashboard 设计

### 10.1 新增页面: Target 告警面板

```
/dashboard/alerts

┌─────────────────────────────────────────────────────────────────────────────┐
│  🔔 Target Alerts                                              [自动刷新 ▼] │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Active Alerts (5)                                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ 🔴 Critical  https://api.anthropic.com          5m ago  [Resolve]    │ │
│  │    Connection timeout - affecting: engineering, research             │ │
│  │                                                                       │ │
│  │ 🟡 Warning   https://api.openai.com             12m ago              │ │
│  │    Rate limited (429) - affecting: standard-users                    │ │
│  │                                                                       │ │
│  │ 🔴 Error     https://bedrock-proxy.com          1h ago  [Resolve]    │ │
│  │    HTTP 503 Service Unavailable - affecting: engineering             │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Target Health Summary                                                      │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Target                          Status    Last Check   Affected Groups│ │
│  │ https://api.anthropic.com       🔴 Down   30s ago      engineering    │ │
│  │ https://api.openai.com          🟡 Slow   1m ago       standard-users │ │
│  │ http://ollama.local:11434       🟢 Up     30s ago      -              │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  [View Alert History]  [Configure Alert Rules]                              │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 10.2 Dashboard 实时告警 SSE

```javascript
// /dashboard/static/js/alerts.js

// 订阅告警事件流
const eventSource = new EventSource('/api/admin/target-alerts/stream');

eventSource.addEventListener('alert_created', (e) => {
    const alert = JSON.parse(e.data);
    showNotification(`⚠️ ${alert.severity}: ${alert.target_url} - ${alert.error_message}`);
    addAlertToPanel(alert);
    playAlertSound();  // 可选
});

eventSource.addEventListener('alert_resolved', (e) => {
    const data = JSON.parse(e.data);
    removeAlertFromPanel(data.id);
    showNotification(`✅ Alert resolved: ${data.target_url}`);
});

eventSource.addEventListener('target_health_changed', (e) => {
    const data = JSON.parse(e.data);
    updateTargetHealthIndicator(data.target_url, data.healthy);
});
```

### 10.3 Group Target Set 管理页面

```
/dashboard/groups

┌─────────────────────────────────────────────────────────────────────────────┐
│  👥 Groups & Target Sets                                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Groups                                                                     │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Name          Users   Daily Limit   Target Set        Actions         │ │
│  │ ───────────── ─────── ───────────── ───────────────── ─────────────── │ │
│  │ engineering   25      1M tokens     ✅ premium-pool   [Edit] [View]  │ │
│  │ standard      50      500K tokens   ✅ standard-pool  [Edit] [View]  │ │
│  │ (default)     12      -             ✅ global-pool    [Edit]        │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Target Sets                                                                │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Name             Strategy       Targets          Health   Actions     │ │
│  │ ──────────────── ───────────── ───────────────── ──────── ─────────── │ │
│  │ premium-pool     weighted      anthropic (w:2)   🔴 1/2   [Edit]     │ │
│  │                                 bedrock (w:1)                        │ │
│  │ standard-pool    round_robin   openai (w:1)      🟡 1/2   [Edit]     │ │
│  │                                 ollama (w:1)                         │ │
│  │ global-pool      weighted      anthropic (w:2)   🔴 1/3   [Edit]     │ │
│  │                                 openai (w:1)                         │ │
│  │                                 ollama (w:1)                         │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  [Create Group]  [Create Target Set]                                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 11. 文件结构

```
internal/
├── db/
│   ├── group_target_set_repo.go      # GroupTargetSetRepo 实现
│   ├── group_target_set_repo_test.go
│   ├── target_alert_repo.go          # TargetAlertRepo 实现
│   └── target_alert_repo_test.go
│
├── proxy/
│   ├── group_target_selector.go      # GroupTargetSelector 核心选择逻辑
│   ├── group_target_selector_test.go
│   └── sproxy.go                     # 修改: pickLLMTarget 逻辑
│
├── alert/
│   ├── target_alert_manager.go       # TargetAlertManager 实现
│   ├── target_alert_manager_test.go
│   └── target_event.go               # AlertEvent 等类型定义
│
├── api/
│   ├── admin_targetset_handler.go    # Admin API: Group Target Set
│   ├── admin_alert_handler.go        # Admin API: Target Alert
│   └── sse_alert_handler.go          # SSE 告警流
│
└── dashboard/
    ├── alert_handler.go              # Dashboard 告警页面 handler
    └── templates/
        ├── alerts.html               # 告警页面模板
        └── groups.html               # Group/Target Set 管理页面

cmd/sproxy/
└── main.go                           # 新增: admin targetset/alert 子命令

config/
└── sproxy.yaml.example               # 新增: group_target_sets, target_alerts 配置示例
```

---

## 12. 迁移策略

### 12.1 数据库迁移

```sql
-- migrations/2026032501_group_target_sets.sql

-- 1. 创建 group_target_sets 表
CREATE TABLE group_target_sets (
    id              TEXT PRIMARY KEY,
    group_id        TEXT,
    name            TEXT NOT NULL,
    strategy        TEXT NOT NULL DEFAULT 'weighted_random',
    retry_policy    TEXT NOT NULL DEFAULT 'try_next',
    is_default      INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);

CREATE INDEX idx_group_target_sets_group_id ON group_target_sets(group_id);
CREATE INDEX idx_group_target_sets_is_default ON group_target_sets(is_default);

-- 2. 创建 group_target_set_members 表
CREATE TABLE group_target_set_members (
    id              TEXT PRIMARY KEY,
    target_set_id   TEXT NOT NULL,
    target_url      TEXT NOT NULL,
    weight          INTEGER NOT NULL DEFAULT 1,
    priority        INTEGER NOT NULL DEFAULT 0,
    is_active       INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id) ON DELETE CASCADE,
    FOREIGN KEY (target_url) REFERENCES llm_targets(url) ON DELETE CASCADE,
    UNIQUE(target_set_id, target_url)
);

CREATE INDEX idx_gt_members_target_set_id ON group_target_set_members(target_set_id);

-- 3. 创建 target_alerts 表
CREATE TABLE target_alerts (
    id              TEXT PRIMARY KEY,
    target_url      TEXT NOT NULL,
    alert_type      TEXT NOT NULL,
    severity        TEXT NOT NULL,
    status_code     INTEGER,
    error_message   TEXT,
    affected_groups TEXT,
    resolved_at     DATETIME,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_url) REFERENCES llm_targets(url)
);

CREATE INDEX idx_target_alerts_target_url ON target_alerts(target_url);
CREATE INDEX idx_target_alerts_created_at ON target_alerts(created_at);
CREATE INDEX idx_target_alerts_unresolved ON target_alerts(resolved_at) WHERE resolved_at IS NULL;

-- 4. 扩展 groups 表
ALTER TABLE groups ADD COLUMN target_set_id TEXT 
    REFERENCES group_target_sets(id) ON DELETE SET NULL;
ALTER TABLE groups ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;

-- 5. 创建默认组（第二类：默认组，用于未分组用户）
INSERT INTO groups (id, name, is_default, created_at)
VALUES ('default', 'default', 1, CURRENT_TIMESTAMP)
ON CONFLICT (id) DO UPDATE SET is_default = 1;

-- 6. 创建默认 target set（第二类：默认组使用所有 active targets）
INSERT INTO group_target_sets (id, group_id, name, strategy, is_default, created_at)
VALUES (
    'default-set',
    NULL,              -- NULL 表示默认组
    'default-global-pool',
    'weighted_random',
    1,                 -- 标记为默认组
    CURRENT_TIMESTAMP
);

-- 7. 将所有现有 active targets 加入默认 set
INSERT INTO group_target_set_members (id, target_set_id, target_url, weight, created_at)
SELECT 
    lower(hex(randomblob(16))),
    'default-set',
    url,
    COALESCE(weight, 1),
    CURRENT_TIMESTAMP
FROM llm_targets
WHERE is_active = 1;
```

### 12.2 配置迁移

```yaml
# 升级后的 sproxy.yaml 示例

# 旧配置（仍然兼容）
groups:
  - name: engineering
    daily_token_limit: 1000000
    # ...

# 新增配置
group_target_sets:
  - name: engineering-pool
    group_name: engineering  # 关联到上面的 group
    strategy: weighted_random
    targets:
      - url: "https://api.anthropic.com"
        weight: 2
      - url: "https://bedrock-proxy.company.com"
        weight: 1

target_alerts:
  enabled: true
  triggers:
    - type: "rate_limited"
      status_codes: [429]
      severity: "warning"
```

### 12.3 向后兼容性

| 场景 | 行为 |
|------|------|
| 未配置 group_target_sets | 使用默认 target set（第二类：默认组使用所有 active targets） |
| Group 无 target set 绑定 | 回退到默认 target set（第二类） |
| 未分组用户（GroupID=NULL） | 自动使用默认 target set（第二类） |
| 存在旧版 LLMBinding | 优先使用 LLMBinding（向后兼容，单 target 模式） |
| 未启用 target_alerts | 告警功能关闭，不影响核心功能 |

---

## 13. 测试策略

### 13.1 单元测试

```go
// internal/proxy/group_target_selector_test.go

func TestGroupTargetSelector_SelectTarget(t *testing.T) {
    // 测试场景:
    // 1. Group 有多个 healthy targets → 按策略选择
    // 2. Group 部分 targets unhealthy → 过滤后选择
    // 3. Group 所有 targets unhealthy → 返回错误
    // 4. Group 无 target set → 使用默认
    // 5. tried 过滤 → 不选择已尝试的 target
}

func TestGroupTargetSelector_Failover(t *testing.T) {
    // 测试组内故障转移:
    // 1. 第一个 target 失败 → 自动选择第二个
    // 2. 所有 targets 失败 → 返回错误
}
```

### 13.2 集成测试

```go
// internal/api/admin_targetset_handler_test.go

func TestAdminTargetSetAPI(t *testing.T) {
    // 测试场景:
    // 1. 创建 target set
    // 2. 添加 targets
    // 3. 更新权重
    // 4. 删除 target set
    // 5. Group 使用 target set 进行路由
}

func TestTargetAlertFlow(t *testing.T) {
    // 测试告警流程:
    // 1. Target 返回错误 → 告警创建
    // 2. Dashboard SSE 收到事件
    // 3. Target 恢复 → 告警解决
}
```

### 13.4 智能路由专项测试（Review 后新增）

```go
// internal/proxy/smart_router_test.go

func TestSmartRouter_ClassificationAccuracy(t *testing.T) {
    // 测试分类准确率
    testCases := []struct {
        name         string
        messages     []Message
        expectedPool string  // "small" or "large"
        minConfidence float64
    }{
        {
            name: "代码理解任务",
            messages: []Message{
                {Role: "user", Content: "解释这段代码的作用：func main() { fmt.Println("Hello") }"},
            },
            expectedPool: "small",
            minConfidence: 0.8,
        },
        {
            name: "复杂推理任务",
            messages: []Message{
                {Role: "user", Content: "分析全球气候变化对农业经济的长期影响，并提出应对策略"},
            },
            expectedPool: "large",
            minConfidence: 0.9,
        },
        // ... 更多测试用例
    }
    
    // 要求分类准确率 > 90%
}

func TestSmartRouter_CacheHit(t *testing.T) {
    // 测试缓存命中
    // 1. 首次请求：缓存未命中，触发异步分类
    // 2. 相同请求：缓存命中，直接使用结果
    // 3. 验证缓存命中率 > 80%
}

func TestSmartRouter_CircuitBreaker(t *testing.T) {
    // 测试熔断机制
    // 1. 模拟分类器连续失败 5 次
    // 2. 验证熔断器打开，请求降级到默认池
    // 3. 等待恢复时间，验证熔断器半开状态
    // 4. 验证测试请求成功后熔断器关闭
}

func TestSmartRouter_FallbackBehavior(t *testing.T) {
    // 测试 fallback 行为
    // 1. 分类为 small，small pool 可用 → 使用 small pool
    // 2. 分类为 small，small pool 不可用 → fallback 到 large pool
    // 3. 分类为 large，large pool 不可用 → **绝不** fallback 到 small pool
}

func TestSmartRouter_Timeout(t *testing.T) {
    // 测试分类超时降级
    // 1. 模拟分类器超时（> 500ms）
    // 2. 验证请求立即使用默认池（不等待分类）
    // 3. 验证后台异步分类仍在执行
}

func TestSmartRouter_PoolIsolation(t *testing.T) {
    // 测试 pool 隔离性
    // 1. small pool 熔断后，请求是否路由到 large pool
    // 2. large pool 熔断后，请求是否返回错误（而不是路由到 small）
}

// internal/proxy/smart_router_benchmark_test.go

func BenchmarkSmartRouter_Classification(b *testing.B) {
    // 基准测试：分类延迟
    // 目标：缓存命中时 < 1ms，缓存未命中时 < 500ms
}

func BenchmarkSmartRouter_CacheHitRatio(b *testing.B) {
    // 基准测试：缓存命中率
    // 模拟真实请求分布，验证命中率 > 80%
}

// internal/lb/circuit_breaker_test.go

func TestCircuitBreaker_StateTransitions(t *testing.T) {
    // 测试状态机转换
    // CLOSED → OPEN (连续失败)
    // OPEN → HALF_OPEN (超时)
    // HALF_OPEN → CLOSED (测试成功)
    // HALF_OPEN → OPEN (测试失败)
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
    // 测试并发安全性
    // 100 个 goroutine 同时调用，验证无竞态条件
}
```

### 13.5 智能路由 E2E 测试

```bash
# test/e2e/smart_routing_test.sh

# =============================================================================
# 智能路由 E2E 测试
# =============================================================================

# 前置：启用智能路由的组
./sproxy admin targetset create smart-pool --group engineering
./sproxy admin targetset set-auto-routing smart-pool --enabled true

# 配置小模型池（Ollama）
./sproxy admin targetset small-pool add smart-pool \
  --url "http://ollama:11434" \
  --model-pattern "qwen2.5-coder:*"
./sproxy admin targetset small-pool set-fallback smart-pool --enabled true

# 配置大模型池
./sproxy admin targetset large-pool add smart-pool \
  --url "https://api.anthropic.com"

# 场景 1: 代码理解任务走小模型
RESPONSE=$(curl -s -X POST http://sproxy:9000/v1/messages \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "messages": [{"role": "user", "content": "解释这段代码的作用：func main() {}"}]
  }')
# 验证：请求路由到 ollama:11434

# 场景 2: 复杂任务走大模型
RESPONSE=$(curl -s -X POST http://sproxy:9000/v1/messages \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "messages": [{"role": "user", "content": "分析全球气候变化对经济的影响"}]
  }')
# 验证：请求路由到 api.anthropic.com

# 场景 3: 小模型故障 fallback 到大模型
# 关闭 Ollama 服务
docker stop ollama

RESPONSE=$(curl -s -X POST http://sproxy:9000/v1/messages \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "messages": [{"role": "user", "content": "解释这段代码"}]
  }')
# 验证：虽然分类为 small，但请求 fallback 到 anthropic

# 场景 4: 分类缓存验证
# 相同请求发送 10 次
for i in {1..10}; do
  curl -s -X POST http://sproxy:9000/v1/messages \
    -H "Authorization: Bearer $TOKEN" \
    -d '{"messages": [{"role": "user", "content": "测试消息"}]}'
done
# 验证：只有首次触发分类 API 调用，后续使用缓存

# 场景 5: Dashboard 实时统计验证
# 检查 Dashboard 显示：
# - Small Pool 请求数
# - Large Pool 请求数
# - Fallback 事件数
# - 缓存命中率

# 场景 6: 熔断器测试
# 模拟分类器故障（返回 500）
mock_classifier --fail 10

# 发送请求
RESPONSE=$(curl -s -X POST http://sproxy:9000/v1/messages ...)
# 验证：第 5 次请求后熔断器打开，请求直接走默认池

# 等待 30s（恢复超时）
sleep 30

# 再次发送请求
RESPONSE=$(curl -s -X POST http://sproxy:9000/v1/messages ...)
# 验证：熔断器半开，允许测试请求
```

---

## 14. 性能考虑

### 14.1 数据库查询优化

```go
// GroupTargetSetRepo 使用缓存

type GroupTargetSetRepo struct {
    db     *gorm.DB
    logger *zap.Logger
    cache  *lru.Cache  // groupID -> *GroupTargetSet
}

// GetByGroupID 优先从缓存读取
func (r *GroupTargetSetRepo) GetByGroupID(groupID string) (*GroupTargetSet, error) {
    if cached, ok := r.cache.Get(groupID); ok {
        return cached.(*GroupTargetSet), nil
    }
    // 从 DB 查询并缓存
}
```

### 14.2 告警系统性能

- **内存限制**: 最多保留 1000 条活跃告警
- **事件聚合**: 同一 target 的相同错误在 1 分钟内聚合为一条告警
- **异步处理**: 告警记录写入使用异步 channel，不阻塞请求路径

### 14.3 目标选择性能

- **预加载**: 启动时预加载所有 target sets
- **健康状态缓存**: 复用现有 HealthChecker 的缓存
- **无锁读取**: 使用 atomic.Value 存储 target set 快照

---

## 15. 安全考虑

### 15.1 权限控制

| 操作 | 所需权限 |
|------|----------|
| 查看 target sets | admin / readonly |
| 创建/修改 target sets | admin only |
| 查看告警 | admin / readonly |
| 解决告警 | admin only |
| 订阅告警 SSE | admin only |

### 15.2 数据保护

- `target_alerts` 表中的 `error_message` 不应包含敏感信息（如 API keys）
- SSE 连接必须使用认证（复用现有 JWT middleware）

---

## 16. 实现检查清单

### 16.1 数据层

- [ ] `GroupTargetSetRepo` 实现 + 单元测试
- [ ] `TargetAlertRepo` 实现 + 单元测试
- [ ] 数据库 migration 脚本

### 16.2 业务逻辑层

- [ ] `GroupTargetSelector` 实现 + 单元测试
- [ ] `TargetAlertManager` 实现 + 单元测试
- [ ] 修改 `pickLLMTarget` 支持 group target sets

### 16.3 API 层

- [ ] Admin API: Group Target Set CRUD
- [ ] Admin API: Target Alert 查询/解决
- [ ] SSE: 实时告警流

### 16.4 Dashboard

- [ ] 告警页面 HTML/JS
- [ ] Group/Target Set 管理页面
- [ ] 实时告警通知组件

### 16.5 CLI

- [ ] `sproxy admin targetset` 子命令
- [ ] `sproxy admin alert` 子命令

### 16.6 配置与文档

- [ ] 更新 `sproxy.yaml.example`
- [ ] 更新 `docs/manual.md`
- [ ] 更新 `docs/UPGRADE.md`

---

## 17. 不在范围内（Out of Scope）

| 功能 | 原因 | 未来方向 |
|------|------|----------|
| 跨 Group 的 target 共享 | 保持隔离性 | v2 可考虑 target pool 模板 |
| 基于 latency 的智能路由 | 复杂度较高 | 可在 GroupTargetSelector 中扩展 |
| 告警通知到外部系统（Slack/Email） | 现有 webhook 已支持 | 复用现有 alert.Notifier |
| 自动扩缩容 target | 需要集成云服务 API | 未来插件化实现 |
| Group 级 API Key 加密隔离 | 当前架构不支持 | 需重新设计 key 管理 |

---

## 18. 成功标准

### 18.1 功能标准

- ✅ **两类群组支持**：
  - 第一类（普通组）：正常创建的群组可绑定专属 target set
  - 第二类（默认组）：未分组用户自动归属，使用全局 target pool
- ✅ 普通组内 target 故障时自动重试同组其他 targets
- ✅ 用户单一归属：同一时间只能属于一个组
- ✅ Target 错误实时显示在 Dashboard 告警面板
- ✅ 向后兼容：旧版 LLMBinding 仍然有效

### 18.2 智能路由标准

- ✅ **分类准确率**: 请求正确分类到小模型或大模型（> 90%）
- ✅ **故障转移**: 小模型失败时自动 fallback 到大模型池
- ✅ **隔离性**: 大模型失败时**绝不**路由到小模型池
- ✅ **降级策略**: 分类失败时保守 fallback 到大模型池
- ✅ **性能**: 分类延迟 < 500ms
- ✅ **可观测**: Dashboard 显示分类统计和 fallback 事件

### 18.3 性能标准

- ✅ Group target 选择 < 1ms
- ✅ 智能路由分类 < 500ms
- ✅ 告警事件延迟 < 100ms（SSE 推送）
- ✅ 支持 1000 个活跃告警不丢事件

### 18.4 质量标准

- ✅ 单元测试覆盖率 > 80%
- ✅ 集成测试覆盖主要场景
- ✅ 文档完整（配置、API、升级指南）

## 19. 智能路由模式（Smart Routing Mode）

### 19.1 设计意图

在满足基本负载均衡的基础上，支持基于请求特征的**分层路由**：

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        智能路由模式架构                                     │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  请求进入                                                                    │
│    ↓                                                                        │
│  检查 Group 的 auto_routing.enabled                                          │
│    ↓                                                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ 智能路由启用                                                        │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │                                                                     │   │
│  │  SemanticRouter 二分类（小模型 vs 大模型）                          │   │
│  │    ↓                                                                │   │
│  │  ┌─────────────────┐    ┌─────────────────┐                        │   │
│  │  │   小模型池      │    │   大模型池      │                        │   │
│  │  │ (Simple Tasks)  │    │ (Complex Tasks) │                        │   │
│  │  │ ─────────────── │    │ ─────────────── │                        │   │
│  │  │ • Ollama qwen   │    │ • Anthropic     │                        │   │
│  │  │ • Local LLM     │    │ • OpenAI        │                        │   │
│  │  │                 │    │ • GPT-4         │                        │   │
│  │  └────────┬────────┘    └────────┬────────┘                        │   │
│  │           │                      │                                 │   │
│  │           ▼                      ▼                                 │   │
│  │    小模型失败时              大模型失败时                          │   │
│  │    ┌──────────────┐         ┌──────────────┐                      │   │
│  │    │ Fallback 到  │         │ 只在大模型池 │                      │   │
│  │    │  大模型池    │         │   内重试     │                      │   │
│  │    └──────────────┘         └──────────────┘                      │   │
│  │           │                      │                                 │   │
│  │           └──────────────────────┘                                 │   │
│  │                      ↓                                             │   │
│  │              所有池都失败 → 返回错误                               │   │
│  │                                                                     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ 智能路由禁用（默认模式）                                            │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │                                                                     │   │
│  │  使用 default_targets 池（所有 targets）                            │   │
│  │    ↓                                                                │   │
│  │  标准负载均衡（weighted_random / round_robin）                      │   │
│  │    ↓                                                                │   │
│  │  失败时在 default_targets 池内重试                                  │   │
│  │                                                                     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 19.2 关键约束

| 约束 | 说明 |
|------|------|
| **小模型定位** | 用于代码理解、简单问答等轻量任务 |
| **大模型定位** | 用于复杂推理、长文本生成等重任务 |
| **小模型故障处理** | 小模型失败（429/503）可 fallback 到大模型池 |
| **大模型故障处理** | 大模型失败只在大模型池内重试，**绝不路由到小模型** |
| **分类粒度** | 二分类：非此即彼，不允许多模型混合 |
| **性能保证** | 分类延迟必须 < 100ms（通过缓存实现），否则降级到默认池 |
| **熔断机制** | 当分类器连续失败时，自动降级到默认池 |

### 19.2.1 性能优化策略（Review 后新增）

为避免 SemanticRouter 调用带来的延迟问题，实现以下优化：

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                     智能路由性能优化架构                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  请求进入                                                                    │
│    ↓                                                                        │
│  提取请求指纹（messages hash）                                              │
│    ↓                                                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ ClassificationCache（内存缓存）                                     │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │                                                                     │   │
│  │  Key: hash(messages[:200])  // 前200字符                           │   │
│  │  Value: {classification, confidence, timestamp}                     │   │
│  │  TTL: 1 hour                                                        │   │
│  │                                                                     │   │
│  │  缓存命中？                                                         │   │
│  │    ├─ Yes → 使用缓存结果                                            │   │
│  │    └─ No  → 异步分类（见下文）                                      │   │
│  │                                                                     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ 异步分类策略（Async Classification）                                │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │                                                                     │   │
│  │  首次请求（无缓存）：                                                 │   │
│  │    1. 立即使用默认池（大模型池）处理请求 ← 不等待分类               │   │
│  │    2. 后台异步执行分类                                                │   │
│  │    3. 缓存分类结果供下次使用                                          │   │
│  │                                                                     │   │
│  │  后续请求（有缓存）：                                                 │   │
│  │    1. 使用缓存的分类结果选择 pool                                     │   │
│  │    2. 后台异步更新分类（如果 messages 变化较大）                     │   │
│  │                                                                     │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**缓存策略详解**：

```go
// ClassificationCache 分类结果缓存
type ClassificationCache struct {
    // LRU 缓存，按用户 ID 或请求指纹
    cache      *lru.Cache
    ttl        time.Duration  // 默认 1 小时
    hitCount   atomic.Int64   // 命中率统计
    missCount  atomic.Int64
}

// 缓存键生成（基于请求内容）
func (c *ClassificationCache) makeKey(messages []Message) string {
    // 取前 200 字符作为 fingerprint（足够判断任务类型）
    fingerprint := extractFingerprint(messages, 200)
    return hash(fingerprint)
}

// GetOrDefault 获取分类结果（带超时降级）
func (c *ClassificationCache) GetOrDefault(
    ctx context.Context,
    messages []Message,
    defaultValue string,  // 默认使用大模型
) (string, bool) {
    key := c.makeKey(messages)
    
    // 1. 检查缓存
    if val, ok := c.cache.Get(key); ok {
        c.hitCount.Add(1)
        return val.(string), true
    }
    c.missCount.Add(1)
    
    // 2. 缓存未命中：启动异步分类
    go c.asyncClassify(key, messages)
    
    // 3. 立即返回默认值（不阻塞请求）
    return defaultValue, false
}

// asyncClassify 异步分类并缓存结果
func (c *ClassificationCache) asyncClassify(key string, messages []Message) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    result, err := c.classifier.Classify(ctx, messages)
    if err != nil {
        c.logger.Warn("async classification failed", zap.Error(err))
        return
    }
    
    c.cache.Add(key, result, c.ttl)
}
```

### 19.2.2 熔断机制（Circuit Breaker）

当分类器或特定 pool 出现问题时，自动降级：

```go
// CircuitBreaker 熔断器
type CircuitBreaker struct {
    name            string
    failureThreshold    int           // 连续失败阈值（默认 5）
    recoveryTimeout     time.Duration // 恢复超时（默认 30s）
    halfOpenMaxCalls    int           // 半开状态测试请求数（默认 3）
    
    state           State          // CLOSED, OPEN, HALF_OPEN
    failureCount    int
    lastFailureTime time.Time
    successCount    int  // 半开状态下的成功计数
}

type State int

const (
    StateClosed    State = 0  // 正常状态
    StateOpen      State = 1  // 熔断状态（快速失败）
    StateHalfOpen  State = 2  // 半开状态（测试恢复）
)

// Call 包装调用，实现熔断逻辑
func (cb *CircuitBreaker) Call(fn func() error) error {
    switch cb.state {
    case StateOpen:
        if time.Since(cb.lastFailureTime) > cb.recoveryTimeout {
            cb.state = StateHalfOpen
            cb.successCount = 0
        } else {
            return ErrCircuitBreakerOpen  // 快速失败
        }
        
    case StateHalfOpen:
        if cb.successCount >= cb.halfOpenMaxCalls {
            cb.state = StateClosed
            cb.failureCount = 0
        }
    }
    
    err := fn()
    if err != nil {
        cb.recordFailure()
        return err
    }
    
    cb.recordSuccess()
    return nil
}
```

**熔断策略应用**：

| 组件 | 熔断条件 | 降级行为 |
|------|----------|----------|
| 分类器 | 连续 5 次失败 | 跳过分类，直接使用默认池 |
| 小模型池 | 错误率 > 50% | 禁用 small pool，所有请求走 large pool |
| 大模型池 | 错误率 > 50% | 返回错误（无降级） |

### 19.2.3 配额处理策略（Review 后新增）

**核心原则**：配额始终按 Group 级别计算，不因 fallback 而改变。

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          配额处理策略                                       │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Group: engineering (Daily Limit: 1,000,000 tokens)                         │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ 初始请求（small pool）                                              │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │ Request 1 → Classification: small                                   │   │
│  │   ↓                                                                 │   │
│  │ Small Pool: Ollama qwen2.5-coder                                    │   │
│  │   ↓                                                                 │   │
│  │ Token Usage: 500 tokens                                             │   │
│  │   ↓                                                                 │   │
│  │ Group Quota: 1,000,000 - 500 = 999,500 remaining                    │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │ Fallback 场景                                                       │   │
│  │ ─────────────────────────────────────────────────────────────────── │   │
│  │ Request 2 → Classification: small                                   │   │
│  │   ↓                                                                 │   │
│  │ Small Pool: Ollama (Quota Exceeded / Timeout)                       │   │
│  │   ↓                                                                 │   │
│  │ Fallback to Large Pool: Anthropic                                 │   │
│  │   ↓                                                                 │   │
│  │ Token Usage: 2,000 tokens                                           │   │
│  │   ↓                                                                 │   │
│  │ Group Quota: 999,500 - 2,000 = 997,500 remaining                    │   │
│  │                                                                     │   │
│  │ ⚠️ 重要：quota 计算基于 Group，不区分 small/large pool             │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

**配额计算规则**：

```go
// QuotaManager 配额管理器（增强版）
type QuotaManager struct {
    // 按 Group 维护配额（不按 pool）
    groupQuotas map[string]*GroupQuota
}

type GroupQuota struct {
    GroupID           string
    DailyLimit        int64
    DailyUsed         int64
    LastResetTime     time.Time
}

// Consume 消费配额（在请求完成后调用）
func (qm *QuotaManager) Consume(groupID string, tokens int64) error {
    quota := qm.groupQuotas[groupID]
    
    // 检查并刷新日配额
    if time.Now().After(quota.LastResetTime.Add(24 * time.Hour)) {
        quota.DailyUsed = 0
        quota.LastResetTime = time.Now()
    }
    
    // 检查配额
    if quota.DailyUsed + tokens > quota.DailyLimit {
        return ErrQuotaExceeded
    }
    
    // 扣除配额（原子操作）
    quota.DailyUsed += tokens
    return nil
}

// CheckBeforeRequest 请求前检查（预估）
func (qm *QuotaManager) CheckBeforeRequest(groupID string, estimatedTokens int64) error {
    quota := qm.groupQuotas[groupID]
    
    if quota.DailyUsed + estimatedTokens > quota.DailyLimit {
        return ErrQuotaExceeded
    }
    
    return nil
}
```

**fallback 场景配额处理**：

```go
// SmartRouter 处理 fallback 时的配额
func (sr *SmartRouter) HandleRequest(ctx context.Context, req Request) (*Response, error) {
    groupID := req.GroupID
    
    // 1. 预估 token 数
    estimatedTokens := estimateTokens(req.Messages)
    
    // 2. 预检查配额
    if err := sr.quotaManager.CheckBeforeRequest(groupID, estimatedTokens); err != nil {
        return nil, err  // 配额不足，直接返回错误
    }
    
    // 3. 路由决策
    decision, err := sr.Route(ctx, req.Messages)
    if err != nil {
        return nil, err
    }
    
    // 4. 尝试目标 pool
    target, err := sr.selectTarget(decision.PoolType, req.Tried)
    if err != nil {
        // 可能需要 fallback
        if decision.PoolType == "small" && sr.config.SmallPool.FallbackEnabled {
            // fallback 到 large pool
            target, err = sr.selectTarget("large", req.Tried)
            if err != nil {
                return nil, err
            }
            // 继续处理，quota 计算不变
        } else {
            return nil, err
        }
    }
    
    // 5. 执行请求
    resp, err := sr.executeRequest(ctx, target, req)
    if err != nil {
        return nil, err
    }
    
    // 6. 实际扣减配额（基于实际返回的 token 数）
    actualTokens := resp.Usage.TotalTokens
    if err := sr.quotaManager.Consume(groupID, actualTokens); err != nil {
        // 实际 token 超出配额，记录超限但请求已完成
        sr.logger.Warn("quota exceeded after request",
            zap.String("group_id", groupID),
            zap.Int64("actual_tokens", actualTokens),
            zap.Int64("quota_remaining", sr.quotaManager.GetRemaining(groupID)),
        )
        // 请求已完成，不中断返回
    }
    
    return resp, nil
}
```

**关键约束**：

| 场景 | 配额处理 |
|------|----------|
| 正常请求（small） | 按 Group 配额扣除 |
| 正常请求（large） | 按 Group 配额扣除 |
| fallback（small → large） | 仍按 Group 配额扣除，不区分 pool |
| 配额不足预检查 | 基于预估 token，不足则直接拒绝 |
| 实际 token 超出 | 记录告警但请求已完成（fail-open） |

#### 扩展 `group_target_sets` 表

```sql
-- 新增 auto_routing 配置字段
ALTER TABLE group_target_sets ADD COLUMN auto_routing_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE group_target_sets ADD COLUMN classifier_config TEXT;  -- JSON: 分类器配置

-- 分类结果类型
CREATE TABLE routing_classifications (
    id              TEXT PRIMARY KEY,
    target_set_id   TEXT NOT NULL,
    classification  TEXT NOT NULL CHECK(classification IN ('small', 'large')),
    description     TEXT,  -- 自然语言描述（给分类器看的）
    priority        INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id) ON DELETE CASCADE,
    UNIQUE(target_set_id, classification)
);
```

#### 新增表: `auto_routing_pools`

```sql
-- 自动路由的目标池配置
CREATE TABLE auto_routing_pools (
    id                  TEXT PRIMARY KEY,
    target_set_id       TEXT NOT NULL,
    pool_type           TEXT NOT NULL CHECK(pool_type IN ('small', 'large')),
    strategy            TEXT NOT NULL DEFAULT 'weighted_random',
    fallback_enabled    INTEGER NOT NULL DEFAULT 0,  -- 小模型池特有：是否可fallback到大模型
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id) ON DELETE CASCADE,
    UNIQUE(target_set_id, pool_type)
);

-- 小模型池成员
CREATE TABLE auto_routing_pool_members (
    id              TEXT PRIMARY KEY,
    pool_id         TEXT NOT NULL,
    target_url      TEXT NOT NULL,
    weight          INTEGER NOT NULL DEFAULT 1,
    model_pattern   TEXT,  -- 模型匹配模式（如 "qwen2.5-coder:*"）
    is_active       INTEGER NOT NULL DEFAULT 1,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    FOREIGN KEY (pool_id) REFERENCES auto_routing_pools(id) ON DELETE CASCADE,
    FOREIGN KEY (target_url) REFERENCES llm_targets(url) ON DELETE CASCADE,
    UNIQUE(pool_id, target_url)
);
```

### 19.3.1 model_pattern 匹配规则（Review 后新增）

`model_pattern` 用于在 Ollama 等支持多模型的端点中，指定使用哪个具体模型。

**匹配规则**（简单通配符匹配，非正则）：

```go
// ModelPatternMatcher 模型模式匹配器
type ModelPatternMatcher struct {
    pattern string
}

// Match 检查模型名称是否匹配模式
// 规则：
//   1. 精确匹配：pattern = "qwen2.5-coder:7b" 匹配 "qwen2.5-coder:7b"
//   2. 前缀匹配：pattern = "qwen2.5-coder:*" 匹配 "qwen2.5-coder:7b", "qwen2.5-coder:14b"
//   3. 通配符：pattern = "*coder*" 匹配包含 "coder" 的任何模型名
//   4. 空 pattern：匹配该端点的默认模型
func (m *ModelPatternMatcher) Match(modelName string) bool {
    if m.pattern == "" || m.pattern == "*" {
        return true  // 空模式匹配所有
    }
    
    if strings.HasSuffix(m.pattern, ":*") {
        // 前缀匹配
        prefix := strings.TrimSuffix(m.pattern, ":*")
        return strings.HasPrefix(modelName, prefix)
    }
    
    if strings.Contains(m.pattern, "*") {
        // 简单通配符（仅支持 * 匹配任意字符）
        regex := strings.ReplaceAll(regexp.QuoteMeta(m.pattern), `\*`, ".*")
        matched, _ := regexp.MatchString("^"+regex+"$", modelName)
        return matched
    }
    
    // 精确匹配
    return m.pattern == modelName
}
```

**配置示例**：

```yaml
# Ollama 端点支持多模型
- url: "http://ollama.local:11434"
  model_pattern: "qwen2.5-coder:*"  # 匹配所有 qwen2.5-coder 变体
  weight: 1

- url: "http://ollama.local:11434"  
  model_pattern: "deepseek-coder:6.7b"  # 精确匹配
  weight: 1

- url: "http://ollama.local:11434"
  model_pattern: "*"  # 使用默认模型
  weight: 1

# 云服务商端点（通常单模型）
- url: "https://api.anthropic.com"
  # model_pattern 为空，直接使用端点默认模型
  weight: 2
```

**请求时模型选择**：

```go
// 在请求 Ollama 时，根据匹配的模式选择模型
func (p *OllamaProvider) SelectModel(targetURL string, pattern string) string {
    if pattern == "" || pattern == "*" {
        return "llama2"  // Ollama 默认模型
    }
    
    // 移除通配符，使用具体模型名
    if strings.HasSuffix(pattern, ":*") {
        // 获取该系列最新版本
        return p.getLatestVersion(strings.TrimSuffix(pattern, ":*"))
    }
    
    return pattern
}
```

### 19.4 配置格式（YAML）

```yaml
# -----------------------------------------------------------------------------
# group_target_sets — 支持智能路由的配置
# -----------------------------------------------------------------------------
group_target_sets:
  # 示例 1: 启用智能路由的组
  - name: "engineering-smart"
    group_name: "engineering"
    
    # 智能路由配置
    auto_routing:
      enabled: true
      classifier:
        # 复用 SemanticRouter 配置
        model: "claude-haiku-3-5"  # 分类器模型
        timeout: "3s"
        
        # 分类规则（SemanticRouter 格式）
        rules:
          - classification: "small"
            description: "代码理解、简单问答、轻量级任务"
          - classification: "large"  
            description: "复杂推理、长文本生成、创意写作"
      
      # 小模型池（简单任务）
      small_pool:
        strategy: "round_robin"  # 小模型内轮询
        fallback_to_large: true   # 小模型失败可fallback到大模型
        targets:
          - url: "http://ollama.local:11434"
            model_pattern: "qwen2.5-coder:7b"  # 本地小模型
            weight: 1
          - url: "http://ollama.local:11434"
            model_pattern: "deepseek-coder:6.7b"
            weight: 1
      
      # 大模型池（复杂任务）
      large_pool:
        strategy: "weighted_random"
        targets:
          - url: "https://api.anthropic.com"
            weight: 2
          - url: "https://api.openai.com"
            weight: 1
    
    # 智能路由禁用时的 fallback（普通负载均衡）
    default_targets:
      - url: "https://api.anthropic.com"
        weight: 2
      - url: "https://api.openai.com"
        weight: 1

  # 示例 2: 禁用智能路由（普通负载均衡模式）
  - name: "research-standard"
    group_name: "research"
    auto_routing:
      enabled: false
    
    # 普通负载均衡
    default_targets:
      - url: "https://api.anthropic.com"
        weight: 1
      - url: "https://api.openai.com"
        weight: 1
```

### 19.5 核心算法流程

```go
// SmartRouter 智能路由器
type SmartRouter struct {
    semanticRouter    *router.SemanticRouter  // 复用现有语义路由
    smallPool         *TargetPool
    largePool         *TargetPool
    fallbackEnabled   bool                    // 小模型失败是否fallback
    logger            *zap.Logger
}

// Route 智能路由主入口
func (sr *SmartRouter) Route(ctx context.Context, messages []Message) (*RoutingDecision, error) {
    // 1. SemanticRouter 二分类
    classification, err := sr.classify(ctx, messages)
    if err != nil {
        // 分类失败 → fallback 到 large pool（保守策略）
        sr.logger.Warn("classification failed, fallback to large pool", zap.Error(err))
        return sr.selectFromLargePool(ctx)
    }
    
    switch classification {
    case "small":
        // 2a. 选择小模型池
        target, err := sr.smallPool.Select(ctx)
        if err != nil {
            // 小模型池不可用
            if sr.fallbackEnabled {
                // Fallback 到大模型池
                sr.logger.Info("small pool exhausted, falling back to large pool")
                return sr.selectFromLargePool(ctx)
            }
            return nil, ErrNoHealthyTarget
        }
        return &RoutingDecision{
            Target:       target,
            PoolType:     "small",
            CanFallback:  sr.fallbackEnabled,
        }, nil
        
    case "large":
        // 2b. 选择大模型池
        target, err := sr.largePool.Select(ctx)
        if err != nil {
            return nil, err
        }
        return &RoutingDecision{
            Target:       target,
            PoolType:     "large",
            CanFallback:  false,  // 大模型不允许再fallback
        }, nil
        
    default:
        // 未知分类 → 使用大模型池
        return sr.selectFromLargePool(ctx)
    }
}

// RetryPolicy 重试策略（关键：大模型失败不重试小模型）
func (sr *SmartRouter) GetRetryTargets(poolType string, tried []string) ([]Target, error) {
    switch poolType {
    case "small":
        // 小模型失败：先在小模型池重试，然后可以fallback到大模型
        smallTargets := sr.smallPool.GetHealthyExcluding(tried)
        if len(smallTargets) > 0 {
            return smallTargets, nil
        }
        if sr.fallbackEnabled {
            return sr.largePool.GetHealthy(), nil
        }
        return nil, ErrNoHealthyTarget
        
    case "large":
        // 大模型失败：**只在大模型池重试**
        largeTargets := sr.largePool.GetHealthyExcluding(tried)
        if len(largeTargets) > 0 {
            return largeTargets, nil
        }
        // **绝不返回小模型**
        return nil, ErrNoHealthyTarget
        
    default:
        return nil, ErrInvalidPoolType
    }
}
```

### 19.6 修改 pickLLMTarget 逻辑

```go
// internal/proxy/sproxy.go

func (sp *SProxy) pickLLMTarget(
    path, userID, groupID string,
    tried []string,
    candidateFilter []string,
) (*lb.LLMTargetInfo, error) {
    
    triedSet := make(map[string]bool, len(tried))
    for _, u := range tried {
        triedSet[u] = true
    }

    // 1. 用户级 LLMBinding（最高优先级，向后兼容）
    if sp.bindingResolver != nil {
        if boundURL, found := sp.bindingResolver(userID, ""); found {
            return sp.llmTargetInfoForURL(boundURL), nil
        }
    }

    // 2. [新增] Group Target Set 选择
    if sp.groupTargetSelector != nil && groupID != "" {
        targetSet, err := sp.groupTargetSelector.GetTargetSet(groupID)
        if err == nil && targetSet != nil {
            
            // 2a. 检查是否启用智能路由
            if targetSet.AutoRoutingEnabled && sp.smartRouter != nil {
                // 从请求 body 提取 messages
                messages := sp.extractMessagesFromContext()
                
                // 智能路由决策
                decision, err := sp.smartRouter.Route(context.Background(), messages)
                if err == nil && decision != nil {
                    sp.logger.Debug("smart routing decision",
                        zap.String("pool_type", decision.PoolType),
                        zap.String("target_url", decision.Target.URL),
                    )
                    
                    return &lb.LLMTargetInfo{
                        URL:      decision.Target.URL,
                        APIKey:   decision.Target.APIKey,
                        Provider: decision.Target.Provider,
                        PoolType: decision.PoolType,  // 传递池类型用于重试策略
                    }, nil
                }
            }
            
            // 2b. 普通负载均衡模式
            target, hasMore, err := sp.groupTargetSelector.SelectTarget(
                context.Background(),
                groupID,
                tried,
            )
            if err == nil && target != nil {
                return &lb.LLMTargetInfo{
                    URL:      target.URL,
                    APIKey:   target.APIKey,
                    Provider: target.Provider,
                }, nil
            }
        }
    }

    // 3. 默认组处理（原有逻辑）
    // ...
}
```

### 19.7 RetryTransport 适配智能路由

```go
// internal/lb/retry_transport.go

func (rt *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    var lastErr error
    tried := make([]string, 0, rt.MaxRetries+1)
    poolType := req.Header.Get("X-Pool-Type")  // 获取池类型（small/large）
    
    for attempt := 0; attempt <= rt.MaxRetries; attempt++ {
        var target *LLMTargetInfo
        var err error
        
        if attempt == 0 {
            // 首次请求
            target, err = rt.PickNext(req.URL.Path, tried)
        } else {
            // 重试：根据池类型选择重试策略
            if rt.smartRouter != nil && poolType != "" {
                // 智能路由重试
                targets, err := rt.smartRouter.GetRetryTargets(poolType, tried)
                if err != nil {
                    return nil, err
                }
                target = rt.selectFromTargets(targets)
            } else {
                // 普通重试
                target, err = rt.PickNext(req.URL.Path, tried)
            }
        }
        
        if err != nil {
            return nil, err
        }
        
        tried = append(tried, target.URL)
        resp, err := rt.doAttempt(req, target)
        
        if err != nil {
            lastErr = err
            rt.recordError(target.URL, err)
            rt.OnFailure(target.URL)
            continue
        }
        
        if rt.shouldRetry(resp.StatusCode) {
            lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
            rt.recordErrorWithStatus(target.URL, resp.StatusCode)
            rt.OnFailure(target.URL)
            continue
        }
        
        rt.OnSuccess(target.URL)
        return resp, nil
    }
    
    return nil, lastErr
}
```

### 19.8 Admin CLI 命令

```bash
# =============================================================================
# 智能路由管理
# =============================================================================

# 启用/禁用组的智能路由
./sproxy admin targetset set-auto-routing <set_name> --enabled true
./sproxy admin targetset set-auto-routing <set_name> --enabled false

# 配置分类器
./sproxy admin targetset set-classifier <set_name> \
  --model "claude-haiku-3-5" \
  --timeout "3s"

# 配置小模型池
./sproxy admin targetset small-pool add <set_name> \
  --url "http://ollama.local:11434" \
  --model-pattern "qwen2.5-coder:7b" \
  --weight 1

./sproxy admin targetset small-pool set-fallback <set_name> --enabled true

# 配置大模型池
./sproxy admin targetset large-pool add <set_name> \
  --url "https://api.anthropic.com" \
  --weight 2

# 查看智能路由配置
./sproxy admin targetset show-routing <set_name>
```

### 19.9 Dashboard 展示

```
/dashboard/groups/engineering

┌─────────────────────────────────────────────────────────────────────────────┐
│  Group: engineering                    [Smart Routing: ENABLED]  [Disable]  │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Smart Routing Configuration                                                │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Classifier: claude-haiku-3-5  Timeout: 3s                            │ │
│  │                                                                       │ │
│  │ Small Pool (Simple Tasks)         Fallback to Large: ✅               │ │
│  │ ┌─────────────────────────────────────────────────────────────────┐ │ │
│  │ │ Target                          Model           Weight  Status  │ │ │
│  │ │ http://ollama.local:11434       qwen2.5-coder:7b  1     🟢 Up   │ │ │
│  │ │ http://ollama.local:11434       deepseek-coder    1     🟢 Up   │ │ │
│  │ └─────────────────────────────────────────────────────────────────┘ │ │
│  │                                                                       │ │
│  │ Large Pool (Complex Tasks)                                            │ │
│  │ ┌─────────────────────────────────────────────────────────────────┐ │ │
│  │ │ Target                          Weight  Avg Latency  Status     │ │ │
│  │ │ https://api.anthropic.com       2       450ms       🟢 Up      │ │ │
│  │ │ https://api.openai.com          1       380ms       🟢 Up      │ │ │
│  │ └─────────────────────────────────────────────────────────────────┘ │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Routing Statistics (Last 24h)                                              │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Total Requests: 15,234                                                │ │
│  │ Small Pool: 8,421 (55%)  Avg Tokens: 512                             │ │
│  │ Large Pool: 6,813 (45%)  Avg Tokens: 2,048                           │ │
│  │ Fallback Events: 23 (small→large)                                    │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 19.10 成功标准（智能路由）

- ✅ 启用智能路由的 Group 能正确分类请求（小模型 vs 大模型）
- ✅ 小模型失败时自动 fallback 到大模型池
- ✅ 大模型失败**绝不**路由到小模型池
- ✅ 分类失败时保守 fallback 到大模型池
- ✅ Dashboard 实时显示分类统计和 fallback 事件
- ✅ 可通过 CLI/API 动态开启/关闭智能路由
- ✅ 向后兼容：禁用智能路由时行为与普通负载均衡一致

---

## 20. 配置验证工具（Review 后新增）

### 20.1 设计目标

解决配置复杂度高的问题，提供配置验证和诊断工具。

### 20.2 验证命令

```bash
# =============================================================================
# 配置验证工具
# =============================================================================

# 验证 YAML 配置文件语法和逻辑
./sproxy admin config validate --file sproxy.yaml

# 输出示例（验证通过）：
# ✓ group_target_sets: 3 sets validated
# ✓ target_set "engineering-pool": 2 targets, all healthy
# ✓ smart routing "engineering-smart": classifier reachable, small_pool has 2 targets
# ✓ default pool: 3 targets
# ✓ All configurations valid

# 输出示例（验证失败）：
# ✗ group_target_set "research-pool": target "http://invalid:8080" unreachable
# ✗ smart routing "engineering-smart": small_pool fallback_to_large=true but large_pool empty
# ✗ model_pattern "qwen2.5-coder:*" in pool "small": no matching models in ollama
# 
# Fix suggestions:
#   1. Check target URL and network connectivity
#   2. Either disable fallback or add targets to large_pool
#   3. Verify ollama has models matching pattern
```

### 20.3 配置测试模式

```bash
# 测试模式：不实际修改配置，仅验证可行性
./sproxy admin config test --file sproxy.yaml --dry-run

# 测试特定 target 的连通性
./sproxy admin config test-target --url "http://ollama:11434" --model "qwen2.5-coder:7b"

# 测试分类器
./sproxy admin config test-classifier \
  --model "claude-haiku-3-5" \
  --message "解释这段代码" \
  --expected "small"

# 输出：
# Classification result: "small" ✓
# Latency: 245ms
# Confidence: 0.92
```

### 20.4 配置生成向导

```bash
# 交互式配置生成
./sproxy admin config wizard

# 向导流程：
# ? Group name: engineering
# ? Enable smart routing? (y/n): y
# ? Classifier model (claude-haiku-3-5/gpt-3.5-turbo): claude-haiku-3-5
# ? Small pool targets (comma-separated URLs): http://ollama:11434
# ? Small pool model pattern: qwen2.5-coder:*
# ? Enable fallback to large pool? (y/n): y
# ? Large pool targets: https://api.anthropic.com, https://api.openai.com
# 
# Generated configuration:
# [显示生成的 YAML，询问是否保存]
```

### 20.5 配置模板

```yaml
# config/templates/smart-routing.yaml
# 一键启用智能路由的模板

template:
  name: "smart-routing-basic"
  description: "基础智能路由配置：小模型(ollama) + 大模型(anthropic)"
  
parameters:
  group_name:
    description: "目标组名"
    required: true
  
  ollama_url:
    description: "Ollama 服务地址"
    default: "http://localhost:11434"
  
  ollama_model:
    description: "小模型模式"
    default: "qwen2.5-coder:*"
  
  large_model_url:
    description: "大模型端点"
    default: "https://api.anthropic.com"
  
  enable_fallback:
    description: "是否启用小模型到大模型的 fallback"
    default: true

# 使用模板
./sproxy admin config apply-template smart-routing-basic \
  --param group_name=engineering \
  --param ollama_url=http://ollama:11434
```

### 20.6 配置健康检查 Dashboard

```
/dashboard/config-health

┌─────────────────────────────────────────────────────────────────────────────┐
│  Configuration Health Check                                                 │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  Overall Status: 🟢 Healthy                                                │
│                                                                             │
│  Group Target Sets                                                          │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ Name              Targets  Smart Routing  Status     Issues           │ │
│  │ engineering       2        Enabled        🟢 Healthy  None           │ │
│  │ research          3        Disabled       🟢 Healthy  None           │ │
│  │ test-group        1        Enabled        🟡 Warning  1 warning      │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Issues                                                                     │
│  ┌───────────────────────────────────────────────────────────────────────┐ │
│  │ ⚠️ test-group: small_pool has only 1 target                          │ │
│  │    Recommendation: Add more targets for high availability            │ │
│  │    [Fix Issue]                                                        │ │
│  └───────────────────────────────────────────────────────────────────────┘ │
│                                                                             │
│  Quick Actions                                                              │
│  [Run Full Validation]  [Download Config]  [Apply Template]                │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 20.7 实现代码示例

```go
// internal/config/validator.go

// ConfigValidator 配置验证器
type ConfigValidator struct {
    logger     *zap.Logger
    testClient *http.Client
}

// ValidateResult 验证结果
type ValidateResult struct {
    Valid      bool
    Errors     []ValidationError
    Warnings   []ValidationWarning
    Suggestions []string
}

// Validate 验证完整配置
func (v *ConfigValidator) Validate(cfg *Config) *ValidateResult {
    result := &ValidateResult{Valid: true}
    
    // 1. 验证 group_target_sets
    for _, set := range cfg.GroupTargetSets {
        v.validateTargetSet(set, result)
    }
    
    // 2. 验证 smart routing
    for _, set := range cfg.GroupTargetSets {
        if set.AutoRouting.Enabled {
            v.validateSmartRouting(set, result)
        }
    }
    
    // 3. 检查循环依赖
    v.checkCircularDeps(cfg, result)
    
    return result
}

// validateSmartRouting 验证智能路由配置
func (v *ConfigValidator) validateSmartRouting(set GroupTargetSet, result *ValidateResult) {
    ar := set.AutoRouting
    
    // 检查 classifier 可访问性
    if err := v.testClassifier(ar.Classifier); err != nil {
        result.AddError(ValidationError{
            Field:   fmt.Sprintf("group_target_sets.%s.auto_routing.classifier", set.Name),
            Message: fmt.Sprintf("Classifier unreachable: %v", err),
            Fix:     "Check classifier URL and API key",
        })
    }
    
    // 检查 small_pool
    if len(ar.SmallPool.Targets) == 0 {
        result.AddError(ValidationError{
            Field:   fmt.Sprintf("group_target_sets.%s.auto_routing.small_pool", set.Name),
            Message: "Small pool has no targets",
            Fix:     "Add at least one target to small_pool",
        })
    }
    
    // 检查 large_pool
    if len(ar.LargePool.Targets) == 0 {
        result.AddError(ValidationError{
            Field:   fmt.Sprintf("group_target_sets.%s.auto_routing.large_pool", set.Name),
            Message: "Large pool has no targets",
            Fix:     "Add at least one target to large_pool",
        })
    }
    
    // 检查 fallback 配置
    if ar.SmallPool.FallbackToLarge && len(ar.LargePool.Targets) == 0 {
        result.AddError(ValidationError{
            Field:   fmt.Sprintf("group_target_sets.%s.auto_routing.small_pool.fallback_to_large", set.Name),
            Message: "Fallback enabled but large_pool is empty",
            Fix:     "Either disable fallback or add targets to large_pool",
        })
    }
    
    // 检查 model_pattern
    for _, t := range ar.SmallPool.Targets {
        if !v.validateModelPattern(t.ModelPattern) {
            result.AddWarning(ValidationWarning{
                Field:   fmt.Sprintf("group_target_sets.%s.auto_routing.small_pool.targets[].model_pattern", set.Name),
                Message: fmt.Sprintf("Model pattern '%s' may not match any models", t.ModelPattern),
                Fix:     "Verify pattern syntax and available models",
            })
        }
    }
}

// testClassifier 测试分类器连通性
func (v *ConfigValidator) testClassifier(cfg ClassifierConfig) error {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    // 发送测试分类请求
    req, _ := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint, nil)
    // ...
    
    resp, err := v.testClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("classifier returned %d", resp.StatusCode)
    }
    
    return nil
}
```

---

## 21. E2E 测试套件（新增章节）

### 21.1 测试环境准备

```bash
# =============================================================================
# E2E 测试前置条件
# =============================================================================

# 1. 启动测试环境
docker-compose -f test/e2e/docker-compose.yml up -d

# 2. 等待服务就绪
./scripts/wait-for-services.sh

# 3. 初始化测试数据
./scripts/init-test-data.sh

# 测试环境包含：
# - sproxy (测试实例)
# - PostgreSQL (可选，默认 SQLite)
# - Mock LLM Targets:
#   * mock-anthropic:8080 (模拟 Anthropic API)
#   * mock-openai:8081 (模拟 OpenAI API)
#   * ollama:11434 (真实 Ollama 实例)
# - Mock Classifier (语义路由测试)
```

### 21.2 基础功能测试

#### 测试 1: Group-Target Set 创建与绑定

```bash
#!/bin/bash
# test/e2e/01_group_target_set.sh

set -e

SProxy_URL="http://localhost:9000"
ADMIN_TOKEN="${ADMIN_TOKEN:-test-admin-token}"

echo "=== Test 1: Group-Target Set 创建与绑定 ==="

# 1.1 创建 Group
echo "Step 1: 创建测试组..."
./sproxy admin group add e2e-test-group \
  --daily-limit 100000 \
  --monthly-limit 1000000 \
  --requests-per-minute 100

# 1.2 创建 Target Set
echo "Step 2: 创建 Target Set..."
./sproxy admin targetset create e2e-test-pool \
  --group e2e-test-group \
  --strategy weighted_random

# 1.3 添加 Targets
echo "Step 3: 添加 Targets..."
./sproxy admin targetset add-target e2e-test-pool \
  --url "http://mock-anthropic:8080" \
  --weight 2

./sproxy admin targetset add-target e2e-test-pool \
  --url "http://mock-openai:8081" \
  --weight 1

# 1.4 验证配置
echo "Step 4: 验证配置..."
./sproxy admin targetset show e2e-test-pool | grep "mock-anthropic"
./sproxy admin targetset show e2e-test-pool | grep "mock-openai"

# 1.5 创建用户并加入组
echo "Step 5: 创建测试用户..."
./sproxy admin user add e2e-user --group e2e-test-group --password test123

# 1.6 获取用户 Token
echo "Step 6: 获取 Token..."
TOKEN=$(./sproxy admin token create e2e-user --ttl 1h)

# 1.7 发送请求，验证路由到组内 target
echo "Step 7: 发送测试请求..."
RESPONSE=$(curl -s -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Hello"}]
  }')

# 1.8 验证请求路由到正确的 target
echo "Step 8: 验证路由..."
# 检查 mock-anthropic 或 mock-openai 收到请求
if ! docker logs mock-anthropic 2>&1 | grep -q "POST /v1/messages"; then
  if ! docker logs mock-openai 2>&1 | grep -q "POST /v1/messages"; then
    echo "FAIL: Request not routed to any target"
    exit 1
  fi
fi

echo "✓ Test 1 passed"
```

#### 测试 2: 组内负载均衡验证

```bash
#!/bin/bash
# test/e2e/02_load_balancing.sh

echo "=== Test 2: 组内负载均衡验证 ==="

# 2.1 发送 100 个请求
for i in {1..100}; do
  curl -s -X POST "${Sproxy_URL}/v1/messages" \
    -H "Authorization: Bearer ${TOKEN}" \
    -d '{
      "model": "claude-3-haiku-20240307",
      "messages": [{"role": "user", "content": "Request '$i'"}]
    }' > /dev/null &
done
wait

# 2.2 统计各 target 收到请求数
ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
OPENAI_COUNT=$(docker logs mock-openai 2>&1 | grep -c "POST /v1/messages" || echo "0")

echo "Anthropic: ${ANTHROPIC_COUNT} requests"
echo "OpenAI: ${OPENAI_COUNT} requests"

# 2.3 验证权重比例 (Anthropic:OpenAI = 2:1)
RATIO=$(echo "scale=2; ${ANTHROPIC_COUNT} / ${OPENAI_COUNT}" | bc)
if (( $(echo "${RATIO} < 1.5 || ${RATIO} > 2.5" | bc -l) )); then
  echo "FAIL: Load balancing ratio not match (expected ~2.0, got ${RATIO})"
  exit 1
fi

echo "✓ Test 2 passed (ratio: ${RATIO})"
```

### 21.3 故障转移测试

#### 测试 3: Target 故障自动转移

```bash
#!/bin/bash
# test/e2e/03_failover.sh

echo "=== Test 3: Target 故障自动转移 ==="

# 3.1 清空日志
docker logs mock-anthropic --tail 0 > /dev/null 2>&1
docker logs mock-openai --tail 0 > /dev/null 2>&1

# 3.2 停止 mock-anthropic（模拟故障）
echo "Step 1: 模拟 Anthropic 故障..."
docker stop mock-anthropic

# 3.3 等待健康检查标记为 unhealthy
sleep 35  # 等待健康检查周期

# 3.4 发送请求，应自动路由到 OpenAI
echo "Step 2: 发送请求..."
for i in {1..10}; do
  curl -s -X POST "${Sproxy_URL}/v1/messages" \
    -H "Authorization: Bearer ${TOKEN}" \
    -d '{
      "model": "claude-3-haiku-20240307",
      "messages": [{"role": "user", "content": "Failover test '$i'"}]
    }' > /dev/null
done

# 3.5 验证所有请求都路由到 OpenAI
echo "Step 3: 验证路由..."
ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
OPENAI_COUNT=$(docker logs mock-openai 2>&1 | grep -c "POST /v1/messages" || echo "0")

if [ "${ANTHROPIC_COUNT}" -ne 0 ]; then
  echo "FAIL: Requests still routed to unhealthy target"
  exit 1
fi

if [ "${OPENAI_COUNT}" -ne 10 ]; then
  echo "FAIL: Not all requests routed to healthy target"
  exit 1
fi

# 3.6 恢复 Anthropic
echo "Step 4: 恢复 Anthropic..."
docker start mock-anthropic
sleep 5

echo "✓ Test 3 passed"
```

#### 测试 4: 所有 Target 故障处理

```bash
#!/bin/bash
# test/e2e/04_all_targets_fail.sh

echo "=== Test 4: 所有 Target 故障处理 ==="

# 4.1 停止所有 targets
docker stop mock-anthropic mock-openai

# 4.2 等待标记为 unhealthy
sleep 35

# 4.3 发送请求，应返回错误
echo "Step 1: 发送请求..."
RESPONSE=$(curl -s -w "%{http_code}" -o /tmp/response.json -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Test"}]
  }')

# 4.4 验证返回 503 错误
if [ "${RESPONSE}" != "503" ]; then
  echo "FAIL: Expected 503, got ${RESPONSE}"
  cat /tmp/response.json
  exit 1
fi

# 4.5 验证告警触发（见告警测试）

# 4.6 恢复
docker start mock-anthropic mock-openai

echo "✓ Test 4 passed"
```

### 21.4 智能路由测试

#### 测试 5: 智能路由分类验证

```bash
#!/bin/bash
# test/e2e/05_smart_routing.sh

echo "=== Test 5: 智能路由分类验证 ==="

# 5.1 创建启用智能路由的组
./sproxy admin targetset create smart-pool \
  --group e2e-smart-group \
  --auto-routing-enabled

# 5.2 配置小模型池（Ollama）
./sproxy admin targetset small-pool add smart-pool \
  --url "http://ollama:11434" \
  --model-pattern "qwen2.5-coder:*"

# 5.3 配置大模型池
docker start mock-anthropic mock-openai  # 确保运行

./sproxy admin targetset large-pool add smart-pool \
  --url "http://mock-anthropic:8080" \
  --weight 1

./sproxy admin targetset set-auto-routing smart-pool --enabled true

# 5.4 创建用户
./sproxy admin user add smart-user --group e2e-smart-group
SMART_TOKEN=$(./sproxy admin token create smart-user --ttl 1h)

# 5.5 测试代码理解任务 → 应路由到小模型
echo "Step 1: 测试代码理解任务..."
docker logs ollama --tail 0 > /dev/null 2>&1

for i in {1..5}; do
  curl -s -X POST "${Sproxy_URL}/v1/messages" \
    -H "Authorization: Bearer ${SMART_TOKEN}" \
    -d '{
      "messages": [{"role": "user", "content": "解释这段代码：func main() { fmt.Println(\"Hello\") }"}]
    }' > /dev/null
done

OLLAMA_COUNT=$(docker logs ollama 2>&1 | grep -c "POST /api/generate" || echo "0")
if [ "${OLLAMA_COUNT}" -lt 3 ]; then
  echo "FAIL: Code understanding tasks not routed to small model"
  exit 1
fi

echo "✓ Code tasks routed to small model: ${OLLAMA_COUNT}/5"

# 5.6 测试复杂任务 → 应路由到大模型
echo "Step 2: 测试复杂任务..."
docker logs mock-anthropic --tail 0 > /dev/null 2>&1

for i in {1..5}; do
  curl -s -X POST "${Sproxy_URL}/v1/messages" \
    -H "Authorization: Bearer ${SMART_TOKEN}" \
    -d '{
      "messages": [{"role": "user", "content": "分析全球气候变化对经济的影响并提出应对策略"}]
    }' > /dev/null
done

ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
if [ "${ANTHROPIC_COUNT}" -lt 3 ]; then
  echo "FAIL: Complex tasks not routed to large model"
  exit 1
fi

echo "✓ Complex tasks routed to large model: ${ANTHROPIC_COUNT}/5"

echo "✓ Test 5 passed"
```

#### 测试 6: 智能路由 Fallback 验证

```bash
#!/bin/bash
# test/e2e/06_smart_routing_fallback.sh

echo "=== Test 6: 智能路由 Fallback 验证 ==="

# 6.1 确保配置 fallback
curl -X PATCH "${Sproxy_URL}/api/admin/group-target-sets/smart-pool" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -d '{"auto_routing": {"small_pool": {"fallback_enabled": true}}}'

# 6.2 停止 Ollama（小模型故障）
docker stop ollama
sleep 35

# 6.3 发送代码理解任务，应 fallback 到大模型
echo "Step 1: 测试小模型故障 fallback..."
docker logs mock-anthropic --tail 0 > /dev/null 2>&1

curl -s -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${SMART_TOKEN}" \
  -d '{
    "messages": [{"role": "user", "content": "解释代码"}]
  }' > /dev/null

ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
if [ "${ANTHROPIC_COUNT}" -ne 1 ]; then
  echo "FAIL: Fallback to large model not working"
  exit 1
fi

echo "✓ Fallback to large model successful"

# 6.4 恢复 Ollama
docker start ollama

echo "✓ Test 6 passed"
```

### 21.5 告警系统测试

#### 测试 7: Target 告警触发与恢复

```bash
#!/bin/bash
# test/e2e/07_alerts.sh

echo "=== Test 7: Target 告警触发与恢复 ==="

# 7.1 订阅 SSE 告警流（后台）
curl -N -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  "${Sproxy_URL}/api/admin/target-alerts/stream" > /tmp/alert_stream.log 2>&1 &
SSE_PID=$!
sleep 2

# 7.2 停止 target 触发告警
docker stop mock-anthropic
sleep 35

# 7.3 验证告警创建
echo "Step 1: 验证告警创建..."
ALERTS=$(curl -s "${Sproxy_URL}/api/admin/target-alerts?status=active" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")

if ! echo "${ALERTS}" | grep -q "mock-anthropic"; then
  echo "FAIL: Alert not created"
  kill $SSE_PID 2>/dev/null
  exit 1
fi

echo "✓ Alert created"

# 7.4 恢复 target
docker start mock-anthropic
sleep 35

# 7.5 验证告警解决
echo "Step 2: 验证告警恢复..."
ALERTS=$(curl -s "${Sproxy_URL}/api/admin/target-alerts?status=active" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")

if echo "${ALERTS}" | grep -q "mock-anthropic"; then
  echo "FAIL: Alert not resolved"
  kill $SSE_PID 2>/dev/null
  exit 1
fi

echo "✓ Alert resolved"

# 7.6 检查 SSE 事件
if ! grep -q "alert_created" /tmp/alert_stream.log; then
  echo "WARN: SSE alert_created event not received"
fi

if ! grep -q "alert_resolved" /tmp/alert_stream.log; then
  echo "WARN: SSE alert_resolved event not received"
fi

kill $SSE_PID 2>/dev/null
echo "✓ Test 7 passed"
```

### 21.6 默认组测试

#### 测试 8: 未分组用户默认组行为

```bash
#!/bin/bash
# test/e2e/08_default_group.sh

echo "=== Test 8: 未分组用户默认组行为 ==="

# 8.1 创建未分组用户
./sproxy admin user add default-user  # 不指定 --group
DEFAULT_TOKEN=$(./sproxy admin token create default-user --ttl 1h)

# 8.2 配置默认组 targets（使用全局配置或显式配置默认组）
# 假设默认组使用所有 active targets

# 8.3 发送请求
echo "Step 1: 发送请求..."
docker logs mock-anthropic --tail 0 > /dev/null 2>&1
docker logs mock-openai --tail 0 > /dev/null 2>&1

for i in {1..10}; do
  curl -s -X POST "${Sproxy_URL}/v1/messages" \
    -H "Authorization: Bearer ${DEFAULT_TOKEN}" \
    -d '{
      "model": "claude-3-haiku-20240307",
      "messages": [{"role": "user", "content": "Hello '$i'"}]
    }' > /dev/null
done

# 8.4 验证请求路由到 targets（默认组应使用全局 targets）
ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
OPENAI_COUNT=$(docker logs mock-openai 2>&1 | grep -c "POST /v1/messages" || echo "0")

TOTAL=$((ANTHROPIC_COUNT + OPENAI_COUNT))
if [ "${TOTAL}" -ne 10 ]; then
  echo "FAIL: Default group routing not working (expected 10, got ${TOTAL})"
  exit 1
fi

echo "✓ Default group routing: Anthropic=${ANTHROPIC_COUNT}, OpenAI=${OPENAI_COUNT}"
echo "✓ Test 8 passed"
```

### 21.7 配额与限流测试

#### 测试 9: 配额耗尽处理

```bash
#!/bin/bash
# test/e2e/09_quota.sh

echo "=== Test 9: 配额耗尽处理 ==="

# 9.1 创建低配额组
./sproxy admin group add quota-test-group \
  --daily-limit 1000  # 非常低的配额

./sproxy admin targetset create quota-pool \
  --group quota-test-group

./sproxy admin targetset add-target quota-pool \
  --url "http://mock-anthropic:8080"

./sproxy admin user add quota-user --group quota-test-group
QUOTA_TOKEN=$(./sproxy admin token create quota-user --ttl 1h)

# 9.2 发送大请求耗尽配额
echo "Step 1: 耗尽配额..."
RESPONSE=$(curl -s -w "%{http_code}" -o /tmp/quota_response.json -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${QUOTA_TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "'$(python3 -c "print('x'*2000)")'"}],
    "max_tokens": 2000
  }')

# 9.3 再次发送请求，应返回配额超限错误
echo "Step 2: 验证配额超限..."
RESPONSE=$(curl -s -w "%{http_code}" -o /tmp/quota_response2.json -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${QUOTA_TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Test"}]
  }')

if [ "${RESPONSE}" != "429" ]; then
  echo "FAIL: Expected 429 quota exceeded, got ${RESPONSE}"
  cat /tmp/quota_response2.json
  exit 1
fi

echo "✓ Quota exceeded handled correctly"

# 9.4 验证告警触发
ALERTS=$(curl -s "${Sproxy_URL}/api/admin/target-alerts?severity=warning" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}")

if ! echo "${ALERTS}" | grep -q "quota"; then
  echo "WARN: Quota alert not triggered"
fi

echo "✓ Test 9 passed"
```

### 21.8 向后兼容性测试

#### 测试 10: 旧版 LLMBinding 兼容

```bash
#!/bin/bash
# test/e2e/10_backward_compat.sh

echo "=== Test 10: 向后兼容性测试 ==="

# 10.1 创建用户并显式绑定单个 target（旧版方式）
./sproxy admin user add legacy-user
./sproxy admin llm bind --user legacy-user --target "http://mock-anthropic:8080"

LEGACY_TOKEN=$(./sproxy admin token create legacy-user --ttl 1h)

# 10.2 发送请求
echo "Step 1: 发送请求..."
docker logs mock-anthropic --tail 0 > /dev/null 2>&1

curl -s -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${LEGACY_TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Legacy binding test"}]
  }' > /dev/null

# 10.3 验证请求路由到绑定的 target
ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
if [ "${ANTHROPIC_COUNT}" -ne 1 ]; then
  echo "FAIL: Legacy binding not working"
  exit 1
fi

echo "✓ Legacy LLMBinding works"

# 10.4 验证 Group Target Set 优先级低于 User Binding
# （绑定用户到组，但用户的单 target 绑定应优先）
./sproxy admin group add legacy-group
./sproxy admin targetset create legacy-pool --group legacy-group
./sproxy admin targetset add-target legacy-pool --url "http://mock-openai:8081"
./sproxy admin user update legacy-user --group legacy-group

# 再次发送请求，仍应路由到 anthropic（用户级绑定优先）
docker logs mock-anthropic --tail 0 > /dev/null 2>&1
docker logs mock-openai --tail 0 > /dev/null 2>&1

curl -s -X POST "${Sproxy_URL}/v1/messages" \
  -H "Authorization: Bearer ${LEGACY_TOKEN}" \
  -d '{
    "model": "claude-3-haiku-20240307",
    "messages": [{"role": "user", "content": "Priority test"}]
  }' > /dev/null

ANTHROPIC_COUNT=$(docker logs mock-anthropic 2>&1 | grep -c "POST /v1/messages" || echo "0")
OPENAI_COUNT=$(docker logs mock-openai 2>&1 | grep -c "POST /v1/messages" || echo "0")

if [ "${ANTHROPIC_COUNT}" -ne 1 ] || [ "${OPENAI_COUNT}" -ne 0 ]; then
  echo "FAIL: User binding priority not respected"
  exit 1
fi

echo "✓ User binding priority respected over Group"
echo "✓ Test 10 passed"
```

### 21.9 运行所有 E2E 测试

```bash
#!/bin/bash
# test/e2e/run_all_tests.sh

set -e

TESTS=(
  "01_group_target_set.sh"
  "02_load_balancing.sh"
  "03_failover.sh"
  "04_all_targets_fail.sh"
  "05_smart_routing.sh"
  "06_smart_routing_fallback.sh"
  "07_alerts.sh"
  "08_default_group.sh"
  "09_quota.sh"
  "10_backward_compat.sh"
)

PASSED=0
FAILED=0

echo "========================================"
echo "Running E2E Test Suite"
echo "========================================"

for test in "${TESTS[@]}"; do
  echo ""
  echo "----------------------------------------"
  echo "Running: ${test}"
  echo "----------------------------------------"
  
  if bash "${test}"; then
    ((PASSED++))
    echo "✓ ${test} PASSED"
  else
    ((FAILED++))
    echo "✗ ${test} FAILED"
  fi
done

echo ""
echo "========================================"
echo "E2E Test Results"
echo "========================================"
echo "Passed: ${PASSED}"
echo "Failed: ${FAILED}"
echo "Total:  ${#TESTS[@]}"

if [ ${FAILED} -eq 0 ]; then
  echo ""
  echo "🎉 All tests passed!"
  exit 0
else
  echo ""
  echo "⚠️  Some tests failed"
  exit 1
fi
```

### 21.10 CI/CD 集成

```yaml
# .github/workflows/e2e.yml
name: E2E Tests

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main ]

jobs:
  e2e:
    runs-on: ubuntu-latest
    
    steps:
    - uses: actions/checkout@v3
    
    - name: Set up Go
      uses: actions/setup-go@v3
      with:
        go-version: 1.24
    
    - name: Build sproxy
      run: make build
    
    - name: Set up test environment
      run: |
        cd test/e2e
        docker-compose up -d
        ./scripts/wait-for-services.sh
    
    - name: Run E2E tests
      run: |
        cd test/e2e
        ./run_all_tests.sh
    
    - name: Collect logs on failure
      if: failure()
      run: |
        docker-compose logs > test/e2e/logs.txt
    
    - name: Cleanup
      if: always()
      run: |
        cd test/e2e
        docker-compose down -v
```

---

**End of Design Document**
