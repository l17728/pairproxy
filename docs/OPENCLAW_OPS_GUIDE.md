# PairProxy OpenClaw 自动化运维手册

**版本**: v1.0
**适用系统**: PairProxy v2.5.0+
**更新日期**: 2026-03-08

---

## 1. 概述

本手册定义了使用 OpenClaw 自动化运维 PairProxy 系统的完整配置，实现无人值守的系统监控、故障诊断和自动修复。

### 1.1 运维目标

- **可用性**: 保持系统 99.5%+ 可用性
- **响应时间**: 故障检测 < 5 分钟，自动修复 < 10 分钟
- **数据安全**: 每日自动备份，保留 7 天
- **故障隔离**: 自动区分系统故障和上游 LLM 服务问题

### 1.2 监控范围

| 组件 | 监控项 | 检查频率 |
|------|--------|----------|
| SProxy 服务 | 进程状态、健康端点、响应时间 | 1 分钟 |
| CProxy 客户端 | 连接状态、Token 有效性 | 5 分钟 |
| 数据库 | 磁盘空间、连接数、慢查询 | 5 分钟 |
| LLM 上游 | 端点可达性、响应时间 | 3 分钟 |
| 集群节点 | 心跳状态、用量同步 | 2 分钟 |

---

## 2. OpenClaw 配置

### 2.1 基础配置文件

创建 `openclaw-pairproxy.yaml`:

```yaml
openclaw:
  version: "1.0"
  project: "pairproxy"

  # 运维代理配置
  agent:
    model: "claude-sonnet-4"
    temperature: 0.1
    max_tokens: 4096

  # 执行环境
  environment:
    working_dir: "/opt/pairproxy"
    shell: "/bin/bash"
    user: "pairproxy"

  # 通知配置
  notifications:
    slack_webhook: "${SLACK_WEBHOOK_URL}"
    email: "ops@company.com"

  # 任务调度
  schedule:
    health_check: "*/1 * * * *"      # 每分钟
    backup: "0 2 * * *"               # 每天 2:00
    log_cleanup: "0 3 * * 0"          # 每周日 3:00
    metrics_report: "0 9 * * 1"       # 每周一 9:00

  # 任务定义
  tasks:
    - name: "health_check"
      type: "monitor"
      config: "tasks/health_check.yaml"

    - name: "llm_upstream_check"
      type: "monitor"
      config: "tasks/llm_check.yaml"

    - name: "database_maintenance"
      type: "maintenance"
      config: "tasks/db_maintenance.yaml"

    - name: "auto_backup"
      type: "backup"
      config: "tasks/backup.yaml"

    - name: "incident_response"
      type: "recovery"
      config: "tasks/incident.yaml"
```

---

## 3. 运维任务定义

### 3.1 健康检查任务

创建 `tasks/health_check.yaml`:

```yaml
task:
  name: "health_check"
  description: "检查 PairProxy 系统健康状态"

  checks:
    - name: "sproxy_process"
      command: "systemctl is-active sproxy"
      expect: "active"
      severity: "critical"

    - name: "sproxy_health_endpoint"
      command: "curl -sf http://localhost:9000/health"
      expect_json:
        status: "ok"
      timeout: 5s
      severity: "critical"

    - name: "database_size"
      command: "du -sm /var/lib/pairproxy/pairproxy.db | awk '{print $1}'"
      threshold: 10000  # 10GB
      severity: "warning"

    - name: "disk_space"
      command: "df -h /var/lib/pairproxy | tail -1 | awk '{print $5}' | sed 's/%//'"
      threshold: 80
      severity: "warning"

    - name: "active_requests"
      command: "curl -s http://localhost:9000/metrics | grep 'active_requests' | awk '{print $2}'"
      threshold: 100
      severity: "warning"

  # 故障响应
  on_failure:
    - check: "sproxy_process"
      action: "restart_sproxy"

    - check: "sproxy_health_endpoint"
      action: "diagnose_and_restart"

    - check: "disk_space"
      action: "cleanup_logs"

    - check: "active_requests"
      action: "alert_high_load"

  # AI 提示词
  ai_prompt: |
    你是 PairProxy 系统的运维专家。当前健康检查发现以下问题：

    {{failure_summary}}

    请根据以下信息诊断问题：
    1. 检查项名称和失败原因
    2. 系统日志（最近 50 行）
    3. 进程状态

    诊断步骤：
    1. 判断问题严重程度（critical/warning）
    2. 识别根本原因（进程崩溃/配置错误/资源耗尽/上游故障）
    3. 提供修复建议（重启服务/清理资源/调整配置）
    4. 如果需要重启，先检查是否有活跃请求

    输出格式：
    - 问题诊断：[简要描述]
    - 根本原因：[原因分析]
    - 修复方案：[具体步骤]
    - 风险评估：[操作风险]
```

