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
