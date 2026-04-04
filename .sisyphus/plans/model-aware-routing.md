# Feature Design: Model-Aware Routing (模型感知路由)

> 状态：Draft v1
> 范围：3 个子特性，向后兼容

---

## 背景与动机

当前网关路由逻辑是「路径 → Provider 过滤 → 负载均衡」：
- `/v1/messages` → 只选 `provider=anthropic` 的 target
- `/v1/chat/completions` → 只选 `provider=openai/ollama` 的 target
- 在同一 provider 池内做加权随机负载均衡

**问题**：当一个 group 下混配了不同供应商的 key（Anthropic + OpenAI + Ollama），网关不管模型差异，所有 target 等概率分配。用户请求 `claude-sonnet-4` 但被路由到 Ollama target 时，LLM 端返回错误。

**目标**：网关能感知每个 target 支持哪些模型，在路由时过滤掉不支持用户所选模型的 target。

---

## 子特性概览

| # | 名称 | 描述 |
|---|------|------|
| F1 | Config-as-Seed | 配置文件只播种不覆盖，WebUI 修改永久生效 |
| F2 | Per-Target Supported Models | 每个 target 声明支持的模型列表，路由时按模型过滤 |
| F3 | Auto Mode | 用户发送 `model: "auto"` 时，网关选择最佳模型并重写请求 |

---

## F1: Config-as-Seed（配置文件只播种不覆盖）

### 现状问题

`syncConfigTargetsToDatabase()` 启动时对配置文件中的每个 target 调用 `repo.Upsert()`。
Upsert 逻辑：URL 已存在 → **覆盖全部字段**。WebUI 对 config 来源 target 的修改（weight、model_mapping 等）下次重启被重置。

### 改动方案

**改动范围**：2 个文件，~30 行

#### 1. `internal/db/llmtarget_repo.go` — 新增 `Seed` 方法

```go
// Seed 插入 target（仅当 URL 不存在时）。已存在则跳过，保留 WebUI 修改。
// 用于配置文件启动同步：配置文件是种子，不是权威源。
func (r *LLMTargetRepo) Seed(target *LLMTarget) error {
    exists, err := r.URLExists(target.URL)
    if err != nil {
        return err
    }
    if exists {
        r.logger.Debug("seed: target already exists, skipping",
            zap.String("url", target.URL))
        return nil
    }
    // 不存在 → 走原有 Upsert 逻辑（含 boolean false 修复）
    return r.Upsert(target)
}
```

#### 2. `internal/proxy/sproxy.go` — `syncConfigTargetsToDatabase` 改用 Seed

```go
// 原代码:
err = repo.Upsert(target)
// 改为:
err = repo.Seed(target)
```

`DeleteConfigTargetsNotInList` 逻辑不变：配置文件删除的 target 仍然会被清理。

### 行为变化

| 场景 | 现状 | 改后 |
|------|------|------|
| 首次启动，DB 空 | Upsert → INSERT | Seed → INSERT（同） |
| 重启，WebUI 未改 | Upsert → UPDATE（无变化） | Seed → SKIP（同效果） |
| 重启，WebUI 改了 weight | Upsert → 覆盖回配置值 | Seed → SKIP，保留 WebUI 值 |
| 重启，WebUI 改了 supported_models | Upsert → 覆盖回空 | Seed → SKIP，保留 WebUI 值 |
| 配置文件新增 target | Upsert → INSERT | Seed → INSERT（同） |
| 配置文件删除 target | DeleteConfigTargetsNotInList → 删除 | DeleteConfigTargetsNotInList → 删除（同） |

---

## F2: Per-Target Supported Models（模型感知路由）

### 数据模型

#### DB — `internal/db/models.go`

在 `LLMTarget` struct 中新增：

```go
SupportedModelsJSON string `gorm:"column:supported_models;default:'[]'"` // JSON array: ["claude-sonnet-4-*", "gpt-4o"]
```

#### Config — `internal/config/config.go`

在 `LLMTarget` struct 中新增：

```go
SupportedModels []string `yaml:"supported_models,omitempty"` // 支持的模型名列表（支持通配符）
```

#### LB 层 — `internal/lb/balancer.go`

`Target` struct 新增（由 `SyncLLMTargets` 赋值，供路由层查询）：

```go
SupportedModels []string // 从 DB 读取，LB 层维护
```

### 数据流与热更新保证

```
启动时：YAML config → syncConfigTargetsToDatabase (Seed) → DB (supported_models_json)
                                                              ↓
                                                        SyncLLMTargets
                                                              ↓
                                               lb.Target(SupportedModels) ← 原子更新

热更新：WebUI/API → handleUpdateTarget → DB 更新 (supported_models_json)
                                              ↓
                                       SyncLLMTargets (手动或定时调用)
                                              ↓
                                    lb.Target(SupportedModels) ← 原子更新
                                              ↓
                            路由查询时调用 supportedModelsFromBalancer()
```

