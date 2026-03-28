#!/bin/bash
# PairProxy v2.20 WebUI 扩展 - 开发快速启动检查清单

## 📋 项目启动前检查（复制粘贴逐一执行）

### Step 1: 验证代码库状态
- [ ] `git status` 工作区干净
- [ ] `git log --oneline -5` 确认在 main 分支
- [ ] `go mod tidy` 依赖更新

### Step 2: 准备开发分支
- [ ] `git checkout -b feat/ui-expand-v2.20` 创建分支
- [ ] `git push -u origin feat/ui-expand-v2.20` 推送到远程

### Step 3: 理解现有代码（必读）
- [ ] 阅读 `internal/dashboard/handler.go` (Handler struct + RegisterRoutes)
- [ ] 阅读 `internal/dashboard/llm_handler.go` (handleLLMPage 逻辑)
- [ ] 阅读 `internal/dashboard/templates/llm.html` (现有模态框 + 表单)
- [ ] 阅读 `internal/dashboard/templates/layout.html` (base 布局 + Flash 消息)
- [ ] 快速 scan `internal/db/group_target_set_repo.go` (GroupTargetSet model)

### Step 4: 审阅设计文档
- [ ] 👉 **必读**: `D:\pairproxy\COMPATIBLE_IMPLEMENTATION_PLAN.md`
      (包含 Phase 1/2/3 完整代码框架)
- [ ] 参考: `UI_PROTOTYPES_AND_FLOWS.md` (交互细节)
- [ ] 快速看: `FINAL_SUMMARY.md` (方案概览)

### Step 5: 准备开发环境
```bash
# 编译检查
go build -v ./...

# 测试运行（确保基线通过）
go test -count=1 -v ./internal/dashboard/...

# 启动开发服务器（可选，用于手工测试）
./sproxy --config sproxy.yaml.example
```

- [ ] 编译成功
- [ ] 基线测试通过
- [ ] 本地 sproxy 能启动

---

## 📝 Phase 1: Group-Target Set 管理 (Day 1-4)

### Code 修改清单

#### handler.go
- [ ] 新增字段 `groupTargetSetRepo *db.GroupTargetSetRepo`
- [ ] 新增 Setter `SetGroupTargetSetRepo(repo)`
- [ ] RegisterRoutes 中追加 6 条路由:
  - `POST /dashboard/llm/targetsets`
  - `POST /dashboard/llm/targetsets/{id}/update`
  - `POST /dashboard/llm/targetsets/{id}/delete`
  - `POST /dashboard/llm/targetsets/{id}/members`
  - `POST /dashboard/llm/targetsets/{id}/members/{memberID}/update`
  - `POST /dashboard/llm/targetsets/{id}/members/{memberID}/delete`
- [ ] 实现 6 个 Handler 函数

#### llm_handler.go
- [ ] 新增 `targetSetWithMembers` struct
- [ ] 扩展 `llmPageData` struct（新增 ActiveTab, TargetSets 等字段）
- [ ] 修改 `handleLLMPage()` 支持 tab 参数路由逻辑

#### llm.html
- [ ] 现有内容搬移到 `{{define "llm-targets-tab"}} ... {{end}}`
- [ ] 现有绑定内容搬移到 `{{define "llm-bindings-tab"}} ... {{end}}`
- [ ] 新增 Tab 栏（3 个链接）
- [ ] 新增 `{{define "llm-targetsets-tab"}}` （双栏布局）
- [ ] 新增 targetSetModal 模态框
- [ ] 新增 JavaScript 交互函数（toggleAddMember 等）

#### group_target_set_repo.go
- [ ] 新增 `ListMembersForSets()` 方法（批量查询）

### 测试验证
- [ ] 编译: `go build -v ./...`
- [ ] 单元测试: `go test -count=1 ./internal/dashboard/...`
- [ ] 手工测试:
  - [ ] 打开 /dashboard/llm?tab=targetsets
  - [ ] 创建新目标集
  - [ ] 添加成员到目标集
  - [ ] 编辑成员权重
  - [ ] 删除目标集

### Commit
```bash
git add internal/dashboard/ internal/db/group_target_set_repo.go
git commit -m "feat(ui): phase 1 - group target set management

- Add GroupTargetSetRepo field and routes to dashboard handler
- Extend llmPageData with targetset management data
- Implement targetsets Tab with dual-panel layout
- Add modal for creating/editing target sets
- Add inline edit for member weight and priority
- Optimize N+1 with ListMembersForSets() batch query
- Support Worker read-only mode

Implements: v2.20 Group-Target Set UI (P0 priority)"
```

---

