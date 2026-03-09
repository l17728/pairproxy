# PairProxy 代码审查建议实施计划

**审查报告来源**: pairproxy-review.md (OpenClaw Assistant)
**审查日期**: 2026-03-09
**计划制定日期**: 2026-03-09
**当前版本**: v2.7.0

---

## 一、审查建议总览

审查报告提出了 10 个改进建议，分为三个优先级：

- 🔴 **Critical (3 项)** - 安全/合规风险，需要尽快处理
- 🟡 **Important (3 项)** - 影响可靠性和扩展性
- 🟢 **Quick Wins (4 项)** - 改善用户体验和可观测性

---

## 二、建议分析与实施计划

### 🔴 Critical #1: 对话追踪的隐私和合规风险

#### 问题描述
- **当前状态**: 对话内容以明文 JSON 存储，文件权限 `0644`（所有用户可读）
- **风险**: GDPR/PIPL 违规，服务器被攻破时数据泄露
- **代码位置**: `internal/track/capture.go:134`
  ```go
  _ = os.WriteFile(path, data, 0o644)  // ❌ 所有用户可读
  ```

#### 实施方案

**Phase 1: 修复文件权限（立即）**
```go
// 修改为 0600（仅所有者可读写）
_ = os.WriteFile(path, data, 0o600)
```

**Phase 2: 添加加密支持（1-2 周）**
```go
// 配置项
type TrackConfig struct {
    Enabled           bool
    OutputDir         string
    EncryptionEnabled bool   // 新增
    EncryptionKey     string // 从环境变量读取
    RetentionDays     int    // 新增，自动删除
}

// 加密实现
func encryptContent(plaintext []byte, key []byte) ([]byte, error) {
    // AES-256-GCM 加密
    block, _ := aes.NewCipher(key)
    gcm, _ := cipher.NewGCM(block)
    nonce := make([]byte, gcm.NonceSize())
    rand.Read(nonce)
    return gcm.Seal(nonce, nonce, plaintext, nil), nil
}
```

**Phase 3: 自动清理（1 周）**
```go
// 定期清理过期文件
func (t *Tracker) startRetentionCleanup(ctx context.Context) {
    ticker := time.NewTicker(24 * time.Hour)
    go func() {
        for {
            select {
            case <-ticker.C:
                t.cleanupExpiredFiles()
            case <-ctx.Done():
                return
            }
        }
    }()
}
```

**Phase 4: 文档和警告（立即）**
- 在 `SECURITY.md` 中添加警告
- 在 `sproxy.yaml.example` 中添加注释
- CLI 启用时显示警告信息

#### 优先级
**P0 - 立即实施**

#### 工作量
- Phase 1: 0.5 天（修改权限 + 测试）
- Phase 2: 2-3 天（加密实现 + 测试）
- Phase 3: 1 天（自动清理 + 测试）
- Phase 4: 0.5 天（文档更新）
- **总计**: 4-5 天

#### 验收标准
- [ ] 文件权限改为 0600
- [ ] 支持 AES-256-GCM 加密（可选）
- [ ] 支持自动清理（retention_days 配置）
- [ ] SECURITY.md 包含合规警告
- [ ] CLI 启用时显示明确警告
- [ ] 测试覆盖率 ≥ 80%

---

### 🔴 Critical #2: SQLite 扩展性上限

#### 问题描述
- **当前状态**: 单个 SQLite 数据库处理所有用量日志
- **瓶颈**: 200+ 用户时写入竞争严重
- **影响**: 响应延迟增加，可能丢失用量数据

#### 实施方案

**Phase 1: 抽象数据库接口（2-3 周）**
```go
// 定义通用接口
type UsageStore interface {
    RecordUsage(log *UsageLog) error
    QueryUsage(filter UsageFilter) ([]*UsageLog, error)
    GetStats(userID string, from, to time.Time) (*UsageStats, error)
}

// SQLite 实现
type SQLiteUsageStore struct { ... }

// PostgreSQL 实现
type PostgreSQLUsageStore struct { ... }
```

**Phase 2: 添加 PostgreSQL 支持（2-3 周）**
```yaml
# sproxy.yaml
database:
  type: sqlite  # 或 postgres

  # SQLite 配置
  sqlite:
    path: ./pairproxy.db

  # PostgreSQL 配置
  postgres:
    host: localhost
    port: 5432
    database: pairproxy
    user: pairproxy
    password: ${POSTGRES_PASSWORD}
    max_connections: 50
```

