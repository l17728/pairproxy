# OpenAI Chat Completions API 协议标准（网关开发参考版）

> **参考来源（2025-03）**
> - [Chat Completions API Reference](https://platform.openai.com/docs/api-reference/chat/create)
> - [Streaming Responses Guide](https://platform.openai.com/docs/api-reference/chat-streaming)
> - [Error Codes Guide](https://platform.openai.com/docs/guides/error-codes)
> - [Rate Limits Guide](https://platform.openai.com/docs/guides/rate-limits)
>
> **当前 API 版本：** `2020-10-01`（通过响应头 `openai-version` 返回）

---

## 1. 端点

```
POST https://api.openai.com/v1/chat/completions
```

---

## 2. 请求头

### 2.1 必填头

| Header | 示例值 | 说明 |
|--------|--------|------|
| `Authorization` | `Bearer sk-...` | OpenAI API 密钥，**Bearer 前缀必须** |
| `Content-Type` | `application/json` | 请求体格式 |

### 2.2 可选头

| Header | 示例值 | 说明 |
|--------|--------|------|
| `OpenAI-Organization` | `org-xxxxxxxx` | 指定计费组织，与 Key 的组织不匹配返回 401/403 |
| `OpenAI-Project` | `proj-xxxxxxxx` | 指定计费项目 |
| `X-Client-Request-Id` | `uuid-string` | 客户端自定义请求 ID；**强烈建议注入**，请求超时时也可通过此 ID 追踪 |

> **网关注意**：转发请求时需替换 `Authorization`，将网关用户 JWT 换为真实 API Key。`OpenAI-Organization` / `OpenAI-Project` 需按需透传或覆盖。

---

## 3. 请求体

### 3.1 必填参数

| 参数 | 类型 | 说明 |
|------|------|------|
| `model` | string | 模型 ID，见 §10 |
| `messages` | array | 对话历史，见 §3.3 |

### 3.2 可选参数

#### 采样控制（o 系列推理模型不支持，见 §3.8）

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `temperature` | float | `1` | 随机性，范围 `0`–`2`。**不要与 `top_p` 同时使用** |
| `top_p` | float | `1` | Nucleus 采样阈值，范围 `0`–`1`。**不要与 `temperature` 同时使用** |
| `n` | integer | `1` | 生成多少个独立的回复选项，全部计费 |
| `max_completion_tokens` | integer | 不限 | 最大输出 token 数，包含推理 token（替代已废弃的 `max_tokens`） |
| `max_tokens` | integer | — | **已废弃**，建议用 `max_completion_tokens` |
| `stop` | string \| string[] | null | 最多 4 个停止词；命中即停止，不包含在输出中 |
| `presence_penalty` | float | `0` | 范围 `-2.0`–`2.0`，正值增加话题新颖度 |
| `frequency_penalty` | float | `0` | 范围 `-2.0`–`2.0`，正值减少重复 |
| `logit_bias` | map | null | Token ID → 偏置值（`-100`–`100`）；`-100` 禁用该 token，`100` 强制输出 |
| `seed` | integer | null | 测试版确定性采样；配合响应的 `system_fingerprint` 检测后端变更 |

#### 流式输出

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `stream` | boolean | `false` | `true` 时以 SSE 格式流式输出 |
| `stream_options` | object | null | 仅 `stream: true` 时有效；`{"include_usage": true}` 使最后一个 chunk 包含完整 token 用量 |

#### 工具调用（Function Calling）

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `tools` | ToolDefinition[] | null | 工具定义列表，见 §3.5 |
| `tool_choice` | string \| object | `"auto"` | 工具调用策略，见 §3.6 |
| `parallel_tool_calls` | boolean | `true` | 是否允许模型并行调用多个工具 |

#### 输出格式

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `response_format` | object | null | 响应格式，见 §3.7 |
| `logprobs` | boolean | `false` | 返回输出 token 的对数概率 |
| `top_logprobs` | integer | null | 每个位置返回最可能的 N 个 token（0–20），需 `logprobs: true` |

#### 多模态输出

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `modalities` | string[] | `["text"]` | 输出模态，如 `["text"]`、`["text", "audio"]`（仅支持模型） |
| `audio` | object | null | 音频输出配置，含 `voice`（声音类型）和 `format`（编码格式）字段 |

#### 路由 / 元数据

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `store` | boolean | `false` | 是否存储输出（用于模型蒸馏/评测） |
| `metadata` | object | null | 最多 16 个 key-value 对；key ≤64 字符，value ≤512 字符 |
| `user` | string | null | **已废弃**，建议用 `safety_identifier` + `prompt_cache_key` 替代 |
| `safety_identifier` | string | null | 稳定的终端用户标识符，用于滥用检测（替代 `user`） |
| `prompt_cache_key` | string | null | 提示缓存优化 key（替代 `user` 的缓存用途） |
| `service_tier` | string | `"auto"` | 处理层级：`"auto"` \| `"default"` \| `"flex"` |
| `prediction` | object | null | Predicted Output（已知大部分输出时加速生成） |

### 3.3 消息格式（messages 数组）

支持的 `role` 值：

```json
{"role": "system",    "content": "你是一个专业助手"}
{"role": "developer", "content": "你是一个专业助手"}
{"role": "user",      "content": "你好"}
{"role": "assistant", "content": "你好！有什么可以帮您的？"}
{"role": "tool",      "tool_call_id": "call_abc", "content": "工具返回结果"}
```

> **重要**：`o1` 及更新的推理模型**不支持 `system` role**，需改用 `developer` role 传入系统级指令。`gpt-4o` 等标准模型仍使用 `system` role。

**多模态消息**（content 为数组）：

```json
{
  "role": "user",
  "content": [
    {"type": "text", "text": "描述这张图片"},
    {
      "type": "image_url",
      "image_url": {
        "url": "https://example.com/image.jpg",
        "detail": "auto"
      }
    }
  ]
}
```

### 3.4 content 块类型（请求侧）

#### TextContentPart
```json
{"type": "text", "text": "string"}
```

#### ImageUrlContentPart
```json
{
  "type": "image_url",
  "image_url": {
    "url": "https://... 或 data:image/jpeg;base64,...",
    "detail": "auto"
  }
}
```
`detail` 取值：`"auto"`（默认）、`"low"`（省 token）、`"high"`（高精度）。图片 > 8MB 会被丢弃。

#### InputAudioContentPart
```json
{
  "type": "input_audio",
  "input_audio": {
    "data": "<base64>",
    "format": "wav"
  }
}
```
`format` 支持：`"wav"`、`"mp3"`（仅音频支持模型）。

#### FileContentPart
```json
{
  "type": "file",
  "file_data": "<base64>",
  "file_id": "file-abc",
  "file_name": "document.pdf"
}
```

### 3.5 工具定义（tools 数组）

```json
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "获取指定城市的天气",
    "strict": true,
    "parameters": {
      "type": "object",
      "properties": {
        "location": {"type": "string", "description": "城市名称"},
        "unit": {"type": "string", "enum": ["celsius", "fahrenheit"]}
      },
      "required": ["location", "unit"],
      "additionalProperties": false
    }
  }
}
```

- 函数名：只允许 `a-z`、`A-Z`、`0-9`、`_`、`-`，最长 64 字符
- `strict: true` 启用 Structured Outputs 模式：**所有对象必须设 `additionalProperties: false`，所有属性必须出现在 `required` 中**

### 3.6 tool_choice

```json
"none"                                          // 禁止调用工具
"auto"                                          // 模型自主决定（有 tools 时默认）
"required"                                      // 必须调用至少一个工具
{"type": "function", "function": {"name": "get_weather"}}  // 强制调用指定工具
```

### 3.7 response_format

```json
{"type": "text"}                                // 默认，普通文本
{"type": "json_object"}                         // JSON 模式：保证输出合法 JSON，但不约束 schema
{
  "type": "json_schema",
  "json_schema": {
    "name": "schema_name",
    "schema": {
      "type": "object",
      "properties": { "field": {"type": "string"} },
      "required": ["field"],
      "additionalProperties": false
    },
    "strict": true
  }
}
```

> **Structured Outputs (`json_schema`) 约束**：
> - 需要 `gpt-4o-2024-08-06`、`gpt-4o-mini-2024-07-18` 或更新模型
> - Schema 中**每个对象层**均需设置 `additionalProperties: false`，且所有属性都放入 `required`
> - 使用 JSON 模式（`json_object`）时，系统提示中**必须明确要求模型输出 JSON**，否则模型可能无限生成 token

### 3.8 o 系列推理模型的参数限制

o1、o3、o4-mini 等推理模型对参数有严格限制，传入不支持的参数会返回 HTTP 400。

**o 系列不支持的参数：**

| 不支持的参数 | 说明 |
|-------------|------|
| `temperature` | 固定为 1，不可修改 |
| `top_p` | 固定为 1，不可修改 |
| `n` | 固定为 1，不可修改 |
| `logprobs` / `top_logprobs` | 不支持 |
| `logit_bias` | 不支持 |
| `presence_penalty` / `frequency_penalty` | 固定为 0，不可修改 |
| `system` role 消息 | 改用 `developer` role |

**o 系列专有参数：**

| 参数 | 类型 | 说明 |
|------|------|------|
| `reasoning_effort` | string | 控制推理深度：`"low"` \| `"medium"` \| `"high"`（o1、o3、o4-mini） |

> **网关注意**：转发到 o 系列模型时，需自动过滤或不注入上述不支持的参数（如 `temperature`），否则会返回 400 错误。`system` role 消息需转换为 `developer` role。

---

## 4. 响应体（非流式，HTTP 200）

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1677858242,
  "model": "gpt-4o-mini",
  "system_fingerprint": "fp_44709d6fcb",
  "service_tier": "default",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "响应内容",
        "tool_calls": null,
        "refusal": null,
        "annotations": []
      },
      "finish_reason": "stop",
      "logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 25,
    "completion_tokens": 15,
    "total_tokens": 40,
    "prompt_tokens_details": {
      "cached_tokens": 0,
      "audio_tokens": 0
    },
    "completion_tokens_details": {
      "reasoning_tokens": 0,
      "audio_tokens": 0,
      "accepted_prediction_tokens": 0,
      "rejected_prediction_tokens": 0
    }
  }
}
```

### 4.1 响应字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | 请求唯一 ID，格式 `chatcmpl-xxx` |
| `object` | string | 固定为 `"chat.completion"` |
| `created` | integer | Unix 时间戳 |
| `model` | string | 实际使用的模型 ID |
| `system_fingerprint` | string | 后端配置指纹；配合 `seed` 参数检测后端变更 |
| `service_tier` | string | 实际使用的服务层级 |
| `choices` | array | 回复选项数组（长度 = `n`） |
| `choices[].index` | integer | 选项索引（0-based） |
| `choices[].message.role` | string | 固定为 `"assistant"` |
| `choices[].message.content` | string \| null | 文本内容；调用工具时为 null |
| `choices[].message.tool_calls` | array \| null | 工具调用数组，见 §4.2 |
| `choices[].message.refusal` | string \| null | 模型拒绝回复时的说明 |
| `choices[].message.annotations` | array | 引用注解（如 `url_citation` 类型），搜索模型使用 |
| `choices[].finish_reason` | string | 停止原因，见 §4.3 |
| `usage.prompt_tokens` | integer | 输入 token 数（**等于完整输入，与 Anthropic 不同**） |
| `usage.completion_tokens` | integer | 输出 token 数 |
| `usage.total_tokens` | integer | prompt + completion 之和 |
| `usage.prompt_tokens_details.cached_tokens` | integer | 命中缓存的 token 数 |
| `usage.completion_tokens_details.reasoning_tokens` | integer | 推理过程消耗的 token（o 系列） |
| `usage.completion_tokens_details.accepted_prediction_tokens` | integer | Predicted Output 命中的 token |
| `usage.completion_tokens_details.rejected_prediction_tokens` | integer | Predicted Output 未命中的 token |

### 4.2 tool_calls 数组

```json
[
  {
    "id": "call_DdmO9pD3xa9XTPNJ32zg2hcA",
    "type": "function",
    "function": {
      "name": "get_weather",
      "arguments": "{\"location\": \"Paris\"}"
    }
  }
]
```

> **重要**：`arguments` 是 **JSON 编码的字符串**，需先 `JSON.parse()` 后使用。模型可能产生非法 JSON 或幻觉参数，**必须校验后再调用函数**。

### 4.3 finish_reason 取值

| 值 | 含义 |
|----|------|
| `"stop"` | 模型自然停止或命中停止词 |
| `"length"` | 达到 `max_completion_tokens` 上限 |
| `"content_filter"` | 内容被过滤 |
| `"tool_calls"` | 模型请求调用工具 |
| `"function_call"` | **已废弃**，即将被移除 |

---

## 5. 响应头

### 5.1 通用响应头

| Header | 说明 |
|--------|------|
| `x-request-id` | 全局唯一请求 ID；客服排查必须提供 |
| `openai-organization` | 计费组织 ID |
| `openai-processing-ms` | 服务端处理耗时（毫秒） |
| `openai-version` | REST API 版本（目前 `2020-10-01`） |

### 5.2 限速状态头（每次响应均包含）

| Header | 说明 |
|--------|------|
| `x-ratelimit-limit-requests` | 当前 RPM 上限 |
| `x-ratelimit-limit-tokens` | 当前 TPM 上限 |
| `x-ratelimit-remaining-requests` | 当前窗口 RPM 剩余次数 |
| `x-ratelimit-remaining-tokens` | 当前窗口 TPM 剩余量 |
| `x-ratelimit-reset-requests` | RPM 重置倒计时（时间字符串，如 `"1s"`、`"6m0s"`） |
| `x-ratelimit-reset-tokens` | TPM 重置倒计时 |

### 5.3 限速触发头（429 时）

| Header | 说明 |
|--------|------|
| `Retry-After` | 需等待的秒数；可能缺失（Azure 部署常见），缺失时使用指数退避 |

> **网关建议**：透传 `x-ratelimit-*` 和 `x-request-id` 给下游客户端，便于客户端自主限流和问题排查。

---

## 6. SSE 流式协议（stream: true）

响应 `Content-Type: text/event-stream`，每行格式为：
```
data: <JSON字符串>

```
（每个事件后有一个空行。**注意：没有 `event:` 行**，这与 Anthropic 协议不同。）

流结束标志：
```
data: [DONE]
```

### 6.1 流式事件序列

```
data: {chunk — role 声明，content 为空}        ← 第 1 个 chunk

data: {chunk — content delta "Hello"}         ← 内容 chunk × N
data: {chunk — content delta " World"}

data: {chunk — finish_reason: "stop", delta: {}}  ← 最后一个 choices chunk

data: {chunk — choices: [], usage: {...}}      ← 仅当 stream_options.include_usage:true
data: [DONE]
```

### 6.2 Chunk JSON 结构

#### 首个 chunk（role 声明）
```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion.chunk",
  "created": 1694268190,
  "model": "gpt-4o-mini",
  "system_fingerprint": "fp_44709d6fcb",
  "choices": [{
    "index": 0,
    "delta": {"role": "assistant", "content": ""},
    "logprobs": null,
    "finish_reason": null
  }]
}
```

#### 内容 chunk
```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion.chunk",
  "created": 1694268190,
  "model": "gpt-4o-mini",
  "choices": [{
    "index": 0,
    "delta": {"content": "Hello"},
    "logprobs": null,
    "finish_reason": null
  }]
}
```

#### 最后一个 choices chunk（finish_reason 非 null）
```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion.chunk",
  "created": 1694268190,
  "model": "gpt-4o-mini",
  "choices": [{
    "index": 0,
    "delta": {},
    "logprobs": null,
    "finish_reason": "stop"
  }]
}
```

#### 用量 chunk（仅当 `stream_options.include_usage: true`，在 `[DONE]` 之前发送）
```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion.chunk",
  "created": 1694268190,
  "model": "gpt-4o-mini",
  "choices": [],
  "usage": {
    "prompt_tokens": 100,
    "completion_tokens": 50,
    "total_tokens": 150,
    "prompt_tokens_details": {"cached_tokens": 0, "audio_tokens": 0},
    "completion_tokens_details": {
      "reasoning_tokens": 0,
      "audio_tokens": 0,
      "accepted_prediction_tokens": 0,
      "rejected_prediction_tokens": 0
    }
  }
}
```

> **重要**：
> - 用量 chunk 的 `choices` 为**空数组**，`usage` 为完整 token 统计
> - 其余所有 chunk 的 `usage` 字段为 `null`（启用 `include_usage` 时）
> - 所有 chunk 共享相同的 `id` 和 `created` 时间戳
> - 若流中断，用量 chunk 可能收不到

### 6.3 tool_calls 流式输出

工具调用通过多个 chunk 的 `delta.tool_calls` 逐步传输：

**第 1 个 chunk**（有 id、type、name）：
```json
[{"index": 0, "id": "call_DdmO9pD3xa9XTPNJ32zg2hcA", "type": "function",
  "function": {"name": "get_weather", "arguments": ""}}]
