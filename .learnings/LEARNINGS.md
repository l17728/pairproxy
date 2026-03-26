# Learnings

Project-specific lessons learned, corrections, and best practices.

---

## [LRN-20260325-001] best_practice

**Logged**: 2026-03-25T09:00:00+08:00
**Priority**: high
**Status**: promoted
**Area**: tests

### Summary
测试用例中使用相同的重复值会掩盖逻辑防重写 bug，导致即使保护条件被删除测试也能通过

### Details
在 `feedOpenAIChunk` 的 error 分支新增 debug 日志时，意外删除了 `if !c.modelFound && chunk.Model != ""` 条件判断，使 `ModelActual` 变为无条件赋值。但现有测试 `TestCollectorOpenAIStreaming` 中所有 chunk 均使用完全相同的 model 字符串 `"gpt-4o-2024-08-06"`，因此即使保护条件缺失，每次覆盖值与原值相同，测试仍然通过。

**Root cause of test blindness**: 防重写逻辑的测试必须让后续输入携带**不同**的值，才能验证"首次设置后不再修改"的语义。所有 chunk 使用相同值，无法区分"写了一次"和"写了 N 次但每次值相同"。

### Suggested Action
为所有带有 "once-set, not overwritten" 语义的字段编写测试时，必须：
1. 让第一个有效输入设置初始值（如 `"gpt-4o-2024-08-06"`）
2. 让后续输入携带**明确不同**的值（如 `"gpt-4o-mini"` 或 `"WRONG-MODEL"`）
3. 断言最终值等于首次设置的值，而非后续值

### Resolution
- **Resolved**: 2026-03-25T09:57:00+08:00
- **Commit/PR**: pending
- **Notes**: 新增 `TestFeedOpenAIChunkModelNotOverwritten`、`TestFeedAnthropicChunkModelNotOverwritten`、`TestFeedOpenAIChunkModelEmptyStringSkipped` 三个测试，后续同类 bug 可在运行期被捕获

### Metadata
- Source: error
- Related Files: internal/corpus/collector.go, internal/corpus/collector_test.go
- Tags: testing, regression, once-set-semantics, modelFound

---

## [LRN-20260325-002] best_practice

**Logged**: 2026-03-25T09:00:00+08:00
**Priority**: high
**Status**: promoted
**Area**: tests

### Summary
在一个 if-err-return 块中插入日志时，极易将紧跟其后的代码行意外纳入 error 分支或破坏结构

### Details
原代码结构：
```go
if !c.modelFound && chunk.Model != "" {
    c.record.ModelActual = chunk.Model
    c.modelFound = true
}
```
修改时在前面插入了 `if err != nil { log; return }`，整个 if 块被误删，赋值语句变成顶层裸代码，并多出一个悬空的 `}`，导致编译错误。

Go 语言中，在已存在条件块前插入新的 `if-return` 时，若不够谨慎，很容易：
- 删掉原有 `if` 的开头行而保留 `}` (产生悬空括号)
- 或将原有赋值移入 error 分支内部（逻辑反转）

### Suggested Action
插入 `if err != nil { return }` 之后，立即检查：
1. 原有条件块是否完整保留（`if ... {` 开头和对应的 `}` 结尾）
2. 新旧代码块之间的缩进层次是否正确
3. 优先通过 `go build` 验证语法，再提交

### Resolution
- **Resolved**: 2026-03-25T08:00:00+08:00
- **Notes**: 已修复，`if !c.modelFound && chunk.Model != ""` 条件完整恢复

### Metadata
- Source: error
- Related Files: internal/corpus/collector.go
- Tags: go, refactoring, if-err-return, structural-corruption

---

## [LRN-20260325-003] best_practice

**Logged**: 2026-03-25T10:00:00+08:00
**Priority**: medium
**Status**: resolved
**Area**: tests

### Summary
新增 Exported API 时必须同步补充专项测试；间接验证不等于专项测试