**Phase 3: 性能测试和文档（1 周）**
- 压力测试：50/100/200/500 用户
- 性能对比报告
- 迁移指南

#### 优先级
**P1 - 3-6 个月内实施**

#### 工作量
- Phase 1: 2-3 周（接口抽象 + 重构）
- Phase 2: 2-3 周（PostgreSQL 实现 + 测试）
- Phase 3: 1 周（性能测试 + 文档）
- **总计**: 5-7 周

#### 验收标准
- [ ] UsageStore 接口定义完整
- [ ] SQLite 实现保持向后兼容
- [ ] PostgreSQL 实现功能完整
- [ ] 性能测试报告（50/100/200/500 用户）
- [ ] 迁移指南文档
- [ ] 测试覆盖率 ≥ 85%

---

### 🔴 Critical #3: JWT 黑名单是内存的

#### 问题描述
- **当前状态**: JWT 黑名单存储在内存中
- **问题**: sproxy 重启后黑名单丢失，被吊销的用户可以继续访问（直到 Token 自然过期）
- **风险**: 安全漏洞，最长 24 小时的访问窗口

#### 实施方案

**方案 A: 持久化到 SQLite（推荐）**
```go
// 新增表
type JWTBlacklist struct {
    JTI       string    `gorm:"primarykey"`
    ExpiresAt time.Time `gorm:"index"`
    CreatedAt time.Time
}

// 修改 Blacklist 实现
type Blacklist struct {
    db     *gorm.DB  // 新增
    cache  sync.Map  // 内存缓存，加速查询
    logger *zap.Logger
}

func (b *Blacklist) Add(jti string, expiry time.Time) {
    // 1. 写入数据库
    b.db.Create(&JWTBlacklist{JTI: jti, ExpiresAt: expiry})

    // 2. 更新内存缓存
    b.cache.Store(jti, expiry)
}

func (b *Blacklist) IsBlocked(jti string) bool {
    // 1. 先查内存缓存
    if val, ok := b.cache.Load(jti); ok {
        return time.Now().Before(val.(time.Time))
    }

    // 2. 查数据库
    var entry JWTBlacklist
    err := b.db.Where("jti = ? AND expires_at > ?", jti, time.Now()).First(&entry).Error
    if err == nil {
        b.cache.Store(jti, entry.ExpiresAt)
        return true
    }
    return false
}
```

**方案 B: 使用 Redis（可选，集群场景）**
```go
type RedisBlacklist struct {
    client *redis.Client
    logger *zap.Logger
}

func (b *RedisBlacklist) Add(jti string, expiry time.Time) {
    ttl := time.Until(expiry)
    b.client.Set(ctx, "blacklist:"+jti, "1", ttl)
}
```

**方案 C: 降低 Token TTL（临时方案）**
```yaml
# sproxy.yaml
auth:
  access_token_ttl: 1h  # 从 24h 降低到 1h
```

#### 优先级
**P0 - 立即实施（方案 A 或 C）**

#### 工作量
- 方案 A: 2-3 天（SQLite 持久化 + 测试）
- 方案 B: 3-4 天（Redis 集成 + 测试）
- 方案 C: 0.5 天（配置调整 + 文档）
- **推荐**: 先实施方案 C（临时），再实施方案 A（长期）

#### 验收标准
- [ ] JWT 黑名单持久化到数据库
- [ ] sproxy 重启后黑名单仍然有效
- [ ] 内存缓存加速查询（< 1ms）
- [ ] 自动清理过期条目
- [ ] 测试覆盖率 ≥ 90%
- [ ] SECURITY.md 更新说明

---

### 🟡 Important #4: 配额执行是 Fail-Open

#### 问题描述
- **当前状态**: 数据库不可达时，允许请求通过（优先可用性）
- **风险**: 数据库故障期间无法限流，可能产生巨额费用

#### 实施方案

**添加配置选项**
```yaml
# sproxy.yaml
quota:
  fail_closed: false  # 默认 false（优先可用性）
                      # true = 数据库故障时拒绝请求
```

**代码实现**
```go
func (c *Checker) Check(userID string, tokens int) error {
    usage, err := c.repo.GetUsage(userID)
    if err != nil {
        if c.failClosed {
            // Fail-closed: 拒绝请求
            return fmt.Errorf("quota check failed: %w", err)
        }
        // Fail-open: 允许请求（当前行为）
        c.logger.Warn("quota check failed, allowing request",
            zap.String("user", userID),
            zap.Error(err))
        return nil
    }
    // 正常检查逻辑...
}
```

