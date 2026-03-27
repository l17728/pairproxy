# Group-Target Set Pooling & Alerting 实施指南

**版本**: 1.0
**日期**: 2026-03-27
**状态**: 完成实施

---

## 目录

1. [概述](#概述)
2. [架构设计](#架构设计)
3. [核心组件](#核心组件)
4. [配置指南](#配置指南)
5. [API 文档](#api-文档)
6. [Dashboard 使用](#dashboard-使用)
7. [故障排查](#故障排查)
8. [性能优化](#性能优化)

---

## 概述

Group-Target Set Pooling & Alerting 功能为 PairProxy Gateway 提供了以下能力：

- **Group-Target Set 绑定**: 为每个 Group 配置一组 LLM targets，支持组内自动负载均衡和故障转移
- **智能路由**: 基于加权随机、轮询或优先级策略选择 targets
- **实时告警**: 当 target 返回错误时，在 Dashboard 上实时显示告警信息
- **健康监控**: 后台定期检查 targets 的健康状态，自动标记不健康的 targets
- **SSE 实时更新**: 通过 Server-Sent Events 推送实时告警更新到 Dashboard

---

## 架构设计

### 系统架构

```
请求进入
  ↓
AuthMiddleware (获取 userID, groupID)
  ↓
QuotaMiddleware (检查配额)
  ↓
pickLLMTarget 逻辑
  ├─ 如果 user 有绑定 → 使用绑定的单个 target
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

[后台] TargetHealthMonitor
  ├─ 定期检查各 target 健康状态
  ├─ 记录 target 错误事件
  └─ 推送告警到 AlertManager

[后台] TargetAlertManager
  ├─ 接收 target 错误/恢复事件
  ├─ 维护告警列表（内存 + 持久化）
  └─ SSE 推送实时更新到 WebUI
```

### 数据模型

#### group_target_sets 表

```sql
CREATE TABLE group_target_sets (
    id              TEXT PRIMARY KEY,           -- UUID
    group_id        TEXT,                       -- 关联 groups.id（NULL=默认组）
    name            TEXT NOT NULL,              -- 显示名称
    strategy        TEXT NOT NULL DEFAULT 'weighted_random',
    retry_policy    TEXT NOT NULL DEFAULT 'try_next',
    is_default      INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE
);
```

#### group_target_set_members 表

```sql
CREATE TABLE group_target_set_members (
    id                  TEXT PRIMARY KEY,
    target_set_id       TEXT NOT NULL,
    target_url          TEXT NOT NULL,
    weight              INTEGER NOT NULL DEFAULT 1,
    priority            INTEGER NOT NULL DEFAULT 0,
    is_active           INTEGER NOT NULL DEFAULT 1,
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,

    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id) ON DELETE CASCADE,
    FOREIGN KEY (target_url) REFERENCES llm_targets(url) ON DELETE CASCADE,
    UNIQUE(target_set_id, target_url)
);
```

#### target_alerts 表

```sql
CREATE TABLE target_alerts (
    id              TEXT PRIMARY KEY,
    target_url      TEXT NOT NULL,
    alert_type      TEXT NOT NULL,              -- error, degraded, recovered
    severity        TEXT NOT NULL,              -- warning, error, critical
    status_code     INTEGER,
    error_message   TEXT,
    affected_groups TEXT,                       -- JSON array
    occurrence_count INTEGER DEFAULT 1,
    first_occurrence DATETIME,
    last_occurrence DATETIME,
    resolved_at     DATETIME,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

---

## 核心组件

### 1. GroupTargetSetRepo (数据库操作层)

**位置**: `internal/db/group_target_set_repo.go`

主要方法：
- `Create(set *GroupTargetSet)` - 创建 target set
- `GetByID(id string)` - 获取 target set
- `ListAll()` - 列出所有 target sets
- `ListByGroupID(groupID string)` - 获取特定 group 的 target sets
- `ListMembers(setID string)` - 获取 target set 的成员
- `AddMember(setID string, member *GroupTargetSetMember)` - 添加成员
- `RemoveMember(setID string, targetURL string)` - 移除成员
- `UpdateTargetHealth(targetURL string, healthy bool)` - 更新 target 健康状态

### 2. GroupTargetSelector (选择器)

**位置**: `internal/proxy/group_target_selector.go`

支持三种选择策略：
- **weighted_random**: 加权随机选择（默认）
- **round_robin**: 轮询选择
- **priority**: 优先级选择

```go
selector := proxy.NewGroupTargetSelector(logger)
target, err := selector.SelectTarget(
    groupID,
    strategy,
    availableTargets,
    triedTargets,
)
```

### 3. TargetAlertManager (告警管理)

**位置**: `internal/alert/target_alert_manager.go`

功能：
- 记录 target 错误
- 检测告警触发条件
- 推送告警事件
- 支持事件订阅

```go
alertManager := alert.NewTargetAlertManager(repo, config, logger)
alertManager.Start(ctx)

// 记录错误
alertManager.RecordError(targetURL, statusCode, errorMsg)

// 订阅事件
eventCh := alertManager.SubscribeEvents()
```

### 4. TargetHealthMonitor (健康监控)

**位置**: `internal/alert/target_health_monitor.go`

功能：
- 后台定期检查 targets 健康状态
- 检测 target 故障和恢复
- 推送健康状态变化事件

```go
monitor := alert.NewTargetHealthMonitor(repo, alertManager, config, logger)
monitor.Start(ctx)
```

### 5. SSE Alert Handler (实时流)

**位置**: `internal/api/admin_targetset_handler.go`

提供 SSE 端点用于实时告警推送：

```
GET /api/admin/alerts/stream
```

---

## 配置指南

### YAML 配置示例

```yaml
group_target_sets:
  - name: "production-pool"
    group_name: "engineering"
    strategy: "weighted_random"
    retry_policy: "try_next"
    targets:
      - url: "https://api.anthropic.com"
        weight: 2
        priority: 0
      - url: "https://api-backup.anthropic.com"
        weight: 1
        priority: 1

  - name: "default-pool"
    is_default: true
    strategy: "weighted_random"
    targets:
      - url: "https://api.anthropic.com"
        weight: 1
        priority: 0

target_alerts:
  enabled: true
  triggers:
    http_error:
      type: "http_error"
      status_codes: [429, 500, 502, 503, 504]
      severity: "error"
      min_occurrences: 3
      window: 5m
  recovery:
    consecutive_successes: 2
    window: 5m
  dashboard:
    max_active_alerts: 100
    retention: 168h
    auto_refresh: true

health_check:
  interval: 30s
  timeout: 5s
  failure_threshold: 3
  success_threshold: 2
  path: "/health"
```

### 环境变量

```bash
# 告警配置
ALERT_ENABLED=true
ALERT_ERROR_THRESHOLD=3
ALERT_RECOVERY_THRESHOLD=2

# 健康检查配置
HEALTH_CHECK_INTERVAL=30s
HEALTH_CHECK_TIMEOUT=5s
HEALTH_CHECK_FAILURE_THRESHOLD=3
```

---

## API 文档

### Group-Target Set 管理

#### 创建 Target Set

```
POST /api/admin/targetsets
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "name": "production-pool",
  "group_id": "engineering",
  "strategy": "weighted_random",
  "retry_policy": "try_next",
  "targets": [
    {
      "url": "https://api.anthropic.com",
      "weight": 2,
      "priority": 0
    }
  ]
}
```

#### 列出 Target Sets

```
GET /api/admin/targetsets
Authorization: Bearer <admin-token>
```

### 告警管理

#### 获取活跃告警

```
GET /api/admin/alerts/active
Authorization: Bearer <admin-token>
```

#### 获取告警历史

```
GET /api/admin/alerts/history?limit=100&since=<RFC3339>
Authorization: Bearer <admin-token>
```

#### 解决告警

```
POST /api/admin/alerts/resolve
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "alert_id": "<alert-id>",
  "reason": "resolved"
}
```

#### 获取告警统计

```
GET /api/admin/alerts/stats
Authorization: Bearer <admin-token>
```

### SSE 实时流

#### 连接告警流

```
GET /api/admin/alerts/stream
Authorization: Bearer <admin-token>
```

事件格式：
```
event: connected
data: {"message": "connected to alert stream"}

event: alert_update
data: {"type": "alert_created", "alert": {...}, "timestamp": "2026-03-27T..."}
```

---

## Dashboard 使用

### 告警页面

访问 `/dashboard/alerts` 查看系统告警和错误日志。

**功能**:
- 实时告警流（通过 SSE）
- 告警过滤（全部/错误/警告）
- 手动刷新
- 告警详情查看

**实时指示**:
- 绿色脉冲: 连接正常
- 红色: 连接失败

### 告警详情

每条告警包含：
- **时间**: 告警发生时间
- **级别**: 错误/警告
- **来源**: Target URL
- **消息**: 错误信息
- **字段**: 额外的上下文信息

---

## 故障排查

### 常见问题

#### 1. Target 显示为不健康

**症状**: Target 在 Dashboard 上显示为不健康

**排查步骤**:
1. 检查 target 的 `/health` 端点是否可访问
2. 查看健康检查日志: `grep "health check failed" logs/sproxy.log`
3. 验证网络连接: `curl -v https://<target-url>/health`

#### 2. 告警未推送到 Dashboard

**症状**: 发生错误但 Dashboard 上没有告警

**排查步骤**:
1. 检查 SSE 连接: 打开浏览器开发者工具，查看 Network 标签
2. 验证告警管理器是否启用: `grep "alert manager" logs/sproxy.log`
3. 检查数据库连接: `sqlite3 sproxy.db "SELECT COUNT(*) FROM target_alerts;"`

#### 3. 故障转移不工作

**症状**: 当 target 失败时，请求没有转移到其他 target

**排查步骤**:
1. 验证 Group-Target Set 配置是否正确
2. 检查 retry_policy 设置: 应该是 "try_next"
3. 查看路由日志: `grep "pickLLMTarget" logs/sproxy.log`

### 日志位置

- **主日志**: `logs/sproxy.log`
- **告警日志**: `logs/sproxy.log` (搜索 "alert")
- **健康检查日志**: `logs/sproxy.log` (搜索 "health")

---

## 性能优化

### 1. 健康检查优化

```yaml
health_check:
  interval: 60s          # 增加检查间隔以减少负载
  timeout: 10s           # 增加超时时间
  failure_threshold: 5   # 增加失败阈值以减少误报
```

### 2. 告警聚合

```yaml
target_alerts:
  dashboard:
    max_active_alerts: 50  # 限制活跃告警数量
    retention: 72h         # 减少保留时间
```

### 3. 数据库优化

```sql
-- 创建索引以加快查询
CREATE INDEX idx_target_alerts_target_url ON target_alerts(target_url);
CREATE INDEX idx_target_alerts_created_at ON target_alerts(created_at);
CREATE INDEX idx_group_target_sets_group_id ON group_target_sets(group_id);
```

### 4. 缓存策略

- Target Set 配置在内存中缓存
- 健康状态在内存中维护
- 告警事件通过通道广播，避免数据库查询

---

## 监控指标

### 关键指标

- **活跃告警数**: 当前系统中的活跃告警数量
- **告警解决率**: 已解决告警 / 总告警数
- **Target 健康率**: 健康 targets / 总 targets 数
- **故障转移成功率**: 成功转移 / 总转移次数

### 告警阈值建议

- **高**: 活跃告警 > 10
- **中**: 活跃告警 > 5
- **低**: 活跃告警 > 0

---

## 总结

Group-Target Set Pooling & Alerting 功能提供了完整的 target 管理、故障转移和告警系统。通过合理配置和监控，可以显著提高系统的可靠性和可观测性。

更多信息请参考：
- 设计文档: `docs/superpowers/specs/2026-03-25-group-target-set-pooling-alerting.md`
- 设计审查: `docs/superpowers/DESIGN_REVIEW.md`
