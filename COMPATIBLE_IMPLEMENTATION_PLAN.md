# PairProxy v2.20 WebUI 扩展 - 兼容性实施方案

**基于现有 WebUI 设计模式的详细实施指南**

---

## 总体决策确认

基于对现有代码的深入分析，已采用所有**推荐方案**：

| 决策点 | 推荐方案 | 理由 |
|-------|--------|------|
| **Tab 内嵌** | ✅ 采用 | 现有导航 8 项满，新增会溢出；SSR 天生支持 URL 状态 |
| **双栏设计** | ✅ 采用 | 符合现有 llm.html 表格布局；易复用现有模态框模式 |
| **健康状态** | ✅ 初始加载后无实时 | 管理员低频访问，30s 延迟可接受；最小改动 |
| **告警保留** | ✅ 90 天滑动 | 防止 DB 爆炸；分页加载避免过大 |
| **Semantic Route** | ✅ v2.21 延后 | 复杂度高；v2.20 focus Target Set + Alert |
| **成员字段** | ✅ 仅 enabled | 简单可用；其他 v2.21 扩展 |
| **reason 字段** | ✅ 可选 | 体验优先；审计可选 |
| **快速面板** | ✅ 启用 | 低成本；提升 overview 体验 |
| **时间表** | ✅ 1 周完成 | 工作量合理；风险低 |

---

## 关键设计模式总结（基于代码分析）

### HTTP 处理流程
```
请求 → ParseForm → 参数验证 → DB操作 → 日志 → 302重定向(flash/error)
```

### Flash 消息机制
```go
// 成功：
http.Redirect(w, r, "/dashboard/page?flash=成功消息", http.StatusSeeOther)

// 错误：
http.Redirect(w, r, "/dashboard/page?error=错误信息", http.StatusSeeOther)

// 模板：
{{if .Flash}} <div class="bg-green-100">{{.Flash}}</div> {{end}}
{{if .Error}} <div class="bg-red-100">{{.Error}}</div> {{end}}
```

### 模态框模式
```javascript
// 打开：移除 hidden class
document.getElementById('modal').classList.remove('hidden');

// 关闭：添加 hidden class
document.getElementById('modal').classList.add('hidden');

// 表单 action 动态设置：
form.action = '/endpoint/' + id + '/update';
```

### Tab 切换模式
```html
<!-- URL 驱动，无 JS 路由 -->
<a href="/dashboard/page?tab=targets">LLM 目标</a>
<a href="/dashboard/page?tab=targetsets">目标集</a>

<!-- 模板条件渲染 -->
{{if eq .ActiveTab "targets"}} ... {{end}}
{{if eq .ActiveTab "targetsets"}} ... {{end}}
```

### 中间件链式应用
```go
// 只读路由
mux.Handle("GET /dashboard/page", h.requireSession(handler))

// 写操作路由（需要可写节点）
rw := func(hf http.Handler) http.Handler {
  return h.requireSession(h.requireWritableNode(hf))
}
mux.Handle("POST /dashboard/page", rw(handler))
```

---

## Phase 1: Group-Target Set 管理（3-4 天）

### 修改文件清单

```
修改：
  ├─ internal/dashboard/handler.go
  │  ├─ 新增字段：groupTargetSetRepo
  │  ├─ 新增 Setter：SetGroupTargetSetRepo
  │  ├─ RegisterRoutes 追加 6 条路由
  │  └─ 新增 6 个 Handler 函数
  │
  ├─ internal/dashboard/llm_handler.go
  │  ├─ llmPageData 新增字段：ActiveTab, TargetSets, SelectedSetID, GroupsForBind
  │  ├─ handleLLMPage 扩展 Tab 分支逻辑
  │  └─ 新增 targetSetWithMembers 结构
  │
  ├─ internal/db/group_target_set_repo.go
  │  └─ 新增 ListMembersForSets() 方法（批量查询，避免 N+1）
  │
  └─ internal/dashboard/templates/llm.html
     ├─ 添加 Tab 栏（3 个标签页）
     ├─ 重组现有内容到 named template（targets、bindings）
     ├─ 新增 targetsets Tab 双栏布局
     ├─ 新增 targetSetModal 模态框
     └─ 新增 JavaScript 交互函数
```

### 核心代码片段

