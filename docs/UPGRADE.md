# PairProxy 升级指南

本文档描述各版本间的升级步骤、数据库 Schema 变更、回滚方法及不兼容变更。

---

## 通用升级流程

1. **备份数据库**

   ```bash
   cp pairproxy.db pairproxy.db.bak
   ```

2. **停止 s-proxy**

   ```bash
   # systemd
   systemctl stop sproxy

   # 或直接发送 SIGTERM
   kill -TERM <sproxy-pid>
   ```

3. **替换二进制文件**

   ```bash
   cp sproxy-new /usr/local/bin/sproxy
   cp cproxy-new /usr/local/bin/cproxy
   ```

4. **启动 s-proxy**（`db.AutoMigrate` 会自动应用 Schema 变更）

   ```bash
   systemctl start sproxy
   # 或
   sproxy start -c sproxy.yaml
   ```

5. **验证启动**

   ```bash
   curl http://localhost:9000/health
   # 期望: {"status":"ok", ...}
   ```

6. **逐步升级客户端**（cproxy 向后兼容，可分批升级）

---

## 版本变更记录

### v2.5.0 — 可靠性增强（用量可靠性 + 健康检查 + 路由发现 + 请求重试）

**无数据库 Schema 变更**，滚动升级，最小化停机时间。

**集群升级顺序**（推荐）

1. 先升级 worker 节点（sp-2, sp-3）
2. 再升级 primary 节点（sp-1）
3. 最后分批升级 cproxy 客户端

这样可以最小化停机时间，primary 停机期间由 worker 承载流量。

**升级时是否中断请求？**

取决于升级方式：

- **快速升级**（直接 `systemctl stop sproxy`）：**会中断正在处理的请求**
  - 流式请求（SSE）会断开
  - 等待 LLM 响应的请求会失败
  - 停机时间：通常 < 5s

- **优雅升级**（使用排水模式）：**零请求中断**
  ```bash
  ./sproxy admin drain enter          # 停止接受新请求
  ./sproxy admin drain wait --timeout 60s  # 等待活跃请求完成
  systemctl stop sproxy               # 安全停止
  ```
  - 详细步骤见本文档"滚动升级（零停机）"章节

**推荐**：生产环境使用排水模式，开发/测试环境可直接停止。

**新增配置字段**（均有合理默认值，不填写也能正常运行）

`cproxy.yaml` 新增：
```yaml
sproxy:
  health_check_timeout: 3s
  health_check_failure_threshold: 3
  health_check_recovery_delay: 60s
  passive_failure_threshold: 3
  shared_secret: ""          # 空=禁用路由主动轮询
  routing_poll_interval: 60s
  retry:
    enabled: true
    max_retries: 2
    retry_on_status: [502, 503, 504]
```

`sproxy.yaml` 新增（worker 节点）：
```yaml
cluster:
  usage_buffer:
    enabled: true
    max_records_per_batch: 1000
```

**Worker 节点数据库配置**（v2.5.0 新增要求）

如果 worker 节点之前没有配置数据库，需要在 sproxy.yaml 中添加：
```yaml
database:
  path: "/var/lib/pairproxy/worker.db"
```

这是因为 v2.5.0 的 usage_buffer 功能需要 worker 本地数据库来缓存用量记录。

**启用路由表主动发现**

需要在 cproxy.yaml 和 sproxy.yaml 中都配置相同的 shared_secret：

cproxy.yaml:
```yaml
sproxy:
  shared_secret: "your-cluster-secret"
  routing_poll_interval: 60s
```

sproxy.yaml (primary):
```yaml
cluster:
  shared_secret: "your-cluster-secret"
```

⚠️ 两边必须配置相同的密钥，否则鉴权失败。如果只配置一边：
- 只配置 cproxy：轮询请求会因鉴权失败返回 401
- 只配置 sproxy：cproxy 不会启动轮询（shared_secret 为空时禁用）

**cproxy 升级**

- v2.4.0 cproxy 可以继续使用，完全兼容 v2.5.0 sproxy
- 但建议升级以享受新功能：
  - 请求级重试（提升可用性）
  - 路由表主动发现（更快感知节点变化）
  - 健康检查优化（更精准的熔断控制）
- 升级顺序建议：先升级 sproxy，再升级 cproxy（避免 cproxy 轮询不存在的端点）

**升级验证**

1. 验证基本功能：
   ```bash
   curl http://localhost:9000/health
   ```

2. 验证路由表主动发现（查看 cproxy 日志）：
   ```bash
   # 应该看到类似日志：
   # INFO routing poll: sending routing update
   ```

3. 验证请求级重试（模拟节点故障）：
   ```bash
   # 停止一个 worker 节点，发送请求，应该自动重试到其他节点
   ```

4. 验证 worker 用量上报（查看 primary 日志）：
   ```bash
   # 应该看到类似日志：
   # INFO usage records received from peer
   ```