### 3.2 LLM 上游检查任务

创建 `tasks/llm_check.yaml`:

```yaml
task:
  name: "llm_upstream_check"
  description: "检查 LLM 上游服务状态，区分系统故障和 LLM 服务问题"

  checks:
    - name: "anthropic_direct"
      command: |
        curl -sf https://api.anthropic.com/v1/messages \
          -H "x-api-key: ${ANTHROPIC_API_KEY}" \
          -H "anthropic-version: 2023-06-01" \
          -H "content-type: application/json" \
          -d '{"model":"claude-3-haiku-20240307","max_tokens":10,"messages":[{"role":"user","content":"test"}]}'
      timeout: 10s
      severity: "info"

    - name: "sproxy_to_llm"
      command: |
        curl -sf http://localhost:9000/v1/messages \
          -H "X-PairProxy-Auth: ${TEST_USER_JWT}" \
          -H "anthropic-version: 2023-06-01" \
          -H "content-type: application/json" \
          -d '{"model":"claude-3-haiku-20240307","max_tokens":10,"messages":[{"role":"user","content":"test"}]}'
      timeout: 15s
      severity: "critical"

  # 故障诊断逻辑
  diagnosis:
    - condition: "anthropic_direct=fail AND sproxy_to_llm=fail"
      conclusion: "LLM 上游服务故障"
      action: "alert_upstream_issue"

    - condition: "anthropic_direct=pass AND sproxy_to_llm=fail"
      conclusion: "PairProxy 系统故障"
      action: "diagnose_sproxy"

    - condition: "anthropic_direct=pass AND sproxy_to_llm=pass"
      conclusion: "系统正常"
      action: "none"

  # AI 提示词
  ai_prompt: |
    你是 PairProxy 系统的运维专家。当前 LLM 连接检查结果：

    - Anthropic 直连测试：{{anthropic_direct_status}}
    - 通过 SProxy 测试：{{sproxy_to_llm_status}}

    请根据以下诊断矩阵判断问题：

    | Anthropic 直连 | SProxy 测试 | 结论 |
    |---------------|-------------|------|
    | ✅ 成功 | ✅ 成功 | 系统正常 |
    | ✅ 成功 | ❌ 失败 | PairProxy 故障 |
    | ❌ 失败 | ❌ 失败 | LLM 上游故障 |
    | ❌ 失败 | ✅ 成功 | 异常（需人工介入）|

    如果是 PairProxy 故障，请检查：
    1. SProxy 进程状态：`systemctl status sproxy`
    2. SProxy 日志：`journalctl -u sproxy -n 50`
    3. 数据库连接：`sqlite3 /var/lib/pairproxy/pairproxy.db ".tables"`
    4. 配置文件：`sproxy admin validate`

    如果是 LLM 上游故障，请：
    1. 记录故障时间和持续时长
    2. 通知相关人员
    3. 检查 Anthropic 状态页：https://status.anthropic.com

    输出格式：
    - 故障定位：[PairProxy/LLM上游/未知]
    - 详细诊断：[具体分析]
    - 建议操作：[下一步行动]
```

### 3.3 数据库维护任务

创建 `tasks/db_maintenance.yaml`:

```yaml
task:
  name: "database_maintenance"
  description: "数据库健康检查和维护"

  checks:
    - name: "db_integrity"
      command: "sqlite3 /var/lib/pairproxy/pairproxy.db 'PRAGMA integrity_check;'"
      expect: "ok"
      severity: "critical"

    - name: "db_size"
      command: "du -sm /var/lib/pairproxy/pairproxy.db | awk '{print $1}'"
      threshold: 10000
      severity: "warning"

    - name: "usage_logs_count"
      command: "sqlite3 /var/lib/pairproxy/pairproxy.db 'SELECT COUNT(*) FROM usage_logs;'"
      threshold: 10000000
      severity: "info"

    - name: "dropped_usage_count"
      command: "curl -s http://localhost:9000/metrics | grep 'usage_writer_dropped' | awk '{print $2}'"
      threshold: 0
      severity: "critical"

  # 维护操作
  maintenance:
    - name: "vacuum"
      command: "sqlite3 /var/lib/pairproxy/pairproxy.db 'VACUUM;'"
      schedule: "weekly"

    - name: "cleanup_old_logs"
      command: "./sproxy admin logs purge --days 90"
      schedule: "weekly"

    - name: "analyze"
      command: "sqlite3 /var/lib/pairproxy/pairproxy.db 'ANALYZE;'"
      schedule: "daily"

  # AI 提示词
  ai_prompt: |
    你是 PairProxy 数据库管理专家。当前数据库状态：

    {{db_status_summary}}

    请评估以下指标：
    1. 数据库完整性检查结果
    2. 数据库文件大小（当前/阈值）
    3. 用量日志记录数
    4. 丢弃的用量记录数（dropped_usage_count）

    关键告警：
    - 如果 dropped_usage_count > 0，说明用量队列满，数据丢失！
    - 如果数据库大小 > 10GB，需要清理旧日志
    - 如果完整性检查失败，需要立即备份并修复

    维护建议：
    1. 定期执行 VACUUM 回收空间
    2. 清理 90 天前的日志
    3. 执行 ANALYZE 优化查询

    输出格式：
    - 健康评分：[0-100]
    - 发现问题：[问题列表]
    - 维护建议：[具体操作]
    - 紧急程度：[低/中/高]
```

### 3.4 自动备份任务

创建 `tasks/backup.yaml`:

```yaml
task:
  name: "auto_backup"
  description: "自动备份数据库"

  backup:
    source: "/var/lib/pairproxy/pairproxy.db"
    destination: "/backup/pairproxy"
    retention_days: 7

  steps:
    - name: "create_backup"
      command: |
        BACKUP_FILE="/backup/pairproxy/pairproxy_$(date +%Y%m%d_%H%M%S).db"
        ./sproxy admin backup --output "$BACKUP_FILE"
        echo "$BACKUP_FILE"

    - name: "verify_backup"
      command: |
        BACKUP_FILE="{{backup_file}}"
        sqlite3 "$BACKUP_FILE" 'PRAGMA integrity_check;'
      expect: "ok"

    - name: "cleanup_old_backups"
      command: |
        find /backup/pairproxy -name "pairproxy_*.db" -mtime +7 -delete

    - name: "report_backup_size"
      command: |
        du -sh /backup/pairproxy

  # AI 提示词
  ai_prompt: |
    你是 PairProxy 备份管理专家。备份任务执行结果：

    {{backup_result}}

    请验证：
    1. 备份文件是否创建成功
    2. 备份文件完整性检查是否通过
    3. 旧备份是否正确清理
    4. 备份目录总大小

    如果备份失败，请检查：
    - 磁盘空间是否充足
    - 数据库文件是否被锁定
    - 备份目录权限是否正确

    输出格式：
    - 备份状态：[成功/失败]
    - 备份文件：[文件路径]
    - 文件大小：[大小]
    - 问题诊断：[如有问题]
```

### 3.5 故障响应任务

创建 `tasks/incident.yaml`:

```yaml
task:
  name: "incident_response"
  description: "自动故障响应和恢复"

  incidents:
    - name: "sproxy_down"
      trigger: "sproxy_process=inactive"
      severity: "critical"
      actions:
        - check_logs
        - attempt_restart
        - verify_recovery
        - notify_team

    - name: "high_error_rate"
      trigger: "error_rate > 5%"
      severity: "warning"
      actions:
        - analyze_errors
        - check_upstream
        - scale_if_needed

    - name: "disk_full"
      trigger: "disk_usage > 90%"
      severity: "critical"
      actions:
        - cleanup_logs
        - compress_old_data
        - alert_capacity

  # 恢复流程
  recovery_procedures:
    restart_sproxy:
      steps:
        - name: "check_active_requests"
          command: "curl -s http://localhost:9000/metrics | grep 'active_requests'"

        - name: "enter_drain_mode"
          command: "./sproxy admin drain enter"
          condition: "active_requests > 0"

        - name: "wait_for_drain"
          command: "./sproxy admin drain wait --timeout 60s"
          condition: "active_requests > 0"

        - name: "restart_service"
          command: "systemctl restart sproxy"

        - name: "verify_health"
          command: "curl -sf http://localhost:9000/health"
          retry: 3
          interval: 10s

  # AI 提示词
  ai_prompt: |
    你是 PairProxy 故障响应专家。当前故障情况：

    故障类型：{{incident_type}}
    触发条件：{{trigger_condition}}
    严重程度：{{severity}}

    系统状态：
    {{system_status}}

    请执行故障响应流程：

    1. **故障确认**
       - 验证故障是否真实存在
       - 评估影响范围（用户数/请求数）
       - 确定故障开始时间

    2. **根因分析**
       - 检查系统日志
       - 检查资源使用情况
       - 检查配置变更历史
       - 检查上游服务状态

    3. **恢复操作**
       - 如果是进程崩溃：使用排水模式重启
       - 如果是资源耗尽：清理资源后重启
       - 如果是配置错误：回滚配置
       - 如果是上游故障：等待上游恢复

    4. **验证恢复**
       - 检查服务健康端点
       - 发送测试请求
       - 监控错误率

    5. **事后处理**
       - 记录故障时间线
       - 更新运维文档
       - 提出改进建议

    输出格式：
    - 故障诊断：[详细分析]
    - 恢复步骤：[具体操作]
    - 执行结果：[成功/失败]
    - 后续建议：[预防措施]
```

---

## 4. 启动和配置

### 4.1 安装 OpenClaw

```bash
# 安装 OpenClaw（假设使用 pip）
pip install openclaw

# 验证安装
openclaw --version
```

### 4.2 配置环境变量

创建 `.env` 文件：

```bash
# PairProxy 配置
PAIRPROXY_HOME=/opt/pairproxy
PAIRPROXY_DB=/var/lib/pairproxy/pairproxy.db

# 测试用户 JWT（用于健康检查）
TEST_USER_JWT=eyJhbGc...

# LLM API Key（用于直连测试）
ANTHROPIC_API_KEY=sk-ant-...

# 通知配置
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/...

# OpenClaw 配置
OPENCLAW_LOG_LEVEL=INFO
OPENCLAW_LOG_FILE=/var/log/openclaw/pairproxy.log
```

### 4.3 启动 OpenClaw

```bash
# 前台运行（测试）
openclaw run --config openclaw-pairproxy.yaml

# 后台运行（生产）
openclaw daemon --config openclaw-pairproxy.yaml

# 使用 systemd
sudo systemctl start openclaw-pairproxy
sudo systemctl enable openclaw-pairproxy
```

### 4.4 Systemd 服务配置

创建 `/etc/systemd/system/openclaw-pairproxy.service`:

```ini
[Unit]
Description=OpenClaw Auto-Ops for PairProxy
After=network-online.target sproxy.service
Wants=network-online.target

[Service]
Type=simple
User=pairproxy
Group=pairproxy
WorkingDirectory=/opt/pairproxy
EnvironmentFile=/opt/pairproxy/.env
ExecStart=/usr/local/bin/openclaw daemon --config /opt/pairproxy/openclaw-pairproxy.yaml
Restart=always
RestartSec=10s

[Install]
WantedBy=multi-user.target
```

---

## 5. 监控和告警

### 5.1 监控指标

OpenClaw 自动收集以下指标：

| 指标 | 说明 | 告警阈值 |
|------|------|----------|
| `sproxy_uptime` | SProxy 运行时间 | < 60s（频繁重启）|
| `health_check_failures` | 健康检查失败次数 | > 3 |
| `llm_upstream_latency` | LLM 上游延迟 | > 5s |
| `database_size_mb` | 数据库大小 | > 10000 |
| `disk_usage_percent` | 磁盘使用率 | > 80% |
| `active_requests` | 活跃请求数 | > 100 |
| `dropped_usage_count` | 丢弃的用量记录 | > 0 |

