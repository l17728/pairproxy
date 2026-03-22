# PairProxy 产品演进路线图

**版本**: v2.0
**更新日期**: 2026-03-22
**基准版本**: v2.18.0

---

## 背景与定位

2026 年 AI 开发工作流中，代理网关正在从"转发工具"演变为"智能路由中枢"。以 LiteLLM 为代表的 Router 层提供了虚拟模型抽象、跨 Provider Fallback、延迟感知路由等能力，已成为多模型协作场景的工程化标准。

**PairProxy 与 LiteLLM 的根本差异**

| 维度 | PairProxy | LiteLLM |
|------|-----------|---------|
| 核心定位 | 企业级访问管控 | 智能路由代理 |
| 用户身份 | JWT 认证，知道"谁在用" | 无用户概念 |
| 配额管理 | 按用户/分组 token 配额 | 无 |
| 审计合规 | 完整操作审计日志 | 无 |
| 路由智能 | 加权随机 + 健康检查 | 延迟/成本/语义多策略 |

**结论**：PairProxy 的护城河在于"知道谁在用"——配额感知路由、用户级策略、审计追溯是 LiteLLM 无法替代的。演进方向是**在企业管控基础上叠加路由智能**，而非成为另一个 LiteLLM。

---

## 现状能力盘点（v2.18.0）

| 能力 | 状态 | 备注 |
|------|------|------|
| 统一接口抽象 | ✅ | Anthropic + OpenAI + Ollama |
| 协议自动转换 | ✅ | Anthropic ↔ OpenAI，含图片/错误/model_mapping |
| 加权随机负载均衡 | ✅ | 跨同类节点 |
| 健康检查 + 熔断 | ✅ | 主动 + 被动双重 |
| 同类节点请求重试 | ✅ | 非流式自动换节点 |
| 成本追踪 | ✅ | 按模型定价，Dashboard 实时费用 |
| 用户/分组配额限流 | ✅ | 日/月 token + RPM |
| 模型名映射 | ✅ | `model_mapping` 配置，仅协议转换层 |
| LLM Target 动态管理 | ✅ | v2.7.0，CLI/WebUI/API 增删改查，无需重启 |
| Direct Proxy (sk-pp-) | ✅ | v2.9.0，无需 cproxy 直接用 API Key 接入 |
| PostgreSQL 支持 | ✅ | v2.13.0，替代 SQLite，解决 Worker 一致性问题 |
| Peer Mode（无主从） | ✅ | v2.14.0，PG 模式下节点完全对等 |
| HMAC Keygen（无碰撞） | ✅ | v2.15.0，替换指纹算法，256 位安全强度 |
| 训练语料采集 | ✅ | v2.16.0，JSONL 格式，质量过滤，文件轮转 |
| LLM 故障转移增强 | ✅ | v2.17.0，429/5xx 时自动 try-next |
| **语义路由** | **✅** | **v2.18.0，按 messages 语义意图缩窄候选池** |
| 跨 Provider Fallback 链（完整） | ❌ | v3.0 规划（现有 retry 为同类节点）|
| 延迟感知路由 | ❌ | v3.0 规划 |
| 虚拟模型别名（路由级） | ❌ | v3.0 规划 |
| 配额感知路由 | ❌ | v3.1 规划（差异化核心）|

---

## 已完成：v2.10.0 ~ v2.18.0 特性回顾

> 以下功能已在 v2.9.0（上一版路线图基准）之后的版本中完成实现。

| 版本 | 特性 | 状态 |
|------|------|------|
| v2.10.0 | OtoA 协议转换（OpenAI → Anthropic 双向）| ✅ 已完成 |
| v2.11.0 | 协议转换增强（图片块、错误转换、前缀替换）| ✅ 已完成 |
| v2.12.0 | Worker 节点一致性优化 | ✅ 已完成 |
| v2.13.0 | PostgreSQL 数据库支持，共享 PG 实例解决 Worker 一致性窗口 | ✅ 已完成 |
| v2.14.0 | Peer Mode，PG 模式下所有节点完全对等，任意节点处理管理操作 | ✅ 已完成 |
| v2.15.0 | HMAC-SHA256 Keygen，替换指纹嵌入算法，消除碰撞漏洞 | ✅ 已完成 |
| v2.16.0 | 训练语料采集 Corpus，JSONL 格式，质量过滤，文件轮转 | ✅ 已完成 |
| v2.17.0 | LLM 故障转移增强，429/5xx 触发 try-next，retry_on_status 配置 | ✅ 已完成 |
| v2.18.0 | **语义路由 Semantic Router**，按 messages 意图缩窄 LLM 候选池，规则 YAML+DB 双来源，分类失败降级 | ✅ 已完成 |

---

## v3.0 — 路由策略升级

> **目标**：解决生产环境中最常见的单点故障和路由效率问题。
> **影响范围**：`internal/lb/`、`internal/config/`、`sproxy.yaml` 配置格式（向后兼容）。

### F-1：跨 Provider Fallback 链

