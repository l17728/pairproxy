# Model-Aware Routing 设计文档 Review 总结

## 概述

本文档记录了对 `.sisyphus/plans/model-aware-routing.md` 设计文档的专业 review，发现并修正了 **10 个问题**，完善了测试用例设计。

---

## Review 发现的问题清单

### 🔴 P1：架构问题 - `sp.targets` 与 WebUI 热更新

**初稿错误识别**：
- 初稿误认为 `SyncLLMTargets` 会写入 `sp.targets`，需要原子保护
- 实际情况：`sp.targets` 是启动时一次性构建的静态字段，不被 `SyncLLMTargets` 修改

**真实问题**：
- WebUI 修改 `supported_models`/`auto_model` 后，路由过滤**不会立即生效**，必须重启
- 根因：路由查询使用 `sp.targets`（静态），而非 `sp.llmBalancer.Targets()`（动态、原子更新）

**修正方案**：
- 新增 `supportedModelsFromBalancer()` 和 `autoModelFromBalancer()` 方法
- 改为从 `sp.llmBalancer.Targets()` 查询，不查 `sp.targets`
- 在 `SyncLLMTargets` 中为 `lb.Target` 赋值 `SupportedModels` 和 `AutoModel` 字段

**影响**：F2、F3 的实现架构重新设计

---

### 🔴 P2：F2 实现问题 - fail-open 两级候选集

**初稿错误**：
- 提议在 filter 闭包内处理 model 过滤
- 这会导致 fail-open 回退时无法独立回退 model 层

**修正方案**：
- 在 `weightedPickExcluding` 返回后单独处理 model 过滤
- 实现清晰的两级候选集：A（provider 过滤后）→ B（model 过滤后）
- B 为空时回退到 A，A 也为空时回退到全量健康

**影响**：F2 的路由过滤实现逻辑调整

---

### 🔴 P3：重试路径问题 - 缺少 `requestedModel` 传递

**遗漏内容**：
- `buildRetryTransport` 的 `PickNext` 闭包未捕获 `requestedModel`
- 重试时会用空的 model 名，导致过滤失效

**修正方案**：
- `serveProxy` 中提取 `requestedModel` 后需传入 `buildRetryTransport`
- `PickNext` 闭包捕获 `requestedModel`，重试时传入 `pickLLMTarget`

**影响**：F2、F3 的重试路径一致性

---

### 🟡 P4：初稿错误 - 参数名混淆

**初稿误会**：
- 认为 `supportedModelsForURL(t.ID)` 是 bug，因为 `t.ID` != `t.URL`

**真相**：
- `SyncLLMTargets` 中 `lb.Target.ID = t.URL`（代码第 557 行）
- 所以 `t.ID` 的值就是 URL 字符串，不是 bug

**修正**：
- 无需改代码，仅在注释中说明 `t.ID` 被赋值为 URL

---

### 🟡 P5：F1 的 IsEditable 问题

**初稿不精确**：
- 提议"Seed 时将 `IsEditable` 更新为 `true`"
- 但 Seed 的设计语义就是"已存在则跳过"，改字段就不是纯粹 Seed 了

**修正方案**：
- 需要在首次通过配置文件播种时，就将 `IsEditable` 设为 `true`
- 或在 API handler 中对新字段做 partial editability（绕过 IsEditable 检查）
- 设计文档标记为"需在实现时明确选择"

---

### 🟡 P6：P3 的触发条件不现实

**初稿假设**：
- "WebUI 修改了 Source 字段"导致 `DeleteConfigTargetsNotInList` 失效

**真相**：
- `handleUpdateTarget` 对 config-sourced target 直接返回 403（因为 `IsEditable=false`）
- WebUI 根本无法修改任何字段，更不用说 Source

**修正**：
- 文档改为基于真实架构（需要让新字段可编辑），而非虚假触发路径

---

### 🟢 P7：通配符语义需明确

**问题**：
- `matchModel` 只支持前缀通配，未清楚说明限制

**修正**：
- 文档补充注释说明支持的精确语义：精确匹配、前缀通配、全通配

---

### 🟢 P8：测试参照函数错误

**初稿错误**：
- 引用了不存在的 `newCProxyWithBalancer` 函数

**修正**：
- 改为引用实际存在的 `newSProxyWithBalancer` 函数

---

### 🟢 P9：Auto 模式 E2E 测试缺少 mock backend

**初稿缺失**：
- 没有说明需要用 `httptest.NewServer` 捕获转发的实际 body

**修正**：
- 补充说明 T5 集成测试必须用 mock backend 验证真正转发的 body
- T2 单元测试只验证函数逻辑

---

### 🟢 P10：缺少重试场景测试

