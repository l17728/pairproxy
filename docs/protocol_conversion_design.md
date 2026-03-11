# 协议转换设计文档：Anthropic ↔ OpenAI（Claude → Ollama）

> **版本**：v1.0
> **适用场景**：Claude CLI（或任何 Anthropic 协议客户端）经由 PairProxy 网关连接 Ollama（OpenAI 兼容协议）的本地 LLM 服务
> **参考文档**：`docs/anthropic_protocal.md`、`docs/openai_protocol.md`

---

## 1. 架构概述

```
┌─────────────────┐      Anthropic 协议       ┌──────────────────────────────────────────┐
│   Claude CLI    │ ─────────────────────────▶ │              PairProxy (sproxy)          │
│  (客户端)       │ ◀───────────────────────── │                                          │
└─────────────────┘      Anthropic 协议       │  ┌────────────────────────────────────┐  │
                                              │  │       协议转换层                   │  │
                                              │  │  Anthropic 请求 → OpenAI 请求     │  │
                                              │  │  OpenAI 响应  → Anthropic 响应    │  │
                                              │  └────────────────────────────────────┘  │
                                              └────────────────────┬─────────────────────┘
                                                                   │  OpenAI 协议
                                                                   ▼
                                                       ┌─────────────────────┐
                                                       │  Ollama / OpenAI 兼  │
                                                       │  容 LLM 端点         │
                                                       └─────────────────────┘
```

**触发条件**：
- 请求路径为 `/v1/messages`（Anthropic 格式）
- 目标 LLM 的 `provider` 配置为 `"ollama"` 或 `"openai"`

转换在 PairProxy 内部完成，Claude CLI 和 Ollama 均无感知。

---

## 2. 请求转换：Anthropic → OpenAI

### 2.1 HTTP 层变更

| 项目 | Anthropic（原始） | OpenAI（转换后） |
|------|-------------------|-----------------|
| **路径** | `POST /v1/messages` | `POST /v1/chat/completions` |
| `x-api-key` | 用户 JWT（PairProxy 鉴权用，已消费） | 替换为真实 API Key（`Authorization: Bearer <key>`） |
| `anthropic-version` | `2023-06-01` | **删除**（OpenAI 无此头） |
| `anthropic-beta` | `prompt-caching-2024-07-31` 等 | **删除**（OpenAI 无此头） |
| `content-type` | `application/json` | 保持不变 |

### 2.2 请求体参数映射

#### 基础参数

| Anthropic 字段 | OpenAI 字段 | 处理方式 |
|----------------|------------|---------|
| `model` | `model` | **直接透传**（Ollama 使用本地模型名，需由配置映射） |
| `max_tokens` | `max_tokens` / `max_completion_tokens` | 直接透传（推荐用 `max_completion_tokens`，但 Ollama 兼容 `max_tokens`） |
| `temperature` | `temperature` | 直接透传 |
| `top_p` | `top_p` | 直接透传 |
| `top_k` | **无对应字段** | **丢弃**（OpenAI/Ollama 不支持） |
| `stop_sequences` | `stop` | 直接透传（Anthropic 数组 → OpenAI 数组，格式相同） |
| `stream` | `stream` | 直接透传 |
| `metadata.user_id` | `safety_identifier` | 可选透传，或丢弃 |
| `thinking` | **无对应字段** | **丢弃**，返回 400（Ollama 不支持扩展思考） |
| `cache_control` | **无对应字段** | **丢弃**（OpenAI 自动缓存，无需配置） |

#### 流式特殊处理

当 `stream: true` 时，**必须**在请求中注入：
```json
"stream_options": {"include_usage": true}
```
这是获取流式 token 用量的唯一方式。若客户端已携带此字段，保持不变（幂等）。

### 2.3 system 字段转换

Anthropic 的独立 `system` 字段需转换为 OpenAI messages 数组中的第一条消息。

#### 情形 A：string 类型
```json
// Anthropic 请求
{
  "system": "你是专业的代码审查助手。"
}

// → OpenAI messages 数组首位插入
{"role": "system", "content": "你是专业的代码审查助手。"}
```

#### 情形 B：TextBlockParam[] 数组（含 cache_control）
```json
// Anthropic 请求
{
  "system": [
    {"type": "text", "text": "你是代码审查助手。", "cache_control": {"type": "ephemeral"}},
    {"type": "text", "text": "专注于安全漏洞。"}
  ]
}

// → 提取所有 type="text" 的文本，拼接后作为 system 消息
// cache_control 字段丢弃（OpenAI 自动处理缓存）
{"role": "system", "content": "你是代码审查助手。\n专注于安全漏洞。"}
```

> **注意**：system 数组中的非 text 块（如 image、document）当前不支持转换，应忽略并记录 WARN 日志。