#### 优先级
**P2 - 1-2 个月内实施**

#### 工作量
- 1-2 天（配置项 + 实现 + 测试 + 文档）

#### 验收标准
- [ ] 支持 `quota.fail_closed` 配置
- [ ] 默认值为 false（向后兼容）
- [ ] 文档说明权衡（可用性 vs 成本控制）
- [ ] 测试覆盖两种模式

---

### 🟡 Important #5: 单主节点是单点故障

#### 问题描述
- **当前状态**: Primary 节点负责路由表分发和用量聚合
- **问题**: Primary 宕机后，Worker 继续服务但用量数据分散
- **影响**: 用量统计不准确，直到 Primary 恢复

#### 实施方案

**方案 A: 文档化限制（推荐，短期）**
```markdown
# docs/CLUSTER_DESIGN.md

## 单主节点限制

### 当前设计
- Primary 节点是路由表的唯一来源
- Primary 宕机后：
  ✅ Worker 继续服务（使用缓存的路由表）
  ❌ 用量数据分散到各 Worker
  ❌ 无自动主节点选举

### 缓解措施
1. 监控 Primary 健康状态（Prometheus + Alertmanager）
2. 快速重启流程（systemd auto-restart）
3. 定期备份数据库
4. 文档化故障恢复步骤

### 未来增强
- Raft 共识算法实现主节点选举
- 或：多主架构（需要分布式锁）
```

**方案 B: Raft 主节点选举（长期）**
- 使用 etcd 或 Consul 实现
- 工作量：4-6 周
- 优先级：P3（未来增强）

#### 优先级
**P2 - 文档化（立即），Raft 实现（6-12 个月）**

#### 工作量
- 方案 A: 1 天（文档 + 监控指南）
- 方案 B: 4-6 周（Raft 实现）

#### 验收标准
- [ ] CLUSTER_DESIGN.md 明确说明限制
- [ ] 提供监控和告警配置示例
- [ ] 文档化故障恢复流程
- [ ] （可选）Raft 实现主节点选举

---

### 🟡 Important #6: 没有测试覆盖率可见性

#### 问题描述
- **当前状态**: 有测试，但没有覆盖率报告
- **影响**: 无法评估测试质量

#### 实施方案

**添加覆盖率徽章**
```markdown
# README.md
[![Test Coverage](https://codecov.io/gh/l17728/pairproxy/branch/main/graph/badge.svg)](https://codecov.io/gh/l17728/pairproxy)
```

**CI 集成**
```yaml
# .github/workflows/test.yml
- name: Generate coverage
  run: go test -coverprofile=coverage.out ./...

- name: Upload to Codecov
  uses: codecov/codecov-action@v3
  with:
    file: ./coverage.out
```

**本地查看**
```bash
make test-cover
# 生成 HTML 报告
go tool cover -html=coverage.out -o coverage.html
```

#### 优先级
**P3 - 1 个月内实施**

#### 工作量
- 1 天（CI 配置 + 徽章 + 文档）

#### 验收标准
- [ ] README 显示覆盖率徽章
- [ ] CI 自动生成覆盖率报告
- [ ] 核心包覆盖率 ≥ 70%（auth, proxy, quota, cluster）

---

### 🟢 Quick Win #7: 添加 cproxy 健康检查

#### 实施方案
```bash
# 新增命令
cproxy status --verbose

# 输出示例
✓ JWT Token: Valid (expires in 2h 15m)
✓ SProxy Connectivity: OK (http://localhost:9000)
✓ Routing Table: Version 42 (3 targets)
✗ Last Request: Failed (connection timeout)
```

#### 优先级
**P3 - 2-3 个月内实施**

#### 工作量
- 1-2 天

---

### 🟢 Quick Win #8: 限流管理 API

#### 实施方案
```go
// 添加中间件
func RateLimitAdmin(next http.Handler) http.Handler {
    limiter := rate.NewLimiter(rate.Every(time.Second), 60) // 60 req/s
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !limiter.Allow() {
            http.Error(w, "Too Many Requests", 429)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

#### 优先级
**P3 - 2-3 个月内实施**

#### 工作量
- 1 天

---

### 🟢 Quick Win #9: 添加请求追踪 ID

#### 实施方案
```go
// cproxy 生成 Request ID
requestID := uuid.NewString()
req.Header.Set("X-Request-ID", requestID)

// sproxy 传播
upstreamReq.Header.Set("X-Request-ID", req.Header.Get("X-Request-ID"))

