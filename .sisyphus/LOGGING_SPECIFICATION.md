# Model-Aware Routing 诊断日志规范

> 本文档定义了实现中所有关键路径的日志输出规范，包括日志级别、字段、以及诊断建议。

## 日志级别说明

| 级别 | 用途 | 示例 |
|------|------|------|
| **DEBUG** | 详细的诊断信息，可用于开发和问题排查 | 模型匹配尝试、过滤链中间步骤 |
| **INFO** | 正常的业务事件，例如成功的操作 | 目标选中、配置同步完成 |
| **WARN** | 可能的问题或非预期的情况，但系统继续运行 | fail-open 回退、配置缺失 |
| **ERROR** | 严重错误，影响功能 | 无健康 target、同步失败 |

---

## 按功能模块的日志规范

### 1. Seed 方法诊断日志 (F1)

**功能**：`internal/db/llmtarget_repo.go` 的 `Seed()` 方法

#### 1a. 首次插入成功
```
INFO seed: inserting new config target
     url=https://api.anthropic.com
     provider=anthropic
     source=config
     is_editable_after_seed=true
```

#### 1b. 已存在，跳过（保留 WebUI 修改）
```
DEBUG seed: target already exists, skipping
      url=https://api.anthropic.com
      source=config
      reason=url_already_exists
      skipped_fields=[supported_models, auto_model, weight]  // WebUI 的修改会被保留
      note="WebUI modifications will not be overwritten by config file"
```

#### 1c. URL 检查失败
```
ERROR seed: failed to check if URL exists
      url=https://api.anthropic.com
      error=database_connection_failed
      recovery_suggestion="check database connectivity and try again"
```

---

### 2. 模型匹配诊断日志 (F2 - matchModel)

**功能**：`internal/proxy/sproxy.go` 的 `matchModel()` 和 `filterByModel()`

#### 2a. 精确匹配成功
```
DEBUG model-aware routing: exact match found
      requested_model=claude-sonnet-4-20250514
      pattern=claude-sonnet-4-20250514
      match_type=exact
```

#### 2b. 前缀通配匹配成功
```
DEBUG model-aware routing: prefix wildcard match
      requested_model=claude-sonnet-4-20250514
      pattern=claude-sonnet-4-*
      match_type=prefix_wildcard
      prefix_matched=claude-sonnet-4-
```

#### 2c. 全通配匹配
```
DEBUG model-aware routing: wildcard match
      requested_model=anything
      pattern=*
      match_type=full_wildcard
      note="target accepts all models"
```

#### 2d. 不匹配
```
DEBUG model-aware routing: no match found
      requested_model=llama3
      attempted_patterns=["claude-*", "claude-opus-*"]
      diagnosis="none of the configured patterns matched the requested model"
      suggestion="check target's supported_models configuration"
```

---

### 3. 路由过滤诊断日志 (F2 - weightedPickExcluding)

**功能**：`internal/proxy/sproxy.go` 的 `weightedPickExcluding()` 和 `filterByModel()`

#### 3a. 成功选中支持该模型的 target
```
INFO model-aware routing: target selected with model support
     path=/v1/messages
     requested_model=claude-sonnet-4-20250514
     selected_target=https://api.anthropic.com
     selected_target_id=api-anthropic-1
     model_supported=true
     provider_filter=["anthropic"]
     weight=2
```

#### 3b. Provider 过滤无结果
```
WARN model-aware routing: provider filter returned no candidates, falling back
     path=/v1/messages
     preferred_providers=["anthropic"]
     all_targets_count=3
     after_provider_filter=0
     falling_back_to=all_healthy_targets
     available_after_fallback=3
     recovery_suggestion="check if healthy anthropic targets are available"
```

#### 3c. 模型过滤无结果，触发 fail-open
```
WARN model-aware routing: no target supports requested model, falling back to provider-filtered candidates
     path=/v1/messages
     requested_model=llama3
     candidates_before_model_filter=2
     model_filter_results=0
     candidates_after_fallback=2
     diagnosis="none of the available anthropic targets are configured to support 'llama3'"
     possible_reasons=[
       "model_not_configured_in_supported_models",
       "model_name_typo_in_request",
       "target_not_configured_with_supported_models_at_all"
     ]
     recovery_suggestion="add 'llama3' to supported_models or reconsider model choice"
     fallback_target_selected=https://api.anthropic.com
     note="will attempt to forward request; LLM may reject if model unsupported"
```

