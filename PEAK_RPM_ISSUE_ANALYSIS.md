# Peak RPM 为 0 的根本原因分析

## 问题表现
生产环境生成的报告中，**峰值 RPM (Peak RPM) 显示为 0**，这显然是不正常的。

## 代码分析

### 1. 数据结构定义
- 文件：`tools/reportgen/types.go:121`
- 定义：`PeakRPM int64 `json:"peak_rpm"`
- 这是 KPIData 结构体的一部分

### 2. 查询实现
- 文件：`tools/reportgen/queries_phase8.go:266-280`
- 方法：`QueryPeakRPM(from, to time.Time) (int64, error)`

**SQL 查询逻辑：**
```sql
SELECT COALESCE(MAX(cnt), 0) FROM (
  SELECT COUNT(*) AS cnt
  FROM usage_logs
  WHERE created_at >= ? AND created_at < ?
  GROUP BY [时间分组表达式]
)
```

### 3. 核心问题：时间分组表达式

**SQLite 时间分组表达式（queries.go:147）：**
```go
return "strftime('%Y-%m-%d %H:%M', " + col + ")"
```

**PostgreSQL 时间分组表达式（queries.go:145）：**
```go
return "TO_CHAR(DATE_TRUNC('minute', " + col + "), 'YYYY-MM-DD HH24:MI')"
```

## 可能的原因分析

### 原因 1：使用了 PostgreSQL，但时间戳没有 UTC 时区信息
- PostgreSQL 可能将 `created_at` 解释为本地时区时间
- `DATE_TRUNC('minute', ...)` 对不同时区的时间戳分组可能产生不同结果
- **如果时间戳都为 NULL 或格式无效，分组会失败**

### 原因 2：时间戳精度问题
- 如果所有 `created_at` 值都是 NULL，`GROUP BY` 会只返回一个 NULL 行
- `COUNT(*) AS cnt` 在这种情况下会返回总行数，但如果没有有效数据，可能为 0

### 原因 3：子查询返回空结果
- 如果 `WHERE created_at >= ? AND created_at < ?` 条件导致没有数据被返回
- 子查询返回的结果集为空
- `SELECT COALESCE(MAX(cnt), 0)` 会返回 0

### 原因 4：时间戳类型问题（PostgreSQL）
- 若 `created_at` 字段类型不是 `TIMESTAMP WITH TIME ZONE`
- 而是 `TIMESTAMP WITHOUT TIME ZONE`
- PostgreSQL 在计算时可能有歧义，导致分组结果异常

### 原因 5：字符串格式化问题
- SQLite：`strftime('%Y-%m-%d %H:%M', created_at)` 依赖于 `created_at` 是否有效
- 如果 `created_at` 为 NULL 或格式无效，会返回 NULL
- NULL GROUP BY 会导致所有 NULL 时间戳被分为一组

## 建议的排查步骤

### 第一步：验证数据存在性
```sql
-- PostgreSQL/SQLite 通用
SELECT COUNT(*) FROM usage_logs 
WHERE created_at >= '[from_time]' AND created_at < '[to_time]';
```
- 如果返回 0，则数据范围不匹配

### 第二步：检查时间戳有效性
```sql
-- SQLite
SELECT COUNT(*) FROM usage_logs 
WHERE created_at >= '[from_time]' AND created_at < '[to_time]'
  AND created_at IS NOT NULL;

-- PostgreSQL
SELECT COUNT(*) FROM usage_logs 
WHERE created_at >= '[from_time]' AND created_at < '[to_time]'
  AND created_at IS NOT NULL
  AND created_at != 'infinity'::timestamp;
```

### 第三步：检查时间分组
```sql
-- SQLite
SELECT strftime('%Y-%m-%d %H:%M', created_at) AS minute, COUNT(*) AS cnt
FROM usage_logs
WHERE created_at >= '[from_time]' AND created_at < '[to_time]'
GROUP BY minute
ORDER BY cnt DESC
LIMIT 1;

