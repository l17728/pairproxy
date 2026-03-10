# PairProxy 项目总结

**项目名称**: PairProxy - 企业级 LLM API 代理网关
**模块路径**: `github.com/l17728/pairproxy`
**开源协议**: Apache License 2.0
**最新版本**: v2.8.0
**Go 版本**: 1.24

---

## 目录

1. [项目概述](#1-项目概述)
2. [系统架构](#2-系统架构)
3. [核心功能](#3-核心功能)
4. [技术实现](#4-技术实现)
5. [代码结构](#5-代码结构)
6. [测试体系](#6-测试体系)
7. [文档体系](#7-文档体系)
8. [部署方案](#8-部署方案)
9. [安全设计](#9-安全设计)
10. [可观测性](#10-可观测性)
11. [版本演进](#11-版本演进)
12. [生产就绪评估](#12-生产就绪评估)

---

## 1. 项目概述

### 1.1 项目定位

PairProxy 是一个**企业级 LLM API 透明代理网关**，专为多人共用 LLM API Key 的场景设计。它通过两层代理架构，实现了：

- **API Key 集中管控**：真实 API Key 仅存储在服务端，用户永远不可见
- **用量精确追踪**：每次请求的 input/output token 数、耗时、费用均有记录
- **配额精细管理**：按分组设置每日/每月 token 上限和每分钟请求次数上限
- **零侵入接入**：用户只需设置两个环境变量，无需修改应用配置

### 1.2 解决的问题

| 问题 | 传统方案 | PairProxy 方案 |
|------|----------|----------------|
| API Key 泄露风险 | 分发给每个人，无法追责 | 集中存储，用户永远不可见 |
| 用量不透明 | 月底看账单才知道超支 | 实时统计，Dashboard 可视化 |
| 无法限流 | 某人用掉大量资源无法拦截 | 配额检查，超额返回 429 |
| 费用分摊困难 | 无法精确计算每人消耗 | 按用户统计，支持导出 |

### 1.3 项目规模

| 指标 | 数值 |
|------|------|
| Go 源文件 | 160+ (internal 目录) |
| 总代码行数 | 65,000+ 行 |
| 内部包数量 | 22 个 |
| 测试用例 | 1,331+ 个 |
| 文档数量 | 12 个主要文档 |
| 文档总量 | 200KB+ |

---

## 2. 系统架构

### 2.1 两层代理架构

```
┌─────────────┐   HTTP/SSE    ┌───────────────────────┐   HTTPS   ┌──────────────────┐
│ Claude Code │──────────────▶│  cproxy  :8080        │──────────▶│  sproxy  :9000   │──▶ LLM API
│ (客户端应用) │               │  注入用户 JWT          │           │  验证身份 · 配额  │
└─────────────┘               └───────────────────────┘           │  统计用量 · 转发  │
                                                                       └──────────────────┘
```

### 2.2 组件职责

| 组件 | 部署位置 | 职责 |
|------|----------|------|
| **cproxy** | 每位开发者电脑 | 拦截请求、注入用户 JWT、负载均衡、健康检查 |
| **sproxy** | 公司服务器 | 验证身份、检查配额、注入真实 API Key、统计用量、管理用户 |

### 2.3 Header 替换流程

```
Claude Code 发出:
  POST http://127.0.0.1:8080/v1/messages
  Authorization: Bearer any-placeholder        ← 假 key

cproxy 转发给 sproxy:
  POST http://proxy.company.com:9000/v1/messages
  X-PairProxy-Auth: eyJhbGc...               ← 用户 JWT（替换原 Authorization）

sproxy 转发给 LLM:
  POST https://api.anthropic.com/v1/messages
  Authorization: Bearer sk-ant-REAL-KEY       ← 真实 API Key（替换 X-PairProxy-Auth）
```

### 2.4 集群架构

```
                    ┌───────────────────────────────┐
[开发者 A] cproxy ──┤                               │
[开发者 B] cproxy ──┤   sp-1  primary  :9000        │──▶ LLM API
[开发者 C] cproxy ──┤   sp-2  worker   :9000        │──▶ LLM API
                    │   sp-3  worker   :9000        │──▶ LLM API
                    └──────────────┬────────────────┘
                                   │
                            Web Dashboard
```

**集群特性**：
- Primary 节点：接受请求、管理路由表、提供服务
- Worker 节点：接受请求、上报用量到 Primary
- 路由表自动下发：通过响应头 piggyback 机制
- 心跳检测：默认 30s 心跳，90s 无响应触发驱逐

---

## 3. 核心功能

### 3.1 功能矩阵

| 分类 | 功能 | 版本支持 |
|------|------|----------|
| **零侵入接入** | 用户只需设置两个环境变量 | v1.0+ |
| **JWT 认证** | access token 24h，refresh token 7 天，自动刷新 | v1.0+ |
| **LDAP/AD 集成** | 一行配置接入企业目录，JIT 自动创建用户 | v1.0+ |
| **多 Provider 支持** | Anthropic / OpenAI / Ollama，按路径自动路由 | v1.0+ |
| **协议自动转换** | Claude CLI ↔ Ollama/OpenAI 自动协议转换，零配置 | v2.6+ |
| **协议转换进阶** | 图片转换、错误格式转换、id前缀替换、prefill/thinking拒绝、强制绑定、model_mapping | v2.8+ |
| **Token 统计** | 同步/流式(SSE)请求均精确统计，不缓冲不延迟 | v1.0+ |
| **费用估算** | 按模型定价配置，Dashboard 实时显示 USD 消耗 | v1.0+ |
| **用户配额** | 按分组设置每日/每月 token 上限，超额返回 429 | v1.0+ |
| **速率限制** | 每用户每分钟请求数(RPM)限制，滑动窗口算法 | v1.0+ |
| **负载均衡** | cproxy↔sproxy、sproxy↔LLM 两级负载均衡 | v1.0+ |
| **健康检查** | 主动(GET /health) + 被动(连续失败熔断)双重检查 | v1.0+ |
| **集群模式** | primary + worker 多节点，路由表自动下发 | v1.0+ |
| **Web Dashboard** | Go 模板 + Tailwind CSS，内嵌二进制 | v1.0+ |
| **Admin CLI** | 命令行管理用户、分组、配额、统计 | v1.0+ |
| **Prometheus 指标** | GET /metrics，标准文本格式 | v1.0+ |
| **Webhook 告警** | 节点故障、配额超限等事件推送 | v1.0+ |
| **OpenTelemetry 追踪** | 可选启用，支持 gRPC/HTTP/stdout exporter | v1.0+ |
| **登录频率限制** | 每 IP 5 次失败后锁定 5 分钟 | v1.0+ |
| **管理审计日志** | 所有用户/分组增删改操作记录 | v1.0+ |
| **Token 自动刷新** | cproxy 自动检测过期，5s 内换新 token | v1.0+ |
| **趋势图表** | Dashboard 概览页显示 7/30/90 天趋势 | v2.0+ |
| **用户自助页面** | 普通用户查看配额状态、用量历史 | v2.0+ |
| **对话内容追踪** | 按用户隔离记录完整对话内容(JSON 文件) | v2.4+ |
| **Worker 用量水印追踪** | 防止 primary 宕机时数据丢失 | v2.5+ |
| **请求级重试** | 非流式请求自动故障转移 | v2.5+ |
| **路由表主动发现** | CProxy 定期轮询获取路由更新 | v2.5+ |
| **健康检查配置化** | 超时/阈值/恢复延迟可配置 | v2.5+ |
| **请求体大小限制** | 10MB 限制防止 OOM | v2.5+ |
| **LLM Target 动态管理** | 配置文件 + 数据库双来源管理 LLM targets | v2.7+ |
| **CLI Target 管理** | targets list, target add/update/delete/enable/disable | v2.7+ |
| **REST API Target 管理** | 7 个端点支持完整 CRUD 操作 | v2.7+ |
| **WebUI Target 管理** | Dashboard 提供 LLM Target 管理界面 | v2.7+ |
| **配置文件同步** | 启动时自动同步配置文件 targets 到数据库 | v2.7+ |
| **URL 唯一性约束** | 数据库层面强制 URL 唯一性 | v2.7+ |
| **配置来源只读** | 配置文件来源的 targets 在 WebUI/API 中只读 | v2.7+ |
| **告警页面** | Dashboard 实时 WARN/ERROR 日志查看器，SSE 推送 | v2.8+ |
| **批量导入** | 文件模板批量创建分组/用户，CLI + WebUI + dry-run | v2.8+ |
| **GLM 风格 SSE 修复** | message_start.input_tokens=0 时从 message_delta 回填 | v2.8+ |

### 3.2 v2.8.0 新增特性

| 功能 | 说明 |
|------|------|
| **告警页面** | Dashboard `/dashboard/alerts`，实时展示 WARN/ERROR 日志，SSE 推送（`/api/admin/alerts/stream`） |
| **批量导入** | `sproxy admin import <file>` + WebUI `/dashboard/import`，支持分组/用户批量创建，`--dry-run` 预览 |
| **图片内容块转换** | Anthropic base64 image → OpenAI image_url，完整 URL / base64 数据 URL 两种格式 |
| **OpenAI 错误响应转换** | 上游返回 OpenAI 格式错误时，自动转为 Anthropic `{"type":"error"}` 格式 |
| **chatcmpl- 前缀替换** | OpenAI 响应 ID 前缀 `chatcmpl-` 替换为 `msg_`，符合 Anthropic 客户端期望 |
| **assistant prefill 拒绝** | 消息列表末尾包含 assistant 消息时，对 OpenAI/Ollama targets 返回 HTTP 400 |
| **thinking 参数拒绝** | 请求体包含 `thinking` 参数时，对 OpenAI/Ollama targets 返回 HTTP 400 |
| **强制 LLM 绑定** | 未配置 LLM target 的用户/分组发起请求时返回 HTTP 403 |
| **model_mapping 配置** | `model_mapping` 字段支持精确匹配和通配符 `*` 回退，Anthropic 模型名 → Ollama 模型名 |
| **GLM 风格 SSE 修复** | 检测 `message_start.input_tokens=0`，从 `message_delta.usage.input_tokens` 回填 |
| **测试覆盖增强** | 32+ 新测试用例，含 SSE 边界、协议转换边界、用户流量 E2E |

### 3.3 v2.7.0 新增特性

| 功能 | 说明 |
|------|------|
| **LLM Target 动态管理** | 配置文件 + 数据库双来源管理 LLM targets，支持运行时增删改查 |
| **数据库表** | llm_targets 表，17 个字段（URL、Provider、APIKeyID、Weight、IsActive 等） |
| **配置文件同步** | 启动时自动同步配置文件中的 targets 到数据库，幂等操作 |
| **CLI 命令** | `sproxy admin llm targets` - 列出所有 targets 及健康状态 |
| | `sproxy admin llm target add` - 添加新 target |
| | `sproxy admin llm target update` - 更新 target 配置 |
| | `sproxy admin llm target delete` - 删除 target |
| | `sproxy admin llm target enable/disable` - 启用/禁用 target |
| **REST API** | 7 个端点支持完整 CRUD 操作 |
| | `GET /api/admin/llm/targets` - 列出所有 targets |
| | `POST /api/admin/llm/targets` - 创建 target |
| | `GET /api/admin/llm/targets/{id}` - 获取单个 target |
| | `PUT /api/admin/llm/targets/{id}` - 更新 target |
| | `DELETE /api/admin/llm/targets/{id}` - 删除 target |
| | `POST /api/admin/llm/targets/{id}/enable` - 启用 target |
| | `POST /api/admin/llm/targets/{id}/disable` - 禁用 target |
| **WebUI 管理** | Dashboard 提供 LLM Target 管理界面，支持可视化操作 |
| **URL 唯一性** | 数据库层面强制 URL 唯一性约束，防止重复配置 |
| **配置来源标识** | Source 字段区分 config/database 来源 |
| **配置来源只读** | 配置文件来源的 targets 在 WebUI/API 中只读，防止误操作 |
| **健康状态集成** | 与现有健康检查机制集成，实时显示 target 状态 |
| **完整测试** | 50+ 测试用例，覆盖数据库层、配置同步、CLI、REST API、E2E |
| **文档完善** | 更新手册、API 文档、测试报告、验收报告 |

### 3.4 v2.6.0 新增特性

| 功能 | 说明 |
|------|------|
| **协议自动转换** | Anthropic Messages API ↔ OpenAI Chat Completions API 双向转换 |
| **智能检测** | 基于请求路径 + 目标 provider 自动触发转换 |
| **完整支持** | System 消息处理、结构化内容提取、流式/非流式双向转换 |
| **零配置** | 无需手动配置，自动启用 |
| **优雅降级** | 转换失败时自动回退到原始请求 |
| **完整日志** | INFO/DEBUG/WARN 三级日志，便于故障排查 |

### 3.5 v2.5.0 新增特性

| 功能 | 说明 |
|------|------|
| **Worker 用量水印追踪** | Reporter 批量上报机制，本地 DB 持久化未同步记录，网络恢复后自动补报 |
| **健康检查优化** | 可配置超时/阈值/恢复延迟 (health_check_timeout, passive_failure_threshold, recovery_delay) |
| **路由表主动发现** | CProxy 定期轮询 `/cluster/routing-poll` 端点，支持 304 Not Modified |
| **请求级重试** | 非流式请求自动重试到未尝试节点 (max_retries, retry_on_status) |
| **请求体大小限制** | 10MB 限制防止 OOM |

---

## 4. 技术实现

### 4.1 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| HTTP 反向代理 | `net/http/httputil.ReverseProxy` | 标准库，SSE 天然支持 |
| 数据库 | SQLite + GORM (`glebarez/sqlite`) | 纯 Go，无 CGO，单文件部署 |
| CLI | `spf13/cobra` | 标准 Go CLI 框架 |
| 日志 | `uber-go/zap` | 结构化日志，高性能 |
| 密码 | bcrypt (`golang.org/x/crypto`) | 行业标准 |
| JWT | `golang-jwt/jwt/v5` | 标准实现 |
| 前端 | Go HTML 模板 + Tailwind CSS CDN | 无需构建，内嵌二进制 |
| 追踪 | OpenTelemetry | 业界标准，可对接 Jaeger/Tempo |
| LDAP | `go-ldap/ldap/v3` | 企业目录集成 |

### 4.2 主要依赖

```go
// 核心依赖
github.com/glebarez/sqlite v1.11.0      // 纯 Go SQLite
github.com/golang-jwt/jwt/v5 v5.3.1     // JWT 处理
github.com/spf13/cobra v1.10.2          // CLI 框架
go.uber.org/zap v1.27.1                 // 结构化日志
golang.org/x/crypto v0.48.0             // bcrypt 密码
gorm.io/gorm v1.31.1                    // ORM

// 可观测性
go.opentelemetry.io/otel v1.40.0        // 分布式追踪
```

### 4.3 Streaming Token 捕获

sproxy 使用 `TeeResponseWriter` 包装响应流：

```
LLM SSE 流
  │
  ├── 同步写给客户端（零缓冲，不增加延迟）
  │
  └── 同时解析 SSE 事件行
        message_start  → 记录 input_tokens
        message_delta  → 记录 output_tokens
        message_stop   → 异步写入 SQLite usage_logs
```

### 4.4 用量数据可靠性（v2.5.0+）

```
Worker sp-2 (SQLite)
  │
  │  reporter.loop()
  │  every 30s: ReportUsage(pending_records)
  │  POST /api/internal/usage  ────────────▶  sp-1
  │                                            │
  │                                            │  usageWriter.Record(each_record)
  │                                            │  → written to sp-1's SQLite
  │                                            │
  │  [网络故障时]
  │  记录保留在本地 DB (synced=0)
  │  usageReportFails 计数递增
  │                                            │
  │  [网络恢复后]
  │  ListUnsynced() 读取未同步记录
  │  自动补报到 sp-1
```

---

## 5. 代码结构

```
pairproxy/
├── cmd/
│   ├── cproxy/main.go        # cproxy CLI 入口 (657 行)
│   ├── sproxy/main.go        # sproxy CLI 入口 + admin 子命令 (3175 行)
│   ├── mockllm/main.go       # 测试用 mock LLM 服务
│   └── mockagent/main.go     # 测试用 mock 客户端
│
├── internal/                  # 内部包 (153 个 Go 文件)
│   ├── auth/                 # JWT 管理、bcrypt、token 文件、LDAP
│   │   ├── jwt.go           # JWT 签发/验证，算法固定 HS256
│   │   ├── token_store.go   # 本地 token 文件管理
│   │   ├── password.go      # bcrypt 密码处理
│   │   ├── ldap.go          # LDAP/AD 认证
│   │   └── provider.go      # 认证提供者抽象
│   │
│   ├── proxy/                # cproxy/sproxy 核心 HTTP 处理器
│   │   ├── cproxy.go        # 客户端代理逻辑
│   │   ├── sproxy.go        # 服务端代理逻辑
│   │   ├── middleware.go    # 认证、RequestID、Recovery 中间件
│   │   ├── protocol_converter.go  # 协议自动转换（v2.6.0）
│   │   └── openai_compat.go # OpenAI 兼容层
│   │
│   ├── tap/                  # TeeResponseWriter + SSE 解析器
│   │   ├── tee_writer.go    # 零延迟流复制
│   │   └── sse_parser.go    # Anthropic/OpenAI SSE 解析
│   │
│   ├── lb/                   # 负载均衡、健康检查
│   │   ├── balancer.go      # Balancer 接口
│   │   ├── weighted.go      # 加权随机算法
│   │   ├── health.go        # 主动+被动健康检查
│   │   └── retry_transport.go # 请求级重试
│   │
│   ├── cluster/              # 集群管理
│   │   ├── manager.go       # 集群管理器
│   │   ├── peer_registry.go # 节点注册表
│   │   ├── reporter.go      # Worker 用量上报器
│   │   └── routing.go       # 路由表管理
│   │
│   ├── quota/                # 配额检查、速率限制
│   │   ├── checker.go       # 日/月配额检查
│   │   ├── rate_limiter.go  # RPM 滑动窗口限流
│   │   └── cache.go         # 配额缓存
│   │
│   ├── db/                   # SQLite + GORM 模型
│   │   ├── db.go            # 数据库连接管理
│   │   ├── models.go        # User, Group, UsageLog 等
│   │   ├── user_repo.go     # 用户 CRUD
│   │   ├── usage_repo.go    # 用量记录
│   │   └── audit_repo.go    # 审计日志
│   │
│   ├── api/                  # HTTP API 处理器
│   │   ├── auth_handler.go  # /auth/* 端点
│   │   ├── admin_handler.go # /api/admin/* 端点
│   │   ├── user_handler.go  # /api/user/* 端点
│   │   └── cluster_handler.go # 集群内部 API
│   │
│   ├── dashboard/            # Web Dashboard
│   │   ├── templates/       # Go HTML 模板
│   │   └── static/          # Tailwind CSS (CDN)
│   │
│   ├── metrics/              # Prometheus 指标
│   ├── alert/                # Webhook 告警
│   ├── track/                # 对话内容追踪
│   ├── config/               # YAML 配置加载
│   ├── otel/                 # OpenTelemetry 集成
│   ├── preflight/            # 启动前检查
│   └── version/              # 版本信息注入
│
├── test/
│   ├── e2e/                  # E2E 测试 (82+ 用例)
│   └── integration/          # 集成测试 (8 用例)
│
├── config/
│   ├── sproxy.yaml.example   # sproxy 配置示例
│   ├── cproxy.yaml.example   # cproxy 配置示例
│   ├── sproxy.service        # systemd 服务文件
│   └── sproxy-worker.service # worker systemd 服务文件
│
├── docs/                      # 文档目录
├── .github/workflows/         # CI/CD 配置
├── Makefile                   # 构建脚本
└── go.mod                     # Go 模块定义
```

---

## 6. 测试体系

### 6.1 测试类型

| 测试类型 | 测试数量 | 通过率 | 说明 |
|---------|---------|--------|------|
| 单元测试 (UT) | 1,142+ | 100% | 21 个包全量单元测试（含 v2.8.0 新功能） |
| 集成测试 | 8 | 100% | 真实数据库操作 |
| E2E 测试 (httptest) | 82 | 100% | httptest 自动化测试（含用户流量5个新E2E） |
| E2E 测试 (integration) | 68 | 100% | 真实进程集成测试 |
| 协议转换测试 | 31 | 100% | 协议转换专项测试（v2.8.0 增强） |
| **总计** | **1,331+** | **100%** | - |

### 6.2 测试覆盖率

| 模块 | 覆盖率 |
|------|--------|
| 总体 | ~75% |
| internal/auth | 85% |
| internal/db | 82% |
| internal/proxy | 80% |
| internal/quota | 88% |
| internal/lb | 80% |
| protocol_converter | 83.2% |

### 6.3 三种 E2E 测试方法

```bash
# 方法1: httptest 自动化测试（推荐日常开发）
go test ./test/e2e/...

# 方法2: 真实进程集成测试
go test -tags=integration ./test/e2e/...

# 方法3: 手动完整链路测试
./mockllm --addr :11434 &
./sproxy start --config test-sproxy.yaml &
./cproxy start --config test-cproxy.yaml &
./mockagent --url http://localhost:8080 --count 100
```

---

## 7. 文档体系

### 7.1 文档清单

| 文档 | 大小 | 说明 |
|------|------|------|
| `README.md` | - | 项目概述、快速开始 |
| `docs/manual.md` | 90KB+ | 完整用户手册（v2.8.0） |
| `docs/API.md` | 22KB | REST API 参考 |
| `docs/SECURITY.md` | 16KB | 安全模型说明 |
| `docs/TROUBLESHOOTING.md` | 17KB | 故障排查手册 |
| `docs/UPGRADE.md` | 13KB | 版本升级指南 |
| `docs/PERFORMANCE.md` | 6.3KB | 性能优化指南 |
| `docs/CLUSTER_DESIGN.md` | 14KB | 集群架构设计 |
| `docs/FAULT_TOLERANCE_ANALYSIS.md` | 24KB | 故障容错分析 (v2.5.0) |
| `docs/PROTOCOL_CONVERSION.md` | - | 协议转换文档 (v2.6.0+) |
| `docs/TEST_REPORT.md` | - | 测试报告（v2.8.0） |
| `docs/ACCEPTANCE_REPORT.md` | 22KB | 验收报告（v2.8.0） |
| `PROJECT_SUMMARY.md` | - | 项目总结（本文档） |

### 7.2 文档覆盖场景

| 角色 | 文档 | 用途 |
|------|------|------|
| 管理员 | manual.md, UPGRADE.md, SECURITY.md | 部署、配置、升级、安全 |
| 开发者 | README.md, manual.md 第6-8章 | 安装 cproxy、配置环境 |
| 运维 | OPENCLAW_OPS_GUIDE.md, TROUBLESHOOTING.md | 监控、告警、故障排查 |
| 集成开发 | API.md | REST API 集成 |

---

## 8. 部署方案

### 8.1 部署方式

| 方式 | 适用场景 | 说明 |
|------|----------|------|
| 二进制部署 | 生产环境 | `make build` 或下载 release |
| Docker 部署 | 容器化环境 | 多架构镜像，~15MB |
| systemd 部署 | Linux 生产环境 | 服务管理、自动重启 |
| 源码编译 | 开发环境 | `go build ./cmd/...` |

### 8.2 跨平台支持

| 平台 | 架构 | 二进制名称 |
|------|------|-----------|
| Linux | amd64, arm64 | cproxy, sproxy |
| macOS | amd64, arm64 | cproxy, sproxy |
| Windows | amd64 | cproxy.exe, sproxy.exe |

### 8.3 systemd 服务文件

```ini
# /etc/systemd/system/sproxy.service
[Unit]
Description=PairProxy Server Proxy
After=network.target

[Service]
Type=simple
User=pairproxy
Group=pairproxy
EnvironmentFile=/etc/pairproxy/sproxy.env
ExecStart=/usr/local/bin/sproxy start --config /etc/pairproxy/sproxy.yaml
Restart=on-failure
RestartSec=5

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true
MemoryDenyWriteExecute=true

[Install]
WantedBy=multi-user.target
```

### 8.4 CI/CD 流程

```yaml
# .github/workflows/release.yml
# 推送 tag 后自动执行：
1. 交叉编译 5 个平台二进制
2. 生成 SHA256SUMS.txt 校验文件
3. 创建 GitHub Release
4. 构建多架构 Docker 镜像
5. 推送到 ghcr.io/l17728/pairproxy
```

---

## 9. 安全设计

### 9.1 安全模型

```
┌─────────────────────────────────────────────────────────────┐
│                      威胁与防护                              │
├─────────────────────┬───────────────────────────────────────┤
│ JWT 算法混淆攻击     │ 强制 HS256，拒绝其他算法              │
│ Token 泄露          │ 24h 过期 + refresh token + 黑名单     │
│ 集群 API 未认证     │ fail-closed 策略，必须 shared_secret   │
│ SQL 注入            │ GORM 参数化查询                       │
│ 路径遍历            │ validateUsername 输入验证             │
│ DDoS 攻击           │ RPM 限流 + 登录限流                   │
│ 密码泄露            │ bcrypt 哈希存储                       │
│ LDAP 凭据拦截       │ 强制 LDAPS (TLS)                      │
└─────────────────────┴───────────────────────────────────────┘
```

### 9.2 JWT 安全

| 属性 | 值 | 说明 |
|------|-----|------|
| 签名算法 | HS256 | 强制固定，防止算法混淆 |
| Access Token TTL | 24h | 短期令牌，限制泄露影响 |
| Refresh Token TTL | 7d | 长期令牌，用于续期 |
| 黑名单 | 内存 + TTL | 支持主动吊销 |

### 9.3 集群 API 认证

```yaml
# Primary 配置
cluster:
  shared_secret: "${CLUSTER_SECRET}"  # 必填

# Worker 配置
cluster:
  shared_secret: "${CLUSTER_SECRET}"  # 必须与 Primary 相同
```

**fail-closed 策略**：
- `shared_secret` 为空时，所有集群 API 请求返回 401
- 单节点部署可留空（无 worker 调用内部 API）

### 9.4 文件权限

```bash
# 配置文件（含密钥）
chmod 600 /etc/pairproxy/sproxy.yaml

# 数据库文件
chmod 640 /var/lib/pairproxy/pairproxy.db
chown sproxy:sproxy /var/lib/pairproxy/

# Token 文件（用户本地）
chmod 600 ~/.config/pairproxy/token.json
```

---

## 10. 可观测性

### 10.1 Prometheus 指标

```
GET /metrics

# Token 用量
pairproxy_tokens_today{type="input"} 1234567
pairproxy_tokens_today{type="output"} 345678
pairproxy_tokens_month{type="input"} 12000000

# 请求统计
pairproxy_requests_today{status="success"} 417
pairproxy_requests_today{status="error"} 3

# 活跃用户
pairproxy_active_users_today 12

# 费用估算
pairproxy_cost_usd_today 12.345678

# 集群指标（v2.5.0+）
usage_report_fails 0
pending_records 0
```

### 10.2 Webhook 告警

```yaml
cluster:
  alert_webhook: "https://hooks.slack.com/services/..."
```

**告警事件**：
- 节点故障（worker 心跳超时）
- 配额超限（用户超额）
- 用量数据丢失（dropped > 0）

### 10.3 OpenTelemetry 追踪

```yaml
otel:
  enabled: true
  exporter: "grpc"  # 或 "http", "stdout"
  endpoint: "localhost:4317"
```

**支持的 exporter**：
- gRPC (Jaeger, Tempo)
- HTTP (Jaeger, Tempo)
- stdout (调试)

### 10.4 审计日志

```sql
-- audit_logs 表结构
CREATE TABLE audit_logs (
    id INTEGER PRIMARY KEY,
    admin_user TEXT,
    action TEXT,         -- create_user, delete_group, etc.
    target TEXT,
    details TEXT,
    created_at DATETIME
);
```

---

## 11. 版本演进

### 11.1 版本历史

| 版本 | 发布日期 | 主要特性 |
|------|----------|----------|
| v1.0.0 | - | 初始版本，核心代理功能 |
| v2.0.0 | - | 趋势图表、用户自助页面 |
| v2.4.0 | - | 对话内容追踪 |
| v2.5.0 | 2026-03-08 | 可靠性增强：水印追踪、请求重试、路由发现、健康检查配置化、请求体大小限制 |
| v2.6.0 | 2026-03-09 | 协议自动转换：Claude CLI ↔ Ollama/OpenAI，智能检测，流式支持，优雅降级 |
| v2.7.0 | 2026-03-09 | LLM Target 动态管理：配置文件 + 数据库双来源，CLI/API/WebUI 全方位管理，50+ 新测试 |
| v2.8.0 | 2026-03-11 | 协议转换进阶（图片/错误/前缀/prefill/thinking/绑定/model_mapping）+ 告警页面 + 批量导入 |

### 11.2 v2.8.0 版本亮点

| 特性 | 问题解决 | 技术方案 |
|------|----------|----------|
| 告警页面 | 管理员无法实时感知错误 | Dashboard SSE 推送 WARN/ERROR 日志 |
| 批量导入 | 手动逐个创建用户低效 | 模板文件 + CLI/WebUI，支持 dry-run 预览 |
| 图片内容块转换 | Claude CLI 发图片到 Ollama 失败 | Anthropic image → OpenAI image_url 自动转换 |
| OpenAI 错误响应转换 | Ollama 错误格式 Claude 客户端无法解析 | 自动转为 Anthropic error 格式 |
| assistant prefill 拒绝 | 非 Anthropic targets 不支持 prefill | 检测末尾 assistant 消息，返回 HTTP 400 |
| thinking 参数拒绝 | 非 Anthropic targets 不支持思考模式 | 检测 thinking 字段，返回 HTTP 400 |
| 强制 LLM 绑定 | 未绑定用户请求不明确路由 | 未绑定时返回 HTTP 403 明确错误 |
| model_mapping 配置 | Anthropic 模型名 Ollama 不认识 | 精确匹配 + 通配符 `*` 回退映射表 |
| GLM 风格 SSE 修复 | GLM-5 等模型 input_tokens 统计为 0 | message_start 为 0 时从 message_delta 回填 |

### 11.3 v2.6.0 版本亮点

| 特性 | 问题解决 | 技术方案 |
|------|----------|----------|
| 协议自动转换 | Claude CLI 无法直接连接 Ollama | Anthropic ↔ OpenAI 双向协议转换 |
| 智能检测 | 需要手动配置转换开关 | 基于请求路径 + provider 自动触发 |
| 流式支持 | SSE 流式响应转换复杂 | 实时 SSE 事件转换，零缓冲 |
| 优雅降级 | 转换失败影响服务 | 自动回退到原始请求 |
| 完整测试 | 协议转换质量保证 | 27 个测试用例，80.1% 覆盖率 |

### 11.4 v2.7.0 版本亮点

| 特性 | 问题解决 | 技术方案 |
|------|----------|----------|
| LLM Target 动态管理 | 配置文件修改需重启服务 | 配置文件 + 数据库双来源，支持运行时增删改查 |
| 配置文件同步 | 配置与数据库不一致 | 启动时自动同步，幂等操作，Source 字段标识来源 |
| CLI 命令支持 | 缺少命令行管理工具 | 5 个子命令：targets/add/update/delete/enable/disable |
| REST API 支持 | 缺少 API 集成能力 | 7 个端点，完整 CRUD 操作，JWT 认证 |
| WebUI 管理界面 | 缺少可视化管理 | Dashboard 集成，Tailwind CSS，实时健康状态 |
| URL 唯一性约束 | 可能配置重复 URL | 数据库 UNIQUE 约束，创建/更新时检查 |
| 配置来源只读 | 误删配置文件 targets | Source=config 的 targets 在 API/WebUI 中只读 |
| 完整测试覆盖 | 新功能质量保证 | 50+ 测试用例，覆盖所有层次（DB/CLI/API/E2E） |

### 11.5 v2.5.0 版本亮点

| 特性 | 问题解决 | 技术方案 |
|------|----------|----------|
| Worker 用量水印追踪 | Primary 宕机时数据丢失 | 本地 DB 持久化 + 自动补报 |
| 请求级重试 | 单次请求失败 | 自动重试到未尝试节点 |
| 路由表主动发现 | 路由更新延迟 | 定期轮询 + 304 缓存 |
| 健康检查配置化 | 固定参数不灵活 | 超时/阈值/恢复延迟可配置 |
| 健康检查配置化 | 固定参数不灵活 | 超时/阈值/恢复延迟可配置 |
| 请求体大小限制 | OOM 风险 | 10MB 限制 |

### 11.6 版本发布流程

```bash
# 1. 确保所有改动已合并到 main
git checkout main
git pull

# 2. 创建并推送 tag
git tag v2.5.0
git push origin v2.5.0

# 3. CI 自动执行
# - 交叉编译 5 个平台
# - 创建 GitHub Release
# - 构建并推送 Docker 镜像
```

---

## 12. 生产就绪评估

### 12.1 可用性等级

**当前等级**: Silver (~99.5%)

| 特性 | 状态 |
|------|------|
| 可承受 Worker 故障 | ✅ 自动恢复 |
| 可承受网络抖动 | ✅ 重试+熔断 |
| Primary 故障 | ⚠️ 用量数据不丢失，但管理功能不可用 |
| 适用规模 | 中小团队 (<50 人) |

### 12.2 故障容错分析结果

基于 `FAULT_TOLERANCE_ANALYSIS.md` 的 24 个故障场景分析：

| 风险等级 | 数量 | 占比 |
|----------|------|------|
| 🟢 低风险 | 13 | 54% |
| 🟡 中等风险 | 5 | 21% |
| 🔴 高风险 | 0 | 0% |

**关键风险已缓解**：
- ✅ 用量数据丢失风险已消除（水印追踪）
- ✅ 请求失败风险已降低（请求级重试）
- ✅ OOM 风险已消除（请求体大小限制）
- ✅ 节点故障恢复时间已优化（健康检查配置化）

### 12.3 改进建议优先级

#### P0 - 生产必需

1. **配置自动数据库备份**
   ```bash
   # crontab
   0 2 * * * /usr/local/bin/sproxy admin backup --output /backup/pairproxy_$(date +\%Y\%m\%d).db
   ```

2. **添加磁盘空间监控**
   - 磁盘使用率 > 80% 告警
   - 监控 `dropped` 指标

#### P1 - 短期优化

3. **反向代理层全局限流**
   ```nginx
   limit_req_zone $binary_remote_addr zone=global:10m rate=100r/s;
   ```

4. **缩短 JWT TTL**
   - 当前 24h → 建议 2h
   - 配合自动刷新机制

#### P2 - 中期规划

5. **Primary HA 方案**
   - VIP + Keepalived
   - 或 etcd/Consul 服务发现

---

## 附录

### A. Makefile 常用命令

```bash
make build          # 编译当前平台二进制
make test           # 运行全部测试
make test-race      # 竞态检测
make test-cover     # 生成覆盖率报告
make release        # 跨平台发布包
make lint           # 代码检查
make bcrypt-hash    # 生成密码 hash
make clean          # 清理构建产物
```

### B. 关键配置项

#### sproxy.yaml

```yaml
listen:
  host: "0.0.0.0"
  port: 9000

llm:
  lb_strategy: "round_robin"
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${ANTHROPIC_API_KEY}"
      weight: 1

database:
  path: "/var/lib/pairproxy/pairproxy.db"

auth:
  jwt_secret: "${JWT_SECRET}"
  access_token_ttl: 24h
  refresh_token_ttl: 168h

cluster:
  role: "primary"
  self_addr: "http://sp-1:9000"
  shared_secret: "${CLUSTER_SECRET}"
```

#### cproxy.yaml

```yaml
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://sp-1:9000"
  targets:
    - "http://sp-2:9000"
    - "http://sp-3:9000"
```

### C. 相关链接

- **GitHub 仓库**: https://github.com/l17728/pairproxy
- **Releases**: https://github.com/l17728/pairproxy/releases
- **问题反馈**: https://github.com/l17728/pairproxy/issues
- **安全漏洞报告**: https://github.com/l17728/pairproxy/security/advisories/new

---

**文档版本**: 1.3
**最后更新**: 2026-03-11
**适用版本**: PairProxy v2.8.0