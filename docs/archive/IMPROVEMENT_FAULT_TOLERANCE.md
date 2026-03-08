# PairProxy 故障处理改进方案

**版本**: v1.0  
**日期**: 2026-03-07  
**状态**: Draft

---

## 一、当前架构概述

```
                     ┌─────────────────────────────────────┐
开发者 A 的 cproxy ──▶  sp-1（primary，主节点）:9000       │──▶ Anthropic
开发者 B 的 cproxy ──▶  sp-2（worker，工作节点）:9000      │──▶ Anthropic
开发者 C 的 cproxy ──▶  sp-3（worker，工作节点）:9000      │──▶ Anthropic
                     └──────────────┬──────────────────────┘
                               Web Dashboard
                           只在 primary 上开启
```

### 节点职责

| 角色 | 职责 | 单点风险 |
|------|------|----------|
| **Primary** | Dashboard、Admin CLI、路由表管理、用量聚合、心跳接收 | ⚠️ 高 |
| **Worker** | 代理请求、本地用量记录、心跳上报 | ✅ 低 |

---

## 二、当前故障处理能力评估

### 评分矩阵

| 故障场景 | 检测时间 | 恢复时间 | 数据影响 | 评分 |
|----------|----------|----------|----------|------|
| Worker 故障 | 90s | 自动恢复 | 无 | ⭐⭐⭐⭐⭐ |
| Worker 重启 | 即时 | <10s | 无 | ⭐⭐⭐⭐⭐ |
| Primary 故障 | 即时 | **需人工** | 用量丢失 | ⭐⭐ |
| Primary 重启 | 即时 | ~60s | 部分用量丢失 | ⭐⭐⭐ |

### 当前可用性等级

**Silver (~99.5%)**

- 可承受 Worker 故障
- Primary 故障需人工干预
- 适合中小规模团队（<50人）

---

## 三、cproxy 故障期间行为详解

### 3.1 核心架构