### 2.4 messages 数组转换

#### 2.4.1 普通文本消息

```json
// Anthropic
{"role": "user", "content": "你好"}
{"role": "assistant", "content": "你好！"}

// → OpenAI（完全相同，role 一致）
{"role": "user", "content": "你好"}
{"role": "assistant", "content": "你好！"}
```

#### 2.4.2 多模态内容块数组

Anthropic content 为数组时，需按块类型分别处理：

```json
// Anthropic
{
  "role": "user",
  "content": [
    {"type": "text", "text": "描述这张图片"},
    {
      "type": "image",
      "source": {"type": "base64", "media_type": "image/jpeg", "data": "<base64>"}
    }
  ]
}

// → OpenAI
{
  "role": "user",
  "content": [
    {"type": "text", "text": "描述这张图片"},
    {
      "type": "image_url",
      "image_url": {
        "url": "data:image/jpeg;base64,<base64>",
        "detail": "auto"
      }
    }
  ]
}
```

URL 类型图片转换：
```json
// Anthropic
{"type": "image", "source": {"type": "url", "url": "https://example.com/img.jpg"}}

// → OpenAI
{"type": "image_url", "image_url": {"url": "https://example.com/img.jpg", "detail": "auto"}}
```

**媒体类型映射**：
| Anthropic source.media_type | OpenAI data URI 前缀 |
|-----------------------------|---------------------|
| `image/jpeg` | `data:image/jpeg;base64,` |
| `image/png`  | `data:image/png;base64,`  |
| `image/gif`  | `data:image/gif;base64,`  |
| `image/webp` | `data:image/webp;base64,` |

#### 2.4.3 工具调用消息（assistant 含 tool_use 块）

Anthropic 的 `tool_use` 内容块需转换为 OpenAI 的 `tool_calls` 数组：

```json
// Anthropic assistant 消息（含工具调用）
{
  "role": "assistant",
  "content": [
    {"type": "text", "text": "我来查询天气。"},
    {
      "type": "tool_use",
      "id": "toolu_01abc",
      "name": "get_weather",
      "input": {"location": "Beijing"}
    }
  ]
}

// → OpenAI
{
  "role": "assistant",
  "content": "我来查询天气。",    // 文本内容保留（若全为工具调用则为 null）
  "tool_calls": [
    {
      "id": "toolu_01abc",         // ID 原样透传
      "type": "function",
      "function": {
        "name": "get_weather",
        "arguments": "{\"location\":\"Beijing\"}"  // input 对象序列化为 JSON 字符串
      }
    }
  ]
}
```

> **关键**：`input`（已解析的 JSON 对象）→ `arguments`（JSON 编码的字符串）。
> 若 assistant 消息**仅含**工具调用（无文本），则 `content` 设为 `null`。

#### 2.4.4 工具结果消息（user 含 tool_result 块）

Anthropic 在 user 消息中回传工具结果，OpenAI 使用独立的 `role: "tool"` 消息：

```json
// Anthropic user 消息（含工具结果）
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_01abc",
      "content": "晴，25°C",
      "is_error": false
    }
  ]
}

// → OpenAI（每个 tool_result 拆分为独立的 tool 消息）
{
  "role": "tool",
  "tool_call_id": "toolu_01abc",
  "content": "晴，25°C"
}
```

**混合消息处理**（user 消息同时含 text 和 tool_result）：
```json
// Anthropic（同一 user 消息内混合）
{
  "role": "user",
  "content": [
    {"type": "tool_result", "tool_use_id": "toolu_01abc", "content": "晴，25°C"},
    {"type": "text", "text": "好的，谢谢"}
  ]
}

// → OpenAI（拆分为两条消息，tool 消息在前）
{"role": "tool", "tool_call_id": "toolu_01abc", "content": "晴，25°C"}
{"role": "user", "content": "好的，谢谢"}
```

> **顺序规则**：tool 消息必须紧跟在 assistant 的 tool_calls 之后。将同一 user 消息中的 tool_result 提取为 tool 消息，剩余文本内容保留为 user 消息。

#### 2.4.5 tool_result content 为数组时

```json
// Anthropic（content 为内容块数组）
{
  "type": "tool_result",
  "tool_use_id": "toolu_01abc",
  "content": [
    {"type": "text", "text": "查询结果：晴，25°C"},
    {"type": "image", "source": {"type": "base64", ...}}
  ]
}

// → OpenAI（提取文本，图片可选转换或丢弃）
{"role": "tool", "tool_call_id": "toolu_01abc", "content": "查询结果：晴，25°C"}
```

#### 2.4.6 Prefill 末尾 assistant 消息