// 日志记录
logger.Info("request processed",
    zap.String("request_id", requestID),
    zap.String("user", userID))
```

#### 优先级
**P2 - 1-2 个月内实施**

#### 工作量
- 2-3 天

---

### 🟢 Quick Win #10: 文档化陷阱

#### 实施方案
创建 `docs/GOTCHAS.md`：
```markdown
# 运维陷阱

## JWT Secret 轮换
轮换 JWT secret 会立即使所有 Token 失效。
需要维护窗口，用户必须重新登录。

## SQLite WAL 文件
崩溃后可能需要手动清理：
  rm pairproxy.db-wal pairproxy.db-shm

## SSE 流式传输
需要禁用代理缓冲：
  nginx: proxy_buffering off;
  Caddy: flush_interval -1
```

#### 优先级
**P3 - 1 个月内实施**

#### 工作量
- 0.5 天

---

## 三、实施优先级总结

### 立即实施（0-2 周）
1. ✅ **对话追踪文件权限** (0.5 天) - P0
2. ✅ **JWT 黑名单持久化** (2-3 天) - P0
3. ✅ **降低 Token TTL** (0.5 天) - P0 临时方案
4. ✅ **文档化单主节点限制** (1 天) - P2

**总工作量**: 4-5 天

### 短期实施（1-2 个月）
5. ✅ **对话追踪加密** (2-3 天) - P0
6. ✅ **对话追踪自动清理** (1 天) - P0
7. ✅ **配额 Fail-Closed 选项** (1-2 天) - P2
8. ✅ **请求追踪 ID** (2-3 天) - P2
9. ✅ **测试覆盖率徽章** (1 天) - P3
10. ✅ **GOTCHAS.md** (0.5 天) - P3

**总工作量**: 8-12 天

### 中期实施（3-6 个月）
11. ✅ **SQLite → PostgreSQL 抽象** (5-7 周) - P1
12. ✅ **cproxy 健康检查** (1-2 天) - P3
13. ✅ **管理 API 限流** (1 天) - P3

**总工作量**: 6-8 周

### 长期规划（6-12 个月）
14. ✅ **Raft 主节点选举** (4-6 周) - P3

---

## 四、资源需求

### 人力
- **核心开发**: 1 人全职
- **测试**: 0.5 人（兼职）
- **文档**: 0.5 人（兼职）

### 时间线
- **Phase 1** (立即): 1 周
- **Phase 2** (短期): 2-3 周
- **Phase 3** (中期): 6-8 周
- **Phase 4** (长期): 4-6 周

**总计**: 约 4-5 个月

---

## 五、风险评估

### 高风险项
1. **SQLite → PostgreSQL 迁移** - 可能影响现有部署
   - 缓解：保持 SQLite 为默认，PostgreSQL 为可选

2. **JWT 黑名单持久化** - 可能影响性能
   - 缓解：内存缓存 + 数据库持久化

### 中风险项
3. **对话追踪加密** - 可能影响性能
   - 缓解：加密为可选功能

### 低风险项
4. 其他改进 - 向后兼容，风险可控

---

## 六、成功指标

### 安全性
- [ ] 对话追踪文件权限 0600
- [ ] JWT 黑名单持久化
- [ ] 合规警告文档完整

### 可靠性
- [ ] 单主节点限制文档化
- [ ] 配额 Fail-Closed 选项可用
- [ ] 请求追踪 ID 实现

### 可扩展性
- [ ] PostgreSQL 支持
- [ ] 性能测试报告（200+ 用户）

### 可观测性
- [ ] 测试覆盖率 ≥ 70%
- [ ] 覆盖率徽章显示
- [ ] GOTCHAS.md 完整

---

## 七、下一步行动

### 本周（Week 1）
- [ ] 修复对话追踪文件权限（0644 → 0600）
- [ ] 实现 JWT 黑名单 SQLite 持久化
- [ ] 降低默认 Token TTL（24h → 1h）
- [ ] 更新 SECURITY.md 和 CLUSTER_DESIGN.md

### 下周（Week 2-3）
- [ ] 实现对话追踪加密
- [ ] 实现对话追踪自动清理
- [ ] 添加配额 Fail-Closed 选项
- [ ] 创建 GOTCHAS.md

### 下月（Month 2-3）
- [ ] 实现请求追踪 ID
- [ ] 添加测试覆盖率徽章
- [ ] 开始 PostgreSQL 抽象设计

---

**文档版本**: v1.0
**最后更新**: 2026-03-09
**负责人**: 待定
