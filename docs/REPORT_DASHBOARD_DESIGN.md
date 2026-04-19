# PairProxy 分析报告大屏设计文档

> 版本: v3.0.0 | 日期: 2026-04-19  
> 状态: 已完成 | 最后更新: 2026-04-19

---

## 一、输出格式选择：为什么是「单个自包含 HTML 文件」

| 格式 | 优点 | 缺点 | 适合场景 |
|------|------|------|----------|
| **Markdown** | 轻量、Git 友好 | 无法渲染图表，只能贴图 | 文字报告 |
| **多文件 HTML** | 完整功能 | 传播需打包，文件多 | Web 应用 |
| **PDF** | 打印友好、正式 | 交互性差、生成链路长 | 归档、打印 |
| **单个 HTML（内嵌数据+JS）** ✅ | 一个文件即可交互查看、零依赖、可直接邮件传播 | 文件稍大（~200-500KB） | **周期性报告分发** |

**最终方案**：Go 程序查询数据库 → 将全部聚合数据 JSON 内嵌到 HTML 模板中 → ECharts（CDN 或 inline）渲染图表 → 输出单个 `.html` 文件。

- 打开即用：浏览器双击打开，无需服务器
- 传播方便：一个文件通过邮件/IM/网盘发送
- 交互完整：悬浮提示、缩放、数据筛选全部可用
- 离线可用：ECharts 和 Tailwind 可选择内联（无 CDN 依赖版本）

---

## 二、数据源：usage_logs 表完整字段

```
usage_logs
├── id              uint        主键（自增）
├── request_id      string      唯一请求 ID
├── user_id         string      用户 ID → users.id
├── model           string      LLM 模型名称（如 claude-3-opus, gpt-4o）
├── input_tokens    int         输入 Token 数
├── output_tokens   int         输出 Token 数
├── total_tokens    int         总 Token 数
├── is_streaming    bool        是否流式请求
├── upstream_url    string      实际上游 LLM 端点 URL
├── status_code     int         HTTP 响应状态码
├── duration_ms     int64       请求耗时（毫秒）
├── cost_usd        float64     估算费用（USD）
├── source_node     string      来源节点标识
├── synced          bool        是否已同步到主节点
└── created_at      time.Time   请求时间（含时分秒）

关联表:
users        → id, username, group_id, is_active, last_login_at
groups       → id, name, daily_token_limit, monthly_token_limit
llm_targets  → id, url, provider, name, weight, is_active
```

**可计算派生指标**：

| 派生指标 | 计算方式 | 分析价值 |
|----------|----------|----------|
| input/output 比率 | `input_tokens / NULLIF(output_tokens, 0)` | 问题复杂度、Prompt 效率 |
| 每请求平均 Token | `SUM(tokens) / COUNT(*)` | 用量密度 |
| 每日活跃用户 DAU | `COUNT(DISTINCT user_id) WHERE DATE(created_at) = today` | 平台活跃度 |
| 成功率 | `SUM(status_code=200) / COUNT(*)` | 服务质量 |
| 流式占比 | `SUM(is_streaming=true) / COUNT(*)` | 使用模式 |
| 小时级请求密度 | `GROUP BY HOUR(created_at)` | 峰谷规律 |

---

## 三、三视角指标体系

### 3.1 👤 使用者视角（User Perspective）

> 使用者关心：我用了多少、花多少钱、什么时间用的、和别人比怎样

#### 3.1.1 个人用量总览

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| 周期总 Token 用量 | 数字卡片（输入/输出/总计） | `SUM(input_tokens), SUM(output_tokens)` WHERE user_id=? |
| 周期总费用 | 数字卡片（USD） | `SUM(cost_usd)` WHERE user_id=? |
| 周期请求次数 | 数字卡片 | `COUNT(*)` WHERE user_id=? |
| 日配额使用率 | 进度条 | 已用 / 分组日配额上限 |
| 月配额使用率 | 进度条 | 已用 / 分组月配额上限 |

#### 3.1.2 个人使用趋势

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| 每日 Token 趋势 | 堆叠面积图（Input 蓝 + Output 绿） | `GROUP BY DATE(created_at)`, SUM(input), SUM(output) |
| 每日请求次数趋势 | 折线图 | `GROUP BY DATE(created_at)`, COUNT(*) |
| 每日费用趋势 | 折线图 + 面积填充 | `GROUP BY DATE(created_at)`, SUM(cost_usd) |
| 每小时使用热力图 | **热力图**（24h × 7天矩阵） | `GROUP BY HOUR(created_at), DAYOFWEEK(created_at)` |