关键：**路由层不查 `sp.targets`（启动时构建的静态字段），改查 `sp.llmBalancer.Targets()`（动态、原子更新）**。

### 路由过滤的实现

#### `pickLLMTarget` 签名变更

新增 `requestedModel` 参数以支持条件路由：

```go
func (sp *SProxy) pickLLMTarget(path, userID, groupID, requestedModel string, tried []string, candidateFilter []string) (*lb.LLMTargetInfo, error)
```

#### 实现流程

1. **在 `serveProxy` 中前移模型提取**（在 `pickLLMTarget` 之前）：

```go
requestedModel := extractModel(r)
if requestedModel == "" && len(bodyBytes) > 0 {
    requestedModel = extractModelFromBody(bodyBytes)
}

// 将 requestedModel 传入
firstInfo, pickErr := sp.pickLLMTarget(r.URL.Path, claims.UserID, claims.GroupID, requestedModel, nil, semanticCandidates)
```

2. **在 `weightedPickExcluding` 返回后做模型过滤**（两级候选集）：

```go
func (sp *SProxy) pickLLMTarget(..., requestedModel string, tried []string, ...) (*lb.LLMTargetInfo, error) {
    // ... provider 层逻辑 ...
    
    // 调用现有逻辑得到候选集 A
    firstInfo, err := sp.weightedPickExcluding(path, tried, candidateFilter)
    if err != nil {
        return nil, err
    }
    
    // 模型过滤：候选集 B = 候选集 A 中支持 requestedModel 的
    if requestedModel != "" {
        filtered := sp.filterByModel(firstInfo, requestedModel)
        if filtered != nil {
            return filtered, nil  // 找到匹配的
        }
        // 候选集 B 为空 → fail-open 回退到 A（尽管不支持，也返回首选）
    }
    
    return firstInfo, nil
}
```

#### `filterByModel` 辅助方法

```go
// filterByModel 检查 targetInfo 是否支持 requestedModel。
// 若支持则返回 targetInfo；若不支持则返回 nil（fail-open：让上层回退）
func (sp *SProxy) filterByModel(targetInfo *lb.LLMTargetInfo, requestedModel string) *lb.LLMTargetInfo {
    for _, t := range sp.llmBalancer.Targets() {
        if t.ID == targetInfo.ID {
            if len(t.SupportedModels) > 0 && !matchModel(requestedModel, t.SupportedModels) {
                return nil  // 不匹配
            }
            return targetInfo  // 匹配或未配置过滤
        }
    }
    return nil
}
```

#### `matchModel` — 模型名匹配

```go
// matchModel 检查 model 是否匹配 patterns 中的任一模式。
// 模式支持:
//   - 精确匹配: "claude-sonnet-4-20250514"
//   - 前缀通配: "claude-sonnet-4-*"
//   - 全通配: "*"
func matchModel(model string, patterns []string) bool {
    for _, p := range patterns {
        if p == "*" || p == model {
            return true
        }
        if strings.HasSuffix(p, "*") && strings.HasPrefix(model, p[:len(p)-1]) {
            return true
        }
    }
    return false
}
```

#### 重试路径 — `buildRetryTransport` 变更

```go
func (sp *SProxy) buildRetryTransport(..., requestedModel string, ...) *http.Client {
    return &http.Client{
        Transport: &RetryTransport{
            PickNext: func(tried map[string]bool) (*lb.LLMTargetInfo, error) {
                // 重试时传入相同的 requestedModel，确保过滤一致
                return sp.pickLLMTarget(effectivePath, userID, groupID, requestedModel, tried, nil)
            },
            ...
        },
    }
}
```

`serveProxy` 中调用 `buildRetryTransport` 时需传入 `requestedModel` 参数。

### WebUI/API 暴露

#### API — `internal/api/admin_llm_target_handler.go`

**Create** 请求 struct 新增：
```go
SupportedModels []string `json:"supported_models"` // 可选，空表示支持所有模型
```

**Update** 请求 struct 新增：
```go
SupportedModels *[]string `json:"supported_models"` // nil 表示不修改
```

#### CLI — `cmd/sproxy/admin_llm_target.go`

新增 flag：
```
--supported-models "claude-sonnet-4-*,claude-opus-4-*"
```

逗号分隔，解析为 `[]string`，序列化为 JSON 存入 DB。

### 向后兼容

| 条件 | 行为 |
|------|------|
| target 未配 `supported_models`（空 `[]`） | 不过滤，参与所有路由（**现有行为不变**） |
| target 配了 `supported_models` | 按模型过滤 |
| 请求无 `model` 字段 | 不做模型过滤（**现有行为不变**） |
| 模型过滤后无候选 | fail-open 回退到首选 target（忽略模型过滤） |