```
┌─────────────────────────────────────────────────────────────────┐
│                         cproxy 架构                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   ┌─────────────┐      ┌─────────────────┐                      │
│   │   请求入口   │─────▶│    Balancer     │                      │
│   └─────────────┘      │  (WeightedRandom)│                      │
│                        └────────┬────────┘                      │
│                                 │                               │
│                        ┌────────▼────────┐                      │
│                        │  HealthChecker  │ (后台运行)            │
│                        │   主动检查      │                       │
│                        │   被动熔断      │                       │
│                        └─────────────────┘                      │
│                                                                  │
│   目标来源 (三源合并):                                            │
│   1. sproxy.primary (配置种子节点)                               │
│   2. sproxy.targets (配置静态 Worker 列表)                       │
│   3. routing-cache.json (上次运行的持久化路由表)                  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### 3.2 关键代码流程

**请求处理** (`internal/proxy/cproxy.go`):

```go
func (cp *CProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
    // 1. 加载 token
    tf, err := cp.tokenStore.Load(cp.tokenDir)
    
    // 2. 选择目标节点（只选择一次）
    target, err := cp.balancer.Pick()
    if err != nil {
        // 无健康节点 → 直接返回 502
        writeJSONError(w, http.StatusBadGateway, "no_healthy_target", ...)
        return
    }
    
    // 3. 发送请求到选中的目标（不重试）
    proxy := &httputil.ReverseProxy{
        ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
            // ⚠️ 请求失败直接返回错误，不重试其他节点
            writeJSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
        },
    }
    proxy.ServeHTTP(w, r)
}
```

**负载均衡选择** (`internal/lb/weighted.go`):

```go
func (b *WeightedRandomBalancer) Pick() (*Target, error) {
    // 计算健康且非排水节点的总权重
    total := 0
    for i := range b.tl.targets {
        if b.tl.targets[i].Healthy && !b.tl.targets[i].Draining {
            total += b.tl.targets[i].Weight
        }
    }
    if total == 0 {
        return nil, ErrNoHealthyTarget  // 无健康节点
    }
    
    // 加权随机选取（只选健康节点）
    for i := range b.tl.targets {
        t := &b.tl.targets[i]
        if !t.Healthy || t.Draining {
            continue  // 跳过不健康节点
        }
        // ...
    }
}
```

### 3.3 cproxy 是否会主动切换到其他节点？

**答案：会切换，但不是"请求级"主动切换**

```
┌─────────────────────────────────────────────────────────────────────┐
│                     cproxy 的两级切换机制                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ❌ 请求级重试：不存在                                                │
│     ┌──────────┐     选中 sp-1      ┌──────────┐                    │
│     │  请求 1  │ ──────────────────▶│   sp-1   │                    │
│     └──────────┘     请求失败        │  (故障)  │                    │
│          │                          └──────────┘                    │
│          ▼                                │                         │
│     ┌──────────┐                          ▼                         │
│     │  502     │                   ┌──────────┐                    │
│     │  直接返回 │                   │  不会重试 │                    │
│     └──────────┘                   │  sp-2/sp-3│                    │
│                                    └──────────┘                    │
│                                                                      │
│  ✅ 健康检查切换：存在（90s 后）                                      │
│     ┌──────────────┐                                                │
│     │ HealthChecker│ ──── 每 30s 检查一次                           │
│     └──────┬───────┘                                                │
│            │                                                         │
│            ▼                                                         │
│     ┌──────────────────────────────────────────┐                    │
│     │  GET sp-1/health → 失败                  │                    │
│     │  GET sp-1/health → 失败                  │                    │
│     │  GET sp-1/health → 失败 (第3次)          │                    │
│     │                                          │                    │
│     │  balancer.MarkUnhealthy(sp-1)            │                    │
│     └──────────────────────────────────────────┘                    │
│            │                                                         │
│            ▼                                                         │
│     ┌──────────────────────────────────────────┐                    │
│     │  后续请求：balancer.Pick()               │                    │
│     │  只返回 Healthy=true 的节点              │                    │
│     │  → sp-2 或 sp-3                          │                    │
│     └──────────────────────────────────────────┘                    │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

| 问题 | 答案 |
|------|------|
| **是否会切换？** | ✅ 会，但需要等待健康检查 |
| **切换时间？** | 最长 90 秒（3 次 × 30 秒） |
| **请求级重试？** | ❌ 不存在，失败直接返回 502 |
| **切换后行为？** | ✅ 后续请求自动路由到健康节点 |

### 3.4 故障场景时间线

#### 场景 A：Primary 故障（有 Worker 可用）

```
时间线:
═══════════════════════════════════════════════════════════════════════

T0: Primary (sp-1) 崩溃，停止响应

     cproxy 状态:
     targets = [sp-1(healthy), sp-2(healthy), sp-3(healthy)]

T0+请求1: 用户发起请求
          │
          │  balancer.Pick() → 随机选择 (sp-1/sp-2/sp-3)
          │  若选中 sp-1:
          │    → 连接失败 / 超时
          │    → ErrorHandler 触发
          │    → 返回 502 Bad Gateway
          │
          │  ⚠️ 不会自动重试其他节点！
          │  ⚠️ sp-1 仍标记为 Healthy
          │
T0+30s: 健康检查周期触发
          │  GET http://sp-1:9000/health → 失败
          │  recordFailure(sp-1): failures[sp-1] = 1
          │  (阈值是 3，未触发熔断)
          │
T0+60s: 第二次健康检查
          │  failures[sp-1] = 2
          │
T0+90s: 第三次健康检查失败
          │  failures[sp-1] = 3 >= failThreshold
          │  balancer.MarkUnhealthy(sp-1)
          │  发送 EventNodeDown 告警
          │
          │  cproxy 状态:
          │  targets = [sp-1(unhealthy), sp-2(healthy), sp-3(healthy)]
          │
T0+91s: 后续请求
          │  balancer.Pick() 只返回 sp-2 或 sp-3
          │  ✅ 请求成功
          │
═══════════════════════════════════════════════════════════════════════

影响：前 90 秒内，约 33% 的请求失败（假设 3 个节点权重相等）
```

