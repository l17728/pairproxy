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
   - [x] `sproxy.go` 新增 `autoModelFromURL()` - 查询 target 的 auto_model

4. **路由签名与实现 (F2)**
   - [x] `pickLLMTarget` 签名新增 `requestedModel` 参数
   - [x] `weightedPickExcluding` 签名新增 `requestedModel`，实现两级 fail-open 模型过滤
   - [x] `buildRetryTransport` 签名新增 `requestedModel`，闭包捕获
   - [x] `serveProxy` 前移模型提取，添加 auto 模式处理

5. **配置同步 (F1+F2+F3)**
   - [x] `syncConfigTargetsToDatabase` 改用 `Seed` 方法
   - [x] `loadAllTargets` 新增 `SupportedModelsJSON` / `AutoModel` 反序列化
   - [x] `SyncLLMTargets` 补充新字段赋值与统计日志

6. **API Handler (F2+F3)**
   - [x] `admin_llm_target_handler.go` - Create/Update 新增 `supported_models`, `auto_model` 字段

7. **CLI (F2+F3)**
   - [x] `admin_llm_target.go` - 新增 `--supported-models`, `--auto-model` flag
   - [x] `help_ref.go` - 帮助文档更新

8. **配置示例**
   - [x] `config/sproxy.yaml.example` - 新增 `supported_models` / `auto_model` 示例

## 测试用例

### 已有单元测试 (sproxy_test.go)
- [x] `TestMatchModel_*` (4 个) - 精确匹配、前缀通配、全通配、边界条件
- [x] `TestRewriteModelInBody_*` (3 个) - 有效 JSON、无效 JSON、无 model 字段
- [x] `TestFilterByModel_*` (5 个) - 精确匹配、模式匹配、未配置、fail-open、多匹配
- [x] `TestAutoModelFrom*` (5 个) - 显式 auto_model、降级到 supported[0]、空配置、不存在、无 balancer

### 新增测试
- [x] `internal/db/llmtarget_repo_test.go` - Seed 方法测试 (3 个)
  - TestLLMTargetRepo_Seed_InsertNew
  - TestLLMTargetRepo_Seed_SkipExisting
  - TestLLMTargetRepo_Seed_SkipExistingWithWebUIModifications

- [x] `internal/proxy/model_routing_test.go` - 集成 + E2E 测试 (15 个)
  - TestModelRouting_ExactMatch_RoutesToCorrectTarget
  - TestModelRouting_NoMatch_FailOpen
  - TestModelRouting_UnconfiguredTarget_NoFilter
  - TestModelRouting_AutoMode_SkipsFilter
  - TestModelRouting_EmptyModel_NoFilter
  - TestModelRouting_AllTargetsUnconfigured_NoFilter
  - TestAutoMode_AutoModelUsed (E2E with httptest backend)
  - TestAutoMode_FallbackToFirstSupportedModel
  - TestAutoMode_FallbackToPassthrough
  - TestAutoMode_NonAutoModel_NotRewritten
  - TestRetry_ModelFilteringConsistency
  - TestRetry_AutoModeConsistency
  - TestE2E_ModelAwareRouting_FullFlow
  - TestE2E_AutoMode_FullFlow
  - TestE2E_SeedThenWebUIUpdate_PreservesChanges

## 最终状态

✅ 编译通过 - `go build ./...` 无错误
✅ 全量测试通过 - 26 个包全部 PASS
✅ F1 Config-as-Seed 完整实现
✅ F2 Per-Target Supported Models 完整实现
✅ F3 Auto Mode 完整实现
✅ 向后兼容 - 无 supported_models 配置时行为不变