**零配置升级**：不升级配置文件、不升级 WebUI，一切照旧。

---

## F3: Auto Mode（自动模型选择）

### 触发条件

客户端发送 `model: "auto"` 时触发。

### 行为

1. **路由阶段**：`model: "auto"` 视为"不限制"——在模型过滤中跳过检查，所有 target 都参与加权负载均衡
2. **选定 target 后**：从该 target 的配置中确定实际模型名（`auto_model` 或 `supported_models[0]`）
3. **请求重写**：将 `model: "auto"` 替换为实际模型名，再转发

### 数据模型

#### Config / DB 新增字段

```yaml
# sproxy.yaml
targets:
  - url: "https://api.anthropic.com"
    auto_model: "claude-sonnet-4-20250514"  # auto 模式下使用此模型
```

DB `LLMTarget` 新增：
```go
AutoModel string `gorm:"column:auto_model;default:''"` // auto 模式使用的模型名
```

LB 层 `Target` 新增：
```go
AutoModel string  // 从 DB 读取
```

#### 降级策略

| target 配置 | auto 模式行为 |
|-------------|--------------|
| `auto_model` 已配置 | 使用配置值，如 `"claude-sonnet-4-20250514"` |
| `auto_model` 未配，`supported_models` 非空 | 使用 `supported_models[0]`（第一个） |
| 都未配 | `model` 保持 `"auto"` 原样透传（LLM 端处理） |

### 实现位置

在 `serveProxy` 中，`pickLLMTarget` 成功选定 target 之后、协议转换之前，处理 auto 重写：

```go
if requestedModel == "auto" {
    actualModel := sp.autoModelFromBalancer(firstInfo.ID)
    if actualModel != "" && len(bodyBytes) > 0 {
        bodyBytes = rewriteModelInBody(bodyBytes, "auto", actualModel)
        r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
        r.ContentLength = int64(len(bodyBytes))
        // 后续协议转换会用实际的 actualModel
    }
    // actualModel 为空 → 透传 "auto"，LLM 端决定
}
```

### 辅助方法

```go
// autoModelFromBalancer 从 balancer.Targets() 中查询 target 的 auto_model。
// 降级策略：auto_model > supported_models[0] > ""
func (sp *SProxy) autoModelFromBalancer(targetID string) string {
    for _, t := range sp.llmBalancer.Targets() {
        if t.ID == targetID {
            if t.AutoModel != "" {
                return t.AutoModel
            }
            if len(t.SupportedModels) > 0 {
                return t.SupportedModels[0]
            }
            return ""
        }
    }
    return ""
}

// rewriteModelInBody 将 JSON body 中的 model 字段从 old 替换为 new。
func rewriteModelInBody(body []byte, old, newModel string) []byte {
    var req map[string]interface{}
    if err := json.Unmarshal(body, &req); err != nil {
        return body  // 解析失败，原样返回
    }
    if m, ok := req["model"].(string); ok && m == old {
        req["model"] = newModel
        newBody, err := json.Marshal(req)
        if err != nil {
            return body
        }
        return newBody
    }
    return body
}
```

### 与协议转换的交互

执行顺序为：**auto 重写 → 协议转换 → 转发**。

- auto 重写在 serveProxy 中完成，输出实际的 `model` 值（如 `"claude-sonnet-4-20250514"`）
- 后续协议转换会用这个实际值去查 `model_mapping`，如果配置了则替换

这样可以支持混合场景，如：auto → 选 Ollama target → auto_model="claude-sonnet-4" → 协议转换映射到 `llama3`。

---

## 关键架构决策

### 问题 1：`sp.targets` 与 WebUI 热更新

`sp.targets` 是运行时路由查询 model/auto_model 的数据源。当前架构中，`sp.targets` 在启动时从配置文件一次性构建，`SyncLLMTargets` 不会更新它，只更新 `sp.llmBalancer`。

这导致：**通过 WebUI 修改 `supported_models`/`auto_model` 并调用 `SyncLLMTargets` 后，路由过滤**不会立即生效**，必须重启服务才能看到变化。

**解决方案**：改为让 `supportedModelsForURL`、`autoModelForURL` 等查询方法不查 `sp.targets`，而改查 `sp.llmBalancer.Targets()`（已在 `SyncLLMTargets` 中原子更新，线程安全）。

实现时，需要在 `lb.Target` struct 中新增 `SupportedModels []string` 和 `AutoModel string` 字段，在 `SyncLLMTargets` 中赋值，查询方法改用 `sp.llmBalancer.Targets()` 遍历而非 `sp.targets`。

### 问题 2：F2 fail-open 两级候选集的实现

