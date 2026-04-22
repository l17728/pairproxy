# OpenAI → Anthropic Protocol Conversion Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add OtoA (OpenAI→Anthropic) protocol conversion so OpenAI-compatible clients (e.g. VS Code Copilot, OpenAI SDK) can transparently access Anthropic LLM endpoints via sproxy by sending requests to `/v1/chat/completions`.

**Architecture:** Extend `protocol_converter.go` with a typed `conversionDirection` enum, a new `detectConversionDirection` function, three new conversion functions (`convertOpenAIToAnthropicRequest`, `convertAnthropicToOpenAIResponseReverse`, `convertAnthropicErrorResponseToOpenAI`), and a new `AnthropicToOpenAIStreamConverter` struct. Update `sproxy.go` to branch on the typed direction for routing, request body conversion, response conversion, stream converter setup, and error handling.

**Tech Stack:** Go 1.23, `go.uber.org/zap`, `net/http`, `encoding/json`, `github.com/stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-03-14-openai-to-anthropic-conversion-design.md`

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/proxy/protocol_converter.go` | Modify | Add `conversionDirection` type, `detectConversionDirection`, `convertOpenAIToAnthropicRequest`, `convertAnthropicToOpenAIResponseReverse`, `convertAnthropicErrorResponseToOpenAI`, `AnthropicToOpenAIStreamConverter` |
| `internal/proxy/protocol_converter_test.go` | Modify | Rename `TestShouldConvertProtocol` → `TestDetectConversionDirection`, update assertions to typed direction; add all new function tests |
| `internal/proxy/sproxy.go` | Modify | Replace `needsConversion bool` with `convDir conversionDirection`; add effectivePath routing; branch request conversion, stream setup, response conversion, error handling on `convDir` |

---

## Chunk 1: Foundation — Direction Type and Detection

### Task 1: Replace `shouldConvertProtocol` with typed `conversionDirection`

**Files:**
- Modify: `internal/proxy/protocol_converter.go` (lines 32–38, add type constants above, replace function)
- Modify: `internal/proxy/protocol_converter_test.go` (lines 44–89, rename and update test)

- [ ] **Step 1.1: Update the test — rename and add OtoA cases**

  In `internal/proxy/protocol_converter_test.go`, replace `TestShouldConvertProtocol` with `TestDetectConversionDirection`:

  ```go
  func TestDetectConversionDirection(t *testing.T) {
      tests := []struct {
          name           string
          requestPath    string
          targetProvider string
          want           conversionDirection
      }{
          {
              name:           "anthropic path + ollama target → AtoO",
              requestPath:    "/v1/messages",
              targetProvider: "ollama",
              want:           conversionAtoO,
          },
          {
              name:           "anthropic path + openai target → AtoO",
              requestPath:    "/v1/messages",
              targetProvider: "openai",
              want:           conversionAtoO,
          },
          {
              name:           "anthropic path + anthropic target → None",
              requestPath:    "/v1/messages",
              targetProvider: "anthropic",
              want:           conversionNone,
          },
          {
              name:           "openai path + anthropic target → OtoA",
              requestPath:    "/v1/chat/completions",
              targetProvider: "anthropic",
              want:           conversionOtoA,
          },
          {
              name:           "openai path + openai target → None",
              requestPath:    "/v1/chat/completions",
              targetProvider: "openai",
              want:           conversionNone,
          },
          {
              name:           "empty provider → None",
              requestPath:    "/v1/messages",
              targetProvider: "",
              want:           conversionNone,
          },
      }
      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              got := detectConversionDirection(tt.requestPath, tt.targetProvider)
              assert.Equal(t, tt.want, got)
          })
      }
  }
  ```

- [ ] **Step 1.2: Run the test — verify it fails (compile error in test file)**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestDetectConversionDirection -v
  ```
  Expected: compile error `undefined: conversionDirection` from the test file. (At this point `shouldConvertProtocol` still exists in `protocol_converter.go`, so `sproxy.go` compiles fine; only the test file references the not-yet-defined type.)

- [ ] **Step 1.3: Implement the type and function, and add backward-compat shim**

  In `internal/proxy/protocol_converter.go`, REPLACE the existing `shouldConvertProtocol` function (lines 32–38) with the following (note: the shim at the end is **required** to keep `sproxy.go` compiling through Tasks 2–4; it will be removed in Task 5):

  ```go
  // conversionDirection 表示协议转换方向。
  type conversionDirection int

  const (
      conversionNone conversionDirection = iota // 无需转换
      conversionAtoO                            // Anthropic 客户端 → OpenAI/Ollama 目标（已有实现）
      conversionOtoA                            // OpenAI 客户端 → Anthropic 目标（新增）
  )

  // detectConversionDirection 根据请求路径和目标 provider 判断协议转换方向。
  func detectConversionDirection(requestPath, targetProvider string) conversionDirection {
      if strings.HasPrefix(requestPath, "/v1/messages") &&
          (targetProvider == "openai" || targetProvider == "ollama") {
          return conversionAtoO
      }
      if strings.HasPrefix(requestPath, "/v1/chat/completions") &&
          targetProvider == "anthropic" {
          return conversionOtoA
      }
      return conversionNone
  }

  // shouldConvertProtocol 为与 sproxy.go 保持兼容而临时保留，将在 Task 5 中删除。
  func shouldConvertProtocol(path, provider string) bool {
      return detectConversionDirection(path, provider) == conversionAtoO
  }
  ```

- [ ] **Step 1.4: Run the test — verify it passes**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestDetectConversionDirection -v
  ```
  Expected: `PASS`, 6 subtests all green

- [ ] **Step 1.5: Run full proxy package tests — verify no regressions**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -count=1 2>&1 | tail -10
  ```
  Expected: PASS — the shim keeps `sproxy.go` compiling, so all existing tests run and pass.

- [ ] **Step 1.6: Commit**

  ```bash
  git add internal/proxy/protocol_converter.go internal/proxy/protocol_converter_test.go
  git commit -m "refactor(proxy): replace shouldConvertProtocol with typed conversionDirection + detectConversionDirection"
  ```

---

## Chunk 2: Request Conversion — OpenAI → Anthropic

### Task 2: `convertOpenAIToAnthropicRequest`

**Files:**
- Modify: `internal/proxy/protocol_converter.go` (add new function after existing AtoO section)
- Modify: `internal/proxy/protocol_converter_test.go` (add `TestConvertOpenAIToAnthropicRequest`)

The function signature is:
```go
func convertOpenAIToAnthropicRequest(body []byte, logger *zap.Logger, reqID string, modelMapping map[string]string) (converted []byte, newPath string, err error)
```
It returns `newPath = "/v1/messages"`.

Fields to **discard** (not forward to Anthropic): `n`, `logprobs`, `top_logprobs`, `presence_penalty`, `frequency_penalty`, `user`, `stream_options`, `seed`, `response_format`, `service_tier`.

Fields to **pass through**: `model` (apply modelMapping), `max_tokens`, `temperature`, `top_p`, `stream`.

