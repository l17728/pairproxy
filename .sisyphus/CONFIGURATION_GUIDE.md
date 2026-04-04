# Model-Aware Routing Configuration Guide

## Overview

The gateway implements Model-Aware Routing with three core features:

- **F1 (Config-as-Seed)**: Configuration file provides initial target definitions that can be modified via WebUI without being overwritten
- **F2 (Per-Target Supported Models)**: Route requests to targets that actually support the requested model
- **F3 (Auto Mode)**: Client can request `model="auto"` and gateway selects appropriate model for each target

## Core Concept: Fail-Open Strategy

When filtering candidates by model availability, the gateway uses a **two-level fail-open strategy**:

1. **Level 1 - Provider Filter**: Filter all targets by provider (anthropic, openai, ollama)
   - If no providers match → fail-open, use all targets
   
2. **Level 2 - Model Filter**: Filter remaining targets by supported_models
   - If no targets support the requested model → fail-open, use all candidates from Level 1

**Why fail-open?** Better to send requests to targets that might work than to reject the request entirely. Failures at the target level are caught and retried appropriately.

## Scenarios Explained

### Scenario 1: Model is Configured and Supported by Target

**Configuration:**
```yaml
targets:
  - name: "claude-primary"
    url: "https://api.anthropic.com/..."
    provider: "anthropic"
    supported_models:
      - "claude-3-*"      # Wildcard pattern
      - "claude-2.1"      # Exact match
```

**Request:**
```json
{"model": "claude-3-sonnet-20250219"}
```

**Behavior:**
- Level 1: Provider is "anthropic" (extracted from config) → matches
- Level 2: Model "claude-3-sonnet-20250219" matches pattern "claude-3-*" → target selected
- **Result**: Request routed to claude-primary

---

### Scenario 2: Model Not Configured but Target Supports All Models

**Configuration:**
```yaml
targets:
  - name: "general-proxy"
    url: "https://proxy.internal/..."
    provider: "openai"
    supported_models: []  # Empty = supports all models
```

**Request:**
```json
{"model": "gpt-4-turbo"}
```

**Behavior:**
- Level 1: Provider is "openai" → matches
- Level 2: supported_models is empty (no filtering) → target selected
- **Result**: Request routed to general-proxy (because it accepts any model)

---

### Scenario 3: Model Nowhere Configured

**Configuration:**
```yaml
targets:
  - name: "primary"
    provider: "anthropic"
    supported_models: ["claude-3-*"]
  - name: "fallback"
    provider: "openai"
    supported_models: ["gpt-4-*"]
```

**Request:**
```json
{"model": "llama-2-7b"}
```

**Behavior:**
- Level 1: Provider filter (extracted from config or context) → might match or not
- Level 2: Model "llama-2-7b" doesn't match any patterns → fail-open!
- **Result**: All candidates from Level 1 are tried; request succeeds if any target can handle it, fails only if all reject

**Key Insight**: The gateway doesn't reject requests for unknown models—it attempts them and lets targets decide.

---

## Configuration Examples

### Minimal Configuration (No Model Filtering)

```yaml
targets:
  - name: "default"
    url: "https://api.example.com"
    provider: "openai"
    # supported_models omitted or empty = no filtering
```

**Effect**: All requests go to this target regardless of model name.

---

### Pattern-Based Filtering

```yaml
targets:
  - name: "anthropic-targets"
    url: "https://api.anthropic.com"
    provider: "anthropic"
    supported_models:
      - "claude-3-opus-*"      # Prefix wildcard: matches claude-3-opus-20250101, etc.
      - "claude-3-sonnet-*"    # Prefix wildcard
      - "claude-2.1"           # Exact match only

  - name: "openai-targets"
    url: "https://api.openai.com"
    provider: "openai"
    supported_models:
      - "gpt-4-*"              # Prefix wildcard
      - "gpt-3.5-turbo"        # Exact match

  - name: "fallback-target"
    url: "https://fallback.internal"
    provider: "custom"
    supported_models: []        # Accept all models
```