`weightedPickExcluding` 中的 filter 闭包同时处理多个条件（健康、tried、candidateFilter、provider），如果在闭包内直接加 model 过滤，则 fail-open 时无法独立回退 model 层——会导致回退逻辑混乱。

**解决方案**：在 filter 闭包外单独处理 model 过滤，流程为：
1. 调用现有逻辑得到候选集 A（provider 过滤、加权随机）
2. 在 A 上做 model 过滤得到候选集 B
3. B 为空则回退到 A
4. A 也为空则回退到全量健康（现有逻辑）

### 问题 3：重试路径中的 `requestedModel` 传递

`buildRetryTransport` 的 `PickNext` 闭包需要捕获 `requestedModel`，在重试时传入 `pickLLMTarget`，确保重试路径的模型过滤与首次请求一致。

---

## 改动文件清单

| 文件 | 改动内容 | 子特性 |
|------|---------|--------|
| `internal/db/models.go` | LLMTarget 新增 `SupportedModelsJSON`、`AutoModel` | F2, F3 |
| `internal/db/llmtarget_repo.go` | 新增 `Seed` 方法 | F1 |
| `internal/config/config.go` | LLMTarget 新增 `SupportedModels`、`AutoModel` | F2, F3 |
| `internal/lb/balancer.go` | Target struct 新增 `SupportedModels`、`AutoModel` | F2, F3 |
| `internal/proxy/sproxy.go` — 路由过滤 | `pickLLMTarget` 增加 `requestedModel` 参数；`weightedPickExcluding` 返回后单独做 model 过滤；新增 `matchModel`、`supportedModelsFromBalancer`、`autoModelFromBalancer` 方法查询 `sp.llmBalancer.Targets()` | F2, F3 |
| `internal/proxy/sproxy.go` — 重试 | `buildRetryTransport` 的 `PickNext` 闭包捕获 `requestedModel` 参数 | F2, F3 |
| `internal/proxy/sproxy.go` — 配置同步 | `syncConfigTargetsToDatabase` 改用 `Seed`；`loadAllTargets` 反序列化新字段；`SyncLLMTargets` 构建带新字段的 lb.Target | F1, F2, F3 |
| `internal/proxy/sproxy.go` — 模型提取与重写 | `serveProxy` 前移 extractModel；auto 模式下调用新增的 `rewriteModelInBody`；新增相关方法 | F3 |
| `internal/api/admin_llm_target_handler.go` | create/update 请求 struct 新增 `supported_models`、`auto_model` 字段 | F2, F3 |
| `cmd/sproxy/admin_llm_target.go` | 新增 `--supported-models`、`--auto-model` flag | F2, F3 |
| `config/sproxy.yaml.example` | target 示例新增 `supported_models`、`auto_model` | F2, F3 |

### 测试文件

| 文件 | 测试内容 |
|------|---------|
| `internal/db/llmtarget_repo_test.go` | Seed 方法：insert vs skip |
| `internal/proxy/sproxy_test.go` 或 `proxy_coverage_test.go` | matchModel 匹配逻辑；模型过滤集成；auto 模式重写 |
| `internal/api/admin_llm_target_handler_test.go` | create/update 包含新字段 |

---

## 配置示例

```yaml
llm:
  targets:
    # Anthropic Claude — 只支持 Claude 模型
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      weight: 2
      supported_models:
        - "claude-sonnet-4-*"
        - "claude-opus-4-*"
      auto_model: "claude-sonnet-4-20250514"

    # OpenAI — 只支持 GPT 模型
    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      weight: 1
      supported_models:
        - "gpt-4o"
        - "gpt-4o-mini"
      auto_model: "gpt-4o"

    # Ollama 本地 — 支持 Llama3（不配 supported_models = 支持所有）
    - url: "http://localhost:11434"
      api_key: "ollama"
      provider: "ollama"
      weight: 1
      model_mapping:
        "*": "llama3"
```

### 路由示例

| 用户请求 model | 候选 target（provider 过滤后） | 模型过滤后 | 最终选择 |
|---------------|------|------|------|
| `claude-sonnet-4-20250514` | Anthropic target | Anthropic target ✓ | Anthropic（weight=2 加权） |
| `gpt-4o` | OpenAI + Ollama | OpenAI ✓（匹配）Ollama ✗（空 supported_models=不过滤） | OpenAI 或 Ollama |
| `auto` | 所有 | 不过滤（auto 跳过模型过滤） | 加权随机 → 用该 target 的 auto_model 重写 |
| 不传 model | 所有 | 不过滤 | 模有行为不变 |
| `claude-haiku-3.5` | Anthropic target | Anthropic target ✗（不匹配 `claude-sonnet-4-*`） | fail-open → 回退到 Anthropic target |

---

## 完备测试设计

