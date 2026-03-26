# Claude Load Tester

分布式并发压力测试工具，用于测试 PairProxy 网关和大模型后端的并发上限。

## 功能特性

- **三种测试模式**：阶梯递增、固定并发、脉冲测试
- **真实模拟**：随机间隔发送请求，模拟真实程序员行为
- **多维指标**：QPS、延迟 P50/P95/P99、错误率、Token 吞吐量
- **熔断保护**：错误率超过阈值自动停止测试
- **分布式支持**：多节点独立运行，结果 JSON 导出后汇总

## 快速开始

### 安装

```bash
# 克隆仓库
git clone <repo-url>
cd pairproxy/tools/loadtest

# 构建
go build -o claude-load-tester ./cmd

# 或者安装到 $GOPATH/bin
go install ./cmd
```

### 基础用法

#### 1. 阶梯递增测试（推荐）

从 1 个并发逐步增加到 50 个，观察性能拐点：

```bash
./claude-load-tester run \
  --mode ramp-up \
  --initial 1 \
  --max 50 \
  --step-size 5 \
  --step-duration 60s \
  --output ./results/ramp-up.json
```

#### 2. 固定并发测试

50 个并发持续运行 10 分钟：

```bash
./claude-load-tester run \
  --mode fixed \
  --workers 50 \
  --duration 10m \
  --output ./results/fixed-50.json
```

#### 3. 脉冲测试

瞬间启动 100 个并发，测试系统瞬时承受能力：

```bash
./claude-load-tester run \
  --mode spike \
  --max 100 \
  --duration 5m \
  --output ./results/spike-100.json
```

### 使用自定义 Prompts

```bash
./claude-load-tester run \
  --mode fixed \
  --workers 20 \
  --duration 5m \
  --prompts ./my-prompts.yaml \
  --output ./results/custom.json
```

## 配置选项

### 命令行参数

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--mode` | 测试模式: `ramp-up`, `fixed`, `spike` | `ramp-up` |
| `--initial` | 初始并发数（ramp-up 模式） | `1` |
| `--max` | 最大并发数 | `50` |
| `--workers` | 固定并发数（fixed 模式） | `10` |
| `--step-size` | 每步增加的并发数 | `5` |
| `--step-duration` | 每步持续时间 | `60s` |
| `--duration` | 测试总时长 | `10m` |
| `--timeout` | 请求超时时间 | `120s` |
| `--think-min` | 最小思考时间 | `10s` |
| `--think-max` | 最大思考时间 | `120s` |
| `--prompts` | Prompts 文件路径 | 内置默认 |
| `--output` | 输出报告路径 | 控制台输出 |
| `--circuit-breaker` | 启用熔断 | `true` |
| `--circuit-threshold` | 熔断错误率阈值 | `0.05` |

### 配置文件

创建 `config.yaml`：

```yaml
claude_path: "claude"
prompts_path: "./prompts/prompts.yaml"
output_path: "./results/test.json"

mode: "ramp-up"

workers:
  initial: 1
  max: 50

ramp_up:
  step_size: 5
  step_duration: "60s"
  interval: "30s"

timeout: "120s"

think_time:
  min: "10s"
  max: "120s"

circuit_breaker:
  enabled: true
  threshold: 0.05
```

## Prompts 格式

`prompts.yaml` 示例：

```yaml
categories:
  code_understanding:
    - "解释这段代码的作用：func main() { fmt.Println(\"Hello\") }"
    - "这段 Python 代码是什么意思：..."
    
  code_refactoring:
    - "重构这段代码，使其更易读..."
    
  debugging:
    - "这段代码报错 'nil pointer dereference'，如何修复？"
```

## 分布式测试

### 多节点独立运行

在多台测试服务器上分别运行：

```bash
# 节点 1
./claude-load-tester run --mode fixed --workers 30 --output ./node1.json

# 节点 2
./claude-load-tester run --mode fixed --workers 30 --output ./node2.json

# 节点 3
./claude-load-tester run --mode fixed --workers 30 --output ./node3.json
```

### 结果汇总

```bash
# 收集所有节点的结果文件
scp node1:/path/to/node1.json ./results/
scp node2:/path/to/node2.json ./results/
scp node3:/path/to/node3.json ./results/

# 汇总生成报告
./claude-load-tester aggregate \
  --inputs ./results/node1.json,./results/node2.json,./results/node3.json \
  --output ./results/summary.json
```

## 测试报告

### 控制台输出

```
============================================================
                    Load Test Report
============================================================

Test Duration:     10m0s
Total Workers:     50

--- Request Statistics ---
Total Requests:    2453
Success:           2401 (97.88%)
Failures:          52 (2.12%)

--- Latency Statistics (ms) ---
Min:               234.56
Mean:              1234.78
Max:               5678.90
P50:               1156.34
P90:               1890.12
P95:               2345.67
P99:               4567.89

--- Throughput ---
Requests/sec:      4.09
Success/sec:       4.00

============================================================
```

### JSON 报告

包含详细的时间序列数据，可用于生成图表：

```json
{
  "start_time": "2026-03-25T10:00:00Z",
  "end_time": "2026-03-25T10:10:00Z",
  "duration": "10m0s",
  "total_workers": 50,
  "total_requests": 2453,
  "success_count": 2401,
  "success_rate": 97.88,
  "latency_stats": {
    "min_ms": 234.56,
    "mean_ms": 1234.78,
    "max_ms": 5678.90,
    "p50_ms": 1156.34,
    "p90_ms": 1890.12,
    "p95_ms": 2345.67,
    "p99_ms": 4567.89
  },
  "time_series": [
    {
      "timestamp": "2026-03-25T10:00:00Z",
      "active_workers": 5,
      "rps": 2.1,
      "avg_latency_ms": 980.5
    }
  ]
}
```

## 测试场景建议

### 场景 1：探索系统极限

逐步增加并发，找到系统的性能拐点：

```bash
./claude-load-tester run \
  --mode ramp-up \
  --initial 1 \
  --max 100 \
  --step-size 10 \
  --step-duration 120s \
  --output ./results/limit-exploration.json
```

### 场景 2：稳定性测试

在目标并发下长时间运行，检查稳定性：

```bash
./claude-load-tester run \
  --mode fixed \
  --workers 30 \
  --duration 1h \
  --output ./results/stability-30w.json
```

### 场景 3：突发流量测试

模拟突发流量，测试系统的瞬时处理能力：

```bash
./claude-load-tester run \
  --mode spike \
  --max 200 \
  --duration 2m \
  --output ./results/spike-200w.json
```

## 注意事项

1. **Claude CLI 配置**：确保测试服务器已配置 Claude CLI 和 PairProxy 网关地址
2. **资源限制**：注意测试机的 CPU、内存、网络带宽限制
3. **成本控制**：大量并发会产生大量 LLM 调用，注意成本
4. **熔断机制**：默认启用，错误率超过 5% 会自动停止测试
5. **优雅退出**：按 Ctrl+C 可以优雅停止测试并生成报告

## 性能指标说明

| 指标 | 说明 | 健康阈值建议 |
|------|------|-------------|
| QPS | 每秒请求数 | 根据系统配置 |
| P50 延迟 | 50% 请求延迟低于此值 | < 2s |
| P95 延迟 | 95% 请求延迟低于此值 | < 5s |
| P99 延迟 | 99% 请求延迟低于此值 | < 10s |
| 错误率 | 失败请求占比 | < 5% |

## 贡献

欢迎提交 Issue 和 PR！

## 许可证

MIT License