#### 3.1.3 个人模型偏好

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| 模型使用分布 | 饼图 / 环形图 | `GROUP BY model`, COUNT(*) |
| 各模型 Token 消耗 | 堆叠柱状图 | `GROUP BY model`, SUM(input), SUM(output) |
| 各模型费用占比 | 环形图 | `GROUP BY model`, SUM(cost_usd) |

#### 3.1.4 个人排名

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| Token 用量排名 | 横向柱状图 + 高亮自己 | `RANK() OVER (ORDER BY SUM(tokens) DESC)` |
| 请求次数排名 | 横向柱状图 + 高亮自己 | `RANK() OVER (ORDER BY COUNT(*) DESC)` |
| 费用排名 | 横向柱状图 + 高亮自己 | `RANK() OVER (ORDER BY SUM(cost_usd) DESC)` |

---

### 3.2 🔧 运维者视角（Ops Perspective）

> 运维者关心：系统健康吗、流量多大、要不要扩容、有没有异常

#### 3.2.1 流量与载荷（RED 方法 — Rate / Errors / Duration）

| 指标 | 可视化 | SQL 核心逻辑 | 运维价值 |
|------|--------|-------------|----------|
| **请求速率 (Rate)** | 实时折线图（每5分钟粒度） | `COUNT(*) GROUP BY FLOOR(UNIX_TIMESTAMP(created_at)/300)` | 识别流量峰谷 |
| **错误率 (Errors)** | 面积图（红色着色） | `SUM(status_code!=200)/COUNT(*)` | 服务降级预警 |
| **延迟分布 (Duration)** | **箱线图**（按小时分组） | 见下方 SQL | 性能基线监控 |
| **P50/P95/P99 延迟** | 多线折线图 | 百分位函数 | SLA 达标判定 |
| **P50/P95/P99 延迟趋势** | 折线图（3条线叠加） | 按日计算百分位 | 延迟退化检测 |

**箱线图 (Box Plot)** — 你说的「方形加上下线段」就是它：

```
         ┌─── 最大值 (Whisker)
         │
    ┌────┤
    │    │  ← Q3 (75th percentile)
    │    │
    │────│  ← 中位数 (Median)
    │    │
    │    │  ← Q1 (25th percentile)
    └────┤
         │
         └─── 最小值 (Whisker)
              ○  离群值 (Outlier)
```

箱线图在运维中的价值：
- **中位数**：典型延迟是多少
- **箱体高度 (IQR)**：延迟波动幅度，越高越不稳定
- **上须 (Whisker)**：最坏情况的正常边界
- **离群点 (Outlier)**：超常慢请求，需要排查
- 按模型/小时/上游分组对比，一眼看出哪个环节有问题

#### 3.2.2 请求分布与流量模式

| 指标 | 可视化 | SQL 核心逻辑 | 运维价值 |
|------|--------|-------------|----------|
| **24h 请求热力图** | **热力图**（X=小时, Y=日期） | `GROUP BY DATE(created_at), HOUR(created_at)` | 找到高峰时段、规划扩容 |
| **24h Token 吞吐热力图** | **热力图**（颜色深浅=Token量） | 同上, SUM(tokens) | 资源消耗时空分布 |
| **星期×小时热力图** | **热力图**（7行×24列） | `DAYOFWEEK + HOUR` | 发现工作日vs周末规律 |
| 请求量按上游分布 | 堆叠面积图 | `GROUP BY upstream_url, DATE` | 负载均衡效果评估 |
| 流式 vs 非流式比例 | 堆叠柱状图 | `GROUP BY is_streaming` | 协议分布感知 |
| 按状态码分布 | 堆叠柱状图 | `GROUP BY status_code` | 错误类型识别 |

#### 3.2.3 上游端点健康

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| 各上游平均延迟 | 柱状图 | `GROUP BY upstream_url`, AVG(duration_ms) |
| 各上游错误率 | 柱状图（红色着色） | `GROUP BY upstream_url`, error_rate |
| 各上游流量占比 | 饼图 | `GROUP BY upstream_url`, COUNT(*) |
| 上游延迟趋势 | 多线折线图 | `GROUP BY upstream_url, DATE`, AVG(duration_ms) |

#### 3.2.4 系统容量评估

| 指标 | 可视化 | SQL 核心逻辑 | 运维价值 |
|------|--------|-------------|----------|
| 峰值 RPM（每分钟请求数） | 数字卡片 + 时间点 | `COUNT(*)/60` 按分钟粒度最大值 | 扩容基线 |
| 平均 RPM | 数字卡片 | `COUNT(*) / 周期分钟数` | 日常基线 |
| 数据库增长速率 | 折线图 | `COUNT(*) GROUP BY DATE` | 存储容量规划 |
| 节点流量分布 | 饼图 | `GROUP BY source_node` | 集群负载均衡 |