#### 3d. 无健康 target
```
ERROR model-aware routing: no healthy targets available
      path=/v1/messages
      provider_filter=["anthropic"]
      healthy_count=0
      draining_count=1
      unhealthy_count=2
      diagnosis="all configured anthropic targets are unhealthy or draining"
      targets_status=[
        {url: "https://api-1.anthropic.com", status: "unhealthy", last_error: "connection timeout"},
        {url: "https://api-2.anthropic.com", status: "draining", reason: "planned maintenance"}
      ]
      recovery_suggestion="wait for targets to recover or check health check configuration"
```

---

### 4. 重试路径诊断日志 (F2 - buildRetryTransport)

**功能**：`buildRetryTransport()` 的重试逻辑

#### 4a. 首次请求成功
```
INFO model-aware routing: request completed successfully (no retry needed)
     request_id=req-12345
     path=/v1/messages
     requested_model=claude-sonnet-4
     selected_target=https://api.anthropic.com
     status_code=200
     attempt=1
```

#### 4b. 首次失败，触发重试
```
WARN model-aware routing: first target failed, attempting retry
     request_id=req-12345
     path=/v1/messages
     requested_model=claude-sonnet-4
     first_target=https://api.anthropic.com
     first_status_code=500
     error=server_error
     retry_count=1
     max_retries=2
     retry_on_status=[500, 502, 503]
     model_filter_in_retry=true
     note="retry will use same model filter to select alternative target"
```

#### 4c. 重试成功
```
INFO model-aware routing: retry succeeded
     request_id=req-12345
     path=/v1/messages
     requested_model=claude-sonnet-4
     first_target_failed=https://api.anthropic.com
     retry_target_succeeded=https://api-backup.anthropic.com
     total_attempts=2
     final_status_code=200
```

#### 4d. 重试全部失败
```
ERROR model-aware routing: all retry attempts failed
     request_id=req-12345
     path=/v1/messages
     requested_model=claude-sonnet-4
     attempts=[
       {target: "https://api.anthropic.com", status: 500, error: "timeout"},
       {target: "https://api-backup.anthropic.com", status: 503, error: "service unavailable"}
     ]
     max_retries_reached=true
     possible_reasons=[
       "all_configured_anthropic_targets_down",
       "network_connectivity_issue",
       "model_not_supported_by_any_target"
     ]
     recovery_suggestion="check target health status and network connectivity"
```

---

### 5. Auto 模式诊断日志 (F3)

**功能**：`serveProxy()` 中的 auto 模式处理

#### 5a. Auto 模式使用 auto_model
```
INFO auto mode: rewrote model in request body
     request_id=req-12345
     path=/v1/messages
     original_model=auto
     selected_target=https://api.anthropic.com
     auto_model_configured=claude-sonnet-4-20250514
     replacement_model=claude-sonnet-4-20250514
     body_size_before=1024
     body_size_after=1025
     rewrite_success=true
```

#### 5b. Auto 模式降级到 supported_models[0]
```
INFO auto mode: falling back to first supported model
     request_id=req-12345
     path=/v1/messages
     original_model=auto
     selected_target=https://api.openai.com
     auto_model_configured=false
     supported_models=["gpt-4o", "gpt-4o-mini", "gpt-4-turbo"]
     replacement_model=gpt-4o
     reason=auto_model_not_configured_fallback_to_supported_models_0
```

#### 5c. Auto 模式无法确定，透传
```
DEBUG auto mode: cannot determine model, passing through 'auto' to LLM
      request_id=req-12345
      path=/v1/messages
      original_model=auto
      selected_target=http://ollama.local:11434
      auto_model_configured=false
      supported_models_count=0
      reason="no auto_model or supported_models configured"
      note="will send model=auto to LLM; LLM must handle 'auto' model name"
      potential_issue="LLM may not support model=auto and return error"
```

#### 5d. 模型重写失败（JSON 解析错误）
```
WARN auto mode: failed to rewrite model in request body
      request_id=req-12345
      path=/v1/messages
      original_model=auto
      selected_target=https://api.anthropic.com
      auto_model=claude-sonnet-4
      json_parse_error=invalid_json
      error_detail="unexpected token at line 1"
      recovery_action="sending request with original body (model=auto)"
      potential_cause="request body is not valid JSON or is corrupted"
```

