# Group-Target Set 设计文档 Review 报告

**日期**: 2026-03-25
**审查者**: Claude Code
**状态**: 待改进

---

## 1. 发现的问题和不足

### 1.1 架构设计问题

#### 问题 1: 默认组的模型设计不清晰
**现状**: 文档中对"默认组"的定义有歧义
- `groups` 表中 `is_default=1` 表示默认组
- `group_target_sets` 表中 `group_id=NULL, is_default=1` 也表示默认组
- 两个表的默认组定义不一致

**改进方案**:
- 明确定义：`groups` 表中的 `is_default` 字段用于标记组本身是否为默认组
- `group_target_sets` 表中的 `is_default` 字段用于标记该 target set 是否为默认 set
- 建立清晰的映射关系：默认组 → 默认 target set

#### 问题 2: 缺少并发控制和事务处理
**现状**:
- `GroupTargetSetRepo` 的 CRUD 操作没有事务支持
- 添加/删除 member 时可能出现不一致状态
- 告警管理器的内存状态没有并发保护细节

**改进方案**:
- 所有涉及多表操作的方法需要事务支持
- 告警管理器使用 `sync.RWMutex` 保护内存状态
- 提供原子操作（如 AddMemberWithValidation）

#### 问题 3: 缺少健康检查和故障转移的详细设计
**现状**:
- 文档提到 `TargetHealthMonitor` 但没有详细设计
- 健康检查的触发条件不清晰
- 故障转移的重试策略不完整

**改进方案**:
- 补充 `TargetHealthMonitor` 的详细设计
- 定义健康检查的间隔、超时、失败阈值
- 明确重试策略：`try_next` vs `fail_fast`

#### 问题 4: 缺少告警聚合和去重机制
**现状**:
- 同一 target 的重复错误会产生多条告警
- 没有告警聚合机制
- 没有告警去重逻辑

**改进方案**:
- 实现告警窗口机制：在 5 分钟内同一 target 的相同错误只产生一条告警
- 实现告警聚合：多个错误合并为一条告警
- 实现告警去重：相同的告警不重复创建

### 1.2 数据模型问题

#### 问题 5: 缺少 target 健康状态字段
**现状**: `group_target_set_members` 表没有健康状态字段

**改进方案**:
```sql
ALTER TABLE group_target_set_members ADD COLUMN health_status TEXT DEFAULT 'unknown';
-- 值: 'healthy', 'degraded', 'unhealthy', 'unknown'
ALTER TABLE group_target_set_members ADD COLUMN last_health_check DATETIME;
ALTER TABLE group_target_set_members ADD COLUMN consecutive_failures INTEGER DEFAULT 0;
```

#### 问题 6: 缺少告警去重和聚合的字段
**现状**: `target_alerts` 表没有支持去重和聚合的字段

**改进方案**:
```sql
ALTER TABLE target_alerts ADD COLUMN alert_key TEXT;  -- 用于去重
ALTER TABLE target_alerts ADD COLUMN occurrence_count INTEGER DEFAULT 1;  -- 聚合计数
ALTER TABLE target_alerts ADD COLUMN last_occurrence DATETIME;  -- 最后发生时间
CREATE INDEX idx_target_alerts_alert_key ON target_alerts(alert_key, resolved_at);
```

#### 问题 7: 缺少审计日志
**现状**: Group Target Set 的修改没有审计日志

**改进方案**:
```sql
CREATE TABLE group_target_set_audit_logs (
    id TEXT PRIMARY KEY,
    target_set_id TEXT NOT NULL,
    action TEXT NOT NULL,  -- 'create', 'update', 'delete', 'add_member', 'remove_member'
    changed_by TEXT,
    old_value TEXT,  -- JSON
    new_value TEXT,  -- JSON
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_set_id) REFERENCES group_target_sets(id)
);
```

### 1.3 API 设计问题

#### 问题 8: 缺少批量操作 API
**现状**: 只有单个 target 的添加/删除 API

**改进方案**:
```http
# 批量添加 targets
POST /api/admin/group-target-sets/{id}/targets/batch
Body: {
  "targets": [
    {"url": "...", "weight": 1, "priority": 1},
    {"url": "...", "weight": 2, "priority": 2}
  ]
}

# 批量删除 targets
DELETE /api/admin/group-target-sets/{id}/targets/batch
Body: {
  "urls": ["...", "..."]
}
```

