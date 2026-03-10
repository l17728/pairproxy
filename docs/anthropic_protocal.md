# Anthropic Messages API 协议标准（网关开发参考版）

> **参考来源（2025-03）**
> - [Messages API Reference](https://platform.claude.com/docs/en/api/messages)
> - [Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
> - [Errors](https://platform.claude.com/docs/en/api/errors)
> - [Rate Limits](https://platform.claude.com/docs/en/api/rate-limits)
> - [Prompt Caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
>
> **当前协议版本：** `2023-06-01`（通过 `anthropic-version` 请求头指定）

---

## 1. 端点

```
POST https://api.anthropic.com/v1/messages
```

---

## 2. 请求头

### 2.1 必填头

| Header | 示例值 | 说明 |
|--------|--------|------|
| `x-api-key` | `sk-ant-...` | Anthropic API 密钥 |
| `anthropic-version` | `2023-06-01` | API 版本，目前固定为此值，**必须携带** |
| `content-type` | `application/json` | 请求体格式 |

### 2.2 可选头

| Header | 示例值 | 说明 |
|--------|--------|------|
| `anthropic-beta` | `prompt-caching-2024-07-31` | 启用 Beta 功能，多个功能以逗号分隔 |

> **网关注意**：转发请求时需原样透传 `anthropic-beta`，不可丢弃。`x-api-key` 需替换为真实 API Key。

---

## 3. 请求体

### 3.1 必填参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `model` | string | 模型 ID，见 §10 |
| `max_tokens` | integer | 最大输出 token 数，必须 ≥ 1（**与 OpenAI 区别：此字段必填**） |
| `messages` | array | 对话历史，见 §3.3 |

### 3.2 可选参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `system` | string \| TextBlockParam[] | — | 系统提示，独立字段（不嵌入 messages） |
| `stream` | boolean | `false` | `true` 时以 SSE 格式流式输出 |
| `temperature` | float | `1.0` | 随机性，范围 `0.0`–`1.0` |
| `top_p` | float | — | Nucleus 采样阈值 |
| `top_k` | integer | — | 候选 token 数上限（Anthropic 特有参数） |
| `stop_sequences` | string[] | — | 自定义停止词，命中任意一个即停止 |
| `tools` | ToolUnion[] | — | 工具定义，见 §3.6 |
| `tool_choice` | ToolChoice | `auto` | 工具调用策略，见 §3.7 |
| `thinking` | ThinkingConfigParam | — | 扩展思考配置，见 §3.8 |
| `cache_control` | CacheControlEphemeral | — | 顶层自动缓存标记，见 §8 |
| `metadata` | object | — | `{"user_id": "string"}` 用于滥用检测 |
| `output_config` | object | — | `{"format": JSONOutputFormat, "effort": "low"\|"medium"\|"high"\|"max"}` |
| `service_tier` | string | `"auto"` | 服务等级：`"auto"` \| `"standard_only"` |
| `inference_geo` | string | — | 推理地理区域（数据驻留控制） |
| `container` | string | — | 代码执行容器 ID（复用会话） |

### 3.3 消息格式（messages 数组）

```json
{
  "role": "user",
  "content": "纯文本（简写）"
}
```

或多模态内容块数组：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "描述这张图片"},
    {
      "type": "image",
      "source": {
        "type": "url",
        "url": "https://example.com/image.jpg"
      }
    }
  ]
}
```

**消息规则（网关必须知晓）：**
- `role` 只能是 `"user"` 或 `"assistant"`，交替出现，**首条必须为 `"user"`**
- 连续相同 role 的消息会被 API **自动合并**为单条
- 若最后一条为 `"assistant"`，模型将从该内容**继续生成**（prefill）
- **Opus 4.6 不支持 prefill**，末尾为 assistant 消息时返回 400
- 单次请求最多 **100,000 条消息**

### 3.4 content 块类型（请求侧）

#### TextBlockParam
```json
{
  "type": "text",
  "text": "string",
  "cache_control": {"type": "ephemeral", "ttl": "5m"},
  "citations": []
}
```

#### ImageBlockParam
```json
{
  "type": "image",
  "source": {
    "type": "base64",
    "media_type": "image/jpeg",
    "data": "<base64>"
  },
  "cache_control": {"type": "ephemeral"}
}
```
```json
{
  "type": "image",
  "source": {
    "type": "url",
    "url": "https://..."
  }
}
```
支持格式：`image/jpeg`、`image/png`、`image/gif`、`image/webp`

#### DocumentBlockParam
```json
{
  "type": "document",
  "source": {
    "type": "base64",
    "media_type": "application/pdf",
    "data": "<base64>"
  },
  "title": "可选文档标题",
  "context": "可选背景描述",
  "citations": {"enabled": true},
  "cache_control": {"type": "ephemeral"}
}
```
`source.type` 支持：`"base64"`（PDF/纯文本）、`"text"`（纯文本）、`"url"`、`"content"`（内容块数组）

#### ToolUseBlockParam（多轮 tool 调用时回传）
```json
{
  "type": "tool_use",
  "id": "toolu_01abc",
  "name": "get_weather",
  "input": {"location": "Beijing"},
  "cache_control": {"type": "ephemeral"},
  "caller": {
    "type": "direct"
  }
}
```
`caller.type` 取值：`"direct"` \| `"code_execution_20250825"` \| `"code_execution_20260120"`

#### ToolResultBlockParam（tool 执行结果回传）
```json
{
  "type": "tool_result",
  "tool_use_id": "toolu_01abc",
  "content": "工具返回的文本",
  "is_error": false,
  "cache_control": {"type": "ephemeral"}
}
```
`content` 可以是 string 或 ContentBlock 数组。

### 3.5 工具定义（tools 数组）

#### 自定义工具
```json
{
  "name": "get_weather",
  "description": "获取指定城市的天气",
  "input_schema": {
    "type": "object",
    "properties": {
      "location": {
        "type": "string",
        "description": "城市名称"
      }
    },
    "required": ["location"]
  },
  "cache_control": {"type": "ephemeral"},
  "strict": false,
  "defer_loading": false,
  "allowed_callers": ["direct"]
}
```

#### 内置服务器工具

```json
{"type": "web_search_20260209", "name": "web_search",
 "allowed_domains": ["example.com"], "blocked_domains": [],
 "max_uses": 10,
 "user_location": {"type": "approximate", "city": "...", "country": "..."}}
```
```json
{"type": "web_fetch_20260209", "name": "web_fetch",
 "max_uses": 10, "max_content_tokens": 10000,
 "citations": {"enabled": true}}
```
```json
{"type": "code_execution_20260120", "name": "code_execution"}
```
```json
{"type": "text_editor_20250728", "name": "str_replace_based_edit_tool",
 "max_characters": 50000}
```
```json
{"type": "bash_20250124", "name": "bash"}
```

### 3.6 tool_choice

```json
{"type": "auto"}                     // 模型自主决定（默认）
{"type": "any"}                      // 必须调用至少一个工具
{"type": "none"}                     // 禁止调用工具
{"type": "tool", "name": "get_weather"}  // 强制调用指定工具
```

所有类型均支持可选字段：
```json
{"disable_parallel_tool_use": true}  // 禁止并行调用多个工具
```

### 3.7 thinking（扩展思考）

```json
{
  "type": "enabled",
  "budget_tokens": 10000
}
```
```json
{"type": "disabled"}
```
```json
{"type": "adaptive"}
```

**约束：**
- `budget_tokens` 必须 ≥ `1024`，且必须 < `max_tokens`
- 启用扩展思考时，`temperature` 固定为 `1`，`top_p`/`top_k` 不可用
- 启用时需禁用 HTTP WriteTimeout（响应可能非常长）

---

## 4. 响应体（非流式，HTTP 200）

```json
{
  "id": "msg_1abc23de4567890f",
  "type": "message",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "响应内容"}
  ],
  "model": "claude-opus-4-6",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 10,
    "output_tokens": 20,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens": 0
  },
  "container": {
    "id": "container_id",
    "expires_at": "2025-01-01T00:00:00Z"
  }
}
```

### 4.1 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 请求唯一 ID，格式 `msg_xxx` |
| `type` | string | 固定为 `"message"` |
| `role` | string | 固定为 `"assistant"` |
| `content` | array | 内容块数组，见 §4.2 |
| `model` | string | 实际使用的模型 ID |
| `stop_reason` | string | 停止原因，见 §4.3 |
| `stop_sequence` | string \| null | 命中的停止词，未命中为 `null` |
| `usage.input_tokens` | integer | **最后一个 cache 断点之后**的 token 数 |
| `usage.output_tokens` | integer | 输出 token 数 |
| `usage.cache_creation_input_tokens` | integer | 写入缓存的 token 数 |
| `usage.cache_read_input_tokens` | integer | 从缓存读取的 token 数 |
| `container` | object \| null | 代码执行容器信息（含 `id` 和 `expires_at`） |

> **重要**：`input_tokens` 不等于完整输入总量。完整输入公式：
> ```
> total_input = cache_read_input_tokens + cache_creation_input_tokens + input_tokens
> ```

### 4.2 响应 content 块类型

#### TextBlock
```json
{
  "type": "text",
  "text": "响应文本",
  "citations": [
    {
      "type": "char_location",
      "cited_text": "被引用的原文",
      "document_index": 0,
      "document_title": "文档名",
      "start_char_index": 0,
      "end_char_index": 50
    }
  ]
}
```

Citation type 取值：`char_location` / `page_location` / `content_block_location` / `web_search_result_location` / `search_result_location`

#### ToolUseBlock
```json
{
  "type": "tool_use",
  "id": "toolu_01abc",
  "name": "get_weather",
  "input": {"location": "Beijing"}
}
```

#### ThinkingBlock（扩展思考）
```json
{
  "type": "thinking",
  "thinking": "推理过程文本...",
  "signature": "完整性验证签名"
}
```

#### RedactedThinkingBlock（被隐去的思考内容）
```json
{
  "type": "redacted_thinking",
  "data": "加密数据字符串"
}
```
> **网关注意**：多轮对话中需将此块原样透传回下一轮请求，不可修改或丢弃。

### 4.3 stop_reason 取值

| 值 | 含义 |
|----|------|
| `"end_turn"` | 模型自然停止 |
| `"max_tokens"` | 达到 `max_tokens` 上限 |
| `"stop_sequence"` | 命中自定义停止词（`stop_sequence` 字段有值） |
| `"tool_use"` | 模型请求调用工具（`content` 包含 `tool_use` 块） |

---

## 5. 响应头

### 5.1 通用响应头

| Header | 说明 |
|--------|------|
| `request-id` | 全局唯一请求 ID（格式 `req_xxx`），客服排查时需提供 |
| `anthropic-organization-id` | 请求所属组织 ID |

### 5.2 限速状态头（每次响应均包含）

| Header | 说明 |
|--------|------|
| `retry-after` | 触发 429 时，需等待的秒数（必须遵守，更早重试仍会失败） |
| `anthropic-ratelimit-requests-limit` | 当前 RPM 上限 |
| `anthropic-ratelimit-requests-remaining` | RPM 剩余次数 |
| `anthropic-ratelimit-requests-reset` | RPM 重置时间（RFC 3339 格式） |
| `anthropic-ratelimit-input-tokens-limit` | ITPM 上限 |
| `anthropic-ratelimit-input-tokens-remaining` | ITPM 剩余（四舍五入到千位） |
| `anthropic-ratelimit-input-tokens-reset` | ITPM 重置时间 |
| `anthropic-ratelimit-output-tokens-limit` | OTPM 上限 |
| `anthropic-ratelimit-output-tokens-remaining` | OTPM 剩余（四舍五入到千位） |
| `anthropic-ratelimit-output-tokens-reset` | OTPM 重置时间 |
| `anthropic-ratelimit-tokens-limit` | 最严格限制下的综合 token 上限 |
| `anthropic-ratelimit-tokens-remaining` | 综合 token 剩余量 |
| `anthropic-ratelimit-tokens-reset` | 综合 token 重置时间 |

> **网关建议**：监控 `anthropic-ratelimit-*-remaining` 实现主动限流，在剩余量极低时提前排队，避免请求因 429 失败后需要重试。

---

## 6. SSE 流式协议（stream: true）

响应 `Content-Type: text/event-stream`，每行格式为：
```
event: <事件类型>
data: <JSON字符串>

