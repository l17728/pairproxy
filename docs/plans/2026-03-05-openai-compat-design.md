# OpenAI API 兼容设计

**日期**: 2026-03-05
**版本**: v2.0.0 (F-1)
**状态**: Approved

---

## 目标

让 sproxy 同时支持 OpenAI 格式客户端（Cursor、Continue.dev、任何 OpenAI 兼容工具），与现有 Anthropic 客户端共享配额、审计、统计系统。

---

## 架构与数据流

### 当前流（cproxy 客户端）
```
Claude Code → cproxy(:8080) → sproxy(:9000) → Anthropic API
                X-PairProxy-Auth: <jwt>        Authorization: Bearer <api-key>
                POST /v1/messages              POST /v1/messages
```

### 新增流（OpenAI 格式客户端）
```
Cursor/Continue.dev → sproxy(:9000) → OpenAI API
  Authorization: Bearer <pairproxy-jwt>   Authorization: Bearer <openai-api-key>
  POST /v1/chat/completions               POST /v1/chat/completions
```

两条路径在 `AuthMiddleware` 之后完全合流，后续逻辑共用。

---

## 核心改动

### 1. 认证扩展 (`internal/proxy/middleware.go`)

**现状**: `AuthMiddleware` 只从 `X-PairProxy-Auth` 头提取 JWT。

**改动**: 扩展 token 提取逻辑，支持标准 `Authorization: Bearer` 头：

```go
token := r.Header.Get("X-PairProxy-Auth")
if token == "" {
    if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
        token = strings.TrimPrefix(auth, "Bearer ")
    }
}
```

**优先级**: `X-PairProxy-Auth` > `Authorization: Bearer`（向后兼容现有 cproxy）

**Director 清理**: 同时删除两个头，防止 PairProxy JWT 泄漏给上游 LLM：
```go
req.Header.Del("X-PairProxy-Auth")
req.Header.Del("Authorization") // Director 会重新 Set 真实 api-key
```

---

### 2. 流式 Token 计数 (`internal/proxy/sproxy.go` + 新文件 `openai_compat.go`)

**问题**: OpenAI SSE 流默认不携带 usage 数据，需客户端请求中包含：
```json
{"stream_options": {"include_usage": true}}
```
若客户端不设置，流式请求的 token 计数为 0 → 配额失效。

**解决方案**: sproxy 自动注入 `stream_options`，对客户端透明。

**注入位置**: 在 `serveProxy` 的 body 读取段（quota checker 已有的 body 读取逻辑）合并注入：

```go
// 已有的 body 读取（quota checker）
bodyBytes, _ := io.ReadAll(r.Body)
r.Body.Close()

// 新增：OpenAI 流式注入
bodyBytes = injectOpenAIStreamOptions(r.URL.Path, bodyBytes, sp.logger, reqID)

r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
r.ContentLength = int64(len(bodyBytes))
```

**函数签名**:
```go
// injectOpenAIStreamOptions 对 OpenAI 流式请求注入 stream_options.include_usage: true。
// 仅当 path 为 /v1/chat/completions 且 "stream": true 时生效。
// 注入失败时静默降级，返回原 body（不中断请求）。
func injectOpenAIStreamOptions(path string, body []byte, logger *zap.Logger, reqID string) []byte
```

**实现细节**:
- 使用 `json.RawMessage` 合并，不全量反序列化（保持原有字段顺序）
- 幂等：若 `stream_options.include_usage` 已为 `true`，跳过
- 降级：畸形 JSON / 空 body / 非流式请求 → 原样返回

---

### 3. 路由复用

**现状**: `preferredProvidersByPath` 已支持按路径返回期望 provider：
```go
case strings.HasPrefix(path, "/v1/chat/completions"):
    return map[string]bool{"openai": true}
case strings.HasPrefix(path, "/v1/messages"):
    return map[string]bool{"anthropic": true}
```

**无需改动**，现有逻辑已完备。

---

## 错误处理

### 认证失败场景

| 场景 | 响应 |
|---|---|
| 两个头均缺失 | 401，`missing_auth_header` |
| Bearer token 是无效/过期 PairProxy JWT | 401，`invalid_token` |
| Bearer token 是 OpenAI API Key（非 JWT）| 401，`invalid_token`（JWT parse 失败）|

错误格式保持现有 JSON 风格，不新增格式。

### stream_options 注入失败场景

| 场景 | 处理方式 |
|---|---|
| body 是畸形 JSON | 跳过注入，原样转发（降级，不中断请求）|
| `stream` 字段不为 `true` | 跳过注入（非流式请求不需要）|
| body 为空 | 跳过注入 |
| 注入后 body 变大 | 同步更新 `r.ContentLength` |

