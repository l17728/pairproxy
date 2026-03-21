# Semantic Router 设计文档

**日期**: 2026-03-21
**项目**: PairProxy Gateway
**状态**: 待实现

---

## 1. 背景与目标

当前 pairproxy 的路由逻辑基于静态绑定（用户绑定 > 分组绑定 > 全局 LB），无法根据请求的语义内容动态选择最适合的 LLM target。本设计引入语义路由模块，在现有负载均衡候选池内，根据请求 messages 的语义意图做二次筛选，将请求路由到最合适的 target。

---

## 2. 核心设计决策

| 决策项 | 选择 | 理由 |
|--------|------|------|
| 分类器端点 | 走现有 LB（不单独配置） | 与整体负载均衡原则一致，避免递归 |
| 路由触发时机 | 语义阶段（读取 messages 内容） | 支持基于意图的动态路由 |
| 规则格式 | 自然语言 description + target 列表 | LLM 直接理解，泛化能力强 |
| 分类结果缓存 | 不缓存 | Agent 场景下重复请求概率极低 |
| 分类失败 fallback | 返回完整候选池（降级到 LB） | 保证可用性，优雅降级 |
| 与绑定逻辑关系 | 语义路由在候选池内二次筛选 | 两者叠加而非互斥 |
| 规则存储 | yaml 默认 + 数据库覆盖（热更新） | 与现有 admin CLI 体系一致 |
| 分类方案 | 单次 LLM 分类调用 | 实现简洁，无外部依赖 |

---

## 3. 架构位置

```
请求进入
  ↓
现有认证 / 用户-分组绑定逻辑
  ↓ （生成候选 target 池）
[新增] SemanticRouter.Route(ctx, messages, candidatePool)
  ├─ 读取路由规则（DB 优先，yaml 兜底）
  ├─ 构建分类 prompt（messages + 规则 descriptions）
  ├─ 调用分类器 LLM（走现有 LB，从候选池中选）
  ├─ 解析响应 → 匹配路由名 → 取交集筛选候选池
  └─ 任何失败 → 原样返回完整候选池（降级到 LB）
  ↓
现有 LB 在最终候选池内选 target 转发
```

---

## 4. 配置格式

### 4.1 sproxy.yaml 默认规则

```yaml
semantic_router:
  enabled: true
  routes:
    - name: code_tasks
      description: "Requests involving code generation, debugging, refactoring, or technical programming"
      targets:
        - "https://api.anthropic.com"
        - "https://deepseek-api.example.com"
    - name: general_chat
      description: "General conversation, simple Q&A, or creative writing"
      targets:
        - "https://haiku-endpoint.example.com"
```

### 4.2 Admin CLI 命令（数据库规则，热更新，优先级高于 yaml）

```bash
# 新增路由规则
./sproxy admin route add code_tasks \
  --description "Code generation and debugging tasks" \
  --targets "https://api.anthropic.com,https://deepseek.example.com"

# 列出所有规则
./sproxy admin route list

# 更新规则
./sproxy admin route update code_tasks \
  --description "Updated description" \
  --targets "https://api.anthropic.com"

# 删除规则
./sproxy admin route delete code_tasks

# 启用/禁用规则
./sproxy admin route enable code_tasks
./sproxy admin route disable code_tasks
```

---

## 5. 分类 Prompt 结构

```
You are a request router. Classify the following conversation into exactly one of these categories.
Reply with ONLY the category name, nothing else.

Categories:
- code_tasks: Code generation, debugging, refactoring, or technical programming
- general_chat: General conversation, simple Q&A, or creative writing

Conversation:
[messages JSON]

Category:
```

分类器响应仅包含路由名称（如 `code_tasks`），解析时做 trim + 小写比较。无法匹配时触发 fallback。

---

## 6. 数据库 Schema

```sql
CREATE TABLE semantic_routes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    targets     TEXT NOT NULL,  -- JSON array of target URLs
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

规则加载优先级：**数据库规则 > yaml 规则**。数据库中 enabled=1 的规则覆盖同名 yaml 规则。

---

## 7. 核心接口

```go
// internal/router/semantic.go

type RouteRule struct {
    Name        string
    Description string
    Targets     []string
}

type SemanticRouter struct {
    db         *sql.DB
    yamlRoutes []RouteRule  // 来自 sproxy.yaml，低优先级
    lbClient   LBClient     // 复用现有 LB 客户端发起分类调用
}

// Route 在候选池内按语义二次筛选。
// 任何错误或无匹配时原样返回 candidatePool（降级到 LB）。
func (r *SemanticRouter) Route(
    ctx context.Context,
    messages []Message,
    candidatePool []string,
) ([]string, error)
```

---

## 8. 关键行为矩阵

| 场景 | 行为 |
|------|------|
| 分类器返回有效路由名 | 取该路由 targets 与候选池的交集作为新候选池 |
| 交集为空（规则 target 不在候选池内） | fallback，返回完整候选池 |
| 分类器调用失败 / 超时 | fallback，返回完整候选池 |
| 分类器响应无法匹配任何路由名 | fallback，返回完整候选池 |
| 无任何路由规则（DB 和 yaml 均为空） | 跳过语义路由，直接返回候选池 |
| `semantic_router.enabled: false` | 跳过语义路由，直接返回候选池 |

---

## 9. 文件结构

```
internal/router/
  semantic.go          # SemanticRouter 核心逻辑
  semantic_test.go     # 单元测试（mock LB 客户端）

internal/config/
  config.go            # 新增 SemanticRouterConfig 字段

internal/db/
  migrations/          # 新增 semantic_routes 表迁移

cmd/sproxy/
  main.go              # 新增 admin route 子命令注册

internal/admin/
  route.go             # admin route CRUD 实现
```

---

## 10. 不在范围内（Out of Scope）

- 分类结果缓存（Agent 场景重复率极低，无必要）
- Embedding 相似度匹配（引入外部依赖，过度设计）
- 多阶段分类（规则集小，单次调用已足够）
- 用户级别的路由规则覆盖（当前版本统一用系统级规则）