#### 场景 B：Worker 故障

```
时间线:
═══════════════════════════════════════════════════════════════════════

T0: Worker (sp-2) 崩溃

T0+请求: 用户发起请求
          │
          │  balancer.Pick() → 随机选择
          │  若选中 sp-1 或 sp-3 → 请求成功 ✅
          │  若选中 sp-2 → 请求失败 502 ❌
          │
          │  概率分析:
          │  - 3 个节点，1 个故障 → 33% 概率失败
          │
T0+90s: 健康检查标记 sp-2 为不健康
          │
          │  后续请求 → 100% 成功
          │
═══════════════════════════════════════════════════════════════════════

影响：影响较小，Primary 通常健康
```

#### 场景 C：所有节点故障

```
时间线:
═══════════════════════════════════════════════════════════════════════

T0: sp-1, sp-2, sp-3 全部崩溃

T0+请求: 用户发起请求
          │
          │  balancer.Pick():
          │    total = 0 (所有节点 Healthy=false)
          │    return nil, ErrNoHealthyTarget
          │
          │  返回 502:
          │  {
          │    "error": "no_healthy_target",
          │    "message": "no healthy s-proxy available"
          │  }
          │
═══════════════════════════════════════════════════════════════════════

影响：完全不可用
```

#### 场景 D：节点恢复

```
时间线:
═══════════════════════════════════════════════════════════════════════

T0: sp-1 (之前 unhealthy) 恢复

T0+30s: 健康检查周期
          │
          │  GET http://sp-1:9000/health → 200 OK
          │  recordSuccess(sp-1):
          │    failures[sp-1] = 0
          │    balancer.MarkHealthy(sp-1)
          │    发送 EventNodeRecovered 告警
          │
T0+31s: 后续请求
          │
          │  balancer.Pick() 可能选中 sp-1
          │  ✅ 恢复正常服务
          │
═══════════════════════════════════════════════════════════════════════

恢复时间：~30 秒（一个健康检查周期）
```

### 3.5 路由表更新机制

#### 正常情况（Primary 健康）

```
cproxy                              sp-1 (Primary)
  │                                      │
  │  POST /v1/messages                   │
  │  X-Routing-Version: 7  ─────────────▶│
  │                                      │
  │                                      │ 检查版本
  │                                      │ serverVersion(12) > 7
  │                                      │
  │  200 OK                              │
  │  X-Routing-Version: 12  ◀────────────│
  │  X-Routing-Update: base64(...)       │
  │                                      │
  │  解码路由表                          │
  │  balancer.UpdateTargets()           │
  │  保存到 routing-cache.json          │
```

#### Primary 故障时

```
cproxy                              sp-1 (Primary)
  │                                      │
  │  POST /v1/messages                   │    ❌ 连接失败
  │  ───────────────────────────────────▶│
  │                                      │
  │  502 Bad Gateway                     │
  │  ◀───────────────────────────────────│
  │                                      │
  │  ⚠️ 无法获取路由表更新               │
  │  ⚠️ 新 Worker 无法被发现             │
  │  ✅ 但已有 Worker 仍可服务           │
```

### 3.6 cproxy 容错能力总结

| 场景 | cproxy 行为 | 用户体验 | 恢复时间 |
|------|-------------|----------|----------|
| **Primary 故障（有 Worker）** | 先 502，90s 后切换 Worker | 部分请求失败 | ~90s |
| **Worker 故障** | 部分请求失败，90s 后移除 | 小概率失败 | ~90s |
| **所有节点故障** | 全部 502 | 完全不可用 | 需人工恢复 |
| **节点恢复** | 自动检测并恢复 | 无感知 | ~30s |
| **新 Worker 加入** | 需通过 Primary 获取路由表 | 感知延迟 | 下次请求 |

### 3.7 对比：sproxy 有请求级重试

**sproxy** 的 `RetryTransport` 会自动重试其他节点：

