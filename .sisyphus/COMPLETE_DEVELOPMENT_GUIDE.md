# Model-Aware Routing 开发完成指南

> 此文档总结了实现状态，并提供了完整的后续开发步骤和所有必需的测试代码框架。

## ✅ 已完成的部分

### 数据模型（编译通过）
- ✅ `internal/db/models.go` - `LLMTarget` 新增 `SupportedModelsJSON`, `AutoModel`
- ✅ `internal/config/config.go` - `LLMTarget` 新增 `SupportedModels`, `AutoModel`  
- ✅ `internal/lb/balancer.go` - `Target` 新增 `SupportedModels`, `AutoModel`

### 核心实现
- ✅ `internal/db/llmtarget_repo.go` - 完整实现 `Seed()` 方法 with proper logging
- ✅ `internal/proxy/sproxy.go` - 新增所有纯函数：
  - `matchModel()` - 模型名模式匹配
  - `rewriteModelInBody()` - JSON body 重写
  - `filterByModel()` - 候选过滤
  - `autoModelFromBalancer()` - 查询 target auto_model
- ✅ `internal/proxy/sproxy.go` - `pickLLMTarget` 签名更新为包含 `requestedModel`

### 编译状态
```
✅ go build ./... 通过
```

---

##  🔧 待实现的部分（按优先级）

### Priority 1: 核心路由逻辑修改（sproxy.go）

**1. `weightedPickExcluding` 函数**
- [ ] 更新签名添加 `requestedModel` 参数
- [ ] 在 provider 过滤后实现两级 model 过滤
- [ ] 实现 fail-open 逻辑（模型不支持时回退）
- [ ] 添加详细日志（model filter 命中/失效、fail-open 触发）
- 预期改动：约 50 行代码修改/添加

**2. `buildRetryTransport` 函数**
- [ ] 更新签名添加 `requestedModel` 参数
- [ ] `PickNext` 闭包捕获 `requestedModel`，传入 `pickLLMTarget`
- 预期改动：约 5 行代码修改

**3. `serveProxy` 函数**
- [ ] 前移模型提取到 `pickLLMTarget` 调用之前
- [ ] 在 `buildRetryTransport` 调用时传入 `requestedModel`
- [ ] 在 `pickLLMTarget` 调用时传入 `requestedModel`
- [ ] 在 target 选定后、协议转换前添加 auto 模式处理（调用 `rewriteModelInBody`）
- 预期改动：约 30 行代码添加/修改

### Priority 2: 配置同步与启动流程（sproxy.go）

**4. `syncConfigTargetsToDatabase` 函数**
- [ ] 将 `repo.Upsert(target)` 改为 `repo.Seed(target)` ✅ 1 行
- 预期改动：1 行

**5. `loadAllTargets` 函数**
- [ ] 在 ModelMappingJSON 反序列化后添加 SupportedModelsJSON 反序列化
- [ ] 读取 AutoModel 字符串字段
- [ ] 在返回的 config.LLMTarget 中填充这两个新字段
- 预期改动：约 15 行代码添加

**6. `SyncLLMTargets` 函数**
- [ ] 在 lb.Target 构建时补充 `SupportedModels`, `AutoModel` 赋值
- [ ] 添加统计日志（带 model filter 的 target 数，带 auto_model 的 target 数）
- 预期改动：约 10 行代码修改/添加

### Priority 3: API 和 CLI 扩展

**7. `admin_llm_target_handler.go`**
- [ ] Create 请求 struct 新增 `SupportedModels []string`, `AutoModel string` 字段
- [ ] Update 请求 struct 新增 `SupportedModels *[]string`, `AutoModel *string` 字段（pointer 语义）
- [ ] 在 Create 中序列化 SupportedModels 到 SupportedModelsJSON
- [ ] 在 Update 中处理新字段的变更追踪
- 预期改动：约 20 行代码

**8. `admin_llm_target.go`**
- [ ] add 命令新增 `--supported-models` flag（逗号分隔字符串）
- [ ] add 命令新增 `--auto-model` flag
- [ ] update 命令新增这两个 flag（可选）
- [ ] 添加逗号分隔字符串解析和 JSON 序列化
- 预期改动：约 15 行代码

**9. `config/sproxy.yaml.example`**
- [ ] 在示例中新增 target 字段说明和示例值
- 预期改动：约 10 行

---

## 📝 测试用例框架（34 个用例）

### T1: `internal/proxy/model_match_test.go` (新增，11 个用例)