```

**后续 chunk**（仅有 partial arguments，其余字段为 null）：
```json
[{"index": 0, "id": null, "type": null, "function": {"name": null, "arguments": "{\"location\""}}]
[{"index": 0, "id": null, "type": null, "function": {"name": null, "arguments": ": \"Paris\"}"}}]
```

用 `index` 字段区分并行工具调用（多个工具时 index 不同）。需自行**拼接所有 `arguments` 片段**，再整体解析为 JSON。`finish_reason` 在最后一个 choices chunk 中为 `"tool_calls"`。

### 6.4 流式 Token 统计方法

| 统计项 | 获取位置 |
|--------|---------|
| `prompt_tokens` | 用量 chunk（`choices: []` 的那个）的 `usage.prompt_tokens` |
| `completion_tokens` | 用量 chunk 的 `usage.completion_tokens` |
| 完整用量 | 需注入 `stream_options.include_usage: true`；流中断时可能收不到 |
| `finish_reason` | 最后一个 `choices` 非空的 chunk（`finish_reason` 不为 null 的那个） |

> **网关建议**：如需统计流式请求的 token 用量，必须在转发前注入 `stream_options: {"include_usage": true}`（幂等操作，不影响客户端体验）。

---

## 7. 错误处理

### 7.1 错误响应结构

```json
{
  "error": {
    "message": "错误的人类可读描述",
    "type":    "error_category",
    "param":   "参数名或null",
    "code":    "具体错误码或null"
  }
}
```

> **重要**：错误信息嵌套在 `"error"` 对象内，**不在根层级**。网关生成合成错误时必须遵守此结构，否则 OpenAI 兼容客户端无法正确解析。

### 7.2 HTTP 错误码

| 状态码 | error.type | 含义 | 网关处理建议 |
|--------|-----------|------|-------------|
| 400 | `invalid_request_error` | 请求参数错误（含 o 系列不支持的参数） | 直接返回客户端，不重试 |
| 401 | `authentication_error` | API Key 无效或缺失 | 检查 Key，不重试 |
| 403 | `permission_error` | 无权访问（地区限制等） | 直接返回客户端，不重试 |
| 404 | `invalid_request_error` | 模型不存在或不可用 | 直接返回客户端，不重试 |
| 409 | — | 资源被并发更新 | 短暂等待后重试 |
| 429 | `rate_limit_error` | 触发速率限制（RPM/TPM） | 读取 `Retry-After` 等待后重试 |
| 429 | `insufficient_quota` | 月度额度/余额耗尽 | **不要重试**，需充值或升级 |
| 500 | `server_error` | 服务端内部错误 | 指数退避重试 |
| 502 | `api_error` | 上游网关错误 | 指数退避重试 |
| 503 | `server_error` | 服务过载 | 尊重 `Retry-After`，指数退避重试 |

> **429 类型区分**：同为 HTTP 429，但 `error.type` 不同——`rate_limit_error` 是限流（可重试），`insufficient_quota` 是额度耗尽（不可重试）。必须读取 `error.type` 而非仅看 HTTP 状态码来决定处理方式。

### 7.3 error.type 全集

| type | 触发场景 |
|------|---------|
| `invalid_request_error` | 参数错误、格式错误、不支持的参数（400） |
| `authentication_error` | API Key 无效、过期、已撤销（401） |
| `permission_error` | 已认证但无权限（403） |
| `rate_limit_error` | 超过 RPM/TPM 速率限制（429） |
| `insufficient_quota` | 月度额度/账户余额不足（429） |
| `server_error` | OpenAI 内部故障（500/503） |
| `api_error` | 通用 API 错误（502） |
| `requests_timed_out` | 请求超时 |

### 7.4 error.code 常见值

| code | 对应 type | 含义 |
|------|----------|------|
| `invalid_api_key` | `authentication_error` | API Key 格式错误或无效 |
| `invalid_type` | `invalid_request_error` | 参数类型错误 |
| `unknown_parameter` | `invalid_request_error` | 未知参数（含 o 系列不支持的参数） |
| `context_length_exceeded` | `invalid_request_error` | 输入超过上下文窗口，`param: "messages"` |
| `model_not_found` | `invalid_request_error` | 模型不存在或无权限 |
| `content_policy_violation` | `invalid_request_error` | Prompt 触发内容政策 |
| `rate_limit_exceeded` | `rate_limit_error` | 超过 RPM/TPM 限制 |
| `insufficient_quota` | `insufficient_quota` | 月度额度/余额不足 |
| `string_above_max_length` | `invalid_request_error` | 字符串字段超出最大长度 |
| null | 任意 | 无具体 code，依赖 `type` + `message` 判断 |

### 7.5 重试策略

```
1. 收到 429：
   - error.type = "rate_limit_error"   → 读取 Retry-After 头（秒），等待后重试
   - error.type = "insufficient_quota" → 不重试，告知用户充值
