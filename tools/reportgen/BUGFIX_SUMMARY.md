# Reportgen Bug 修复总结 (v2.24.4)

## 修复日期: 2026-04-09

---

## Bug 1: Anthropic 路由错误

### 问题
`insights_llm.go` 中判断 Anthropic 路由的条件为：
```go
target.Provider == "anthropic" && target.Model == ""
```
当 `llm_targets` 表记录中同时设置了 `provider=anthropic` 和 `model` 字段时，条件不成立，流量错误地走 OpenAI Chat Completions 协议（`/v1/chat/completions`），导致 Anthropic API 报错。

### 修复
```go
// 修复前
if target.Provider == "anthropic" && target.Model == "" {

// 修复后
if target.Provider == "anthropic" {
```

### 测试
- `TestLLMRoutingAnthropic` — 有 model 字段时仍走 `/v1/messages`
- `TestLLMRoutingAnthropicNoModel` — 无 model 字段时也走 `/v1/messages`

---

## Bug 2: 无效 Anthropic 模型 ID

### 问题
默认模型 ID 为 `claude-haiku-4-5-20251001`，该 ID 不存在，Anthropic API 返回 404。

### 修复
```go
// 修复前
const defaultAnthropicModel = "claude-haiku-4-5-20251001"

// 修复后
const defaultAnthropicModel = "claude-haiku-4-5"
```

### 测试
- `TestLLMRoutingAnthropicNoModel` — 验证请求体中 model 字段为 `claude-haiku-4-5`

---

## Bug 3: 空库 NULL 导致 Scan 崩溃

### 问题
SQLite 中 `SUM(CASE WHEN ... END)` 在表为空或无匹配行时返回 NULL，Go 的 `rows.Scan` 将 NULL 赋值给 `int64` 会 panic 或返回错误，导致报告生成中断。

### 影响范围
`queries.go` 中以下查询：
- KPI 主查询（`error_count`, `stream_cnt`）
- KPI 前期对比查询
- 每日趋势查询
- 流式比率查询

### 修复
```sql
-- 修复前
SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END) AS error_count

-- 修复后
COALESCE(SUM(CASE WHEN status_code NOT IN (200,201,204) THEN 1 ELSE 0 END), 0) AS error_count
```

### 测试
- `TestQueryKPIEmptyDB` — 空库时所有字段返回 0，不报错
- `TestQueryKPIWithData` — 2 行数据，1 个错误，计数正确
- `TestQueryKPIZeroTokenRequests` — 零 token 401 行处理正确

---

## Bug 4: HTTP 413 不触发上下文重试

### 问题
`isContextTooLong()` 函数只检查 HTTP 400 响应体中的关键词，未处理 HTTP 413（Request Entity Too Large）。当 LLM 网关返回 413 时，reportgen 不重试裁剪后的请求，直接放弃 LLM 洞察。

### 修复
```go
func isContextTooLong(err error) bool {
    var le *llmError
    if !errors.As(err, &le) {
        return false
    }
    if le.status == 413 {  // 新增
        return true
    }
    // ... 原有 400 关键词检查
}
```

### 测试
- `TestIsContextTooLong/HTTP_413` — 413 返回 true
- `TestLLMContextTooLongRetry` — 413 触发两次请求（第一次完整数据，第二次裁剪后数据）

---

## Bug 5: 死图表 — initIORatio 无对应 div

### 问题
`init()` 函数调用了 `initIORatio()`，但 HTML 模板中不存在 `chart-io-ratio` div，导致 ECharts 初始化失败，图表区域显示空白。

### 修复
从 `init()` 中移除 `initIORatio()` 调用；同时将 `initUpstreamShare()` 加入 `init()` 并在模板中补充对应的 `chart-upstream-share` div。

---

## Bug 6: chart-upstream-share 未接入

### 问题
`initUpstreamShare()` 函数已实现（渲染上游请求占比饼图），但：
1. HTML 模板中无对应 div
2. `init()` 中未调用该函数

因此图表完全不显示。

### 修复
在 HTML 模板 Row 3 改为 3 列 Grid 布局，新增 `chart-upstream-share` div：
```html
<div class="grid" style="grid-template-columns:1fr 1fr 1fr">
  <div class="card"><h3>🤖 模型使用分布</h3><div id="chart-model-dist" ...></div></div>
  <div class="card"><h3>🌐 上游端点状态</h3><div id="chart-upstream" ...></div></div>
  <div class="card"><h3>🥧 上游请求占比</h3><div id="chart-upstream-share" ...></div></div>
</div>
```
并在 `init()` 中添加 `initUpstreamShare()` 调用。

---

## Bug 7: stickness 拼写错误

### 问题
`initStickinessGauge()` 中读取数据字段名为 `D.engagement.stickness`（拼写错误），而 Go 后端输出的 JSON 字段名为 `stickiness`，导致仪表盘始终显示 0。

### 修复
```js
// 修复前
const val = D.engagement.stickness * 100;

// 修复后
const raw = D.engagement.stickiness ?? D.engagement.stickness;
if (raw == null) return;
const val = raw * 100;
```
使用 `??` 兼容 fallback，同时增加 null guard 防止 `0 * 100` 时意外渲染。

---

## Bug 8: mockdata schema 不完整

### 问题
`cmd/mockdata/main.go` 生成的 `users` 表缺少 `daily_limit` / `monthly_limit` 列，导致配额查询 SQL 报列不存在错误。

### 修复
```sql
-- 修复前
CREATE TABLE users (id, username, group_id, ...)

-- 修复后
CREATE TABLE users (id, username, group_id, ...,
  daily_limit INTEGER DEFAULT 0,
  monthly_limit INTEGER DEFAULT 0
)
```
同时 `model` 字段改存友好名称（如 `claude-haiku-4-5`）而非原始 URL，新增 20 条零 token 401 边界数据和 10 条 NULL model 边界数据。

---

## 修复文件汇总

| 文件 | 修改内容 |
|------|---------|
| `insights_llm.go` | 路由判断去掉 `&& target.Model == ""` |
| `insights_llm.go` | 默认模型改为 `claude-haiku-4-5` |
| `insights_llm.go` | `isContextTooLong` 新增 HTTP 413 分支 |
| `queries.go` | 所有 `SUM(CASE WHEN...)` 包 `COALESCE(..., 0)` |
| `templates/report.html` | 移除 `initIORatio()` 调用 |
| `templates/report.html` | 新增 `chart-upstream-share` div，接入 `initUpstreamShare()` |
| `templates/report.html` | `stickness` → `stickiness`，增加 null guard |
| `templates/report.html` | `initAdoptionRate`、`initTopUsersByRequest` 增加数值默认值 |
| `cmd/mockdata/main.go` | 补全 `daily_limit`/`monthly_limit`，修正 model 字段 |
| `integration_test.go` | 新增 32 个测试用例（新建文件） |

---

## 测试结果

```
ok  github.com/l17728/pairproxy/tools/reportgen  0.648s
```

全部 32 个用例通过，0 失败。