---

### 3.3 📊 管理者视角（Admin Perspective）

> 管理者关心：谁在用谁没用、使用效率、成本控制、投资回报

#### 3.3.1 用户活跃度与参与度

| 指标 | 可视化 | SQL 核心逻辑 | 管理价值 |
|------|--------|-------------|----------|
| **DAU/WAU/MAU** | 数字卡片 + 趋势折线 | `COUNT(DISTINCT user_id)` 按日/周/月 | 平台活跃度脉搏 |
| **DAU/MAU 比率（粘性系数）** | 仪表盘 | DAU÷MAU | <20%说明用户不常回来 |
| 活跃用户 vs 注册用户比例 | 双环形图 | 活跃/总数 | 采纳率（Adoption） |
| **用户使用频次直方图** | **直方图** | 见下方说明 | 用户分层 |
| 新用户首次使用时间 | 折线图 | MIN(created_at) per user | 推广效果评估 |
| 用户留存（7日/30日） | 留存曲线图 | 同期群分析 | 用户粘性趋势 |

**用户使用频次直方图** 是管理者最核心的分析之一：

```
频次
  │
20│ ██
  │ ██ ██
15│ ██ ██
  │ ██ ██ ██
10│ ██ ██ ██ ██
  │ ██ ██ ██ ██ ██
 5│ ██ ██ ██ ██ ██ ██
  │ ██ ██ ██ ██ ██ ██ ██
  └────────────────────────────
    0  1-5 6-20 21-50 51-100 100+
          请求次数区间

    │← 不活跃 →│← 普通 →│← 活跃 →│← 重度 →│
```

这个直方图能一眼看出：
- **左侧高耸** = 大量低频用户（不活跃，需要推动）
- **右侧拖尾** = 少量重度用户（Power Users，可能需要关注成本）
- **中间集中** = 健康的用户分布

#### 3.3.2 TOP N 用户分析

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| TOP10 用户（Token 用量） | 横向柱状图 | `GROUP BY user_id ORDER BY SUM(tokens) DESC LIMIT 10` |
| TOP10 用户（请求次数） | 横向柱状图 | `GROUP BY user_id ORDER BY COUNT(*) DESC LIMIT 10` |
| TOP10 用户（费用） | 横向柱状图 | `GROUP BY user_id ORDER BY SUM(cost_usd) DESC LIMIT 10` |
| TOP10 用户（平均延迟） | 横向柱状图 | `GROUP BY user_id ORDER BY AVG(duration_ms) DESC LIMIT 10` |
| **帕累托图（80/20）** | **帕累托图**（柱状+累积%折线） | 累积百分比计算 | 少数用户是否贡献了大部分用量 |

**帕累托图示例**：

```
用量  ── 累积%
  │
  │██                          ────────── 100%
  │██ ██                   ────            80%
  │██ ██ ██            ────
  │██ ██ ██ ██     ────                   ← 3个用户贡献72%
  │██ ██ ██ ██ ██ ────
  └──────────────────────
   Alice Bob  Carol Dave Eve ...
```

#### 3.3.3 分组（Group/Team）对比

| 指标 | 可视化 | SQL 核心逻辑 |
|------|--------|-------------|
| 各组 Token 用量对比 | 柱状图 | JOIN users→groups, `GROUP BY group_id` |
| 各组费用对比 | 柱状图 | 同上, SUM(cost_usd) |
| 各组活跃用户数 | 柱状图 | `COUNT(DISTINCT user_id) GROUP BY group_id` |
| 各组配额利用率 | 分组进度条 | 已用 / 配额上限 |
| **组间用量箱线图对比** | **分组箱线图** | 按组计算每个用户的用量，画箱线图 | 哪个组内部差异大 |

#### 3.3.4 成本分析与效率

| 指标 | 可视化 | SQL 核心逻辑 | 管理价值 |
|------|--------|-------------|----------|
| 总费用及趋势 | 面积图 | `SUM(cost_usd) GROUP BY DATE` | 预算管控 |
| **人均成本** | 数字卡片 + 趋势线 | 总费用 / 活跃用户数 | 投资回报率 |
| 模型费用占比 | 环形图 | `GROUP BY model`, SUM(cost_usd) | 高价模型使用是否合理 |
| 费用预测 | 虚线外推 | 月初至今日日均 × 剩余天数 | 是否超预算 |
| 单 Token 成本趋势 | 折线图 | SUM(cost)/SUM(tokens) per day | 定价效率 |

---

### 3.4 🔬 深度分析视角（Advanced Analytics）

> 你提到的 Input/Output 分布、响应时间分析、业务特征挖掘