---

### 6. 配置同步诊断日志 (F1+F2+F3)

**功能**：`syncConfigTargetsToDatabase()`, `loadAllTargets()`, `SyncLLMTargets()`

#### 6a. 配置文件同步开始
```
INFO sync: config targets sync initiated
     config_targets_count=3
     db_targets_before=5
     source=startup
```

#### 6b. 使用 Seed 播种
```
INFO sync: seeding config target (first time)
     url=https://api.anthropic.com
     provider=anthropic
     source=config
     action=insert
```

#### 6c. Seed 跳过已存在的
```
DEBUG sync: config target already exists, preserving WebUI modifications
       url=https://api.anthropic.com
       preserved_fields=[supported_models, auto_model, weight]
       db_weight=2
       config_weight=1
       db_supported_models=["claude-*"]
       config_supported_models=["claude-opus-*"]
       note="WebUI value takes precedence over config"
```

#### 6d. 清理被删除的 config targets
```
INFO sync: cleaning up removed config targets
     removed_count=2
     removed_urls=["https://old-api.anthropic.com", "https://decommissioned.openai.com"]
     action=delete
```

#### 6e. 同步完成统计
```
INFO sync: config targets sync completed
     total_processed=3
     inserted=1
     preserved=2
     removed=2
     db_targets_after=6
     with_supported_models_count=2
     with_auto_model_count=1
     balancer_updated=true
     health_checker_updated=true
```

#### 6f. 配置项缺失告警
```
WARN sync: target missing supported_models configuration
      url=https://api.ollama.com
      provider=ollama
      supported_models_count=0
      note="target will accept all models without filtering"
      implication="requests for unsupported models may fail at LLM level"
      suggestion="configure supported_models if this target has model restrictions"
```

---

### 7. 模型提取诊断日志 (F2+F3)

**功能**：`serveProxy()` 的模型提取阶段

#### 7a. 从请求头提取
```
DEBUG model extraction: model found in X-PairProxy-Model header
      request_id=req-12345
      path=/v1/messages
      model=claude-sonnet-4
      source=header
```

#### 7b. 从 JSON body 提取
```
DEBUG model extraction: model found in request body
      request_id=req-12345
      path=/v1/messages
      model=gpt-4o
      source=body_json
      body_sample_size=512
```

#### 7c. 无模型指定
```
DEBUG model extraction: no model specified in request
      request_id=req-12345
      path=/v1/messages
      x_pairproxy_model_header=not_present
      body_contains_model_field=false
      action="routing without model filter (all healthy targets eligible)"
```

#### 7d. 模型提取失败
```
WARN model extraction: failed to parse model from body
      request_id=req-12345
      path=/v1/messages
      error=invalid_json
      error_detail="unexpected token at position 42"
      action="continuing without model; will use provider filter only"
```

---

### 8. API Handler 诊断日志 (F2+F3)

**功能**：`admin_llm_target_handler.go` 的 Create/Update

#### 8a. 创建带 supported_models
```
INFO admin api: create target with supported_models
     id=target-123
     url=https://api.anthropic.com
     provider=anthropic
     supported_models=["claude-*"]
     auto_model=claude-sonnet-4
     source=database
     is_editable=true
     action=create
```

#### 8b. 更新失败（配置源）
```
WARN admin api: cannot update config-sourced target
      id=target-456
      url=https://api.openai.com
      reason=config_sourced_target_not_editable
      source=config
      is_editable=false
      suggestion="config-sourced targets can only be modified by changing the config file"
      recovery="either: (1) modify sproxy.yaml and restart, or (2) contact infrastructure team"
```

#### 8c. 更新后触发同步
```
INFO admin api: target updated, syncing to balancer
      id=target-789
      url=https://api.anthropic.com
      changed_fields=[supported_models, auto_model]
      sync_initiated=true
      sync_target_updated=true
```

---

### 9. 字段验证诊断日志

**功能**：各种字段验证

#### 9a. 模型名验证失败
```
WARN model validation: suspicious model name format
      model=claude-sonnet-4-20250514
      length=30
      contains_whitespace=false
      contains_special_chars=false
      warning=none
      note="model name looks valid"
```