2. 退避公式：delay = min(base * 2^attempt + jitter, 60s)，base ≈ 0.5s，jitter ≈ 25%
3. 最大重试次数：6 次（可调）
4. 500/502/503：等待 1–2s 后重试，最多 2–3 次
5. 400/401：不重试（需修改请求或检查 Key）
6. 注意：失败请求也计入 RPM/TPM 限额，快速重试会加剧问题
```

---

## 8. 速率限制

### 8.1 五种限制维度

| 维度 | 说明 |
|------|------|
| **RPM** | 每分钟请求数 |
| **RPD** | 每天请求数 |
| **TPM** | 每分钟 token 数（prompt + completion；`max_completion_tokens` 按设定值计入，非实际生成量） |
| **TPD** | 每天 token 数 |
| **IPM** | 每分钟图片生成数（图像端点） |

> **注意**：RPM 限制可能按子分钟窗口执行（如 RPM=600 → 实际 ≤10 req/sec），短时突发也会触发限制。

### 8.2 各 Tier 限速参考（GPT-4o，近似值）

| Tier | 资格（消费金额） | RPM | RPD | TPM |
|------|----------------|-----|-----|-----|
| Free | 新账号 | 3 | 200 | 40,000 |
| Tier 1 | ~$5+ | 500 | 10,000 | 30,000 |
| Tier 2 | ~$50+ | 5,000 | — | 450,000 |
| Tier 3 | ~$100+ | 5,000 | — | 800,000 |
| Tier 4 | ~$250+ | 10,000 | — | 2,000,000 |
| Tier 5 | ~$1,000+ | 10,000 | — | 30,000,000 |

> Tier 自动升级；如需更高限额可在 Dashboard 申请。

### 8.3 429 处理

```
HTTP 429 + Retry-After: 30
```

1. 先判断 `error.type`：`rate_limit_error` 可重试，`insufficient_quota` 不可重试
2. 读取 `Retry-After` 响应头（单位：秒），等待对应时间后重试
3. 若无 `Retry-After`（Azure 部署常见），使用指数退避（初始 0.5s，最大 60s）
4. 记录 `x-request-id` 便于排查
5. 监控 `x-ratelimit-remaining-*` 头，实现主动预限流

---

## 9. Prompt Caching（提示缓存）

OpenAI 自动对满足条件的输入应用 KV Cache，**无需显式配置**（与 Anthropic 的 `cache_control` 不同）。

- **触发条件**：请求前缀完全一致，模型相同，长度超过最低阈值
- **费用**：缓存命中 token 按约 50% 折扣计费
- **响应统计**：`usage.prompt_tokens_details.cached_tokens` 记录命中缓存的 token 数
- **缓存优化**：可用 `prompt_cache_key` 参数（替代旧的 `user` 参数）提高缓存命中率

> **网关注意**：无需特殊处理，透传请求即可自动获益。如需准确计算实际费用，需读取 `cached_tokens` 字段对缓存命中部分应用折扣价格。

---

## 10. 当前主要模型 ID

| 模型 | 类型 | 说明 |
|------|------|------|
| `gpt-4.1` | 标准 | 最新旗舰模型，高性能长上下文 |
| `gpt-4o` | 标准 | 多模态旗舰，支持视觉/音频 |
| `gpt-4o-mini` | 标准 | 轻量快速，适合日常任务 |
| `gpt-4` | 标准 | GPT-4 经典版 |
| `gpt-3.5-turbo` | 标准 | 经济型，速度最快 |
| `o3` | 推理 | 强推理，适合复杂逻辑/数学；**见 §3.8 参数限制** |
| `o4-mini` | 推理 | 轻量推理型；**见 §3.8 参数限制** |
| `o1` | 推理 | 早期推理模型，**不支持 `system` role**；**见 §3.8** |

---

## 11. 与 Anthropic API 的关键差异（协议转换参考）

| 项目 | OpenAI | Anthropic |
|------|--------|-----------|
| 端点 | `POST /v1/chat/completions` | `POST /v1/messages` |
| 认证头 | `Authorization: Bearer <key>` | `x-api-key: <key>` |
| 版本头 | 无（可选 `openai-version`） | `anthropic-version` **必填** |
| `max_tokens` | 可选（推荐用 `max_completion_tokens`） | **必填** |
| 系统提示 | `messages` 中 `role: "system"`（o 系列用 `"developer"`） | 独立 `system` 字段 |
| 推理模型系统指令 | `role: "developer"`（o1+ 专用） | 同用 `system` 字段 |
| 流式 SSE 格式 | 只有 `data:` 行，**无 `event:` 行** | 含 `event:` 行的标准 SSE |
| 流式 usage | 最后一个专用 chunk（需 `stream_options.include_usage: true`） | `message_delta` 事件（默认包含） |
| 流式结束标志 | `data: [DONE]`（**不是 JSON**，不可 parse） | `event: message_stop` |
| `prompt_tokens` | 等于完整输入总量（含缓存命中） | 仅为末尾 cache 断点之后的量（需加 cache 字段） |
| 缓存 token | 无显式配置，自动触发，结果在 `cached_tokens` 字段 | 需显式 `cache_control` 标记 |
| 扩展思考 | o 系列通过 `reasoning_effort` 控制，token 记录在 `reasoning_tokens` | 原生支持 `thinking` 参数 |
| `top_k` | 不支持 | 支持 |
| 工具参数字段名 | `arguments`（JSON 字符串，需 parse） | `input`（已解析的 JSON 对象） |
| 工具并行调用 | `parallel_tool_calls: true`（默认）| `disable_parallel_tool_use: true`（需显式禁用） |
| 响应 content | `choices[].message.content` 字符串 | 数组（可含多种类型块） |
| 请求大小上限 | 无明文规定（~16MB） | 32 MB（Cloudflare 层拦截） |
| 额度耗尽 error.type | `insufficient_quota`（独立类型） | `overloaded_error` 或 429 |

---

## 12. 完整请求示例

### 简单文本
```json
{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "user", "content": "你好"}
  ]
}
```

### 带 system + 流式
```json
{
  "model": "gpt-4o",
  "stream": true,
  "stream_options": {"include_usage": true},
  "messages": [
    {"role": "system", "content": "你是专业的代码审查助手。"},
    {"role": "user", "content": "请审查以下代码..."}
  ],
  "max_completion_tokens": 2048
}
```

### o 系列推理模型（使用 developer role，不传 temperature）
```json
{
  "model": "o3",
  "reasoning_effort": "high",
  "messages": [
    {"role": "developer", "content": "你是专业的数学解题助手。"},
    {"role": "user", "content": "请解这道数学竞赛题..."}
  ],
  "max_completion_tokens": 8000
}
```

### 工具调用（完整多轮）
```json
// 第1轮：模型返回 tool_calls
{
  "model": "gpt-4o",
  "tools": [{
    "type": "function",
    "function": {
      "name": "get_weather",
      "description": "获取天气",
      "parameters": {
        "type": "object",
        "properties": {"location": {"type": "string"}},
        "required": ["location"]
      }
    }
  }],
  "messages": [
    {"role": "user", "content": "北京今天天气如何？"}
  ]
}

