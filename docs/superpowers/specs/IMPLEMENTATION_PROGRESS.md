# Group-Target Set 实施进度报告

**日期**: 2026-03-25
**状态**: 进行中 - 核心模块实施阶段

---

## 已完成的工作

### 1. 设计文档审查 ✅
- 完整阅读了设计文档
- 创建了详细的 DESIGN_REVIEW.md，包含：
  - 17 个发现的问题和不足
  - 详细的改进方案
  - 补充的细节设计
  - 完整的测试场景覆盖
  - 改进建议优先级表

### 2. 数据库模型实现 ✅
- 添加了 3 个新表的模型定义：
  - `GroupTargetSet` - Group 与 Target Set 的绑定关系
  - `GroupTargetSetMember` - Target Set 的成员（多对多关系）
  - `TargetAlert` - Target 告警事件
- 扩展了 `Group` 表的字段（虽然在模型中还未显式添加，但在 repo 中已支持）
- 更新了 db.go 的 Migrate 函数以包含新表

### 3. 数据库操作层实现 ✅
- **GroupTargetSetRepo** (group_target_set_repo.go)
  - ✅ Create - 创建 target set
  - ✅ GetByID - 根据 ID 获取
  - ✅ GetByName - 根据名称获取
  - ✅ GetByGroupID - 根据 group ID 获取
  - ✅ GetDefault - 获取默认 target set
  - ✅ Update - 更新 target set
  - ✅ Delete - 删除 target set
  - ✅ ListAll - 列出所有 target sets
  - ✅ AddMember - 添加 member
  - ✅ RemoveMember - 删除 member
  - ✅ UpdateMember - 更新 member 权重和优先级
  - ✅ ListMembers - 列出 members
  - ✅ GetAvailableTargetsForGroup - 获取 group 的可用 targets

- **TargetAlertRepo** (target_alert_repo.go)
  - ✅ Create - 创建告警
  - ✅ ListActive - 查询活跃告警
  - ✅ ListHistory - 查询历史告警
  - ✅ Resolve - 标记告警为已解决
  - ✅ GetStats - 获取统计信息
  - ✅ Cleanup - 清理旧数据
  - ✅ GetByID - 根据 ID 获取告警
  - ✅ GetOrCreateAlert - 获取或创建告警（支持去重）

### 4. 核心选择器实现 ✅
- **GroupTargetSelector** (group_target_selector.go)
  - ✅ WeightedRandomStrategy - 加权随机选择
  - ✅ RoundRobinStrategy - 轮询选择
  - ✅ PriorityStrategy - 优先级选择
  - ✅ SelectTarget - 为 Group 选择 target
  - ✅ UpdateTargetHealth - 更新 target 健康状态
  - 支持已尝试过滤、健康检查过滤、并发安全

---

## 待完成的工作

### 5. TargetAlertManager 告警管理器 (任务 #6)
需要实现：
- 错误记录和恢复检测
- 告警聚合和去重
- 内存状态管理
- 事件推送机制
- 后台 goroutine 管理

### 6. TargetHealthMonitor 健康检查 (任务 #7)
需要实现：
- 定期健康检查
- 失败阈值检测
- 恢复阈值检测
- 检查结果记录
- 告警事件推送

### 7. Admin API 端点 (任务 #8)
需要实现：
- Group Target Set 管理 API
- Target Alert 管理 API
- SSE 告警流端点
- 权限控制
- 请求验证

### 8. 修改现有代码 (任务 #11, #12)
需要修改：
- pickLLMTarget 路由逻辑
- RetryTransport 集成告警

### 9. Admin CLI 命令 (任务 #13)
需要实现：
- targetset 子命令
- alert 子命令

### 10. Dashboard 页面 (任务 #14)
需要实现：
- 告警页面
- Group/Target Set 管理页面
- SSE 实时更新

### 11. 配置加载 (任务 #15)
需要实现：
- 配置文件加载
- 配置验证
- 默认值设置