```go
// internal/lb/retry_transport.go (sproxy 使用)
func (t *RetryTransport) RoundTrip(req *http.Request) {
    for attempt := 0; ; attempt++ {
        resp, err := t.Inner.RoundTrip(req)
        
        if resp.StatusCode < 500 {
            return resp, nil  // 成功
        }
        
        // 5xx 错误：选择下一个 target 重试
        next, _ := t.PickNext(req.URL.Path, tried)
        // 继续循环...
    }
}
```

**cproxy 缺少这个机制**，这是需要改进的关键点。

---

## 四、改进方案

### 改进项 1：Primary 高可用（P0 - 关键）

#### 问题

Primary 故障后：
- Dashboard 不可用
- Admin CLI 无法操作
- Worker 无法上报用量
- 路由表无法更新
- 新 Worker 无法注册

#### 方案 A：VIP 漂移（推荐）

```
┌──────────────────────────────────────────────────────────────┐
│                    VIP: 192.168.1.100                        │
│                      (浮动 IP)                                │
└───────────────────────┬──────────────────────────────────────┘
                        │
         ┌──────────────┴──────────────┐
         │                             │
    ┌────▼────┐                   ┌────▼────┐
    │ sp-1    │ ◀─── keepalived ──▶│ sp-2    │
    │ PRIMARY │    心跳检测         │ STANDBY │
    │ (Active)│                     │(Passive)│
    └─────────┘                     └─────────┘
```

**实现步骤**：

1. 部署两个 Primary 节点（sp-1, sp-2）
2. 使用 keepalived 实现 VIP 漂移
3. 共享 SQLite 数据库（NFS 或迁移到 PostgreSQL）

**配置示例**：

```yaml
# sp-1 (Active Primary)
cluster:
  role: "primary"
  self_addr: "http://192.168.1.100:9000"  # VIP
  ha:
    enabled: true
    mode: "active-passive"
    peer: "http://sp-2-internal:9000"
    priority: 100  # 高优先级为主

# sp-2 (Standby Primary)
cluster:
  role: "primary"
  self_addr: "http://192.168.1.100:9000"  # VIP
  ha:
    enabled: true
    mode: "active-passive"
    peer: "http://sp-1-internal:9000"
    priority: 50  # 低优先级为备
```

**优点**：
- 故障切换时间 <5 秒
- 客户端无需修改配置
- 成熟方案，运维简单

**缺点**：
- 需要共享存储或数据库迁移
- 增加部署复杂度

---

#### 方案 B：基于租约的自动选举（备选）

```
┌─────────────────────────────────────────────────────────────┐
│                     etcd / Consul                            │
│              (分布式协调服务)                                 │└───────────────────────┬─────────────────────────────┘
                        │
         ┌──────────────┼──────────────┐
         │              │              │
    ┌────▼────┐    ┌────▼────┐    ┌────▼────┐
    │ sp-1    │    │ sp-2    │    │ sp-3    │
    │ CANDIDATE│    │ LEADER  │    │ CANDIDATE│
    └─────────┘    └─────────┘    └─────────┘
```

**实现步骤**：

1. 部署 etcd 集群
2. 所有 Primary 节点通过租约竞争 Leader
3. Leader 负责接收心跳和聚合用量

**优点**：
- 无单点
- 自动故障转移

**缺点**：
- 引入新组件（etcd）
- 实现复杂

---

### 改进项 2：用量数据可靠性（P1 - 重要）

#### 问题

Primary 故障期间，Worker 上报的用量数据丢失。

#### 方案：Worker 本地持久化 + 延迟上报

