# PairProxy Changelog

## [v2.24.1] - 2026-04-05

### 🔧 CI/CD
- **Reportgen 发布自动化**: GitHub Release 流水线新增 reportgen 工具的交叉编译步骤，推送 `v*` tag 后自动构建并发布以下平台的预编译二进制：
  - `reportgen-<version>-linux-amd64.tar.gz`
  - `reportgen-<version>-linux-arm64.tar.gz`
  - `reportgen-<version>-darwin-amd64.tar.gz`
  - `reportgen-<version>-darwin-arm64.tar.gz`
  - `reportgen-<version>-windows-amd64.zip`
  - `reportgen-<version>-windows-arm64.zip`
- 所有产物纳入统一 `SHA256SUMS.txt` 校验文件

### 📚 Documentation
- 更新 CHANGELOG、Release Notes、测试报告、验收报告、API 手册、reportgen 使用手册至 v2.24.1

---

## [v2.24.0] - 2026-04-04

### ✨ New Features

#### Model-Aware Routing (F1/F2/F3)

**F1 — Config-as-Seed（配置即种子）**
- 配置文件中的 LLM target 在首次启动时自动写入数据库（种子化）
- 数据库中已存在的记录（WebUI 修改过的）不会被覆盖
- `LLMTargetRepo` 新增 `Seed()` 方法，带存在性检查，防止重复写入

**F2 — Per-Target Supported Models（目标级模型声明）**
- 每个 LLM target 可通过 `supported_models` 字段声明支持的模型列表
- 支持精确匹配、前缀通配（`claude-3-*`）、全通配（`*`）三种模式
- 空列表 = 接受所有模型（向后兼容）
- 新增 `auto_model` 字段：当请求模型不在支持列表时自动替换为指定模型
- 降级策略：`auto_model` > `supported_models[0]` > 透传（空字符串）
- 两级 Fail-Open 策略：
  - Level 1：按 Provider 过滤，若结果为空则使用所有健康 target
  - Level 2：按模型过滤，若结果为空则使用所有健康 target（Fail-Open）

**F3 — REST API & CLI 支持**
- `POST /api/admin/llm/targets` 和 `PUT /api/admin/llm/targets/{url}` 新增 `supported_models`、`auto_model` 字段
- CLI `sproxy admin llm target add/update` 新增 `--supported-models`、`--auto-model` 参数

---

## [v2.23.0] - 2026-04-04

### 🐛 Bug Fixes

#### Issue #2: API Key 号池共享修复
- **问题**：同一 provider（如 `openai`）只能存储一个 API Key，导致多 Key 号池共享失效
- **根因**：`resolveAPIKeyID` 仅按 `provider` 字段唯一化，同类型多 Key 互相覆盖
- **修复**：UNIQUE 约束从 `(provider)` 改为 `(provider, encrypted_value)`，支持同 provider 多 Key 共存
- **影响**：百炼、火山引擎等 OpenAI 兼容 provider 现在可配置多个不同 API Key，实现真正的号池共享
- **向后兼容**：现有 DB 记录自动过渡，无需手动迁移

#### Issue #3: `admin.key_encryption_key` 文档与行为一致性修复
- **问题**：文档将 `admin.key_encryption_key` 标记为可选，但使用 `admin apikey` 命令时实为必填
- **修复**：UPGRADE.md 和 config/README.md 明确标注「使用 API Key 管理功能时必填」
- **改进**：错误消息更具体，提示用户配置该字段

#### Issue #4: 健康检查支持大厂 API 认证
- **问题**：Anthropic、OpenAI 等没有 `/health` 端点的 LLM 提供商，健康检查因缺少认证头而失败（401）
- **解决方案**：实现 provider 感知的认证注入，使用推理 API 替代 `/health` 端点
- **支持的 Provider**：
  - Anthropic Claude：注入 `x-api-key` + `anthropic-version` 头
  - OpenAI / Codex：注入 `Authorization: Bearer` 头
  - 阿里云百炼（DashScope）：Bearer token
  - 火山引擎（Ark）：Bearer token
  - 华为云 MaaS：框架就绪，后续支持 AKSK 签名
  - vLLM / sglang：向后兼容，无需认证
- **新增**：`TargetCredential` 结构体、`WithCredentials` 选项、`injectAuth()` 方法
- **可观测性**：DEBUG 日志追踪认证注入，INFO 日志追踪 credential 更新

### 🔧 Data Race Fix
- **问题**：`HealthChecker` 测试在 CI 中间歇性出现 data race（Go `-race` 检测）
- **根因**：`WaitGroup` 未追踪主循环 goroutine 本身（`loop()`），仅追踪子任务
- **修复**：在 `Start()` 中添加 `wg.Add(1)`，在 `loop()` 开头添加 `defer wg.Done()`
- **教训**：WaitGroup 必须追踪所有长生命周期 goroutine（主循环 + 子任务），详见 `docs/GO_CONCURRENCY_TEACHING_MATERIAL.md`

