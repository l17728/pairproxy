# Issue #2 修复总结：APIKey 管理按 provider 唯一化导致号池共享失效

## 问题描述

系统号池共享功能希望支持多个 API Key 组成 Key 池，管理员按用户/分组分配。但当前 `resolveAPIKeyID` 函数在同步配置文件 targets 时，只按 `provider` 字段查找 APIKey 记录。由于 provider 只有 3 种值（anthropic/openai/ollama），导致：

- 整个系统同一 provider 最多只能存 1 个 config-sourced Key
- 同类型多供应商无法各自使用不同的 Key（如百炼和火山引擎都是 openai 兼容，但 Key 互相覆盖）
- 号池共享的核心设计被完全破坏

## 根因分析

```go
// 当前（错误）：只按 provider 查找，后续 key 会覆盖前一条的 encrypted_value
err := sp.db.Where("provider = ?", provider).First(&existingKey).Error
if err == nil {
    // 直接覆盖！这是 bug 所在
    sp.db.Model(&existingKey).Update("encrypted_value", obfuscated)
}
```

## 修复方案

### 1. `internal/proxy/sproxy.go` - 核心修复

**函数签名变更**：添加 `targetURL` 参数
```go
// 原来
func (sp *SProxy) resolveAPIKeyID(apiKey, provider string) (*string, error)

// 修复后
func (sp *SProxy) resolveAPIKeyID(apiKey, provider, targetURL string) (*string, error)
```

**查询逻辑改进**：按 `(provider, encrypted_value)` 唯一化
```go
// 原来：只按 provider，会导致同 provider 的 key 互相覆盖
err := sp.db.Where("provider = ?", provider).First(&existingKey).Error

// 修复后：按 (provider, encrypted_value)，相同 key 复用，不同 key 独立
err := sp.db.Where("provider = ? AND encrypted_value = ?", provider, obfuscated).First(&existingKey).Error
if err == nil {
    // 已存在相同 key 值的记录，直接复用（不覆盖）
    return &existingKey.ID, nil
}
```

**Name 字段改进**：从静态的 `"Auto-created for {provider}"` 改为动态的 `"Auto-{targetURL}"`，避免 uniqueIndex 冲突

**调用处修改**：
```go
// 原来
apiKeyID, err := sp.resolveAPIKeyID(ct.APIKey, ct.Provider)

// 修复后
apiKeyID, err := sp.resolveAPIKeyID(ct.APIKey, ct.Provider, ct.URL)
```

### 2. `internal/db/apikey_repo.go` - 新增 repo 方法

```go
// FindByProviderAndValue 按 (provider, encrypted_value) 查找 API Key
func (r *APIKeyRepo) FindByProviderAndValue(provider, encryptedValue string) (*APIKey, error) {
    var key APIKey
    err := r.db.Where("provider = ? AND encrypted_value = ?", provider, encryptedValue).First(&key).Error
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return nil, nil
        }
        return nil, fmt.Errorf("find api key by provider and value: %w", err)
    }
    return &key, nil
}
```

### 3. `internal/proxy/sproxy_sync_test.go` - 完整测试套件

新增 6 个测试用例，覆盖所有场景：

| 测试用例 | 目的 | 验证点 |
|---------|------|--------|
| `TestResolveAPIKeyID_SameProvider_DifferentKeys` | **复现 Issue #2** | 同 provider 两个不同 key 不互相覆盖，各自创建独立记录 |
| `TestResolveAPIKeyID_SameProvider_SameKey_Reuses` | 相同 key 复用 | 相同 key 值返回同一个 API Key ID，不重复创建 |
| `TestResolveAPIKeyID_DifferentProviders_Independent` | 回归测试 | 不同 provider 的 key 各自独立，互不影响 |
| `TestResolveAPIKeyID_EmptyKey_ReturnsNil` | 边界情况 | 空 key 返回 nil，不创建 DB 记录 |
| `TestSyncConfigTargets_MultipleOpenAI_DifferentKeys` | 完整流程 | 多个 openai targets 同步时各自创建独立 APIKey |
| `TestSyncConfigTargets_Idempotent_MultipleSync` | 幂等性 | 重复执行 sync，APIKey 记录数不增加 |