#### 3.4.1 Input/Output Token 分析

| 指标 | 可视化 | SQL 核心逻辑 | 分析价值 |
|------|--------|-------------|----------|
| **Input/Output 比率分布** | **直方图** | 计算每次请求的 i/o ratio, 分桶 | 业务特征洞察 |
| **Input Token 箱线图**（按模型） | **分组箱线图** | 按 model 分组, input_tokens 箱线图 | 各模型的 Prompt 复杂度 |
| **Output Token 箱线图**（按模型） | **分组箱线图** | 按 model 分组, output_tokens 箱线图 | 各模型的回答丰富度 |
| Input vs Output **散点图** | **散点图** | 每次请求的 (input, output) 点 | 问题大小与答案大小的相关性 |
| **平均 I/O 比率趋势** | 折线图 | 按日计算 AVG(input/output) | Prompt 优化效果追踪 |

**Input/Output 比率分析的业务含义**：

```
I/O 比率   含义                     典型场景
──────────────────────────────────────────────────────
> 10:1     问题很长，回答很短         文档摘要、分类、提取
3:1~10:1   正常对话，上下文较多       多轮对话、代码补全
1:1~3:1    平衡的问答               翻译、解释、一般对话
< 1:1      简短提问，长篇输出         写文章、生成报告、翻译长文
```

**Input 大 → 问题复杂（需要大量上下文），Output 大 → 生成任务重**。
管理者可以通过 I/O 比率分布判断团队在做什么类型的任务。

#### 3.4.2 响应时间（延迟）深度分析

| 指标 | 可视化 | SQL 核心逻辑 | 分析价值 |
|------|--------|-------------|----------|
| **延迟直方图** | **直方图** | duration_ms 分桶计数 | 延迟整体分布形态 |
| **延迟箱线图**（按模型） | **分组箱线图** | 按 model, 计算 Q1/Median/Q3/Whiskers/Outliers | 哪个模型慢、波动大 |
| **延迟箱线图**（按上游） | **分组箱线图** | 按 upstream_url | 哪个端点需要优化 |
| **延迟趋势（P50/P95/P99）** | 多线折线图 | 按日计算百分位 | 性能退化检测 |
| **慢请求 TOP10** | 表格 | `ORDER BY duration_ms DESC LIMIT 10` | 具体慢在哪里 |
| **延迟 vs Token 散点图** | **散点图** | (total_tokens, duration_ms) | Token 数与延迟的相关性 |
| 流式 vs 非流式延迟对比 | 并列箱线图 | 按 is_streaming 分组 | 流式是否真的更快（首字节） |

#### 3.4.3 模型使用深度分析

| 指标 | 可视化 | SQL 核心逻辑 | 分析价值 |
|------|--------|-------------|----------|
| 模型使用趋势（堆叠面积） | **堆叠面积图** | `GROUP BY model, DATE` COUNT(*) | 模型切换趋势 |
| 模型 Token 消耗对比 | 堆叠柱状图 | `GROUP BY model` SUM(input), SUM(output) | 各模型资源消耗 |
| 模型成本效率对比 | 柱状图 | `SUM(cost_usd) / SUM(tokens)` per model | 每模型单价对比 |
| 模型错误率对比 | 柱状图 | error_rate per model | 模型可用性 |
| **模型能力雷达图** | **雷达图** | 各模型在 5 个维度归一化评分 | 一图对比所有模型 |

**模型能力雷达图维度**：
```
          延迟表现
            ↑
     成本效率 ←→ 吞吐量
            ↓
          可靠性  ←→  用户采纳度
```

---

## 四、完整可视化图形清单

综合上述所有指标，以下是全部需要的可视化图形类型：

| # | 图形类型 | 用途 | 出现次数 |
|---|---------|------|---------|
| 1 | **数字卡片 (KPI Card)** | 核心指标一目了然 | 15+ |
| 2 | **折线图 (Line)** | 趋势追踪 | 8 |
| 3 | **面积图 (Area)** | 累积趋势、总量感知 | 4 |
| 4 | **堆叠面积图 (Stacked Area)** | 多模型趋势对比 | 2 |
| 5 | **柱状图 (Bar)** | 分类对比 | 6 |
| 6 | **堆叠柱状图 (Stacked Bar)** | 构成分析 | 4 |
| 7 | **横向柱状图 (Horizontal Bar)** | TOP N 排名 | 4 |
| 8 | **饼图/环形图 (Pie/Doughnut)** | 占比分布 | 5 |
| 9 | **热力图 (Heatmap)** | 时间×维度矩阵 | 3 |
| 10 | **箱线图 (Box Plot)** | 分布+离群值检测 | 6 |
| 11 | **直方图 (Histogram)** | 频次分布 | 4 |
| 12 | **散点图 (Scatter)** | 相关性分析 | 2 |
| 13 | **帕累托图 (Pareto)** | 80/20 分析 | 1 |
| 14 | **雷达图 (Radar)** | 多维度对比 | 1 |
| 15 | **进度条 (Progress)** | 配额利用率 | 2 |
| 16 | **表格 (Table)** | 明细数据 | 3 |
| 17 | **留存曲线 (Retention)** | 用户留存 | 1 |
| 18 | **仪表盘 (Gauge)** | 单一比率指标 | 2 |