- [ ] **Step 2.1: Write the failing tests**

  Add `TestConvertOpenAIToAnthropicRequest` to `protocol_converter_test.go`:

  ```go
  func TestConvertOpenAIToAnthropicRequest(t *testing.T) {
      logger := zap.NewNop()

      t.Run("basic text message with system", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [
                  {"role": "system", "content": "You are helpful."},
                  {"role": "user", "content": "Hello"}
              ],
              "max_tokens": 100,
              "temperature": 0.7
          }`
          out, path, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req1", nil)
          require.NoError(t, err)
          assert.Equal(t, "/v1/messages", path)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Equal(t, "gpt-4o", got["model"])
          assert.Equal(t, "You are helpful.", got["system"])
          msgs := got["messages"].([]interface{})
          assert.Len(t, msgs, 1)
          m0 := msgs[0].(map[string]interface{})
          assert.Equal(t, "user", m0["role"])
          assert.Equal(t, "Hello", m0["content"])
          assert.Equal(t, float64(100), got["max_tokens"])
          assert.Equal(t, float64(0.7), got["temperature"])
      })

      t.Run("multiple system messages joined", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [
                  {"role": "system", "content": "Part 1."},
                  {"role": "system", "content": "Part 2."},
                  {"role": "user", "content": "Hi"}
              ]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req2", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Equal(t, "Part 1.\n\nPart 2.", got["system"])
          msgs := got["messages"].([]interface{})
          assert.Len(t, msgs, 1) // system removed from messages array
      })

      t.Run("tool role messages merged into user message", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [
                  {"role": "user", "content": "Call the tool"},
                  {"role": "assistant", "content": null, "tool_calls": [
                      {"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}}
                  ]},
                  {"role": "tool", "tool_call_id": "call_1", "content": "Sunny, 25°C"},
                  {"role": "tool", "tool_call_id": "call_2", "content": "Done"}
              ]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req3", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          // Expect: user, assistant (with tool_use), user (merged tool_results)
          assert.Len(t, msgs, 3)
          last := msgs[2].(map[string]interface{})
          assert.Equal(t, "user", last["role"])
          content := last["content"].([]interface{})
          assert.Len(t, content, 2) // two tool_result blocks
          tr0 := content[0].(map[string]interface{})
          assert.Equal(t, "tool_result", tr0["type"])
          assert.Equal(t, "call_1", tr0["tool_use_id"])
          assert.Equal(t, "Sunny, 25°C", tr0["content"])
          tr1 := content[1].(map[string]interface{})
          assert.Equal(t, "tool_result", tr1["type"])
          assert.Equal(t, "call_2", tr1["tool_use_id"])
          assert.Equal(t, "Done", tr1["content"])
      })

      t.Run("tool role message with array content joined", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [
                  {"role": "user", "content": "Go"},
                  {"role": "assistant", "tool_calls": [
                      {"id": "c1", "type": "function", "function": {"name": "fn", "arguments": "{}"}}
                  ]},
                  {"role": "tool", "tool_call_id": "c1", "content": [
                      {"type": "text", "text": "Line 1"},
                      {"type": "text", "text": "Line 2"}
                  ]}
              ]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req4", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          last := msgs[len(msgs)-1].(map[string]interface{})
          content := last["content"].([]interface{})
          tr := content[0].(map[string]interface{})
          assert.Equal(t, "Line 1\nLine 2", tr["content"])
      })

      t.Run("assistant message with tool_calls converted to tool_use", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [
                  {"role": "user", "content": "Use tool"},
                  {"role": "assistant", "content": "Calling tool", "tool_calls": [
                      {"id": "c1", "type": "function", "function": {"name": "search", "arguments": "{\"q\":\"go\"}"}}
                  ]}
              ]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req5", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          asst := msgs[1].(map[string]interface{})
          assert.Equal(t, "assistant", asst["role"])
          blocks := asst["content"].([]interface{})
          // text block + tool_use block
          assert.Len(t, blocks, 2)
          text := blocks[0].(map[string]interface{})
          assert.Equal(t, "text", text["type"])
          assert.Equal(t, "Calling tool", text["text"])
          tu := blocks[1].(map[string]interface{})
          assert.Equal(t, "tool_use", tu["type"])
          assert.Equal(t, "c1", tu["id"])
          assert.Equal(t, "search", tu["name"])
          inp := tu["input"].(map[string]interface{})
          assert.Equal(t, "go", inp["q"])
      })

      t.Run("user message with image_url data URI", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [{"role": "user", "content": [
                  {"type": "text", "text": "Describe this"},
                  {"type": "image_url", "image_url": {"url": "data:image/png;base64,abc123"}}
              ]}]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req6", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          content := msgs[0].(map[string]interface{})["content"].([]interface{})
          img := content[1].(map[string]interface{})
          assert.Equal(t, "image", img["type"])
          src := img["source"].(map[string]interface{})
          assert.Equal(t, "base64", src["type"])
          assert.Equal(t, "image/png", src["media_type"])
          assert.Equal(t, "abc123", src["data"])
      })

      t.Run("user message with image_url https URL", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [{"role": "user", "content": [
                  {"type": "image_url", "image_url": {"url": "https://example.com/img.png"}}
              ]}]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req7", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          content := msgs[0].(map[string]interface{})["content"].([]interface{})
          img := content[0].(map[string]interface{})
          src := img["source"].(map[string]interface{})
          assert.Equal(t, "url", src["type"])
          assert.Equal(t, "https://example.com/img.png", src["url"])
      })

      t.Run("tools array conversion — unwrap function wrapper", func(t *testing.T) {
          input := `{
              "model": "gpt-4o",
              "messages": [{"role": "user", "content": "Hi"}],
              "tools": [{"type": "function", "function": {
                  "name": "get_weather",
                  "description": "Get weather",
                  "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
              }}]
          }`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req8", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          tools := got["tools"].([]interface{})
          assert.Len(t, tools, 1)
          tool := tools[0].(map[string]interface{})
          assert.Equal(t, "get_weather", tool["name"])
          assert.Equal(t, "Get weather", tool["description"])
          schema := tool["input_schema"].(map[string]interface{})
          assert.Equal(t, "object", schema["type"])
          // No "type":"function" wrapper in Anthropic format
          assert.Nil(t, tool["type"])
      })

      t.Run("tool_choice mapping — all variants", func(t *testing.T) {
          cases := []struct {
              openai   string
              wantType string
          }{
              {`"auto"`, "auto"},
              {`"none"`, "none"},
              {`"required"`, "any"},
          }
          for _, c := range cases {
              input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":` + c.openai + `}`
              out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req9", nil)
              require.NoError(t, err)
              var got map[string]interface{}
              require.NoError(t, json.Unmarshal(out, &got))
              tc := got["tool_choice"].(map[string]interface{})
              assert.Equal(t, c.wantType, tc["type"], "tool_choice=%s", c.openai)
          }
          // object form: {"type":"function","function":{"name":"X"}}
          input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"search"}}}`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req9b", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          tc := got["tool_choice"].(map[string]interface{})
          assert.Equal(t, "tool", tc["type"])
          assert.Equal(t, "search", tc["name"])
      })

      t.Run("stop string normalized to array", func(t *testing.T) {
          input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":"STOP"}`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req10", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          seqs := got["stop_sequences"].([]interface{})
          assert.Equal(t, []interface{}{"STOP"}, seqs)
          // stop array case
          input2 := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stop":["END","STOP"]}`
          out2, _, err := convertOpenAIToAnthropicRequest([]byte(input2), logger, "req10b", nil)
          require.NoError(t, err)
          var got2 map[string]interface{}
          require.NoError(t, json.Unmarshal(out2, &got2))
          seqs2 := got2["stop_sequences"].([]interface{})
          assert.Len(t, seqs2, 2)
      })

      t.Run("stream_options discarded", func(t *testing.T) {
          input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req11", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Nil(t, got["stream_options"], "stream_options must be discarded")
          assert.Equal(t, true, got["stream"]) // stream itself kept
      })

      t.Run("model mapping applied", func(t *testing.T) {
          input := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
          mapping := map[string]string{"gpt-4o": "claude-opus-4-6"}
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req12", mapping)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Equal(t, "claude-opus-4-6", got["model"])
      })

      t.Run("tool message without preceding assistant is silently dropped", func(t *testing.T) {
          // Per spec: tool messages are only merged when consecutive after an assistant.
          // An orphaned tool message (e.g. at start, or after user) is dropped — document this behavior.
          input := `{"model":"gpt-4o","messages":[
              {"role": "tool", "tool_call_id": "c1", "content": "orphan"},
              {"role": "user", "content": "hi"}
          ]}`
          out, _, err := convertOpenAIToAnthropicRequest([]byte(input), logger, "req_orphan", nil)
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          msgs := got["messages"].([]interface{})
          // Only the user message remains; orphaned tool is dropped
          assert.Len(t, msgs, 1)
          assert.Equal(t, "user", msgs[0].(map[string]interface{})["role"])
      })

      t.Run("malformed JSON returns error", func(t *testing.T) {
          _, _, err := convertOpenAIToAnthropicRequest([]byte(`{invalid`), logger, "req13", nil)
          assert.Error(t, err)
      })

      t.Run("empty body returned as-is", func(t *testing.T) {
          out, path, err := convertOpenAIToAnthropicRequest(nil, logger, "req14", nil)
          require.NoError(t, err)
          assert.Equal(t, "/v1/messages", path)
          assert.Nil(t, out)
      })
  }
  ```

- [ ] **Step 2.2: Run the test — verify it fails**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertOpenAIToAnthropicRequest -v 2>&1 | head -30
  ```
  Expected: compile error `undefined: convertOpenAIToAnthropicRequest`

- [ ] **Step 2.3: Implement `convertOpenAIToAnthropicRequest`**

  Add the following to `internal/proxy/protocol_converter.go` after the existing AtoO section (after `convertAnthropicTools`, around line 500+). The code relies on two existing helpers already in the file: `mapModelName` (line 43) and `extractToolResultContent` (line 362) — do **not** redefine them. The function must:
  1. Return `newPath = "/v1/messages"` and `nil` error for empty body
  2. Parse OpenAI body as `map[string]interface{}`; on parse error, return error (not degrade)
  3. Apply modelMapping to `model` field
  4. Discard: `n`, `logprobs`, `top_logprobs`, `presence_penalty`, `frequency_penalty`, `user`, `stream_options`, `seed`, `response_format`, `service_tier`
  5. Pass through: `model`, `max_tokens`, `temperature`, `top_p`, `stream`
  6. Map `stop` (string or array) → `stop_sequences` (always array)
  7. Map `tools` → Anthropic format (name/description/input_schema, no wrapper)
  8. Map `tool_choice` → Anthropic format (see table below)
  9. Extract all system role messages → join with `"\n\n"` → top-level `system` field; remove from messages array
  10. Convert remaining messages (user/assistant/tool) — see message conversion rules below

  **`tool_choice` mapping:**
  - `"auto"` → `{"type":"auto"}`
  - `"none"` → `{"type":"none"}`
  - `"required"` → `{"type":"any"}`
  - `{"type":"function","function":{"name":"X"}}` → `{"type":"tool","name":"X"}`

  **Message conversion rules:**
  - **user** string content → `{"role":"user","content":"..."}` (pass through)
  - **user** array content → convert items: `{type:"text"}` → pass; `{type:"image_url",image_url:{url:"data:T;base64,D"}}` → `{type:"image",source:{type:"base64",media_type:"T",data:"D"}}`; `{type:"image_url",image_url:{url:"https://..."}}` → `{type:"image",source:{type:"url",url:"..."}}`
  - **assistant** string content → `{"role":"assistant","content":[{type:"text",text:"..."}]}`; `tool_calls` → append `{type:"tool_use",id,name,input(parsed JSON object)}` blocks; if both content and tool_calls present, text block first
  - **tool** (role=tool): **consecutive** tool messages after an assistant → merge into ONE `{"role":"user","content":[{type:"tool_result",tool_use_id,content},...]}` message. The `content` field of the tool message may be string or array; if array, extract text items and join with `"\n"`.

  ```go
  // convertOpenAIToAnthropicRequest 将 OpenAI Chat Completions 请求转换为 Anthropic Messages API 格式。
  // Returns (converted body, "/v1/messages", nil) on success.
  // Returns (nil, "/v1/messages", error) on JSON parse failure — caller must return HTTP 400.
  func convertOpenAIToAnthropicRequest(body []byte, logger *zap.Logger, reqID string, modelMapping map[string]string) ([]byte, string, error) {
      const newPath = "/v1/messages"
      if len(body) == 0 {
          return body, newPath, nil
      }

      var openaiReq map[string]interface{}
      if err := json.Unmarshal(body, &openaiReq); err != nil {
          logger.Warn("failed to parse OpenAI request for OtoA conversion",
              zap.String("request_id", reqID),
              zap.Error(err),
          )
          return nil, newPath, err
      }

      anthropicReq := make(map[string]interface{})

      // 1. model (with mapping)
      if model, ok := openaiReq["model"].(string); ok {
          anthropicReq["model"] = mapModelName(model, modelMapping)
      }

      // 2. Pass-through scalars
      for _, key := range []string{"max_tokens", "temperature", "top_p", "stream"} {
          if v, ok := openaiReq[key]; ok {
              anthropicReq[key] = v
          }
      }

      // 3. stop → stop_sequences (normalize to array)
      if stop, ok := openaiReq["stop"]; ok {
          switch s := stop.(type) {
          case string:
              anthropicReq["stop_sequences"] = []string{s}
          case []interface{}:
              anthropicReq["stop_sequences"] = s
          }
      }

      // 4. tools → Anthropic format
      if tools, ok := openaiReq["tools"].([]interface{}); ok {
          anthropicTools := make([]map[string]interface{}, 0, len(tools))
          for _, t := range tools {
              tm, ok := t.(map[string]interface{})
              if !ok {
                  continue
              }
              fn, ok := tm["function"].(map[string]interface{})
              if !ok {
                  continue
              }
              at := map[string]interface{}{
                  "name": fn["name"],
              }
              if desc, ok := fn["description"]; ok {
                  at["description"] = desc
              }
              if params, ok := fn["parameters"]; ok {
                  at["input_schema"] = params
              }
              anthropicTools = append(anthropicTools, at)
          }
          if len(anthropicTools) > 0 {
              anthropicTools2 := make([]interface{}, len(anthropicTools))
              for i, at := range anthropicTools {
                  anthropicTools2[i] = at
              }
              anthropicReq["tools"] = anthropicTools2
          }
      }

      // 5. tool_choice
      if tc, ok := openaiReq["tool_choice"]; ok {
          anthropicReq["tool_choice"] = convertOpenAIToolChoice(tc)
      }

      // 6. messages: extract system, convert roles
      rawMessages, _ := openaiReq["messages"].([]interface{})
      var systemParts []string
      var anthropicMessages []interface{}

      i := 0
      for i < len(rawMessages) {
          msg, ok := rawMessages[i].(map[string]interface{})
          if !ok {
              i++
              continue
          }
          role, _ := msg["role"].(string)
          switch role {
          case "system":
              if text, ok := msg["content"].(string); ok {
                  systemParts = append(systemParts, text)
              }
              i++
          case "user":
              anthropicMessages = append(anthropicMessages, convertOpenAIUserMessage(msg))
              i++
          case "assistant":
              anthropicMessages = append(anthropicMessages, convertOpenAIAssistantMessage(msg))
              i++
              // Collect consecutive tool messages that follow
              var toolResults []interface{}
              for i < len(rawMessages) {
                  next, ok := rawMessages[i].(map[string]interface{})
                  if !ok || next["role"] != "tool" {
                      break
                  }
                  toolResults = append(toolResults, map[string]interface{}{
                      "type":        "tool_result",
                      "tool_use_id": next["tool_call_id"],
                      "content":     extractToolResultContent(next["content"]),
                  })
                  i++
              }
              if len(toolResults) > 0 {
                  anthropicMessages = append(anthropicMessages, map[string]interface{}{
                      "role":    "user",
                      "content": toolResults,
                  })
              }
          default:
              i++
          }
      }

      if len(systemParts) > 0 {
          anthropicReq["system"] = strings.Join(systemParts, "\n\n")
      }
      if len(anthropicMessages) > 0 {
          anthropicReq["messages"] = anthropicMessages
      }

      converted, err := json.Marshal(anthropicReq)
      if err != nil {
          logger.Warn("failed to marshal Anthropic request",
              zap.String("request_id", reqID),
              zap.Error(err),
          )
          return nil, newPath, err
      }
      logger.Debug("OpenAI request converted to Anthropic format",
          zap.String("request_id", reqID),
          zap.Int("original_size", len(body)),
          zap.Int("converted_size", len(converted)),
      )
      return converted, newPath, nil
  }

  // convertOpenAIToolChoice 将 OpenAI tool_choice 值转换为 Anthropic 格式。
  func convertOpenAIToolChoice(tc interface{}) map[string]interface{} {
      switch v := tc.(type) {
      case string:
          switch v {
          case "auto":
              return map[string]interface{}{"type": "auto"}
          case "none":
              return map[string]interface{}{"type": "none"}
          case "required":
              return map[string]interface{}{"type": "any"}
          }
      case map[string]interface{}:
          if fn, ok := v["function"].(map[string]interface{}); ok {
              return map[string]interface{}{
                  "type": "tool",
                  "name": fn["name"],
              }
          }
      }
      return map[string]interface{}{"type": "auto"}
  }

  // convertOpenAIUserMessage 转换 user 角色的消息。
  func convertOpenAIUserMessage(msg map[string]interface{}) map[string]interface{} {
      content := msg["content"]
      switch c := content.(type) {
      case string:
          return map[string]interface{}{"role": "user", "content": c}
      case []interface{}:
          anthropicContent := make([]interface{}, 0, len(c))
          for _, item := range c {
              im, ok := item.(map[string]interface{})
              if !ok {
                  continue
              }
              switch im["type"] {
              case "text":
                  anthropicContent = append(anthropicContent, map[string]interface{}{
                      "type": "text",
                      "text": im["text"],
                  })
              case "image_url":
                  if iu, ok := im["image_url"].(map[string]interface{}); ok {
                      if block := convertOpenAIImageURL(iu); block != nil {
                          anthropicContent = append(anthropicContent, block)
                      }
                  }
              }
          }
          return map[string]interface{}{"role": "user", "content": anthropicContent}
      default:
          return map[string]interface{}{"role": "user", "content": ""}
      }
  }

  // convertOpenAIImageURL 将 OpenAI image_url 条目转换为 Anthropic image 块。
  func convertOpenAIImageURL(iu map[string]interface{}) map[string]interface{} {
      rawURL, _ := iu["url"].(string)
      if strings.HasPrefix(rawURL, "data:") {
          // data:TYPE;base64,DATA
          rest := strings.TrimPrefix(rawURL, "data:")
          parts := strings.SplitN(rest, ";base64,", 2)
          if len(parts) != 2 {
              return nil
          }
          return map[string]interface{}{
              "type": "image",
              "source": map[string]interface{}{
                  "type":       "base64",
                  "media_type": parts[0],
                  "data":       parts[1],
              },
          }
      }
      return map[string]interface{}{
          "type": "image",
          "source": map[string]interface{}{
              "type": "url",
              "url":  rawURL,
          },
      }
  }

  // convertOpenAIAssistantMessage 转换 assistant 角色的消息。
  func convertOpenAIAssistantMessage(msg map[string]interface{}) map[string]interface{} {
      var blocks []interface{}

      if text, ok := msg["content"].(string); ok && text != "" {
          blocks = append(blocks, map[string]interface{}{"type": "text", "text": text})
      }

      if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
          for _, tc := range toolCalls {
              tm, ok := tc.(map[string]interface{})
              if !ok {
                  continue
              }
              fn, ok := tm["function"].(map[string]interface{})
              if !ok {
                  continue
              }
              // arguments is a JSON string → parse to object
              var input interface{}
              if args, ok := fn["arguments"].(string); ok {
                  if err := json.Unmarshal([]byte(args), &input); err != nil {
                      input = map[string]interface{}{}
                  }
              }
              blocks = append(blocks, map[string]interface{}{
                  "type":  "tool_use",
                  "id":    tm["id"],
                  "name":  fn["name"],
                  "input": input,
              })
          }
      }

      if len(blocks) == 0 {
          blocks = []interface{}{map[string]interface{}{"type": "text", "text": ""}}
      }

      return map[string]interface{}{"role": "assistant", "content": blocks}
  }
  ```