#### 问题 9: 缺少告警规则管理 API
**现状**: 告警规则只能通过配置文件配置

**改进方案**:
```http
# 获取告警规则
GET /api/admin/target-alerts/rules

# 创建/更新告警规则
POST /api/admin/target-alerts/rules
Body: {
  "type": "http_error",
  "status_codes": [500, 502, 503],
  "severity": "error",
  "min_occurrences": 3,
  "window": "5m"
}

# 删除告警规则
DELETE /api/admin/target-alerts/rules/{rule_id}
```

#### 问题 10: 缺少权限控制
**现状**: API 没有明确的权限控制设计

**改进方案**:
- 所有 admin API 需要 `admin` 角色
- 支持细粒度权限：`group_target_set:read`, `group_target_set:write`, `alert:read`, `alert:write`
- 在 API 文档中明确标注权限要求

### 1.4 测试覆盖问题

#### 问题 11: 缺少边界条件测试
**现状**: 测试场景不完整

**改进方案**:
- 添加边界条件测试：0 个 targets、1 个 target、大量 targets
- 添加并发测试：多个请求同时修改 target set
- 添加性能测试：大规模 target set 的选择性能

#### 问题 12: 缺少故障场景测试
**现状**: 没有测试数据库故障、网络故障等场景

**改进方案**:
- 添加数据库连接失败的测试
- 添加 target 不存在的测试
- 添加告警管理器崩溃恢复的测试

#### 问题 13: 缺少集成测试
**现状**: 没有端到端的集成测试

**改进方案**:
- 添加完整的请求流程测试：创建 group → 创建 target set → 添加 targets → 路由请求
- 添加告警流程的集成测试：错误发生 → 告警创建 → SSE 推送 → 告警解决

### 1.5 配置和部署问题

#### 问题 14: 缺少配置验证
**现状**: 配置文件没有验证机制

**改进方案**:
- 在启动时验证配置文件
- 检查 group_name 是否存在
- 检查 target_url 是否有效
- 检查权重和优先级的范围

#### 问题 15: 缺少灰度发布策略
**现状**: 没有灰度发布的设计

**改进方案**:
- 支持功能开关：`enable_group_target_sets`, `enable_target_alerts`
- 支持灰度比例：逐步将流量从旧路由切换到新路由
- 支持回滚：快速回滚到旧路由

### 1.6 监控和可观测性问题

#### 问题 16: 缺少指标定义
**现状**: 没有定义关键指标

**改进方案**:
- 添加指标：`group_target_selector_latency`, `group_target_selector_errors`
- 添加指标：`target_alert_created`, `target_alert_resolved`
- 添加指标：`target_health_check_latency`, `target_health_check_failures`

#### 问题 17: 缺少日志设计
**现状**: 日志输出不规范

**改进方案**:
- 定义日志级别：DEBUG（选择过程）、INFO（重要事件）、WARN（异常情况）、ERROR（错误）
- 定义日志格式：包含 target_url、group_id、user_id 等上下文
- 定义日志采样：高频日志需要采样

---

## 2. 补充的细节

### 2.1 详细的故障转移流程

```
请求进入
  ↓
获取 user 的 group_id
  ↓
pickLLMTarget(groupID, tried=[])
  ├─ 检查用户级 LLMBinding（优先级最高）
  │   ├─ 存在 → 返回绑定的 target
  │   └─ 不存在 → 继续
  │
  ├─ 检查 Group 的 Target Set
  │   ├─ 存在 → SelectTarget(groupID, tried=[])
  │   │   ├─ 获取 target set 的所有 members
  │   │   ├─ 过滤不健康的 targets
  │   │   ├─ 过滤已尝试的 targets（tried）
  │   │   ├─ 按策略选择（weighted_random / round_robin）
  │   │   └─ 返回选中的 target
  │   └─ 不存在 → 继续
  │
  ├─ 使用默认 Target Set（第二类）
  │   └─ SelectTarget("", tried=[])
  │
  └─ 回退到全局负载均衡
      └─ 使用原有逻辑

发送请求到选中的 target
  ↓
[成功] → 记录成功，返回响应
  ↓
[失败] → 记录错误到告警管理器
  ↓
是否还有可用 targets？
  ├─ 是 → tried.append(target_url)，重新 pickLLMTarget(groupID, tried)
  └─ 否 → 返回错误
```