```
（每个事件后有一个空行）

### 6.1 事件完整序列

```
message_start
  content_block_start   (index=0)
    content_block_delta × N
  content_block_stop    (index=0)
  content_block_start   (index=1)  ← 多个 block 时重复
    content_block_delta × N
  content_block_stop    (index=1)
  ...
message_delta
message_stop
（ping 事件可在任意位置出现）
```

### 6.2 各事件 JSON 结构

#### message_start
```json
event: message_start
data: {
  "type": "message_start",
  "message": {
    "id": "msg_xxx",
    "type": "message",
    "role": "assistant",
    "content": [],
    "model": "claude-opus-4-6",
    "stop_reason": null,
    "stop_sequence": null,
    "usage": {"input_tokens": 25, "output_tokens": 1}
  }
}
```
> `usage.output_tokens` 此时为 1（预占位），非最终值。

#### content_block_start
```json
event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}
```
tool_use 块：
```json
data: {"type": "content_block_start", "index": 1, "content_block": {"type": "tool_use", "id": "toolu_01xxx", "name": "get_weather", "input": {}}}
```

#### content_block_delta
文本增量：
```json
event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}
```
Tool 参数增量（partial JSON）：
```json
data: {"type": "content_block_delta", "index": 1, "delta": {"type": "input_json_delta", "partial_json": "{\"location\": \"San Fra"}}
```
扩展思考内容增量：
```json
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "Let me analyze..."}}
```
扩展思考签名（在 content_block_stop 之前发送）：
```json
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "signature_delta", "signature": "EqQBCgIYAhIM..."}}
```

#### content_block_stop
```json
event: content_block_stop
data: {"type": "content_block_stop", "index": 0}
```

#### message_delta
```json
event: message_delta
data: {
  "type": "message_delta",
  "delta": {
    "stop_reason": "end_turn",
    "stop_sequence": null
  },
  "usage": {"output_tokens": 15}
}
```
> `usage.output_tokens` 为**累计值**，此处为最终准确值。

#### message_stop
```json
event: message_stop
data: {"type": "message_stop"}
```

#### ping
```json
event: ping
data: {"type": "ping"}
```

#### error（流式中途错误）
```json
event: error
data: {"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}
```
> **网关注意**：流式响应返回 HTTP 200 后仍可能出现 `error` 事件，需在 SSE 解析层处理，不能只依赖 HTTP 状态码。

### 6.3 delta 类型汇总

| `delta.type` | 所属 block | 字段 | 说明 |
|---|---|---|---|
| `text_delta` | `text` | `text` | 普通文本增量 |
| `input_json_delta` | `tool_use` | `partial_json` | Tool 参数 partial JSON |
| `thinking_delta` | `thinking` | `thinking` | 扩展思考内容增量 |
| `signature_delta` | `thinking` | `signature` | 思考块完整性签名 |

### 6.4 流式 Token 统计方法

网关若需统计 token：

- **input_tokens**：从 `message_start.message.usage.input_tokens` 读取
- **output_tokens**：从 `message_delta.usage.output_tokens` 读取（**累计值，取最后一个**）
- **cache_creation / cache_read**：从 `message_start.message.usage` 中读取

---

## 7. 错误处理

### 7.1 HTTP 错误码

| 状态码 | error.type | 含义 | 网关处理建议 |
|--------|-----------|------|-------------|
| 400 | `invalid_request_error` | 请求格式或参数错误 | 直接返回客户端，不重试 |
| 401 | `authentication_error` | API Key 无效或缺失 | 检查 Key 配置，不重试 |
| 403 | `permission_error` | 无权访问该资源 | 直接返回客户端，不重试 |
| 404 | `not_found_error` | 资源不存在 | 直接返回客户端，不重试 |
| 413 | `request_too_large` | 请求体超过 32 MB | 直接返回客户端，不重试 |
| 429 | `rate_limit_error` | 触发速率限制 | 读取 `retry-after` 头后重试 |
| 500 | `api_error` | Anthropic 内部错误 | 指数退避重试 |
| 529 | `overloaded_error` | 服务过载 | 指数退避重试；流式中以 `error` 事件形式出现 |

### 7.2 错误响应结构

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "max_tokens: field required"
  },
  "request_id": "req_018EeWyXxfu5pfWkrYcMdjWG"
}
```

### 7.3 请求大小限制

| 端点 | 最大请求体 |
|------|-----------|
| Messages API | **32 MB** |
| Token Counting API | 32 MB |
| Batch API | 256 MB |
| Files API | 500 MB |

超出返回 413，由 Cloudflare 层拦截，不会到达 API 服务器。

### 7.4 特殊限制（网关必知）

- **Opus 4.6 不支持 prefill**：末尾消息为 `role: "assistant"` 时返回 400
- **长请求超时**：`max_tokens` 较大时强烈建议启用 `stream: true`；非流式请求若超过 10 分钟网络可能断开
- **TCP Keep-Alive**：直接集成时建议设置 TCP socket keep-alive，减少空闲连接超时风险

---

## 8. Prompt Caching

### 8.1 两种使用方式

**方式一：顶层自动缓存**（推荐用于多轮对话）

在请求体顶层加 `cache_control`，系统自动将断点应用到最后一个可缓存块，并随对话增长自动前移：

```json
{
  "model": "claude-opus-4-6",
  "max_tokens": 1024,
  "cache_control": {"type": "ephemeral"},
  "system": "...",
  "messages": [...]
}
```

**方式二：显式断点**（精细控制缓存位置）

在具体内容块上加 `cache_control`：

```json
{
  "system": [
    {
      "type": "text",
      "text": "这是会缓存的系统提示",
      "cache_control": {"type": "ephemeral", "ttl": "1h"}
    }
  ]
}
```

### 8.2 cache_control 格式

```json
{
  "type": "ephemeral",
  "ttl": "5m"
}
```

| TTL | 费用倍率 | 说明 |
|-----|---------|------|
| `"5m"`（默认）| 1.25x 写入，0.1x 读取 | 5 分钟有效期 |
| `"1h"` | 2x 写入，0.1x 读取 | 1 小时有效期 |

### 8.3 最小可缓存 token 数

| 模型 | 最小 token 数 |
|------|--------------|
| Claude Opus 4.6 / 4.5 | 4,096 |
| Claude Sonnet 4.6 | 2,048 |
| Claude Sonnet 4.5 / 4 / Opus 4.1 | 1,024 |
| Claude Haiku 4.5 | 4,096 |
| Claude Haiku 3.5 / 3 | 2,048 |

### 8.4 使用规则

- 每次请求最多设置 **4 个**缓存断点
- 系统从显式断点向前最多检查 **20 个块**；超过 20 块的提示需手动设置多个断点
- 缓存按 `tools → system → messages` 层次失效：修改靠前的内容会使其后所有缓存失效
- 并行请求时，第一个响应开始返回后再发后续请求才能命中缓存
- 不同组织之间缓存**完全隔离**

### 8.5 缓存 Token 与限速的关系

**关键**：对大多数模型，`cache_read_input_tokens` **不计入** ITPM 限速：

```
计入ITPM的tokens = input_tokens + cache_creation_input_tokens
不计入ITPM的tokens = cache_read_input_tokens
```

> 例：ITPM 上限 2,000,000，缓存命中率 80%，实际可处理 10,000,000 total input tokens/min。

---

## 9. 速率限制

### 9.1 限速算法

使用 **Token Bucket 算法**（令牌桶）：容量持续补充至上限，不按固定时间窗口重置。即便 RPM = 60，也可能按 1 req/sec 执行，短时间爆发仍会触发 429。

### 9.2 三种限速维度

| 维度 | 说明 |
|------|------|
| **RPM**（Requests Per Minute）| 每分钟请求数 |
| **ITPM**（Input Tokens Per Minute）| 每分钟输入 token 数（缓存读取不计入，见 §8.5）|
| **OTPM**（Output Tokens Per Minute）| 每分钟输出 token 数（按实际生成计，`max_tokens` 不影响）|

### 9.3 各 Tier 限速（主要模型）

**Tier 1（初始）**

| 模型 | RPM | ITPM | OTPM |
|------|-----|------|------|
| Claude Opus 4.x | 50 | 30,000 | 8,000 |
| Claude Sonnet 4.x | 50 | 30,000 | 8,000 |
| Claude Haiku 4.5 | 50 | 50,000 | 10,000 |

**Tier 2**

| 模型 | RPM | ITPM | OTPM |
|------|-----|------|------|
| Claude Opus 4.x | 1,000 | 450,000 | 90,000 |
| Claude Sonnet 4.x | 1,000 | 450,000 | 90,000 |
| Claude Haiku 4.5 | 1,000 | 450,000 | 90,000 |

**Tier 3**

| 模型 | RPM | ITPM | OTPM |
|------|-----|------|------|
| Claude Opus 4.x | 2,000 | 800,000 | 160,000 |
| Claude Sonnet 4.x | 2,000 | 800,000 | 160,000 |
| Claude Haiku 4.5 | 2,000 | 1,000,000 | 200,000 |

**Tier 4**

| 模型 | RPM | ITPM | OTPM |
|------|-----|------|------|
| Claude Opus 4.x | 4,000 | 2,000,000 | 400,000 |
| Claude Sonnet 4.x | 4,000 | 2,000,000 | 400,000 |
| Claude Haiku 4.5 | 4,000 | 4,000,000 | 800,000 |

> Opus 4.x 限速为 Opus 4.6 / 4.5 / 4.1 / 4 的**合计**上限。
> Sonnet 4.x 限速为 Sonnet 4.6 / 4.5 / 4 的**合计**上限。

### 9.4 429 处理

```
HTTP 429 + retry-after: 30
```

网关重试策略：
1. 读取 `retry-after` 响应头（单位：秒），等待对应时间后重试
2. 若无 `retry-after`，使用指数退避（建议初始 1s，最大 60s）
3. 重试时建议记录 `request-id` 便于排查
4. 监控 `anthropic-ratelimit-*-remaining` 头，实现主动预限流

---

## 10. 当前可用模型 ID

| 模型 | 说明 |
|------|------|
| `claude-opus-4-6` | 最强推理，支持扩展思考 |
| `claude-opus-4-5` | Opus 系列前代 |
| `claude-sonnet-4-6` | 均衡性能，推荐日常使用 |
| `claude-sonnet-4-5` | Sonnet 系列前代 |
| `claude-haiku-4-5` | 轻量快速 |

---

## 11. 与 OpenAI API 的关键差异（协议转换参考）

| 项目 | Anthropic | OpenAI |
|------|-----------|--------|
| 端点 | `POST /v1/messages` | `POST /v1/chat/completions` |
| 认证头 | `x-api-key: <key>` | `Authorization: Bearer <key>` |
| 版本头 | `anthropic-version` **必填** | 无 |
| `max_tokens` | **必填** | 可选 |
| 系统提示 | 独立 `system` 字段 | `messages` 中 `role: "system"` |
| 流式 usage | 在 `message_delta` 事件中（**累计值**） | 最后一个 chunk 中 |
| 流式格式 | 含 `event:` 行的标准 SSE | 只有 `data:` 行 |
| 扩展思考 | 原生支持（`thinking` 参数） | 不支持（o 系列通过不同接口） |
| `top_k` | 支持 | 不支持 |
| 响应 content | 数组（可含多种类型块） | 单一 `message.content` 字符串 |
| Tool 参数流 | partial JSON 字符串，需自行拼接 | 类似，也是 partial JSON |
| Token 计数 | `input_tokens` ≠ 总输入（见 §4.1）| `prompt_tokens` = 完整输入 |
| 请求大小上限 | 32 MB | 无明文规定（通常 ~16MB） |

---

## 12. 完整请求示例

### 简单文本
```json
{
  "model": "claude-sonnet-4-6",
  "max_tokens": 1024,
  "messages": [
    {"role": "user", "content": "你好"}
  ]
}
```

### 带 system + 缓存 + 流式
```json
{
  "model": "claude-opus-4-6",
  "max_tokens": 2048,
  "stream": true,
  "cache_control": {"type": "ephemeral"},
  "system": [
    {
      "type": "text",
      "text": "你是专业的代码审查助手。",
      "cache_control": {"type": "ephemeral", "ttl": "1h"}
    }
  ],
  "messages": [
    {"role": "user", "content": "请审查以下代码..."}
  ]
}
```

### 工具调用（完整多轮）
```json
// 第1轮：模型返回 tool_use
{
  "model": "claude-opus-4-6",
  "max_tokens": 1024,
  "tools": [
    {
      "name": "get_weather",
      "description": "获取天气",
      "input_schema": {
        "type": "object",
        "properties": {"location": {"type": "string"}},
        "required": ["location"]
      }
    }
  ],
  "messages": [
    {"role": "user", "content": "北京今天天气如何？"}
  ]
}

// 第2轮：携带 tool 结果回传
{
  "model": "claude-opus-4-6",
  "max_tokens": 1024,
  "tools": [...],
  "messages": [
    {"role": "user", "content": "北京今天天气如何？"},
    {
      "role": "assistant",
      "content": [
        {"type": "text", "text": "我来查询一下。"},
        {"type": "tool_use", "id": "toolu_01abc", "name": "get_weather", "input": {"location": "Beijing"}}
      ]
    },
    {
      "role": "user",
      "content": [
        {"type": "tool_result", "tool_use_id": "toolu_01abc", "content": "晴，25°C"}
      ]
    }
  ]
}
```

### 扩展思考
```json
{
  "model": "claude-opus-4-6",
  "max_tokens": 16000,
  "thinking": {
    "type": "enabled",
    "budget_tokens": 10000
  },
  "messages": [
    {"role": "user", "content": "请解决这道数学竞赛题..."}
  ]
}
```

---

## 13. 网关开发注意事项汇总

| 场景 | 要点 |
|------|------|
| **Token 统计** | 流式取 `message_delta.usage.output_tokens`（最后一个）；input 取 `message_start.usage`；总输入 = `cache_read + cache_creation + input_tokens` |
| **扩展思考** | 禁用 WriteTimeout；`budget_tokens` ≥ 1024 且 < `max_tokens` |
| **RedactedThinkingBlock** | 多轮对话必须原样透传，不可删改 |
| **流式错误** | HTTP 200 后仍需解析 `event: error` 事件 |
| **缓存透传** | `anthropic-beta` 头需原样透传；`cache_control` 字段不可删除 |
| **Prefill 限制** | Opus 4.6 末尾不可为 assistant 消息 |
| **限速响应头** | 透传 `anthropic-ratelimit-*` 和 `request-id` 头给调用方 |
| **429 重试** | 严格遵守 `retry-after` 秒数，更早重试仍会失败 |
| **请求大小** | 超 32 MB 在 Cloudflare 层被拒绝，返回 413 |
| **长请求** | `max_tokens` 大时强制 `stream: true`，同时设置 TCP keep-alive |
| **tool_use 流** | `input_json_delta` 的 partial_json 需自行拼接后整体解析为 JSON |
| **model 限速分组** | Opus 4.x / Sonnet 4.x 各为合计限速，不同子版本共享同一池 |