**Matching Logic**:
- `"claude-3-sonnet-*"` matches: `claude-3-sonnet-20250219`, `claude-3-sonnet-v1`, etc.
- `"claude-2.1"` matches: `claude-2.1` exactly, NOT `claude-2.1.0`
- `"*"` (full wildcard) matches any model
- Empty list `[]` is equivalent to `["*"]`—accepts all models

---

### Auto Mode Configuration

When client sends `model="auto"`, gateway selects model from target's `auto_model` setting.

```yaml
targets:
  - name: "claude-primary"
    url: "https://api.anthropic.com"
    provider: "anthropic"
    supported_models: ["claude-3-*", "claude-2.1"]
    auto_model: "claude-3-sonnet-20250219"  # Default for auto mode

  - name: "openai-backup"
    url: "https://api.openai.com"
    provider: "openai"
    supported_models: ["gpt-4-*", "gpt-3.5-turbo"]
    auto_model: "gpt-4-turbo"              # Default for auto mode

  - name: "local-llama"
    url: "http://localhost:8000"
    provider: "ollama"
    supported_models: ["llama2", "mistral"]
    auto_model: "mistral"                  # Default for auto mode
```

**Behavior when client sends `{"model": "auto"}`**:
1. Gateway queries each target's `auto_model` value
2. For targets without `auto_model` set: fall back to target's most capable model (implementation dependent)
3. Rewrite request body: `{"model": "auto"}` → `{"model": "claude-3-sonnet-20250219"}`
4. Forward rewritten request to target

**Example request flow**:
```json
// Client request
{"model": "auto", "messages": [...]}

// To claude-primary, rewritten to:
{"model": "claude-3-sonnet-20250219", "messages": [...]}

// To openai-backup, rewritten to:
{"model": "gpt-4-turbo", "messages": [...]}
```

---

### Multi-Provider Setup with Filtering

Typical production configuration with multiple providers:

```yaml
targets:
  # Primary Anthropic targets with model filtering
  - name: "claude-us-east-1"
    url: "https://us-east-1.anthropic.api"
    provider: "anthropic"
    weight: 10
    supported_models:
      - "claude-3-opus-20250119"
      - "claude-3-sonnet-20250219"
      - "claude-3-haiku-*"
    auto_model: "claude-3-sonnet-20250219"

  - name: "claude-us-west-2"
    url: "https://us-west-2.anthropic.api"
    provider: "anthropic"
    weight: 8
    supported_models:
      - "claude-3-opus-20250119"
      - "claude-3-sonnet-20250219"
      - "claude-3-haiku-*"
    auto_model: "claude-3-sonnet-20250219"

  # OpenAI backup
  - name: "openai-primary"
    url: "https://api.openai.com"
    provider: "openai"
    weight: 5
    supported_models:
      - "gpt-4-turbo"
      - "gpt-4-*"
      - "gpt-3.5-turbo"
    auto_model: "gpt-4-turbo"

  # Fallback accepts everything
  - name: "universal-fallback"
    url: "https://fallback.internal:8080"
    provider: "custom"
    weight: 1
    supported_models: []  # Accepts all models
    auto_model: "default-model"
```

**Routing behavior**:

| Request Model | Scenario | Targets Tried | Notes |
|---|---|---|---|
| `claude-3-sonnet-20250219` | Model in supported_models | claude-us-east-1, claude-us-west-2 | Direct match |
| `claude-3-haiku-1.0.1` | Matches pattern | claude-us-east-1, claude-us-west-2 | Pattern match on "claude-3-haiku-*" |
| `gpt-4-turbo` | Single provider match | openai-primary | Exact match in OpenAI target |
| `gpt-3.5-turbo` | Model in list | openai-primary | Direct match |
| `llama2` | Not configured anywhere | All targets (fail-open!) | No match in any filter → use all candidates |
| `auto` | Special mode | All targets (each gets its auto_model) | Rewrite per target |

---

## Complete Example Configuration File

### File: `sproxy.yaml`