### 2.2 告警生命周期

```
Target 返回错误
  ↓
AlertManager.RecordError(targetURL, statusCode, err, affectedGroups)
  ├─ 检查是否已有活跃告警
  │   ├─ 有 → 更新计数器
  │   └─ 无 → 创建新告警
  │
  ├─ 检查是否达到触发阈值
  │   ├─ 是 → 创建 TargetAlert 记录
  │   │   ├─ 保存到数据库
  │   │   ├─ 推送 SSE 事件 (alert_created)
  │   │   └─ 更新内存状态
  │   └─ 否 → 继续监控
  │
  └─ 启动恢复检测
      └─ 监控连续成功次数

Target 恢复正常
  ↓
AlertManager.RecordSuccess(targetURL)
  ├─ 增加连续成功计数
  ├─ 检查是否达到恢复阈值
  │   ├─ 是 → 标记告警为已解决
  │   │   ├─ 更新数据库 (resolved_at)
  │   │   ├─ 推送 SSE 事件 (alert_resolved)
  │   │   └─ 更新内存状态
  │   └─ 否 → 继续监控
  │
  └─ 重置错误计数器
```

### 2.3 健康检查设计

```go
type HealthCheckConfig struct {
    Interval      time.Duration  // 检查间隔，默认 30s
    Timeout       time.Duration  // 检查超时，默认 5s
    FailureThreshold int         // 失败阈值，默认 3 次
    SuccessThreshold int         // 恢复阈值，默认 2 次
    Path          string         // 健康检查路径，默认 /health
}

// 健康检查流程
1. 定期（每 30s）对每个 target 发送 GET /health
2. 如果连续失败 3 次 → 标记为 unhealthy
3. 如果连续成功 2 次 → 标记为 healthy
4. 记录检查结果到 group_target_set_members.last_health_check
```

### 2.4 权重和优先级的使用

```
权重 (weight): 用于加权随机选择
- 权重越高，被选中的概率越高
- 示例：weight=[2, 1, 1] → 选中概率为 [50%, 25%, 25%]

优先级 (priority): 用于优先级选择
- 优先级越高，越优先被选中
- 示例：priority=[1, 2, 3] → 先选 priority=1，再选 priority=2，最后选 priority=3
- 仅在 strategy='priority' 时使用

策略 (strategy):
- 'weighted_random': 按权重随机选择
- 'round_robin': 轮询选择
- 'priority': 按优先级选择（新增）
```

---

## 3. 覆盖的测试场景

### 3.1 单元测试场景

#### GroupTargetSetRepo 测试
- [ ] Create: 创建 target set，验证 ID、name、group_id 等字段
- [ ] GetByID: 获取存在/不存在的 target set
- [ ] GetByName: 获取存在/不存在的 target set
- [ ] GetByGroupID: 获取 group 的 target set
- [ ] GetDefault: 获取默认 target set
- [ ] Update: 更新 target set 的字段
- [ ] Delete: 删除 target set，验证级联删除
- [ ] ListAll: 列出所有 target sets
- [ ] AddMember: 添加 member，验证唯一性约束
- [ ] RemoveMember: 删除 member
- [ ] UpdateMember: 更新 member 的权重和优先级
- [ ] ListMembers: 列出 target set 的所有 members
- [ ] GetAvailableTargetsForGroup: 获取 group 的可用 targets

#### GroupTargetSelector 测试
- [ ] SelectTarget: 多个 healthy targets → 按策略选择
- [ ] SelectTarget: 部分 unhealthy targets → 过滤后选择
- [ ] SelectTarget: 所有 unhealthy targets → 返回错误
- [ ] SelectTarget: 无 target set → 使用默认
- [ ] SelectTarget: tried 过滤 → 不选择已尝试的 target
- [ ] SelectTarget: 权重分布 → 验证选择概率
- [ ] SelectTarget: 并发调用 → 验证线程安全