**初稿遗漏**：
- 没有专门的重试测试用例

**修正**：
- 新增 T6 小节，覆盖两个重试测试：
  - `TestRetry_ModelFilteringConsistency`
  - `TestRetry_AutoModeConsistency`

---

## 修改统计

| 类别 | 数量 |
|------|------|
| **架构层面调整** | 3 项 |
| **功能设计修正** | 3 项 |
| **测试设计完善** | 4 项 |
| **文档问题修正** | 10 项 |
| **新增测试用例** | 2 个（重试测试） |
| **删除测试用例** | 1 个（错误的并发测试 T7） |
| **测试总数调整** | 32 → 34 个 |

---

## 关键改进

### 1. 并发模型的正确认识

**原错误**：认为 `sp.targets` 需要原子保护
**正确理解**：
- `sp.targets` 是静态的，无并发问题
- 真正的热更新通过 `sp.llmBalancer.UpdateTargets()` 实现（已有并发保护）

### 2. 查询路径的架构调整

**新增方法**：
- `supportedModelsFromBalancer(targetID string) []string`
- `autoModelFromBalancer(targetID string) string`

**改查来源**：`sp.targets` → `sp.llmBalancer.Targets()`

**意义**：WebUI 修改后立即生效，无需重启

### 3. 两级过滤的清晰设计

**分离原则**：
- provider 过滤（在 `weightedPickExcluding` 内）
- model 过滤（在其返回后单独处理）

**好处**：
- fail-open 策略易于理解和维护
- 避免闭包内逻辑过于复杂

### 4. 重试路径的完整性

**新需求**：
- `buildRetryTransport(requestedModel)` 需要捕获参数
- 重试时确保模型过滤一致

**保证**：重试和首次请求使用相同的过滤条件

---

## 文档结构调整

### 新增

1. **关键架构决策** 小节 - 说明三个核心架构问题的解决方案
2. **T6 重试测试** 小节 - 覆盖重试时的一致性
3. **设计文档改进总结** 小节 - 整体改进的总结

### 重写

1. **F2 小节** - 改为从 `sp.llmBalancer.Targets()` 查询（热更新支持）
2. **F3 小节** - 改为从 `sp.llmBalancer.Targets()` 查询，明确执行顺序
3. **改动文件清单** - 改为按功能分类，突出架构改动
4. **测试设计** - 删除错误的并发测试，完善现有测试说明

### 删除

1. **T7 Race Condition Tests** - 基于错误的并发假设，已删除
2. **初稿的"专家 Review 发现与建议"** - 问题描述错误且混乱，已重新组织

---

## 对实现的影响

### 必需的代码改动

1. **`internal/lb/balancer.go`**
   - `Target` struct 新增 `SupportedModels []string` 和 `AutoModel string`

2. **`internal/proxy/sproxy.go`**
   - 新增 `supportedModelsFromBalancer()` 和 `autoModelFromBalancer()` 方法
   - 修改 `pickLLMTarget()` 签名，新增 `requestedModel` 参数
   - 在 `weightedPickExcluding()` 返回后单独处理 model 过滤（`filterByModel()` 方法）
   - `buildRetryTransport()` 捕获 `requestedModel` 参数
   - `SyncLLMTargets` 中为 `lb.Target` 赋值新字段

3. **`internal/proxy/sproxy.go` — F1**
   - `syncConfigTargetsToDatabase` 改用 `Seed` 方法

4. **数据模型**（DB、Config、LB 层）
   - 各层新增 `SupportedModels` 和 `AutoModel` 字段

### 测试实现要点

1. **使用 `httptest.NewServer`** - 所有 E2E 和集成测试都需用 mock backend
2. **捕获转发的 body** - 验证 auto 重写和协议转换的正确性
3. **重试一致性** - 验证 `requestedModel` 在重试路径中的传递
4. **移除并发测试** - 不需要对 `sp.targets` 做并发测试

---

## 后续建议

### 实现前

1. **IsEditable 决策** - 确定首次创建就设为 true，还是做 partial editability
2. **Code Review** - 将修正后的设计文档与核心开发者共享
3. **API 设计** - 确认新增的 `supported_models` 和 `auto_model` 字段的 API 表示

### 实现后

1. **集成测试充分性** - 确保 E2E 测试覆盖所有主要场景
2. **性能基准** - 验证模型过滤的性能开销（JSON 反序列化 + 过滤）
3. **用户文档** - 说明如何通过 WebUI 配置新字段

---

## 总结

这次 review 和改进**纠正了关键的架构误解**（并发模型、查询路径），**完善了实现细节**（两级过滤、重试一致性），**强化了测试设计**（E2E 完整性）。修正后的设计文档更准确、更清晰、更易实现。