Anthropic 支持以 assistant 消息结尾（prefill），OpenAI 不支持。
当最后一条消息的 role 为 `assistant` 时：
- **推荐处理**：返回 HTTP 400，错误信息 `"OpenAI/Ollama endpoints do not support assistant prefill"`
- **备选处理**：移除末尾 assistant 消息并记录 WARN 日志（可能改变语义，需谨慎）

### 2.5 tools 字段转换

```json
// Anthropic 工具定义
{
  "name": "get_weather",
  "description": "获取指定城市的天气",
  "input_schema": {
    "type": "object",
    "properties": {
      "location": {"type": "string", "description": "城市名称"}
    },
    "required": ["location"]
  },
  "cache_control": {"type": "ephemeral"}    // 丢弃
}

// → OpenAI 工具定义
{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "获取指定城市的天气",
    "parameters": {                          // input_schema → parameters
      "type": "object",
      "properties": {
        "location": {"type": "string", "description": "城市名称"}
      },
      "required": ["location"]
    }
    // strict 字段：Ollama 不支持，不注入
  }
}
```

**字段映射**：
| Anthropic | OpenAI | 说明 |
|-----------|--------|------|
| `name` | `function.name` | 直接透传 |
| `description` | `function.description` | 直接透传 |
| `input_schema` | `function.parameters` | 重命名，内容不变 |
| `cache_control` | — | **丢弃** |
| `strict` | `function.strict` | 可选透传（Ollama 可能不支持） |
| — | `type: "function"` | **固定注入** |

### 2.6 tool_choice 转换

| Anthropic | OpenAI | 说明 |
|-----------|--------|------|
| `{"type": "auto"}` | `"auto"` | 模型自主决定 |
| `{"type": "any"}` | `"required"` | 必须调用工具 |
| `{"type": "none"}` | `"none"` | 禁止工具调用 |
| `{"type": "tool", "name": "get_weather"}` | `{"type": "function", "function": {"name": "get_weather"}}` | 强制调用指定工具 |
| `{..., "disable_parallel_tool_use": true}` | `parallel_tool_calls: false`（单独字段） | 禁止并行工具调用 |

---

## 3. 响应转换：OpenAI → Anthropic（非流式）

### 3.1 完整字段映射

```json
// OpenAI 响应（输入）
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1677858242,
  "model": "llama3.2",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "响应文本",
      "tool_calls": null,
      "refusal": null
    },
    "finish_reason": "stop"
  }],
  "usage": {
    "prompt_tokens": 25,
    "completion_tokens": 15,
    "total_tokens": 40,
    "prompt_tokens_details": {"cached_tokens": 10}
  }
}

// → Anthropic 响应（输出）
{
  "id": "msg_abc123",           // 前缀替换：chatcmpl- → msg_
  "type": "message",            // 固定值
  "role": "assistant",          // 固定值
  "content": [
    {"type": "text", "text": "响应文本"}
  ],
  "model": "claude-opus-4-6",   // 映射回请求中的 Anthropic 模型名
  "stop_reason": "end_turn",    // finish_reason 转换
  "stop_sequence": null,        // 默认 null
  "usage": {
    "input_tokens": 15,         // prompt_tokens - cached_tokens（非缓存部分）
    "output_tokens": 15,        // completion_tokens
    "cache_read_input_tokens": 10,   // prompt_tokens_details.cached_tokens
    "cache_creation_input_tokens": 0 // 固定 0（OpenAI 无此概念）
  }
}
```

### 3.2 id 前缀替换

| OpenAI 前缀 | Anthropic 前缀 | 处理方式 |
|------------|---------------|---------|
| `chatcmpl-` | `msg_` | 替换前缀，保留后续部分 |
| 其他格式 | `msg_` | 保留原始 ID，前缀改为 `msg_` |

### 3.3 finish_reason → stop_reason 映射

| OpenAI finish_reason | Anthropic stop_reason | 说明 |
|---------------------|----------------------|------|
| `"stop"` | `"end_turn"` | 正常结束 |
| `"length"` | `"max_tokens"` | 达到 token 上限 |
| `"tool_calls"` | `"tool_use"` | 工具调用 |
| `"content_filter"` | `"end_turn"` | 内容被过滤（近似映射） |
| `"function_call"` | `"tool_use"` | 旧版工具调用（已废弃） |
| `null` | `"end_turn"` | 默认值 |

### 3.4 tool_calls 转换（非流式）

```json
// OpenAI 响应（含工具调用）
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [
        {
          "id": "call_DdmO9pD3",
          "type": "function",
          "function": {
            "name": "get_weather",
            "arguments": "{\"location\": \"Beijing\"}"
          }
        }
      ]
    },
    "finish_reason": "tool_calls"
  }]
}

// → Anthropic 响应
{
  "content": [
    {
      "type": "tool_use",
      "id": "call_DdmO9pD3",           // ID 原样保留（多轮时回传同一 ID）
      "name": "get_weather",
      "input": {"location": "Beijing"}  // arguments JSON 字符串 → 解析为对象
    }
  ],
  "stop_reason": "tool_use"
}
```