```go
// internal/cluster/reporter.go 改进

type Reporter struct {
    // 新增：未上报记录队列
    pendingRecords []db.UsageRecord
    maxPending     int  // 最大积压数量
    retryInterval  time.Duration
}

func (r *Reporter) loop(ctx context.Context) {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // 1. 发送心跳
            r.sendHeartbeat(ctx)
            
            // 2. 上报用量（失败则保留本地）
            r.reportUsageWithRetry(ctx)
        }
    }
}

func (r *Reporter) reportUsageWithRetry(ctx context.Context) {
    records := r.loadPendingRecords()
    if len(records) == 0 {
        return
    }

    err := r.ReportUsage(ctx, records)
    if err != nil {
        r.logger.Warn("usage report failed, keeping locally",
            zap.Int("count", len(records)),
            zap.Error(err))
        // 保留本地，下次重试
        return
    }

    // 上报成功，清理本地记录
    r.clearPendingRecords()
}
```

**配置项**：

```yaml
cluster:
  role: "worker"
  usage_buffer:
    enabled: true
    max_pending_records: 10000  # 最大积压 1 万条
    retry_interval: 60s         # 重试间隔
    persist_path: "/var/lib/pairproxy/pending_usage.json"
```

---

### 改进项 3：健康检查优化（P2 - 建议）

#### 问题

当前健康检查间隔 30s，连续 3 次失败才标记不健康，最坏情况需 90s。

#### 方案：可配置的快速检测

```yaml
# cproxy.yaml
sproxy:
  health_check:
    interval: 10s           # 检查间隔
    timeout: 3s             # 单次超时
    failure_threshold: 2    # 连续失败阈值
    recovery_delay: 30s     # 自动恢复延迟
```

**权衡**：
- 更短间隔 → 更快检测，但增加网络开销
- 建议生产环境使用 10-15s 间隔

---

### 改进项 4：路由表主动发现（P2 - 建议）

#### 问题

cproxy 完全依赖响应头携带路由表更新，Primary 故障时无法获取新路由。

#### 方案：定期轮询路由端点

```go
// internal/proxy/cproxy.go 改进

func (cp *CProxy) startRoutingPoller(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // 尝试从已知节点获取路由表
            cp.pollRoutingTable(ctx)
        }
    }
}

func (cp *CProxy) pollRoutingTable(ctx context.Context) {
    targets := cp.balancer.Targets()
    for _, t := range targets {
        if !t.Healthy {
            continue
        }
        
        url := t.Addr + "/cluster/routing"
        resp, err := http.Get(url)
        if err != nil {
            continue
        }
        
        var rt cluster.RoutingTable
        json.NewDecoder(resp.Body).Decode(&rt)
        resp.Body.Close()
        
        cp.balancer.UpdateTargets(rt.ToTargets())
        cp.saveRoutingCache(&rt)
        return  // 成功则退出
    }
}
```

---

### 改进项 5：cproxy 请求级重试（P0 - 关键）

#### 问题

cproxy 当前行为：
1. `balancer.Pick()` 只选择一次目标节点
2. 请求失败后直接返回 502，**不会重试其他健康节点**
3. 用户需要等待健康检查（最长 90s）才能切换到健康节点

**对比 sproxy**：sproxy 有 `RetryTransport`，会自动重试其他 LLM 目标。

#### 影响

```
假设 3 个节点配置：sp-1 (primary), sp-2, sp-3

当 sp-1 故障时：
- 前 90 秒：约 33% 请求失败（随机选中 sp-1）
- 90 秒后：健康检查标记 sp-1 为 unhealthy
- 之后：100% 请求成功（路由到 sp-2/sp-3）

用户体验：每 3 个请求就有 1 个失败，持续 90 秒
```

#### 方案：实现请求级重试