### 📚 Documentation
- 新增 `docs/GO_CONCURRENCY_TEACHING_MATERIAL.md`：Go 并发编程教材（含 Mermaid 流程图、WaitGroup 模式、GitHub 工作流）
- 新增 `docs/CONCURRENCY_GUIDELINES.md`：并发编程规范与检查清单
- 更新 `CLAUDE.md`：新增并发测试必须遵守的规范章节

---

## [v2.22.0] - 2026-03-28

### ✨ New Features

#### WebUI Expansion - Phase 1: Group-Target Set Management
- **Target Set Management UI**: Create, update, delete Group-Target Sets with full member management
  - Dual-panel layout for viewing target set list and details
  - Add/remove/update members with inline weight editing
  - Automatic group binding and strategy configuration
  - Full audit logging of all target set operations
  - Member permissions validation (read-only for Worker nodes)

#### WebUI Expansion - Phase 2: Alert Management Enhancement
- **Alert Management Dashboard** with 3 tabs:
  - **Live Tab**: Real-time event streaming with level filtering (error/warn/all)
  - **Active Tab**: Active alerts with batch resolution capability
    - Severity statistics cards (Critical/Error/Warning)
    - Single and batch alert resolution with audit tracking
  - **History Tab**: 90-day alert history with advanced filtering
    - Time range selection (7/30/90 days)
    - Level and source filtering
    - Pagination support (50 items per page)

#### WebUI Expansion - Phase 3: Quick Operations Panel
- **Dashboard Quick Operations Section** on overview page
  - LLM Target Status card: health count, active alerts, target set count
  - System Alerts card: unresolved alert statistics and severity distribution
  - Users/Groups card: active user count, total groups, new users today
  - Async data loading from existing APIs (non-blocking)
  - Quick navigation links to management pages

### 🐛 Bug Fixes

- **Critical**: Fixed template scope issue in target set details panel where `$.SelectedSetID` was incorrectly accessed
- **Critical**: Fixed member delete/update routes to use POST form fields instead of URL path segments (prevents 404 errors)
- **Critical**: Fixed unencoded error messages in redirect URLs that caused malformed HTTP Location headers
- **Important**: Fixed batch alert resolve flash message containing literal space character
- **Important**: Fixed edit target set modal that didn't populate current values
- **Important**: Added ID format validation for target sets (alphanumeric, dash, underscore only)
- **Important**: Fixed redundant time import and custom itoa function

### 🔧 Technical Improvements

- **Code Quality**: All handler implementations follow existing project patterns
  - Middleware chain composition (requireSession + requireWritableNode)
  - Flash message pattern via URL query parameters
  - Audit logging via auditRepo.Create()
  - GORM repository pattern for data access

- **Template Improvements**:
  - Tab-based navigation using URL query parameters (?tab=targetsets)
  - Modal dialog patterns with hidden CSS class toggling
  - Responsive Tailwind CSS grid layouts
  - Named templates for organizing Tab content

- **Data Integrity**:
  - Proper null pointer handling for optional GroupID field
  - N+1 query prevention through batch member loading
  - Cascading delete for target set members
  - Type-safe form field conversions

### 📚 Documentation Updates

- Updated API.md with new dashboard endpoints:
  - `POST /dashboard/llm/targetsets` - Create target set
  - `POST /dashboard/llm/targetsets/{id}/update` - Update target set
  - `POST /dashboard/llm/targetsets/{id}/delete` - Delete target set
  - `POST /dashboard/llm/targetsets/{id}/members` - Add member
  - `POST /dashboard/llm/targetsets/{id}/members/update` - Update member
  - `POST /dashboard/llm/targetsets/{id}/members/delete` - Remove member
  - `POST /dashboard/alerts/resolve` - Resolve single alert
  - `POST /dashboard/alerts/resolve-batch` - Resolve multiple alerts

- Updated manual.md with new UI workflows:
  - Target Set Management workflow
  - Alert Management workflow (live/active/history tabs)
  - Quick Operations panel usage guide

### ✅ Backward Compatibility

- **No Breaking Changes**: All existing APIs and functionality remain unchanged
- **Worker Node Support**: Read-only mode properly enforced for new features
- **Database Schema**: No migrations required; GroupTargetSet and related tables already exist in v2.19+

### 📋 Testing

- All implementations follow existing test patterns:
  - Table-driven test structure
  - In-memory SQLite for integration tests
  - httptest for HTTP handler testing
  - Testify assertions and require patterns
  - Full audit logging verification

### 🎯 Known Limitations

- Alert resolution handlers currently log the action but don't modify in-memory event state (future enhancement)
- LLM target health status requires separate API integration (placeholder in quick ops panel)
- Quick operations panel uses cached data (5-minute TTL for user stats)

### 🚀 Deployment Notes

- No database migrations required
- No configuration changes needed
- Fully backward compatible with v2.19.x deployments
- All new features are optional (repo dependencies check for nil before use)
- Worker nodes automatically enforced read-only mode for new features

---

## [v2.19.0] - 2026-03-15

(See previous releases for v2.19 and earlier changes)