#### TargetAlertManager 测试
- [ ] RecordError: 创建新告警
- [ ] RecordError: 更新现有告警的计数
- [ ] RecordError: 达到触发阈值 → 创建 TargetAlert
- [ ] RecordSuccess: 增加连续成功计数
- [ ] RecordSuccess: 达到恢复阈值 → 标记告警为已解决
- [ ] GetActiveAlerts: 返回所有活跃告警
- [ ] SubscribeEvents: 订阅告警事件

#### TargetAlertRepo 测试
- [ ] Create: 创建告警
- [ ] ListActive: 列出活跃告警，支持过滤
- [ ] ListHistory: 列出历史告警，支持分页
- [ ] Resolve: 标记告警为已解决
- [ ] GetStats: 获取统计信息
- [ ] Cleanup: 清理旧数据

### 3.2 集成测试场景

#### 完整的请求流程
- [ ] 创建 group
- [ ] 创建 target set
- [ ] 添加 targets
- [ ] 发送请求 → 路由到 target set 中的 target
- [ ] 验证请求成功

#### 故障转移流程
- [ ] 第一个 target 失败 → 自动选择第二个
- [ ] 所有 targets 失败 → 返回错误
- [ ] 部分 targets 不健康 → 只选择健康的

#### 告警流程
- [ ] Target 返回错误 → 告警创建
- [ ] Dashboard SSE 收到 alert_created 事件
- [ ] Target 恢复 → 告警解决
- [ ] Dashboard SSE 收到 alert_resolved 事件

#### 并发测试
- [ ] 多个请求同时修改 target set
- [ ] 多个请求同时选择 target
- [ ] 多个请求同时记录错误

### 3.3 边界条件测试

- [ ] 0 个 targets → 返回错误
- [ ] 1 个 target → 总是选择该 target
- [ ] 大量 targets（1000+） → 验证性能
- [ ] 权重为 0 → 不选择
- [ ] 权重为负数 → 验证错误处理
- [ ] 优先级重复 → 验证处理方式
- [ ] target_url 为空 → 验证错误处理
- [ ] group_id 为空 → 使用默认 target set

### 3.4 故障场景测试

- [ ] 数据库连接失败 → 返回错误
- [ ] target 不存在 → 验证外键约束
- [ ] group 不存在 → 验证外键约束
- [ ] 告警管理器崩溃 → 恢复后继续工作
- [ ] SSE 连接断开 → 客户端重连
- [ ] 配置文件无效 → 启动失败并提示错误

---

## 4. 改进建议总结

| 优先级 | 类别 | 问题 | 改进方案 |
|--------|------|------|---------|
| P0 | 架构 | 默认组模型不清晰 | 明确定义默认组的映射关系 |
| P0 | 数据模型 | 缺少 target 健康状态 | 添加 health_status、last_health_check 字段 |
| P0 | 测试 | 缺少集成测试 | 添加端到端的集成测试 |
| P1 | 架构 | 缺少事务处理 | 为多表操作添加事务支持 |
| P1 | 架构 | 缺少健康检查设计 | 补充 TargetHealthMonitor 的详细设计 |
| P1 | 数据模型 | 缺少告警去重字段 | 添加 alert_key、occurrence_count 字段 |
| P1 | API | 缺少批量操作 | 添加批量添加/删除 targets API |
| P1 | 监控 | 缺少指标定义 | 定义关键指标 |
| P2 | API | 缺少权限控制 | 添加细粒度权限控制 |
| P2 | 配置 | 缺少配置验证 | 在启动时验证配置文件 |
| P2 | 部署 | 缺少灰度发布 | 支持功能开关和灰度比例 |
| P2 | 测试 | 缺少性能测试 | 添加基准测试 |

---

## 5. 下一步行动

1. **立即修改设计文档**（P0）
   - 明确默认组的模型
   - 补充 TargetHealthMonitor 的详细设计
   - 补充数据模型的新字段

2. **启动编码实施**（P0）
   - 实现数据库表和迁移
   - 实现 GroupTargetSetRepo
   - 实现 GroupTargetSelector
   - 实现 TargetAlertManager

3. **补充 API 和测试**（P1）
   - 实现 Admin API
   - 实现 SSE 告警流
   - 编写集成测试

4. **优化和部署**（P2）
   - 添加权限控制
   - 添加灰度发布支持
   - 性能优化和基准测试