**混合内容**（文本 + 工具调用）：
```json
// OpenAI：content 有文本 + tool_calls
{"content": "我来查询天气。", "tool_calls": [...]}

// → Anthropic：text 块在前，tool_use 块在后
{
  "content": [
    {"type": "text", "text": "我来查询天气。"},
    {"type": "tool_use", "id": "...", "name": "...", "input": {...}}
  ]
}
```

### 3.5 usage 字段映射

| OpenAI 字段 | Anthropic 字段 | 计算方式 |
|------------|---------------|---------|
| `prompt_tokens` | `input_tokens` + `cache_read_input_tokens` = 总输入 | 需拆分（见下） |
| `completion_tokens` | `output_tokens` | 直接映射 |
| `prompt_tokens_details.cached_tokens` | `cache_read_input_tokens` | 直接映射 |
| — | `cache_creation_input_tokens` | 固定 `0`（OpenAI 不区分写入/读取） |

**input_tokens 计算**：
```
input_tokens = prompt_tokens - cached_tokens
cache_read_input_tokens = cached_tokens
cache_creation_input_tokens = 0
```

> **重要**：Anthropic 客户端的 tap 解析器会将这三个字段求和计算总输入。
> 若简单将 `prompt_tokens` → `input_tokens`，缓存命中的 token 会被**双重计数**。
> 必须正确拆分：`input_tokens` 仅为非缓存部分，`cache_read_input_tokens` 为缓存命中部分。

### 3.6 model 字段处理

Anthropic 响应的 `model` 字段应与请求中的模型一致。由于 Ollama 返回的是本地模型名（如 `llama3.2`），需映射回请求中的 Anthropic 模型名（如 `claude-opus-4-6`）。

**处理策略**：网关在请求上下文中保存原始 Anthropic 模型名，响应转换时使用保存值覆盖 Ollama 返回的模型名。

---

## 4. 响应转换：OpenAI → Anthropic（流式）

### 4.1 关键设计决策：输入 token 获取时机

**问题**：Anthropic 的 `message_start` 事件（流的第一个事件）需要 `input_tokens`，但 OpenAI 的 token 用量只在流的最后一个 chunk 中出现（usage chunk）。

**解决方案**：**两阶段缓冲策略**：
1. 缓冲所有 OpenAI SSE chunk 直到收到 `[DONE]`
2. 解析最后的 usage chunk 获取 `prompt_tokens`
3. 按正确顺序重新发出 Anthropic SSE 事件序列

**优缺点**：
- ✅ token 统计准确（计费关键）
- ✅ Anthropic 事件顺序完全正确
- ❌ 客户端看到"延迟一拍"——模型全部生成完毕后才开始接收流
- ✅ 对于 LAN 上的 Ollama，网络延迟极低，体验影响可忽略

**替代方案**（仅供参考）：真流式模式——`message_start` 的 `input_tokens` 设为 `0`，后续不再修正。此方案客户端体验好，但 token 统计不准确，不适合生产网关。

### 4.2 OpenAI 流事件 → Anthropic 流事件映射

```
OpenAI SSE 流                          →    Anthropic SSE 事件序列
─────────────────────────────────────────────────────────────────────────────

[缓冲阶段：全部收集]
data: {choices: [{delta: {role: "assistant"}}]}         ← 丢弃（提取 id、model）
data: {choices: [{delta: {content: "Hello"}}]}          ← 缓存文本
data: {choices: [{delta: {content: " World"}}]}         ← 缓存文本
data: {choices: [{delta: {}, finish_reason: "stop"}]}   ← 记录 finish_reason
data: {choices: [], usage: {prompt_tokens:25, ...}}     ← 提取 usage
data: [DONE]

[发射阶段：按序输出]
event: message_start
data: {"type":"message_start","message":{"id":"msg_xxx","type":"message",
       "role":"assistant","content":[],"model":"claude-opus-4-6",
       "stop_reason":null,"stop_sequence":null,
       "usage":{"input_tokens":15,"output_tokens":1,
                "cache_read_input_tokens":10,"cache_creation_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,
       "content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,
       "delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,
       "delta":{"type":"text_delta","text":" World"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},
       "usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
```

### 4.3 完整流转换状态机