注入失败均**静默降级**，不中断请求——最坏结果是流式 token 计数为 0，而非请求报错。

### 与 quota checker 的交互

若 `quotaChecker == nil`（未配置配额），body 读取逻辑当前不执行。需要确保 stream_options 注入在 quota checker **不存在时也能独立触发**，即 body 读取段需改为：对 OpenAI 路径，无论是否有 quota checker 都执行 body 读取。

---

## 测试策略

### 单元测试（~15 个新增）

**1. `middleware_test.go` — `TestAuthMiddleware_BearerToken`**
- 有效 Bearer JWT → 通过
- 无效 Bearer token → 401
- 两个头都有时优先 `X-PairProxy-Auth`（向后兼容）

**2. `openai_compat_test.go` — `TestInjectOpenAIStreamOptions`**
- 非 OpenAI 路径（`/v1/messages`）→ 原样返回
- OpenAI 非流式（`"stream": false`）→ 原样返回
- OpenAI 流式无 `stream_options` → 注入 `{"include_usage": true}`
- 已有 `stream_options.include_usage: true` → 幂等跳过
- 已有 `stream_options.include_usage: false` → 覆盖为 `true`
- 畸形 JSON → 原样返回（降级）
- 空 body → 原样返回

**3. `sproxy_test.go` — `TestSProxyOpenAICompatE2E`**
- Mock OpenAI target，发送 `Authorization: Bearer <jwt>` + `/v1/chat/completions` 流式请求
- 验证：转发到 OpenAI target 的请求包含 `stream_options.include_usage: true`
- 验证：响应中 usage 被正确解析并记录到 DB

### 集成测试（可选）

`test/e2e/openai_compat_e2e_test.go` — 启动真实 sproxy + mock OpenAI server，用标准 OpenAI SDK 发请求，验证全链路。

### 手动测试清单

```bash
# 1. 配置 sproxy.yaml 添加 OpenAI target
# 2. 用 curl 模拟 OpenAI 客户端
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer <pairproxy-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"hi"}],"stream":true}'

# 3. 检查 sproxy 日志确认路由到 OpenAI target
# 4. 检查 usage_logs 表确认 token 计数非零
```

---

## 日志增强

### 新增日志点

**`AuthMiddleware`**:
```go
logger.Debug("JWT extracted from Authorization Bearer header",
    zap.String("request_id", reqID),
)
```

**`injectOpenAIStreamOptions`**:
```go
logger.Debug("injected stream_options for OpenAI streaming request",
    zap.String("request_id", reqID),
    zap.Int("original_size", len(body)),
    zap.Int("modified_size", len(modified)),
)

logger.Warn("failed to inject stream_options, forwarding original body",
    zap.String("request_id", reqID),
    zap.Error(err),
)
```

**`serveProxy`**:
```go
logger.Debug("OpenAI streaming request detected, injecting stream_options",
    zap.String("request_id", reqID),
    zap.String("path", r.URL.Path),
)
```

---

## 配置示例

**`config/sproxy.yaml.example`**:
```yaml
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"
      weight: 1

    - url: "https://api.openai.com"
      api_key: "${OPENAI_API_KEY}"
      provider: "openai"
      name: "OpenAI GPT"
      weight: 1
```

---

## 文档更新

**`docs/manual.md` 新增 §16**:
- 支持的客户端列表
- 配置步骤
- 认证方式说明（Bearer token = PairProxy JWT）
- 自动功能（路由、token 计数、配额共享）

**`CLAUDE.md` 更新**:
- 环境信息章节补充多 Provider 支持说明

---

## 向后兼容性

- 现有 cproxy 客户端完全不受影响（`X-PairProxy-Auth` 优先级更高）
- 未配置 OpenAI target 时，OpenAI 格式请求返回 `no_upstream`（现有行为）
- 配置文件中 `provider` 字段可选，默认 `anthropic`（现有行为）

---

## 实现顺序

1. `internal/proxy/openai_compat.go` + 测试（独立，无依赖）
2. `internal/proxy/middleware.go` 认证扩展 + 测试
3. `internal/proxy/sproxy.go` 集成注入逻辑
4. E2E 测试
5. 配置示例 + 文档更新
6. 手动测试验证

---

## 风险与缓解

| 风险 | 缓解措施 |
|---|---|
| stream_options 注入破坏原始请求 | 畸形 JSON 时静默降级；单元测试覆盖边界情况 |
| Bearer auth 与其他系统冲突 | `X-PairProxy-Auth` 优先级更高，现有客户端不受影响 |
| OpenAI 流式无 usage 导致配额失效 | 自动注入 + 日志记录注入失败情况 |

---

**设计完成，准备进入实现。**