**场景**：首选 Claude API 遭遇限流或故障时，自动降级到备用模型，对用户完全透明。

**配置设计**（`sproxy.yaml`）：

```yaml
llm:
  fallback_chains:
    - name: "default"
      trigger: [rate_limit, server_error, timeout]   # 触发条件
      chain:
        - target: "https://api.anthropic.com"        # 首选
        - target: "https://api.deepseek.com"         # 次选（低成本）
        - target: "http://localhost:11434"            # 兜底（本地 Ollama）
```

**行为说明**：
- `rate_limit`（429）：立即切换，不计入健康检查失败
- `server_error`（5xx）：切换 + 触发被动健康检查计数
- `timeout`：切换到链中下一个节点
- Fallback 切换记录在 metrics 和审计日志中

**实现要点**：
- `lb.Balancer` 扩展 `PickWithFallback(excludeURLs []string)` 方法
- `proxy/sproxy.go` Director 改为循环尝试，每次将已失败节点加入排除列表
- 流式请求首个 chunk 到达前可重试；首 chunk 之后不可重试（已开始输出）

---

### F-2：路由策略扩展

在现有加权随机基础上，支持更多策略。

**配置设计**（`sproxy.yaml`）：

```yaml
llm:
  lb_strategy: "latency-based"   # weighted-random | latency-based | round-robin | cost-aware
```

**`latency-based` 策略**：
- 每个 target 维护滑动窗口 p50/p99 延迟（窗口大小 100 次请求）
- 路由时优先选 p99 最低的健康节点
- 冷启动（请求数 < 10）时回退到加权随机
- 延迟数据已在 `internal/metrics/` 包中采集，只需加路由决策逻辑

**`cost-aware` 策略**：
- 基于 `pricing` 配置中的 `input_per_1k` 计算每个 target 的预估成本
- 优先选单价最低的健康节点
- 适合批量处理、非实时场景

**实现要点**：
- `lb.Strategy` 接口新增 `LatencyBased`、`CostAware` 实现
- 延迟统计使用原子操作 + 环形缓冲，避免锁竞争

---

### F-3：虚拟模型别名（路由级全局）

**现状**：`model_mapping` 仅在协议转换层生效，且无法跨 target 路由。

**目标**：用户请求 `model: "fast"`，网关决定走哪个 target + 映射为哪个实际模型名。

**配置设计**（`sproxy.yaml`）：

```yaml
llm:
  model_aliases:
    "fast":   "claude-haiku-4-5"     # 低延迟任务 → 轻量模型
    "smart":  "claude-opus-4-6"      # 复杂任务 → 顶级模型
    "local":  "qwen2.5-coder:32b"    # 本地优先 → Ollama target
    "cheap":  "deepseek-chat"        # 成本敏感 → 低价模型
```

**行为说明**：
- 别名解析在 Director 阶段，请求体中的 `model` 字段被替换后再转发
- 别名可绑定特定 target（`local` → Ollama）；未绑定则由负载均衡选择
- 与 `model_mapping`（协议转换层）保持独立，互不干扰
- Dashboard 日志展示原始别名 + 解析后的实际模型名

**实现要点**：
- `internal/config/` 新增 `ModelAliases map[string]string`
- `proxy/sproxy.go` Director 中在路由选择前替换 `model` 字段
- target 绑定通过 `model_alias_bindings` 配置（可选）

---

## v3.1 — 成本智能路由

> **目标**：利用 PairProxy 独有的用户配额信息，实现差异化路由策略。
> **核心差异化**：LiteLLM 无法实现，因其不知道用户配额状态。

### F-4：配额感知路由（差异化核心功能）

**核心思想**：网关知道用户今日剩余配额，据此动态调整路由目标，在用户将要耗尽配额前自动降档，避免高价模型浪费。

**配置设计**（分组级）：

```yaml
# sproxy.yaml 分组策略（或通过 Dashboard/CLI 配置）
groups:
  engineering:
    quota_routing:
      tiers:
        - threshold: 80%    # 剩余配额 > 80%：使用高质量模型
          target_tag: "smart"
        - threshold: 30%    # 剩余配额 30%~80%：使用均衡模型
          target_tag: "fast"
        - threshold: 0%     # 剩余配额 < 30%：使用低成本模型
          target_tag: "cheap"
```

**行为说明**：
- 每次请求到达时，从配额缓存（已有）读取剩余配额百分比
- 根据 tier 选择对应的虚拟模型别名或 target tag
- 无感知降级：用户无需手动切换，日志中记录降级原因
- 管理员可在 Dashboard 实时查看各用户当前所在 tier

**实现要点**：
- 依赖 F-3 虚拟模型别名
- 配额读取复用现有 `quota.Checker` 的缓存逻辑（无额外 DB 查询）
- 路由决策注入 `proxy/sproxy.go` 的认证后、转发前阶段

---

### F-5：请求大小预估路由

**场景**：超长 Prompt 需要大上下文窗口模型；短 Prompt 优先用低延迟模型，避免资源浪费。

**配置设计**：