```
WARN model validation: suspicious model name format
      model=CLAUDE-SONNET-4
      length=15
      format_issue=all_uppercase
      suggestion="model names are typically lowercase; ensure this is intentional"
```

#### 9b. 通配符配置验证
```
DEBUG pattern validation: supported_models pattern
      pattern=claude-*
      type=prefix_wildcard
      scope_example_models=["claude-sonnet-4", "claude-opus-4", "claude-instant"]
      valid=true
```

```
WARN pattern validation: overly broad wildcard
      pattern=*
      type=full_wildcard
      scope=all_models
      warning="this target will accept all model names without filtering"
      implication="routing will rely entirely on provider filter"
```

---

## 告警触发规则

### 🔴 ERROR 级告警（立即通知运维）

```
1. 无健康 target 可用
   → 影响：所有请求将失败
   → 操作：检查 target 健康检查配置

2. 所有模型过滤都失败
   → 影响：无法处理特定模型的请求
   → 操作：检查 supported_models 配置

3. 配置同步失败
   → 影响：WebUI 改动不生效
   → 操作：检查数据库连接和权限
```

### 🟡 WARN 级告警（监控并分析）

```
1. fail-open 发生
   → 说明：网关无法精确路由，使用回退策略
   → 建议：检查是否需要更新配置

2. 重试全部失败
   → 说明：所有 target 都不可用或不支持模型
   → 建议：检查 target 状态和配置

3. Config-sourced target 无法编辑
   → 说明：尝试通过 WebUI 修改配置文件来源的 target
   → 建议：通过配置文件修改或使用数据库来源
```

### 🔵 INFO 级日志（用于审计和性能监控）

```
1. Target 选中
   → 用途：跟踪每个请求的路由决策
   → 聚合：统计各 target 的使用频率

2. 配置同步完成
   → 用途：监控启动和配置更新
   → 聚合：统计变更量和执行时间

3. Auto 模式使用
   → 用途：监控 auto 模式的有效性
   → 聚合：统计 auto 模式被使用的频率
```

---

## 日志聚合和查询示例

### Elasticsearch / Datadog 查询

```
# 找出所有 fail-open 事件
{
  "message": "no LLM target supports requested model"
  AND "severity": "WARN"
}
过去 24h 统计：fail-open 事件发生了多少次？

# 找出无健康 target 的事件
{
  "message": "no healthy targets available"
  AND "severity": "ERROR"
}
过去 1h 统计：影响了多少用户请求？

# 找出 config 和 WebUI 冲突的事件
{
  "message": "target already exists, skipping"
  OR "message": "preserved_fields"
}
最近的冲突：哪些字段被保留了？
```

### 告警规则配置

```yaml
alerts:
  - name: "model_filter_fail_open_high_rate"
    condition: "count(severity=WARN AND message contains 'fail-open') > 10 per 5m"
    severity: "warning"
    notification: "slack:#mlops-alerts"
    runbook: "check supported_models configuration and model name consistency"
  
  - name: "no_healthy_targets"
    condition: "severity=ERROR AND message contains 'no healthy targets'"
    severity: "critical"
    notification: "pagerduty:on-call"
    runbook: "restart target health checker, check target endpoints"
  
  - name: "config_sync_failure"
    condition: "severity=ERROR AND message contains 'sync failed'"
    severity: "high"
    notification: "slack:#ops"
    runbook: "check database connectivity and permissions"
```

---

## 日志字段标准化

所有日志都遵循以下字段规范，便于查询和聚合：

```go
// 基础字段（所有日志必有）
logger.With(
    zap.String("request_id", reqID),           // 请求唯一 ID，便于追踪
    zap.String("component", "model-routing"),  // 组件标识
    zap.String("path", r.URL.Path),            // API 路径
    ...
)

// 诊断字段（根据情况选择）
logger.With(
    zap.String("requested_model", model),      // 用户请求的模型
    zap.String("selected_target", targetURL),  // 选中的 target
    zap.String("error_code", "model_not_supported"),  // 标准化错误代码
    zap.String("diagnosis", "..."),            // 诊断信息
    zap.String("recovery_suggestion", "..."),  // 恢复建议
    ...
)
```

这样便于日志查询系统进行聚合、监控和告警。
