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
11. [对话内容追踪问题](#11-对话内容追踪问题)
12. [Direct Proxy (sk-pp- API Key) 问题](#12-direct-proxy-sk-pp--api-key-问题)
13. [语义路由问题](#13-语义路由问题)
14. [训练语料采集问题](#14-训练语料采集问题)
15. [PostgreSQL 连接问题](#15-postgresql-连接问题)
16. [健康检查运行时同步问题](#16-健康检查运行时同步问题v2190)

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

---

## 11. 对话内容追踪问题

> v2.4.0+，涉及 `sproxy admin track` 功能。

### 11.1 启用追踪后未产生任何记录文件

**可能原因 1**: 追踪在请求之后才启用。

只有 `track enable` **之后**发送的请求才被记录，存量请求不会补录。

**可能原因 2**: 磁盘权限问题。

sproxy 进程无法写入 `track/` 目录时会静默跳过（不影响主流程），查看日志：

```bash
journalctl -u sproxy | grep -i "track\|conversation"
```

若出现权限错误，修复目录权限（路径以 `track_dir=` 日志字段为准）：

```bash
chown -R sproxy:sproxy <track_dir>/
chmod -R 755 <track_dir>/
```

> **Peer/PostgreSQL 模式**：若报 `mkdir track: read-only file system`，参见 [11.7 节](#117-peerpostgresql-模式启动时报-mkdir-track-read-only-file-system)。

**可能原因 3**: 用户名大小写不匹配。

JWT 中的 username 与 `track enable` 参数必须完全一致（大小写敏感）。

```bash
# 确认当前被追踪的用户名
sproxy admin track list
```

---

### 11.2 对话记录的 response 字段为空

**非流式请求**：response 从响应 JSON body 提取，若 LLM 返回非标准格式（如 Anthropic `content` 数组为空，或 OpenAI `choices` 为空），则 response 为空字符串。

**流式请求**：response 从 SSE chunks 累积。若请求在 `message_stop`（Anthropic）或 `[DONE]`（OpenAI）之前被客户端中断，可能无法完整捕获。

调试方法：
```bash
# 查看原始对话记录
sproxy admin track show <username>
cat <track_dir>/conversations/<username>/<filename>.json | jq .
```

---

### 11.3 token 计数为 0

**非流式**：token 从响应 `usage` 字段提取。若 LLM 响应不包含 `usage` 字段，则计数为 0（记录仍然写入）。

**流式**：token 计数当前不从 SSE 流中提取（流式 token 统计由 `tap` 模块负责，单独记录到数据库）。

---

### 11.4 磁盘空间占用过大

每条对话记录通常为 1 KB ~ 10 KB，高频用户长期追踪会积累大量文件。

定期清理：
```bash
# 清除 alice 的所有历史记录（保留追踪状态）
sproxy admin track clear alice

# 或手动按日期删除（保留最近 7 天）
find <track_dir>/conversations/alice/ -name "*.json" \
  -mtime +7 -delete
```

若需批量清理所有用户：
```bash
for user in $(sproxy admin track list | grep -v "^No\|^Tracked"); do
  sproxy admin track clear "$user"
done
```

---

### 11.5 `track enable` 报错 "invalid username"

用户名含有路径遍历字符时被拒绝：`..`、`/`、`\`，或为空字符串。

合法用户名示例：`alice`、`bob123`、`user.name`、`user-name`。

---

### 11.6 旧版本（v2.3.0 及以下）报 "unknown command: track"

`sproxy admin track` 命令在 **v2.4.0** 引入。请升级二进制：

```bash
./sproxy --version   # 确认版本
```

---

### 11.7 Peer/PostgreSQL 模式启动时报 `mkdir track: read-only file system`

**根因**：`track.dir` 默认为 `"./track"`（相对于进程 CWD），在所有模式下均如此。若 CWD 所在文件系统只读（容器常见场景），`os.MkdirAll` 尝试创建 `track/users` 时报此错误。tracker 初始化失败后对话跟踪功能被禁用，但服务正常启动。

**解决方法**：在 `sproxy.yaml` 中显式配置有写权限的绝对路径：

```yaml
track:
  dir: "/data/pairproxy/track"
```

然后确保目录存在且 sproxy 进程有写权限：

```bash
mkdir -p /data/pairproxy/track
chown -R sproxy:sproxy /data/pairproxy/track
```

重启 sproxy 后日志应出现：

```
conversation tracker initialized  track_dir=/data/pairproxy/track
```

---

## 12. Direct Proxy (sk-pp- API Key) 问题

> v2.9.0+，涉及 `sk-pp-` 前缀 API Key 直连模式（无需 cproxy）。

### 12.1 API Key 认证失败（401）

**症状**：使用 `sk-pp-` Key 请求时返回 401，日志出现：
```
keygen verification failed  {"error": "invalid key format"}
```

**排查步骤**：

1. 确认 Key 格式正确（`sk-pp-` 前缀 + 48 字符 Base62）：
   ```bash
   echo -n "sk-pp-YOUR_KEY" | wc -c   # 应输出 54
   ```

2. 确认 `auth.keygen_secret` 配置一致（v2.15.0+）：
   ```bash
   grep keygen_secret sproxy.yaml
   echo $KEYGEN_SECRET   # 确认环境变量已导出
   ```

3. Key 是否由当前 sproxy 实例生成（keygen_secret 必须一致）：
   ```bash
   sproxy admin keygen --user alice   # 重新生成 Key
   ```

### 12.2 旧版 Key 失效

**v2.15.0 升级后**，早期版本生成的 sk-pp- Key 将失效（算法从指纹嵌入改为 HMAC-SHA256）。

**v2.24.7 升级后**，Key 派生算法改为基于用户 PasswordHash，所有现有 Key 失效，用户需重新获取。

用户需重新获取 Key：
```bash
# 管理员为用户生成新 Key
sproxy admin keygen --user alice

# 或用户通过 Keygen WebUI 自助获取
# 访问 https://sproxy.company.com/keygen/
```

### 12.3 改密后旧 Key 仍可访问（v2.24.8 前）

**症状**：用户修改密码后，持有旧 Key 的客户端仍然可以正常请求。

**原因**：v2.24.8 以前，`ValidateWithLegacySecret` 兜底路径不检查密码是否已更改。

**v2.24.8+ 行为**：调用 `UpdatePassword` 时自动设置 `legacy_key_revoked=true`，该用户的 legacy Key 即时失效。用户需通过 Keygen WebUI 重新获取基于新密码哈希派生的 Key。

### 12.4 配额限制对直连路径不生效（v2.24.8 前）

**症状**：为用户设置了 daily/monthly token 上限或 RPM 限制，但 sk-pp- 直连请求不受影响，日志中无配额拦截记录。

**原因**：v2.24.8 以前，`DirectProxyHandler` 的中间件链未包含 `QuotaMiddleware`。

**v2.24.8+ 行为**：配额中间件已挂载至直连路径，行为与 JWT 路径一致。升级后配额立即生效，请提前告知用户。

### 12.5 Model Mapping 在直连模式下不生效（v2.24.8 前）

**症状**：配置了 `model_mapping`（如 `{"*":"MiniMax-2.5"}`），但通过直连 sk-pp- 发送的请求仍使用原始模型名，上游返回"模型不存在"。

**原因**：v2.24.8 以前，model rewrite 仅在协议转换（Anthropic ↔ OpenAI）时执行，同协议透传路径未做重写。

**v2.24.8+ 行为**：透传路径（`conversionNone`）现在也会查找并应用 `model_mapping`，发往上游前完成模型名替换。

---

## 13. 语义路由问题

> v2.18.0+，涉及 `semantic_router` 功能。

### 13.1 语义路由未生效（请求仍走全量负载均衡）

**可能原因 1**：用户已有 LLM 绑定，语义路由仅对无绑定用户生效。

```bash
# 检查用户是否有绑定
sproxy admin llm list | grep alice
```

**可能原因 2**：分类失败触发降级。

```bash
# 检查分类器状态
sproxy admin semantic-router status

# 查看降级日志
journalctl -u sproxy | jq 'select(.msg | contains("semantic router"))'
```

**可能原因 3**：规则未启用。

```bash
sproxy admin semantic-router list   # 检查 enabled 状态
sproxy admin semantic-router enable <rule-id>
```

### 13.2 分类器超时

**症状**：日志出现：
```
semantic router classification timeout  {"timeout_ms": 5000}
```

**排查步骤**：

1. 检查分类器 URL 配置：
   ```yaml
   semantic_router:
     classifier_url: "http://localhost:9000"   # 默认本机
   ```

2. 确认分类器 LLM Target 健康：
   ```bash
   sproxy admin llm targets   # 查看 classifier pool targets
   ```

3. 调整超时配置（若分类器较慢）：
   ```yaml
   semantic_router:
     classifier_timeout: 10s   # 默认 5s
   ```

### 13.3 路由结果不符合预期

**症状**：请求应命中规则 A，却命中了规则 B 或未命中任何规则。

**排查步骤**：

1. 开启 debug 日志，查看分类结果：
   ```bash
   kill -HUP $(pidof sproxy)   # 先在 sproxy.yaml 中将 log.level 改为 debug
   journalctl -u sproxy | jq 'select(.msg | contains("classified"))'
   ```

2. 检查规则 priority（数字越小优先级越高）：
   ```bash
   sproxy admin semantic-router list   # 查看 priority 字段
   ```

3. 测试单条分类：
   ```bash
   curl -X POST http://sproxy:9000/api/admin/semantic-router/classify \
     -H "Authorization: Bearer $ADMIN_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"messages":[{"role":"user","content":"帮我写一段 Python 代码"}]}'
   ```

---

## 14. 训练语料采集问题

> v2.16.0+，涉及 `corpus` 功能。

### 14.1 语料文件未生成

**检查步骤**：

```bash
# 确认采集已启用
sproxy admin corpus status

# 查看输出目录
ls -la /var/lib/pairproxy/corpus/

# 查看日志中的 corpus 相关条目
journalctl -u sproxy | grep -i corpus
```

### 14.2 语料文件体积异常大

```bash
# 查看当前文件大小
sproxy admin corpus list

# 检查轮转配置
grep corpus sproxy.yaml
```

---

## 15. PostgreSQL 连接问题

> v2.13.0+，适用于使用 PostgreSQL 数据库的部署。

### 15.1 PostgreSQL 连接失败

**症状**：sproxy 启动时报错：
```
open database: failed to connect to postgres: ...
```

**排查步骤**：

```bash
# 测试 PG 连接
psql "$PG_DSN" -c "SELECT 1;"

# 确认环境变量
echo $PG_DSN   # 应输出完整 DSN

# 查看 sproxy.yaml 配置
grep -A5 database sproxy.yaml
```

### 15.2 Peer Mode 节点未互相发现

> v2.14.0+ Peer Mode（PG 模式下所有节点对等）

**症状**：多节点部署中，节点无法看到彼此。

**排查步骤**：

```bash
# 检查所有节点是否共用同一 PG 实例
grep dsn sproxy.yaml   # 各节点应指向同一 PG

# 查看 peer 注册表
curl -H "Authorization: Bearer $CLUSTER_SECRET" \
     http://sproxy:9000/cluster/routing | jq .
```

---

## 16. 健康检查运行时同步问题（v2.19.0）

> v2.19.0+，涉及通过 WebUI/API 管理 LLM target 后的运行时同步。

### 16.1 新增 target 后仍显示 Healthy=false

**症状**：通过 WebUI 添加了新 target，Dashboard 上一直显示 Healthy=false，不自动恢复。

**排查步骤**：

```bash
# 1. 查看智能探活的发现日志（v2.24.5+，无需手动配置路径）
journalctl -u sproxy | grep "smart probe:\|probe:" | tail -20

# 2. 确认 target 的 health_check_path 配置（显式路径优先于智能探活）
curl -H "Authorization: Bearer $ADMIN_TOKEN" \
     http://localhost:9000/api/admin/llm-targets | jq '.[] | {url, health_check_path, provider}'

# 3. 若有显式 health_check_path，手动测试该路径
curl -v http://<target-url><health_check_path>

# 4. 若无显式路径，手动测试智能探活的候选路径
curl -v -H "Authorization: Bearer <your-api-key>" http://<target-url>/v1/models
curl -v http://<target-url>/health
```

**可能原因与解决方案**：

| 原因 | 诊断 | 解决方法 |
|------|------|---------|
| API Key 无效（401 持续） | 日志中 `smart probe: initial health check failed, status: 401` | 更新 API Key |
| 服务不可达（连接拒绝） | 日志中 `probe: target unreachable during discovery` | 检查网络连通性 |
| 所有内置策略均不匹配 | 日志中 `smart probe: no suitable health check method found` | 配置显式 `health_check_path` |
| 显式路径返回非 200 | 日志中 `health check failed` | 修正路径或等待后端恢复 |

**智能探活日志含义（v2.24.5+）**：

```bash
# 正常：发现路径（401 表示端点存在，key 无效）
INFO  probe: discovered working health check method  {method: GET /v1/models, status: 401}
DEBUG smart probe: initial health check failed after discovery  {status: 401}
# → target 当前标记为 Healthy=false，更换有效 key 后下次心跳将恢复

# 正常：key 有效
INFO  probe: discovered working health check method  {method: GET /v1/models, status: 200}
DEBUG smart probe: initial health check ok after discovery
# → target 标记为 Healthy=true

# 异常：完全不可达
WARN  probe: target unreachable during discovery  {error: connection refused}
# → 检查防火墙、网络配置

# 异常：无合适路径（阿里 DashScope /coding/v1 等特殊路径）
WARN  smart probe: no suitable health check method found, recording failure
# → 需要配置 health_check_path，或接受纯被动熔断模式
```

### 16.2 Sync 后已熔断 target 被意外复活

**症状**：某 target 因连续失败被熔断（Healthy=false），编辑另一个 target 保存后，熔断 target 突然变 Healthy=true。

**原因**：这是 v2.19.0 之前的 bug，已修复。现在 `SyncLLMTargets` 会保留存量 target 的健康状态。

**验证**：
```bash
grep "SyncLLMTargets\|balancer and health checker updated" sproxy.log | tail -10
# 已熔断的 target 应保持 Healthy=false
```

### 16.3 新 target 消耗了真实用户请求才被熔断

**症状**：添加了一个坏 target（无法连接），前几个用户请求失败后才被熔断。

**原因（v2.24.5 前）**：target 未配置 `health_check_path`，以 Healthy=true 乐观入场，依赖被动熔断。

**v2.24.5 之后**：新 target 加入后，智能探活会在下一个心跳周期（默认 30s）自动发现其状态。若服务不可达，会在用户请求消耗前被标记为 Healthy=false。

**保证**（v2.24.6+）：
- 连接拒绝 / DNS 失败 → Discover 立即返回 `unreachable=true`，**同一心跳周期内**被标记为不健康
- HTTP 超时 → Discover 继续尝试下一路径，不立即标记为不可达；若所有路径均超时，本次心跳记录一次失败，但不标记 `unreachable`

### 16.4 智能探活缓存相关问题

**症状**：更换了 API Key，但健康检查行为没有及时更新。

**原因**：探活策略缓存有 TTL（默认 2h），更换 key 后缓存应自动失效。

**排查**：
```bash
# 查看凭证更新日志
grep "credentials updated" sproxy.log | tail -5

# 凭证更新后应触发缓存失效
INFO  health_checker  credentials updated  {count: 3}
# 下次心跳将重新 discover
```

若日志中没有 `credentials updated`，可能是 `UpdateCredentials()` 未被调用，检查配置热重载是否生效。

**症状**：删除了目标的 `health_check_path` 配置，但探活路径没有变化。

**原因（v2.24.6 前）**：`UpdateHealthPaths` 仅更新显式路径映射表，不清除 probe 缓存，缓存中的旧策略会持续沿用直到 TTL 过期（最多 2h）。

**v2.24.6 修复**：`UpdateHealthPaths` 现在对失去显式路径的目标立即调用 `probeCache.invalidate()`，确保配置变更在下一个心跳周期生效。

**症状**：智能探活 2 小时后开始重新探活，产生大量 discover 日志。

**原因**：缓存 TTL 到期（正常行为），系统自动重新发现探活策略。这不影响服务可用性。若希望减少此现象，当前版本无 TTL 配置项（固定 2h）。

---

## 17. CI 测试 Data Race 排查

### 17.1 zaptest logger 在测试结束后被写入

**症状**：`-race` 模式下报 `WARNING: DATA RACE`，read 栈在 `zaptest.TestingWriter.Write`，write 栈在 `testing.tRunner.func1`（测试已结束）。

**根因**：测试中通过 `hc.Start(ctx)` / `writer.Start(ctx)` 启动的后台 goroutine，在测试函数返回后仍在运行，继续向已失效的 zaptest logger 写日志。

**修复模式**：
```go
// 错误：defer cancel() 不等待 goroutine 退出
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
hc.Start(ctx)

// 正确：cancel 后 Wait，确保所有子 goroutine 退出
ctx, cancel := context.WithCancel(context.Background())
hc.Start(ctx)
defer func() { cancel(); hc.Wait() }()
```

**规律**：凡是测试中调用 `XXX.Start(ctx)` 的地方，必须配套 `defer func() { cancel(); XXX.Wait() }()`。

---

### 17.2 HTTP handler goroutine 与测试主 goroutine 共享变量

**症状**：`-race` 模式下报 data race，一端在 `httptest.Server` 的 handler goroutine 写变量，另一端在测试主 goroutine 读变量。

**根因**：handler 是并发执行的，测试主 goroutine 在 `srv.Close()` 之前就读取了 handler 写入的变量。

**修复模式**：
```go
// 错误：无保护直接读
var receivedAuth string
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    receivedAuth = r.Header.Get("Authorization") // handler goroutine 写
}))
// ... 发请求 ...
assert(receivedAuth) // 主 goroutine 读，race！

// 正确：mutex 保护 + srv.Close() 后再读
var mu sync.Mutex
var receivedAuth string
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    mu.Lock()
    receivedAuth = r.Header.Get("Authorization")
    mu.Unlock()
}))
// ... 发请求 ...
srv.Close() // 等所有 handler 完成
mu.Lock()
val := receivedAuth
mu.Unlock()
assert(val)
```

---

### 17.3 SQLite ON CONFLICT 不能更新主键

**症状**：`ON CONFLICT(url) DO UPDATE SET id=excluded.id` 触发 `UNIQUE constraint failed: table.id`。

**根因**：SQLite 不支持在 upsert 中修改主键列，即使 `ON CONFLICT` 指定的是其他列。

**修复**：先按冲突列（url）删除 id 不同的旧记录，再按主键（id）做普通 upsert：
```go
// 先删除 URL 相同但 ID 不同的旧记录
db.Where("url = ? AND id != ?", target.URL, target.ID).Delete(&LLMTarget{})
// 再按 ID upsert
db.Save(&target)
```

---

### 17.4 异步状态断言时序问题

**症状**：测试偶发失败，断言 `Healthy=false` 但实际仍为 `true`；或断言通过但 CI 偶尔失败。

**根因**：`Start()` 后立即断言异步副作用，goroutine 可能尚未执行到对应逻辑。

**修复**：移除对中间状态的即时断言，只验证最终稳定状态；或用轮询等待：
```go
// 等待最终状态，最多 2s
require.Eventually(t, func() bool {
    return balancer.IsHealthy(targetURL)
}, 2*time.Second, 50*time.Millisecond)
```

**解决方案**：为所有 target 配置 `health_check_path`（如 `/health`），确保后端实现该端点。有 `health_check_path` 的新 target 会先经过主动检查再进入路由池。
---

## 17. Group-Target Set 问题

### 17.1 IsActive=false 的成员仍出现在路由结果中

症状：AddMember 传入 IsActive=false，但 GetAvailableTargetsForGroup 仍返回该成员。
根因：GORM Create 将 bool 零值 false 视为未设置，应用 default:true 标签写为 true（Bug 7）。
修复：v2.20.0 已修复，AddMember 改用原生 SQL INSERT。
验证：SELECT id, target_url, is_active FROM group_target_set_members;

### 17.2 健康监控不检查某个 target

症状：Target Health Monitor 不对某个 target 执行健康检查。
排查：确认该 target 的 group_target_set_members 记录中 is_active=true。健康监控只检查 IsActive=true 的成员。

### 17.3 告警 Stop() 阻塞

症状：alert.enabled=false 时调用 Stop() 永久阻塞。
修复：v2.20.0 已修复，Start() 通过 sync.Once 安全关闭 done 通道。