> 基于 AGENTS.md 测试规范、代码库中的测试模式（`setupXxxTest` + `t.Helper()` + `t.Cleanup` + `httptest.NewServer` + `:memory:` SQLite）

### T1: Unit Tests — `matchModel` 函数

**文件**: `internal/proxy/model_match_test.go`（新文件）

| 用例 | patterns | 输入 | 期望 |
|------|----------|------|------|
| 精确匹配 | `["claude-sonnet-4-20250514"]` | `"claude-sonnet-4-20250514"` | `true` |
| 前缀通配 | `["claude-sonnet-4-*"]` | `"claude-sonnet-4-20250514"` | `true` |
| 全通配 | `["*"]` | `"anything"` | `true` |
| 不匹配 | `["claude-sonnet-4-*"]` | `"gpt-4o"` | `false` |
| 前缀不匹配 | `["claude-sonnet-4-*"]` | `"claude-opus-4-20250514"` | `false` |
| 空 patterns | `[]` | `"claude-sonnet-4"` | `false` |
| nil patterns | `nil` | `"claude-sonnet-4"` | `false` |
| 多模式，第一个命中 | `["claude-opus-*", "claude-sonnet-*"]` | `"claude-sonnet-4"` | `true` |

测试函数：`TestMatchModel_ExactMatch`、`TestMatchModel_PrefixWildcard`、`TestMatchModel_FullWildcard`、`TestMatchModel_NoMatch`、`TestMatchModel_EmptyPatterns`、`TestMatchModel_NilPatterns`、`TestMatchModel_MultiplePatterns_FirstWins`

### T2: Unit Tests — `rewriteModelInBody` 函数

**文件**: `internal/proxy/model_match_test.go`（同文件）

| 用例 | body | 期望结果 |
|------|------|------|
| 正常替换 | `{"model":"auto","messages":[]}` | `{"model":"claude-sonnet-4"}` 且 messages 保留 |
| model 非 auto | `{"model":"gpt-4o"}` | 不替换，原样返回 |
| 无效 JSON | `not json` | 原样返回 |
| 空 body | `[]byte{}` | 原样返回 |

测试函数：`TestRewriteModelInBody_Success`、`TestRewriteModelInBody_NotAuto`、`TestRewriteModelInBody_InvalidJSON`、`TestRewriteModelInBody_EmptyBody`

### T3: Unit Tests — Seed（F1）

**文件**: `internal/db/llmtarget_repo_test.go`（追加）

| 用例 | 初始状态 | 操作 | 期望 |
|------|----------|------|------|
| DB 空，首次插入 | DB 空 | Seed(anthropic target) | 插入成功，URL 存在 |
| URL 已存在，跳过 | DB 已有 URL=`https://api.anthropic.com` weight=2 | Seed(same URL, weight=1) | 跳过，weight 保持 2（不被覆盖） |
| WebUI 改过字段 | DB 已有 target，weight=1, supported_models=["gpt-4o"] | Seed(same URL, supported_models=[]) | 跳过，supported_models 保持 ["gpt-4o"] |

测试函数：`TestLLMTargetRepo_Seed_InsertNew`、`TestLLMTargetRepo_Seed_SkipExisting`、`TestLLMTargetRepo_Seed_SkipExistingWithWebUIModifications`

### T4: Integration Tests — 模型过滤路由

**文件**: `internal/proxy/model_routing_test.go`（新文件）

使用 helper 函数 `setupModelRoutingTest(t, targets []lb.Target) (*SProxy, func())`：
- 创建内存 DB
- 初始化 LLMTargetRepo + lb.WeightedRandomBalancer + SProxy
- 返回 SProxy 实例和清理函数，通过 `t.Cleanup()` 注册

| 用例 | targets 配置 | 请求 model | 期望路由 |
|------|------------|------|------|
| 精确匹配路由 | A: supported=["claude-*"], B: supported=["gpt-4o"] | `claude-sonnet-4` | 路由到 A（支持）；B 被过滤 |
| 不匹配 fail-open | A: supported=["claude-*"], B: supported=["gpt-4o"] | `llama3` | A/B 都不支持，fail-open 返回首选 A |
| 未配置不过滤 | A: supported=["claude-*"], B: supported=[] (空) | `gpt-4o` | B 未配置过滤，可能被选中；或 A 被选中后 fail-open |
| auto 跳过过滤 | A: supported=["claude-*"], B: supported=["gpt-4o"] | `auto` | 不过滤 supported_models，加权随机选择 |
| 空 model 不过滤 | A: supported=["claude-*"], B: supported=["gpt-4o"] | `""` | 不过滤 |
| 全部未配置 | A: supported=[], B: supported=[] | `claude-sonnet-4` | 不过滤（两个都空） |
| 绑定用户忽略模型 | A: supported=["claude-*"], binding=user→A | `gpt-4o` | 强制路由到 A（绑定优先，不过滤） |