- [ ] **Step 2.4: Run the test — verify it passes**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertOpenAIToAnthropicRequest -v
  ```
  Expected: all subtests PASS

- [ ] **Step 2.5: Run full proxy package tests**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -count=1 2>&1 | tail -5
  ```
  Expected: PASS (may still have compile issues in sproxy.go if shim from Task 1 not in place — add the shim if missing)

- [ ] **Step 2.6: Commit**

  ```bash
  git add internal/proxy/protocol_converter.go internal/proxy/protocol_converter_test.go
  git commit -m "feat(proxy): add convertOpenAIToAnthropicRequest for OtoA protocol conversion"
  ```

---

## Chunk 3: Response Conversion — Anthropic → OpenAI

### Task 3a: `convertAnthropicToOpenAIResponseReverse` (non-streaming)

**Files:**
- Modify: `internal/proxy/protocol_converter.go` (add after OtoA request section)
- Modify: `internal/proxy/protocol_converter_test.go` (add `TestConvertAnthropicToOpenAIResponseReverse`)

Function signature:
```go
func convertAnthropicToOpenAIResponseReverse(body []byte, logger *zap.Logger, reqID string, requestedModel string) ([]byte, error)
```

**Field mapping (Anthropic → OpenAI):**
| Anthropic field | OpenAI field | Notes |
|---|---|---|
| `id` (msg_xxx) | `id` (chatcmpl-xxx) | swap `msg_` prefix → `chatcmpl-` |
| `model` | `model` | Use `requestedModel` if non-empty, else `model` from response |
| `content[type=text].text` | `choices[0].message.content` | join multiple with `"\n"` |
| `content[type=tool_use]` | `choices[0].message.tool_calls[]` | `input` object → `arguments` JSON string |
| `stop_reason` | `choices[0].finish_reason` | see table below |
| `usage.input_tokens` | `usage.prompt_tokens` | |
| `usage.output_tokens` | `usage.completion_tokens` | |
| `usage.cache_read_input_tokens` | `usage.prompt_tokens_details.cached_tokens` | omit if zero |

**stop_reason mapping:**
| Anthropic | OpenAI |
|---|---|
| `end_turn` | `stop` |
| `max_tokens` | `length` |
| `tool_use` | `tool_calls` |
| `stop_sequence` | `stop` |
| others | pass through |

**ID conversion:** `msg_abc` → `chatcmpl-abc` (swap prefix); if no `msg_` prefix, prepend `chatcmpl-`.

**Response envelope:**
```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": <unix_timestamp>,
  "model": "...",
  "choices": [{
    "index": 0,
    "message": {"role": "assistant", "content": "...", "tool_calls": [...]},
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": N, "completion_tokens": M, "total_tokens": N+M}
}
```

- [ ] **Step 3a.1: Write the failing tests**

  Add `TestConvertAnthropicToOpenAIResponseReverse` to `protocol_converter_test.go`:

  ```go
  func TestConvertAnthropicToOpenAIResponseReverse(t *testing.T) {
      logger := zap.NewNop()

      t.Run("text response", func(t *testing.T) {
          input := `{
              "id": "msg_abc123",
              "type": "message",
              "role": "assistant",
              "model": "claude-opus-4-6",
              "content": [{"type": "text", "text": "Hello, world!"}],
              "stop_reason": "end_turn",
              "usage": {"input_tokens": 10, "output_tokens": 5}
          }`
          out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req1", "gpt-4o")
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Equal(t, "chatcmpl-abc123", got["id"])
          assert.Equal(t, "chat.completion", got["object"])
          assert.Equal(t, "gpt-4o", got["model"]) // requestedModel overrides
          choices := got["choices"].([]interface{})
          assert.Len(t, choices, 1)
          ch := choices[0].(map[string]interface{})
          assert.Equal(t, "stop", ch["finish_reason"])
          msg := ch["message"].(map[string]interface{})
          assert.Equal(t, "assistant", msg["role"])
          assert.Equal(t, "Hello, world!", msg["content"])
          usage := got["usage"].(map[string]interface{})
          assert.Equal(t, float64(10), usage["prompt_tokens"])
          assert.Equal(t, float64(5), usage["completion_tokens"])
          assert.Equal(t, float64(15), usage["total_tokens"])
      })

      t.Run("tool use response", func(t *testing.T) {
          input := `{
              "id": "msg_tool1",
              "type": "message",
              "role": "assistant",
              "model": "claude-opus-4-6",
              "content": [
                  {"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"q": "go lang"}}
              ],
              "stop_reason": "tool_use",
              "usage": {"input_tokens": 20, "output_tokens": 10}
          }`
          out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req2", "")
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          choices := got["choices"].([]interface{})
          ch := choices[0].(map[string]interface{})
          assert.Equal(t, "tool_calls", ch["finish_reason"])
          msg := ch["message"].(map[string]interface{})
          assert.Nil(t, msg["content"]) // no text content
          toolCalls := msg["tool_calls"].([]interface{})
          assert.Len(t, toolCalls, 1)
          tc := toolCalls[0].(map[string]interface{})
          assert.Equal(t, "toolu_1", tc["id"])
          assert.Equal(t, "function", tc["type"])
          assert.Nil(t, tc["index"], "non-streaming tool_calls must NOT include index field")
          fn := tc["function"].(map[string]interface{})
          assert.Equal(t, "search", fn["name"])
          // arguments is a JSON string
          var args map[string]interface{}
          require.NoError(t, json.Unmarshal([]byte(fn["arguments"].(string)), &args))
          assert.Equal(t, "go lang", args["q"])
      })

      t.Run("stop_reason mapping — all variants", func(t *testing.T) {
          cases := []struct{ anthropic, openai string }{
              {"end_turn", "stop"},
              {"max_tokens", "length"},
              {"tool_use", "tool_calls"},
              {"stop_sequence", "stop"},
              {"unknown_reason", "unknown_reason"},
          }
          for _, c := range cases {
              input := fmt.Sprintf(`{"id":"msg_x","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"hi"}],"stop_reason":%q,"usage":{"input_tokens":1,"output_tokens":1}}`, c.anthropic)
              out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "reqX", "")
              require.NoError(t, err, "stop_reason=%s", c.anthropic)
              var got map[string]interface{}
              require.NoError(t, json.Unmarshal(out, &got))
              ch := got["choices"].([]interface{})[0].(map[string]interface{})
              assert.Equal(t, c.openai, ch["finish_reason"], "stop_reason=%s", c.anthropic)
          }
      })

      t.Run("requestedModel propagated", func(t *testing.T) {
          input := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
          out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req3", "my-model")
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          assert.Equal(t, "my-model", got["model"])
      })

      t.Run("cache_read_input_tokens mapped", func(t *testing.T) {
          input := `{"id":"msg_c","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3}}`
          out, err := convertAnthropicToOpenAIResponseReverse([]byte(input), logger, "req4", "")
          require.NoError(t, err)
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          usage := got["usage"].(map[string]interface{})
          details := usage["prompt_tokens_details"].(map[string]interface{})
          assert.Equal(t, float64(3), details["cached_tokens"])
      })

      t.Run("empty body returns error", func(t *testing.T) {
          _, err := convertAnthropicToOpenAIResponseReverse([]byte{}, logger, "req5", "")
          assert.Error(t, err)
      })
  }
  ```

- [ ] **Step 3a.2: Run — verify it fails**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertAnthropicToOpenAIResponseReverse -v 2>&1 | head -10
  ```
  Expected: compile error `undefined: convertAnthropicToOpenAIResponseReverse`

