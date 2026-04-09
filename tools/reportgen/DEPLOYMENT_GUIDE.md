# Reportgen 部署指南

## 架构特点

Reportgen 是一个**完全无状态的、纯数据读取工具**，具有以下特点：

### ✅ 不需要本地 YAML 配置文件

- **零本地配置**: 工具仅通过命令行参数接收所有必要信息
- **无依赖**: 不读取 `config.yaml`、`config.toml` 或其他本地配置文件
- **可单独部署**: 可以在任何机器上独立运行，无需 pairproxy 完整配置

### ✅ 无主从架构限制

- **无中心限制**: 不需要连接到特定的主节点
- **任意节点可用**: 只要能连接到数据库，任何安装了网关的服务器都可以执行
- **水平扩展友好**: 可以在多个节点上并行运行，互不影响

---

## 部署模式

### 模式 1: SQLite 本地数据库

**场景**: 小型部署、单机测试、开发环境

```bash
# 在任何机器上执行（只需要二进制 + SQLite 文件）
./reportgen -db /path/to/pairproxy.db -from 2026-04-01 -to 2026-04-07
```

**要求**:
- ✅ 有读取权限的 SQLite 文件
- ✅ 二进制文件
- ❌ 不需要 YAML 配置
- ❌ 不需要其他依赖

---

### 模式 2: PostgreSQL 中央数据库（推荐生产环境）

**场景**: 生产环境、多节点部署、集中数据管理

#### A. DSN 连接方式

```bash
# 在任何能访问数据库的服务器上执行
./reportgen \
  -pg-dsn "postgres://user:password@db-server:5432/pairproxy" \
  -from 2026-04-01 -to 2026-04-07 \
  -output /path/to/report.html
```

#### B. 独立参数方式

```bash
# 通过环境变量或配置管理工具传入参数
./reportgen \
  -pg-host ${DB_HOST} \
  -pg-port ${DB_PORT} \
  -pg-user ${DB_USER} \
  -pg-password ${DB_PASSWORD} \
  -pg-dbname ${DB_NAME} \
  -pg-sslmode require \
  -from 2026-04-01 -to 2026-04-07
```

**要求**:
- ✅ 网络访问到 PostgreSQL 服务器（任何节点）
- ✅ 二进制文件
- ✅ 数据库凭证（可通过环境变量注入）
- ❌ 不需要 YAML 配置
- ❌ 不需要与特定主节点绑定

---

## 多节点部署示例

### 场景: 生产集群有 3 个网关节点，1 个 PostgreSQL 数据库

```
┌─────────────────┐
│  PostgreSQL     │
│  (中央数据库)    │
└────────┬────────┘
         │
    ┌────┴────┬────────┬────────┐
    │         │        │        │
┌───▼──┐ ┌───▼──┐ ┌───▼──┐    
│Gate  │ │Gate  │ │Gate  │ (网关节点1-3)
│way 1 │ │way 2 │ │way 3 │    
└──┬───┘ └──┬───┘ └──┬───┘
   │        │       │
   └────────┴───┬───┘
        reportgen 工具
      (可在任意节点执行)
```

**执行方式（三选一）**:

```bash
# 选项 1: 在网关节点 1 上执行
ssh gateway1@192.168.1.10 << 'EOF'
./reportgen -pg-dsn "postgres://app:secret@db.local:5432/pairproxy" \
  -from 2026-04-01 -to 2026-04-07 -output /reports/weekly.html
EOF

# 选项 2: 在网关节点 2 上执行（完全相同的命令）
ssh gateway2@192.168.1.11 << 'EOF'
./reportgen -pg-dsn "postgres://app:secret@db.local:5432/pairproxy" \
  -from 2026-04-01 -to 2026-04-07 -output /reports/weekly.html
EOF

# 选项 3: 在网关节点 3 上执行（完全相同的命令）
ssh gateway3@192.168.1.12 << 'EOF'
./reportgen -pg-dsn "postgres://app:secret@db.local:5432/pairproxy" \
  -from 2026-04-01 -to 2026-04-07 -output /reports/weekly.html
EOF

# 选项 4: 在完全独立的机器上执行（只要能访问数据库）
./reportgen -pg-dsn "postgres://app:secret@db.local:5432/pairproxy" \
  -from 2026-04-01 -to 2026-04-07 -output /reports/weekly.html
```

**优势**:
- ✅ 工具可在任意节点运行，无单点依赖
- ✅ 可在多个节点并行运行（无冲突）
- ✅ 不需要同步本地配置
- ✅ 易于集成到 CI/CD 和定时任务

---

## CI/CD 集成示例

### Kubernetes CronJob

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: pairproxy-weekly-report
spec:
  schedule: "0 9 * * 1"  # 每周一早上 9 点
  jobTemplate:
    spec:
      template:
        spec:
          serviceAccountName: pairproxy
          containers:
          - name: reportgen
            image: pairproxy/reportgen:v2.24.4
            env:
            - name: DB_HOST
              valueFrom:
                secretKeyRef:
                  name: db-credentials
                  key: host
            - name: DB_USER
              valueFrom:
                secretKeyRef:
                  name: db-credentials
                  key: user
            - name: DB_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: db-credentials
                  key: password
            command:
            - /bin/sh
            - -c
            - |
              reportgen \
                -pg-host ${DB_HOST} \
                -pg-user ${DB_USER} \
                -pg-password ${DB_PASSWORD} \
                -pg-dbname pairproxy \
                -pg-sslmode require \
                -from $(date -d '7 days ago' +%Y-%m-%d) \
                -to $(date +%Y-%m-%d) \
                -output /reports/weekly-$(date +%Y%m%d).html
            volumeMounts:
            - name: reports
              mountPath: /reports
          volumes:
          - name: reports
            persistentVolumeClaim:
              claimName: reports-pvc
          restartPolicy: OnFailure