### 12. 测试 (任务 #9, #10, #17)
需要编写：
- 单元测试
- 集成测试
- 端到端测试

### 13. 文档 (任务 #18)
需要编写：
- 实施文档
- API 文档
- 配置指南

---

## 关键设计决策

### 1. 默认组模型
- `groups` 表中 `is_default=1` 标记默认组
- `group_target_sets` 表中 `group_id=NULL, is_default=1` 标记默认 target set
- 未分组用户自动使用默认 target set

### 2. 告警去重和聚合
- 使用 `alert_key` 字段进行去重
- 使用 `occurrence_count` 字段进行聚合
- 使用 `last_occurrence` 字段记录最后发生时间

### 3. 健康状态管理
- 在 `GroupTargetSetMember` 中添加 `health_status` 字段
- 支持 4 种状态：healthy、degraded、unhealthy、unknown
- 记录 `last_health_check` 和 `consecutive_failures`

### 4. 选择策略
- 支持 3 种策略：weighted_random、round_robin、priority
- 策略缓存以提高性能
- 支持已尝试过滤和健康检查过滤

---

## 代码质量指标

- **代码行数**: ~1000 行（3 个 repo + 1 个 selector）
- **测试覆盖**: 待编写（计划 >80%）
- **文档**: 已完成设计审查，待编写实施文档
- **错误处理**: 完整的错误处理和日志记录
- **并发安全**: 使用 sync.RWMutex 保护共享状态

---

## 下一步行动

1. **立即完成** (今天)
   - [ ] 实现 TargetAlertManager
   - [ ] 实现 TargetHealthMonitor
   - [ ] 编写单元测试

2. **本周完成**
   - [ ] 实现 Admin API 端点
   - [ ] 修改现有代码集成
   - [ ] 编写集成测试

3. **下周完成**
   - [ ] 实现 Admin CLI 命令
   - [ ] 实现 Dashboard 页面
   - [ ] 编写端到端测试
   - [ ] 编写文档

---

## 风险和缓解措施

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| 并发问题 | 数据不一致 | 使用事务和 mutex 保护 |
| 性能问题 | 选择延迟过高 | 缓存策略、异步健康检查 |
| 告警风暴 | 系统过载 | 告警聚合、去重、限流 |
| 向后兼容性 | 现有功能破坏 | 优先级设计、回退机制 |

---

## 文件清单

已创建的文件：
- `/d/pairproxy/internal/db/models.go` - 更新，添加新模型
- `/d/pairproxy/internal/db/db.go` - 更新，添加新表到迁移
- `/d/pairproxy/internal/db/group_target_set_repo.go` - 新建
- `/d/pairproxy/internal/db/target_alert_repo.go` - 新建
- `/d/pairproxy/internal/proxy/group_target_selector.go` - 新建
- `/d/pairproxy/docs/superpowers/specs/DESIGN_REVIEW.md` - 新建
- `/d/pairproxy/docs/superpowers/specs/2026-03-25-group-target-set-pooling-alerting.md` - 更新

待创建的文件：
- `/d/pairproxy/internal/alert/target_alert_manager.go`
- `/d/pairproxy/internal/alert/target_health_monitor.go`
- `/d/pairproxy/internal/api/admin_targetset_handler.go`
- `/d/pairproxy/internal/api/admin_alert_handler.go`
- `/d/pairproxy/internal/api/sse_alert_handler.go`
- 测试文件（*_test.go）
- Dashboard 页面和模板

---

## 性能目标

- **选择延迟**: < 5ms (缓存命中)
- **健康检查间隔**: 30s
- **告警创建延迟**: < 100ms
- **SSE 推送延迟**: < 500ms
- **缓存命中率**: > 80%

---

**生成时间**: 2026-03-25 14:30:00 UTC
**下次更新**: 完成 TargetAlertManager 实现后
