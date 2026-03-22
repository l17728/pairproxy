# Contributing to PairProxy

感谢你有兴趣参与贡献！本文档说明如何搭建开发环境、提交代码和发布版本。

---

## 目录

- [开发环境](#开发环境)
- [工作流程](#工作流程)
- [代码规范](#代码规范)
- [测试要求](#测试要求)
- [提交 PR](#提交-pr)
- [Commit 规范](#commit-规范)
- [发布流程](#发布流程)（维护者）
- [报告 Bug](#报告-bug)

---

## 开发环境

**依赖：**

| 工具 | 版本 | 说明 |
|------|------|------|
| Go | 1.23+ | 与 `go.mod` 中 go 1.23 保持一致 |
| make | 任意 | 运行 Makefile 目标 |
| git | 2.x+ | 版本管理 |
| golangci-lint | latest | 本地运行 lint（可选，CI 中会自动检查） |

**安装 golangci-lint：**

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

**克隆并确认可以构建：**

```bash
git clone https://github.com/l17728/pairproxy.git
cd pairproxy
make build        # 输出 bin/cproxy bin/sproxy
make test         # 所有测试通过
```

---

## 工作流程

1. **Fork** 仓库，并基于 `main` 创建特性分支：

   ```bash
   git checkout -b feat/your-feature
   ```

2. **修改代码**，同时编写或更新对应测试。

3. **本地验证**，确保全部检查通过后再推送：

   ```bash
   make fmt          # 格式化
   make vet          # go vet
   make lint         # golangci-lint
   make test-race    # 竞态检测
   ```

4. **推送**到你的 Fork 并在 GitHub 上**创建 Pull Request**。

---

## 代码规范

### 格式

- 统一使用 `gofmt` / `goimports` 格式化，提交前运行 `make fmt`。
- 行宽无硬性限制，但尽量保持可读性（通常不超过 120 字符）。

### 命名

- 遵循 [Effective Go](https://go.dev/doc/effective_go) 和 [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments) 中的命名惯例。
- 包名简短、小写、无下划线。

### 日志

项目使用 `go.uber.org/zap` 结构化日志，遵循以下级别规范：

| 级别 | 场景 |
|------|------|
| `DEBUG` | 每条记录级别的详情（token 计数、SSE 解析），生产环境不开启 |
| `INFO` | 服务生命周期事件（启动、关闭、token 加载） |
| `WARN` | 可恢复的异常（DB 写入失败降级、健康检查失败） |
| `ERROR` | 不可恢复的错误，需要人工介入 |

### 错误处理

- 错误向上传播时用 `fmt.Errorf("context: %w", err)` 包装，保留调用栈语义。
- 不要忽略 `error` 返回值；若确实无需处理，添加 `//nolint:errcheck` 并注明原因。
- 对外暴露的函数（HTTP handler、公共 API）必须处理所有错误路径。

### 注释

- 导出符号（函数、类型、常量）必须有 godoc 注释。
- 复杂逻辑块可用中文或英文注释，保持与现有代码风格一致（项目中文注释较多）。

---

## 测试要求

- 新功能必须附带测试；Bug 修复建议附带能复现问题的测试。
- 测试文件命名：`xxx_test.go`，与被测文件同包（或 `_test` 包做黑盒测试均可）。
- 使用标准库 `testing` 包，不引入额外的断言框架。
- 涉及并发的代码，测试应能通过 `go test -race`。
- 禁止在测试中引入真实网络请求或文件系统副作用；使用 `httptest.NewServer` 和临时目录。

运行方式：

```bash
make test              # 所有包
make test-race         # race detector
make test-cover        # 生成 coverage.html
make test-pkg PKG=./internal/quota/...  # 单包
```

### 新模块测试覆盖要求

以下模块需要特别注意测试覆盖（v2.13.0+ 引入）：

**PostgreSQL 相关测试**：
- 需要通过 `build tag` 区分 SQLite / PG 测试
- PG 集成测试需要真实 PG 实例（使用测试容器或 CI 中的 PG service）
  ```bash
  # 运行 PG 集成测试（需设置 TEST_PG_DSN）
  TEST_PG_DSN="postgres://..." go test -tags=pg ./internal/db/...
  ```

**语义路由测试**（v2.18.0+）：
- 单元测试：规则优先级、降级策略、递归防止
- 集成测试：分类器超时处理、规则 DB vs YAML 优先级
  ```bash
  go test ./internal/semantic_router/...
  ```

**训练语料测试**（v2.16.0+）：
- 单元测试：JSONL 格式、文件轮转、质量过滤（最小 token 数、排除分组）
  ```bash
  go test ./internal/corpus/...
  ```

---

## 提交 PR

- PR 标题简洁说明改动意图，格式与 [Commit 规范](#commit-规范) 一致。
- PR 描述中说明：**做了什么**、**为什么这样做**、**如何测试**。
- 保持每个 PR 聚焦单一目的；大型功能请先开 Issue 讨论方案。
- PR 合并前 CI（build + vet + race test + lint）必须全绿。
- 维护者会进行 Code Review，请耐心等待并及时响应反馈。

---

## Commit 规范

采用 [Conventional Commits](https://www.conventionalcommits.org/) 风格：

```
<type>(<scope>): <subject>

[body]

[footer]
```

**type 列表：**

| type | 含义 |
|------|------|
| `feat` | 新功能 |
| `fix` | Bug 修复 |
| `perf` | 性能优化 |
| `refactor` | 重构（不改变行为） |
| `test` | 添加或修改测试 |
| `docs` | 文档 |
| `ci` | CI/CD 配置 |
| `chore` | 构建脚本、依赖更新等杂项 |
| `deploy` | 部署相关文件（Dockerfile、systemd 等） |

**示例：**

```
feat(quota): add per-user RPM rate limiting

Implement a sliding window rate limiter that tracks per-user request
timestamps within a 1-minute window. Integrated into QuotaChecker and
exposed via SetRPM() on GroupRepo.

Closes #42
```

- subject 使用英文，首字母小写，不加句号。
- body 和 footer 可选，用于说明 breaking changes 或关联 Issue。

---

## 发布流程

> 仅维护者（拥有 push 权限的贡献者）需要关注本节。

所有发布工作由 CI 自动完成，维护者只需推送一个符合语义版本的 tag：

```bash
# 确认 main 分支处于期望状态
git checkout main && git pull

# 打 tag（遵循 semver：major.minor.patch）
git tag v1.2.3

# 推送 tag，触发 release.yml
git push origin v1.2.3
```

**CI 自动执行：**

1. 交叉编译 5 个平台（Linux/macOS/Windows × amd64/arm64）
2. 生成 `SHA256SUMS.txt` 校验文件
3. 在 GitHub 创建 Release，附上所有产物和自动生成的 release notes
4. 构建多架构 Docker 镜像并推送到 `ghcr.io/l17728/pairproxy`，标签：`v1.2.3`、`1.2`、`1`、`latest`

**版本号约定（semver）：**

| 变更类型 | 版本号 |
|----------|--------|
| 不兼容的 API / 配置文件改动 | major（1.x.x → 2.0.0） |
| 向后兼容的新功能 | minor（1.2.x → 1.3.0） |
| Bug 修复、性能优化 | patch（1.2.3 → 1.2.4） |

---

## 报告 Bug

请在 [GitHub Issues](https://github.com/l17728/pairproxy/issues) 中提交，包含以下信息：

- **PairProxy 版本**：`sproxy version` / `cproxy version` 输出
- **操作系统和架构**：如 `linux/amd64`
- **复现步骤**：最小化的能稳定复现的操作序列
- **期望行为** vs **实际行为**
- **相关日志**：`journalctl -u sproxy` 或启动时的控制台输出（注意脱敏 API Key / JWT）

**安全漏洞** 请勿在公开 Issue 中披露，发送邮件至 [l17728@126.com](mailto:l17728@126.com)。

---

## 架构演进速查（快速了解重要模块）

面向新贡献者的模块导读，帮助快速找到代码位置：

| 功能 | 版本 | 包路径 | 关键文件 |
|------|------|--------|---------|
| JWT 认证 | v1.0+ | `internal/auth/` | `manager.go`, `store.go` |
| 配额限流 | v1.0+ | `internal/quota/` | `checker.go`, `rate_limiter.go` |
| 负载均衡 | v2.0+ | `internal/lb/` | `balancer.go`, `health.go` |
| 协议转换（双向） | v2.6.0+ | `internal/proxy/` | `converter.go`, `ota_converter.go` |
| LLM Target 动态管理 | v2.7.0+ | `internal/db/`, `internal/api/` | `llm_target_repo.go` |
| Direct Proxy (sk-pp-) | v2.9.0+ | `internal/keygen/` | `keygen.go`, `validator.go` |
| PostgreSQL 支持 | v2.13.0+ | `internal/db/` | `db.go`（driver 分支） |
| Peer Mode | v2.14.0+ | `internal/cluster/` | `pg_peer_registry.go` |
| HMAC Keygen | v2.15.0+ | `internal/keygen/` | `hmac_keygen.go` |
| 训练语料采集 | v2.16.0+ | `internal/corpus/` | `writer.go`, `filter.go` |
| 语义路由 | v2.18.0+ | `internal/semantic_router/` | `router.go`, `classifier.go`, `rule_repo.go` |

## 新功能开发清单

开发新功能时，除代码外还需完成：

- [ ] **单元测试**（覆盖率不低于现有水平）
- [ ] **集成测试**（涉及数据库/HTTP 的功能）
- [ ] **API 文档**（新 REST 端点更新 `docs/API.md`）
- [ ] **用户手册**（面向用户的功能更新 `docs/manual.md`）
- [ ] **升级指南**（有 DB Schema 变更或配置变更时更新 `docs/UPGRADE.md`）
- [ ] **CLAUDE.md 命令速查**（新增 CLI 命令时更新 `CLAUDE.md`）
- [ ] **config/README.md**（新增配置项时更新配置文档）