---

## 五、智能洞察（AI Insights）— 文字分析面板

> 除了图表，每个报告还需要自动生成的文字洞察

### 5.1 环比变化分析

```
📈 本周关键指标变化：
• 总请求量：12,456 次（↑ 23.5% vs 上周）
• 总 Token 用量：8.2M（↑ 18.2%）
• 总费用：$127.40（↑ 15.8%）
• 活跃用户：42 人（↑ 5 人）
• 平均延迟：1,234ms（↓ 8.3% 改善）

增长主要来自：用户 alice（+3,200次请求）、bob（+1,800次请求）
```

### 5.2 异常检测

```
⚠️ 异常检测：
• 3月15日 14:00-16:00 用量突增至日均的 3.2 倍
  主要贡献者：alice（占 67%），建议确认是否为正常业务需求
• 用户 carol 在 3月12日 连续发送 890 次请求（日均仅 30 次）
  可能存在自动化脚本异常调用
```

### 5.3 成本预警

```
💰 成本预警：
• 按当前趋势，本月费用预计 $523，超月度预算 $450 约 16%
• 主要超支来源：claude-3-opus（占 62% 费用，仅占 15% 请求量）
• 建议：检查是否有 claude-3-opus → claude-3-sonnet 的替代空间（预估可节省 70%）
```

### 5.4 用户参与度洞察

```
👥 用户参与度：
• 本月活跃用户 42 / 注册用户 78（采纳率 53.8%）
• DAU/MAU = 28/42 = 66.7%（粘性良好）
• TOP 3 用户贡献 72% 用量（帕累托效应显著）
• 36 名用户（46%）本月零使用，建议推动培训
• 新用户 dave 首周即成为 TOP10 用户，高潜力用户
```

### 5.5 效率建议

```
🎯 效率优化建议：
• 平均 I/O 比率 5.2:1，高于行业平均，提示 Prompt 可精简
• 用户 eve 的平均输入 Token 为 12,500（全局平均 3,200）
  建议审查是否有冗余上下文传入
• 流式请求占比 87%，非流式请求延迟高出 2.3x
  建议全面切换流式模式
```

---

## 六、报告页面布局设计

### 单个 HTML 文件的页面结构

