# 报告模板变更记录 (v2.24.4)

## 当前模板状态 (templates/report.html)

### 图表布局

#### Row 1: KPI 概览
4 张 KPI 卡片：总请求数、活跃用户、平均延迟、总成本（含环比变化）

#### Row 2: 趋势图（2列）
- Token 用量趋势（折线）
- 费用趋势（折线）

#### Row 3: 模型分布 + 上游状态 + 上游占比（3列）
- 🤖 模型使用分布（饼图）`chart-model-dist`
- 🌐 上游端点状态（条形）`chart-upstream`
- 🥧 上游请求占比（饼图）`chart-upstream-share` ✨ **v2.24.4 新增**

#### Row 4: 每日趋势（折线组合）
- 每日请求数 / 模型每日用量堆叠面积

#### Row 5: 延迟分析（3列）
- 延迟分布直方图 `chart-latency-hist`
- 上游延迟箱线图 `chart-upstream-latency`
- 延迟百分位趋势 `chart-latency-pct-trend`

#### Row 6: 热力图（2列）
- 请求热力图（时段×星期）
- Token 吞吐热力图

#### Row 7: 用户分析
- TOP10 用户 Token / 费用 / 请求数（3列）
- 用户分层分布 + Token 百分位（2列）
- 用户请求数箱线图 + 帕累托图（2列）

#### Row 8: 高级分析
- I/O Token 散点图
- 延迟 vs Token 散点图
- 用户请求频次分布
- 分组对比

#### Row 9: 参与度 & 留存
- DAU/WAU/MAU 趋势
- DAU/MAU 粘性指数（仪表盘）`chart-stickiness-gauge`
- 用户留存曲线

#### Row 10: 管理视角
- 模型能力雷达图
- 用户采纳率
- 配额使用表格

#### Row 11: 请求明细
- 错误请求明细表（可按状态码/模型过滤）
- 慢请求 TOP10

#### Row 12: AI 洞察
- 🤖 AI 智能洞察面板（LLM 生成或规则降级）

---

## v2.24.4 变更内容

### 新增
- `chart-upstream-share` div（上游请求占比饼图），Row 3 改为 3 列 Grid 布局
- `initUpstreamShare()` 加入 `init()` 调用链

### 修复
- 移除 `initIORatio()` 在 `init()` 中的调用（对应 div 不存在，造成死图表）
- `initStickinessGauge()`：`stickness` → `stickiness`，增加 null guard
- `initAdoptionRate()`：`adoption_percent` 增加 `||0` 默认值，防止 undefined
- `initTopUsersByRequest()`：`cost_usd` 增加 `||0` 默认值

### init() 调用链（当前完整列表）
```js
function init() {
  initKPI();
  initDailyTrend();
  initModelDist();
  initUpstream();
  initUpstreamShare();       // ✨ v2.24.4 新增
  initHeatmap();
  initTokenHeatmap();
  initTopUsers();
  initUserTier();
  initTokenPercentile();
  initTopUsersByRequest();
  initUserRequestBoxplot();
  initPareto();
  initModelRadar();
  initAdoptionRate();
  initStickinessGauge();
  initRetention();
  initIOScatter();
  initLatencyScatter();
  initLatencyHist();
  initUpstreamLatency();
  initLatencyPctTrend();
  initDailyLatency();
  initFreqBucket();
  initIOBucket();
  initModelDailyTrend();
  initUpstreamLatencyTrend();
  initUpstreamShare();
  initGroupTokenBoxplot();
  initGroupCompare();
  initEngagementTrend();
  initQuotaTable();
  initModelCostBar();
  initUserTierPie();
  initInsights();
}
```

---

## IP/URL 隐私处理

以下图表对上游 URL 进行了脱敏处理，显示为"上游-1"、"上游-2"等序号标签：
- `initUpstream()` — 上游端点状态条形图
- `initUpstreamLatency()` — 上游延迟箱线图（显示模型名）
- `initUpstreamLatencyTrend()` — 上游延迟趋势多线图

错误请求明细表和慢请求表均已删除"上游"列。