```
初始状态
  │
  ▼ 收到第一个 OpenAI chunk
收集 id、model
  │
  ├── role: "assistant" → 进入文本收集模式
  │   │ 收到 content delta → 追加到文本缓冲
  │   │ 收到 finish_reason → 记录 stop_reason
  │   └──────────────────────────────────────
  │
  ├── tool_calls[i].id 出现 → 进入工具调用模式
  │   │ 收到 arguments delta → 按 index 追加到各工具的参数缓冲
  │   │ 收到 finish_reason: "tool_calls" → 记录
  │   └──────────────────────────────────────
  │
  ├── 收到 usage chunk（choices: []）→ 记录 prompt_tokens、completion_tokens、cached_tokens
  │
  └── 收到 [DONE] → 进入发射阶段

发射阶段：
  emit message_start（使用缓冲的 input_tokens）
  for each content block (text first, then tool_calls):
    emit content_block_start
    emit content_block_delta × N（按缓冲内容分片）
    emit content_block_stop
  emit message_delta（stop_reason + output_tokens）
  emit message_stop
```

### 4.4 流式工具调用转换

OpenAI 工具调用通过多个 chunk 的 `delta.tool_calls` 逐步传输，需要重建完整工具调用后再发射 Anthropic 事件：

```json
// OpenAI 流：工具调用 chunk 序列
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":null,"type":null,"function":{"name":null,"arguments":"{\"location\":"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":null,"type":null,"function":{"name":null,"arguments":"\"Beijing\"}"}}]}}]}
data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}
data: {"choices":[],"usage":{...}}
data: [DONE]

// → 缓冲阶段重建：
toolCalls[0] = {id: "call_abc", name: "get_weather", arguments: "{\"location\":\"Beijing\"}"}

// → 发射阶段（Anthropic SSE）：
event: message_start
data: {"type":"message_start","message":{...,"usage":{"input_tokens":N,...}}}

event: content_block_start
data: {"type":"content_block_start","index":0,
       "content_block":{"type":"tool_use","id":"call_abc","name":"get_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,
       "delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,
       "delta":{"type":"input_json_delta","partial_json":"\"Beijing\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},
       "usage":{"output_tokens":N}}

event: message_stop
data: {"type":"message_stop"}
```

### 4.5 并行工具调用流转换

当 OpenAI 返回多个并行工具调用（不同 `index`）时，每个工具调用对应一个独立的 Anthropic 内容块：

```
// OpenAI：index=0 和 index=1 的 chunks 交错出现

// → 发射阶段：按 index 顺序依次发射各工具块
event: content_block_start  (index=0, type=tool_use, id=call_A, name=weather)
event: content_block_delta × N  (index=0, input_json_delta)
event: content_block_stop   (index=0)

event: content_block_start  (index=1, type=tool_use, id=call_B, name=search)
event: content_block_delta × N  (index=1, input_json_delta)
event: content_block_stop   (index=1)
```

### 4.6 混合文本 + 工具调用流转换

当模型先输出文本后再调用工具时：

```
// 缓冲阶段收集：text_buffer = "我来查询天气。", toolCalls = [{...}]

// 发射阶段：
event: content_block_start  (index=0, type=text)
event: content_block_delta  (index=0, text="我来查询天气。")
event: content_block_stop   (index=0)

event: content_block_start  (index=1, type=tool_use)
event: content_block_delta × N  (index=1, input_json_delta)
event: content_block_stop   (index=1)
```

### 4.7 流式 message_start 中的 usage 计算

```
input_tokens          = prompt_tokens - cached_tokens
cache_read_input_tokens   = cached_tokens
cache_creation_input_tokens = 0
output_tokens（message_start） = 1      // 占位值，真实值在 message_delta
output_tokens（message_delta） = completion_tokens  // 最终准确值
```

---

## 5. 错误转换

### 5.1 上游 HTTP 错误（Ollama 返回错误）

OpenAI 格式的错误响应需转换为 Anthropic 格式：

```json
// OpenAI 错误（输入）
{
  "error": {
    "message": "The model `gpt-4` does not exist",
    "type": "invalid_request_error",
    "param": null,
    "code": "model_not_found"
  }
}

// → Anthropic 错误（输出）
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "The model `gpt-4` does not exist"
  }
}
```

**字段映射**：

| OpenAI | Anthropic | 说明 |
|--------|-----------|------|
| `error.message` | `error.message` | 直接透传 |
| `error.type` | `error.type` | 直接透传（部分需映射） |
| `error.param` | — | 丢弃（Anthropic 无此字段） |
| `error.code` | — | 丢弃（Anthropic 无此字段，信息已含在 message 中） |
| — | `type: "error"` | **固定注入**（Anthropic 根层级字段） |

**error.type 映射**：