**回滚说明**

降级到 v2.4.0 时，新增配置字段会被忽略，无兼容性问题。

---

### v2.4.0 — 用户对话内容追踪

**无数据库 Schema 变更**，直接替换二进制即可，零停机升级。

**新增文件系统目录**（启动时自动创建，无需手动操作）

```
<db_dir>/track/
├── users/          # 追踪状态标记文件
└── conversations/  # 按用户分目录存储的 JSON 对话记录
```

目录位置：数据库文件（`database.path`）同级目录下的 `track/` 子目录。例如数据库在 `./pairproxy.db`，则追踪目录为 `./track/`。

**新增 CLI 命令**（无需配置文件变更）

```bash
sproxy admin track enable <username>
sproxy admin track disable <username>
sproxy admin track list
sproxy admin track show <username>
sproxy admin track clear <username>
```

**回滚说明**

降级到 v2.3.0 时，`track/` 目录会被保留但不再使用，可手动删除。无数据库兼容性问题。

---

### P3 升级（F-1 ~ F-7）

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `api_keys` | 系统级 API Key（加密存储，F-5） |
| `api_key_assignments` | API Key 分配记录（用户级/分组级，F-5） |

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 | 说明 |
|------|------|------|--------|------|
| `users` | `auth_provider` | TEXT | `'local'` | 认证来源（F-4） |
| `users` | `external_id` | TEXT | `''` | 外部系统 ID（LDAP uid，F-4） |
| `groups` | `max_tokens_per_request` | INTEGER | NULL | 单次最大 token 数（F-3） |
| `groups` | `concurrent_requests` | INTEGER | NULL | 最大并发请求数（F-3） |

所有新列均有默认值，AutoMigrate 自动添加，存量数据完全兼容。

**配置文件新增字段（均为可选，有合理默认值）**

| 字段路径 | 默认值 | 说明 |
|----------|--------|------|
| `auth.provider` | `"local"` | 认证提供者，`"local"` 或 `"ldap"` |
| `auth.ldap.*` | 见示例 | LDAP 配置（provider="ldap" 时生效） |
| `admin.key_encryption_key` | `""` | API Key 加密密钥（F-5） |
| `llm.targets[].provider` | `"anthropic"` | LLM 类型（F-1） |
| `cluster.alert_webhooks` | `[]` | 多 Webhook 告警（F-6） |

---

### Phase 6 → P2 升级

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `audit_logs` | 管理员操作审计日志（P2-3） |

AutoMigrate 会自动创建新表，**无需手动执行 SQL**。

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 |
|------|------|------|--------|
| `groups` | `requests_per_minute` | INTEGER | NULL |
| `usage_logs` | `cost_usd` | REAL | 0 |
| `usage_logs` | `source_node` | TEXT | `'local'` |
| `usage_logs` | `synced` | INTEGER | 0 |

这些列均有默认值，存量数据自动兼容。

**配置文件变更（P2 无破坏性变更）**

无需修改配置文件，所有新特性均自动启用。

---

### Phase 5 → Phase 6 升级

**新增数据库列**

| 表名 | 列名 | 类型 | 默认值 |
|------|------|------|--------|
| `groups` | `requests_per_minute` | INTEGER | NULL |
| `usage_logs` | `cost_usd` | REAL | 0 |

**新增配置字段（可选）**

```yaml
pricing:
  default_input_per_1k: 0.003   # USD/1K input tokens
  default_output_per_1k: 0.015  # USD/1K output tokens
  models:
    claude-3-5-sonnet-20241022:
      input_per_1k: 0.003
      output_per_1k: 0.015

cluster:
  alert_webhook: "https://hooks.slack.com/..."  # 可选
```

---

### Phase 4 → Phase 5 升级

**新增 Admin Dashboard**

在 `sproxy.yaml` 中启用（可选）：

```yaml
dashboard:
  enabled: true

admin:
  username: admin
  password_hash: "$2a$12$..."  # bcrypt hash
```

生成 admin 密码哈希：

```bash
sproxy admin hash-password
```

**新增数据库表**

| 表名 | 说明 |
|------|------|
| `peers` | 集群节点注册表 |

---

### Phase 1 → Phase 2 升级

**无破坏性变更**，新增 `usage_logs` 表（首次启动时自动创建）。

---

## 回滚方法

### 回滚到上一版本二进制

1. 停止服务
2. 恢复旧二进制
3. **如果新版本添加了数据库列**，旧版本通常仍可运行（GORM 只读未知列不报错）
4. **如果新版本添加了数据库表**，旧版本不会报错（表不存在时 AutoMigrate 报错可忽略）
5. 启动旧版本服务

### 完整回滚（含数据库）

如果需要完整回滚数据库：