```yaml
llm:
  size_routing:
    - max_chars: 8000       # ~2k tokens：路由到低延迟模型
      target_tag: "fast"
    - max_chars: 80000      # ~20k tokens：路由到标准模型
      target_tag: "smart"
    - max_chars: 999999     # 超长：路由到大上下文模型
      target_tag: "long-context"
```

**实现要点**：
- 在 Director 读取 request body 时顺便统计 `content` 字段总字符数
- 估算公式：`estimated_tokens ≈ char_count / 3.5`（保守估算）
- 仅读取大小，不做语义解析，性能开销 < 0.1ms
- 非流式请求可完整读取 body；流式请求读取到 `messages` 字段后截断估算

---

## v3.2 — 高级特性

> **目标**：进一步提升性能和成本效率，适合大规模部署场景。

### F-6：Anthropic Prompt Caching 自动注入

**背景**：Anthropic Prompt Caching 可将重复系统提示的 token 成本降低 90%，但需要客户端在请求中显式标记 `cache_control`。大多数客户端（包括 Claude Code）不会自动加此标记。

**方案**：网关在转发前，对满足条件的系统提示自动注入缓存标记。

**触发条件**：
- 请求包含 `system` 字段
- `system` 字段长度 > 1024 tokens（短系统提示缓存收益不大）
- 目标 provider 为 `anthropic`

**实现**：

```go
// 在 Director 阶段修改请求体
if shouldInjectCacheControl(req, target) {
    body["system"] = appendCacheControl(body["system"])
}
```

**指标**：
- 新增 `pairproxy_prompt_cache_hits_total` metric
- Dashboard 展示缓存命中率和节省的 token 数

---

### F-7：多模型竞速（Fan-out，可选策略）

**场景**：对延迟极度敏感的任务，同时向两个模型发送相同请求，取最先返回的结果并取消另一个。

**适用场景**：
- 实时代码补全（< 500ms 响应要求）
- 需要高可用保证的关键路径

**配置**：

```yaml
llm:
  fanout:
    enabled: false          # 默认关闭，按需启用
    strategy: "first-wins"  # first-wins | best-quality（未来）
    max_targets: 2          # 同时竞速的 target 数量上限
    trigger_groups: ["vip"] # 仅对特定分组启用
```

**实现要点**：
- `context.WithCancel` 管理多个并发请求，首个成功后取消其余
- 计费仅按实际使用的 target 统计
- 流式请求不适用（无法"取消"已开始输出的流）

---

## 实现优先级总览

```
（基准：v2.18.0，以下为后续规划）

v3.0（路由策略升级）
├── F-1 跨 Provider Fallback 链     ★★★★★  语义路由后的自然延伸
├── F-2 路由策略扩展（延迟/成本）   ★★★★☆  延迟数据已有，加路由逻辑即可
└── F-3 虚拟模型别名（全局）        ★★★☆☆  配合语义路由规则使用效果更佳

v3.1（成本智能）
├── F-4 配额感知路由                ★★★★★  PairProxy 独有差异化，依赖 F-3
└── F-5 请求大小预估路由            ★★★☆☆  实现简单，适合大规模部署

v3.2（高级特性）
├── F-6 Prompt Caching 自动注入    ★★★☆☆  成本优化，适合 Anthropic 重度用户
└── F-7 多模型竞速 Fan-out         ★★☆☆☆  实现复杂，仅特定场景有价值
```

---

## 架构演进图

```
v2.x（基础版本）
  用户 → cproxy/sk-pp- → sproxy → [加权随机] → Target A
                                              → Target B

v2.18.0（当前）
  用户 → cproxy/sk-pp- → sproxy → [语义分类] → 候选池A（匹配规则）
                                     ↓ 无规则    → 完整候选池（加权随机）
                                     ↓ 分类失败  → 完整候选池（降级）

v3.0
  用户 → cproxy/sk-pp- → sproxy → [策略路由]  → Target A（首选）
                                    ↓ 失败       → Target B（Fallback）
                                    ↓ 失败       → Target C（兜底）

v3.1
  用户 → cproxy/sk-pp- → sproxy → [配额检查]
                                    ↓ 配额充足   → 高质量 Target
                                    ↓ 配额警戒   → 均衡 Target
                                    ↓ 配额耗尽   → 低成本 Target
```

---

## 配置向后兼容策略

所有新配置项均为**可选字段，有合理默认值**：

- `fallback_chains`：未配置时，行为与 v2.x 完全相同
- `lb_strategy`：新增策略值，默认值保持 `weighted-random`
- `model_aliases`：未配置时，不做别名解析
- `quota_routing`：未配置时，不做配额感知路由

已有 `sproxy.yaml` 无需修改即可升级到 v3.x。

---

## 相关文档

- 当前架构：`docs/CLUSTER_DESIGN.md`
- 协议转换：`docs/PROTOCOL_CONVERSION.md`
- 性能调优：`docs/PERFORMANCE.md`
- 故障容错分析：`docs/FAULT_TOLERANCE_ANALYSIS.md`