## ⚠️ Phase 2: Alert 管理升级 (Day 2-3，可并行)

### Code 修改清单

#### handler.go
- [ ] 新增字段 `alertRepo *db.TargetAlertRepo`
- [ ] 新增 Setter `SetAlertRepo(repo)`
- [ ] RegisterRoutes 中追加 2 条路由:
  - `POST /dashboard/alerts/{id}/resolve`
  - `POST /dashboard/alerts/resolve-batch`
- [ ] 修改 `handleAlertsPage()` 支持 tab 参数
- [ ] 实现 `handleAlertResolve()` 和 `handleAlertResolveBatch()`

#### alerts.html
- [ ] 现有内容搬移到 `{{define "alerts-live-tab"}} ... {{end}}`
- [ ] 新增 Tab 栏（3 个链接：live、active、history）
- [ ] 新增 `{{define "alerts-active-tab"}}` （活跃告警表 + 批量解决）
- [ ] 新增 `{{define "alerts-history-tab"}}` （历史查询 + 分页）
- [ ] 新增 JavaScript 交互（checkbox、resolve、batch 等）

### 测试验证
- [ ] 编译: `go build -v ./...`
- [ ] 单元测试: `go test -count=1 ./internal/dashboard/...`
- [ ] 手工测试:
  - [ ] 打开 /dashboard/alerts?tab=active
  - [ ] 选择告警并批量解决
  - [ ] 打开 /dashboard/alerts?tab=history
  - [ ] 查询不同时间范围的告警
  - [ ] 分页翻页

### Commit
```bash
git add internal/dashboard/
git commit -m "feat(ui): phase 2 - alert management upgrade

- Extend alerts page with active and history tabs
- Implement active alerts view with batch resolve
- Implement history alerts with date range and pagination
- Add checkbox selection and batch operation UI
- Add resolve confirmation modal
- Integrate with TargetAlertRepo for DB persistence

Implements: v2.20 Alert Management UI (P1 priority)"
```

---

## 🚀 Phase 3: 快速操作面板 (Day 4)

### Code 修改清单

#### overview.html
- [ ] 在趋势图下方新增 `<section>` 快速操作面板
- [ ] 3 个卡片：LLM 状态、告警、用户/分组
- [ ] 异步 fetch 加载数据的 JavaScript
- [ ] 卡片链接导航

### 测试验证
- [ ] 编译: `go build -v ./...`
- [ ] 手工测试:
  - [ ] 打开 /dashboard/overview
  - [ ] 等待卡片数据加载（应看到异步加载的数据）
  - [ ] 点击卡片中的快捷链接

### Commit
```bash
git add internal/dashboard/templates/overview.html
git commit -m "feat(ui): phase 3 - quick ops panel on overview page

- Add LLM targets status card with health count and alerts
- Add system alerts summary card with severity breakdown
- Add users/groups statistics card
- Implement async data loading via fetch API
- Add quick navigation links to management pages

Enhances: v2.20 Dashboard UX (P2 priority)"
```

---

## ✅ Phase 4: 测试 + 文档 (Day 5)

### 功能测试 (按照 COMPATIBLE_IMPLEMENTATION_PLAN.md 的清单)
- [ ] 创建目标集
- [ ] 编辑目标集
- [ ] 删除目标集（验证级联删除成员）
- [ ] 添加成员
- [ ] 修改成员权重/优先级
- [ ] 删除成员
- [ ] 批量解决告警
- [ ] 历史查询分页
- [ ] 快速面板卡片加载

### 兼容性测试
- [ ] Tab 切换工作正常
- [ ] URL 书签保持状态 (?tab=targetsets&selected=id)
- [ ] Flash 消息显示正常
- [ ] Worker 节点按钮禁用
- [ ] 响应式设计（移动设备）

### 浏览器测试
- [ ] Chrome/Edge (Windows)
- [ ] Firefox (Windows)
- [ ] Safari (如有 Mac)

### 性能检查
- [ ] 页面加载 < 1s
- [ ] 表格操作响应 < 200ms
- [ ] 无明显 N+1 SQL 查询 (使用 `EXPLAIN` 检查)

### 文档更新
- [ ] 更新 `docs/manual.md` 版本号 + 新增章节说明 WebUI 新功能
- [ ] 更新 `docs/API.md` 新增的 API 端点
- [ ] 更新 `docs/CHANGELOG.md` v2.20.0 条目
- [ ] 准备发布公告内容

### Final Commit
```bash
git add docs/
git commit -m "docs: update documentation for v2.20 UI expansion

- Update manual.md with Group-Target Set UI section
- Update API.md with new dashboard endpoints
- Add CHANGELOG entry for v2.20.0 features
- Add screenshots/demo descriptions (optional)"
```