| OpenAI error.type | Anthropic error.type | HTTP 状态码 |
|-------------------|---------------------|------------|
| `invalid_request_error` | `invalid_request_error` | 400 |
| `authentication_error` | `authentication_error` | 401 |
| `permission_error` | `permission_error` | 403 |
| `rate_limit_error` | `rate_limit_error` | 429 |
| `insufficient_quota` | `rate_limit_error` | 429（Anthropic 无专用类型） |
| `server_error` | `api_error` | 500 |
| `api_error` | `api_error` | 502 |

### 5.2 流式中途错误

OpenAI 流中途错误通过 HTTP 错误码（非 SSE 事件）返回；
Anthropic 支持流中途 `event: error` 事件（HTTP 200 + 内联错误）。

**处理策略**：若上游在流开始后断开或返回错误，转换层应：
1. 发射 `event: error` 事件（Anthropic 格式）
2. 不发射 `message_stop`（客户端知道流异常终止）

```
event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Upstream connection lost"}}
```

### 5.3 网关自生成错误

当转换层自身检测到错误（如 prefill 不支持、thinking 参数拒绝），直接返回 Anthropic 格式错误，不转发到上游：

```json
// 返回 HTTP 400
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "Extended thinking (thinking parameter) is not supported for OpenAI/Ollama targets"
  }
}
```

---

## 6. 边缘情况处理

### 6.1 模型名映射

Anthropic 客户端发送 Anthropic 模型名（`claude-opus-4-6`），Ollama 使用本地模型名（`llama3.2`、`qwen2.5`、`deepseek-r1` 等）。

**处理方案**：在 LLM target 配置中增加 `model_mapping` 字段：
```yaml
llm:
  targets:
    - url: "http://localhost:11434"
      provider: "ollama"
      model_mapping:
        "claude-opus-4-6": "llama3.2"
        "claude-sonnet-4-6": "qwen2.5:14b"
        "*": "llama3.2"   # 默认映射
```

若无映射配置，直接透传原始模型名（适合 Ollama 使用 Anthropic 模型名作为别名的情况）。

### 6.2 `[DONE]` 检测

OpenAI 流的结束标志 `data: [DONE]` **不是 JSON**，严禁 JSON.parse。转换层必须用字符串比较：
```go
if string(payload) == "[DONE]" {
    // 开始发射阶段
}
```

### 6.3 空 content 响应

某些情况下 OpenAI 响应 `content: null`（仅工具调用时）。Anthropic 的 `content` 字段应为数组（可以不含 text 块）：
```json
// 正确的 Anthropic 响应（仅工具调用，无文本）
{
  "content": [
    {"type": "tool_use", "id": "...", "name": "...", "input": {...}}
  ]
}
```

### 6.4 工具 arguments JSON 校验

OpenAI 流式工具调用的 `arguments` 是分片传输的，拼接后需验证是否为合法 JSON：
```go
var input map[string]interface{}
if err := json.Unmarshal([]byte(args), &input); err != nil {
    // 记录 WARN，使用空 map {} 作为 fallback
    input = map[string]interface{}{}
}
```

### 6.5 usage chunk 缺失

Ollama 某些版本可能不在流式响应中包含 usage 信息（即便注入了 `stream_options.include_usage: true`）。

**处理策略**：
- 若收到 `[DONE]` 前未见 usage chunk，在 `message_start` 中使用 `{input_tokens:0, output_tokens:1}`
- 在 `message_delta` 中使用 `{output_tokens:0}`（无法准确统计）
- 记录 WARN 日志：`"upstream did not include usage in stream response"`

### 6.6 Refusal 响应

OpenAI 的 `message.refusal` 字段（模型拒绝回答时的说明）无 Anthropic 对应。

**处理方案**：将 refusal 文本作为 text 块添加到 content 数组，前缀标记：
```json
{"type": "text", "text": "[Refusal] The model declined to answer: <refusal_message>"}
```

### 6.7 document 类型内容块

Anthropic 的 `document` 内容块无 OpenAI 对应。
**处理方案**：提取文档的 `context` 或 `title`，将文档内容转为 text 块：
```json
// Anthropic
{"type": "document", "title": "Doc", "source": {"type": "text", "text": "文档内容..."}}

// → OpenAI（退化为文本）
{"type": "text", "text": "[Document: Doc]\n文档内容..."}
```

### 6.8 redacted_thinking 块

Anthropic 的 `redacted_thinking` 是多轮对话中透传的加密思考内容，发往 OpenAI/Ollama 时**必须丢弃**（OpenAI 无此概念）：
```go
case "redacted_thinking", "thinking":
    // 丢弃，记录 DEBUG 日志
    continue
```

### 6.9 n > 1（多选项生成）

Anthropic 协议不支持 `n > 1`（只有一个响应）。OpenAI 支持但 Claude CLI 不会发送 `n > 1`。
若请求中携带 `n > 1`，转换时丢弃该字段，只取 `choices[0]` 转换响应。

