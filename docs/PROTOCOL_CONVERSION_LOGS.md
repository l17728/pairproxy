# 协议转换日志示例

本文档展示 PairProxy 协议转换功能的完整日志输出，帮助理解转换流程和故障排查。

---

## 完整转换流程日志

### 场景：Claude CLI → Ollama（非流式请求）

```log
# 1. 检测到需要协议转换
2026-03-09T15:30:00.123+0800  INFO   sproxy  protocol conversion required
  request_id: abc123def456
  from: anthropic
  to: openai
  target_provider: ollama
  target_url: http://localhost:11434
  original_path: /v1/messages

# 2. 请求转换成功
2026-03-09T15:30:00.125+0800  INFO   sproxy  converted Anthropic request to OpenAI format
  request_id: abc123def456
  original_size: 256
  converted_size: 312
  message_count: 2
  has_system: true
  is_streaming: false

# 3. 请求转换成功（汇总）
2026-03-09T15:30:00.126+0800  INFO   sproxy  request converted successfully
  request_id: abc123def456
  new_path: /v1/chat/completions
  converted_size: 312

# 4. Director 修改请求路径
2026-03-09T15:30:00.127+0800  DEBUG  sproxy  request path converted for protocol conversion
  request_id: abc123def456
  original_path: /v1/messages
  converted_path: /v1/chat/completions
  target: http://localhost:11434

# 5. 代理请求到 LLM
2026-03-09T15:30:00.128+0800  DEBUG  sproxy  proxying request to LLM
  request_id: abc123def456
  user_id: user_001
  target: http://localhost:11434
  path: /v1/chat/completions
  method: POST

# 6. 接收到 LLM 响应
2026-03-09T15:30:01.234+0800  INFO   sproxy  LLM response received
  request_id: abc123def456
  user_id: user_001
  status: 200
  streaming: false
  duration_ms: 1106

# 7. 开始转换响应
2026-03-09T15:30:01.235+0800  DEBUG  sproxy  converting non-streaming response
  request_id: abc123def456
  original_size: 512

# 8. 响应转换成功
2026-03-09T15:30:01.236+0800  DEBUG  sproxy  converted OpenAI response to Anthropic format
  request_id: abc123def456
  original_size: 512
  converted_size: 468
  stop_reason: end_turn

# 9. 响应转换成功（汇总）
2026-03-09T15:30:01.237+0800  INFO   sproxy  non-streaming response converted successfully
  request_id: abc123def456
  converted_size: 468
```

---

## 流式请求日志

### 场景：Claude CLI → Ollama（流式请求）

```log
# 1. 检测到需要协议转换（流式）
2026-03-09T15:35:00.123+0800  INFO   sproxy  protocol conversion required
  request_id: xyz789abc123
  from: anthropic
  to: openai
  target_provider: ollama
  target_url: http://localhost:11434
  original_path: /v1/messages

# 2. 请求转换成功（流式，自动注入 stream_options）
2026-03-09T15:35:00.125+0800  INFO   sproxy  converted Anthropic request to OpenAI format
  request_id: xyz789abc123
  original_size: 256
  converted_size: 348
  message_count: 1
  has_system: false
  is_streaming: true

# 3. 插入流转换器
2026-03-09T15:35:00.126+0800  DEBUG  sproxy  stream converter inserted for protocol conversion
  request_id: xyz789abc123

# 4. 流式响应开始
2026-03-09T15:35:00.234+0800  INFO   sproxy  LLM response received
  request_id: xyz789abc123
  user_id: user_001
  status: 200
  streaming: true
  duration_ms: 108

# 5. 流转换：发送 message_start
2026-03-09T15:35:00.235+0800  DEBUG  sproxy  stream conversion: message_start sent
  request_id: xyz789abc123
  message_id: msg_xyz789ab

# 6. 流转换：发送 content_block_delta（多次）
2026-03-09T15:35:00.236+0800  DEBUG  sproxy  ← LLM stream chunk
  request_id: xyz789abc123
  data: data: {"id":"chatcmpl-123","choices":[{"delta":{"content":"Hello"},...

2026-03-09T15:35:00.237+0800  DEBUG  sproxy  ← LLM stream chunk
  request_id: xyz789abc123
  data: data: {"id":"chatcmpl-123","choices":[{"delta":{"content":" world"},...

# 7. 流转换：发送 message_delta（含 usage）
2026-03-09T15:35:00.345+0800  DEBUG  sproxy  stream conversion: message_delta sent with usage
  request_id: xyz789abc123
  output_tokens: 8

# 8. 流转换：发送 message_stop
2026-03-09T15:35:00.346+0800  DEBUG  sproxy  stream conversion: message_stop sent, stream complete
  request_id: xyz789abc123
```

---

## 错误场景日志

### 场景 1：请求转换失败（JSON 格式错误）

```log
2026-03-09T15:40:00.123+0800  INFO   sproxy  protocol conversion required
  request_id: err001
  from: anthropic
  to: openai
  target_provider: ollama
  target_url: http://localhost:11434
  original_path: /v1/messages

2026-03-09T15:40:00.124+0800  WARN   sproxy  failed to parse Anthropic request for conversion, forwarding original
  request_id: err001
  error: invalid character '}' looking for beginning of object key string

2026-03-09T15:40:00.125+0800  WARN   sproxy  protocol conversion failed, forwarding original request
  request_id: err001
  error: invalid character '}' looking for beginning of object key string
```

### 场景 2：响应转换失败