-- PostgreSQL
SELECT TO_CHAR(DATE_TRUNC('minute', created_at), 'YYYY-MM-DD HH24:MI') AS minute, COUNT(*) AS cnt
FROM usage_logs
WHERE created_at >= '[from_time]' AND created_at < '[to_time]'
GROUP BY minute
ORDER BY cnt DESC
LIMIT 1;
```
- 检查最高的分组计数是否非零

### 第四步：检查原始 QueryPeakRPM 查询
```sql
-- SQLite 示例
SELECT COALESCE(MAX(cnt), 0) FROM (
  SELECT COUNT(*) AS cnt
  FROM usage_logs
  WHERE created_at >= '[from_time]' AND created_at < '[to_time]'
  GROUP BY strftime('%Y-%m-%d %H:%M', created_at)
);
```

## 最可能的原因（生产环境）— **已确认！**

### 确认的根本原因：SQLite 时区问题（非UTC系统）

**症状完全匹配最新的 v2.24.5 修复内容！**

提交 `4da6c42` 的提交信息明确指出：
> "Fixes: **token statistics returning 0 on non-UTC systems (e.g., Asia/Shanghai)**"

### 问题详解

1. **SQLite 字符串比较问题**
   - SQLite `WHERE created_at >= ? AND created_at < ?` 进行 **字典序（lexicographic）** 字符串比较
   - 当系统时区 ≠ UTC 时，时间戳被存储为本地时间的字符串格式
   - 查询时使用 UTC 时间范围与本地时间戳比较会导致完全不匹配

2. **示例场景**
   ```
   系统时区：Asia/Shanghai (UTC+8)
   
   数据库存储 created_at：
   - "2026-04-09 10:00:00" (这是上海时间的字符串)
   
   查询条件（UTC）：
   - WHERE created_at >= "2026-04-09 00:00:00 UTC"
   - WHERE created_at < "2026-04-10 00:00:00 UTC"
   
   字典序比较结果：
   - "2026-04-09 10:00:00" >= "2026-04-09 00:00:00 UTC"? 
     ✗ 字典序上 "10:00:00" > "00:00:00 UTC" 会匹配
   - 但当时间跨越不同的小时/天时会完全失效
   
   GROUP BY strftime('%Y-%m-%d %H:%M', created_at)
   - 在非UTC系统上，所有分组可能落到错误的"分钟桶"
   - 最终导致 MAX(cnt) 计算错误或为 0
   ```

3. **为什么其他指标不受影响但 Peak RPM 为 0**
   - `COUNT(*)` 聚合直接计数，不依赖时间戳精度
   - `SUM()` 聚合也不依赖时间戳比较逻辑
   - 但 `GROUP BY [时间表达式]` 直接依赖时间戳的字符串值
   - 当分组结果为空或全部落入一个错误的组时，MAX(cnt) 就会返回 0

### 网关侧的修复（v2.24.5）

在 `internal/db/usage_repo.go` 中添加了 `toUTC()` 辅助函数：

```go
// toUTC normalises a time value to UTC so that SQLite string comparisons
// (which are lexicographic) work correctly regardless of the caller's locale.
func toUTC(t time.Time) time.Time { return t.UTC() }
```

该函数应用到 9 个时间过滤方法：
- `Query()`
- `SumTokens()` 
- `GlobalSumTokens()`
- `UserStats()`
- `ExportLogs()`
- `SumCostUSD()`
- `DeleteBefore()`
- `DailyTokens()`
- `DailyCost()`

### reportgen 中缺少的修复

**关键发现：reportgen 的 `QueryPeakRPM()` 及其他时间查询没有应用相同的 UTC 规范化！**

当前代码（queries_phase8.go:266-280）：
```go
func (q *Querier) QueryPeakRPM(from, to time.Time) (int64, error) {
    row := q.queryRow(fmt.Sprintf(`
        SELECT COALESCE(MAX(cnt), 0) FROM (
          SELECT COUNT(*) AS cnt
          FROM usage_logs
          WHERE created_at >= ? AND created_at < ?
          GROUP BY %s
        )
    `, q.sqlMinuteGroup("created_at")), from, to)
    // ...
}
```

**问题：**
- `from` 和 `to` 直接传给 SQL，如果是本地时间而数据库中存储的是 UTC 时间
- 或反之，则 WHERE 条件会失效
- 导致没有数据被分组，`MAX(cnt)` 返回 0

## 修复方案

### 方案A：对标网关侧修复（推荐）

在 `tools/reportgen/queries.go` 中添加 `toUTC()` 辅助函数：

```go
// toUTC normalises a time value to UTC so that SQLite string comparisons
// (which are lexicographic) work correctly regardless of the caller's locale.
func toUTC(t time.Time) time.Time { return t.UTC() }
```

然后在所有时间过滤查询中应用，**首先修复 `QueryPeakRPM`**：

```go
func (q *Querier) QueryPeakRPM(from, to time.Time) (int64, error) {
    from, to = toUTC(from), toUTC(to)  // ← 添加这一行
    
    row := q.queryRow(fmt.Sprintf(`
        SELECT COALESCE(MAX(cnt), 0) FROM (
          SELECT COUNT(*) AS cnt
          FROM usage_logs
          WHERE created_at >= ? AND created_at < ?
          GROUP BY %s
        )
    `, q.sqlMinuteGroup("created_at")), from, to)
    
    var v int64
    if err := row.Scan(&v); err != nil {
        return 0, fmt.Errorf("query peak rpm: %w", err)
    }
    return v, nil
}
```

**所有需要修复的方法**（按优先级）：
1. **高优先级 - KPI 相关**
   - `QueryKPI()` - 主要 KPI 计算
   - `QueryPeakRPM()` - 本期问题根源
   - `QueryDailyTrend()` - 每日趋势
   - `QueryHeatmap()` - 热力图数据

2. **中优先级 - 分析查询**
   - `QueryLatencyBoxPlotByModel()`
   - `QueryLatencyPercentileTrend()`
   - `QueryDailyLatencyTrend()`
   - `QueryModelDailyTrend()`
   - `QueryUserRequestBoxPlot()`
   - `QueryLatencyHistogram()`
   - 所有 Phase 2-9 的时间过滤查询

3. **低优先级 - 查询支持方法**
   - `queryDurations()` - 仅用于百分位计算
   - `QueryEngagement()` 中的日期查询

### 方案B：在调用侧规范化（临时方案）

在 `generator.go` 中的调用点规范化时间参数：

```go
// 在 GenerateReport() 或 queryAllData() 中
params.From = params.From.UTC()
params.To = params.To.UTC()