- [ ] **Step 3a.3: Implement the function**

  Add to `internal/proxy/protocol_converter.go` (after OtoA request conversion section):

  ```go
  // convertAnthropicToOpenAIResponseReverse 将 Anthropic Messages API 响应转换为 OpenAI Chat Completions 格式。
  // requestedModel 为原始 OpenAI 请求的 model 名，非空时用于填充响应的 model 字段。
  func convertAnthropicToOpenAIResponseReverse(body []byte, logger *zap.Logger, reqID string, requestedModel string) ([]byte, error) {
      if len(body) == 0 {
          return nil, fmt.Errorf("empty response body")
      }

      var anthropicResp map[string]interface{}
      if err := json.Unmarshal(body, &anthropicResp); err != nil {
          logger.Warn("failed to parse Anthropic response for reverse conversion",
              zap.String("request_id", reqID),
              zap.Error(err),
          )
          return nil, err
      }

      // ID: msg_xxx → chatcmpl-xxx
      rawID, _ := anthropicResp["id"].(string)
      openaiID := convertMessageIDReverse(rawID)

      // model
      model := requestedModel
      if model == "" {
          model, _ = anthropicResp["model"].(string)
      }

      // content → message
      var textParts []string
      var toolCalls []interface{}
      if content, ok := anthropicResp["content"].([]interface{}); ok {
          for _, block := range content {
              bm, ok := block.(map[string]interface{})
              if !ok {
                  continue
              }
              switch bm["type"] {
              case "text":
                  if t, ok := bm["text"].(string); ok {
                      textParts = append(textParts, t)
                  }
              case "tool_use":
                  argsBytes, _ := json.Marshal(bm["input"])
                  // NOTE: non-streaming responses do NOT include "index" on tool_calls items
                  // (only streaming deltas use "index"). Omit it here.
                  toolCalls = append(toolCalls, map[string]interface{}{
                      "id":   bm["id"],
                      "type": "function",
                      "function": map[string]interface{}{
                          "name":      bm["name"],
                          "arguments": string(argsBytes),
                      },
                  })
              }
          }
      }

      message := map[string]interface{}{"role": "assistant"}
      if len(textParts) > 0 {
          message["content"] = strings.Join(textParts, "\n")
      }
      if len(toolCalls) > 0 {
          message["tool_calls"] = toolCalls
      }

      // stop_reason → finish_reason
      stopReason, _ := anthropicResp["stop_reason"].(string)
      finishReason := convertStopReasonToFinishReason(stopReason)

      // usage
      usage := map[string]interface{}{}
      promptTokens := 0
      completionTokens := 0
      if u, ok := anthropicResp["usage"].(map[string]interface{}); ok {
          if v, ok := u["input_tokens"].(float64); ok {
              promptTokens = int(v)
          }
          if v, ok := u["output_tokens"].(float64); ok {
              completionTokens = int(v)
          }
          // Map cache_read_input_tokens unconditionally when present (spec maps it without omit-if-zero condition)
          if cached, ok := u["cache_read_input_tokens"].(float64); ok {
              usage["prompt_tokens_details"] = map[string]interface{}{
                  "cached_tokens": cached,
              }
          }
      }
      usage["prompt_tokens"] = promptTokens
      usage["completion_tokens"] = completionTokens
      usage["total_tokens"] = promptTokens + completionTokens

      // "created" is not listed in the spec's minimal envelope example but is a mandatory field
      // in the real OpenAI Chat Completions API response. OpenAI client libraries (openai-python,
      // openai-go, LangChain, etc.) read this field and may fail or warn without it.
      // The spec's envelope is illustrative, not exhaustive; adding "created" for full compatibility.
      openaiResp := map[string]interface{}{
          "id":      openaiID,
          "object":  "chat.completion",
          "created": time.Now().Unix(),
          "model":   model,
          "choices": []interface{}{
              map[string]interface{}{
                  "index":         0,
                  "message":       message,
                  "finish_reason": finishReason,
              },
          },
          "usage": usage,
      }

      converted, err := json.Marshal(openaiResp)
      if err != nil {
          logger.Warn("failed to marshal OpenAI response",
              zap.String("request_id", reqID),
              zap.Error(err),
          )
          return nil, err
      }
      logger.Debug("Anthropic response converted to OpenAI format",
          zap.String("request_id", reqID),
          zap.Int("converted_size", len(converted)),
      )
      return converted, nil
  }

  // convertMessageIDReverse 将 msg_xxx 转换为 chatcmpl-xxx。
  func convertMessageIDReverse(id string) string {
      if after, found := strings.CutPrefix(id, "msg_"); found {
          return "chatcmpl-" + after
      }
      return "chatcmpl-" + id
  }

  // convertStopReasonToFinishReason 将 Anthropic stop_reason 转换为 OpenAI finish_reason。
  func convertStopReasonToFinishReason(stopReason string) string {
      switch stopReason {
      case "end_turn", "stop_sequence":
          return "stop"
      case "max_tokens":
          return "length"
      case "tool_use":
          return "tool_calls"
      default:
          return stopReason
      }
  }
  ```

  Also add `"time"` to imports if not already present.

- [ ] **Step 3a.4: Run — verify tests pass**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertAnthropicToOpenAIResponseReverse -v
  ```
  Expected: all subtests PASS

### Task 3b: `convertAnthropicErrorResponseToOpenAI`

**Files:**
- Modify: `internal/proxy/protocol_converter.go` (add after 3a)
- Modify: `internal/proxy/protocol_converter_test.go` (add `TestConvertAnthropicErrorResponseToOpenAI`)

**Conversion:**
```
Anthropic: {"type":"error","error":{"type":"authentication_error","message":"..."}}
OpenAI:    {"error":{"type":"authentication_error","message":"..."}}
```
If body is not Anthropic error format (no `error.message`), return unchanged.

- [ ] **Step 3b.1: Write the failing test**

  ```go
  func TestConvertAnthropicErrorResponseToOpenAI(t *testing.T) {
      logger := zap.NewNop()

      t.Run("valid Anthropic error", func(t *testing.T) {
          input := `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`
          out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req1")
          var got map[string]interface{}
          require.NoError(t, json.Unmarshal(out, &got))
          // No top-level "type" key
          assert.Nil(t, got["type"])
          errObj := got["error"].(map[string]interface{})
          assert.Equal(t, "authentication_error", errObj["type"])
          assert.Equal(t, "Invalid API key", errObj["message"])
      })

      t.Run("non-Anthropic format returned unchanged", func(t *testing.T) {
          input := `{"message":"something else"}`
          out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req2")
          assert.Equal(t, input, string(out))
      })

      t.Run("malformed JSON returned unchanged", func(t *testing.T) {
          input := `{invalid`
          out := convertAnthropicErrorResponseToOpenAI([]byte(input), logger, "req3")
          assert.Equal(t, input, string(out))
      })
  }
  ```

- [ ] **Step 3b.2: Run — verify it fails (undefined)**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertAnthropicErrorResponseToOpenAI -v 2>&1 | head -5
  ```

- [ ] **Step 3b.3: Implement the function**

  ```go
  // convertAnthropicErrorResponseToOpenAI 将 Anthropic 格式的错误响应转换为 OpenAI 格式。
  // 若非 Anthropic 格式则原样返回。
  func convertAnthropicErrorResponseToOpenAI(body []byte, logger *zap.Logger, reqID string) []byte {
      var anthropicErr struct {
          Type  string `json:"type"`
          Error struct {
              Type    string `json:"type"`
              Message string `json:"message"`
          } `json:"error"`
      }
      if err := json.Unmarshal(body, &anthropicErr); err != nil || anthropicErr.Error.Message == "" {
          return body
      }

      errType := anthropicErr.Error.Type
      if errType == "" {
          errType = "api_error"
      }

      resp := map[string]interface{}{
          "error": map[string]interface{}{
              "type":    errType,
              "message": anthropicErr.Error.Message,
          },
      }
      converted, err := json.Marshal(resp)
      if err != nil {
          logger.Warn("failed to marshal OpenAI error response",
              zap.String("request_id", reqID),
              zap.Error(err),
          )
          return body
      }
      logger.Debug("Anthropic error converted to OpenAI format",
          zap.String("request_id", reqID),
          zap.String("error_type", errType),
      )
      return converted
  }
  ```

- [ ] **Step 3b.4: Run — verify tests pass**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestConvertAnthropicErrorResponseToOpenAI -v
  ```
  Expected: PASS

- [ ] **Step 3b.5: Run full proxy package tests**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -count=1 2>&1 | tail -5
  ```

- [ ] **Step 3b.6: Commit**

  ```bash
  git add internal/proxy/protocol_converter.go internal/proxy/protocol_converter_test.go
  git commit -m "feat(proxy): add convertAnthropicToOpenAIResponseReverse and convertAnthropicErrorResponseToOpenAI"
  ```

---

## Chunk 4: Streaming Conversion — Anthropic SSE → OpenAI SSE

### Task 4: `AnthropicToOpenAIStreamConverter`

**Files:**
- Modify: `internal/proxy/protocol_converter.go` (add after OtoA non-streaming section)
- Modify: `internal/proxy/protocol_converter_test.go` (add `TestAnthropicToOpenAIStreamConverter`)

The struct implements `http.ResponseWriter` AND `http.Flusher`. It wraps the real client `http.ResponseWriter`. `TeeResponseWriter` wraps this converter so raw Anthropic bytes flow to the token parser before translation.

**Internal state:**
- `w http.ResponseWriter` — real client writer
- `logger *zap.Logger`
- `reqID string`
- `model string` — OpenAI model name from original request (passed by caller)
- `created int64` — captured at construction time (for `created` field in all chunks)
- `messageID string` — extracted from `message_start`, used in all output chunks
- `inputTokens int` — from `message_start.message.usage.input_tokens`
- `outputTokens int` — from `message_delta.usage.output_tokens`
- `pendingToolIndex map[int]int` — Anthropic block index → OpenAI tool_calls index

**Event handling table:**