```log
2026-03-09T15:45:00.123+0800  INFO   sproxy  protocol conversion required
  request_id: err002
  from: anthropic
  to: openai
  target_provider: ollama
  target_url: http://localhost:11434
  original_path: /v1/messages

2026-03-09T15:45:00.125+0800  INFO   sproxy  request converted successfully
  request_id: err002
  new_path: /v1/chat/completions
  converted_size: 312

2026-03-09T15:45:01.234+0800  INFO   sproxy  LLM response received
  request_id: err002
  user_id: user_001
  status: 200
  streaming: false
  duration_ms: 1109

2026-03-09T15:45:01.235+0800  DEBUG  sproxy  converting non-streaming response
  request_id: err002
  original_size: 512

2026-03-09T15:45:01.236+0800  WARN   sproxy  failed to parse OpenAI response for conversion, forwarding original
  request_id: err002
  error: unexpected end of JSON input

2026-03-09T15:45:01.237+0800  WARN   sproxy  response conversion failed, forwarding original
  request_id: err002
  error: unexpected end of JSON input
```

### 场景 3：空请求体

```log
2026-03-09T15:50:00.123+0800  INFO   sproxy  protocol conversion required
  request_id: err003
  from: anthropic
  to: openai
  target_provider: ollama
  target_url: http://localhost:11434
  original_path: /v1/messages

2026-03-09T15:50:00.124+0800  WARN   sproxy  protocol conversion skipped: empty request body
  request_id: err003
```

---

## 日志级别说明

### INFO 级别（生产环境推荐）

记录关键转换事件：
- ✅ 检测到需要协议转换
- ✅ 请求转换成功
- ✅ 响应转换成功
- ✅ LLM 响应接收

**用途**：监控转换是否正常工作，统计转换频率

### DEBUG 级别（调试时启用）

记录详细转换过程：
- 🔍 路径转换细节
- 🔍 流式事件转换
- 🔍 响应体大小变化
- 🔍 LLM stream chunks

**用途**：故障排查，性能分析

### WARN 级别（始终记录）

记录转换失败和降级：
- ⚠️ JSON 解析失败
- ⚠️ 转换失败，转发原始请求/响应
- ⚠️ 空请求体跳过转换

**用途**：告警，识别潜在问题

---

## 日志查询示例

### 查看所有协议转换请求

```bash
grep "protocol conversion required" sproxy.log
```

### 查看转换失败的请求

```bash
grep "protocol conversion failed" sproxy.log
```

### 查看特定请求的完整转换流程

```bash
grep "request_id: abc123def456" sproxy.log | grep -E "(protocol|converted)"
```

### 统计转换成功率

```bash
# 总转换请求数
grep -c "protocol conversion required" sproxy.log

# 转换失败数
grep -c "protocol conversion failed" sproxy.log

# 成功率 = (总数 - 失败数) / 总数
```

### 查看流式转换详情

```bash
grep "stream conversion:" sproxy.log
```

---

## 性能监控

### 转换耗时分析

通过日志时间戳计算转换耗时：

```bash
# 请求转换耗时（从 "protocol conversion required" 到 "request converted successfully"）
# 通常 < 1ms

# 响应转换耗时（从 "converting non-streaming response" 到 "converted successfully"）
# 通常 < 1ms
```

### 转换大小统计

```bash
# 查看请求体大小变化
grep "converted Anthropic request" sproxy.log | \
  awk '{print $NF}' | \
  awk -F: '{sum+=$2; count++} END {print "平均转换后大小:", sum/count}'

# 查看响应体大小变化
grep "non-streaming response converted" sproxy.log | \
  awk '{print $NF}' | \
  awk -F: '{sum+=$2; count++} END {print "平均转换后大小:", sum/count}'
```

---

## 告警建议

### 关键告警

1. **转换失败率过高**
   ```bash
   # 当转换失败率 > 5% 时告警
   if [ $(grep -c "protocol conversion failed" sproxy.log) -gt \
        $(($(grep -c "protocol conversion required" sproxy.log) / 20)) ]; then
     echo "ALERT: Protocol conversion failure rate > 5%"
   fi
   ```

2. **空请求体频繁出现**
   ```bash
   # 当空请求体 > 10 次/小时时告警
   if [ $(grep -c "empty request body" sproxy.log | tail -1h) -gt 10 ]; then
     echo "ALERT: Too many empty request bodies"
   fi
   ```

### 性能告警

1. **转换耗时过长**
   - 正常：< 1ms
   - 警告：> 10ms
   - 严重：> 100ms

2. **转换后大小异常**
   - 正常：转换后大小变化 < 50%
   - 警告：转换后大小增加 > 100%

---

## 故障排查清单

### 问题：转换失败

1. ✅ 检查日志中的 error 字段
2. ✅ 确认请求/响应 JSON 格式是否正确
3. ✅ 验证后端是否返回标准 OpenAI 格式
4. ✅ 检查是否有特殊字符或编码问题

### 问题：Token 统计为 0

1. ✅ 确认流式请求是否注入了 `stream_options.include_usage: true`
2. ✅ 检查后端是否支持 usage 统计
3. ✅ 查看日志确认 `message_delta sent with usage` 是否出现
4. ✅ 验证响应转换是否正确提取 usage 字段

### 问题：客户端无法解析响应

1. ✅ 检查响应转换是否成功
2. ✅ 确认转换后的 Anthropic 格式是否正确
3. ✅ 查看 debug 日志中的响应体内容
4. ✅ 验证 stop_reason 映射是否正确

---

**日志完备性已确认，所有关键路径均有详细日志记录。**