```
┌─────────────────────────────────────────────────────────────┐
│  📊 PairProxy 周报 2026-W14 (4/1-4/7)          [切换视角 ▾] │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐    │
│  │总请求│ │总Token│ │总费用│ │活跃用户│ │错误率│ │平均延迟│   │
│  │12,456│ │8.2M  │ │$127  │ │ 42   │ │0.8% │ │1234ms│    │
│  │↑23.5%│ │↑18.2%│ │↑15.8%│ │↑5   │ │↓0.2%│ │↓8.3% │    │
│  └──────┘ └──────┘ └──────┘ └──────┘ └──────┘ └──────┘    │
│                                                             │
├─────────────────────────────┬───────────────────────────────┤
│                             │                               │
│   Token 用量趋势            │   费用趋势 (USD)              │
│   (堆叠面积图)              │   (面积折线图)                │
│   ████████                  │   ⋯⋯⋯⋯⋯⋯                    │
│   ████████████              │   ⋯⋯⋯⋯⋯⋯⋯⋯                  │
│                             │                               │
├─────────────────────────────┬───────────────────────────────┤
│                             │                               │
│   24h×7天 请求热力图         │   TOP10 用户 Token 用量       │
│   (热力图矩阵)              │   (横向柱状图)                │
│   ░░▒▒▓▓██▓▓▒▒░░           │   alice ████████████  2.1M   │
│   ░▒▒▓▓██▓▓▓▒▒░░           │   bob   ██████████    1.8M   │
│                             │   carol ████████      1.2M   │
├─────────────────────────────┼───────────────────────────────┤
│                             │                               │
│   模型使用分布               │   延迟分布                    │
│   (环形图)                  │   (箱线图 × 模型)             │
│      ╭───╮                  │   ┌─┐                        │
│     ╱     ╲                 │   ├─┼─┐   ┌─┐               │
│    │ ● ● ● │                │   └─┘ └─┬─┘ └──○            │
│     ╲     ╱                 │   opus  sonnet  haiku         │
│      ╰───╯                  │                               │
├─────────────────────────────┴───────────────────────────────┤
│                                                             │
│  Input/Output Token 分布分析                                │
│  ┌──────────────────┐  ┌──────────────────┐                │
│  │ I/O 比率直方图    │  │ Input vs Output  │                │
│  │ (直方图)          │  │ (散点图)          │                │
│  │ ██                │  │     ·  ·         │                │
│  │ ██ ██             │  │   · ··· ·        │                │
│  │ ██ ██ ██          │  │  ········        │                │
│  └──────────────────┘  └──────────────────┘                │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  📝 智能洞察                                                │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ 📈 环比变化：本周总用量↑23.5%，主要贡献来自 alice、bob   ││
│  │ ⚠️ 异常检测：3月15日用量突增至日均3.2倍                   ││
│  │ 💰 成本预警：本月预计$523，超预算16%                      ││
│  │ 👥 参与度：46%用户零使用，建议推动培训                     ││
│  │ 🎯 优化建议：eve的输入Token远超平均，建议审查Prompt       ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
├─────────────────────────────────────────────────────────────┤
│  📊 分组对比                                                │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ 各组 Token 用量  │ 各组费用对比  │ 各组活跃用户          ││
│  │ (柱状图)         │ (柱状图)      │ (柱状图)              ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
├─────────────────────────────────────────────────────────────┤
│  ⏱️ 延迟深度分析                                            │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ P50/P95/P99 延迟趋势   │ 流式 vs 非流式延迟对比          ││
│  │ (多线折线图)            │ (并列箱线图)                    ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
├─────────────────────────────────────────────────────────────┤
│  🏆 用户频次分布                                            │
│  ┌─────────────────────────────────────────────────────────┐│
│  │ 用户请求频次直方图       │ 帕累托图（累积贡献%）           ││
│  │ (直方图)                 │ (柱状+折线)                    ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
├─────────────────────────────────────────────────────────────┤
│  📋 慢请求 TOP10 (表格)                                     │
│  ┌─────┬──────┬──────┬──────┬─────────┬───────┐            │
│  │时间  │用户   │模型   │Token │延迟(ms) │状态码 │            │
│  ├─────┼──────┼──────┼──────┼─────────┼───────┤            │
│  │...  │...   │...   │...   │...      │...    │            │
│  └─────┴──────┴──────┴──────┴─────────┴───────┘            │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│  报告生成时间: 2026-04-07 00:05:23 | 数据周期: 2026-W14     │
│  PairProxy Report Generator v1.0                            │
└─────────────────────────────────────────────────────────────┘
```

---

## 七、技术架构

### 7.1 程序结构

```
cmd/reportgen/
├── main.go                  # CLI 入口（Cobra 命令）
└── main_test.go

internal/report/
├── generator.go             # 报告生成器主体
├── queries.go               # 所有 SQL 查询函数
├── insights.go              # 智能洞察计算（环比、异常、建议）
├── template.go              # HTML 模板嵌入（embed.FS）
├── types.go                 # 数据结构定义
├── generator_test.go
├── queries_test.go
├── insights_test.go
└── templates/
    └── report.html          # ECharts + Tailwind 模板
```

### 7.2 CLI 命令

#### 基础用法（从数据库配置读取 LLM）
```bash
# 生成周报
./reportgen -db ./pairproxy.db -from 2026-04-01 -to 2026-04-07 -output report-2026-W14.html

# 生成月报
./reportgen -db ./pairproxy.db -from 2026-03-01 -to 2026-03-31 -output report-2026-03.html
```

#### 新增：直接指定 LLM 端点（v2.24.3+）
```bash
# 使用本地 LLM（无需数据库配置）
./reportgen -db ./pairproxy.db -from 2026-04-01 -to 2026-04-07 \
  -llm-url http://localhost:9000 \
  -llm-key "your-api-key" \
  -llm-model gpt-4o-mini

# 指定 Anthropic 端点
./reportgen -db ./pairproxy.db -from 2026-04-01 -to 2026-04-07 \
  -llm-url https://api.anthropic.com \
  -llm-key "sk-ant-xxx" \
  -llm-model claude-haiku-4-5-20251001
```

#### 纯规则分析（跳过 LLM）
```bash
# 不指定任何 LLM 参数时，自动降级为纯规则分析
./reportgen -db ./pairproxy.db -from 2026-04-01 -to 2026-04-07
```

**优先级说明**：
1. 命令行 `-llm-url` 和 `-llm-key` 优先使用
2. 否则从数据库查询 LLM 配置（需 KEY_ENCRYPTION_KEY）
3. 两者均未指定时，降级为纯规则洞察