### 6.10 并发内容块数量限制

OpenAI `tool_calls` 数组最多同时有多个并行工具调用；Anthropic 内容块数组无数量限制。按实际工具数量生成对应数量的内容块即可。

---

## 7. 当前实现 vs 本设计 Gap 分析

下表记录 `protocol_converter.go` 的实现状态（最后更新：v2.9.0）：

| 功能点 | 实现状态 | 设计参考 | 备注 |
|--------|---------|---------|------|
| **请求转换（Anthropic → OpenAI）** | | | |
| 基础文本请求转换 | ✅ 已实现 | §2.1 | — |
| system 字段转换 | ✅ 已实现 | §2.3 | — |
| 流式 stream_options 注入 | ✅ 已实现 | §2.1 | 自动注入 `include_usage:true` |
| temperature/top_p/stop_sequences 转换 | ✅ 已实现 | §2.2 | `stop_sequences` → `stop` |
| top_k 丢弃 | ✅ 已实现 | §2.2 | 隐式丢弃（不拷贝到 OpenAI 请求） |
| tools 字段转换（input_schema→parameters） | ✅ 已实现 | §2.5 | 含 `cache_control` 丢弃 |
| tool_choice 转换 | ✅ 已实现 | §2.6 | 含 `disable_parallel_tool_use` → `parallel_tool_calls:false` |
| assistant 消息中 tool_use 块 → tool_calls | ✅ 已实现 | §2.4.3 | input 对象 → arguments JSON 字符串 |
| user 消息中 tool_result 块 → tool 消息 | ✅ 已实现 | §2.4.4 | 拆分为独立 role=tool 消息 |
| 图片内容块转换（base64/url） | ✅ 已实现 | §2.4.2 | 转 OpenAI image_url 格式 |
| prefill 末尾 assistant 消息拒绝 | ✅ 已实现 | §2.4.6 | 返回 HTTP 400 |
| thinking 参数拒绝 | ✅ 已实现 | §6.8 | 返回 HTTP 400 |
| thinking/redacted_thinking 块丢弃 | ✅ 已实现 | §6.8 | 静默丢弃，不转发给 OpenAI |
| **非流式响应转换（OpenAI → Anthropic）** | | | |
| 响应 id 前缀替换 | ✅ 已实现 | §3.1 | `chatcmpl-xxx` → `msg_xxx` |
| stop_reason 完整映射 | ✅ 已实现 | §3.3 | stop/length/tool_calls/content_filter 全部覆盖 |
| tool_calls → tool_use 内容块 | ✅ 已实现 | §3.4 | arguments JSON 字符串 → input 对象 |
| usage cached_tokens 拆分 | ✅ 已实现 | §3.5 | input_tokens = prompt_tokens − cached_tokens |
| model 字段映射回 Anthropic 名 | ✅ 已实现 | §3.6 | requestedModel 参数覆盖 OpenAI 返回的模型名 |
| **流式响应转换（OpenAI → Anthropic）** | | | |
| 文本内容渐进发射 | ✅ 已实现 | §4.2 | 逐 chunk 即时发射 |
| finish_reason → stop_reason 映射 | ✅ 已实现 | §4.3 | 通过 convertFinishReason() 含 content_filter |
| 工具调用流式转换 | ✅ 已实现 | §4.4 | 含并行工具调用、input_json_delta |
| input_tokens 准确值 | ✅ 已实现 | §4.1 | 采用渐进策略：message_start 占位为 0，message_delta 携带准确值¹ |
| cached_tokens → cache_read_input_tokens | ✅ 已实现 | §4.7 | 在 message_delta usage 中发射 |
| **错误与通用处理** | | | |
| 错误响应格式转换 | ✅ 已实现 | §5.1 | insufficient_quota→rate_limit_error；server_error→api_error |
| 模型名映射配置 | ✅ 已实现 | §6.1 | 精确匹配 + 通配符 `"*"` |

**¹ 流式 input_tokens 实现说明**：本设计（§4.1）建议缓冲所有 chunk 后再发射 message_start（含准确 input_tokens）。实际采用渐进策略：message_start 发射时 input_tokens=0（占位，维持低延迟），message_delta（流尾）补充完整的 input_tokens、cache_read_input_tokens、cache_creation_input_tokens。服务端计费由 TeeResponseWriter 直接解析原始 OpenAI SSE 获取，不受客户端侧 message_start 占位值影响。

---

## 8. 实现建议

### 8.1 流式转换架构调整

将现有的"实时转换"改为"缓冲后发射"：

