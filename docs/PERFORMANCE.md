# PairProxy 性能调优指南

本文档介绍影响 PairProxy 吞吐量和延迟的关键配置项，以及适合不同场景的调优建议。

---

## 用量写入缓冲区（write_buffer_size / flush_interval）

每次 LLM 请求完成后，s-proxy 会将 token 用量记录写入 SQLite。为了避免高并发下 SQLite 写锁竞争，采用了**异步批量写入**机制：

```yaml
database:
  write_buffer_size: 200   # 批量写入的记录数阈值
  flush_interval: 5s       # 强制刷新间隔（即使未达到 200 条）
```

### 权衡说明

| 参数 | 调大的效果 | 调大的风险 |
|------|-----------|-----------|
| `write_buffer_size` | 减少写操作次数，降低锁竞争 | 进程崩溃时丢失更多未持久化记录 |
| `flush_interval` | 合并更多写操作为一次 batch INSERT | 崩溃窗口更长；/metrics 指标稍有滞后 |

### 推荐配置

| 场景 | write_buffer_size | flush_interval |
|------|-------------------|----------------|
| 低并发（< 10 用户） | 50（默认 200） | 2s |
| 中等并发（10–50 用户） | 200（默认） | 5s |
| 高并发（50+ 用户） | 500 | 10s |
| 成本敏感（不能丢数据） | 1 | 1s |

> 将 `write_buffer_size=1` + `flush_interval=1s` 等同于每请求同步写入，适合测试但生产环境不推荐。

---

## SQLite WAL 模式