```go
// internal/proxy/cproxy.go 改进

func (cp *CProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
    // ... token 验证逻辑 ...
    
    const maxRetries = 2
    tried := make(map[string]bool)
    var lastErr error
    
    for attempt := 0; attempt <= maxRetries; attempt++ {
        // 选择一个健康节点
        target, err := cp.balancer.Pick()
        if err != nil {
            // 无健康节点可用
            if lastErr != nil {
                writeJSONError(w, 502, "all_targets_failed", lastErr.Error())
            } else {
                writeJSONError(w, 502, "no_healthy_target", "no healthy s-proxy available")
            }
            return
        }
        
        // 跳过已尝试过的节点
        if tried[target.ID] {
            continue
        }
        tried[target.ID] = true
        
        // 发送请求
        resp, err := cp.forwardToTarget(r, target, tf)
        if err == nil && resp.StatusCode < 500 {
            // 成功（2xx/3xx/4xx）
            resp.Write(w)
            cp.healthChecker.RecordSuccess(target.ID)
            return
        }
        
        // 失败：记录被动熔断
        lastErr = err
        cp.healthChecker.RecordFailure(target.ID)
        
        // 5xx 错误：关闭响应体，继续重试
        if resp != nil {
            io.Copy(io.Discard, resp.Body)
            resp.Body.Close()
        }
        
        cp.logger.Warn("request failed, retrying with next target",
            zap.Int("attempt", attempt+1),
            zap.String("failed_target", target.ID),
            zap.Error(err),
        )
    }
    
    // 所有重试都失败
    writeJSONError(w, 502, "all_targets_exhausted", lastErr.Error())
}

func (cp *CProxy) forwardToTarget(r *http.Request, target *lb.Target, tf *auth.TokenFile) (*http.Response, error) {
    targetURL, _ := url.Parse(target.Addr)
    
    // 克隆请求
    cloned := r.Clone(r.Context())
    cloned.URL.Scheme = targetURL.Scheme
    cloned.URL.Host = targetURL.Host
    cloned.Host = targetURL.Host
    
    // 注入认证头
    cloned.Header.Del("Authorization")
    cloned.Header.Set("X-PairProxy-Auth", tf.AccessToken)
    
    return cp.transport.RoundTrip(cloned)
}
```

#### 配置项

```yaml
# cproxy.yaml
sproxy:
  retry:
    enabled: true
    max_retries: 2          # 最大重试次数（不含首次）
    retry_on_status: [502, 503, 504]  # 触发重试的状态码
```

#### 预期效果

```
改进前（sp-1 故障时）：
- 33% 请求立即失败（选中 sp-1）
- 需等待 90s 才能切换

改进后（sp-1 故障时）：
- 0% 请求立即失败
- 自动重试到 sp-2 或 sp-3
- 用户无感知切换
```

#### 权衡

| 方面 | 考量 |
|------|------|
| **延迟** | 重试会增加延迟，但比 502 错误好 |
| **幂等性** | 需确保请求幂等（LLM API 通常安全） |
| **复杂性** | 增加代码复杂度，但价值大 |

---

## 五、实施路线图

### Phase 1（短期，1-2 周）

| 改进项 | 优先级 | 工作量 | 风险 |
|--------|--------|--------|------|
| **cproxy 请求级重试** | **P0** | 中 | 低 |
| 健康检查优化 | P2 | 低 | 低 |
| 用量本地持久化 | P1 | 中 | 低 |

### Phase 2（中期，1-2 月）

| 改进项 | 优先级 | 工作量 | 风险 |
|--------|--------|--------|------|
| 路由表主动发现 | P2 | 中 | 低 |
| Primary VIP 漂移 | P0 | 中 | 中 |

### Phase 3（长期，3+ 月）

| 改进项 | 优先级 | 工作量 | 风险 |
|--------|--------|--------|------|
| 基于租约的自动选举 | P1 | 高 | 高 |
| 数据库迁移到 PostgreSQL | P1 | 高 | 中 |

---

## 六、预期效果

### 改进后可用性

| 指标 | 当前 | 目标 |
|------|------|------|
| 可用性等级 | Silver (99.5%) | Gold (99.9%) |
| Primary 故障恢复 | 需人工 | <30s 自动 |
| Worker 故障恢复 | 90s | 30s |
| **请求失败率（节点故障时）** | **33%（无重试）** | **0%（自动重试）** |
| 数据丢失风险 | 中 | 低 |

### cproxy 请求级重试效果对比

