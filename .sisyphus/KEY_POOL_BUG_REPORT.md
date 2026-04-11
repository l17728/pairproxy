# 号池功能 Bug 诊断报告

> Issue: https://github.com/l17728/pairproxy/issues/2
> 日期: 2026-04-10
> 更新: 2026-04-11（代码核查后修订）
> 状态: 修复中

## 背景

用户反映通过多 key 配置到不同 group 形成号池的功能仍然有错误。Issue #2 原始问题是 API Key 按 provider 管理（系统只能有 3 个 key），已通过 `(URL, APIKeyID)` 复合唯一键修复。但号池功能（多 key 动态分配给不同 group）仍存在多个 bug。

**注意：Issue #2 关注的是"同一 provider 只能有 3 个 key"的问题，该问题本身已修复。但号池的完整功能依赖下述 bug 的修复，否则多 key 仍无法正确工作。**

## 架构概览

生产环境中有两条 API Key 路径：

| 路径 | 条件 | 机制 | 是否生效 |
|---|---|---|---|
| **路径 B: 动态 Key（号池）** | `cfg.Admin.KeyEncryptionKey != ""` | `SetAPIKeyResolver` → `APIKeyRepo.FindForUser` → `auth.Decrypt` → 真实 AES 解密 | 仅当配置了 `admin.key_encryption_key` |
| **路径 A: 静态 Target Key** | 兜底路径 | `LLMTarget.APIKey` → `obfuscateKey` 混淆存储 | 始终作为 fallback |

路径 B 的接线点在 `cmd/sproxy/main.go:576-601`：

```go
if cfg.Admin.KeyEncryptionKey != "" {
    apiKeyRepo := db.NewAPIKeyRepo(database, logger)
    sp.SetAPIKeyResolver(func(userID string) (string, bool) {
        user, _ := userRepo.GetByID(userID)
        groupID := ""
        if user.GroupID != nil { groupID = *user.GroupID }
        key, _ := apiKeyRepo.FindForUser(userID, groupID)
        plain, _ := decryptFn(key.EncryptedValue)
        return plain, true
    })
}
```

路径 A 的存储链路：`obfuscateKey(原始key)` → 写入 `api_keys.encrypted_value` → 读取时 `obfuscateKey(DB值)` = 原始 key（对称性保证）。

---

## Bug 列表

### BUG-1（P0 严重）：启动路径用 URL 做 ID — 同 URL 多 key 场景完全失效

**位置**: `cmd/sproxy/main.go:391-403`（启动构建）vs `internal/proxy/sproxy.go:574-617`（SyncLLMTargets）

```go
// main.go — 启动路径（有问题）
lbLLMTargets = append(lbLLMTargets, lb.Target{
    ID:   t.URL,   // ← ID = URL！同 URL 不同 key 的两个 target 有相同 ID
    Addr: t.URL,
    ...
})
if t.APIKey != "" {
    credentials[t.URL] = lb.TargetCredential{  // ← URL 做 key，后者覆盖前者
        APIKey:   t.APIKey,
        Provider: t.Provider,
    }
}

// sproxy.go — SyncLLMTargets 路径（正确）
targetID := t.ID          // ← 使用 DB UUID
if targetID == "" {
    targetID = t.URL      // config-sourced targets 的防御性回退
}
credentials[targetID] = lb.TargetCredential{...}  // ← UUID 做 key，不会覆盖
```

**问题**：启动路径完全没有 UUID 回退逻辑，强制使用 URL 作为 ID：
1. `credentials[t.URL]` 被后续 target 覆盖，同 URL 多 key 只保留最后一个
2. `lbLLMTargets` 的 `ID` = URL，两个不同 DB target 变成同一个 balancer ID
3. `llmTargetInfoForID` 在 `sp.targets`（以 UUID 为 key）中查不到 URL 格式的 ID，返回无 API Key 的 fallback
4. 健康检查用 URL 查凭证，永远只拿到最后一个 key

**影响时间窗口**：从启动到首次 `SyncLLMTargets` 完成（默认 sync 间隔决定，可能是几十秒）。对高并发场景不可忽视。首次 Sync 后 balancer 中 ID 变为 UUID，问题自愈——但 `credentials` 也会被 `SyncLLMTargets` 用 `targetID`（UUID）重建，届时两边一致。

**修复方案**: 启动路径改用 `t.ID`（UUID）做 `lb.Target.ID` 和 `credentials` map 的 key，并加入与 SyncLLMTargets 一致的空 ID 防御逻辑：