测试函数：`TestModelRouting_ExactMatch_RoutesToCorrectTarget`、`TestModelRouting_NoMatch_FailOpen`、`TestModelRouting_UnconfiguredTarget_NoFilter`、`TestModelRouting_AutoMode_SkipsFilter`、`TestModelRouting_EmptyModel_NoFilter`、`TestModelRouting_MultipleMatchingTargets_Weighted`、`TestModelRouting_AllTargetsUnconfigured_NoFilter`、`TestModelRouting_BoundUser_IgnoresModelFilter`

### T5: Integration Tests — Auto 模式

**文件**: `internal/proxy/model_routing_test.go`（同文件）

**重点**：使用 `httptest.NewServer` 作为 mock backend，验证实际转发的 body。

| 用例 | target 配置 | 请求 model | 期望行为 | 验证方式 |
|------|------------|------|------|--------|
| auto_model 配置 | auto_model="claude-sonnet-4-20250514" | `auto` | body 中 model 被替换为 `claude-sonnet-4-20250514` | mock backend 捕获 req.Body，验证 `model` 字段值 |
| auto_model 空，降级到 supported[0] | auto_model="", supported=["gpt-4o","gpt-4o-mini"] | `auto` | body 中 model 被替换为 `gpt-4o` | 同上 |
| 都空，透传 | auto_model="", supported=[] | `auto` | body 中 model 保持 `"auto"` | 同上 |
| 非 auto | 任意 | `claude-sonnet-4` | body 不被修改，model 保持原值 | 同上 |
| auto 与 model_mapping 交互 | auto_model="claude-sonnet-4", model_mapping={"claude-sonnet-4":"llama3"} | `auto` | body 中 model 先重写为 claude-sonnet-4，再由协议转换映射为 llama3 | mock backend 验证收到 llama3 |

测试函数：`TestAutoMode_AutoModelUsed`、`TestAutoModel_FallbackToFirstSupportedModel`、`TestAutoModel_FallbackToPassthrough`、`TestAutoModel_NonAutoModel_NotRewritten`、`TestAutoMode_WithModelMapping`

### T6: Retry Tests — 重试路径一致性

**文件**: `internal/proxy/model_routing_test.go`

验证 retry 时 requestedModel 传递的正确性。

| 用例 | 首次请求 model | 首选 target | 首选 target 模拟失败 | 重试路由 |
|------|---------|----------|----------|----------|
| 重试保持过滤 | `claude-sonnet-4` | A (支持 claude-*) | 模拟超时 | 重试时仍用 `claude-sonnet-4` 过滤，不会路由到 B (只支持 gpt-4o) |
| 重试 auto 一致 | `auto` | A (auto_model=claude-sonnet-4) | 模拟超时 | 重试时用同一 target 的 auto 值 |

测试函数：`TestRetry_ModelFilteringConsistency`、`TestRetry_AutoModeConsistency`

### T7: API Handler Tests — 新字段

**文件**: `internal/api/admin_llm_target_handler_test.go`（追加）

| 用例 | 操作 | 验证 |
|------|------|------|
| 创建带 supported_models | POST {"supported_models":["gpt-4o"]} | DB 中 supported_models_json=`["gpt-4o"]` |
| 创建带 auto_model | POST {"auto_model":"gpt-4o"} | DB 中 auto_model=`gpt-4o` |
| 更新 supported_models | PUT {"supported_models":["gpt-4o","gpt-4o-mini"]} | DB 更新成功 |
| 更新 auto_model | PUT {"auto_model":"claude-sonnet-4"} | DB 更新成功 |
| PUT 传 nil，字段不变 | PUT {} (不含新字段) | DB 中字段不变 |

测试函数：`TestCreateTarget_WithSupportedModels`、`TestCreateTarget_WithAutoModel`、`TestUpdateTarget_UpdateSupportedModels`、`TestUpdateTarget_UpdateAutoModel`、`TestUpdateTarget_NilFieldsNotChanged`

### T8: End-to-End Test — 完整流程

**文件**: `internal/proxy/model_routing_test.go`

```
TestE2E_ModelAwareRouting_FullFlow
```

1. 配置 Anthropic target（supported=["claude-*"]）+ OpenAI target（supported=["gpt-*"]）
2. 启动两个 `httptest.NewServer` 作为后端 mock
3. 创建 SProxy，连接到 mock 后端
4. 发送 `POST /v1/messages` 带 `{"model":"claude-sonnet-4","messages":[...]}`
5. 验证：
   - Anthropic mock 服务器**收到**该请求
   - OpenAI mock 服务器**未收到**请求

```
TestE2E_AutoMode_FullFlow
```

