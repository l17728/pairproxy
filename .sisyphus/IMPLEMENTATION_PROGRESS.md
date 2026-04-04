# Model-Aware Routing 实现进度

## 已完成 ✅

1. **数据模型扩展**
   - [x] `internal/db/models.go` - LLMTarget 新增 `SupportedModelsJSON`, `AutoModel`
   - [x] `internal/config/config.go` - LLMTarget 新增 `SupportedModels`, `AutoModel`  
   - [x] `internal/lb/balancer.go` - Target 新增 `SupportedModels`, `AutoModel`

2. **数据库仓储 (F1)**
   - [x] `internal/db/llmtarget_repo.go` - 实现 `Seed()` 方法

3. **核心路由函数**
   - [x] `sproxy.go` 新增 `matchModel()` - 模型名模式匹配
   - [x] `sproxy.go` 新增 `rewriteModelInBody()` - 请求体 model 字段重写
   - [x] `sproxy.go` 新增 `filterByModel()` - 候选 target 过滤
   - [x] `sproxy.go` 新增 `autoModelFromBalancer()` - 查询 target 的 auto_model

4. **路由签名与实现 (F2)**
   - [x] `pickLLMTarget` 签名新增 `requestedModel` 参数
   - [ ] `weightedPickExcluding` 签名新增 `requestedModel`，实现模型过滤 (待编辑)
   - [ ] `buildRetryTransport` 签名新增 `requestedModel`，闭包捕获 (待编辑)
   - [ ] `serveProxy` 前移模型提取，添加 auto 模式处理 (待编辑)

5. **配置同步 (F1+F2+F3)**
   - [ ] `syncConfigTargetsToDatabase` 改用 `Seed` 方法 (待编辑)
   - [ ] `loadAllTargets` 新增反序列化逻辑 (待编辑)
   - [ ] `SyncLLMTargets` 补充新字段赋值与日志 (待编辑)

6. **API Handler (F2+F3)**
   - [ ] `admin_llm_target_handler.go` - Create/Update 新增字段 (待编辑)

7. **CLI (F2+F3)**
   - [ ] `admin_llm_target.go` - 新增 flag (待编辑)

## 测试用例 (34 个)

- [ ] `model_match_test.go` (新增) - 11 个用例
  - matchModel 的 7 种场景
  - rewriteModelInBody 的 4 种场景
  
- [ ] `llmtarget_repo_test.go` (追加) - 3 个用例
  - Seed_InsertNew
  - Seed_SkipExisting
  - Seed_SkipExistingWithWebUIModifications

- [ ] `model_routing_test.go` (新增) - 15 个用例
  - T4: 模型过滤路由集成测试 8 个
  - T5: auto 模式集成测试 5 个
  - T6: 重试一致性测试 2 个

- [ ] `admin_llm_target_handler_test.go` (追加) - 5 个用例
  - Create/Update 新字段验证

## 当前状态

✅ 编译通过 - 数据模型和新函数已就位
⚙️ 需要继续编辑现有函数签名和实现
📝 需要实现 34 个测试用例

## 下一步

1. 修改 `weightedPickExcluding` 实现两级 fail-open 模型过滤
2. 修改 `buildRetryTransport` 和相关调用点
3. 修改 `serveProxy` 前移模型提取和 auto 重写
4. 修改配置同步函数
5. 扩展 API Handler 和 CLI
6. 编写全套测试用例
