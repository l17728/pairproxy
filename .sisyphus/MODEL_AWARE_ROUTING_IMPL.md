# Model-Aware Routing 完整实现方案

## 1. weightedPickExcluding 函数完整实现

替换 Line 996 开始的函数：

```go
// weightedPickExcluding 从 llmBalancer 中选取健康 target，排除 tried，并应用 provider 过滤、语义候选集过滤和模型过滤。
// 执行顺序：provider 过滤 → model 过滤（fail-open）→ 加权随机
func (sp *SProxy) weightedPickExcluding(path, requestedModel string, tried map[string]bool, candidateFilter map[string]bool) (*lb.LLMTargetInfo, error) {
	all := sp.llmBalancer.Targets()
	preferred := preferredProvidersByPath(path)

	// 步骤 1: provider + tried + candidateFilter 过滤（基础过滤）
	filter := func(targets []lb.Target, providerFilter map[string]bool) []lb.Target {
		var out []lb.Target
		for _, t := range targets {
			if !t.Healthy || tried[t.ID] {
				continue
			}
			if len(candidateFilter) > 0 && !candidateFilter[t.ID] {
				continue
			}
			if providerFilter != nil {
				prov := sp.providerForURL(t.ID)
				if !providerFilter[prov] {
					continue
				}
			}
			out = append(out, t)
		}
		return out
	}

	// 第一次尝试：带 provider 偏好的过滤
	candidates := filter(all, preferred)
	usedFallback := false
	if len(candidates) == 0 && preferred != nil {
		// provider 没结果，回退到无 provider 限制
		candidates = filter(all, nil)
		usedFallback = true
		sp.logger.Warn("weightedPickExcluding: falling back from provider filter",
			zap.String("path", path),
			zap.Int("available_targets", len(all)))
	}
	if len(candidates) == 0 {
		return nil, lb.ErrNoHealthyTarget
	}

	// 步骤 2: 模型过滤（两级 fail-open）
	// auto 模式不过滤（让所有 target 参与负载均衡）
	if requestedModel != "" && requestedModel != "auto" {
		modelFiltered := filterByModel(candidates, requestedModel)
		if len(modelFiltered) > 0 {
			candidates = modelFiltered
		} else {
			// model 过滤无结果，fail-open 回退到 provider 过滤后的候选集
			sp.logger.Warn("weightedPickExcluding: no LLM target supports requested model, using fail-open routing",
				zap.String("requested_model", requestedModel),
				zap.Int("available_targets_before_model_filter", len(candidates)),
			)
			// candidates 保持原值（fail-open）
		}
	}

	// 步骤 3: 加权随机选择
	total := 0
	for _, c := range candidates {
		total += c.Weight
	}
	r := rand.IntN(total)
	for i := range candidates {
		r -= candidates[i].Weight
		if r < 0 {
			sp.logger.Debug("weightedPickExcluding: selected target",
				zap.String("target_id", candidates[i].ID),
				zap.String("requested_model", requestedModel),
				zap.Bool("used_provider_fallback", usedFallback),
			)
			return sp.llmTargetInfoForURL(candidates[i].ID), nil
		}
	}
	return sp.llmTargetInfoForURL(candidates[0].ID), nil
}
```

## 2. buildRetryTransport 函数修改

替换 Line 1127 开始的函数（签名和 PickNext 闭包）：

```go
// buildRetryTransport 为代理请求构建重试传输（使用 llmBalancer）
// requestedModel: 为保证重试一致性，需要与首次请求相同的模型过滤条件
func (sp *SProxy) buildRetryTransport(userID, groupID, effectivePath, requestedModel string) http.RoundTripper {
	if sp.llmBalancer == nil {
		return sp.transport
	}
	maxRetries := sp.maxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	return &lb.RetryTransport{
		Inner:         sp.transport,
		MaxRetries:    maxRetries,
		RetryOnStatus: sp.retryOnStatus,
		PickNext: func(_ string, tried []string) (*lb.LLMTargetInfo, error) {
			// 重试时传入相同的 requestedModel，确保过滤条件一致
			return sp.pickLLMTarget(effectivePath, userID, groupID, requestedModel, tried, nil)
		},
		OnSuccess: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordSuccess(targetURL)
			}
		},
		OnFailure: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordFailure(targetURL)
			}
		},
		Logger: sp.logger,
	}
}
```

## 3. serveProxy 中的修改

### 3a. 前移模型提取（在 pickLLMTarget 之前）

找到约 Line 1365 的模型提取代码，确保它在 pickLLMTarget 调用之前执行：

```go
	// 提取客户端请求的模型名（用于模型感知路由 F2 + auto 模式 F3）
	requestedModel := extractModel(r)
	if requestedModel == "" && len(bodyBytes) > 0 {
		requestedModel = extractModelFromBody(bodyBytes)
	}
	if requestedModel != "" {
		sp.logger.Debug("model-aware routing: extracted model from request",
			zap.String("request_id", reqID),
			zap.String("model", requestedModel),
		)
	}

	// [... 后续代码继续使用 requestedModel ...]
```

### 3b. 调用 buildRetryTransport 时传入 requestedModel

找到 buildRetryTransport 的调用位置（约 Line 1250），改为：

```go
	// OLD: retryRT := sp.buildRetryTransport(claims.UserID, claims.GroupID, effectivePath)
	// NEW:
	retryRT := sp.buildRetryTransport(claims.UserID, claims.GroupID, effectivePath, requestedModel)
```