```go
targetID := t.ID
if targetID == "" {
    targetID = t.URL
}
lbLLMTargets = append(lbLLMTargets, lb.Target{
    ID:   targetID,
    Addr: t.URL,
    ...
})
if t.APIKey != "" {
    credentials[targetID] = lb.TargetCredential{...}
}
```

---

### BUG-2（P1 中等）：`apiKeyResolver` 只接收 `userID` — 每请求额外 DB 查询且 groupID 可能过期

**位置**: `internal/proxy/sproxy.go:86`（字段声明）、`sproxy.go:177`（接口定义）、`sproxy.go:1787`（调用点）、`cmd/sproxy/main.go:576`（注册 lambda）

```go
// sproxy.go:86
apiKeyResolver func(userID string) (apiKey string, found bool)

// sproxy.go:1787 — 调用点
if sp.apiKeyResolver != nil {
    if k, ok := sp.apiKeyResolver(claims.UserID); ok {  // ← 没有传 claims.GroupID
```

```go
// main.go:578 — resolver 实现
sp.SetAPIKeyResolver(func(userID string) (string, bool) {
    user, err := userRepo.GetByID(userID)  // ← 每个请求都查一次 DB！
    if err != nil || user == nil { return "", false }
    groupID := ""
    if user.GroupID != nil { groupID = *user.GroupID }
    ...
})
```

**问题**：
1. **性能**：`claims` 中已有 `GroupID`，但没有传给 resolver，导致每个代理请求都调用 `userRepo.GetByID`，增加一次额外 DB 查询
2. **正确性风险**：JWT 颁发后 GroupID 已固定在 token 中，resolver 重新查 DB 可能读到比 JWT 更新的 GroupID，导致 key 选择与 JWT 声明的 group 不一致（token 未过期但行为已改变）

**需要修改的三处**：

```go
// 1. sproxy.go:86 — 字段签名
apiKeyResolver func(userID, groupID string) (apiKey string, found bool)

// 2. sproxy.go:177 — SetAPIKeyResolver
func (sp *SProxy) SetAPIKeyResolver(fn func(userID, groupID string) (string, bool)) {

// 3. sproxy.go:1787 — 调用点
if k, ok := sp.apiKeyResolver(claims.UserID, claims.GroupID); ok {

// 4. main.go:576 — lambda 实现（消除 DB 查询）
sp.SetAPIKeyResolver(func(userID, groupID string) (string, bool) {
    key, err := apiKeyRepo.FindForUser(userID, groupID)
    ...
})
```

---

### BUG-3（P2 中等）：`findByAssignment` 只返回第一个 — 号池无法 key 轮换

**位置**: `internal/db/apikey_repo.go:204`

```go
func (r *APIKeyRepo) findByAssignment(id string, isUser bool) (*APIKey, error) {
    var assign APIKeyAssignment
    q := r.db
    if isUser {
        q = q.Where("user_id = ?", id)
    } else {
        q = q.Where("group_id = ?", id)
    }
    err := q.First(&assign).Error  // ← 只取第一条记录（ROWID 最小那条）
```

**背景说明**：此 bug 处于"用户/分组 → key 分配"层，与 BUG-1 的"负载均衡 target 层"是两个不同层次：
- **BUG-1**（target 层）：多个 target 共享同一 URL，每个 target 有独立 key，由 balancer 选择 target
- **BUG-3**（分配层）：一个用户/分组被分配了多个 API key，应在这些 key 之间轮换

**问题**：即使一个 group 被分配了多个 API key，`FindForUser` 永远只返回第一条分配记录（ROWID 最小）。号池场景下所有请求始终使用同一个 key，其余 key 闲置。

**修复方案**：新增 `FindAllForGroup(groupID string) ([]APIKey, error)` 方法，在 resolver 或上层实现轮换策略（随机、轮询或加权）：

```go
func (r *APIKeyRepo) FindAllForGroup(groupID string) ([]APIKey, error) {
    var assigns []APIKeyAssignment
    if err := r.db.Where("group_id = ?", groupID).Find(&assigns).Error; err != nil {
        return nil, err
    }
    ids := make([]string, 0, len(assigns))
    for _, a := range assigns {
        ids = append(ids, a.APIKeyID)
    }
    var keys []APIKey
    if err := r.db.Where("id IN ? AND is_active = ?", ids, true).Find(&keys).Error; err != nil {
        return nil, err
    }
    return keys, nil
}
```

---

### BUG-4（P1 严重，原 P3 低估）：`obfuscateKey` 路径与 `auth.Encrypt` 路径存储格式不一致

**位置**: `internal/proxy/sproxy.go:259`（config sync 写入）vs `sproxy.go:486`（loadAllTargets 读取）

**两条写入路径**：

