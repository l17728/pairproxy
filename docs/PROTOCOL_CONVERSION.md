# Protocol Conversion: Anthropic ↔ OpenAI

**版本**: v2.8.0
**日期**: 2026-03-11
**状态**: Implemented

---

## 概述

PairProxy 现在支持自动协议转换，允许 Claude CLI（使用 Anthropic Messages API）连接到 Ollama 或其他 OpenAI 兼容的后端。

### 使用场景

企业环境中常见的部署模式：
- **客户端**: Claude CLI（Anthropic 协议）
- **后端**: Ollama 本地部署（OpenAI 协议）
- **需求**: 自动协议转换，对用户透明

---

## 工作原理

### 自动检测

协议转换在以下条件下自动触发：
1. 请求路径为 `/v1/messages`（Anthropic 格式）
2. 目标 LLM 的 `provider` 字段为 `"ollama"` 或 `"openai"`

### 转换流程

```
Claude CLI → PairProxy → Ollama
   (Anthropic)    ↓ 转换 ↓    (OpenAI)

请求转换:
  /v1/messages → /v1/chat/completions
  Anthropic JSON → OpenAI JSON

响应转换:
  OpenAI JSON → Anthropic JSON (非流式)
  OpenAI SSE → Anthropic SSE (流式)
```

---

## 配置示例

### sproxy.yaml

```yaml
llm:
  targets:
    # Ollama 本地部署（需要协议转换）
    - url: "http://localhost:11434"
      api_key: "ollama"  # Ollama 不需要真实 API Key
      provider: "ollama"
      name: "Ollama Local"
      weight: 1

    # Anthropic 官方 API（无需转换）
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      provider: "anthropic"
      name: "Anthropic Claude"
      weight: 1
```

### 使用 Claude CLI

```bash
# Claude CLI 配置（使用 Anthropic 协议）
export ANTHROPIC_API_KEY="<pairproxy-jwt>"
export ANTHROPIC_BASE_URL="http://localhost:9000"

# 发送请求（自动转换）
claude "What is 2+2?"
```

---

## 协议差异

### 请求格式

**Anthropic Messages API**:
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "max_tokens": 1024,
  "system": "You are a helpful assistant.",
  "messages": [
    {
      "role": "user",
      "content": "Hello!"
    }
  ]
}
```

**OpenAI Chat Completions API**:
```json
{
  "model": "claude-3-5-sonnet-20241022",
  "max_tokens": 1024,
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful assistant."
    },
    {
      "role": "user",
      "content": "Hello!"
    }
  ],
  "stream_options": {
    "include_usage": true
  }
}
```

### 响应格式（非流式）

**Anthropic**:
```json
{
  "id": "msg_123",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "Hello! How can I help you?"
    }
  ],
  "usage": {
    "input_tokens": 10,
    "output_tokens": 8
  },
  "stop_reason": "end_turn"
}
```

**OpenAI**:
```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "model": "gpt-4",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 8,
    "total_tokens": 18
  }
}
```

### 响应格式（流式）

**Anthropic SSE**:
```
event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","usage":{"output_tokens":8}}

event: message_stop
data: {"type":"message_stop"}
```

**OpenAI SSE**:
```
data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"},"finish_reason":null}],"usage":null}

data: {"id":"chatcmpl-123","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":8,"total_tokens":18}}

data: [DONE]
```

---

## 实现细节

### 核心文件

1. **`internal/proxy/protocol_converter.go`**
   - `shouldConvertProtocol()` - 检测是否需要转换
   - `convertAnthropicToOpenAIRequest()` - 请求转换
   - `convertOpenAIToAnthropicResponse()` - 非流式响应转换
   - `OpenAIToAnthropicStreamConverter` - 流式响应转换器

2. **`internal/proxy/sproxy.go`**
   - 集成转换逻辑到代理流程
   - 在 Director 中修改请求路径
   - 在 ModifyResponse 中转换响应

### 转换逻辑

#### 请求转换

1. **路径转换**: `/v1/messages` → `/v1/chat/completions`
2. **System 字段**: 从顶层移到 messages 数组首位
3. **Content 结构化**: 提取 text 类型的 content blocks
4. **Stream Options**: 自动注入 `stream_options.include_usage: true`

#### 响应转换（非流式）

1. **结构映射**:
   - `choices[0].message.content` → `content[0].text`
   - `usage.prompt_tokens` → `usage.input_tokens`
   - `usage.completion_tokens` → `usage.output_tokens`
   - `finish_reason` → `stop_reason`

2. **Stop Reason 映射**:
   - `"stop"` → `"end_turn"`
   - `"length"` → `"max_tokens"`

#### 响应转换（流式）

实时转换 OpenAI SSE 事件为 Anthropic SSE 事件：

1. **message_start**: 首次接收到 delta 时发送
2. **content_block_start**: 首次接收到 content 时发送
3. **content_block_delta**: 每个 content delta 转换
4. **message_delta**: 接收到 usage 时发送
5. **message_stop**: 接收到 `[DONE]` 时发送

---

## 日志示例

### 检测到协议转换

```
INFO  sproxy  protocol conversion required
  request_id: abc123
  from: anthropic
  to: openai
  target_provider: ollama