### 3c. pickLLMTarget 调用传入 requestedModel

找到 pickLLMTarget 的调用位置，改为：

```go
	// OLD: firstInfo, pickErr := sp.pickLLMTarget(r.URL.Path, claims.UserID, claims.GroupID, tried, semanticCandidates)
	// NEW:
	firstInfo, pickErr := sp.pickLLMTarget(r.URL.Path, claims.UserID, claims.GroupID, requestedModel, tried, semanticCandidates)
```

### 3d. Auto 模式处理（在 pickLLMTarget 成功后、协议转换前）

在 pickLLMTarget 成功选定 target 之后、协议转换之前，添加：

```go
	// Auto 模式处理（F3）：选定 target 后，用 target 的 auto_model 重写请求体中的 model 字段
	if requestedModel == "auto" && sp.llmBalancer != nil && len(bodyBytes) > 0 {
		actualModel := sp.autoModelFromBalancer(firstInfo.ID)
		if actualModel != "" {
			rewritten := rewriteModelInBody(bodyBytes, "auto", actualModel)
			if len(rewritten) != len(bodyBytes) || string(rewritten) != string(bodyBytes) {
				bodyBytes = rewritten
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
				sp.logger.Info("auto mode: rewrote model in request body",
					zap.String("request_id", reqID),
					zap.String("target", firstInfo.ID),
					zap.String("actual_model", actualModel),
				)
			}
		} else {
			sp.logger.Debug("auto mode: no auto_model configured, passing through 'auto' to LLM",
				zap.String("request_id", reqID),
				zap.String("target", firstInfo.ID),
			)
		}
	}
```

## 4. 配置同步函数修改

### 4a. syncConfigTargetsToDatabase 改用 Seed (F1)

将 Line 那一行的 `repo.Upsert(target)` 改为 `repo.Seed(target)`，并确保构造 target 时 `IsEditable: false` 被设置（Seed 方法会强制覆盖为 true）。

### 4b. loadAllTargets 新增反序列化 (F2+F3)

在现有 ModelMappingJSON 反序列化之后添加：

```go
		// 反序列化 SupportedModelsJSON
		var supportedModels []string
		if dt.SupportedModelsJSON != "" && dt.SupportedModelsJSON != "[]" {
			if err := json.Unmarshal([]byte(dt.SupportedModelsJSON), &supportedModels); err != nil {
				sp.logger.Warn("failed to parse supported_models, treating as unrestricted",
					zap.String("url", dt.URL),
					zap.String("raw", dt.SupportedModelsJSON),
					zap.Error(err),
				)
			}
		}
		// AutoModel 直接读取字符串字段

		targets = append(targets, config.LLMTarget{
			URL:             dt.URL,
			APIKey:          apiKey,
			Provider:        dt.Provider,
			Name:            dt.Name,
			Weight:          dt.Weight,
			HealthCheckPath: dt.HealthCheckPath,
			ModelMapping:    modelMapping,
			SupportedModels: supportedModels,  // 新增
			AutoModel:       dt.AutoModel,     // 新增
		})
```

### 4c. SyncLLMTargets 补充新字段 (F2+F3)

在构建 lb.Target 时补充新字段：

```go
		lbTargets = append(lbTargets, lb.Target{
			ID:              t.URL,
			Addr:            t.URL,
			Weight:          w,
			Healthy:         healthy,
			Draining:        draining,
			SupportedModels: t.SupportedModels,  // 新增
			AutoModel:       t.AutoModel,        // 新增
		})
```

添加统计日志（在 UpdateTargets 之后）：

```go
	// 统计带新字段的 target 数量
	countWithModels := 0
	countWithAutoModel := 0
	for _, t := range lbTargets {
		if len(t.SupportedModels) > 0 {
			countWithModels++
		}
		if t.AutoModel != "" {
			countWithAutoModel++
		}
	}

	sp.logger.Info("SyncLLMTargets: balancer and health checker updated",
		zap.Int("targets", len(lbTargets)),
		zap.Int("health_check_paths", len(healthPaths)),
		zap.Int("credentials", len(credentials)),
		zap.Int("with_model_filter", countWithModels),
		zap.Int("with_auto_model", countWithAutoModel),
		zap.Int("new_targets_checking", len(newTargetsWithPath)),
	)
```

## 5. 其他改动

- `admin_llm_target_handler.go` - Create/Update 请求新增 `SupportedModels`, `AutoModel` 字段及对应的序列化逻辑
- `admin_llm_target.go` - add/update 命令新增 `--supported-models` 和 `--auto-model` flag  
- `config/sproxy.yaml.example` - 示例中新增字段说明

## 验证清单

```bash
go build ./...            # 编译检查
make test                 # 全量测试（包含新增 34 个测试）
make test-race -count=10  # 并发检测
```

## 日志输出示例

```
INFO  seed: inserting new config target
      url=https://api.anthropic.com provider=anthropic

INFO  model-aware routing: extracted model from request
      request_id=req-12345 model=claude-sonnet-4

WARN  weightedPickExcluding: no LLM target supports requested model, using fail-open routing
      requested_model=llama3 available_targets_before_model_filter=2

INFO  auto mode: rewrote model in request body
      request_id=req-12345 target=https://api.anthropic.com actual_model=claude-sonnet-4-20250514

INFO  SyncLLMTargets: balancer and health checker updated
      targets=3 with_model_filter=2 with_auto_model=2
```