---

## 🔄 Code Review 清单

### 代码质量
- [ ] 无 Go lint 错误: `golangci-lint run ./internal/dashboard/`
- [ ] 无 unused imports/variables
- [ ] 错误处理完整（errcheck）
- [ ] HTTP response body 都关闭了（bodyclose）

### 功能完整性
- [ ] CRUD 操作全覆盖
- [ ] 错误消息清晰
- [ ] Flash 消息正确
- [ ] 业务逻辑正确（如级联删除）

### UI/UX
- [ ] 按钮状态清晰（hover/active/disabled）
- [ ] 表单验证前端 + 后端都有
- [ ] 确认框出现在危险操作前（删除等）
- [ ] Worker 节点只读保护正确

### 性能
- [ ] 无 N+1 SQL 查询
- [ ] 表格分页合理（避免一次加载过多）
- [ ] 缓存机制正确（如有）

### 安全
- [ ] SQL injection 防护（使用参数化查询）
- [ ] XSS 防护（HTML escape）
- [ ] CSRF token（如需）
- [ ] 权限检查（只读 vs 写入）

---

## 📊 发布前最终检查

```bash
# 1. 完整编译和测试
go build -v ./...
go test -count=1 -v ./...

# 2. Lint 检查
golangci-lint run ./internal/dashboard/...

# 3. 集成测试（如有）
go test -count=1 -race ./...

# 4. 手工烟雾测试（Smoke Test）
# 启动 sproxy，手工点击所有新增功能

# 5. 确认分支是最新状态
git fetch origin main
git rebase origin/main
git push origin feat/ui-expand-v2.20 --force-if-needed

# 6. 检查 PR 是否可以合并
# 在 GitHub 上创建 PR，确认无冲突、CI 通过
```

- [ ] 编译通过
- [ ] 测试通过
- [ ] Lint 通过
- [ ] 手工烟雾测试通过
- [ ] PR 无冲突且 CI 绿色
- [ ] 文档更新完整
- [ ] Code Review 通过

---

## 🎉 发布流程

### 合并到 main
```bash
# 在 GitHub 上合并 PR（确保 Squash or Rebase）
# 或本地合并：
git checkout main
git pull origin main
git merge feat/ui-expand-v2.20
git push origin main
```

- [ ] PR 已合并到 main
- [ ] CI 在 main 上通过

### 打 Tag
```bash
# v2.20.0 应包含所有新功能
git tag -a v2.20.0 -m "v2.20.0 - WebUI expansion for Group-Target Set and Alert management"
git push origin v2.20.0
```

- [ ] Tag 已推送
- [ ] Release workflow 触发（自动编译 5 平台二进制）

### 发布公告
- [ ] GitHub Release notes 已补写（gh release edit）
- [ ] 项目主页 README 已更新（版本号）
- [ ] 内部 Slack/钉钉 已通知

---

## 📞 遇到问题？

### 常见问题排查

**Q: 编译失败 - undefined Handler.groupTargetSetRepo**
A: 确认已在 handler.go 中添加了字段声明和 Setter 函数

**Q: 模板错误 - template: layout: executeTemplate of templates parsing...**
A: 检查 named template 定义是否正确 ({{define "llm-targetsets-tab"}} ... {{end}})

**Q: 数据为空 - TargetSets 列表为空**
A: 确认 groupTargetSetRepo 在初始化时被正确 Set 到 handler

**Q: Flash 消息不显示**
A: 检查重定向 URL 中是否有 query param: `?flash=成功消息`

**Q: JavaScript 不执行**
A: 检查浏览器控制台 console 是否有 JS 错误；确保 function 名称正确

### 调试技巧

```bash
# 查看数据库内容
sqlite3 pairproxy.db "SELECT * FROM group_target_sets;"

# 查看 SQL 日志（GORM）
# 在代码中启用 DB.Debug()

# 查看 HTTP 请求（浏览器开发工具 Network 标签）
# 确认请求方法、URL、body 都正确

# 查看模板渲染结果
# 在浏览器中右键"查看源代码"
```

---

## ✨ 大功告成！

当所有检查都通过，恭喜！🎉

你已完成 PairProxy v2.20 WebUI 的完整扩展，包括：
- ✅ Group-Target Set 管理（双栏设计）
- ✅ Alert 升级（实时流 + 活跃 + 历史 + 批量）
- ✅ 快速操作面板（overview 增强）
- ✅ 完整文档和测试

这是一个重要的功能里程碑，为管理员提供了完整的 WebUI 管理能力！

---

**开发从这里开始！** 🚀