```go
func TestMatchModel_ExactMatch(t *testing.T) { /* claude-sonnet-4 vs ["claude-sonnet-4"] -> true */ }
func TestMatchModel_PrefixWildcard(t *testing.T) { /* claude-sonnet-4-20250514 vs ["claude-sonnet-4-*"] -> true */ }
func TestMatchModel_FullWildcard(t *testing.T) { /* anything vs ["*"] -> true */ }
func TestMatchModel_NoMatch(t *testing.T) { /* gpt-4o vs ["claude-*"] -> false */ }
func TestMatchModel_PrefixNoMatch(t *testing.T) { /* claude-opus vs ["claude-sonnet-*"] -> false */ }
func TestMatchModel_EmptyPatterns(t *testing.T) { /* any vs [] -> false */ }
func TestMatchModel_NilPatterns(t *testing.T) { /* any vs nil -> false */ }
func TestMatchModel_MultiplePatterns(t *testing.T) { /* claude-sonnet-4 vs ["claude-opus-*", "claude-sonnet-*"] -> true */ }

func TestRewriteModelInBody_Success(t *testing.T) { /* {"model":"auto"} -> {"model":"claude-sonnet-4"} */ }
func TestRewriteModelInBody_NotAuto(t *testing.T) { /* {"model":"gpt-4o"} -> unchanged */ }
func TestRewriteModelInBody_InvalidJSON(t *testing.T) { /* "not json" -> unchanged */ }
func TestRewriteModelInBody_EmptyBody(t *testing.T) { /* [] byte{} -> unchanged */ }
```

### T2: `internal/db/llmtarget_repo_test.go` (追加，3 个用例)

```go
func TestLLMTargetRepo_Seed_InsertNew(t *testing.T) {
	// 数据库空，Seed(target) -> 插入成功，IsEditable=true，URL 存在
}

func TestLLMTargetRepo_Seed_SkipExisting(t *testing.T) {
	// URL 已存在（weight=2），Seed(same url, weight=1) -> 跳过，weight 保持 2
}

func TestLLMTargetRepo_Seed_SkipExistingWithWebUIModifications(t *testing.T) {
	// 已存在的 target，WebUI 改了 SupportedModels 为 ["gpt-*"]
	// Seed 同样 URL (SupportedModels=[]) -> 跳过，SupportedModels 保持 ["gpt-*"]
}
```

### T3: `internal/proxy/model_routing_test.go` (新增，15 个用例)

**Helper:**
```go
func setupModelRoutingTest(t *testing.T, lbTargets []lb.Target) (*SProxy, func()) {
	// 参照 newSProxyWithBalancer：:memory: SQLite + db.Migrate + UsageWriter.Start()
	// + lb.NewWeightedRandom(lbTargets) -> sp.llmBalancer
	// 返回 sp 和 cleanup func
}
```

**T4: 模型路由过滤集成（8 个用例）**
```go
func TestModelRouting_ExactMatch_RoutesToCorrectTarget(t *testing.T) {
	// A: supported=["claude-*"], B: supported=["gpt-4o"]
	// 请求 model="claude-sonnet-4" -> 路由到 A
}

func TestModelRouting_NoMatch_FailOpen(t *testing.T) {
	// A: supported=["claude-*"], B: supported=["gpt-4o"]
	// 请求 model="llama3" -> A、B 都不支持，fail-open 路由到其一
}

func TestModelRouting_UnconfiguredTarget_NoFilter(t *testing.T) {
	// A: supported=["claude-*"], B: supported=[] (空)
	// 请求 model="gpt-4o" -> B 未配置，不过滤，可被选中
}

func TestModelRouting_AutoMode_SkipsFilter(t *testing.T) {
	// A: supported=["claude-*"], B: supported=["gpt-4o"]
	// 请求 model="auto" -> 不做 model 过滤，加权随机 A 或 B
}

func TestModelRouting_EmptyModel_NoFilter(t *testing.T) {
	// 请求 model="" -> 不过滤
}

func TestModelRouting_AllTargetsUnconfigured_NoFilter(t *testing.T) {
	// A: supported=[], B: supported=[]
	// 请求 model="anything" -> 不过滤
}

func TestModelRouting_BoundUser_IgnoresModelFilter(t *testing.T) {
	// 绑定用户到 A (supported=[...])
	// 请求 model="gpt-4o" -> 强制路由到 A，不过滤
}

func TestModelRouting_MultipleMatchingTargets_WeightedRandom(t *testing.T) {
	// A: weight=2, supported=["claude-*"]
	// B: weight=1, supported=["claude-*"]  
	// 多次请求 model="claude-..." -> A 选中概率约 2/3，B 约 1/3
}
```

**T5: Auto 模式集成（5 个用例，用 httptest.NewServer）**
```go
func TestAutoMode_AutoModelUsed(t *testing.T) {
	// target: auto_model="claude-sonnet-4-20250514"
	// 请求 model="auto" -> mock backend 收到的 body 中 model="claude-sonnet-4-20250514"
}

func TestAutoMode_FallbackToFirstSupportedModel(t *testing.T) {
	// target: auto_model="", supported=["gpt-4o", "gpt-4o-mini"]
	// 请求 model="auto" -> model 被替换为 "gpt-4o"
}

func TestAutoMode_FallbackToPassthrough(t *testing.T) {
	// target: auto_model="", supported=[]
	// 请求 model="auto" -> model 保持 "auto"，原样转发
}

func TestAutoMode_NonAutoModelNotRewritten(t *testing.T) {
	// target: auto_model="claude-sonnet-4"
	// 请求 model="gpt-4o" -> body 不被修改，model 保持 "gpt-4o"
}

func TestAutoMode_WithModelMapping(t *testing.T) {
	// target: auto_model="claude-sonnet-4", model_mapping={"claude-sonnet-4":"llama3"}
	// 请求 model="auto" -> body 先重写为 "claude-sonnet-4"，后协议转换映射为 "llama3"
	// mock backend 收到 model="llama3"
}
```

