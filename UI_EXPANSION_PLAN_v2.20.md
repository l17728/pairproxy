# PairProxy v2.20 WebUI 扩展设计方案

**文档时间**: 2025年3月
**版本**: v2.20.0
**状态**: 设计方案（等待评审）

---

## 目录

1. [现状分析](#现状分析)
2. [需求概览](#需求概览)
3. [UI/UX 设计方案](#uiux-设计方案)
4. [技术实施路线](#技术实施路线)
5. [决策点与建议](#决策点与建议)

---

## 现状分析

### 当前 WebUI 最小集（v1.0-v2.15 遗留）

PairProxy 当前 WebUI 由 **10 个页面** 组成，均为服务端渲染（SSR）+ 原生 JavaScript：

| 页面 | 功能 | 数据修改能力 | 状态 |
|-----|------|-----------|------|
| login | 管理员认证 | ❌ | v1.0+ |
| overview | 仪表板（日统计、趋势、Top用户） | ❌ | v1.0+ |
| users | 用户 CRUD、密码/Token管理 | ✅ 完整CRUD | v1.0+ |
| groups | 分组 CRD、配额修改 | ✅ CRD+配额 | v1.1+ |
| logs | 请求日志查询、导出、清空 | ⚠️ 清空 | v1.2+ |
| audit | 管理操作审计日志 | ❌ 只读 | v2.0+ |
| llm | LLM 目标 CRUD、绑定管理 | ✅ 部分 | v2.10+ |
| my-usage | 用户流量统计、配额进度 | ❌ 只读 | v2.5+ |
| import | 批量导入用户/分组 | ✅ | v2.15+ |
| alerts | 系统告警实时流 | ⚠️ 手动解决 | **v2.20 新增** |

### v2.16-v2.20 版本演进中的新特性

| 版本 | 核心特性 | UI 覆盖度 | 缺口 |
|-----|---------|---------|------|
| v2.18 | Semantic Router（语义路由） | ❌ 无 | **缺 UI 管理页** |
| v2.19 | LLM Target 热更新（WebUI同步） | ✅ 完整 | — |
| v2.20 | 三大新特性 | ⚠️ 部分 | 详见下 |

**v2.20 三大新特性与 UI 覆盖**：

1. **Group-Target Set** (目标分组集合)
   - 功能：为每个 Group 定义专属 LLM Target 组合、加权策略、重试方案
   - API 完整度：✅ 100% (CRUD 端点已全)
   - UI 覆盖度：⚠️ **0%（关键缺失）**
   - 影响：管理员无法通过 WebUI 管理，降低易用性

2. **Target Health Monitor** (背景健康检查)
   - 功能：30s 周期主动探测所有 LLM 目标，更新成员健康状态，触发告警
   - UI 覆盖度：⚠️ **30%**（告警页完整，但目标健康状态未可视化）
   - 缺失：llm.html 中目标列表缺 `HealthStatus` 列

3. **Alert Manager** (告警管理)
   - 功能：事件驱动告警，实时 SSE 推送，DB 持久化，手动解决
   - UI 覆盖度：✅ **85%**（实时流完整，缺历史查询、批量操作）
   - 缺失：活跃告警、历史查询 Tab

### 管理员实时管理需求分析

管理员需要在 UI 上快速执行的操作分为 **7 大对象**：

| 对象 | 当前 UI | API | 修改生效 | 重启需求 | 优先级 |
|-----|--------|-----|---------|---------|--------|
| API Key | ✅ CRD | ✅ | 立即 | ❌ | — |
| User | ✅ CRUD | ✅ | 立即 | ❌ | — |
| Group | ✅ CRD+配额 | ✅ | 立即 | ❌ | — |
| LLM Target | ✅ CRUD | ✅ | 立即 | ❌ | — |
| LLM Binding | ✅ CRUD | ✅ | 立即 | ❌ | — |
| **Semantic Route** | ❌ | ✅ | 立即 | ❌ | **🔴 高** |
| **Group-Target Set** | ❌ | ✅ | 立即 | ❌ | **🔴 高** |

关键发现：**API 层支持完整，但 UI 严重滞后**。特别是 v2.20 引入的两大新对象（语义路由、分组目标集），完全无 UI 支持，管理员被迫使用 CLI 或 REST API。

---

## 需求概览

### 用户故事

**作为** 企业网关管理员
**我需要** 在 WebUI 上实时完成所有配置修改（无需 SSH/CLI）
**以便** 快速响应业务变化、故障恢复、权限调整

### 核心功能需求

#### 需求 1：Group-Target Set 管理（最高优先级）

- [ ] **创建目标集**：定义分组专属的 LLM Target 组合、加权策略（加权随机/轮询/优先级）、重试策略
- [ ] **绑定分组**：为 Group 指定 Target Set，覆盖全局默认
- [ ] **成员管理**：增删改目标成员、权重、优先级、启用/禁用状态
- [ ] **健康状态可视化**：实时显示成员健康（健康/降级/故障/未知）

#### 需求 2：告警管理升级

- [ ] **活跃告警 Tab**：实时数据库告警汇总、按目标/级别筛选、**批量解决**
- [ ] **历史查询 Tab**：7/30/90 天范围查询、分页、导出选项
- [ ] **告警统计**：Critical/Error/Warning 分布图、目标故障率排行

#### 需求 3：语义路由管理（可选，v2.21 后续）

- [ ] **路由规则编辑**：创建分类规则、定义意图匹配、目标映射
- [ ] **效果统计**：分类准确率、目标分布

#### 需求 4：运维一体化**仪表板**（可选增强）

- [ ] **快速操作面板**：LLM 健康概览、告警汇总、最近修改
- [ ] **快捷导航**：直达功能主页

---

## UI/UX 设计方案

### 设计原则

1. **无新增顶级导航**：现有导航栏已有 8 项，再加会破坏小屏适应。采用 **Tab 内嵌** 方案
2. **SSR + 原生 JS 保持**：不引入前端框架，保持既有"无构建步骤"优势
3. **渐进增强**：新功能通过 POST 表单 + 302 重定向，保证即使 JS 失效也可用
4. **信息架构扁平化**：复杂对象仍保持"列表+详情"双栏，不增加层级深度

### 方案 A：推荐方案 - Tab 内嵌策略

**核心理念**：LLM 和告警页面扩展为多标签页，本质不增加新页面数。

#### 1. LLM 管理页面（llm.html）重构

```
当前: /dashboard/llm → 单一目标管理
新版: /dashboard/llm?tab=targets|targetsets|bindings → 三标签页
```

**Tab 结构**：

| Tab | URL | 功能 | 来源 |
|-----|-----|------|------|
| LLM 目标 | `?tab=targets`（默认） | 目标 CRUD + 排水控制 | 现有内容搬移 |
| **目标集** | `?tab=targetsets` | **新增**：分组目标集管理 | **新页面** |
| 绑定关系 | `?tab=bindings` | User→Target 绑定 | 现有内容搬移 |

**目标集 Tab 布局（关键设计）**：

```
┌─────────────────────────────────────────────────────────┐
│ LLM 管理                                                 │
├────────────┬────────────┬─────────────────────────────┤
│[LLM目标]  [目标集] [绑定] │ [+ 新建] [绑定到分组] │ 搜索 │
├─────────────────────────────────────────────────────────┤
│ LEFT (1/3)              │ RIGHT (2/3)                   │
├─────────────────────────┼──────────────────────────────┤
│ 目标集列表              │ 选中目标集详情 + 成员列表    │
│ ┌───────────────────┐  │ ┌──────────────────────────┐ │
│ │[默] 默认目标集    │  │ │名: 默认目标集            │ │
│ │3个成员 加权随机   │  │ │策略: 加权随机           │ │
│ │[编] [删]          │  │ │重试: 顺延               │ │
│ └───────────────────┘  │ │绑定: default-group      │ │
│ ┌───────────────────┐  │ ├──────────────────────────┤ │
│ │[组] 工程师组集    │  │ │成员列表                 │ │
│ │2个成员 轮询       │  │ │URL  权重 优先 启用 健康 │ │
│ │[编] [删]          │  │ │api.o  3   0  [on] [健] │ │
│ └───────────────────┘  │ │api.a  1   0  [on] [健] │ │
│ [+ 新建目标集]        │ │[+ 添加成员]             │ │
│                        │ └──────────────────────────┘ │
└─────────────────────────┴──────────────────────────────┘
```

**核心特性**：
- 左栏列表：卡片式目标集，显示成员数、策略、绑定分组、快捷操作
- 右栏详情：选中目标集的成员列表、内联编辑权重/优先级、删除成员
- 健康状态徽章：绿(健康) / 橙(降级) / 红(故障) / 灰(未知)
- 添加成员：下拉选择已有 LLM Target，设置权重/优先级，一键添加

**交互流程**：

```
创建流程:
1. 点 [+ 新建目标集]
2. 模态框弹出 → 填名称、策略、绑定分组、可选添加初始成员
3. 提交 POST /dashboard/llm/targetsets
4. 302 redirect /dashboard/llm?tab=targetsets&flash=已创建
5. 页面刷新，新项在列表出现

编辑成员权重:
1. 右栏点成员行的 [编辑]
2. 该行权重/优先级变为 <input>
3. 改值后点 [保存]
4. 提交 POST /dashboard/llm/targetsets/{id}/members/{url}/update
5. 302 redirect 回来，行恢复只读

删除成员:
1. 右栏点 [删除]
2. 确认框弹出
3. 提交 DELETE （实际用 POST 伪装）
4. 302 redirect，成员行消失
```

#### 2. 告警管理页面（alerts.html）升级

```
当前: /dashboard/alerts → 实时流单视图
新版: /dashboard/alerts?tab=live|active|history → 三标签页
```

**Tab 结构**：

| Tab | URL | 功能 | 说明 |
|-----|-----|------|------|
| 实时事件流 | `?tab=live`（默认） | SSE 实时推送事件 | 现有保持 |
| **活跃告警** | `?tab=active` | **新增**：DB 未解决告警 | 数据源改为 TargetAlertRepo |
| **历史查询** | `?tab=history` | **新增**：日期范围查询 | 分页加载，7天 |

**活跃告警 Tab 布局**：

```
┌────────────────────────────────────────────────────────┐
│ 告警管理                                               │
├──────────────┬──────────────┬─────────────────────────┤
│[实时] [活跃] [历史] │ [🔄] [x 清除] [↓ 导出] │ 搜索 │
├────────────────────────────────────────────────────────┤
│ 共N条未解决  Critical: X  Error: Y  Warning: Z         │
│ [筛选] [批量解决N条]                                   │
├────────────────────────────────────────────────────────┤
│[x]目标URL      事件类型 级别 次数 首发 最后 操作       │
│[x]api.openai   error  C  12  2h  1h  [解决][详情]    │
│[x]api.local    degrad W   3  5m  2m  [解决][详情]    │
│    ...                                                 │
├────────────────────────────────────────────────────────┤
│[汇总卡片]                                              │
│ api.openai: 12 次告警   故障率 100%   [详情历史]      │
│ api.local: 3 次告警     故障率 33%    [详情历史]      │
└────────────────────────────────────────────────────────┘
```

**关键交互**：

```
批量解决:
1. 勾选多行 checkbox 或点 [全选]
2. 底部 [批量解决N条] 按钮激活
3. 点击 → 确认框
4. POST /dashboard/alerts/resolve-batch?ids=xx&ids=yy
5. 302 redirect /dashboard/alerts?tab=active&flash=已解决N条
6. 页面刷新，表格更新，checkbox 重置
```

**历史查询 Tab 布局**：

```
┌────────────────────────────────────────────────────────┐
│ [时间范围] [7天 v] [目标筛选] [全部 v] [查询]         │
├────────────────────────────────────────────────────────┤
│ 时间   目标URL    事件类型  级别  次数  解决时间       │
│ 2025-01-01  api.openai  error  C    5   2025-01-02   │
│ 2025-01-01  api.local   degrad W    3   (未解决)     │
│ ...                                                    │
├────────────────────────────────────────────────────────┤
│ [< 上一页] 第 1/5 页  [下一页 >]   每页 50 条        │
└────────────────────────────────────────────────────────┘
```

#### 3. 概览页面（overview.html）增强

在现有趋势图下方新增 **"运维快速操作"** 卡片区：

```
┌────────────────────────────────────────────────────────┐
│ 运维快速操作                                           │
├────────────────────────────────────────────────────────┤
│ LLM 目标状态                                          │
│ [健康 3/4] [告警 2] [目标集 3] → [进入管理]            │
│                                                      │
│ 系统告警                                              │
│ [未解决 5] (Critical 2 / Error 1 / Warning 2)        │
│ → [查看告警详情]                                      │
│                                                      │
│ 用户/分组                                             │
│ [活跃用户 42] [总分组 8] [新增今日 2]               │
│ → [管理用户]                                          │
├────────────────────────────────────────────────────────┤
│ 最近修改                                              │
│ 15 min ago: 修改 api.local 权重 (管理员 root)        │
│ 1 hour ago: 添加分组 marketing-group (管理员 root)    │
└────────────────────────────────────────────────────────┘
```

这些数据通过页面加载时异步 fetch 获得，不阻塞关键渲染路径。

---

### 方案 B：替代方案 - 独立新页面方案

（仅在如果方案 A 实施困难时考虑）

```
新增:
/dashboard/targetsets → Target Set 管理独立页面
/dashboard/alerts/history → 历史告警查询独立页面
```

**缺点**：
- 导航栏会超长（需 dropdown menu）
- 违反现有扁平化设计
- 用户需要额外点击进入

**不推荐**，仅作备选。

---

## 技术实施路线

### 实施阶段规划

#### Phase 1：Group-Target Set 管理（最高优先级，约 3-4 天）

**文件修改清单**：

```
修改:
  ├─ internal/dashboard/handler.go
  │  ├─ Handler struct 新增字段: groupTargetSetRepo
  │  ├─ RegisterRoutes 新增 6 条 targetset 路由
  │  └─ registerTargetSetRoutes() 新函数
  │
  ├─ internal/dashboard/llm_handler.go
  │  ├─ llmPageData 新增字段: ActiveTab, TargetSets, SelectedSetID, GroupsForBind
  │  ├─ handleLLMPage() 扩展 Tab 分支逻辑
  │  └─ 新增 6 个 handler:
  │     ├─ handleTargetSetCreate
  │     ├─ handleTargetSetUpdate
  │     ├─ handleTargetSetDelete
  │     ├─ handleTargetSetAddMember
  │     ├─ handleTargetSetRemoveMember
  │     └─ handleTargetSetUpdateMember
  │
  ├─ internal/db/group_target_set_repo.go
  │  └─ 新增 ListMembersForSets(setIDs []string) 方法（优化 N+1）
  │
  └─ internal/dashboard/templates/llm.html
     ├─ 顶部 Tab 栏（3 个 Tab）
     ├─ Tab1: LLM 目标（现有内容）
     ├─ Tab2: 目标集管理（新增，两栏布局）
     ├─ Tab3: 绑定关系（搬移现有内容）
     ├─ 模态框: 新建目标集表单
     └─ JS: 成员内联编辑、添加成员交互
```

**数据库 ER 关系确认**（无需修改，已存在）：

```
GroupTargetSet
  ├─ id (PK)
  ├─ group_id (FK, nullable → 默认组)
  ├─ name (unique)
  ├─ strategy (weighted_random|round_robin|priority)
  ├─ retry_policy (try_next|fail_fast)
  └─ is_default

GroupTargetSetMember
  ├─ id (PK)
  ├─ target_set_id (FK)
  ├─ target_url (FK)
  ├─ weight (int)
  ├─ priority (int)
  ├─ is_active
  ├─ health_status (healthy|degraded|unhealthy|unknown)
  ├─ consecutive_failures (int)
  └─ last_health_check (timestamp)
```

**API 调用时序**（SSR 页面加载）：

```
GET /dashboard/llm?tab=targetsets
 └─ handler.handleLLMPage()
     ├─ groupTargetSetRepo.ListAll()
     ├─ groupTargetSetRepo.ListMembersForSets(ids) [批量，优化]
     ├─ groupRepo.List() [用于显示绑定的分组名]
     ├─ llmTargetRepo.ListAll() [用于添加成员下拉]
     └─ 合并到 llmPageData，渲染 llm.html
```

**写操作路由示例**：

```
POST /dashboard/llm/targetsets
  body: name=default&group_id=&strategy=weighted_random&retry_policy=try_next
  → groupTargetSetRepo.Create(&GroupTargetSet{...})
  → 302 /dashboard/llm?tab=targetsets&flash=Created

POST /dashboard/llm/targetsets/{setID}/members
  body: target_url=https://api.openai.com&weight=3&priority=0
  → groupTargetSetRepo.AddMember(setID, &GroupTargetSetMember{...})
  → 302 /dashboard/llm?tab=targetsets&selected={setID}&flash=Member+Added
```

**风险点**：
1. `GroupTargetSetMember.TargetURL` 含 `://`，路由参数需 URL 编码
   - 解决：改用 `memberID` 路由，存储 memberID
2. Target Set 删除时级联删除成员（GORM 无 Claude 配置）
   - 解决：handler 中事务包裹或 repo 中手工删除成员

#### Phase 2：告警管理升级（约 2-3 天）

**文件修改清单**：

```
修改:
  ├─ internal/dashboard/handler.go
  │  ├─ Handler struct 新增: alertRepo
  │  ├─ RegisterRoutes 新增 /dashboard/alerts/resolve/{id} 和 /resolve-batch
  │  └─ 新增 2 个 handler:
  │     ├─ handleAlertResolve (单条)
  │     └─ handleAlertResolveBatch (批量)
  │
  ├─ internal/dashboard/handler.go 中 handleAlertsPage
  │  ├─ tab := r.URL.Query().Get("tab") [live|active|history]
  │  ├─ 若 tab=active: alertRepo.ListActive(filters)
  │  ├─ 若 tab=history: alertRepo.ListHistory(days, page, pageSize)
  │  └─ 装入 alertsPageData
  │
  └─ internal/dashboard/templates/alerts.html
     ├─ Tab 栏（3 个 Tab）
     ├─ Tab1: 实时事件流（现有，无改）
     ├─ Tab2: 活跃告警（新增）
     │   ├─ checkbox 全选
     │   ├─ 汇总卡片（按目标聚合）
     │   └─ [批量解决] 按钮
     ├─ Tab3: 历史查询（新增）
     │   ├─ GET 表单 (days, target_filter, page)
     │   └─ 分页表格
     └─ JS: 全选/反选、计数更新
```

**API 设计** (已存在，dashboard 需调用)：

```
GET /api/admin/alerts/active
  → 所有 resolved_at IS NULL 的告警

GET /api/admin/alerts/history?days=30&page=1&limit=50
  → 指定日期范围的历史告警，分页

GET /api/admin/alerts/stats?days=30
  → 告警统计 (Critical/Error/Warning 计数)

POST /api/admin/alerts/{id}/resolve
  → 手动解决单条告警

POST /api/admin/alerts/resolve-batch
  body: ids[]=xx&ids[]=yy
  → 批量解决
```

**性能优化**：

- 活跃告警页：数据直接 DB 查询，通常 <100 条，无需分页
- 历史查询：分页加载（50 条/页），避免一次加载大量数据
- SSE 实时流：保持内存 eventlog（容量 500 条，重启清空），不改

#### Phase 3：运维快速操作面板（约 1 天，可选）

**文件修改**：

```
修改:
  └─ internal/dashboard/templates/overview.html
     ├─ 新增 <section class="quick-ops"> 卡片区块
     ├─ 页面加载时 fetch 3 个 API:
     │  ├─ /api/admin/llm/targets/health-summary
     │  ├─ /api/admin/alerts?resolved=false&limit=1
     │  └─ /api/admin/stats/users-groups
     ├─ 填充卡片数据（JS 异步）
     └─ 提供快捷导航链接
```

**API 需求** (新增轻量级端点)：

```
GET /api/admin/llm/targets/health-summary
  → { healthy: 3, degraded: 1, unhealthy: 0, targetSetCount: 3 }

GET /api/admin/alerts?resolved=false&limit=1
  → { unresolved_count: 5, critical: 2, error: 1, warning: 2 }

GET /api/admin/stats/users-groups
  → { active_users: 42, total_groups: 8, new_today: 2 }
```

如这些端点不存在，可用现有端点替代（稍详细些，但可接受）。

#### Phase 4：其他优化（可选）

- [ ] Semantic Route 管理 UI（v2.21 后续）
- [ ] 目标列表 llm.html Tab 新增 `HealthStatus` 列
- [ ] 告警历史导出为 CSV/Excel
- [ ] 批量告警解决前的确认框

---

## 决策点与建议

### 决策 1：Tab 方案 vs 新页面方案

| 方案 | 优点 | 缺点 | 推荐 |
|-----|------|------|------|
| **Tab 内嵌** (A) | 导航简洁；学习曲线低；深链接可分享 | 单页面复杂度上升；Tab 切换无动画感 | ✅ **推荐** |
| **独立新页面** (B) | 单个页面逻辑独立 | 导航栏溢出；用户需更多点击 | ❌ 不推荐 |

**建议**：采方案 A（Tab 内嵌）。理由：
- 保持导航栏 8 项的简洁设计
- LLM 和告警页本质相关（都是配置/运维工作），Tab 组织更合理
- 深链接 `?tab=targetsets` 可直达，便于收藏和分享

---

### 决策 2：优先级排序

| 优先级 | 功能 | 工作量 | 关键性 | 截止 |
|-------|------|--------|--------|------|
| 🔴 **P0** | **Group-Target Set UI** | 3-4 天 | v2.20 核心特性，无 UI 无法用 | v2.20 |
| 🔴 **P1** | **Alert 批量解决** | 1 天 | 常见运维需求 | v2.20 |
| 🟡 **P2** | Alert 历史查询 Tab | 1 天 | 审计需求，可延后 | v2.21 |
| 🟡 **P2** | 快速操作面板 | 1 天 | 体验增强，可选 | v2.21 |
| 🟢 **P3** | Semantic Route UI | 2 天 | v2.18 特性，使用少 | v2.21+ |

**建议发布计划**：

```
v2.20.0 (立即)
  ├─ ✅ Group-Target Set 管理 UI (P0)
  └─ ✅ Alert 批量解决 + 活跃 Tab (P1)

v2.20.1 (1-2 周)
  ├─ ✅ Alert 历史查询 Tab (P2)
  └─ ⚠️ 快速操作面板 (P2, 可选)

v2.21.0
  ├─ ✅ Semantic Route 管理 UI (P3)
  └─ 其他优化
```

---

### 决策 3：Worker 节点只读保护

**现状**：handler.go 中 `rw` 中间件已对所有 POST 路由返回 403（Worker 节点）。

**建议**：
1. 模板层再加一层防守：新增写操作按钮加 `{{if not .IsWorkerNode}}` 判断
2. 可选：为 Worker 节点提供"只读模式提示"（浅色按钮 + disabled 属性）

**代码示例**：

```html
{{if not .IsWorkerNode}}
  <button onclick="...">添加成员</button>
{{else}}
  <button disabled class="opacity-50 cursor-not-allowed" title="Worker节点不可修改">
    添加成员（仅主节点可用）
  </button>
{{end}}
```

---

### 决策 4：UI 组件库与一致性

**现状**：使用 Tailwind CSS (CDN) + 原生 HTML，无第三方组件库。

**建议**：保持现状，理由：
- Tailwind 足以构建现代 UI
- 无需额外依赖
- SSR 快速加载

**样式规范**：
- 表格：`<table class="min-w-full divide-y divide-gray-200">`
- 按钮主色：`bg-indigo-600 hover:bg-indigo-700`
- 告警色：绿(healthy) / 橙(degraded) / 红(unhealthy)
- 卡片：`bg-white shadow rounded-lg p-4`

---

### 决策 5：实时性与数据一致性

**问题**：Target Set 成员的 `health_status` 由后台 TargetHealthMonitor 30s 更新一次。前端 UI 显示可能不是实时最新。

**方案**：
1. **实时流 Tab**：通过 SSE 推送最新事件（已有）
2. **活跃告警 Tab**：显示 DB 数据（有 30s 延迟，可接受）
3. **目标集详情右栏**：成员健康状态通过页面初始加载 DB，可在表头加"最后更新：刚刚"标签

**建议**：可接受 30s 延迟。如需更实时，可在活跃告警 Tab 添加"刷新"按钮，用户手动触发 API refresh。

---

### 决策 6：Target Set 删除的级联逻辑

**问题**：Group-Target Set 删除时，关联的 GroupTargetSetMember 需清理，但 GORM 未配置 `cascade:delete`。

**方案**：handler 中事务包裹：

```go
func (h *Handler) handleTargetSetDelete(w http.ResponseWriter, r *http.Request) {
    id := chi.URLParam(r, "id")
    tx := h.db.BeginTx(r.Context(), nil)

    // 1. 删除成员
    if err := tx.Delete(&db.GroupTargetSetMember{}, "target_set_id = ?", id).Error; err != nil {
        tx.Rollback()
        h.respondError(w, "删除失败", 500)
        return
    }

    // 2. 删除 TargetSet
    if err := tx.Delete(&db.GroupTargetSet{}, "id = ?", id).Error; err != nil {
        tx.Rollback()
        h.respondError(w, "删除失败", 500)
        return
    }

    tx.Commit()
    http.Redirect(w, r, "/dashboard/llm?tab=targetsets&flash=已删除", 302)
}
```

**建议**：采用事务方案，保证数据一致性。

---

## 实施检查清单

### Code Review 要点

- [ ] **Handler 字段初始化**：`SetGroupTargetSetRepo()` 调用点（cmd/sproxy/main.go）
- [ ] **路由冲突**：新的 `/dashboard/llm/targetsets/*` 路由与现有路由无冲突
- [ ] **SQL N+1**：`ListMembersForSets` 批量查询实现正确
- [ ] **URL 编码**：成员删除路由中 memberID 或 URL 的编码/解码
- [ ] **错误处理**：模态框提交失败时的错误提示（flash message）
- [ ] **Worker 节点**：所有新增写操作都检查 `IsWorkerNode`
- [ ] **事务**：Target Set 删除时级联逻辑用事务保护
- [ ] **模板**：Tab 切换时活跃样式正确应用（`bg-indigo-600` vs `bg-gray-100`）

### 测试覆盖

- [ ] **创建目标集**：无分组（默认）+ 指定分组
- [ ] **添加成员**：重复 Target + 超出数量限制
- [ ] **删除目标集**：级联删除成员验证
- [ ] **健康状态同步**：后台更新 health_status 后页面是否反映（刷新后可见）
- [ ] **批量告警解决**：勾选 + 解决 + 刷新验证
- [ ] **Worker 节点**：Worker 访问时写操作按钮禁用

### 性能指标

- [ ] 页面加载时间：`/dashboard/llm?tab=targetsets` < 1s
- [ ] 表格渲染：>1000 个成员时列表仍可用（或分页）
- [ ] SSE 推送：告警实时流延迟 < 1s

---

## 附录：信息架构最终图

```
PairProxy Dashboard v2.20+
│
├─ 概览 (overview.html)
│  ├─ 日统计卡片
│  ├─ 趋势图表
│  ├─ [新] 运维快速操作面板
│  └─ Top 5 用户
│
├─ 用户 (users.html)
│  ├─ 用户列表 CRUD
│  ├─ 分组管理
│  └─ Token 吊销
│
├─ 分组 (groups.html)
│  ├─ 分组列表 CRD
│  └─ 配额设置（日/月/RPM/并发）
│
├─ LLM 管理 (llm.html) [扩展为 3-Tab]
│  ├─ Tab1: LLM 目标
│  │  ├─ 排水控制条幅
│  │  ├─ 目标列表 (URL / 供应商 / 权重 / 状态)
│  │  └─ CRUD 操作
│  │
│  ├─ Tab2: 目标集 [新增]
│  │  ├─ 左栏：目标集列表
│  │  ├─ 右栏：选中集的成员列表（权重 / 优先级 / 健康状态）
│  │  ├─ 新建 / 编辑 / 删除目标集
│  │  └─ 添加 / 移除 / 修改成员
│  │
│  └─ Tab3: 绑定关系 [迁移]
│     ├─ User→Target 绑定列表
│     └─ CRUD 操作
│
├─ 告警 (alerts.html) [扩展为 3-Tab]
│  ├─ Tab1: 实时事件流 [保留]
│  │  └─ SSE 推送实时告警事件（500 条容量）
│  │
│  ├─ Tab2: 活跃告警 [新增]
│  │  ├─ 数据源：TargetAlertRepo (resolved_at IS NULL)
│  │  ├─ 汇总卡片（按目标聚合故障率）
│  │  ├─ 批量解决操作
│  │  └─ 按级别筛选
│  │
│  └─ Tab3: 历史查询 [新增]
│     ├─ 时间范围选择 (7/30/90 天)
│     ├─ 目标筛选
│     ├─ 分页表格 (50 条/页)
│     └─ 导出选项 (可选)
│
├─ 日志 (logs.html)
│  ├─ 请求日志查询 / 过滤
│  └─ 导出 / 清空
│
├─ 审计 (audit.html)
│  └─ 管理操作审计日志（只读）
│
├─ 批量导入 (import.html)
│  ├─ 用户导入
│  ├─ 分组导入
│  └─ Dry-run 预览
│
└─ 我的用量 (my-usage.html)
   ├─ 用户流量统计
   ├─ 配额进度条
   └─ 用量历史图表
```

---

## 总结与建议

### 核心建议

1. **立即启动 Phase 1 和 Phase 2**（3-4 天）
   - Group-Target Set UI 是 v2.20 的核心缺失
   - Alert 批量解决满足常见运维需求

2. **采用 Tab 内嵌方案**，保持导航简洁

3. **不引入新前端框架**，保持 SSR + 原生 JS 的轻量特性

4. **并行处理**：Phase 1 与 Phase 2 可双轨进行（不同文件，无冲突）

5. **v2.20.0 发布前**交付 P0 + P1 功能

### 预期效果

实施完成后，管理员可在 WebUI 上完成：

✅ Group-Target Set 的完整生命周期管理（创建/编辑/删除/成员管理）
✅ 实时告警查看、批量解决、历史审计
✅ LLM 目标健康状态可视化
✅ 一站式运维仪表板（快速操作面板）

**所有操作实时生效，无需重启网关，完全满足管理员需求**。

---

**文档维护者**：Claude Code
**最后更新**：2025-03-
**相关 Issue**：v2.20 WebUI 最小集扩展计划