```
路径 A（config target）: obfuscateKey(原始key) → api_keys.encrypted_value
路径 B（Admin API key）: auth.Encrypt(原始key, kek) → api_keys.encrypted_value
```

**读取路径（统一）**：
```go
// sproxy.go:486 — resolveAPIKey 中统一用 obfuscateKey 解读
return obfuscateKey(apiKey.EncryptedValue), nil  // ← 对 AES 密文执行 obfuscate 得到乱码
```

**为何影响严重（非"有限"）**：

当 Admin API 创建了 key（AES 加密），并且 `loadAllTargets` 调用 `resolveAPIKey` 时：
```
obfuscateKey(AES密文) → 乱码key → 写入 LLMTarget.APIKey → 静态路径发给上游 → 403
```

这会导致所有通过 Admin API 管理的 target 在静态路径下（`apiKeyResolver` 返回 false 时的 fallback）发出乱码 key，请求被上游拒绝。

**修复方案**：统一 config sync 路径也使用 `auth.Encrypt/Decrypt`（需要 `KeyEncryptionKey` 存在时），或通过新增标记列（`encryption_type ENUM('obfuscate','aes')`）区分存储格式，读取时按标记选择解密方式。

**推荐方案**：当 `KeyEncryptionKey` 配置存在时，config sync 路径也改用 `auth.Encrypt`；如果 `KeyEncryptionKey` 未配置，则继续用 `obfuscateKey`（向后兼容）。

---

## ~~BUG-5~~（已并入 BUG-1）

原 BUG-5（启动路径 `lb.Target.ID` 与 `SyncLLMTargets` 不一致）与 BUG-1 是同一根因，修复 BUG-1 即可解决。

---

## Model-Aware Routing Review 补充

以下为并行 code review 中发现的 Model-Aware Routing 相关问题（与号池 bug 无关，但值得记录）：

### MISSING-LOG-1: `weightedPickExcluding` 模型过滤成功时无日志

**位置**: `internal/proxy/sproxy.go` — `weightedPickExcluding` 函数

当模型过滤成功缩小候选集时（`modelFiltered` 有结果），没有 debug 日志记录过滤前后的数量变化。只有 fail-open 时有 WARN 日志。

**建议**: 在模型过滤成功后添加 Debug 日志：
```go
sp.logger.Debug("weightedPickExcluding: model filtering narrowed candidates",
    zap.Int("before", len(candidates)),
    zap.Int("after", len(modelFiltered)),
    zap.String("requested_model", requestedModel))
```

### MISSING-LOG-2: `syncConfigTargetsToDatabase` Seed 不区分 insert vs skip

**位置**: `internal/proxy/sproxy.go` — `syncConfigTargetsToDatabase` 函数

使用 `repo.Seed()` 后，日志不区分是新插入还是跳过已有记录。建议让 `Seed` 返回一个 bool 表示是否实际插入了新记录。

### MISSING-TEST-1: Bound user + model filter 交互测试

**缺失**: 没有测试验证 binding resolver 返回的 target 不受模型过滤影响。当用户绑定了特定 target 时，即使该 target 的 `SupportedModels` 不匹配请求的模型，仍应使用绑定 target。

### MISSING-TEST-2: Auto mode + model_mapping 交互测试

**缺失**: 没有测试验证 auto 模式重写后的模型名是否还会被 `model_mapping` 再次转换。理论上 auto → actual model → mapped model 是一个两步链路，需要验证不会出现双重重写。

### MISSING-TEST-3: API Handler 新字段测试

**缺失**: `internal/api/admin_llm_target_handler_test.go` 中没有测试 `supported_models` 和 `auto_model` 字段的 create/update 操作。

---

## 修复优先级总结

| 优先级 | Bug | 修复方案 | 影响范围 |
|---|---|---|---|
| **P0** | BUG-1: 启动路径 URL 做 ID，credentials 覆盖 | `main.go` 改用 UUID 做 ID 和 credentials key，加空 ID 防御 | 启动窗口内号池完全无法工作，同 URL 多 key 无法区分 |
| **P1** | BUG-2: resolver 签名缺少 groupID | 改 `apiKeyResolver` 签名为 `func(userID, groupID string)`，消除 DB 查询 | 每请求一次额外 DB 查询 + groupID 可能不一致 |
| **P1** | BUG-4: obfuscate vs AES 路径不一致 | 统一为 `auth.Encrypt/Decrypt` 或按 `encryption_type` 列区分 | Admin API 创建的 key 在静态路径下发乱码，请求被上游 403 |
| **P2** | BUG-3: findByAssignment 只取第一个 | 新增 `FindAllForGroup` 返回多 key，实现轮换 | 号池 key 无法轮换，多 key 配置无实际意义 |
