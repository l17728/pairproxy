# PairProxy 报告生成器 (Reportgen) 使用手册

## 概述

reportgen 是 PairProxy 的可视化分析报告生成工具，能够从 SQLite 或 PostgreSQL 数据库中提取使用数据，生成交互式 HTML 报告。报告包含 16+ 个可视化卡片，覆盖用户、运维和管理三个视角的分析需求。

**最新版本**: v3.0.0
**发布日期**: 2026-04-19
**数据库支持**: SQLite（默认）| PostgreSQL

---

## 快速开始

### 方式一：直接下载预编译二进制（推荐）

从 [GitHub Releases](https://github.com/l17728/pairproxy/releases/latest) 下载对应平台的预编译包，无需安装 Go 环境：

| 平台 | 文件 |
|------|------|
| Linux x86_64 | `reportgen-v3.0.0-linux-amd64.tar.gz` |
| Linux ARM64 | `reportgen-v3.0.0-linux-arm64.tar.gz` |
| macOS x86_64 | `reportgen-v3.0.0-darwin-amd64.tar.gz` |
| macOS ARM64 (Apple Silicon) | `reportgen-v3.0.0-darwin-arm64.tar.gz` |
| Windows x86_64 | `reportgen-v3.0.0-windows-amd64.zip` |
| Windows ARM64 | `reportgen-v3.0.0-windows-arm64.zip` |

```bash
# Linux/macOS 示例
tar -xzf reportgen-v3.0.0-linux-amd64.tar.gz
./reportgen -db /path/to/pairproxy.db          # 不加日期，默认分析过去30天
./reportgen -db /path/to/pairproxy.db -from 2026-04-01 -to 2026-04-05  # 指定范围
```

### 方式二：从源码编译

**前置条件**: Go 1.21+

```bash
# 在 tools/reportgen 目录
go build -o reportgen

# 生成报告（默认过去30天）
./reportgen -db /path/to/pairproxy.db

# 指定日期范围
./reportgen -db /path/to/pairproxy.db -from 2026-04-01 -to 2026-04-05
```

这会在当前目录生成 `report.html`，可直接用浏览器打开。

### 完整命令参数

#### SQLite 模式
```bash
./reportgen -db <数据库> [选项]

必填参数:
  -db <path>        SQLite 数据库文件路径

可选参数:
  -from <YYYY-MM-DD> 分析的开始日期 (包含，默认: 30天前)
  -to <YYYY-MM-DD>   分析的结束日期 (包含，默认: 今天)
  -output <path>    输出 HTML 文件路径 (默认: report.html)
  -template <path>  HTML 模板文件路径 (默认: templates/report.html)
  -llm-url <url>    LLM 端点 URL (如 http://localhost:9000，可选)
  -llm-key <key>    LLM API Key, Bearer token (可选)
  -llm-model <model> LLM 模型名 (默认: gpt-4o-mini)
```

#### PostgreSQL 模式（方案一：完整 DSN）
```bash
./reportgen -pg-dsn "postgres://user:password@host:5432/dbname" [选项]

必填参数:
  -pg-dsn <DSN>     PostgreSQL 连接字符串，格式: postgres://user:password@host:port/dbname

可选参数:
  -from <YYYY-MM-DD> 分析的开始日期 (包含，默认: 30天前)
  -to <YYYY-MM-DD>   分析的结束日期 (包含，默认: 今天)
  -output <path>    输出 HTML 文件路径 (默认: report.html)
  -template <path>  HTML 模板文件路径 (默认: templates/report.html)
  -llm-url <url>    LLM 端点 URL (可选)
  -llm-key <key>    LLM API Key, Bearer token (可选)
  -llm-model <model> LLM 模型名 (默认: gpt-4o-mini)
```

#### PostgreSQL 模式（方案二：独立字段）
```bash
./reportgen -pg-host <host> -pg-user <user> -pg-password <password> -pg-dbname <dbname> \
  [选项]

必填参数:
  -pg-host <host>       PostgreSQL 主机名 (默认: localhost)
  -pg-port <port>       PostgreSQL 端口 (默认: 5432)
  -pg-user <user>       PostgreSQL 用户名
  -pg-password <pass>   PostgreSQL 密码
  -pg-dbname <dbname>   PostgreSQL 数据库名

可选参数:
  -from <YYYY-MM-DD>    分析的开始日期 (包含，默认: 30天前)
  -to <YYYY-MM-DD>      分析的结束日期 (包含，默认: 今天)
  -pg-sslmode <mode>    SSL 模式: disable|require|verify-full (默认: disable)
  -output <path>        输出 HTML 文件路径 (默认: report.html)
  -template <path>      HTML 模板文件路径 (默认: templates/report.html)
  -llm-url <url>        LLM 端点 URL (可选)
  -llm-key <key>        LLM API Key, Bearer token (可选)
  -llm-model <model>    LLM 模型名 (默认: gpt-4o-mini)
```

### 常见示例

#### SQLite: 最简用法（默认过去30天）
```bash
./reportgen -db pairproxy.db
```

#### SQLite: 生成周报 (过去7天)
```bash
./reportgen -db pairproxy.db -from 2026-03-28 -to 2026-04-04 -output weekly-report.html
```

#### SQLite: 生成月报 (整个三月)
```bash
./reportgen -db pairproxy.db -from 2026-03-01 -to 2026-03-31 -output march-report.html
```

#### PostgreSQL: 使用完整 DSN
```bash
./reportgen -pg-dsn "postgres://app:secret@db.example.com:5432/pairproxy" \
  -from 2026-03-28 -to 2026-04-04 -output weekly-report.html
```

#### PostgreSQL: 使用独立字段
```bash
./reportgen -pg-host db.example.com -pg-user app -pg-password secret \
  -pg-dbname pairproxy -from 2026-03-28 -to 2026-04-04 -output weekly-report.html
```

#### PostgreSQL: 启用 SSL 验证
```bash
./reportgen -pg-host db.example.com -pg-user app -pg-password secret \
  -pg-dbname pairproxy -pg-sslmode verify-full \
  -from 2026-03-28 -to 2026-04-04 -output weekly-report.html
```

#### 指定自定义模板
```bash
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07 \
  -template /custom/templates/report.html -output custom-report.html
```

#### 使用直接 LLM URL 和 Key (无需数据库配置)
```bash
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07 \
  -llm-url http://localhost:9000 -llm-key "your-api-key" -llm-model gpt-4o-mini
```

#### 仅使用规则洞察（跳过 LLM）
```bash
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07
# 不指定 -llm-url 和 -llm-key 时，自动降级为纯规则分析
```

---

## 数据库要求

reportgen 需要以下表结构:

### users 表
```sql
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  group_id INTEGER,
  is_active BOOLEAN DEFAULT 1,
  auth_provider TEXT,
  created_at DATETIME,
  last_login_at DATETIME
);
```

### groups 表
```sql
CREATE TABLE groups (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  daily_token_limit INTEGER,
  monthly_token_limit INTEGER,
  created_at DATETIME
);
```

### usage_logs 表 (必需列)
```sql
CREATE TABLE usage_logs (
  id INTEGER PRIMARY KEY,
  request_id TEXT,
  user_id TEXT,
  model TEXT,
  input_tokens INTEGER,
  output_tokens INTEGER,
  total_tokens INTEGER,
  is_streaming BOOLEAN,
  upstream_url TEXT,
  status_code INTEGER,
  duration_ms INTEGER,
  cost_usd REAL,
  source_node TEXT,
  created_at DATETIME
);
```

**注**: 如果表缺少某些可选列，报告会优雅地降级（相关图表为空）。

---

## 报告内容详解

报告由 4 个主要部分组成，共 16+ 个可视化卡片：

### 📊 第一部分: KPI 指标卡 (4 卡)

快速查看关键指标一览：
- **总请求数**: 期间内所有 API 请求总数
- **活跃用户**: 在期间内至少发起过一个请求的用户数
- **平均延迟**: 所有请求的平均响应时间 (ms)
- **总成本**: 期间内所有请求消耗的成本 (USD)

**使用场景**: 高管概览、成本审计、性能监控

---

### 👥 第二部分: 用户视角 (5 卡)

帮助用户了解自己的使用情况和最佳实践：

#### 1. **日活跃用户趋势** (折线图)
- 每天的活跃用户数变化
- 识别使用峰值和谷值
- 用于评估团队参与度

#### 2. **用户分层分布** (用户层级表)
- 展示 4 个层级: 超级用户、活跃用户、普通用户、非活跃用户
- 显示每层用户数和 Token 消耗占比
- 帮助识别关键贡献者

#### 3. **Token 用量百分位数** (柱状图)
- P50, P75, P90, P95, P99 的 Token 消耗分布
- 理解用户间的差异程度
- 识别异常高用量用户

#### 4. **Token 分布箱线图** (箱线图)
- 完整的四分位数分布 (Min, Q1, Median, Q3, Max)
- 直观看出数据离散度
- 识别潜在的数据异常

#### 5. **Engagement 趋势** (折线图)
- DAU/WAU/MAU 指标追踪
- 用户留存和增长趋势
- 评估产品健康度

**使用场景**: 用户自我管理、团队负责人了解使用情况

---

### 🛠️ 第三部分: 运维视角 (6 卡)

帮助运维人员监控系统表现和资源使用：

#### 1. **延迟百分位数监控** (柱状图)
- P50, P95, P99 延迟分析
- 识别性能瓶颈
- 对标 SLA 要求

#### 2. **上游服务延迟箱线图** (箱线图)
- 按上游服务分组的延迟分布
- 对比 Anthropic/OpenAI 等性能
- 快速定位慢服务

#### 3. **请求分布热力图** (热力图)
- 时间×用户的请求热力
- 识别高峰时段
- 发现异常使用模式

#### 4. **输入输出 Token 散点图** (散点图)
- 每个请求的 input/output 比例
- 识别异常的 Token 消耗模式
- 检测可能的滥用

#### 5. **模型成本分布** (柱状图)
- 按模型统计成本
- 识别高成本模型
- 优化模型选择

#### 6. **配额使用表** (表格)
- 按分组统计日/月配额使用率
- 预警接近上限的分组
- 支持动态配额调整

**使用场景**: 日常监控、故障诊断、性能优化、成本控制

---

### 📈 第四部分: 管理视角 (4+ 卡)

为管理层提供决策支持：

#### 1. **采用率指标** (进度条)
- 注册用户 vs 活跃用户比例
- 评估产品采用程度
- 识别未使用的账号

#### 2. **模型雷达图** (多维雷达)
- 6 个维度对比: 请求数、用户数、成本、延迟、可用性、增长
- 快速评估各模型表现
- 支持模型选型决策

#### 3. **用户分组 Token 分布** (箱线图)
- 按分组对比用户的 Token 使用
- 识别分组间的差异
- 优化分组配额

#### 4. **队伍成员贡献 Pareto 分析** (柱状图)
- 排名前 N 个用户的 Token 贡献
- 遵循 80/20 法则识别关键用户
- 指导资源分配

**使用场景**: 周期性总结、董事会报告、成本预算、人员评估

---

## LLM 智能洞察

除规则洞察外，reportgen 支持调用上游 LLM（Anthropic 或 OpenAI）对完整报告数据进行深度分析，生成三视角（使用者/运维/管理者）的中文洞察报告。

### 启用方式（二选一）

#### 方案 1：命令行参数（推荐用于本地开发/临时配置）

```bash
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07 \
  -llm-url http://localhost:9000 \
  -llm-key "your-api-key" \
  -llm-model gpt-4o-mini
```

**优点**：
- 无需修改数据库
- 快速指定任意 LLM 端点
- 便于本地测试和开发

**参数说明**：
- `-llm-url`: LLM 端点 URL（如 `http://localhost:9000`）
- `-llm-key`: LLM API Key，Bearer token 格式
- `-llm-model`: LLM 模型名（默认 `gpt-4o-mini`；Anthropic 端点推荐 `claude-haiku-4-5`）

#### 方案 2：数据库配置（生产环境推荐）

在数据库中配置 LLM 目标，设置环境变量解密 API Key：

```bash
export KEY_ENCRYPTION_KEY="your-key-encryption-key"
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07
```

此时 reportgen 会自动查询数据库中的 LLM 目标配置。

### 优先级

若同时指定了命令行参数和环境变量：
- **命令行参数优先**：若指定 `-llm-url` 和 `-llm-key`，优先使用
- **环境变量备选**：否则从数据库查询（需要 `KEY_ENCRYPTION_KEY`）
- **纯规则分析**：两者均未指定时，自动降级为规则洞察

### 工作原理

- reportgen 读取 LLM 端点配置（命令行或数据库）
- 将完整报告 JSON 发送给 LLM，要求其从三个视角各给出 3~5 条洞察
- 若报告 JSON 超出 LLM 上下文窗口，自动去除 `error_requests`、`slow_requests`、`io_scatter_plot`、`retention_data` 等大数组后重试
- 洞察以纯文本形式附加到报告末尾的"🤖 AI 智能洞察"面板

### 模型选择

| Provider | 使用模型 | 说明 |
|---|---|---|
| Anthropic | `claude-haiku-4-5` | 速度快、成本低，适合分析任务 |
| OpenAI | `gpt-4o-mini` | 成本较低的替代方案 |

**API 兼容性自动判断**：
- Anthropic native：使用 `/v1/messages` API
- OpenAI 兼容：使用 `/v1/chat/completions` API

### 跳过 LLM 洞察

若不需要 LLM 洞察：
- 不指定 `-llm-url` 和 `-llm-key` 参数
- 或不设置 `KEY_ENCRYPTION_KEY` 环境变量

reportgen 会自动降级为纯规则洞察，在 stderr 打印提示：

```
⚠️  LLM insights skipped: 未指定 LLM 配置，使用纯规则分析
```

即使 LLM 连接失败，报告仍可正常生成。

### 容错机制

reportgen 包含完善的容错机制，确保任何故障都不会中断报告生成：

| 故障类型 | 表现 |
|---------|------|
| LLM 连接失败 | 降级为纯规则分析，报告仍可用 |
| LLM 返回 HTTP 错误 (如 429) | 记录错误并跳过洞察 |
| 数据库查询失败 | 相关查询被跳过，报告继续生成 |
| LLM 调用异常 (panic) | 异常被捕获，不影响主流程 |
| 报告模板缺失 | 使用内置最小模板渲染 |
| 数据为空 | 自动生成"暂无数据"提示洞察 |

---

## 进阶使用

### 构建自定义模板

模板路径: `templates/report.html`

报告使用 ECharts 库进行可视化，数据通过 JavaScript 嵌入。修改模板时需要保留以下数据容器和初始化函数的调用：

```html
<!-- 数据容器 -->
<div id="kpiCard-0"></div>
<div id="engagement-chart"></div>
<div id="adoption-rate-chart"></div>
<!-- ... 更多容器 -->

<!-- 初始化代码必须包含 -->
<script>
  // 数据嵌入 (由 reportgen 自动填充)
  const reportData = {/* ... */};
  
  // 调用初始化函数
  initKPICards(reportData.kpis);
  initEngagementTrend(reportData.engagementTrend);
  // ... 其他初始化
</script>
```

### 批量生成报告

创建脚本 `generate_reports.sh`:

```bash
#!/bin/bash
DB="pairproxy.db"

# 生成每周的报告
for week in {0..12}; do
  FROM=$(date -d "$(($week * 7)) days ago" +%Y-%m-%d)
  TO=$(date -d "$(($week * 7 - 6)) days ago" +%Y-%m-%d)
  ./reportgen -db $DB -from $FROM -to $TO -output "report-week-$week.html"
  echo "Generated: report-week-$week.html"
done
```

### 用于 CI/CD 集成

```bash
# 在 cron 任务中每周生成报告
0 9 * * 1 cd /path/to/reportgen && \
  ./reportgen -db /data/pairproxy.db \
    -from $(date -d '7 days ago' +%Y-%m-%d) \
    -to $(date +%Y-%m-%d) \
    -output /var/www/html/weekly-report.html
```

---

## 生成测试报告

#### SQLite: 生成测试报告（mockdata）
```bash
# 生成 mock 测试数据库
cd cmd/mockdata && go run .

# 生成报告（默认过去30天，无需指定日期）
cd ../.. && ./reportgen -db cmd/mockdata/mock.db
```

---

## 故障排查

### 问题 1: 数据库文件不存在

```
错误：数据库文件不存在: /path/to/db
```

**解决方案**: 检查数据库路径是否正确
```bash
ls -la /path/to/pairproxy.db
```

### 问题 2: 无效的日期格式

```
错误：无效的开始日期格式: 2026/04/01（需要 YYYY-MM-DD）
```

**解决方案**: 使用正确的日期格式 `YYYY-MM-DD`
```bash
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07
```

### 问题 3: 开始日期晚于结束日期

```
错误：开始日期必须早于结束日期
```

**解决方案**: 确保 `-from` 早于 `-to`
```bash
# ❌ 错误
./reportgen -db pairproxy.db -from 2026-04-07 -to 2026-04-01

# ✅ 正确
./reportgen -db pairproxy.db -from 2026-04-01 -to 2026-04-07
```

### 问题 4: 报告生成慢或卡住

**原因**: 数据量过大或查询耗时

**解决方案**:
- 缩小日期范围
- 检查数据库索引
- 在服务器而非笔记本生成
- 查看是否有其他数据库操作在进行

### 问题 5: 报告为空或图表不显示

**原因**: 可能是数据不足或浏览器不支持

**解决方案**:
- 检查数据库是否有该时间段的数据
- 使用现代浏览器 (Chrome/Firefox 最新版)
- 打开浏览器控制台 (F12) 查看 JavaScript 错误
- 检查模板文件是否正确

---

## 性能优化

### 数据库优化

为 `usage_logs` 添加索引加快查询:

```sql
CREATE INDEX idx_usage_logs_created ON usage_logs(created_at);
CREATE INDEX idx_usage_logs_user ON usage_logs(user_id);
CREATE INDEX idx_usage_logs_model ON usage_logs(model);
CREATE INDEX idx_usage_logs_status ON usage_logs(status_code);
```

### 报告生成时间参考

基准: SQLite 数据库, 100 万条日志记录

| 日期范围 | 生成时间 |
|---------|---------|
| 1 天 | ~1-2 秒 |
| 7 天 | ~3-5 秒 |
| 30 天 | ~10-15 秒 |
| 90 天 | ~30-45 秒 |
| 365 天 | ~2-3 分钟 |

---

## 报告格式

### 输出文件

生成的 HTML 文件包含:
- **大小**: 30-100 KB (取决于数据量)
- **格式**: 独立 HTML 文件，无需外部依赖
- **兼容性**: 所有现代浏览器
- **交互性**: 支持缩放、平移、数据查询

### 离线查看

生成的报告可完全离线查看，无需网络连接：
```bash
# 直接打开浏览器
open report.html  # macOS
xdg-open report.html  # Linux
start report.html  # Windows
```

### 分享和发布

```bash
# 上传到 Web 服务器
scp report.html user@server:/var/www/html/

# 或转发为 PDF (使用浏览器打印功能)
# Chrome: Ctrl+P -> 另存为 PDF
```

---

## 开发和扩展

### 项目结构

```
tools/reportgen/
├── main.go              # 命令行入口，-from/-to 默认最近30天
├── generator.go         # 报告生成核心
├── queries.go           # Phase 1-2 查询 (基础 + 延迟)
├── queries_phase3.go    # Phase 3 查询 (留存 + 成本)
├── queries_phase4.go    # Phase 4 查询 (趋势 + 配额)
├── queries_phase6.go    # Phase 6 查询 (雷达 + 采用率 + 请求统计)
├── types.go             # 数据结构定义
├── insights.go          # 规则洞察计算 (分层、Pareto等)
├── insights_llm.go      # LLM 智能洞察 (Anthropic/OpenAI)
├── integration_test.go  # 集成测试 (LLM路由、SQL、空库等)
├── main_test.go         # 单元测试 (endOfDay、时间范围)
├── templates/
│   └── report.html      # HTML 模板
├── cmd/mockdata/
│   └── main.go          # Mock 测试数据生成工具
└── README.md            # 本文件
```

### 添加新的可视化

1. 在 `types.go` 中定义数据结构
2. 在 `queries.go` 或新的 `queries_phase*.go` 中实现查询函数
3. 在 `generator.go` 的 `GenerateReport()` 中调用
4. 在 `templates/report.html` 中添加 ECharts 初始化代码

示例: 添加"请求状态码分布"图表

```go
// types.go
type StatusCodeDist struct {
  StatusCode int
  Count      int
  Percentage float64
}

// queries.go
func QueryStatusCodeDistribution(db *sql.DB, from, to time.Time) ([]StatusCodeDist, error) {
  // 实现查询逻辑
}

// generator.go
data.StatusCodeDist, _ = QueryStatusCodeDistribution(db, params.From, params.To)

// templates/report.html
<div id="status-code-chart"></div>
<script>
function initStatusCodeChart(data) {
  // ECharts 配置和初始化
}
initStatusCodeChart(reportData.statusCodeDist);
</script>
```

---

## 常见问题 (FAQ)

**Q: 报告可以导出为 PDF 吗?**  
A: 可以。在浏览器中打开 HTML 报告，按 Ctrl+P (Windows/Linux) 或 Cmd+P (macOS)，选择"另存为 PDF"。

**Q: 支持实时报告吗?**  
A: 当前是静态生成模式。如需实时报告，可定时运行 reportgen 或集成到 Web 框架 (如 Gin)。

**Q: 数据库可以是远程数据库吗?**  
A: 当前仅支持本地 SQLite 文件。如需远程数据库，可先将数据导出为本地 SQLite 或修改代码支持 MySQL/PostgreSQL。

**Q: 如何定制报告的颜色和风格?**  
A: 编辑 `templates/report.html` 中的 ECharts 配置对象的 `color` 字段。

**Q: 可以生成某个用户的个人报告吗?**  
A: 当前报告是全局视角。扩展功能可参考"开发和扩展"章节。

---

## 变更日志

### v3.0.0 (2026-04-19)
- 🔧 同步 pairproxy 主版本升级至 v3.0.0

### v2.24.4 (2026-04-09)
- 🐛 修复 Anthropic 路由 bug：有 model 字段时错误走 OpenAI 协议（`/v1/chat/completions`），现已统一按 provider 判断
- 🐛 修复无效模型 ID：`claude-haiku-4-5-20251001` → `claude-haiku-4-5`
- 🐛 修复空库 NULL 崩溃：所有 `SUM(CASE WHEN...)` 列增加 `COALESCE(..., 0)` 保护
- 🐛 修复 HTTP 413 不触发上下文裁剪重试（`isContextTooLong` 未处理 413 状态码）
- ✨ 新增 `chart-upstream-share`（上游请求占比饼图），修复 3 列 Grid 布局
- 🐛 修复 `stickness` 拼写错误 → `stickiness`，保留向后兼容 fallback
- 🐛 修复 `initIORatio()` 在无对应 div 时被调用导致死图表
- ✨ mockdata 生成器补全 `daily_limit`/`monthly_limit` 字段，model 字段改存友好名称
- 🧪 新增 32 个测试用例：LLM 路由、空库查询、isContextTooLong、日期边界等全面覆盖

### v2.24.3 (2026-04-08)
- ✨ 新增命令行 LLM 参数支持 (`-llm-url`, `-llm-key`, `-llm-model`)，无需数据库配置直接指定 LLM 端点
- 🔧 完善 LLM API 兼容性支持 (OpenAI `/v1/chat/completions` vs Anthropic `/v1/messages` 自动判断)
- 🛡️ 全面容错机制：数据库查询失败、LLM 连接失败、模板缺失、无数据场景等均可优雅降级
- 🔄 改进错误日志：区分 HTTP 错误 (429 等) 与网络连接失败，提供针对性修复建议
- 📊 完整支持 v2.15.0+ 数据库架构，自动适配不同版本

### v2.26.0 (2026-04-07)
- ✨ 新增模型每日用量堆叠面积图（按模型×日期）
- ✨ 新增峰值 RPM KPI 卡片
- ✨ 全部设计文档特性补全，16 类图表 100% 实现

### v2.25.0 (2026-04-07)
- ✨ 新增 LLM 智能洞察 (Anthropic/OpenAI 双提供商，AES-GCM API Key 解密，上下文超限自动重试)
- ✨ Phase 7: 用户请求次数箱线图统计
- 📝 更新使用手册，补充 LLM 洞察配置说明

### v2.24.0 (2026-04-04)
- ✨ 补充 6 阶段可视化覆盖 (从 52% → 90%)
- ✨ 新增 Pareto 分析、用户分层、采用率等高级分析
- 🐛 修复测试数据生成器语法错误
- 📝 完成使用手册和设计文档

### v2.23.0
- 基础报告功能 (KPI + 用户视角)

---

## 联系和支持

- 🐛 **报告 Bug**: https://github.com/l17728/pairproxy/issues
- 💬 **讨论功能**: GitHub Discussions
- 📧 **反馈**: 欢迎提交 PR 和 Issue

---

**文档版本**: v2.24.4 | **最后更新**: 2026-04-09