| Anthropic event | Action |
|---|---|
| `message_start` | Extract `id` as `messageID` (apply `convertMessageIDReverse`); extract `input_tokens`; emit first chunk: `{"id":"chatcmpl-...","object":"chat.completion.chunk","created":T,"model":"...","choices":[{"delta":{"role":"assistant","content":""},"index":0,"finish_reason":null}]}` |
| `content_block_start` (type=text) | No output |
| `content_block_delta` (text_delta) | Emit: `{"choices":[{"delta":{"content":"TEXT"},"index":0,"finish_reason":null}],...}` |
| `content_block_start` (type=tool_use) | Emit: `{"choices":[{"delta":{"tool_calls":[{"index":N,"id":"ID","type":"function","function":{"name":"NAME","arguments":""}}]},"index":0}],...}` |
| `content_block_delta` (input_json_delta) | Emit: `{"choices":[{"delta":{"tool_calls":[{"index":N,"function":{"arguments":"PARTIAL"}}]},"index":0}],...}` |
| `content_block_stop` | No output |
| `message_delta` | Extract `stop_reason`, `output_tokens`; emit final chunk: `{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":N,"completion_tokens":M,"total_tokens":N+M},...}` |
| `message_stop` | Emit `data: [DONE]\n\n` |

- [ ] **Step 4.1: Write the failing tests**

  Add `TestAnthropicToOpenAIStreamConverter` to `protocol_converter_test.go`:

  // Note: `mockResponseWriter` and `newMockResponseWriter()` are already defined in the
  // existing `protocol_converter_test.go` file (used by `TestOpenAIToAnthropicStreamConverter`).
  // Do NOT redefine them — just add the `mockFlusher` type below.

  ```go
  // mockFlusher wraps mockResponseWriter and implements http.Flusher
  type mockFlusher struct {
      *mockResponseWriter
      flushCount int
  }

  func (m *mockFlusher) Flush() {
      m.flushCount++
  }

  func TestAnthropicToOpenAIStreamConverter(t *testing.T) {
      logger := zap.NewNop()

      feedEvents := func(conv *AnthropicToOpenAIStreamConverter, events []string) {
          for _, ev := range events {
              _, err := conv.Write([]byte(ev))
              require.NoError(t, err)
          }
      }

      t.Run("text streaming", func(t *testing.T) {
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req1", "gpt-4o")

          events := []string{
              "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abc\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":10}}}\n\n",
              "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
              "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
              "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n",
              "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
              "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n",
              "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
          }
          feedEvents(conv, events)

          output := mf.String()
          // Should contain data: {...} chunks and [DONE]
          assert.Contains(t, output, "chat.completion.chunk")
          assert.Contains(t, output, "gpt-4o")
          assert.Contains(t, output, "Hello")
          assert.Contains(t, output, " world")
          assert.Contains(t, output, `"finish_reason":"stop"`)
          assert.Contains(t, output, `data: [DONE]`)
          // Check message_start chunk sets role and has finish_reason:null (not omitted, not "stop")
          assert.Contains(t, output, `"role":"assistant"`)
          assert.Contains(t, output, `"finish_reason":null`)
          // Check chatcmpl ID (from msg_abc)
          assert.Contains(t, output, "chatcmpl-abc")
          // Flusher called
          assert.Greater(t, mf.flushCount, 0)
      })

      t.Run("tool use streaming", func(t *testing.T) {
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req2", "gpt-4o")

          events := []string{
              "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_tool\",\"model\":\"claude\",\"usage\":{\"input_tokens\":15}}}\n\n",
              "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\",\"input\":{}}}\n\n",
              "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\"}}\n\n",
              "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"go\\\"}\"  }}\n\n",
              "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
              "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":8}}\n\n",
              "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
          }
          feedEvents(conv, events)

          output := mf.String()
          assert.Contains(t, output, "tool_calls")
          assert.Contains(t, output, "toolu_1")
          assert.Contains(t, output, "search")
          assert.Contains(t, output, `"finish_reason":"tool_calls"`)
          assert.Contains(t, output, `data: [DONE]`)
      })

      t.Run("finish_reason mapping", func(t *testing.T) {
          cases := []struct{ anthropic, openai string }{
              {"end_turn", "stop"},
              {"max_tokens", "length"},
              {"tool_use", "tool_calls"},
              {"stop_sequence", "stop"},
          }
          for _, c := range cases {
              mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
              conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req", "m")
              events := []string{
                  "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_x\",\"usage\":{\"input_tokens\":1}}}\n\n",
                  fmt.Sprintf("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\n", c.anthropic),
                  "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
              }
              feedEvents(conv, events)
              assert.Contains(t, mf.String(), fmt.Sprintf(`"finish_reason":%q`, c.openai), "anthropic=%s", c.anthropic)
          }
      })

      t.Run("usage in final chunk", func(t *testing.T) {
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req3", "gpt-4o")

          events := []string{
              "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_u\",\"usage\":{\"input_tokens\":10}}}\n\n",
              "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":20}}\n\n",
              "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
          }
          feedEvents(conv, events)

          output := mf.String()
          // Final chunk should have usage
          assert.Contains(t, output, `"prompt_tokens":10`)
          assert.Contains(t, output, `"completion_tokens":20`)
          assert.Contains(t, output, `"total_tokens":30`)
      })

      t.Run("final chunk has empty delta", func(t *testing.T) {
          // Spec: message_delta emits final chunk with delta={} (empty object)
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req5", "gpt-4o")
          events := []string{
              "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_d\",\"usage\":{\"input_tokens\":5}}}\n\n",
              "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n",
              "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
          }
          feedEvents(conv, events)
          output := mf.String()
          // The final data chunk (before [DONE]) should contain "delta":{} (empty object)
          assert.Contains(t, output, `"delta":{}`)
          assert.Contains(t, output, `"finish_reason":"stop"`)
      })

      t.Run("empty response (only message_stop)", func(t *testing.T) {
          // Empty streaming response: message_start then immediately message_stop, no content events.
          // Spec testing section lists "Empty response" as a required case.
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req6", "gpt-4o")
          events := []string{
              "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_e\",\"usage\":{\"input_tokens\":1}}}\n\n",
              "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
          }
          feedEvents(conv, events)
          output := mf.String()
          // Should produce initial role chunk (with content:"") and [DONE], no crash
          assert.Contains(t, output, `"role":"assistant"`)
          assert.Contains(t, output, `data: [DONE]`)
          // No message_delta was emitted, so no finish_reason chunk appears
          // (no finish_reason:"stop" or finish_reason:"length" etc.)
          assert.NotContains(t, output, `"finish_reason":"stop"`)
          assert.NotContains(t, output, `"finish_reason":"length"`)
      })

      t.Run("Flush delegates to inner writer", func(t *testing.T) {
          mf := &mockFlusher{mockResponseWriter: newMockResponseWriter()}
          conv := NewAnthropicToOpenAIStreamConverter(mf, logger, "req7", "gpt-4o")
          conv.Flush()
          assert.Equal(t, 1, mf.flushCount)
      })
  }
  ```

- [ ] **Step 4.2: Run — verify it fails**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestAnthropicToOpenAIStreamConverter -v 2>&1 | head -10
  ```
  Expected: compile error `undefined: AnthropicToOpenAIStreamConverter`

- [ ] **Step 4.3: Implement `AnthropicToOpenAIStreamConverter`**

  Add to `internal/proxy/protocol_converter.go` after the OtoA non-streaming section:

  ```go
  // AnthropicToOpenAIStreamConverter 将 Anthropic SSE 流转换为 OpenAI SSE 块。
  // 实现 http.ResponseWriter 和 http.Flusher 接口。
  // TeeResponseWriter 包装此 converter，将原始 Anthropic 字节传递给 token 解析器。
  type AnthropicToOpenAIStreamConverter struct {
      w       http.ResponseWriter
      logger  *zap.Logger
      reqID   string
      model   string  // OpenAI model name requested by client
      created int64   // captured at construction

      messageID   string
      inputTokens int
      outputTokens int

      // tool call tracking: anthropic block index → openai tool_calls index
      // Note: nextToolIdx is an implementation detail not listed in the spec but is required
      // for toolCallIndex to function correctly (it tracks the next OpenAI index to assign).
      toolCallIndex map[int]int
      nextToolIdx   int
  }

  // NewAnthropicToOpenAIStreamConverter 创建 AnthropicToOpenAIStreamConverter。
  func NewAnthropicToOpenAIStreamConverter(w http.ResponseWriter, logger *zap.Logger, reqID string, model string) *AnthropicToOpenAIStreamConverter {
      return &AnthropicToOpenAIStreamConverter{
          w:             w,
          logger:        logger,
          reqID:         reqID,
          model:         model,
          created:       time.Now().Unix(),
          toolCallIndex: make(map[int]int),
      }
  }

  // Header 实现 http.ResponseWriter。
  func (c *AnthropicToOpenAIStreamConverter) Header() http.Header {
      return c.w.Header()
  }

  // WriteHeader 实现 http.ResponseWriter。
  func (c *AnthropicToOpenAIStreamConverter) WriteHeader(statusCode int) {
      c.w.WriteHeader(statusCode)
  }

  // Flush 实现 http.Flusher — 委托给内部 writer 执行刷新。
  func (c *AnthropicToOpenAIStreamConverter) Flush() {
      if f, ok := c.w.(http.Flusher); ok {
          f.Flush()
      }
  }

  // Write 接收 Anthropic SSE 块，转换为 OpenAI SSE 块后输出。
  // 已知限制：不对跨 Write() 调用的 SSE 事件进行缓冲。
  // 这与现有 OpenAIToAnthropicStreamConverter（AtoO 方向）的设计方针相同，
  // 上游 HTTP/2 或 HTTP/1.1 帧确保每个 SSE 事件以完整 chunk 形式
  // 传入（实测确认每个 "data: ...