```go
type OpenAIToAnthropicStreamConverter struct {
    writer        http.ResponseWriter
    logger        *zap.Logger
    reqID         string
    anthropicModel string          // 请求中的原始 Anthropic 模型名

    // 缓冲区
    buf           bytes.Buffer      // 原始 SSE 数据
    messageID     string
    textBuffer    strings.Builder
    toolCalls     map[int]*toolCallBuffer  // index → 工具调用缓冲
    finishReason  string
    usage         *openaiUsage
}

// Write 接收 OpenAI SSE chunk，缓冲所有数据
func (c *OpenAIToAnthropicStreamConverter) Write(chunk []byte) (int, error) {
    // 逐行解析，填充缓冲区各字段
    // [DONE] 时触发 flush()
}

// flush 收到 [DONE] 后，按序发射 Anthropic SSE 事件
func (c *OpenAIToAnthropicStreamConverter) flush() {
    c.emitMessageStart()
    c.emitContentBlocks()
    c.emitMessageDelta()
    c.emitMessageStop()
}
```

### 8.2 请求转换扩展

扩展 `convertAnthropicToOpenAIRequest` 支持工具调用消息：

```go
// 处理 messages 时，检测内容块类型
for _, msg := range messages {
    switch msg.Role {
    case "assistant":
        // 检测是否含 tool_use 块
        openaiMsg := buildAssistantMessage(msg.Content)
    case "user":
        // 检测是否含 tool_result 块
        openaiMsgs := expandUserMessage(msg.Content) // 可能返回多条消息
    }
}
```

### 8.3 推荐测试用例矩阵

| 场景 | 请求类型 | 工具调用 | 多模态 | 期望行为 |
|------|----------|---------|--------|---------|
| 基础文本（非流式） | 非流式 | 无 | 无 | 正确转换 content 和 usage |
| 基础文本（流式） | 流式 | 无 | 无 | input_tokens 准确；事件序列完整 |
| 单工具调用（非流式） | 非流式 | 有 | 无 | tool_use 块正确；stop_reason=tool_use |
| 单工具调用（流式） | 流式 | 有 | 无 | tool_use 块正确；input_json_delta 正确 |
| 并行工具调用 | 流式 | 多个 | 无 | 多个 content block，顺序正确 |
| 多轮工具对话 | 任意 | 多轮 | 无 | tool_result → tool 消息，ID 一致 |
| 图片消息 | 非流式 | 无 | 有 | base64 data URI 正确构建 |
| 文本+图片 | 流式 | 无 | 有 | content 数组正确 |
| usage 缺失 | 流式 | 无 | 无 | WARN 日志；tokens=0（不崩溃） |
| Ollama 错误 400 | — | — | — | Anthropic 格式错误响应 |
| Ollama 流中断 | 流式 | — | — | error 事件，无 message_stop |

---

## 9. 协议差异速查表

| 维度 | Anthropic | OpenAI/Ollama | 转换方向 |
|------|-----------|--------------|---------|
| 端点路径 | `/v1/messages` | `/v1/chat/completions` | 替换 |
| 认证头 | `x-api-key` | `Authorization: Bearer` | 替换 |
| 版本头 | `anthropic-version`（必填） | 无 | 删除 |
| max_tokens | 必填 | 可选 | 透传 |
| 系统提示位置 | 独立 `system` 字段 | messages[0] role=system | 提取并插入 |
| 工具参数字段名 | `input_schema` | `parameters` | 重命名 |
| 工具参数数据 | `input`（对象） | `arguments`（JSON字符串） | 序列化/反序列化 |
| tool_choice any | `{"type":"any"}` | `"required"` | 映射 |
| 工具结果消息 | user 消息内的 content 块 | 独立的 role=tool 消息 | 拆分提取 |
| 流式结束标志 | `event: message_stop` | `data: [DONE]`（非JSON） | 检测字符串 |
| 流式事件行格式 | `event: xxx\ndata: yyy\n\n` | `data: yyy\n\n`（无 event 行） | 加/去 event 行 |
| 输入 token 统计 | 分三字段（input+cache_read+cache_creation） | prompt_tokens（含缓存） | 拆分 |
| 输入 token 时机（流式） | message_start（流首） | usage chunk（流尾） | 缓冲后重放 |
| 输出 token 时机（流式） | message_delta | usage chunk | 映射 |
| stop_reason/finish_reason | end_turn/max_tokens/tool_use | stop/length/tool_calls | 映射 |
| 错误根结构 | `{"type":"error","error":{...}}` | `{"error":{...}}` | 包装 |
| 缓存 token 控制 | 显式 `cache_control` 标记 | 自动（无需配置） | 丢弃标记 |
| top_k 参数 | 支持 | 不支持 | 丢弃 |
| 扩展思考 | `thinking` 参数 | 不支持 | 拒绝/丢弃 |