### 7.3 核心数据流

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   SQLite /   │────→│  SQL 查询层  │────→│  洞察计算层  │────→│  HTML 渲染   │
│  PostgreSQL  │     │ (queries.go) │     │(insights.go) │     │(template.go) │
└─────────────┘     └─────────────┘     └─────────────┘     └─────────────┘
                         │                    │                     │
                    25+ 聚合查询          环比/异常/预测         数据嵌入 JSON
                    5 张表 JOIN          文字洞察生成           ECharts 渲染
                                                              单文件输出
```

### 7.4 ECharts 图形映射

| 图形类型 | ECharts type | 使用场景 |
|----------|-------------|----------|
| 折线图 | `type: 'line'` | 趋势 |
| 面积图 | `type: 'line', areaStyle: {}` | 累积趋势 |
| 堆叠面积图 | `stack: 'total'` | 多模型趋势 |
| 柱状图 | `type: 'bar'` | 分类对比 |
| 堆叠柱状图 | `stack: 'total'` | 构成分析 |
| 横向柱状图 | `xAxis: 'value', yAxis: 'category'` | TOP N |
| 饼图 | `type: 'pie'` | 占比 |
| 环形图 | `type: 'pie', radius: ['40%', '70%']` | 占比（中空） |
| 热力图 | `type: 'heatmap'` | 时间矩阵 |
| 箱线图 | `type: 'boxplot'` | 分布+离群值 |
| 直方图 | `type: 'bar'` + 预处理分桶 | 频次分布 |
| 散点图 | `type: 'scatter'` | 相关性 |
| 雷达图 | `type: 'radar'` | 多维对比 |
| 仪表盘 | `type: 'gauge'` | 比率指标 |

### 7.6 容错机制 (v2.24.3+)

reportgen 包含全面的容错设计，确保即使部分功能失败也能生成报告：

| 故障场景 | 处理方案 | 结果 |
|---------|---------|------|
| 数据库查询失败 | 跳过该查询，继续处理其他数据 | 报告缺少部分图表，但仍可用 |
| LLM 连接失败 | 自动降级到纯规则分析 | 无 AI 智能洞察，仅规则洞察 |
| LLM 返回 HTTP 错误 | 记录错误，跳过 LLM 调用 | 规则洞察完整可用 |
| LLM 调用异常 (panic) | defer/recover 捕获异常 | 不中断主流程 |
| 报告模板缺失 | 使用内置最小 HTML 模板 | 基础报告结构可用 |
| 时间段无数据 | 自动生成"暂无数据"提示 | 空报告但不出错 |

**目标**：0 故障中断，所有失败都优雅降级

### 7.7 LLM API 兼容性 (v2.24.3+)

reportgen 自动识别 LLM 提供商并使用正确的 API：

| Provider | API 端点 | 请求格式 | 认证方式 | 模型示例 |
|----------|---------|---------|---------|----------|
| **OpenAI** | `/v1/chat/completions` | messages | `Authorization: Bearer` | gpt-4o-mini |
| **OpenAI 兼容** | `/v1/chat/completions` | messages | `Authorization: Bearer` | (任何兼容端点) |
| **Anthropic** | `/v1/messages` | messages | `x-api-key` | claude-haiku-4-5 |

**自动判断**：
- 若 provider 被检测为 "anthropic" 且命令行未指定模型 → 使用 `/v1/messages`
- 其他情况 → 默认使用 `/v1/chat/completions` (OpenAI 兼容格式)

#### 7.5.1 箱线图数据（延迟分布按模型）

```sql
-- 先按模型计算百分位点
SELECT
  model,
  MIN(duration_ms) as min_val,
  -- SQLite 没有原生百分位函数，使用子查询模拟
  -- 或在 Go 层排序后取百分位
  AVG(duration_ms) as avg_val,
  MAX(duration_ms) as max_val,
  COUNT(*) as count
FROM usage_logs
WHERE created_at >= ? AND created_at <= ?
GROUP BY model
ORDER BY count DESC;
```

**注意**：SQLite 没有原生 `PERCENTILE()` 函数。两种策略：
1. **Go 层计算**：查询全部 duration_ms 值，排序后在 Go 中计算 Q1/Median/Q3（推荐，简单可靠）
2. **SQL 模拟**：使用 `NTILE(100)` 窗口函数近似计算（复杂但纯 SQL）

#### 7.5.2 热力图数据（24h × 7天请求密度）

```sql
SELECT
  CAST(strftime('%H', created_at) AS INTEGER) as hour,
  CAST(strftime('%w', created_at) AS INTEGER) as day_of_week,
  COUNT(*) as request_count