```yaml
# Gateway Configuration
gateway:
  name: "llm-gateway"
  listen: ":8080"
  
# LLM Targets with Model-Aware Routing
targets:
  # ============= ANTHROPIC CLUSTER =============
  - name: "anthropic-primary"
    url: "https://api.anthropic.com/v1"
    provider: "anthropic"
    weight: 10
    
    # F2: Per-Target Model Filtering
    # Patterns supported:
    #   - "exact-model-name"      → exact match only
    #   - "prefix-*"              → prefix match
    #   - "*"                     → match all
    #   - [] or omitted           → match all (same as ["*"])
    supported_models:
      - "claude-3-opus-20250119"
      - "claude-3-sonnet-20250219"
      - "claude-3-haiku-*"        # Matches claude-3-haiku-1, claude-3-haiku-1.0, etc.
      - "claude-2.1"
    
    # F3: Auto Mode - What model to use when client sends "auto"
    auto_model: "claude-3-sonnet-20250219"
    
    # Optional: timeout, retry settings, etc.
    timeout: "30s"
    max_retries: 3

  - name: "anthropic-fallback"
    url: "https://fallback-api.anthropic.com/v1"
    provider: "anthropic"
    weight: 5
    supported_models:
      - "claude-3-opus-20250119"
      - "claude-3-sonnet-20250219"
      - "claude-3-haiku-*"
      - "claude-2.1"
    auto_model: "claude-3-sonnet-20250219"
    timeout: "30s"

  # ============= OPENAI CLUSTER =============
  - name: "openai-us-east"
    url: "https://api.openai.com/v1"
    provider: "openai"
    weight: 8
    supported_models:
      - "gpt-4-turbo"
      - "gpt-4-turbo-preview"
      - "gpt-4-*"
      - "gpt-3.5-turbo"
      - "gpt-3.5-turbo-*"
    auto_model: "gpt-4-turbo"
    timeout: "30s"
    auth:
      type: "bearer"
      # Bearer token configured via environment: OPENAI_API_KEY

  - name: "openai-eu-west"
    url: "https://eu-api.openai.com/v1"
    provider: "openai"
    weight: 6
    supported_models:
      - "gpt-4-*"
      - "gpt-3.5-turbo"
    auto_model: "gpt-4-turbo"
    timeout: "30s"

  # ============= OLLAMA LOCAL =============
  - name: "local-ollama-premium"
    url: "http://localhost:11434"
    provider: "ollama"
    weight: 3
    # Premium tier serves higher-quality models
    supported_models:
      - "mistral"
      - "neural-chat"
      - "llama2-*"
    auto_model: "mistral"
    timeout: "60s"  # Local inference may be slower

  - name: "local-ollama-economy"
    url: "http://localhost:11434"
    provider: "ollama"
    weight: 1
    # Economy tier: everything else
    supported_models: []  # Accept all models
    auto_model: "llama2"
    timeout: "90s"

  # ============= UNIVERSAL FALLBACK =============
  - name: "experimental-proxy"
    url: "https://experimental.internal/llm"
    provider: "custom"
    weight: 1
    # No model filtering - accepts anything
    supported_models: []
    auto_model: "default"
    timeout: "45s"
```

---

## Troubleshooting with Logs

### Problem: Requests for certain models are failing

**Check logs for**:
```
level: error, msg: "model not supported by any target"
model: "unknown-model-xyz"
```

**Solutions**:

1. **Model isn't configured anywhere**:
   - Check all target entries for `supported_models`
   - Add the model to at least one target's supported_models list

2. **Model pattern is wrong**:
   - Verify pattern syntax: `"claude-*"` not `"claude*"`
   - Test exact match first if pattern matching fails
   - Check for typos: `"claude-3-sonnet-*"` vs `"claude-3-sonet-*"`

3. **Provider mismatch**:
   - Check if model's provider is properly identified
   - Ensure target for that provider exists and has the model in supported_models

**Example fix**:
```yaml
# Before: requests for "mistral-7b" are failing
targets:
  - name: "ollama"
    provider: "ollama"
    supported_models: ["mistral"]  # Wrong: only exact "mistral" matches

# After: added pattern to accept all mistral variants
targets:
  - name: "ollama"
    provider: "ollama"
    supported_models: ["mistral*"]  # Matches mistral, mistral-7b, etc.
```