```
改进前（sp-1 故障，3节点配置）:
┌─────────────────────────────────────────────────────────────┐
│  T0 ──────────────────────────────────────────────▶ T0+90s │
│                                                              │
│  约 33% 请求失败（选中故障节点）                              │
│  用户看到 502 错误                                           │
│                                                              │
│  T0+90s 后：健康检查标记 sp-1 为 unhealthy                   │
│  后续请求 100% 成功                                          │
└─────────────────────────────────────────────────────────────┘

改进后（请求级重试）:
┌─────────────────────────────────────────────────────────────┐
│  T0 ──────────────────────────────────────────────────────▶ │
│                                                              │
│  0% 请求失败（自动重试到健康节点）                           │
│  用户无感知切换                                              │
│                                                              │
│  最大延迟增加：1-2 次重试 ≈ 额外 100-200ms                   │
└─────────────────────────────────────────────────────────────┘
```

### 改进后架构

```
                        ┌─────────────────────┐
                        │   VIP (浮动 IP)     │
                        │ 192.168.1.100:9000 │
                        └──────────┬──────────┘
                                   │
                    ┌──────────────┴──────────────┐
                    │                             │
               ┌────▼────┐                   ┌────▼────┐
               │ sp-1    │ ◀─── keepalived ──▶│ sp-2    │
               │ PRIMARY │                     │ PRIMARY │
               │ (Active)│                     │(Standby)│
               └────┬────┘                     └────┬────┘
                    │                             │
         ┌──────────┼──────────┐      ┌──────────┼──────────┐
         │          │          │      │          │          │
    ┌────▼────┐ ┌───▼───┐ ┌────▼────┐ │    ┌────▼────┐
    │ worker-1│ │worker-2│ │worker-3│ │    │worker-4│
    └─────────┘ └───────┘ └─────────┘ │    └─────────┘
                                      │
                              (共享 PostgreSQL)
```

---

## 七、附录：代码修改清单

### A. 健康检查配置化

**文件**: `internal/config/config.go`

```go
type HealthCheckConfig struct {
    Interval        time.Duration `yaml:"interval"`
    Timeout         time.Duration `yaml:"timeout"`
    FailureThreshold int          `yaml:"failure_threshold"`
    RecoveryDelay   time.Duration `yaml:"recovery_delay"`
}
```

### B. 用量本地持久化

**文件**: `internal/cluster/reporter.go`

```go
type Reporter struct {
    // ... existing fields ...
    pendingPath string  // 新增：持久化路径
    maxPending  int     // 新增：最大积压数
}
```

### C. 路由表轮询

**文件**: `internal/proxy/cproxy.go`

```go
func (cp *CProxy) startRoutingPoller(ctx context.Context) {
    // 新增：定期轮询路由端点
}
```

### D. cproxy 请求级重试

**文件**: `internal/proxy/cproxy.go`

```go
// 修改 serveProxy 方法，添加重试逻辑
func (cp *CProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
    const maxRetries = 2
    tried := make(map[string]bool)
    
    for attempt := 0; attempt <= maxRetries; attempt++ {
        target, err := cp.balancer.Pick()
        if err != nil {
            // 无健康节点
            writeJSONError(w, 502, "no_healthy_target", ...)
            return
        }
        
        if tried[target.ID] {
            continue
        }
        tried[target.ID] = true
        
        resp, err := cp.forwardToTarget(r, target, tf)
        if err == nil && resp.StatusCode < 500 {
            resp.Write(w)
            cp.healthChecker.RecordSuccess(target.ID)
            return
        }
        
        // 失败：记录并重试
        cp.healthChecker.RecordFailure(target.ID)
    }
}
```

**文件**: `internal/config/config.go`

```go
type RetryConfig struct {
    Enabled        bool     `yaml:"enabled"`
    MaxRetries     int      `yaml:"max_retries"`
    RetryOnStatus  []int    `yaml:"retry_on_status"`
}
```

---

## 八、参考文档

- [CLUSTER_DESIGN.md](./CLUSTER_DESIGN.md) - 当前集群设计
- [manual.md](./manual.md) - 用户手册
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) - 故障排查