```bash
# 停止服务
systemctl stop sproxy

# 恢复备份
cp pairproxy.db.bak pairproxy.db

# 恢复旧二进制
cp sproxy-old /usr/local/bin/sproxy

# 启动
systemctl start sproxy
```

> ⚠️ 完整回滚会丢失回滚点之后的所有用量记录。

---

## 不兼容变更清单

### P2 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `NewAdminHandler` 签名新增 `auditRepo` 参数 | 仅影响直接调用此函数的自定义代码 | 传入 `db.NewAuditRepo(logger, database)` |
| `dashboard.NewHandler` 签名新增 `auditRepo` 参数 | 同上 | 同上 |

### Phase 6 引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `GroupRepo.SetQuota` 新增第4参数 `rpm *int` | 自定义代码需更新调用处 | 传入 `nil` 保持不限制 |
| `UsageLog.CostUSD` 新增列 | 旧版本读取不报错 | 无需处理 |

### 集群（Phase 3）引入的不兼容变更

| 变更 | 影响 | 处理方式 |
|------|------|----------|
| `NewCProxy` 第4参数从 `[]SProxyTarget` 改为 `lb.Balancer` | 需更新调用处 | 使用 `lb.NewWeightedRandom(targets)` |
| 内部 API 需要 `cluster.shared_secret` | worker 节点心跳需认证 | 在 sproxy.yaml 中配置 `cluster.shared_secret` |

---

## 常见升级问题

### Q: AutoMigrate 是否安全？

是。GORM AutoMigrate 只会 **新增** 列和表，不会删除或修改现有列。

### Q: 能否零停机升级？

**单节点**：需要短暂停机（通常 < 5s，包含 AutoMigrate 时间）。

**多节点集群**：先升级 primary，再逐步升级 worker。由于 c-proxy 支持多 target 负载均衡，worker 升级期间流量自动切走。

### Q: 如何检查数据库是否正常迁移？

```bash
sqlite3 pairproxy.db ".tables"
# 应包含: audit_logs groups peers refresh_tokens usage_logs users
```

---

## 滚动升级（零停机）

多节点集群支持通过排水（Drain）机制实现零停机滚动升级。

### 滚动升级原理

排水模式允许节点优雅下线：
1. 节点进入排水模式后，不再接受新请求
2. 正在处理的请求继续完成
3. 当活跃请求数归零后，可安全停止节点
4. 升级完成后恢复正常模式

### 单节点升级流程

```bash
# 1. 备份数据库
cp pairproxy.db pairproxy.db.bak

# 2. 进入排水模式
./sproxy admin drain enter

# 3. 等待活跃请求归零
./sproxy admin drain wait --timeout 60s

# 4. 停止服务
systemctl stop sproxy

# 5. 替换二进制
cp sproxy-new /usr/local/bin/sproxy

# 6. 启动服务
systemctl start sproxy

# 7. 验证
curl http://localhost:9000/health
```

### 多节点集群升级流程

假设有 primary (sp-1) 和多个 worker (sp-2, sp-3)：

```bash
# ===== 升级 worker 节点（逐个进行）=====

# 在 sp-2 上执行：
./sproxy admin drain enter
./sproxy admin drain wait --timeout 120s
systemctl stop sproxy
cp sproxy-new /usr/local/bin/sproxy
systemctl start sproxy
curl http://localhost:9000/health

# 在 sp-3 上重复相同步骤...

# ===== 升级 primary 节点 =====

# 在 sp-1 上执行：
./sproxy admin drain enter
./sproxy admin drain wait --timeout 120s
systemctl stop sproxy
cp sproxy-new /usr/local/bin/sproxy
systemctl start sproxy
curl http://localhost:9000/health
```

### 排水命令详解

| 命令 | 说明 |
|------|------|
| `sproxy admin drain enter` | 进入排水模式 |
| `sproxy admin drain exit` | 退出排水模式 |
| `sproxy admin drain status` | 查看排水状态和活跃请求数 |
| `sproxy admin drain wait --timeout 60s` | 等待活跃请求归零 |

### 通过 REST API 操作

```bash
# 进入排水模式
curl -X POST http://localhost:9000/api/admin/drain \
  -H "Authorization: Bearer <admin-token>"

# 查看状态
curl http://localhost:9000/api/admin/drain/status \
  -H "Authorization: Bearer <admin-token>"

# 退出排水模式
curl -X POST http://localhost:9000/api/admin/undrain \
  -H "Authorization: Bearer <admin-token>"
```

### 通过 Dashboard 操作

访问 `/dashboard/` → LLM 管理 → 节点列表 → 点击 "Drain" 按钮

### 注意事项

1. **确保至少有一个健康节点**：排水期间其他节点应能承接流量
2. **设置合理的超时**：`drain wait --timeout` 避免无限等待
3. **长连接请求**：SSE 流式请求可能需要较长时间完成
4. **自动恢复**：节点重启后自动退出排水模式