1. 配置 Anthropic target（auto_model="claude-sonnet-4-20250514"）
2. 启动 mock 后端
3. 发送 `POST /v1/messages` 带 `{"model":"auto","messages":[...]}`
4. 验证：Anthropic mock 收到的 body 中 model 字段为 `"claude-sonnet-4-20250514"`（被重写）

```
TestE2E_SeedThenWebUIUpdate_PreservesChanges
```

1. DB 空，调用 `Seed(target_A)` → 插入成功
2. 通过 API Update 修改 target_A 的 supported_models 为 `["gpt-*"]`
3. 调用 `SyncLLMTargets` 更新 balancer
4. 发送 `POST /v1/messages` 带 `model: "gpt-4o"`
5. 验证：target_A 被选中（WebUI 修改的 supported_models 生效）

### 测试执行策略

```bash
# 单元测试（快速）
go test -v ./internal/proxy/ -run "TestMatchModel|TestRewriteModelInBody"
go test -v ./internal/db/ -run "TestLLMTargetRepo_Seed"

# 集成测试
go test -v ./internal/proxy/ -run "TestModelRouting|TestAutoMode|TestRetry|TestE2E"
go test -v ./internal/api/ -run "TestCreateTarget_With|TestUpdateTarget_"

# 单元 + 集成一起
go test -v ./internal/proxy/ ./internal/db/ ./internal/api/ -run "Model|Auto|Seed"
```

### 测试文件清单（完整）

| 文件 | 新增/追加 | 测试数 | 用途 |
|------|---------|-------|------|
| `internal/proxy/model_match_test.go` | 新增 | 11（T1+T2） | 纯函数单元测试 |
| `internal/db/llmtarget_repo_test.go` | 追加 | 3（T3） | F1 功能验证 |
| `internal/proxy/model_routing_test.go` | 新增 | 15（T4+T5+T6+T8） | F2/F3 集成测试 + E2E |
| `internal/api/admin_llm_target_handler_test.go` | 追加 | 5（T7） | API 新字段验证 |
| **总计** | | **34 个测试用例** | |

---

## 专家 Review 发现与改进

> 基于 codebase 深度审查（源码 + 设计文档交叉比对）

共发现并修正 **10 个问题**，以下逐一说明改进方案。

### 🔴 关键架构问题 P1：`sp.targets` 与 WebUI 热更新

**问题**：设计初稿中提出的 "sp.targets 并发安全问题" 认识有误。实际代码中 `sp.targets` 是启动时一次性构建的静态字段，`SyncLLMTargets` 不会修改它。这导致：**WebUI 修改 `supported_models` 和 `auto_model` 后调用 `SyncLLMTargets`，路由过滤**不会立即生效**，必须重启服务。

**改进方案**：已在上文"关键架构决策 > 问题 1"中详细说明。核心是改为让 `supportedModelsFromBalancer()`、`autoModelFromBalancer()` 等查询方法从 `sp.llmBalancer.Targets()` 读取（已原子更新），而非查 `sp.targets`。

---

### 🔴 F2 实现问题 P2：fail-open 两级候选集

**问题**：初稿说在 filter 闭包内处理 model 过滤，这会导致 fail-open 时无法独立回退。

**改进方案**：已在上文"关键架构决策 > 问题 2"中说明，改为在 `weightedPickExcluding` 返回后单独处理 model 过滤的两级候选集。

---

### 🔴 重试路径问题 P3：`buildRetryTransport` 未传 `requestedModel`

**问题**：`buildRetryTransport` 的 `PickNext` 闭包在重试时没有捕获并传入 `requestedModel`，导致重试时模型过滤失效。

**改进方案**：已在上文 F2 小节"重试路径"中给出明确的代码修复方案。`serveProxy` 中提取 `requestedModel` 后需要传入 `buildRetryTransport`，供 `PickNext` 闭包捕获使用。

---

### 🟡 初稿错误 P4：`supportedModelsForURL(t.ID)` 参数名混淆

**问题**：初稿中提到这是一个 bug，实际是误会。

**真相**：`SyncLLMTargets` 中明确赋值 `lb.Target.ID = t.URL`（代码第 557 行），所以 `t.ID` 的值就是 URL 字符串。而 `supportedModelsFromBalancer()` 的参数用 `t.ID` 来查询也是对的。不是 bug，仅需在代码中加注释说明 `t.ID` 被赋值为 URL。

---

### 🟡 P5：F1 的 IsEditable 问题

**问题**：初稿提出"Seed 时如果 URL 已存在，将 `IsEditable` 更新为 `true`"这样的解决方案。但 Seed 的设计语义就是"已存在则跳过"，如果还要更新字段就不是纯粹的 Seed 了。