// 第2轮：携带 tool 结果回传
{
  "model": "gpt-4o",
  "tools": [...],
  "messages": [
    {"role": "user", "content": "北京今天天气如何？"},
    {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "call_abc123",
        "type": "function",
        "function": {"name": "get_weather", "arguments": "{\"location\": \"Beijing\"}"}
      }]
    },
    {
      "role": "tool",
      "tool_call_id": "call_abc123",
      "content": "晴，25°C"
    }
  ]
}
```

### Structured Outputs（schema 严格模式）
```json
{
  "model": "gpt-4o-2024-08-06",
  "messages": [{"role": "user", "content": "提取文章中的人名和地点"}],
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "entities",
      "strict": true,
      "schema": {
        "type": "object",
        "properties": {
          "names":     {"type": "array", "items": {"type": "string"}},
          "locations": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["names", "locations"],
        "additionalProperties": false
      }
    }
  }
}
```

---

## 13. 网关开发注意事项汇总

| 场景 | 要点 |
|------|------|
| **Token 统计（流式）** | 必须注入 `stream_options.include_usage: true`；从 `choices: []` 的专用 chunk 读取 `usage` |
| **Token 统计（非流式）** | `usage.prompt_tokens` 即完整输入量（含缓存命中，无需额外加法） |
| **缓存命中折扣** | 读取 `usage.prompt_tokens_details.cached_tokens` 计算精确费用 |
| **流式结束检测** | 检测 `data: [DONE]` 字符串（**非 JSON，不可 JSON.parse**） |
| **tool_calls 拼接** | 用 `index` 分组，拼接所有 `arguments` 片段，整体 JSON.parse；校验结果后再调用 |
| **错误结构** | 合成错误必须嵌套在 `{"error": {...}}` 内，否则 OpenAI 兼容客户端无法解析 |
| **429 必须区分 type** | `rate_limit_error` 可重试；`insufficient_quota` 不可重试（充值） |
| **Retry-After 缺失** | Azure 等部署常见，缺失时按指数退避处理 |
| **失败请求计费** | 失败请求也消耗 RPM/TPM 配额，避免快速重试循环 |
| **透传限速头** | 转发 `x-ratelimit-*` 和 `x-request-id` 给下游客户端 |
| **注入 X-Client-Request-Id** | 向上游注入 UUID，确保超时时也能追溯 |
| **长请求超时** | `max_completion_tokens` 较大或 o 系列推理时推荐 `stream: true`；禁用 HTTP WriteTimeout |
| **JSON 模式限制** | `response_format: json_object` 必须在 prompt 中明确要求输出 JSON |
| **Structured Outputs 要求** | schema 每层必须设 `additionalProperties: false`，所有属性放入 `required` |
| **TPM 计算** | `max_completion_tokens` 按设定值计入 TPM，而非实际生成量 |
| **o 系列参数过滤** | 转发到 o 系列模型时，自动过滤 `temperature`/`top_p`/`n` 等不支持参数；`system` role 转为 `developer` |
