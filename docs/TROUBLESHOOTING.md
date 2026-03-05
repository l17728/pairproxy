# PairProxy 故障排查手册

本文档汇总常见问题及排查步骤，覆盖启动失败、认证错误、配额超限、连接问题和性能排查。

---

## 目录

1. [启动失败](#1-启动失败)
2. [JWT 认证错误](#2-jwt-认证错误)
3. [配额超限 / 速率限制](#3-配额超限--速率限制)
4. [无法连接到 s-proxy](#4-无法连接到-s-proxy)
5. [集群节点问题](#5-集群节点问题)
6. [Dashboard / Admin API 问题](#6-dashboard--admin-api-问题)
7. [性能问题](#7-性能问题)
8. [调试命令速查](#8-调试命令速查)
9. [日志文件位置](#9-日志文件位置)
10. [LLM Extended Thinking / 长流连接断开](#10-llm-extended-thinking--长流连接断开)

---

## 1. 启动失败

### 1.1 配置验证失败

**症状**：`sproxy start` 立即退出，输出类似：

```
config validation failed:
  - auth.jwt_secret is required
  - llm.targets must not be empty
```

**排查步骤**：

1. 确认 `sproxy.yaml` 中所有必填字段已填写：
   - `auth.jwt_secret`（建议通过 `${JWT_SECRET}` 环境变量注入）
   - `database.path`
   - `llm.targets`（至少一条，含 `url` 和 `api_key`）
2. 检查环境变量是否已导出：
   ```bash
   echo $JWT_SECRET            # 应输出非空字符串
   echo $ANTHROPIC_API_KEY_1   # 应输出 sk-ant-... 开头的字符串
   ```
3. 如果值已在配置文件中但仍报空，检查是否有多余空格或引号：
   ```yaml
   auth:
     jwt_secret: "${JWT_SECRET}"  # 正确
     jwt_secret: " ${JWT_SECRET}" # 错误：前导空格
   ```

### 1.2 Preflight 检查失败：端口占用

**症状**：

```
preflight: preflight checks failed:
  - listen address "0.0.0.0:9000" is already in use
```

**排查步骤**：

```bash
# Linux/macOS
lsof -i :9000
ss -tlnp | grep 9000

# Windows
netstat -ano | findstr :9000
```

如果端口被旧的 sproxy 进程占用：

```bash
# 找到 PID（见 netstat 输出）
kill <PID>           # Linux/macOS
taskkill /PID <PID> /F  # Windows
```

或在配置文件中修改 `listen.port`。

### 1.3 Preflight 检查失败：数据库路径不可写

**症状**：

```
preflight: preflight checks failed:
  - database path "/var/lib/pairproxy/pairproxy.db" is not writable
```

**排查步骤**：

```bash
# 创建目录并赋予权限
mkdir -p /var/lib/pairproxy
chown $(whoami) /var/lib/pairproxy
chmod 750 /var/lib/pairproxy

# 或使用相对路径
database:
  path: "./pairproxy.db"
```

### 1.4 数据库迁移失败

**症状**：

```
migrate database: ...
```

**排查步骤**：

1. 确认数据库文件未被其他进程占用（SQLite WAL 模式仅允许一个写入者）：
   ```bash
   lsof pairproxy.db
   ```
2. 检查磁盘空间：
   ```bash
   df -h /var/lib/pairproxy/
   ```
3. 若数据库文件损坏，可通过备份恢复或删除文件让 sproxy 重新创建：
   ```bash
   mv pairproxy.db pairproxy.db.bak
   sproxy start  # 自动创建新 DB
   ```

---

## 2. JWT 认证错误

### 2.1 客户端：401 Unauthorized（无 token）

**症状**：cproxy 所有请求返回 401，sproxy 日志出现：

```
JWT verification failed  {"path": "/v1/messages", "error": "missing X-PairProxy-Auth header"}
```

**排查步骤**：

```bash
cproxy status   # 查看 token 状态
```

如果 token 未登录或已过期：

```bash
cproxy login --server http://sproxy.company.com:9000
```

### 2.2 客户端：401 Unauthorized（token 过期）

**症状**：sproxy 日志：

```
JWT verification failed  {"error": "token has expired"}
```

**排查步骤**：

1. 重新登录或等待 cproxy 自动刷新（若 `auto_refresh: true`）：
   ```bash
   cproxy login --server http://sproxy.company.com:9000
   ```
2. 缩短 access_token_ttl + 确保 refresh_token_ttl 足够长：
   ```yaml
   auth:
     access_token_ttl: 24h
     refresh_token_ttl: 168h  # 7 天
   ```

### 2.3 JWT 算法混淆攻击被拒绝

**症状**：sproxy 日志出现 WARN 级别：

```
JWT algorithm mismatch  {"got": "HS384", "want": "HS256"}
```

**原因**：有人使用非 HS256 算法签发 token 尝试绕过认证。这是正常的安全拒绝，无需操作。若大量出现，检查是否有恶意客户端攻击。

### 2.4 Token 被吊销

**症状**：客户端收到 401，sproxy 日志：

```
JWT verification failed  {"error": "token has been revoked"}
```

**操作**：重新登录。

---

## 3. 配额超限 / 速率限制

### 3.1 用户收到 429 Too Many Requests

**症状**：请求返回 HTTP 429，body：

```json
{"error": "quota_exceeded", "detail": "daily token quota exceeded", "reset_at": "2025-01-02T00:00:00Z"}
```

**排查步骤**：

1. 查询用户今日用量：
   ```bash
   sproxy admin stats --user alice --days 1
   ```
2. 查看分组配额配置：
   ```bash
   sproxy admin group list
   ```
3. 临时提高配额（当日紧急情况）：
   ```bash
   sproxy admin group set-quota engineering --daily 2000000 --monthly 50000000
   ```

### 3.2 速率限制（RPM 超限）

**症状**：响应头包含：

```
X-RateLimit-Limit: 60
X-RateLimit-Used: 61
X-RateLimit-Reset: 2025-01-01T12:01:00Z
```

**排查步骤**：

1. 查看分组 RPM 配置：
   ```bash
   sproxy admin group list
   ```
2. 调整 RPM 限制（0 = 无限制）：
   ```bash
   sproxy admin group set-quota engineering --daily 0 --monthly 0 --rpm 0
   ```

---

## 4. 无法连接到 s-proxy

### 4.1 cproxy 无法连接 s-proxy（502 Bad Gateway）

**症状**：所有请求返回 502，cproxy 日志：

```
upstream request failed  {"target": "http://sp-1:9000", "error": "connection refused"}
```

**排查步骤**：

1. 确认 sproxy 正在运行：
   ```bash
   curl http://sp-1:9000/health
   # 期望：{"status":"ok","version":"...","uptime_seconds":...}
   ```
2. 检查防火墙规则允许 9000 端口访问：
   ```bash
   # Linux
   iptables -L INPUT -n | grep 9000
   # 或 ufw
   ufw status
   ```
3. 检查 cproxy 配置中的地址是否正确：
   ```yaml
   sproxy:
     primary: "http://sp-1.company.com:9000"  # 确认 hostname 可解析
   ```
4. 使用 `cproxy status` 查看当前负载均衡器健康状态

### 4.2 健康检查失败时 cproxy 的行为

cproxy 内置主动健康检查（每 30s 一次），检测到节点不可达时：

- 将节点标记为 `Healthy: false`
- 将流量路由到其他健康节点
- 节点恢复后自动重新标记为健康

如果**所有节点**都不可达，cproxy 会返回 503。

---

## 5. 集群节点问题

### 5.1 Worker 注册失败

**症状**：worker 日志：

```
heartbeat failed  {"primary": "http://sp-1:9000", "error": "401 Unauthorized"}
```

**原因**：`cluster.shared_secret` 配置不匹配。

**排查步骤**：

1. 确认 primary 和 worker 的 `cluster.shared_secret` 完全一致：
   ```bash
   # primary
   grep shared_secret /etc/pairproxy/sproxy.yaml
   # worker
   grep shared_secret /etc/pairproxy/sproxy-worker.yaml
   ```
2. 确认环境变量已正确设置：
   ```bash
   echo $CLUSTER_SECRET   # 两台机器应输出相同值
   ```

### 5.2 Worker 不出现在路由表

**症状**：c-proxy 的路由更新中看不到 worker 节点。

**排查步骤**：

1. 查看 primary 当前路由表：
   ```bash
   curl -H "Authorization: Bearer $CLUSTER_SECRET" http://sp-1:9000/cluster/routing
   ```
2. 检查 worker 心跳日志：
   ```bash
   journalctl -u sproxy-worker -n 50 | grep heartbeat
   ```
3. 检查 TTL 配置（默认 90s = 3 次心跳）：
   ```yaml
   cluster:
     report_interval: 30s     # worker: 心跳间隔
     peer_monitor_interval: 30s  # primary: 检查间隔
   ```

---

## 6. Dashboard / Admin API 问题

### 6.1 Dashboard 登录失败

**症状**：输入密码后返回登录页，或 `POST /api/admin/login` 返回 401。

**排查步骤**：

1. 验证密码 hash 是否正确生成：
   ```bash
   sproxy admin hash-password --password "your-password"
   # 将输出的 hash 设置到 admin.password_hash
   ```
2. 检查 `sproxy.yaml` 中 `admin.password_hash` 字段：
   ```yaml
   admin:
     password_hash: "${ADMIN_PASSWORD_HASH}"
   ```
3. 确认环境变量已设置：
   ```bash
   echo $ADMIN_PASSWORD_HASH  # 应输出 $2a$12$... 开头的 bcrypt hash
   ```

### 6.2 Admin API 返回 403

**症状**：`GET /api/admin/users` 等接口返回 403。

**原因**：Bearer token 没有 admin 权限，或使用了用户 JWT（非 admin JWT）。

**解决**：使用 `/api/admin/login` 获取 admin JWT，然后在请求头中携带：

```bash
TOKEN=$(curl -s -X POST http://sproxy:9000/api/admin/login \
  -H "Content-Type: application/json" \
  -d '{"password":"your-admin-password"}' | jq -r .access_token)

curl -H "Authorization: Bearer $TOKEN" http://sproxy:9000/api/admin/users
```

---

## 7. 性能问题

### 7.1 Usage 写入积压（queue_depth 持续升高）

**症状**：`GET /health` 返回的 `usage_queue_depth` 持续增大，接近 `write_buffer_size * 2`。

**排查步骤**：

1. 检查 `/health` 端点：
   ```bash
   curl http://sproxy:9000/health | jq .usage_queue_depth
   ```
2. 检查 SQLite 写入性能（是否有慢查询）：
   ```bash
   # 打开 sproxy debug 日志（SIGHUP 热重载）
   kill -HUP $(pidof sproxy)
   # 查看 "usage batch written" 日志的频率
   journalctl -u sproxy -n 100 | grep "usage batch"
   ```
3. 调整缓冲参数（增大 buffer_size 或减小 flush_interval）：
   ```yaml
   database:
     write_buffer_size: 500  # 增大批量写入阈值
     flush_interval: 10s     # 适当延长 flush 间隔
   ```

### 7.2 请求延迟过高

**症状**：LLM 响应正常，但 sproxy 总体延迟明显高于预期。

**排查步骤**：

1. 检查活跃请求数（是否过载）：
   ```bash
   curl http://sproxy:9000/health | jq .active_requests
   ```
2. 检查配额检查缓存命中率（如果缓存频繁 miss，会触发 DB 查询）：
   - 开启 debug 日志，搜索 `"quota cache"` 相关条目
3. 检查 LLM 目标健康状态（是否有目标连接缓慢）：
   ```bash
   curl http://sproxy:9000/metrics | grep pairproxy_requests
   ```

### 7.3 动态调整日志级别（不重启）

在 Linux/macOS 上，可通过 SIGHUP 信号热重载 `log.level`：

```bash
# 修改 sproxy.yaml 中的 log.level 为 "debug"
sed -i 's/level: "info"/level: "debug"/' sproxy.yaml

# 发送 SIGHUP 触发重载
kill -HUP $(pidof sproxy)

# 恢复 info 级别
sed -i 's/level: "debug"/level: "info"/' sproxy.yaml
kill -HUP $(pidof sproxy)
```

**注意**：Windows 不支持 SIGHUP，需重启服务：
```powershell
Restart-Service sproxy
```

---

## 8. 调试命令速查

```bash
# 查看 sproxy 健康状态（包含运行时指标）
curl http://sproxy:9000/health | jq .

# 查看 Prometheus 指标
curl http://sproxy:9000/metrics

# 查看集群路由表（需 shared_secret）
curl -H "Authorization: Bearer $CLUSTER_SECRET" \
     http://sproxy:9000/cluster/routing | jq .

# 查询全局 token 用量（最近 7 天）
sproxy admin stats --days 7

# 查询特定用户用量
sproxy admin stats --user alice --days 30

# 列出所有用户
sproxy admin user list

# 列出所有分组及配额
sproxy admin group list

# 检查 cproxy 状态（token 有效性 + 上游健康状态）
cproxy status
```

---

## 9. 日志文件位置

PairProxy 使用结构化 JSON 日志输出到 **标准错误 (stderr)**。具体存储位置取决于部署方式：

### systemd 部署

```bash
# 实时跟踪
journalctl -u sproxy -f

# 查看最近 100 条
journalctl -u sproxy -n 100

# 按时间范围过滤
journalctl -u sproxy --since "2025-01-01 00:00" --until "2025-01-01 12:00"

# 过滤错误级别
journalctl -u sproxy | grep '"level":"error"'
```

### Docker 部署

```bash
docker logs sproxy -f
docker logs sproxy --tail 100 | jq 'select(.level == "error")'
```

### 直接运行（前台）

日志直接输出到终端。建议重定向：

```bash
sproxy start --config sproxy.yaml 2>> /var/log/pairproxy/sproxy.log &
```

### 使用 jq 过滤日志

```bash
# 只看错误
journalctl -u sproxy | jq 'select(.level == "error")'

# 按用户过滤
journalctl -u sproxy | jq 'select(.user_id == "alice-uuid")'

# 查看最近的 JWT 认证失败
journalctl -u sproxy | jq 'select(.msg | contains("JWT verification failed"))'

# 查看 usage 写入情况
journalctl -u sproxy | jq 'select(.logger == "usage_writer")'
```

---

## 10. LLM Extended Thinking / 长流连接断开

### 10.1 症状

使用 Claude extended thinking（深度思考）模式时，LLM 在返回任何内容前可能**静默持续 30 分钟以上**。此期间连接空闲但并未断开，如果中间层有超时限制则会提前切断。

常见表现：
- Claude Code 报错 `stream closed unexpectedly` 或 `EOF`
- sproxy 日志出现 `write: broken pipe`
- 请求在约 N 分钟后固定超时（说明某层有固定超时值）

### 10.2 排查层级

按从外到内的顺序逐层检查：

| 层级 | 排查方法 | 建议值 |
|------|---------|--------|
| **nginx / Caddy 反向代理** | 检查 `proxy_read_timeout` / `proxy_send_timeout` | `0`（不限制）|
| **sproxy HTTP Server** | 查看 `cmd/sproxy/main.go` 中 `WriteTimeout` | `0`（已禁用）|
| **cproxy → sproxy transport** | `ResponseHeaderTimeout` 仅限首包，不影响流 | 30s（正常）|
| **客户端（Claude Code）** | 客户端自身超时设置 | 依客户端配置 |

### 10.3 sproxy 自检

确认当前版本 `WriteTimeout` 已禁用：

```bash
# 在源码中确认（应看到 WriteTimeout: 0）
grep -A5 'http.Server{' cmd/sproxy/main.go
```

预期输出：
```go
server := &http.Server{
    ...
    WriteTimeout: 0,
    ...
}
```

### 10.4 nginx 前置时的正确配置

```nginx
# SSE / Extended Thinking：必须禁用缓冲，超时设为 0
proxy_buffering off;
proxy_read_timeout 0;
proxy_send_timeout 0;
```

> **注意**：`proxy_read_timeout 0` 表示不限制，依赖 sproxy 的客户端断开检测回收挂起连接。若担心空连接积压，可改为 `7200`（2 小时）作为保底。

### 10.5 仍然断开的排查步骤

1. 确认 nginx（或其他反代）超时已设为 0 或足够大
2. 在 sproxy 日志中搜索断开原因：
   ```bash
   journalctl -u sproxy | jq 'select(.msg | contains("broken pipe") or contains("write error"))'
   ```
3. 若日志显示 `context canceled`，说明是客户端（Claude Code）主动断开，非 sproxy 问题
4. 若无日志异常但仍断开，检查 TCP keepalive 配置（操作系统级别）：
   ```bash
   sysctl net.ipv4.tcp_keepalive_time    # 建议 ≤ 300
   sysctl net.ipv4.tcp_keepalive_intvl   # 建议 60
   ```