PairProxy 默认启用 [WAL（Write-Ahead Logging）](https://www.sqlite.org/wal.html) 模式，配置如下：

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA cache_size=-64000;   -- 64MB 页缓存
```

### WAL 模式的优势

- **读写并发**：读操作不阻塞写操作，写操作不阻塞读操作（Dashboard 查询与请求代理互不干扰）
- **更低写延迟**：WAL 追加写比传统 journal 模式快 2–10x
- **`NORMAL` 同步级别**：WAL 模式下 `NORMAL` 提供充分的数据安全性，同时比 `FULL` 快约 30%

### WAL 模式的限制

| 限制 | 说明 |
|------|------|
| 单一写入者 | SQLite WAL 仍然是单写并发；PairProxy 通过单 goroutine UsageWriter 规避竞争 |
| 网络文件系统 | **不支持**在 NFS/SMB 上使用 WAL 模式；如需多节点共享存储，请使用独立数据库 + 数据聚合 |
| 数据库文件数量 | WAL 模式会创建 `.db-wal` 和 `.db-shm` 辅助文件，需一同备份 |
| 内存数据库 | `:memory:` 不支持 WAL 模式（测试用途不受影响） |

### 判断 WAL 是否生效

```bash
sqlite3 pairproxy.db "PRAGMA journal_mode;"
# 输出: wal
```

---

## 连接池配置（max_connections）

s-proxy 使用 GORM + `glebarez/sqlite`（纯 Go，不依赖 CGO）。连接池通过底层 `database/sql` 配置：

```go
// 当前硬编码配置（internal/db/db.go）:
sqlDB.SetMaxOpenConns(1)   // WAL 下写串行，1个写连接足够
sqlDB.SetMaxIdleConns(4)   // 保留4个读连接复用
```

### 为什么限制为 1 个写连接？

SQLite 在同一时刻只允许一个写事务。即使开启 WAL，多个 goroutine 同时写入也会串行排队（由 `busy_timeout=5000` 控制等待）。因此：

- **写连接设为 1**：避免无效的连接竞争，直接让 GORM 层串行
- **读连接设为 4**：Dashboard 查询和 /metrics 等读操作可并发

### 高并发读场景

如果 Dashboard 或 /api/admin/ 的查询响应较慢，可调大读连接数：

```go
// 修改 internal/db/db.go:
sqlDB.SetMaxIdleConns(8)    // 提升并发读性能
```

> 注意：增加连接数会略微增加内存占用（每连接约 64KB 缓存）。

---

## 配额缓存（quota_cache）

配额检查对每次请求都需要查询用户的已用 token 数。为避免每次都查 DB，使用内存缓存：

```yaml
quota:
  cache_ttl: 60s   # 缓存有效期（默认60秒）
```

### 缓存命中率监控

通过 `/metrics` 端点查看：

```
pairproxy_quota_cache_hits_total 1250
pairproxy_quota_cache_misses_total 45
```

### 权衡

| cache_ttl | 优点 | 缺点 |
|-----------|------|------|
| 短（< 30s） | 配额变更后生效快 | DB 查询频繁 |
| 长（> 120s） | DB 压力小，命中率高 | 配额修改最多 2min 后生效 |

**建议**：默认 60s 适合大多数场景。如用户数量较多（> 100），可适当调大到 120s。

---

## 路由缓存内存占用

c-proxy 在本地缓存 s-proxy 的路由表，避免每次请求都查询：

```yaml
# cproxy.yaml
sproxy:
  lb_strategy: round_robin
  targets:
    - url: "http://proxy1.company.com:9000"
      weight: 1
```

路由表持久化在 `~/.config/pairproxy/*.routing/` 目录，每次启动自动加载。

### 内存估算

| 组件 | 每条目内存 | 备注 |
|------|-----------|------|
| 路由表条目 | ~200 bytes | 含 ID、地址、权重、健康状态 |
| 配额缓存条目 | ~100 bytes | 含 UserID、用量、TTL |
| 用量 buffer | ~300 bytes/条 | UsageLog 结构体 |

**典型配置内存开销**：
- 10 个 s-proxy 节点：~2KB（路由表）
- 100 个活跃用户：~10KB（配额缓存）
- 200 条 buffer：~60KB（用量写入缓冲）

总计远低于 1MB，内存不是 PairProxy 的瓶颈。

---

## 代理延迟分析

PairProxy 在代理链路上引入的额外延迟主要来自：

| 环节 | 典型延迟 | 说明 |
|------|---------|------|
| JWT 验证（s-proxy） | < 0.1ms | 纯内存操作 |
| 配额检查（缓存命中） | < 0.1ms | sync.Map 读 |
| 配额检查（缓存未命中） | 1–5ms | SQLite 查询 |
| 用量记录（异步） | 0ms | 不阻塞响应 |
| 网络转发开销 | < 1ms | 本地网络 |

**总额外延迟**：< 5ms（正常情况 < 1ms）。LLM 响应延迟通常在 500ms–30s，代理开销可忽略不计。

---

## 高并发建议

当服务 50+ 并发用户时：

1. **增大 `write_buffer_size`** 到 500，减少批量写入频率
2. **增大 `quota.cache_ttl`** 到 120s，减少 DB 读查询
3. **监控 `/metrics`**：关注 `pairproxy_quota_cache_misses_total` 增长趋势
4. **WAL checkpoint**：长时间运行后可手动触发：

   ```bash
   sqlite3 pairproxy.db "PRAGMA wal_checkpoint(TRUNCATE);"
   ```

5. **数据库文件大小**：通过 `pairproxy_database_size_bytes` 监控，超过 1GB 时考虑清理旧日志：

   ```bash
   # 删除 90 天前的用量日志（需自定义）
   sqlite3 pairproxy.db "DELETE FROM usage_logs WHERE created_at < datetime('now', '-90 days');"
   sqlite3 pairproxy.db "VACUUM;"
   ```

---

## PostgreSQL 模式性能（v2.13.0+）

v2.13.0 起支持 PostgreSQL 替代 SQLite，对性能影响如下：

### PostgreSQL vs SQLite 性能对比

| 方面 | SQLite | PostgreSQL |
|------|--------|------------|
| 写入吞吐 | 单写者，批量 200条/批 | 多写者并发，默认连接池 25 |
| 读取延迟 | 文件本地读取，< 1ms | 网络延迟，本机约 1-3ms |
| 多节点一致性 | Worker 异步上报（窗口期） | 实时一致（共享 PG） |
| 适用规模 | < 50 用户 | 50+ 用户，多节点 |

### PostgreSQL 连接池调优

```yaml
database:
  driver: postgres
  dsn: "${PG_DSN}"
  max_open_conns: 25       # 默认 25，高并发可增大
  max_idle_conns: 5        # 建议为 max_open_conns 的 20%
  conn_max_lifetime: 1h    # 超过 1 小时的连接自动关闭
```

### PgBouncer 建议（高并发 100+ 用户）

在 PostgreSQL 前置 PgBouncer 连接池代理，可减少 PG 连接开销：

```
sproxy → PgBouncer (pool_mode=transaction) → PostgreSQL
```

- `pool_mode=transaction`：最节省连接数，适合短事务
- `max_client_conn`: 设为 sproxy 节点数 × `max_open_conns`

---

## 语义路由性能（v2.18.0+）

### 分类延迟

语义路由在每个用户请求前增加一次 LLM 分类调用，额外延迟取决于：

| 因素 | 典型值 |
|------|--------|
| 分类器 LLM 延迟（本机） | 200-500ms（小模型） |
| 分类器超时 | 5s（可配置） |
| 降级决策开销 | < 1ms |

> ⚠️ **语义路由会增加请求总延迟**。若对延迟敏感，建议使用分类准确但速度快的轻量模型作为分类器（如 claude-haiku 系列）。

### 优化建议

1. **使用轻量分类器**：将 `semantic_router.classifier_url` 指向专用的轻量 LLM Target
   ```yaml
   semantic_router:
     classifier_url: "http://localhost:9000"   # 使用本机，避免额外网络开销
   ```

2. **合理设置超时**：
   ```yaml
   semantic_router:
     classifier_timeout: 3s   # 根据分类器速度调整，过长影响用户体验
   ```

3. **仅对需要的用户启用**：语义路由仅对无 LLM 绑定的用户生效；
   给高频用户显式绑定 LLM（`sproxy admin llm bind`）可跳过分类开销。

4. **规则数量控制**：规则越少，优先级排序越快；建议控制在 20 条以内。

### 语义路由 Metrics

```
# HELP pairproxy_semantic_router_classifications_total 语义路由分类请求总数
pairproxy_semantic_router_classifications_total{result="hit"} 1234
pairproxy_semantic_router_classifications_total{result="fallback"} 56

# HELP pairproxy_semantic_router_classification_duration_ms 语义路由分类耗时（毫秒）
pairproxy_semantic_router_classification_duration_ms{quantile="0.5"} 320
pairproxy_semantic_router_classification_duration_ms{quantile="0.99"} 1850
```

监控建议：`fallback` 占比 > 10% 时，检查分类器健康状态和超时配置。

---

## 训练语料采集性能（v2.16.0+）

语料采集为**异步写入**，对主流程延迟影响 < 0.1ms。

主要性能影响在于**磁盘 I/O**：

| 请求量 | 语料文件写入速率 | 磁盘占用（估算）|
|--------|---------------|---------------|
| 100 req/min | ~100 次磁盘追加写 | 约 1-10 MB/min |
| 1000 req/min | ~1000 次磁盘追加写 | 约 10-100 MB/min |

优化建议：
- 将 `corpus.output_dir` 配置在 SSD 挂载点
- 对磁盘空间设置监控告警（≥ 80% 时告警）
- 定期运行 `sproxy admin corpus` 清理旧文件

---

## 内存占用更新（v2.18.0）

v2.18.0 新增模块的内存开销：

| 组件 | 每条目内存 | 备注 |
|------|-----------|------|
| 语义路由规则 | ~500 bytes/条 | 含 description 字符串 |
| 语义路由 LRU 缓存 | ~1 KB/条 | 相同 messages 前缀命中缓存（如有配置）|
| 训练语料队列 | ~1 KB/条 | 异步写入队列，与 usage buffer 类似 |

**典型 v2.18.0 部署额外内存**（基于 v2.9.0 基线）：
- 20 条语义路由规则：~10 KB
- 训练语料队列（1000 条）：~1 MB
- 总计额外：< 5 MB（可忽略）