### 5.2 告警规则

```yaml
alerts:
  - name: "sproxy_down"
    condition: "sproxy_uptime == 0"
    severity: "critical"
    message: "SProxy 服务已停止"

  - name: "high_error_rate"
    condition: "error_rate > 5%"
    severity: "warning"
    message: "错误率超过 5%"

  - name: "data_loss"
    condition: "dropped_usage_count > 0"
    severity: "critical"
    message: "检测到用量数据丢失"

  - name: "disk_full"
    condition: "disk_usage_percent > 90%"
    severity: "critical"
    message: "磁盘空间不足"
```

---

## 6. 故障场景和响应

### 6.1 常见故障场景

| 场景 | 检测方法 | 自动响应 | 人工介入 |
|------|----------|----------|----------|
| SProxy 进程崩溃 | 进程检查 | 自动重启 | 否 |
| 数据库损坏 | 完整性检查 | 从备份恢复 | 是 |
| 磁盘空间不足 | 磁盘检查 | 清理日志 | 否 |
| LLM 上游故障 | 直连测试 | 告警通知 | 是 |
| 配额耗尽 | 配额检查 | 告警通知 | 是 |
| 用量数据丢失 | dropped 计数 | 告警+调查 | 是 |

### 6.2 故障响应流程图

```
故障检测
    ↓
判断故障类型
    ↓
    ├─→ 系统故障 → 自动诊断 → 自动修复 → 验证恢复
    │                              ↓
    │                          失败 → 人工介入
    │
    └─→ 上游故障 → 告警通知 → 等待恢复 → 持续监控
```

---

## 7. 最佳实践

### 7.1 运维建议

1. **定期检查 OpenClaw 日志**
   ```bash
   tail -f /var/log/openclaw/pairproxy.log
   ```

2. **每周审查自动化操作**
   - 检查自动重启次数
   - 审查故障响应记录
   - 评估告警准确性

3. **定期测试恢复流程**
   ```bash
   # 模拟故障
   systemctl stop sproxy

   # 观察 OpenClaw 响应
   tail -f /var/log/openclaw/pairproxy.log
   ```

4. **保持配置同步**
   - 系统配置变更后更新 OpenClaw 配置
   - 定期审查运维任务定义

### 7.2 安全注意事项

1. **凭证管理**
   - 使用环境变量存储敏感信息
   - 定期轮换 API Key 和 JWT
   - 限制 OpenClaw 运行用户权限

2. **操作审计**
   - 记录所有自动化操作
   - 保留操作日志至少 30 天
   - 定期审查异常操作

3. **权限控制**
   - OpenClaw 使用专用用户运行
   - 限制对生产数据库的写权限
   - 重要操作需要人工确认

---

## 8. 故障排查

### 8.1 OpenClaw 自身问题

**问题**: OpenClaw 无法启动

```bash
# 检查配置文件
openclaw validate --config openclaw-pairproxy.yaml

# 检查日志
tail -100 /var/log/openclaw/pairproxy.log

# 检查环境变量
env | grep OPENCLAW
```

**问题**: 任务执行失败

```bash
# 手动执行任务
openclaw run-task --config openclaw-pairproxy.yaml --task health_check

# 查看任务历史
openclaw history --task health_check --limit 10
```

### 8.2 PairProxy 系统问题

参考 `docs/TROUBLESHOOTING.md` 和 `docs/FAULT_TOLERANCE_ANALYSIS.md`。

---

## 9. 附录

### 9.1 完整配置示例

完整的配置文件和任务定义已在上文提供。

### 9.2 相关文档

- 用户手册: `docs/manual.md`
- 故障排查: `docs/TROUBLESHOOTING.md`
- 故障容错分析: `docs/FAULT_TOLERANCE_ANALYSIS.md`
- 升级指南: `docs/UPGRADE.md`

### 9.3 联系方式

- 项目仓库: https://github.com/l17728/pairproxy
- Issue 追踪: https://github.com/l17728/pairproxy/issues

---

**文档版本**: 1.0
**最后更新**: 2026-03-08
**维护者**: PairProxy Team
