# Protocol Conversion: Anthropic ↔ OpenAI

**版本**: v2.10.0
**日期**: 2026-03-14
**状态**: Implemented（双向完整支持）

---

## 概述

PairProxy 支持双向自动协议转换，覆盖两种企业常见场景：

| 方向 | 简称 | 客户端 | 目标后端 | 引入版本 |
|------|------|--------|----------|----------|
| Anthropic → OpenAI | **AtoO** | Claude CLI / claude-code | Ollama、vLLM、OpenAI 兼容后端 | v2.6.0 |
| OpenAI → Anthropic | **OtoA** | Cursor、Continue.dev、任意 OpenAI 兼容客户端 | Anthropic API | v2.10.0 |

---

## AtoO 方向（Anthropic → OpenAI）

### 触发条件

同时满足以下两个条件时自动触发：
1. 请求路径为 `/v1/messages`（Anthropic 格式）
2. 目标 LLM 的 `provider` 字段为 `"ollama"` 或 `"openai"`

### 转换流程

```
Claude CLI → PairProxy → Ollama / OpenAI 兼容后端
   (Anthropic)    ↓ AtoO ↓    (OpenAI)

请求转换:
  /v1/messages → /v1/chat/completions
  system 字段 → messages[0] {role: "system"}
  content blocks → message content
  tool_choice / tools → OpenAI function 格式
  注入 stream_options.include_usage: true

响应转换:
  choices[0].message.content → content[0].text
  finish_reason (stop/length) → stop_reason (end_turn/max_tokens)
  chatcmpl- 前缀 → msg_ 前缀
  usage.prompt_tokens → input_tokens
  usage.completion_tokens → output_tokens
```

### 配置示例

```yaml
llm:
  targets:
    - url: "http://localhost:11434"
      api_key: "ollama"
      provider: "ollama"         # 触发 AtoO 转换
      name: "Ollama Local"
      weight: 1
```

### Claude CLI 使用

```bash
export ANTHROPIC_API_KEY="<pairproxy-jwt>"
export ANTHROPIC_BASE_URL="http://localhost:9000"
claude "What is 2+2?"
```

### 特殊处理

- **图片内容块**: Anthropic base64 image → OpenAI `image_url` 格式
- **model_mapping**: 支持精确匹配 + 通配符 `*` 回退映射
- **prefill 拒绝**: messages 末尾有 assistant turn 时返回 HTTP 400
- **thinking 参数**: 静默剥离（v2.9.1+，不再 400 拒绝）
- **强制 LLM 绑定**: 协议转换请求必须有 user/group 绑定，否则 HTTP 403

---

## OtoA 方向（OpenAI → Anthropic）

### 触发条件（v2.10.0+）

同时满足以下两个条件时自动触发：
1. 请求路径为 `/v1/chat/completions`（OpenAI 格式）
2. 目标 LLM 的 `provider` 字段为 `"anthropic"`

### 转换流程

```
OpenAI 客户端 → PairProxy → Anthropic API
/v1/chat/completions   ↓ OtoA ↓   /v1/messages
   (OpenAI JSON)                   (Anthropic JSON)

请求转换:
  messages[{role:"system"}] → 顶层 system 字段
  其余 messages → messages 数组
  tools (function) → Anthropic tool 格式
  stop (字符串/数组) → stop_sequences 数组
  model 名称映射（通过 model_mapping 配置）

响应转换（非流式）:
  id: chatcmpl-xxx → msg_xxx（前缀替换）
  finish_reason (stop/length) → stop_reason (end_turn/max_tokens)
  usage.prompt_tokens → input_tokens
  usage.completion_tokens → output_tokens
  tool_calls → tool_use content block

响应转换（流式）:
  AnthropicToOpenAIStreamConverter 实时转换:
  message_start → {id, model, object, choices:[]}
  content_block_delta → choices[0].delta.content
  input_json_delta → choices[0].delta.tool_calls
  message_delta → usage chunk
  message_stop → [DONE]
```

### 配置示例

```yaml
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"      # 触发 OtoA 转换（当客户端用 /v1/chat/completions）
      name: "Anthropic Claude"
      weight: 1
```

### OpenAI 客户端使用

