# PairProxy v2.24.1 Release Notes

**Release Date:** April 5, 2026
**Git Tag:** `v2.24.1`
**Base Version:** v2.24.0

---

## 概述

v2.24.1 是一个工具链补丁版本，将 v2.24.0 中已完成的 **Model-Aware Routing** 功能与 **reportgen 报告生成工具**纳入统一的 GitHub Release 发布流程，用户现在可以直接下载预编译的 reportgen 二进制，无需自行编译。

**本版本包含内容：**
- v2.24.0 的全部功能（Model-Aware Routing F1/F2/F3）
- reportgen 工具 6 个平台的预编译二进制（新增）
- CI/CD 流水线更新
- 文档全面更新至 v2.24.1

---

## v2.24.0 核心功能：Model-Aware Routing

### 背景

在多 Provider、多账号的企业环境中，不同的 LLM target 通常只支持特定的模型。在此功能之前，网关无法感知模型维度，可能将请求路由到不支持该模型的 target，导致上游返回错误。

### F1 — Config-as-Seed（配置文件种子化）

- 首次启动时，配置文件中的 LLM target 自动写入数据库
- 已通过 WebUI/API 修改的数据库记录不被覆盖（DB 优先）
- 支持动态扩容：新增配置项重启后自动入库，无需手动执行 SQL

### F2 — Per-Target Supported Models（模型声明）

每个 LLM target 现在支持以下新字段：

| 字段 | 类型 | 说明 |
|------|------|------|
| `supported_models` | `[]string` | 该 target 支持的模型列表，支持通配符；空 = 接受所有模型 |
| `auto_model` | `string` | 当请求模型不在列表中时自动替换的模型名 |

**通配符语法：**
```yaml
supported_models:
  - "claude-3-5-sonnet-20241022"   # 精确匹配
  - "claude-3-*"                   # 前缀通配
  - "*"                            # 全通配（等同于空列表）
```

**模型替换降级策略（优先级由高到低）：**
1. `auto_model` 字段值
2. `supported_models[0]`（第一个支持的模型）
3. 透传（不替换，由上游决定）

**Fail-Open 双重保障：**
- 按 Provider 过滤后若结果为空 → 使用所有健康 target
- 按 Model 过滤后若结果为空 → 使用所有健康 target（不阻塞请求）

### F3 — API & CLI 支持

**REST API 变更（`POST /api/admin/llm/targets`）：**
```json
{
  "url": "https://api.anthropic.com/v1/messages",
  "provider": "anthropic",
  "supported_models": ["claude-3-5-sonnet-20241022", "claude-3-*"],
  "auto_model": "claude-3-5-sonnet-20241022"
}
```

**CLI 变更：**
```bash
sproxy admin llm target add \
  --url https://api.anthropic.com/v1/messages \
  --provider anthropic \
  --supported-models "claude-3-5-sonnet-20241022,claude-3-*" \
  --auto-model "claude-3-5-sonnet-20241022"
```

---

## v2.24.1 新增：Reportgen 预编译发布

### 下载

从本 Release 直接下载对应平台的预编译二进制，无需安装 Go 环境：

| 平台 | 文件名 |
|------|--------|
| Linux x86_64 | `reportgen-v2.24.1-linux-amd64.tar.gz` |
| Linux ARM64 | `reportgen-v2.24.1-linux-arm64.tar.gz` |
| macOS x86_64 | `reportgen-v2.24.1-darwin-amd64.tar.gz` |
| macOS ARM64 (Apple Silicon) | `reportgen-v2.24.1-darwin-arm64.tar.gz` |
| Windows x86_64 | `reportgen-v2.24.1-windows-amd64.zip` |
| Windows ARM64 | `reportgen-v2.24.1-windows-arm64.zip` |

### 快速使用

```bash
# Linux/macOS
tar -xzf reportgen-v2.24.1-linux-amd64.tar.gz
./reportgen -db /path/to/pairproxy.db -from 2026-04-01 -to 2026-04-05

# Windows
# 解压 zip 后直接运行
reportgen.exe -db C:\pairproxy\pairproxy.db -from 2026-04-01 -to 2026-04-05
```

### 校验完整性

```bash
sha256sum -c SHA256SUMS.txt
```

---

## Docker 镜像

Docker 镜像发布在 GitHub Container Registry（GHCR），不在 Release Assets 中：

```bash
docker pull ghcr.io/l17728/pairproxy:v2.24.1
docker pull ghcr.io/l17728/pairproxy:latest
```

---

## 升级指南

### 从 v2.23.x 升级

1. 停止现有服务
2. 替换 `sproxy` / `cproxy` 二进制
3. 重启服务（DB schema 无变更，无需迁移）
4. 按需为现有 LLM target 配置 `supported_models`（可选，空列表保持原有行为）

### 从 v2.24.0 升级

直接替换二进制即可，无任何配置变更。

---

## 文件清单

| 文件 | 说明 |
|------|------|
| `pairproxy-v2.24.1-linux-amd64.tar.gz` | sproxy + cproxy，Linux x86_64 |
| `pairproxy-v2.24.1-linux-arm64.tar.gz` | sproxy + cproxy，Linux ARM64 |
| `pairproxy-v2.24.1-darwin-amd64.tar.gz` | sproxy + cproxy，macOS x86_64 |
| `pairproxy-v2.24.1-darwin-arm64.tar.gz` | sproxy + cproxy，macOS ARM64 |
| `pairproxy-v2.24.1-windows-amd64.zip` | sproxy + cproxy，Windows x86_64 |
| `pairproxy-v2.24.1-windows-arm64.zip` | sproxy + cproxy，Windows ARM64 |
| `reportgen-v2.24.1-linux-amd64.tar.gz` | reportgen 报告生成工具，Linux x86_64 |
| `reportgen-v2.24.1-linux-arm64.tar.gz` | reportgen 报告生成工具，Linux ARM64 |
| `reportgen-v2.24.1-darwin-amd64.tar.gz` | reportgen 报告生成工具，macOS x86_64 |
| `reportgen-v2.24.1-darwin-arm64.tar.gz` | reportgen 报告生成工具，macOS ARM64 |
| `reportgen-v2.24.1-windows-amd64.zip` | reportgen 报告生成工具，Windows x86_64 |
| `reportgen-v2.24.1-windows-arm64.zip` | reportgen 报告生成工具，Windows ARM64 |
| `SHA256SUMS.txt` | 所有文件的 SHA256 校验值 |