" 以单次 Write 到达）。
  // 若该假设不成立，则可能需要切换至带缓冲的实现。
  func (c *AnthropicToOpenAIStreamConverter) Write(chunk []byte) (int, error) {
      lines := bytes.Split(chunk, []byte("\n"))
      var eventType string
      var dataLine string

      for _, line := range lines {
          line = bytes.TrimRight(line, "\r")
          if bytes.HasPrefix(line, []byte("event:")) {
              eventType = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
          } else if bytes.HasPrefix(line, []byte("data:")) {
              dataLine = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("data:"))))
          } else if len(bytes.TrimSpace(line)) == 0 && dataLine != "" {
              // End of event: process it
              c.handleEvent(eventType, dataLine)
              eventType = ""
              dataLine = ""
          }
      }
      // If chunk ended without blank line
      if dataLine != "" {
          c.handleEvent(eventType, dataLine)
      }
      return len(chunk), nil
  }

  func (c *AnthropicToOpenAIStreamConverter) handleEvent(eventType, data string) {
      var ev map[string]interface{}
      if err := json.Unmarshal([]byte(data), &ev); err != nil {
          return
      }

      evType, _ := ev["type"].(string)
      switch evType {
      case "message_start":
          msg, _ := ev["message"].(map[string]interface{})
          if msg != nil {
              rawID, _ := msg["id"].(string)
              c.messageID = convertMessageIDReverse(rawID)
              if usage, ok := msg["usage"].(map[string]interface{}); ok {
                  c.inputTokens = int(floatToInt(usage["input_tokens"]))
              }
          }
          // Emit first chunk: role=assistant
          c.emitChunk(map[string]interface{}{
              "delta":        map[string]interface{}{"role": "assistant", "content": ""},
              "index":        0,
              "finish_reason": nil,
          }, nil)

      case "content_block_start":
          blockIdx := int(floatToInt(ev["index"]))
          cb, _ := ev["content_block"].(map[string]interface{})
          if cb == nil {
              return
          }
          switch cb["type"] {
          case "tool_use":
              toolIdx := c.nextToolIdx
              c.toolCallIndex[blockIdx] = toolIdx
              c.nextToolIdx++
              name, _ := cb["name"].(string)
              id, _ := cb["id"].(string)
              c.emitChunk(map[string]interface{}{
                  "delta": map[string]interface{}{
                      "tool_calls": []interface{}{
                          map[string]interface{}{
                              "index": toolIdx,
                              "id":    id,
                              "type":  "function",
                              "function": map[string]interface{}{
                                  "name":      name,
                                  "arguments": "",
                              },
                          },
                      },
                  },
                  "index": 0,
              }, nil)
          }
          // text: no output

      case "content_block_delta":
          blockIdx := int(floatToInt(ev["index"]))
          delta, _ := ev["delta"].(map[string]interface{})
          if delta == nil {
              return
          }
          switch delta["type"] {
          case "text_delta":
              text, _ := delta["text"].(string)
              c.emitChunk(map[string]interface{}{
                  "delta":        map[string]interface{}{"content": text},
                  "index":        0,
                  "finish_reason": nil,
              }, nil)
          case "input_json_delta":
              partial, _ := delta["partial_json"].(string)
              toolIdx, ok := c.toolCallIndex[blockIdx]
              if !ok {
                  return
              }
              c.emitChunk(map[string]interface{}{
                  "delta": map[string]interface{}{
                      "tool_calls": []interface{}{
                          map[string]interface{}{
                              "index": toolIdx,
                              "function": map[string]interface{}{
                                  "arguments": partial,
                              },
                          },
                      },
                  },
                  "index": 0,
              }, nil)
          }

      case "message_delta":
          delta, _ := ev["delta"].(map[string]interface{})
          stopReason := ""
          if delta != nil {
              stopReason, _ = delta["stop_reason"].(string)
          }
          if usage, ok := ev["usage"].(map[string]interface{}); ok {
              c.outputTokens = int(floatToInt(usage["output_tokens"]))
          }
          finishReason := convertStopReasonToFinishReason(stopReason)
          usageObj := map[string]interface{}{
              "prompt_tokens":     c.inputTokens,
              "completion_tokens": c.outputTokens,
              "total_tokens":      c.inputTokens + c.outputTokens,
          }
          c.emitChunk(map[string]interface{}{
              "delta":        map[string]interface{}{},
              "finish_reason": finishReason,
              "index":        0,
          }, usageObj)

      case "message_stop":
          c.emitDone()
      }
  }

  // emitChunk 写入 OpenAI SSE 块。usage 为可选项（nil 则省略）。
  func (c *AnthropicToOpenAIStreamConverter) emitChunk(choice map[string]interface{}, usage map[string]interface{}) {
      envelope := map[string]interface{}{
          "id":      c.messageID,
          "object":  "chat.completion.chunk",
          "created": c.created,
          "model":   c.model,
          "choices": []interface{}{choice},
      }
      if usage != nil {
          envelope["usage"] = usage
      }
      data, err := json.Marshal(envelope)
      if err != nil {
          c.logger.Warn("failed to marshal OpenAI SSE chunk",
              zap.String("request_id", c.reqID),
              zap.Error(err),
          )
          return
      }
      line := "data: " + string(data) + "\n\n"
      if _, err := c.w.Write([]byte(line)); err != nil {
          c.logger.Debug("write error for SSE chunk",
              zap.String("request_id", c.reqID),
              zap.Error(err),
          )
      }
      c.Flush()
  }

  // emitDone 发送 OpenAI SSE 结束信号。
  func (c *AnthropicToOpenAIStreamConverter) emitDone() {
      if _, err := c.w.Write([]byte("data: [DONE]\n\n")); err != nil {
          c.logger.Debug("write error for [DONE]",
              zap.String("request_id", c.reqID),
              zap.Error(err),
          )
      }
      c.Flush()
  }
  ```

- [ ] **Step 4.4: Run — verify tests pass**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -run TestAnthropicToOpenAIStreamConverter -v
  ```
  Expected: all subtests PASS