```

### Docker Compose

```yaml
version: '3.8'

services:
  reportgen-weekly:
    image: pairproxy/reportgen:v2.24.4
    environment:
      PG_DSN: postgres://app:${DB_PASSWORD}@postgres:5432/pairproxy
    volumes:
      - reports:/reports
    command: |
      reportgen
        -pg-dsn postgres://app:${DB_PASSWORD}@postgres:5432/pairproxy
        -from 2026-04-01
        -to 2026-04-07
        -output /reports/weekly.html
    depends_on:
      - postgres
    profiles:
      - manual  # 手动执行: docker-compose --profile manual up

  postgres:
    image: postgres:15
    environment:
      POSTGRES_DB: pairproxy
      POSTGRES_USER: app
      POSTGRES_PASSWORD: ${DB_PASSWORD}
    volumes:
      - postgres_data:/var/lib/postgresql/data

volumes:
  reports:
  postgres_data:
```

### 定时任务 (Cron)

```bash
# /etc/cron.d/pairproxy-reports
# 每周一 9:00 生成报告
0 9 * * 1 reportgen /usr/local/bin/reportgen \
  -pg-host db.internal \
  -pg-user app \
  -pg-password $(cat /etc/pairproxy/db-password) \
  -pg-dbname pairproxy \
  -from $(date -d '7 days ago' +\%Y-\%m-\%d) \
  -to $(date +\%Y-\%m-\%d) \
  -output /var/www/reports/weekly-$(date +\%Y\%m\%d).html
```

---

## 环境配置

### 可选: LLM 智能洞察

如果要启用 LLM 智能洞察（使用 Anthropic 或 OpenAI），需要设置：

```bash
export KEY_ENCRYPTION_KEY="your-encryption-key"
./reportgen -pg-dsn "..." -from ... -to ...
```

**注意**: 这是可选的，不设置时工具仍然可以生成完整报告（不含 LLM 洞察）

---

## 网络拓扑建议

### 小型部署（<1M 日志）
```
单机 SQLite
└─ reportgen (本地)
```

### 中型部署（>1M 日志、多节点）
```
PostgreSQL 主从
├─ 从库 (读取)
│  └─ reportgen (任意网关节点)
└─ 主库 (写入，由网关使用)
```

**注意**: reportgen 只读数据库，可连接从库以避免影响主库性能

### 大型部署（>100M 日志、分析数据库）
```
PostgreSQL 生产数据库 (OLTP)
└─ ETL 同步
   └─ PostgreSQL 分析库 (OLAP)
      └─ reportgen (任意节点)
```

---

## 故障排查

### 问题 1: 连接数据库失败

**症状**: `error: failed to open database`

**检查清单**:
- ✅ 网络连通性: `ping db-host`
- ✅ 防火墙规则: `telnet db-host 5432`
- ✅ 数据库凭证: 用户名、密码、数据库名正确
- ✅ 服务器时间同步（某些 SSL 证书对时间敏感）

### 问题 2: 报告生成慢

**症状**: 生成大时间范围报告时超时

**优化方案**:
- 缩小日期范围
- 在 PostgreSQL 上为 `usage_logs(created_at)` 添加索引
- 在数据库从库上运行（避免主库压力）

### 问题 3: SQLite 锁定错误

**症状**: `database is locked`

**原因**: SQLite 在高并发场景下的限制

**解决方案**: 迁移到 PostgreSQL（v2.24.2 已支持）

---

## 最佳实践

### ✅ Do 优先采纳

1. **使用 PostgreSQL** (生产环境)
   - 支持高并发
   - 可在多个节点运行
   - 支持从库读取

2. **用环境变量注入敏感信息**
   ```bash
   export DB_PASSWORD="secret"
   ./reportgen -pg-password ${DB_PASSWORD} ...
   ```

3. **在从库上运行** (有主从时)
   ```bash
   ./reportgen -pg-host replica-1.internal ...
   ```

4. **定期备份报告**
   ```bash
   rsync -av /reports/ backup-server:/reports-archive/
   ```

### ❌ Avoid 避免做法

1. **不要** 把数据库密码硬编码在脚本中
2. **不要** 在主库负载高时运行（尤其是大时间范围）
3. **不要** 在多个节点同时写入相同的输出文件
4. **不要** 期望从本地 YAML 文件读取配置（不支持）

---

## 总结

| 特性 | 支持 | 说明 |
|------|------|------|
| 本地 YAML 配置 | ❌ | 全部通过命令行参数或环境变量 |
| 主从架构依赖 | ❌ | 可在任意节点运行，无单点限制 |
| 多节点并行 | ✅ | 完全无状态，支持水平扩展 |
| 数据库选择 | ✅ | SQLite 或 PostgreSQL |
| 网络隔离 | ✅ | 只需网络访问数据库 |
| CI/CD 集成 | ✅ | 支持 K8s、Docker、Cron 等 |

**结论**: Reportgen 是一个云原生、分布式友好的分析工具，可以在任意安装了网关的服务器上执行，无需本地配置或特定的架构依赖。