**改进方案**：需要在首次通过配置文件播种时，就将 `IsEditable` 设为 `true`（F1 的目标是让 WebUI 能修改 config-sourced target），或者在 API handler 中对 `supported_models` 和 `auto_model` 这两个新字段做 partial editability（绕过 IsEditable 检查）。设计文档中已标记为"需在实现时明确选择"。

---

### 🟡 P6：P3 的触发条件描述不现实

**问题**：初稿说"如果 WebUI 修改了 Source 字段"。但实际 `handleUpdateTarget` 对 config-sourced target 直接返回 403（因为 `IsEditable=false`），WebUI 根本无法修改任何字段。

**改进方案**：文档已改为基于现实的架构决策（"需要让 config-sourced target 的新字段可编辑"），而非虚假的触发路径。

---

### 🟢 P7：通配符语义需明确

**问题**：初稿的 `matchModel` 只支持前缀通配（`claude-*`），不支持中间通配或 `?` 模式。未清楚说明限制。

**改进方案**：文档已补充注释说明模式支持的精确语义（精确匹配、前缀通配、全通配），避免日后用户误会。

---

### 🟢 P8：测试参照函数错误

**问题**：初稿引用了不存在的 `newCProxyWithBalancer` 函数。

**改进方案**：已改为引用实际存在的 `newSProxyWithBalancer` 函数，并补充了 helper 使用模式的说明。

---

### 🟢 P9：Auto 模式 E2E 测试缺少 mock backend

**问题**：初稿的 T5 auto 模式测试没有明确说明需要用 `httptest.NewServer` 捕获转发后的实际 body。

**改进方案**：文档已补充说明 T5 的集成测试必须用 mock backend 验证真正转发的 body 内容，而 T2 的单元测试只验证函数逻辑。

---

### 🟢 P10：缺少重试场景测试

**问题**：初稿没有明确的重试测试用例。

**改进方案**：已补充 T6 小节，新增 `TestRetry_ModelFilteringConsistency` 和 `TestRetry_AutoModeConsistency` 测试用例，验证重试时 requestedModel 传递的正确性。

---

## 设计文档改进总结

本文档是对初稿的全面 review 和改进，主要调整如下：

### 架构层面

1. **重新定位 `sp.targets` 的角色**：从一个需要原子保护的并发数据结构，改为正确理解为启动时的静态字段。真正的热更新需求通过 `sp.llmBalancer.Targets()` 实现（已有并发保护）。

2. **两级候选集的实现**：改为在 `weightedPickExcluding` 返回后单独处理模型过滤，而非在 filter 闭包内混合，确保 fail-open 策略的清晰性和正确性。

3. **重试路径的完整性**：明确 `buildRetryTransport` 需要捕获并传递 `requestedModel`，保证重试时的过滤一致性。

### 功能设计

1. **F1（Config-as-Seed）**：保持原有设计，但强调了 IsEditable 字段的需要决策（首次创建就设为 true，或做 partial editability）。

2. **F2（Per-Target Supported Models）**：改为从 `sp.llmBalancer.Targets()` 查询 `SupportedModels`，支持 WebUI 热更新。

3. **F3（Auto Mode）**：改为从 `sp.llmBalancer.Targets()` 查询 `AutoModel`，支持热更新，并明确了与 `model_mapping` 的执行顺序（auto 重写 → 协议转换）。

### 测试设计

1. **删除了不现实的并发测试**：初稿的 T7（Race Condition Tests）基于错误的并发假设，已删除。实际的并发访问发生在 `sp.llmBalancer` 层，已有原生保护。

2. **新增了重试测试**（T6）：覆盖重试时 requestedModel 传递的一致性。

3. **强化了 E2E 测试**：T5 auto 模式集成测试和 T8 E2E 测试都明确了使用 `httptest.NewServer` 作为 mock backend 的要求。

4. **测试总数从 32 个调整为 34 个**：新增 2 个重试相关测试，删除了 1 个并发测试，微调了分类。

### 代码改动清单更新

改动文件清单已完全重写，改为按功能分类而非按文件分类，更清楚地指出了关键的架构改动：

- `lb.Target` 新增 `SupportedModels` 和 `AutoModel` 字段
- 新增 `supportedModelsFromBalancer()` 和 `autoModelFromBalancer()` 查询方法，改查 `sp.llmBalancer` 而非 `sp.targets`
- `buildRetryTransport` 需要捕获 `requestedModel` 参数

### 改进的关键认识

1. **并发模型**：`sp.targets` 是静态的，真正的热更新通过 `sp.llmBalancer.UpdateTargets()` 的原子更新实现。

2. **查询路径**：路由时应从 `sp.llmBalancer.Targets()` 读取动态数据（`SupportedModels`, `AutoModel`），确保 WebUI 修改立即生效。

3. **两级过滤**：provider 过滤和 model 过滤逻辑清晰分离，fail-open 策略独立于闭包外，容易理解和维护。