### Details
`WrittenCount()`、`QueueDepth()` 上线时均无测试。`DroppedCount()` 仅在 `TestWriterDropWhenFull` 中通过字段间接验证，未直接调用方法本身。三个运维可观测性 API 均缺乏专项断言。

### Resolution
- **Resolved**: 2026-03-25T10:27:00+08:00
- **Notes**: 新增 `TestWriterWrittenCount`、`TestWriterQueueDepth`、`TestWriterDroppedCount`

### Metadata
- Source: conversation
- Related Files: internal/corpus/writer.go, internal/corpus/writer_test.go
- Tags: testing, exported-api, checklist

---

## [LRN-20260325-004] best_practice

**Logged**: 2026-03-25T10:00:00+08:00
**Priority**: medium
**Status**: pending
**Area**: tests

### Summary
新增 provider 路径（如 OpenAI malformed chunk）时，需同步检查已有测试是否对称覆盖了新路径

### Details
`TestCollectorMalformedChunks` 仅测试了 Anthropic provider 的 malformed chunk 容错，OpenAI/Ollama 路径完全缺失对应测试。当 `feedOpenAIChunk` 的错误处理逻辑被修改时，没有任何测试能捕获 OpenAI 路径上的回归。

理想状态：每个 provider 的流式处理函数（`feedAnthropicChunk`、`feedOpenAIChunk`）应各自拥有独立的 malformed-then-recover 测试。

### Suggested Action
每次新增或修改 provider-specific 处理逻辑时，检查是否所有 provider 都有：
1. 正常流式测试
2. malformed chunk 容错测试
3. 非流式测试

### Metadata
- Source: conversation
- Related Files: internal/corpus/collector.go, internal/corpus/collector_test.go
- Tags: testing, provider-symmetry, malformed-chunk

---

## [LRN-20260325-005] bug

**Logged**: 2026-03-25T21:00:00+08:00
**Priority**: critical
**Status**: resolved
**Area**: health-check, runtime-sync

### Summary
通过 WebUI / REST API 增删改启停 LLM target 后，运行中的 `llmBalancer` 和 `llmHC` 从未被通知，导致新增 target 永远不健康、删除 target 仍被路由。

### Root Cause
`AdminLLMTargetHandler` 和 `dashboard.Handler` 的写操作只调用 `llmTargetRepo`（写 DB），没有任何对 `llmBalancer`/`llmHC` 的引用。`llmBalancer` 和 `llmHC` 仅在进程启动时初始化一次，此后对 DB 的变更对它们完全透明。

```
写操作路径：WebUI → handler.llmTargetRepo.Create(target) → DB ✓
                                                           ↓
                                          llmBalancer 不知道 ✗
                                          llmHC 不知道       ✗
```

另外 `checkAll()` 里 `if len(hc.healthPaths) > 0` 的跳过逻辑会让不在 healthPaths 中的 target 完全跳过主动检查，永远不健康。

### Fix
1. `lb.HealthChecker.UpdateHealthPaths(paths)` — 加锁替换 healthPaths 映射，`checkAll()` 改为持锁拷贝后再使用，避免并发读写
2. `lb.HealthChecker.CheckTarget(id)` — 对单个 target 立即发起一次主动检查（异步 goroutine）
3. `proxy.SProxy.SyncLLMTargets()` — 从 DB 重新加载全量活跃 target，同时更新 balancer + hc
4. `AdminLLMTargetHandler.SetSyncFn(fn)` / `dashboard.Handler.SetLLMSyncFn(fn)` — 注入回调，5 个写操作成功后调用
5. `main.go` 连线：`llmTargetHandler.SetSyncFn(sp.SyncLLMTargets)` + `dashHandler.SetLLMSyncFn(sp.SyncLLMTargets)`

### Anti-Pattern
> 当系统有 DB 持久层 + 内存运行时层两层状态时，写操作必须同时更新两层。只写 DB 会导致两层永久分裂。

每次 handler 只调用 repo 写 DB 时，必须问：**运行时内存状态有没有同步更新？**