---

### Problem: Auto mode requests are slow or failing

**Check logs for**:
```
level: warn, msg: "auto_model not configured for target", target: "xyz"
level: info, msg: "fallback to provider default model"
```

**Solutions**:

1. **Missing auto_model configuration**:
   ```yaml
   # Before: no auto_model specified
   - name: "target"
     provider: "openai"
     # auto_model not specified
   
   # After: add explicit auto_model
   - name: "target"
     provider: "openai"
     auto_model: "gpt-4-turbo"
   ```

2. **Slow model selection**:
   - If seeing high latency for auto requests, check if fallback logic is being triggered
   - Set auto_model explicitly for each target to avoid fallback queries

---

### Problem: Requests reaching targets that don't support the model

**Check logs for**:
```
level: warn, msg: "target does not support model but request sent (fail-open)"
target: "primary", model: "unknown-model", reason: "no matching candidates"
```

**This is expected behavior** when:
- Model isn't configured in any target's supported_models
- Fail-open strategy is activated to avoid rejecting requests

**To prevent**:
- Ensure all supported models are added to at least one target:
  ```yaml
  targets:
    - name: "comprehensive"
      supported_models:
        - "claude-*"
        - "gpt-4-*"
        - "mistral*"
        - "*"  # Fallback: match anything
  ```

---

### Problem: Configuration changes not taking effect

**Check**:
```
level: info, msg: "loading targets from configuration", count: N
level: info, msg: "seeding target from config", url: "...", source: "config"
```

**Verify**:
1. Configuration file is being read (check logs on startup)
2. No parse errors (check for YAML syntax errors)
3. Changes not overwritten by WebUI:
   - Go to WebUI → check if target exists
   - If exists: WebUI modifications take precedence over config (F1 behavior)
   - If not exists: configuration will be seeded as new target

**To reset a target back to config**:
- Delete target in WebUI first
- Restart gateway (or trigger config reload)
- Target will be re-seeded from configuration file

---

## Migration Examples

### From No Model Filtering to Per-Target Filtering

```yaml
# Before: all requests go to all targets
targets:
  - name: "target-1"
    url: "https://api1.example.com"
    provider: "openai"
  - name: "target-2"
    url: "https://api2.example.com"
    provider: "anthropic"

# After: add model filtering
targets:
  - name: "target-1"
    url: "https://api1.example.com"
    provider: "openai"
    supported_models:  # NEW
      - "gpt-4-*"
      - "gpt-3.5-turbo"
      
  - name: "target-2"
    url: "https://api2.example.com"
    provider: "anthropic"
    supported_models:  # NEW
      - "claude-3-*"
      - "claude-2.1"
```

**Impact**: Requests are now routed intelligently based on model availability. If a model isn't found, fail-open strategy kicks in.

---

### From Manual Model Selection to Auto Mode

```yaml
# Before: client must choose specific model for each provider
# Client sends: {"provider": "openai", "model": "gpt-4-turbo"}

# After: client can send model="auto" and get best model from each provider
targets:
  - name: "openai"
    provider: "openai"
    auto_model: "gpt-4-turbo"         # NEW
    
  - name: "anthropic"
    provider: "anthropic"
    auto_model: "claude-3-sonnet-20250219"  # NEW

# Client now sends: {"model": "auto"}
# Gateway rewrites to appropriate model for each target
```

---

## Summary

**Remember**:
1. **supported_models** filters which models each target can handle
2. **Fail-open strategy** means unknown models are still attempted (they're not rejected)
3. **auto_model** specifies which model to use when client sends `model="auto"`
4. **Patterns** support exact match, prefix wildcard, and full wildcard
5. **Configuration as seed** (F1) means config provides initial definitions that WebUI can modify
6. **Logs tell the story** — when things go wrong, check the detailed log messages for what happened

For additional help, see the **LOGGING_SPECIFICATION.md** for complete diagnostic message reference and recovery suggestions.