#### handler.go - 新增字段和路由

```go
type Handler struct {
  // ... 现有 ...
  groupTargetSetRepo *db.GroupTargetSetRepo
}

func (h *Handler) SetGroupTargetSetRepo(repo *db.GroupTargetSetRepo) {
  h.groupTargetSetRepo = repo
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
  // ... 现有 ...
  rw := func(hf http.Handler) http.Handler {
    return h.requireSession(h.requireWritableNode(hf))
  }

  mux.Handle("POST /dashboard/llm/targetsets",
    rw(http.HandlerFunc(h.handleTargetSetCreate)))
  mux.Handle("POST /dashboard/llm/targetsets/{id}/update",
    rw(http.HandlerFunc(h.handleTargetSetUpdate)))
  mux.Handle("POST /dashboard/llm/targetsets/{id}/delete",
    rw(http.HandlerFunc(h.handleTargetSetDelete)))
  mux.Handle("POST /dashboard/llm/targetsets/{id}/members",
    rw(http.HandlerFunc(h.handleTargetSetAddMember)))
  mux.Handle("POST /dashboard/llm/targetsets/{id}/members/{memberID}/update",
    rw(http.HandlerFunc(h.handleTargetSetUpdateMember)))
  mux.Handle("POST /dashboard/llm/targetsets/{id}/members/{memberID}/delete",
    rw(http.HandlerFunc(h.handleTargetSetRemoveMember)))
}
```

#### llm_handler.go - 扩展数据结构

```go
type llmPageData struct {
  baseData

  // 现有字段...

  // v2.20 新增
  ActiveTab      string
  TargetSets     []targetSetWithMembers
  SelectedSetID  string
  GroupsForBind  []db.Group
}

type targetSetWithMembers struct {
  db.GroupTargetSet
  Members        []db.GroupTargetSetMember
  BoundGroupName string
  MemberCount    int
  HealthyCount   int
  DegradedCount  int
  UnhealthyCount int
}
```

#### llm.html - Tab 栏结构

```html
<div class="border-b border-gray-200 mb-6">
  <nav class="flex gap-1">
    <a href="/dashboard/llm?tab=targets"
       class="px-3 py-2 {{if eq .ActiveTab "targets"}}text-indigo-600 border-b-2 border-indigo-600{{else}}text-gray-600{{end}}">
      LLM 目标
    </a>
    <a href="/dashboard/llm?tab=targetsets"
       class="px-3 py-2 {{if eq .ActiveTab "targetsets"}}text-indigo-600 border-b-2 border-indigo-600{{else}}text-gray-600{{end}}">
      目标集 ({{len .TargetSets}})
    </a>
    <a href="/dashboard/llm?tab=bindings"
       class="px-3 py-2 {{if eq .ActiveTab "bindings"}}text-indigo-600 border-b-2 border-indigo-600{{else}}text-gray-600{{end}}">
      绑定关系 ({{len .Bindings}})
    </a>
  </nav>
</div>

{{if eq .ActiveTab "targets"}}
  {{template "llm-targets-tab" .}}
{{else if eq .ActiveTab "targetsets"}}
  {{template "llm-targetsets-tab" .}}
{{else}}
  {{template "llm-bindings-tab" .}}
{{end}}
```

---

## Phase 2: Alert 管理升级（1-2 天）

### 修改文件清单

```
修改：
  ├─ internal/dashboard/handler.go
  │  ├─ 新增字段：alertRepo
  │  ├─ RegisterRoutes 新增 resolve/resolve-batch 路由
  │  ├─ 修改 handleAlertsPage 支持多 Tab
  │  ├─ 新增 handleAlertResolve（单条）
  │  └─ 新增 handleAlertResolveBatch（批量）
  │
  └─ internal/dashboard/templates/alerts.html
     ├─ 添加 Tab 栏（3 个标签页：live、active、history）
     ├─ 保留现有 live Tab 内容
     ├─ 新增 active Tab（活跃告警表）
     ├─ 新增 history Tab（历史查询）
     └─ 新增 JavaScript 交互
```

### 核心特性

#### 活跃告警 Tab
- ✅ 列表展示（checkbox 选择）
- ✅ 统计卡片（Critical/Error/Warning）
- ✅ 单条解决（确认框）
- ✅ 批量解决（POST /dashboard/alerts/resolve-batch）
- ✅ 表格操作按钮