## 测试结果

```
=== RUN   TestResolveAPIKeyID_SameProvider_DifferentKeys
--- PASS: TestResolveAPIKeyID_SameProvider_DifferentKeys (0.02s)
=== RUN   TestResolveAPIKeyID_SameProvider_SameKey_Reuses
--- PASS: TestResolveAPIKeyID_SameProvider_SameKey_Reuses (0.01s)
=== RUN   TestResolveAPIKeyID_DifferentProviders_Independent
--- PASS: TestResolveAPIKeyID_DifferentProviders_Independent (0.00s)
=== RUN   TestResolveAPIKeyID_EmptyKey_ReturnsNil
--- PASS: TestResolveAPIKeyID_EmptyKey_ReturnsNil (0.01s)
=== RUN   TestSyncConfigTargets_MultipleOpenAI_DifferentKeys
--- PASS: TestSyncConfigTargets_MultipleOpenAI_DifferentKeys (0.01s)
=== RUN   TestSyncConfigTargets_Idempotent_MultipleSync
--- PASS: TestSyncConfigTargets_Idempotent_MultipleSync (0.01s)

✅ 全部测试通过
✅ 现有测试无回归（18 个 sync/resolve 相关测试全通）
✅ 构建成功（sproxy 和 cproxy）
```

## 号池共享功能保障

修复**不影响**号池共享的其他关键路径：

| 路径 | 状态 | 说明 |
|------|------|------|
| Admin 手动创建 APIKey | ✅ 不受影响 | 通过 `POST /api/admin/api-keys` 手动管理 |
| 号池分配（FindForUser）| ✅ 不受影响 | 按用户/分组分配，逻辑完全独立 |
| 运行时 Key 注入 | ✅ 不受影响 | apiKeyResolver 闭包解密并注入 |
| 数据库 Schema | ✅ 无需迁移 | 现有 DB 记录自动过渡 |

## 向后兼容性

- 现有数据库中的 `"Auto-created for {provider}"` 记录会自动过渡为孤立记录（无任何 llm_targets 引用），无功能副作用
- 新配置文件重新 sync 会创建新的 `"Auto-{url}"` 格式记录，旧孤立记录可定期手动清理
- 旧新配置文件可混合运行

## 修改统计

```
 internal/db/apikey_repo.go         |  15 ++
 internal/proxy/sproxy.go           |  20 +--
 internal/proxy/sproxy_sync_test.go | 277 ++++++++++++++++++++++++++++++++
 3 files changed, 303 insertions(+), 9 deletions(-)
```

## Commit

- Commit: `32ce7ae`
- Message: `fix(apikey): support multiple API keys per provider for key pool sharing`
- Fixes: Issue #2

---

## 使用示例：百炼 + 火山引擎

修复后，配置文件可以这样写：

```yaml
llm:
  targets:
    # 百炼 - OpenAI 兼容
    - url: https://dashscope.aliyuncs.com/api/v1
      api_key: ${BAILIAN_API_KEY}
      provider: openai
      name: "Alibaba Bailian"
      weight: 50
    
    # 火山引擎 - OpenAI 兼容
    - url: https://ark.cn-beijing.volces.com/api/v1
      api_key: ${HUOSHAN_API_KEY}
      provider: openai
      name: "Volcano Huoshan"
      weight: 50
    
    # 安谱诺智
    - url: https://api.anthropic.com
      api_key: ${ANTHROPIC_API_KEY}
      provider: anthropic
      name: "Anthropic"
      weight: 30
```

现在系统会：
1. 为百炼创建 APIKey 记录 `Auto-https://dashscope.aliyuncs.com/api/v1`
2. 为火山创建 APIKey 记录 `Auto-https://ark.cn-beijing.volces.com/api/v1`
3. 为安谱诺智创建 APIKey 记录 `Auto-https://api.anthropic.com`
4. 三个 llm_targets 各自指向正确的 APIKey，**不再互相覆盖**
5. 号池共享可以为不同用户分配不同的 Key 来使用这些供应商