### Resolution
- **Resolved**: 2026-03-25T21:00:00+08:00
- **Files**: `internal/lb/health.go`, `internal/proxy/sproxy.go`, `internal/api/admin_llm_target_handler.go`, `internal/dashboard/handler.go`, `internal/dashboard/llm_handler.go`, `cmd/sproxy/main.go`

### Metadata
- Source: bug
- Tags: runtime-sync, two-layer-state, health-check, webui

---

## [LRN-20260325-006] bug

**Logged**: 2026-03-25T21:00:00+08:00
**Priority**: high
**Status**: resolved
**Area**: health-check, new-node-admission

### Summary
新加入的 target 以 `Healthy=true` 乐观初始化，会用真实用户请求试错坏节点，而非先验证再路由。

### Root Cause
`SyncLLMTargets` 在组装新 target 时统一设置 `Healthy: true`，新 target 立即进入可路由池。若该节点实际不可达，前 `failThreshold`（默认3）次真实用户请求会失败，才触发被动熔断。

### Fix
- **有 HealthCheckPath**：新 target 以 `Healthy=false` 加入，`SyncLLMTargets` 立即调用 `hc.CheckTarget(url)` 发起单次主动检查（异步，秒级完成），检查通过后 `MarkHealthy`，无需等 30s ticker
- **无 HealthCheckPath**：保持 `Healthy=true`（没有主动检查机制，只能乐观初始化，依赖被动熔断，这是合理的权衡）
- 已存在的 target 在 Sync 时**保留其当前健康/排水状态**，不因 Sync 而误重置被熔断的节点

### Anti-Pattern
> `UpdateTargets` 会完全替换目标列表。若新列表中所有 target 均设 `Healthy=true`，则已被被动熔断标记为不健康的节点会被错误复活，重新接受流量。

Sync 时必须：先快照现有 balancer 的健康状态，再在新列表中保留已知 target 的状态。

### Checklist（新节点入场）
1. 有主动检查路径 → `Healthy=false` + 立即 `CheckTarget`
2. 无主动检查路径 → `Healthy=true`（被动熔断兜底）
3. 已存在节点 → 保留 `Healthy` + `Draining` 状态

### Resolution
- **Resolved**: 2026-03-25T21:00:00+08:00
- **Files**: `internal/proxy/sproxy.go`, `internal/lb/health.go`
- **Tests**: `TestSyncLLMTargets_NewTargetWithHealthPath_StartsUnhealthyThenRecovers`, `TestCheckTarget_HealthyServerBecomesPickable`

### Metadata
- Source: bug
- Tags: new-node-admission, optimistic-healthy, health-check, passive-circuit-breaker

---

## [LRN-20260325-007] best_practice

**Logged**: 2026-03-25T21:00:00+08:00
**Priority**: high
**Status**: promoted
**Area**: tests, concurrency

### Summary
`checkAll()` 直接读取 `hc.healthPaths` 而不加锁，与 `UpdateHealthPaths()` 产生 data race；测试中断言切换后旧 target 不再被检查，但因并发飞行中的请求存在过渡性窗口，断言过严导致偶发失败。

### Details
- `checkAll()` 在持锁期间拷贝 `healthPaths` 后再使用，避免并发读写
- `UpdateHealthPaths` 切换后，当前正在执行的 `checkOneWithPath` goroutine 已用旧路径发出请求，无法撤回，这是正常的过渡窗口（非 bug）
- 测试断言应允许切换后最多 1 次旧路径的过渡性检查，而非严格断言为 0

### Anti-Pattern
> 对并发系统做"切换后立即变零"的精确断言，不考虑过渡窗口，会产生偶发 flaky test。
并发状态机的测试应断言最终状态（N 个周期后），而非瞬间状态。

### Suggested Test Pattern
- 对于切换语义：断言切换后「最多 M 次旧行为」（过渡容忍），而非「切换后零次」
- 对于最终一致性：poll + timeout，而非固定 sleep

### Metadata
- Source: conversation
- Tags: concurrency, flaky-test, transition-window, checkAll

---