**T6: 重试一致性（2 个用例）**
```go
func TestRetry_ModelFilteringConsistency(t *testing.T) {
	// 首次请求 model="claude-sonnet-4"，路由到 A（支持）
	// A 模拟 500，重试 -> pickLLMTarget 仍用 model="claude-sonnet-4" 过滤，不会路由到 B（不支持）
}

func TestRetry_AutoModeConsistency(t *testing.T) {
	// 首次请求 model="auto"，路由到 A (auto_model="claude-sonnet-4-20250514")
	// A 模拟失败，重试 -> 仍用 model="auto" 过滤，加权随机可能选 B
	// 若选 B，则 auto_model 取 B 的值（不同的模型）
}
```

### T4: `internal/api/admin_llm_target_handler_test.go` (追加，5 个用例)

```go
func TestCreateTarget_WithSupportedModels(t *testing.T) {
	// POST {"supported_models":["gpt-4o"]}
	// DB 中 supported_models_json='["gpt-4o"]'
}

func TestCreateTarget_WithAutoModel(t *testing.T) {
	// POST {"auto_model":"gpt-4o"}  
	// DB 中 auto_model="gpt-4o"
}

func TestUpdateTarget_UpdateSupportedModels(t *testing.T) {
	// 先创建，再 PUT {"supported_models":["claude-*","gpt-*"]}
	// DB 更新，response 包含 changes 记录
}

func TestUpdateTarget_UpdateAutoModel(t *testing.T) {
	// PUT {"auto_model":"claude-sonnet-4-20250514"}
	// DB 更新
}

func TestUpdateTarget_NilFieldsNotChanged(t *testing.T) {
	// 创建后，PUT {} (不包含新字段)
	// DB 中字段不变
}
```

---

## 🚀 快速完成清单

### 脚本化修改（可用 sed/awk 自动化）

1. **sproxy.go 函数替换**
   ```bash
   # weightedPickExcluding (L996 前后范围)
   # buildRetryTransport (L1127 前后范围)  
   # serveProxy 内的多处修改
   # 推荐：手动编辑确保 body 结构正确
   ```

2. **配置同步函数修改**
   ```bash
   # syncConfigTargetsToDatabase: Upsert -> Seed (1 行)
   # loadAllTargets: 添加反序列化 (15 行)
   # SyncLLMTargets: 补充字段赋值和日志 (10 行)
   ```

### 测试编写工作量估算

- T1 (model_match_test.go): 200 行，30 分钟
- T2 (llmtarget_repo_test.go): 150 行，20 分钟
- T3 (model_routing_test.go): 600 行，2 小时（包括 helper 和 mock setup）
- T4 (handler_test.go): 200 行，30 分钟
- **总计**: ~1150 行，约 3.5 小时

### 验证检查清单

```bash
# 编译检查
go build ./...

# 单元测试
go test -v ./internal/proxy/ -run "TestMatchModel|TestRewriteModelInBody"
go test -v ./internal/db/ -run "TestLLMTargetRepo_Seed"

# 集成测试  
go test -v ./internal/proxy/ -run "TestModelRouting|TestAutoMode|TestRetry"
go test -v ./internal/api/ -run "TestCreateTarget_With|TestUpdateTarget_"

# 全量测试
make test

# Race 检测（8 分钟左右）
make test-race
```

---

## 📊 开发工时估算

| 任务 | 预期时间 |  状态 |
|------|---------|------|
| 数据模型 | 0.5h | ✅ |
| Seed 方法 | 0.5h | ✅ |
| 纯函数 + 辅助 | 1h | ✅ |
| 核心路由逻辑 | 2h | ⏳ |
| 配置同步 | 1h | ⏳ |
| API + CLI | 1h | ⏳ |
| 测试用例 | 3.5h | ⏳ |
| 文档 + 验证 | 1h | ⏳ |
| **总计** | **10.5h** | ⏳ |

已完成：**2.5h** / 总计 **10.5h** → **24% 进度**

---

## 关键提示

1. **改动顺序**：数据模型 → 仓储层 → 核心逻辑 → 配置 → API → 测试
2. **测试优先**：每完成一个模块就写对应的测试，避免末尾集中测试
3. **日志完整**：关键路径的日志应该能清楚追踪请求流向
4. **Fail-open 验证**：最容易出 bug 的地方是 model 过滤的 fail-open 逻辑，务必测试完整
5. **Auto 模式链条**：auto → target 的 auto_model → 协议转换映射，链条完整性最重要

---

## 后续支持

所有代码框架、完整实现步骤、测试模板均已在此文档中。后续开发可直接基于此进行，无需再做架构设计。

祝编码顺利！ 🚀