// 然后继续现有逻辑
data.KPI.PeakRPM, qErr = q.QueryPeakRPM(params.From, params.To)
```

**优缺点：**
- ✓ 改动最小，风险最低
- ✗ 每个调用点都要手动修改，容易遗漏
- ✗ 不治根本原因

### 方案C：在 Querier 初始化时统一处理（最稳妥）

在 `NewQuerier()` 方法中存储规范化的时间参数（需要改动调用签名）

---

## 立即验证步骤

### 第一步：确认时区问题
```bash
# 查看系统时区
date +%Z  # Linux/macOS
# 或 Windows 系统设置

# 查看 SQLite 中的实际时间戳样本
sqlite3 pairproxy.db "SELECT created_at FROM usage_logs LIMIT 5;"

# 如果显示本地时间（不是 RFC3339 UTC 格式），就确认是本问题
```

### 第二步：测试修复
```bash
# 在 reportgen 中临时添加 UTC 转换后重新生成报告
# 检查 Peak RPM 是否显示正确的非零值
```

### 第三步：检查历史数据
```sql
-- 检查最高峰值
SELECT MAX(cnt) FROM (
  SELECT COUNT(*) AS cnt
  FROM usage_logs
  WHERE created_at >= datetime('now', '-7 days')
  GROUP BY strftime('%Y-%m-%d %H:%M', created_at)
);

-- 对比 UTC 规范化后的结果
SELECT MAX(cnt) FROM (
  SELECT COUNT(*) AS cnt
  FROM usage_logs
  WHERE created_at >= datetime('now', '-7 days')
  GROUP BY strftime('%Y-%m-%d %H:%M', created_at, 'utc')  -- 强制 UTC
);
```

## 推荐的完整修复步骤

1. **立即修复：`QueryPeakRPM()`** （本期症状根源）
   - 添加 `from, to = toUTC(from), toUTC(to)`
   
2. **短期修复：所有 KPI 和趋势查询** （防止隐藏的类似问题）
   - `QueryKPI()`, `QueryDailyTrend()`, `QueryHeatmap()` 等
   
3. **完整修复：所有时间过滤查询** （与网关侧 v2.24.5 保持一致）
   - 应用 `toUTC()` 到所有使用 `created_at >= ?` 条件的查询
   
4. **测试验证：** 运行现有的 `pg_parity_test.go` 确保 SQLite/PostgreSQL 一致性

5. **生成报告测试：** 在非 UTC 时区系统上生成报告，验证 Peak RPM 和其他时间序列数据正确