#### 历史查询 Tab
- ✅ 时间范围选择（7/30/90 天）
- ✅ 目标筛选（可选）
- ✅ 分页加载（每页 50 条）
- ✅ 表格显示（发生时间、解决时间等）

---

## Phase 3: 快速操作面板（1 天，可选）

### 修改文件

```
修改：
  └─ internal/dashboard/templates/overview.html
     ├─ 新增 section：运维快速操作
     ├─ 3 个卡片：LLM 状态、告警、用户/分组
     └─ 异步 fetch 加载数据（页面加载后）
```

### 卡片内容

1. **LLM 目标状态**
   - 健康：3/4
   - 告警：2 条
   - 目标集：3 个
   - 快捷链接：进入 LLM 管理

2. **系统告警**
   - 未解决：5 条
   - 级别分布：Critical/Error/Warning
   - 快捷链接：查看告警详情

3. **用户/分组**
   - 活跃用户：42
   - 总分组：8
   - 今日新增：2
   - 快捷链接：管理用户

---

## 代码实施要点

### 1. 数据库优化（N+1 避免）

```go
// 不推荐：循环查询
for _, setID := range setIDs {
  members := repo.GetMembers(setID)  // N+1!
}

// 推荐：批量查询
membersMap := repo.ListMembersForSets(setIDs)  // 1 次查询
```

### 2. Worker 节点只读保护

```html
<!-- 模板层防守 -->
{{if not .IsWorkerNode}}
  <button onclick="...">添加成员</button>
{{else}}
  <button disabled class="opacity-50">添加成员（仅主节点）</button>
{{end}}
```

```go
// 路由层防守（已有中间件）
rw := func(hf http.Handler) http.Handler {
  return h.requireSession(h.requireWritableNode(hf))
}
```

### 3. 模态框级联删除

```go
// 目标集删除时，级联删除成员
if err := h.groupTargetSetRepo.Delete(id); err != nil {
  // 在 repo.Delete 中使用事务
  // 1. 删除所有成员
  // 2. 删除目标集本身
}
```

### 4. 成员权重编辑内联模式

```javascript
// 初始：显示值
<span id="weight-123">3</span>

// 编辑：显示输入框
<input type="number" value="3" id="weight-input-123">

// 保存：提交表单
POST /dashboard/llm/targetsets/{setID}/members/{memberID}/update
body: weight=5&priority=0
```

---

## 测试清单

### 功能测试

- [ ] 创建目标集
- [ ] 编辑目标集名称、绑定分组、策略
- [ ] 删除目标集（级联删除成员）
- [ ] 添加成员到目标集
- [ ] 修改成员权重/优先级（内联编辑）
- [ ] 删除成员
- [ ] 批量解决告警
- [ ] 历史查询分页
- [ ] 快速面板卡片加载

### 兼容性测试

- [ ] Tab 切换（targets/targetsets/bindings）
- [ ] URL 书签保持（?tab=targetsets&selected=id）
- [ ] Flash 消息正常显示
- [ ] Worker 节点按钮禁用
- [ ] 响应式设计（桌面/平板/手机）

### 性能测试

- [ ] 页面加载时间 < 1s
- [ ] 表格操作响应 < 200ms
- [ ] 无明显的 N+1 SQL 查询
- [ ] 成员列表支持 >100 条

### 浏览器兼容性

- [ ] Chrome/Edge
- [ ] Firefox
- [ ] Safari

---

## 发布计划

### v2.20.0（1 周内）

- ✅ Phase 1: Group-Target Set UI
- ✅ Phase 2: Alert 活跃 + 批量解决
- ✅ Phase 3: 快速操作面板

### v2.20.1（后续）

- Alert 历史查询优化
- Semantic Route UI（延后至 v2.21）

---

## 立即可实施

所有代码设计基于现有 WebUI 实现，**无兼容性问题**，**无破坏性修改**。

✅ Tab 内嵌方式与现有导航一致
✅ 模态框、Flash 消息、中间件全部复用
✅ Tailwind 样式与现有设计统一
✅ JavaScript 交互遵循现有模式
✅ 数据库操作遵循现有 repo 模式

**可立即开始代码实施！**
