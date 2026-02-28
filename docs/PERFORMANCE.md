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