FROM usage_logs
WHERE created_at >= ? AND created_at <= ?
GROUP BY hour, day_of_week
ORDER BY day_of_week, hour;
```

#### 7.5.3 用户频次分布直方图

```sql
-- 先统计每个用户的请求次数
SELECT user_id, COUNT(*) as req_count
FROM usage_logs
WHERE created_at >= ? AND created_at <= ?
GROUP BY user_id;

-- Go 层将 req_count 分桶：
-- [0], [1-5], [6-20], [21-50], [51-100], [100+]
```

#### 7.5.4 帕累托图（累积贡献）

```sql
SELECT
  u.username,
  SUM(ul.total_tokens) as total_tokens,
  SUM(SUM(ul.total_tokens)) OVER (ORDER BY SUM(ul.total_tokens) DESC) as cumulative
FROM usage_logs ul
JOIN users u ON u.id = ul.user_id
WHERE ul.created_at >= ? AND ul.created_at <= ?
GROUP BY ul.user_id, u.username
ORDER BY total_tokens DESC;
```

#### 7.5.5 P50/P95/P99 延迟（Go 层排序计算）

```sql
-- 查询全部 duration_ms，Go 层排序后取百分位
SELECT duration_ms
FROM usage_logs
WHERE created_at >= ? AND created_at <= ?
  AND status_code = 200
ORDER BY duration_ms;
```

```go
func percentile(sorted []int64, p float64) int64 {
    if len(sorted) == 0 { return 0 }
    idx := int(float64(len(sorted)-1) * p / 100.0)
    return sorted[idx]
}
// P50 = percentile(durations, 50)
// P95 = percentile(durations, 95)
// P99 = percentile(durations, 99)
```

---

## 八、实现优先级与路线图

### Phase 1：核心报告（MVP）✅

- [x] CLI 框架 + 数据库连接
- [x] 核心聚合查询（15 个基础查询）
- [x] HTML 模板（ECharts 嵌入）
- [x] KPI 数字卡片 + 环比变化
- [x] 趋势图（Token、费用、请求量）
- [x] TOP10 用户横向柱状图
- [x] 模型分布饼图
- [x] 基础文字洞察（环比、TOP贡献者）

### Phase 2：深度分析 ✅

- [x] 箱线图（延迟分布、Token 分布）
- [x] 热力图（24h × 7天 请求密度）
- [x] 直方图（用户频次、I/O 比率）
- [x] 散点图（Input vs Output）
- [x] 帕累托图
- [x] 雷达图（模型多维度对比）
- [x] 留存曲线
- [x] 异常检测算法

### Phase 3：智能洞察 ✅

- [x] 成本预测（线性外推）
- [x] 模型替代建议（高价→低价）
- [x] 配额耗尽预测
- [x] 用户参与度评分
- [x] Prompt 效率建议
- [x] LLM 深度分析（Anthropic/OpenAI，AES-GCM Key 解密，自动降级重试）

---

## 附录 A：可视化图形中英文对照

| 中文名 | 英文名 | 你描述的"方形+线段"是哪个 |
|--------|--------|--------------------------|
| 箱线图 | Box Plot / Box-and-Whisker Plot | ✅ **就是它** — 方形箱体 + 上下延伸的须线 + 圆点离群值 |
| 热力图 | Heatmap | 色块矩阵 |
| 直方图 | Histogram | 连续柱状（无间距） |
| 散点图 | Scatter Plot | 点阵 |
| 帕累托图 | Pareto Chart | 柱状+累积折线 |
| 雷达图 | Radar Chart / Spider Chart | 蛛网图 |
| 漏斗图 | Funnel Chart | 倒三角 |
| 桑基图 | Sankey Diagram | 流向图 |
| 小提琴图 | Violin Plot | 箱线图+核密度（箱线图的高级版） |

## 附录 B：引用与参考

| 来源 | URL | 参考内容 |
|------|-----|----------|
| OpenAI Usage Dashboard | https://help.openai.com/en/articles/8554956 | 用量分析范式 |
| Portkey LLM Analytics | https://docs.portkey.ai | 成本追踪、延迟分析 |
| Langfuse Observability | https://github.com/langfuse/langfuse | LLM 可观测性指标 |
| Helicone User Metrics | https://github.com/Helicone/helicone | 用户行为分析 |
| Microsoft Copilot Dashboard | https://github.com/microsoft/copilot-metrics-dashboard | 采纳率、活跃用户 |
| Grafana RED Method | https://grafana.com/blog | SRE 监控方法论 |
| Plotly Box Plot | https://plotly.com/javascript/box-plots/ | 箱线图实现参考 |