- [ ] **Step 4.5: Run full proxy package**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -count=1 2>&1 | tail -5
  ```

- [ ] **Step 4.6: Commit**

  ```bash
  git add internal/proxy/protocol_converter.go internal/proxy/protocol_converter_test.go
  git commit -m "feat(proxy): add AnthropicToOpenAIStreamConverter for OtoA streaming conversion"
  ```

---

## Chunk 5: Wiring — Update `sproxy.go`

### Task 5: Replace `needsConversion bool` with `convDir conversionDirection` and wire all OtoA paths

**Files:**
- Modify: `internal/proxy/sproxy.go` (serveProxy function, lines ~1190–1490)
- Modify: `internal/proxy/protocol_converter.go` (remove temporary `shouldConvertProtocol` shim if added)

**Changes summary:**
1. Remove the shim `shouldConvertProtocol` from `protocol_converter.go`
2. In `serveProxy`:
   - Replace `needsConversion := shouldConvertProtocol(r.URL.Path, targetProvider)` → `convDir := detectConversionDirection(r.URL.Path, targetProvider)`
   - Compute `effectivePath`: `"/v1/messages"` when `convDir == conversionOtoA`, else `r.URL.Path`; re-run `pickLLMTarget` with `effectivePath` for spec compliance (no-op for bound users — see "Important" note below)
   - Add `effectivePath` parameter to `buildRetryTransport` call; capture it in `PickNext` closure (the closure's `path` parameter from `RetryTransport` is shadowed by `effectivePath` intentionally — rename to `_`)
   - Stream converter setup (lines ~1307–1316, **before** `tw :=` at ~1318): switch on `convDir` — AtoO → `NewOpenAIToAnthropicStreamConverter`, OtoA → `NewAnthropicToOpenAIStreamConverter`
   - Request conversion: branch on `convDir` (AtoO preserves graceful degradation; OtoA returns HTTP 400 on error — do NOT add `convDir = conversionNone` fallback in the OtoA block)
   - Director path rewrite: branch on `convDir != conversionNone`
   - Non-streaming response: switch on `convDir`; OtoA records tokens with `tw.RecordNonStreaming` BEFORE conversion (with raw Anthropic body), sets `otoaRecorded=true`; default recording at line ~1472 guarded with `if !otoaRecorded`; all post-processing (debug log, model extraction, conversation tracking) runs for all directions
   - Error response (HTTP 4xx/5xx body from upstream): AtoO → `convertOpenAIErrorResponse`, OtoA → `convertAnthropicErrorResponseToOpenAI`, None → pass through
   - `span.SetAttributes`: update to use `convDir != conversionNone`
   - **Out of scope:** The `ErrorHandler` in `httputil.ReverseProxy` handles transport-level failures (connection refused, timeout) and currently writes OpenAI-format errors unconditionally. Updating `ErrorHandler` to branch on `convDir` is out of scope for this task — it is a pre-existing condition for AtoO and the fix would require threading `convDir` into a separate context. Transport-level failures are rare; the upstream HTTP body path (ModifyResponse) is correctly handled by this task.

**Important: effectivePath and pickLLMTarget for OtoA**

The spec (§ "Target Routing Fix for OtoA") requires `effectivePath = "/v1/messages"` to be passed to **both** the initial `pickLLMTarget` call AND `buildRetryTransport`'s `PickNext` closure.

**How initial pick works for OtoA:** `pickLLMTarget` has two code paths:

1. **Binding-based** (user/group has an explicit LLM binding): Returns the bound target **regardless of path** — `preferredProvidersByPath` is NOT consulted. This is the normal OtoA scenario. If the user is bound to an Anthropic target, then `sp.pickLLMTarget("/v1/chat/completions", ...)` at line ~1134 still returns that Anthropic target. Then `targetProvider` = `"anthropic"`, `detectConversionDirection("/v1/chat/completions", "anthropic")` = `conversionOtoA` ✅, and the re-pick below fires (returning the same Anthropic target — it is effectively a no-op that preserves spec compliance).

2. **Balancer-based** (no binding): `preferredProvidersByPath("/v1/chat/completions")` returns `{openai, ollama}`, so Anthropic targets are filtered out of the initial pick. The initial pick returns an OpenAI/Ollama target; `targetProvider` = `"openai"` → `convDir` = `conversionNone`. **OtoA conversion requires a user/group binding to an Anthropic target when using the load balancer.** This is the intended deployment model.

**Prerequisite:** OtoA requires the Anthropic LLM target to have `provider: "anthropic"` configured in `sproxy.yaml` (or in the DB). If the target has no `provider` set, `sp.providerForURL(firstInfo.URL)` returns `""`, the existing fallback at lines 1177–1179 sets `targetProvider = "openai"`, and `detectConversionDirection` returns `conversionNone` — OtoA is silently skipped. This is a configuration requirement, not a code change. No modification to the lines 1177–1179 fallback is needed.

The re-pick in the code below is included for spec conformance. Note on retry path correctness:

- **Primary mechanism (Step 5.10):** The Director closure rewrites `req.URL.Path` to `convertedPath` (`"/v1/messages"`) before each upstream attempt. This runs on every retry — the Director fires on every round trip, so retries always send `/v1/messages` to the upstream regardless of what is in the `PickNext` closure.
- **Defense-in-depth (Step 5.4/5.9):** `buildRetryTransport`'s `PickNext` closure captures `effectivePath` and passes it to `pickLLMTarget` on retry picks. This ensures the balancer selects from `preferredProvidersByPath["/v1/messages"]` (Anthropic targets) rather than `preferredProvidersByPath["/v1/chat/completions"]` (OpenAI/Ollama targets) when choosing a new target for a retry. This is secondary to the Director — the Director's path rewrite is what guarantees the upstream sees `/v1/messages`; the closure is what ensures the **target selection** during retries also prefers Anthropic providers.

Do NOT omit Step 5.10 (Director rewrite) thinking the closure handles retry path correctness — the closure only affects target selection, not the path written to the outgoing request.

```go
// After detecting convDir:
effectivePath := r.URL.Path
if convDir == conversionOtoA {
    effectivePath = "/v1/messages"
    // Re-pick with effectivePath for spec conformance (§"Target Routing Fix for OtoA").
    // For bound users this returns the same Anthropic target — it is a no-op.
    // Retries will also use effectivePath via buildRetryTransport below.
    if repicked, pickErr := sp.pickLLMTarget(effectivePath, claims.UserID, claims.GroupID, nil); pickErr == nil {
        firstInfo = repicked
        targetProvider = sp.providerForURL(firstInfo.URL)
        // Update targetURL so the Director closure (which captured it at line ~1164) uses
        // the re-picked target's host. For bound users this is the same URL — it is a no-op.
        // IMPORTANT: targetURL is a *url.URL variable (not a value copy) captured by reference
        // in the Director closure. Reassigning `targetURL = newURL` here propagates the new URL
        // into the closure — this is correct Go closure semantics (closures capture variables,
        // not values). Verify at line ~1164 that the Director references `targetURL` as a variable
        // (not a captured constant): it should use `targetURL.Scheme` and `targetURL.Host`.
        if newURL, parseErr := url.Parse(firstInfo.URL); parseErr == nil {
            targetURL = newURL
        }
        // Safety check: if misconfiguration caused a non-Anthropic target to be re-picked,
        // reset convDir to avoid applying OtoA conversion to a wrong target.
        if sp.providerForURL(firstInfo.URL) != "anthropic" {
            convDir = conversionNone
        }
    } else {
        // Re-pick failed (transient: target healthy for initial pick but unavailable now).
        // Continue with firstInfo from the initial pick — it was healthy and is the bound target.
        sp.logger.Warn("OtoA re-pick failed, continuing with initial target",
            zap.String("request_id", reqID),
            zap.Error(pickErr),
        )
    }
}
// Pass effectivePath to buildRetryTransport (it captures it in PickNext closure)
transport := sp.buildRetryTransport(claims.UserID, claims.GroupID, effectivePath)
```

Update `buildRetryTransport` signature to accept `effectivePath string` and capture it in PickNext:
```go
func (sp *SProxy) buildRetryTransport(userID, groupID, effectivePath string) http.RoundTripper {
    ...
    PickNext: func(_ string, tried []string) (*lb.LLMTargetInfo, error) {
        // The RetryTransport's path argument is intentionally ignored — see Step 5.4 comment.
        return sp.pickLLMTarget(effectivePath, userID, groupID, tried)
    },
```

> **Chunk 5 prerequisites:** By the time Chunk 5 is executed, Chunks 1–4 must be fully implemented and committed. In particular:
> - `TestShouldConvertProtocol` was renamed to `TestDetectConversionDirection` in Chunk 1 Step 1.1. That test already calls `detectConversionDirection` with `conversionDirection` enum values (including the OtoA case). No change to this test is needed in Chunk 5.
> - `targetProvider` is updated in the re-pick block (Step 5.5) before `tap.NewTeeResponseWriter` at line ~1318. This ordering is intentional and critical: `NewTeeResponseWriter` uses `targetProvider = "anthropic"` to select `AnthropicSSEParser` for the token-counting tee path — so raw Anthropic upstream bytes are parsed correctly for OtoA.
> - The re-pick block (Step 5.5) must also execute before `usageRecord.UpstreamURL` is set at line ~1258 and before `captureSession` is initialized at line ~1272/1277 (both of which consume `firstInfo.URL` and `targetProvider`). Do NOT reorder the re-pick block below those lines; the re-pick updates `firstInfo` and `targetProvider` in-place so all downstream consumers automatically use the re-picked values.

- [ ] **Step 5.1: Verify `shouldConvertProtocol` is gone**

  Chunk 1 renamed `shouldConvertProtocol` to `detectConversionDirection`. If Chunk 1 was completed correctly, the old name does NOT exist anywhere in `protocol_converter.go`. Verify unconditionally:

  ```bash
  grep -n "shouldConvertProtocol" internal/proxy/protocol_converter.go
  ```
  Expected: **no output**. If `shouldConvertProtocol` still appears (e.g. as a leftover shim), Chunk 1 was not completed correctly — stop and finish Chunk 1 before proceeding with Chunk 5.

- [ ] **Step 5.2: Verify baseline build state**

  > Note: Steps 5.2+ require Chunks 1–4 to be fully implemented first (all functions in `protocol_converter.go` must exist).

  ```bash
  "C:/Program Files/Go/bin/go.exe" build ./internal/proxy/... 2>&1
  ```
  After Step 5.1, the ONLY expected compile error is `undefined: shouldConvertProtocol` from `sproxy.go`. If you see errors about other undefined symbols (e.g. `detectConversionDirection`, `conversionAtoO`, `convertOpenAIToAnthropicRequest`), Chunks 1–4 are not fully implemented — stop and fix those chunks first. If the only error is `undefined: shouldConvertProtocol`, proceed to Step 5.3. The error will be resolved in Step 5.5.

- [ ] **Step 5.3: Ensure OtoA failure-path test exists (idempotent)**

  > This is an idempotent step. First check if `TestOtoARequestConversionFailurePath` already exists in `protocol_converter_test.go` (Chunk 2 may have added it). If it exists, skip this step. If it does not exist, add it. The test validates the no-silent-degradation contract relied on by Step 5.6 (returns error → sproxy returns HTTP 400). It calls `convertOpenAIToAnthropicRequest` directly from Chunk 2 — no `sproxy.go` interaction.

  ```bash
  grep -n "TestOtoARequestConversionFailurePath" internal/proxy/protocol_converter_test.go
  ```
  If output is non-empty: **skip this step** (test already exists). Otherwise, add:

  Add to `protocol_converter_test.go`:

  ```go
  func TestOtoARequestConversionFailurePath(t *testing.T) {
      // Malformed JSON must return error (not degrade silently).
      // This verifies the contract relied on by Step 5.6 (sproxy.go returns HTTP 400 on error).
      logger := zap.NewNop()
      _, _, err := convertOpenAIToAnthropicRequest([]byte(`{invalid json`), logger, "req1", nil)
      assert.Error(t, err, "malformed JSON should return error for OtoA (no silent degradation)")
  }
  ```

- [ ] **Step 5.4: Update `buildRetryTransport` to accept `effectivePath`**

  In `sproxy.go` at line ~978, change signature and PickNext:

  ```go
  // buildRetryTransport 构建 RetryTransport（当 llmBalancer 已配置时）。
  // effectivePath 是传递给 PickNext 的路径（OtoA 时为 "/v1/messages"，其他为 r.URL.Path）。
  // 这是次要防御机制：确保 retry 时 pickLLMTarget 从 /v1/messages 对应的 preferredProvidersByPath
  // 中选 Anthropic targets。主要机制是 Director 将出向请求路径改写为 convertedPath（Step 5.10）。
  func (sp *SProxy) buildRetryTransport(userID, groupID, effectivePath string) http.RoundTripper {
      if sp.llmBalancer == nil {
          return sp.transport
      }
      maxRetries := sp.maxRetries
      if maxRetries <= 0 {
          maxRetries = 2
      }
      return &lb.RetryTransport{
          Inner:      sp.transport,
          MaxRetries: maxRetries,
          PickNext: func(_ string, tried []string) (*lb.LLMTargetInfo, error) {
              // The `path` parameter passed by RetryTransport is intentionally shadowed by the
              // captured `effectivePath`. This is correct: for OtoA retries, effectivePath is
              // "/v1/messages" (Anthropic targets); for all other cases it equals r.URL.Path
              // (same as what RetryTransport would pass). RetryTransport's `path` arg is the
              // original request path before Director rewrite — we don't want that here.
              return sp.pickLLMTarget(effectivePath, userID, groupID, tried)
          },
          // ... rest unchanged
      }
  }
  ```

- [ ] **Step 5.5: Update `serveProxy` — replace `needsConversion` with `convDir`, add re-pick**

  At line ~1190 in `sproxy.go`, replace:
  ```go
  needsConversion := shouldConvertProtocol(r.URL.Path, targetProvider)
  var convertedPath string
  if needsConversion {
  ```
  With:
  ```go
  convDir := detectConversionDirection(r.URL.Path, targetProvider)
  effectivePath := r.URL.Path
  if convDir == conversionOtoA {
      effectivePath = "/v1/messages"
      // Re-pick using effectivePath so preferredProvidersByPath["/v1/messages"] matches
      // Anthropic targets correctly (spec §"Target Routing Fix for OtoA").
      if repicked, pickErr := sp.pickLLMTarget(effectivePath, claims.UserID, claims.GroupID, nil); pickErr == nil {
          firstInfo = repicked
          targetProvider = sp.providerForURL(firstInfo.URL)
          // Update targetURL so the Director closure (captured at line ~1164) uses the right host.
          // For bound users re-pick returns the same target — this is a no-op in that case.
          if newURL, parseErr := url.Parse(firstInfo.URL); parseErr == nil {
              targetURL = newURL
          }
          // Safety check: if re-pick somehow returned a non-Anthropic target (misconfiguration),
          // reset convDir to avoid applying OtoA conversion to an OpenAI/Ollama endpoint.
          // In correct configuration this never fires (bound Anthropic target always re-picked).
          if sp.providerForURL(firstInfo.URL) != "anthropic" {
              convDir = conversionNone
          }
      } else {
          sp.logger.Warn("OtoA re-pick failed, continuing with initial target",
              zap.String("request_id", reqID),
              zap.Error(pickErr),
          )
      }
  }
  var convertedPath string
  if convDir == conversionAtoO {
  ```

  Then, inside the existing AtoO block body that follows (lines ~1193–1236), change the two `needsConversion = false` fallback assignments to `convDir = conversionNone`. These are:

  ```go
  // Line ~1229 — error fallback path:
  // Before: needsConversion = false // 降级：不转换响应
  //  After: convDir = conversionNone // 降级：不转换响应

  // Line ~1235 — empty body path:
  // Before: needsConversion = false
  //  After: convDir = conversionNone
  ```

  The `ErrPrefillNotSupported` branch at line ~1218 is unaffected — it already returns immediately with HTTP 400. No other changes are needed inside the AtoO block body. This must NOT be changed for `conversionAtoO`; the no-graceful-degradation policy applies only to `conversionOtoA` (which returns HTTP 400 on error in Step 5.6).

- [ ] **Step 5.6: Add OtoA request conversion block**

  > **Note on error format**: OtoA uses `writeJSONError` (OpenAI error format), NOT `writeAnthropicError` — the client is an OpenAI client expecting OpenAI-format errors. AtoO uses `writeAnthropicError`. Keep them distinct.
  >
  > **No degradation for OtoA**: Do NOT add `convDir = conversionNone` fallback anywhere in this block. OtoA must return HTTP 400 on conversion failure — unlike AtoO which degrades gracefully. The `return` statements after `writeJSONError` calls are the correct exit path.

  After the `if convDir == conversionAtoO { ... }` block, add:

  ```go
  if convDir == conversionOtoA {
      sp.logger.Info("protocol conversion required (OtoA)",
          zap.String("request_id", reqID),
          zap.String("from", "openai"),
          zap.String("to", "anthropic"),
          zap.String("target_provider", targetProvider),
          zap.String("target_url", firstInfo.URL),
          zap.String("original_path", r.URL.Path),
      )
      if len(bodyBytes) > 0 {
          modelMapping := sp.modelMappingForURL(firstInfo.URL)
          converted, newPath, convErr := convertOpenAIToAnthropicRequest(bodyBytes, sp.logger, reqID, modelMapping)
          if convErr == nil {
              bodyBytes = converted
              convertedPath = newPath
              r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
              r.ContentLength = int64(len(bodyBytes))
              sp.logger.Info("OtoA request converted successfully",
                  zap.String("request_id", reqID),
                  zap.String("new_path", newPath),
                  zap.Int("converted_size", len(converted)),
              )
          } else {
              sp.logger.Warn("OtoA request conversion failed, returning 400",
                  zap.String("request_id", reqID),
                  zap.Error(convErr),
              )
              writeJSONError(w, http.StatusBadRequest, "invalid_request_error",
                  "failed to convert OpenAI request to Anthropic format: "+convErr.Error())
              return
          }
      } else {
          sp.logger.Warn("OtoA conversion skipped: empty request body",
              zap.String("request_id", reqID),
          )
          // Empty body: cannot convert, return 400
          writeJSONError(w, http.StatusBadRequest, "invalid_request_error",
              "request body is required for OpenAI→Anthropic conversion")
          return
      }
  }
  ```

- [ ] **Step 5.7: Update span attributes line**

  Replace `if needsConversion {` with `if convDir != conversionNone {`

- [ ] **Step 5.8: Update stream converter setup (lines ~1307–1316)**

  > **Ordering constraint:** The following code replaces lines ~1307–1316, which appear **BEFORE** `tw := tap.NewTeeResponseWriter(finalWriter, ...)` at line ~1318. The switch must run first so that `finalWriter` is set to the correct stream converter before `tw` wraps it. Do NOT move this block after line 1318.
  >
  > **Note on streaming chain:** `AnthropicToOpenAIStreamConverter` (OtoA, new) implements both `http.ResponseWriter` AND `http.Flusher` (implemented in Chunk 4 — `Flush()` delegates to `w.(http.Flusher).Flush()`). `tw = tap.NewTeeResponseWriter(finalWriter, ...)` at line ~1318 wraps `finalWriter` (the stream converter). Upstream bytes flow: upstream → `tw` (tee-fan-out) → both [token parser path] and [stream converter path → client]. The token parser sees raw Anthropic bytes; the stream converter translates them to OpenAI format for the client. This matches spec §"Data Flow" streaming chain layout.
  >
  > **Pre-existing limitation (AtoO):** `OpenAIToAnthropicStreamConverter` (existing AtoO converter) does NOT implement `http.Flusher` — it has no `Flush()` method. When `httputil.ReverseProxy` calls `tw.Flush()` during AtoO streaming, the flush does not propagate to the actual `http.ResponseWriter`, which may cause AtoO SSE chunks to be buffered rather than delivered in real-time. This is a pre-existing condition unrelated to this OtoA feature; fixing it is out of scope for this task.

  Replace lines ~1307–1316:
  ```go
  var finalWriter http.ResponseWriter = w
  if needsConversion {
      streamConverter := NewOpenAIToAnthropicStreamConverter(w, sp.logger, reqID, model)
      finalWriter = streamConverter
      ...
  }
  ```
  With (this block runs **before** the `tw :=` line at ~1318):
  ```go
  var finalWriter http.ResponseWriter = w
  switch convDir {
  case conversionAtoO:
      streamConverter := NewOpenAIToAnthropicStreamConverter(w, sp.logger, reqID, model)
      finalWriter = streamConverter
      sp.logger.Debug("AtoO stream converter inserted",
          zap.String("request_id", reqID),
      )
  case conversionOtoA:
      streamConverter := NewAnthropicToOpenAIStreamConverter(w, sp.logger, reqID, model)
      finalWriter = streamConverter
      sp.logger.Debug("OtoA stream converter inserted",
          zap.String("request_id", reqID),
      )
  }
  // tw := tap.NewTeeResponseWriter(finalWriter, ...) follows at line ~1318 — unchanged
  ```

- [ ] **Step 5.9: Update `buildRetryTransport` call to pass `effectivePath`**

  Replace:
  ```go
  transport := sp.buildRetryTransport(claims.UserID, claims.GroupID)
  ```
  With:
  ```go
  transport := sp.buildRetryTransport(claims.UserID, claims.GroupID, effectivePath)
  ```

- [ ] **Step 5.10: Update Director path rewrite**

  Replace:
  ```go
  if needsConversion && convertedPath != "" {
  ```
  With:
  ```go
  if convDir != conversionNone && convertedPath != "" {
  ```

- [ ] **Step 5.11: Update non-streaming response conversion block (lines ~1415–1444)**

  **Design: `otoaRecorded` flag (no `return nil`)**

  OtoA must call `tw.RecordNonStreaming` with the **raw Anthropic body** (before conversion) so `AnthropicSSEParser` sees the correct format. Calling it again at the default location (line ~1472) would double-record. Solve this with an `otoaRecorded bool` flag — NOT `return nil` (which would skip debug logging, model extraction, and conversation tracking).

  | Code path | When `RecordNonStreaming` fires | Body seen by parser |
  |---|---|---|
  | `conversionNone` | line ~1472 (default) | original upstream body |
  | `conversionAtoO` | line ~1472 (default) | converted Anthropic body |
  | `conversionOtoA` | inside switch case (before conversion) | raw Anthropic body ✅ |

  Step 5.11a — Declare `otoaRecorded` just before the non-streaming block:
  ```go
  otoaRecorded := false
  if !isStreaming {
  ```

  Step 5.11b — Replace the existing `if needsConversion && readErr == nil && len(body) > 0 {` block with:

  ```go
  if readErr == nil && len(body) > 0 {
      switch convDir {
      case conversionAtoO:
          // AtoO: convert OpenAI response → Anthropic format (existing behavior, unchanged)
          sp.logger.Debug("AtoO: converting non-streaming response",
              zap.String("request_id", reqID),
              zap.Int("original_size", len(body)),
          )
          if resp.StatusCode >= 400 {
              body = convertOpenAIErrorResponse(body, sp.logger, reqID)
              sp.logger.Info("AtoO: error response converted to Anthropic format",
                  zap.String("request_id", reqID),
              )
          } else {
              converted, convErr := convertOpenAIToAnthropicResponse(body, sp.logger, reqID, model)
              if convErr == nil {
                  body = converted
                  sp.logger.Info("AtoO: non-streaming response converted successfully",
                      zap.String("request_id", reqID),
                  )
              } else {
                  sp.logger.Warn("AtoO: response conversion failed, forwarding original",
                      zap.String("request_id", reqID),
                      zap.Error(convErr),
                  )
              }
          }

      case conversionOtoA:
          // OtoA: RECORD FIRST with raw Anthropic body so AnthropicSSEParser parses correctly.
          // Then convert to OpenAI format for the client.
          // Use otoaRecorded=true to prevent double-recording at line ~1472.
          sp.logger.Debug("OtoA: converting non-streaming response",
              zap.String("request_id", reqID),
              zap.Int("original_size", len(body)),
          )
          tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
          otoaRecorded = true
          if resp.StatusCode >= 400 {
              body = convertAnthropicErrorResponseToOpenAI(body, sp.logger, reqID)
              sp.logger.Info("OtoA: error response converted to OpenAI format",
                  zap.String("request_id", reqID),
              )
          } else {
              converted, convErr := convertAnthropicToOpenAIResponseReverse(body, sp.logger, reqID, requestedModel)
              if convErr == nil {
                  body = converted
                  sp.logger.Info("OtoA: non-streaming response converted successfully",
                      zap.String("request_id", reqID),
                  )
              } else {
                  sp.logger.Warn("OtoA: response conversion failed, forwarding original Anthropic response",
                      zap.String("request_id", reqID),
                      zap.Error(convErr),
                  )
              }
          }
      }
  }
  ```

  The `resp.Body = io.NopCloser(bytes.NewReader(body))` line at ~1446 (outside the switch, unchanged) sets the body for ALL directions including OtoA.

  > **Timing note:** `tw.RecordNonStreaming` for OtoA is called before the post-body model-extraction block at lines ~1455–1469. However, `usageRecord.Model` is already set from `requestedModel` at line ~1253 (extracted from the OpenAI request body, which always contains a `model` field for well-formed requests). The post-body model-extraction at lines 1455–1469 only fires when `usageRecord.Model == ""`.
  >
  > **Edge case — absent `model` field in OtoA request:** If the OpenAI client sends a request without a `model` field (malformed), `requestedModel` at line ~1253 is `""`. The post-body model extraction at lines ~1455–1469 will then attempt `tw.UpdateModel(...)` on `tw` — after OtoA's `tw.RecordNonStreaming` has already fired. Whether `UpdateModel` after `RecordNonStreaming` is a no-op depends on `TeeResponseWriter`'s implementation; if `RecordNonStreaming` finalizes the usage record, the `UpdateModel` call may be dropped. This is an acceptable edge case: a well-formed OpenAI request always includes `model`, and the OtoA block in Step 5.6 returns HTTP 400 for empty bodies. If a `model`-less but otherwise valid body slips through, the usage record may log an empty model string — this does not affect correctness of the proxy response.

- [ ] **Step 5.12: Guard `tw.RecordNonStreaming` at line ~1472 to skip OtoA**

  The default `tw.RecordNonStreaming` call at line ~1472 runs for `conversionNone` and `conversionAtoO`. Guard it so OtoA (which already recorded above) is skipped:

  Replace:
  ```go
  tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
  ```
  With:
  ```go
  if !otoaRecorded {
      tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
  }
  ```

  Debug logging (lines ~1448–1453), model extraction (lines ~1455–1469), and conversation tracking (lines ~1474–1478) are **outside** the switch block and run unchanged for all `convDir` values including OtoA — no other changes needed in those sections.

- [ ] **Step 5.13: Update `else if needsConversion` branch at line ~1479 (streaming comment)**

  The `else if needsConversion` branch at line ~1479 is the streaming counterpart to the non-streaming block. After Step 5.5 removes `needsConversion`, this causes a compile error. Replace:

  ```go
  } else if needsConversion {
      // 协议转换：OpenAI SSE → Anthropic SSE（流式响应）
      sp.logger.Debug("streaming response will be converted",
          zap.String("request_id", reqID),
      )
      // 注意：流式转换在 TeeResponseWriter.Write() 中处理
  }
  ```
  With:
  ```go
  } else if convDir != conversionNone {
      // 协议转换（streaming）：
      // AtoO: 上游返回 OpenAI SSE → OpenAIToAnthropicStreamConverter 转为 Anthropic SSE 给客户端
      // OtoA: 上游返回 Anthropic SSE → AnthropicToOpenAIStreamConverter 转为 OpenAI SSE 给客户端
      // 注意：实际转换在 TeeResponseWriter.Write() 调用 streamConverter.Write() 中处理
      sp.logger.Debug("streaming response will be converted",
          zap.String("request_id", reqID),
      )
  }
  ```

- [ ] **Step 5.14: Build the project — verify no compile errors**

  ```bash
  "C:/Program Files/Go/bin/go.exe" build ./... 2>&1
  ```
  Expected: clean build, no errors

- [ ] **Step 5.15: Run full test suite**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./... -count=1 2>&1 | tail -20
  ```
  Expected: PASS for all packages. If failures: read the error and fix.

- [ ] **Step 5.16: Run with -race flag**

  ```bash
  "C:/Program Files/Go/bin/go.exe" test ./internal/proxy/... -race -count=1 2>&1 | tail -10
  ```
  Expected: PASS, no data races

- [ ] **Step 5.17: Commit**

  ```bash
  # Verify staged state first.
  git status
  # protocol_converter.go: include only if Step 5.1 had no changes to make (shim was still present).
  #   If the shim was correctly removed during Chunk 1, protocol_converter.go has no changes here
  #   and `git add` is a no-op for it — that is correct.
  # protocol_converter_test.go: include only if Step 5.3 added TestOtoARequestConversionFailurePath
  #   in this chunk (not already committed in Chunk 2).
  git add internal/proxy/sproxy.go internal/proxy/protocol_converter.go internal/proxy/protocol_converter_test.go
  git commit -m "feat(proxy/v2.10.0): wire OtoA protocol conversion in serveProxy — OpenAI clients can now transparently target Anthropic LLMs"
  ```

---

## Post-Implementation Checklist

- [ ] Run `go vet ./...` — no new warnings
- [ ] Run `go test ./... -count=1` — all pass
- [ ] Manually verify OtoA path end-to-end by inspecting logs with a curl request (optional but recommended)
- [ ] Update `docs/TEST_REPORT.md` and `docs/ACCEPTANCE_REPORT.md` with new test counts
- [ ] Update `MEMORY.md` under "Released Versions" with v2.10.0 entry

---

Plan complete and saved to `docs/superpowers/plans/2026-03-14-openai-to-anthropic-conversion.md`. Ready to execute?