```bash
# Cursor / Continue.dev / 任意 OpenAI 兼容客户端
export OPENAI_API_KEY="<pairproxy-jwt>"
export OPENAI_BASE_URL="http://localhost:9000"

# curl 示例
curl -X POST http://localhost:9000/v1/chat/completions \
  -H "Authorization: Bearer <pairproxy-jwt>" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet-20241022",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

### 防双计费保护

Anthropic 后端在响应中会返回真实 token 用量。系统使用 `otoaRecorded` 标志确保
token 仅记录一次，不会因格式转换而重复统计。

---

## 内部实现

### conversionDirection 枚举（v2.10.0）

```go
type conversionDirection int

const (
    conversionNone conversionDirection = iota  // 不转换
    conversionAtoO                             // Anthropic → OpenAI
    conversionOtoA                             // OpenAI → Anthropic
)
```

### 核心文件

| 文件 | 说明 |
|------|------|
| `internal/proxy/protocol_converter.go` | 所有转换函数 |
| `internal/proxy/sproxy.go` | Director + ModifyResponse 集成点 |

### 关键函数

| 函数 | 方向 | 说明 |
|------|------|------|
| `detectConversionDirection()` | — | 判断转换方向（枚举） |
| `convertAnthropicToOpenAIRequest()` | AtoO | 请求转换 |
| `convertOpenAIToAnthropicResponse()` | AtoO | 非流式响应转换 |
| `OpenAIToAnthropicStreamConverter` | AtoO | 流式响应转换器 |
| `convertOpenAIToAnthropicRequest()` | OtoA | 请求转换 |
| `convertAnthropicToOpenAIResponseReverse()` | OtoA | 非流式响应转换 |
| `convertAnthropicErrorResponseToOpenAI()` | OtoA | 错误响应转换 |
| `AnthropicToOpenAIStreamConverter` | OtoA | 流式响应转换器 |

---

## 日志示例

```
INFO  sproxy  protocol conversion required
  request_id: abc123
  direction: AtoO          # 或 OtoA
  from: anthropic
  to: openai
  target_provider: ollama

INFO  sproxy  request converted successfully
  request_id: abc123
  new_path: /v1/chat/completions
  converted_size: 256

DEBUG sproxy  non-streaming response converted
  request_id: abc123
  converted_size: 512
```

---

## 错误处理

| 场景 | 行为 |
|------|------|
| 转换失败 | WARN 日志 + 转发原始请求 |
| 无 LLM 绑定（协议转换请求）| HTTP 403 |
| messages 末尾有 prefill（AtoO）| HTTP 400 |

---

## 性能

- **请求/非流式响应转换**: ~0.1ms（JSON 解析 + 重组）
- **流式转换**: 实时处理，无额外延迟
- **内存**: body 需完整读入内存；流式转换使用流式处理

---

## 测试

```bash
go test ./internal/proxy -run "Protocol|Converter|OtoA|AtoO" -v
```

覆盖场景（截至 v2.10.0，共 45+ 个协议转换测试）：
- AtoO 请求/响应转换（含图片块、model_mapping、工具调用）
- OtoA 请求/响应转换（含工具调用、stop_sequences、model mapping）
- OtoA 流式转换（AnthropicToOpenAIStreamConverter 全事件类型）
- prefill/thinking 拒绝
- 防双计费（otoaRecorded）
- 强制 LLM 绑定

---

## 故障排查

### 转换后请求失败（400）

1. 检查日志确认 `direction` 字段值是否符合预期
2. 确认 `provider` 字段配置正确
3. 对 AtoO：验证后端是否真正支持 OpenAI 格式

### 流式响应中断

1. 检查 sproxy 日志是否有转换错误
2. AtoO：验证后端是否正确发送 `[DONE]` 标记
3. OtoA：验证 Anthropic 端是否正确发送 `message_stop` 事件

### Token 计数为 0

- AtoO：确认后端返回 `usage` 字段，且请求注入了 `stream_options.include_usage: true`
- OtoA：确认 `otoaRecorded` 正常工作（查看 DEBUG 日志）

---

## 参考资料

- [Anthropic Messages API](https://docs.anthropic.com/claude/reference/messages_post)
- [OpenAI Chat Completions API](https://platform.openai.com/docs/api-reference/chat)
- [Ollama API](https://github.com/ollama/ollama/blob/main/docs/api.md)
- PairProxy 源码: `internal/proxy/protocol_converter.go`

---

**AtoO 在 v2.6.0 完成，OtoA 在 v2.10.0 完成，双向协议转换现已完整支持。**