```

### 请求转换成功

```
INFO  sproxy  request converted successfully
  request_id: abc123
  new_path: /v1/chat/completions
  converted_size: 256
```

### 响应转换

```
DEBUG sproxy  non-streaming response converted
  request_id: abc123
  converted_size: 512
```

---

## 错误处理

### 降级策略

协议转换失败时，PairProxy 会**静默降级**，转发原始请求：

1. **请求转换失败**: 转发原始 Anthropic 请求（可能导致后端报错）
2. **响应转换失败**: 转发原始 OpenAI 响应（客户端可能无法解析）

### 日志记录

所有转换失败均记录 WARN 级别日志：

```
WARN  sproxy  protocol conversion failed, forwarding original request
  request_id: abc123
  error: json: cannot unmarshal...
```

---

## 性能影响

### 开销分析

1. **请求转换**: ~0.1ms（JSON 解析 + 重组）
2. **非流式响应转换**: ~0.1ms
3. **流式响应转换**: 实时转换，无额外延迟

### 内存占用

- 请求/响应 body 需要完整读取到内存
- 流式转换使用流式处理，内存占用最小

---

## 测试

### 单元测试

```bash
go test ./internal/proxy -run "Protocol|Converter" -v
```

**覆盖场景**:
- 检测逻辑（5 个测试）
- 请求转换（8 个测试，含图片块、model_mapping）
- 响应转换（4 个测试，含 chatcmpl- 前缀替换）
- 流式转换（3 个测试）
- 完整转换流程（1 个测试）
- model_mapping（5 个测试）
- prefill/thinking 拒绝（1 个测试）

### 手动测试

```bash
# 1. 启动 Ollama
ollama serve

# 2. 配置 sproxy.yaml（添加 Ollama target）
# 3. 启动 sproxy
./sproxy start

# 4. 使用 Claude CLI 测试
export ANTHROPIC_API_KEY="<your-pairproxy-jwt>"
export ANTHROPIC_BASE_URL="http://localhost:9000"
claude "What is the capital of France?"

# 5. 检查日志确认协议转换
tail -f sproxy.log | grep "protocol conversion"
```

---

## 限制与注意事项

### 当前限制

1. **单向转换**: 仅支持 Anthropic → OpenAI，不支持反向
2. **工具调用**: 不支持 function calling / tool use 转换

### v2.8.0 新增支持

- ✅ **图片内容块**: Anthropic base64 image → OpenAI image_url 格式
- ✅ **错误格式转换**: OpenAI 错误响应 → Anthropic 格式
- ✅ **ID 前缀替换**: `chatcmpl-` → `msg_`
- ✅ **model_mapping 配置**: 支持精确匹配 + 通配符 `*` 回退
- ✅ **prefill 拒绝**: assistant turn 末尾 prefill → HTTP 400（OpenAI/Ollama 不支持）
- ✅ **thinking 参数拒绝**: thinking block 请求 → HTTP 400
- ✅ **强制 LLM 绑定**: 协议转换请求必须有 user/group 绑定，否则 HTTP 403

### 兼容性

- ✅ Ollama（完全兼容）
- ✅ OpenAI API（完全兼容）
- ✅ 其他 OpenAI 兼容 API（如 vLLM、LocalAI）
- ❌ 需要特殊认证的 API（如 Azure OpenAI）

---

## 故障排查

### 问题：转换后请求失败

**症状**: 后端返回 400 Bad Request

**排查步骤**:
1. 检查日志确认转换是否成功
2. 启用 debug 日志查看转换后的请求体
3. 验证后端是否真正支持 OpenAI 格式

**解决方案**:
- 确认 `provider` 字段配置正确
- 检查后端 API 文档确认格式要求

### 问题：流式响应中断

**症状**: 客户端接收部分响应后断开

**排查步骤**:
1. 检查 sproxy 日志是否有转换错误
2. 验证后端是否正确发送 `[DONE]` 标记
3. 检查网络连接稳定性

**解决方案**:
- 确保后端正确实现 OpenAI SSE 格式
- 检查防火墙/代理是否干扰 SSE 连接

### 问题：Token 计数为 0

**症状**: usage_logs 表中 token 数为 0

**排查步骤**:
1. 检查后端是否返回 usage 字段
2. 验证流式请求是否包含 `stream_options.include_usage: true`
3. 检查响应转换是否正确提取 usage

**解决方案**:
- 确认后端支持 usage 统计
- 检查转换逻辑是否正确映射 usage 字段

---

## 未来增强

### 计划功能

1. **双向转换**: 支持 OpenAI → Anthropic
2. **工具调用转换**: 支持 function calling 格式转换
3. **配置化**: 允许禁用自动转换，手动指定转换规则

### 性能优化

1. **零拷贝转换**: 使用 JSON streaming 减少内存分配
2. **并行转换**: 流式响应并行处理多个 chunk
3. **缓存**: 缓存常见请求的转换结果

---

## 参考资料

- [Anthropic Messages API 文档](https://docs.anthropic.com/claude/reference/messages_post)
- [OpenAI Chat Completions API 文档](https://platform.openai.com/docs/api-reference/chat)
- [Ollama API 文档](https://github.com/ollama/ollama/blob/main/docs/api.md)
- PairProxy 源码: `internal/proxy/protocol_converter.go`

---

**实现完成，功能已集成到 v2.6.0。**
