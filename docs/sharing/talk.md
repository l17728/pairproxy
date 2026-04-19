# AI Coding 的五个控制点：从"帮我写段代码"到"按规格自测自验交付"

> 基于 13.3 万行代码（Go + HTML + 配置）、24 个版本、390 次提交的实战方法论
> 不讲怎么写 Prompt，讲怎么系统性地跟 AI 协作交付软件

---

## 开场

两种 AI Coding 的状态：

**状态 A**：随手丢给 AI 一个需求，AI 给了一段代码，粘贴进去，莫名其妙 bug，再问 AI，AI 再改……补丁打补丁，不知道什么时候算完。

**状态 B**：44 天之后，133,000 行代码（121,000 行 Go + 8,100 行 HTML + 3,700 行配置/脚本），3 个独立工具（代理网关、报告生成器、负载测试器），24 个版本迭代，390 次提交（其中 75 次由 Claude Code 独立完成），2,090+ 个测试 0 失败，每个版本都有设计文档和验收报告存档。

这两种结果的差距，不在于 AI 的能力，在于**你给 AI 的工作环境**。

### 核心命题

> AI 是一个执行能力极强、但记忆极短、判断需要被约束的工程师。
> 你的工作，是设计它的工作环境，而不是跟它聊天。

---

## Part 1：上下文管理——给 AI 的文档和给人的文档，是两件事

### 方法论

这个项目里存在三种文档，写给三种不同的读者：

- `README.md` → 给用户：这个项目是什么，怎么用
- `docs/manual.md` → 给运维：怎么部署，怎么管理
- `CLAUDE.md` / `AGENTS.md` → **给 AI**：怎么在这个代码库里工作

大多数人把 AI 当搜索引擎用，上来就问问题，没有背景。更好的方式，是把 AI 当新入职的工程师对待：**第一件事是让它读项目文档，第二件事才是分配任务**。

### CLAUDE.md 写什么

**❌ 不要写（AI 不需要被提醒的废话）：**
```
- 请认真编写单元测试
- 错误处理要友好
- 不要提交 API Key 到 Git
```

**✅ 应该写（AI 不读代码不会知道的隐性知识）：**

**一、架构决策（尤其是非直觉的部分）**

```markdown
## Fail-Open vs Fail-Closed 原则
这个项目有明确的分层原则，不要随意修改：

- 配额/DB 错误：Fail-Open（放行 + WARN 日志）
  理由：可用性优先，不因内部故障影响用户
- 用户禁用校验：Fail-Closed（返回 HTTP 500）
  理由：安全边界，DB 不可达时宁可拒绝
- 语义路由超时：Fail-Open（降级到完整候选池）
  理由：路由失败不能阻断用户请求
```

**二、已知的坑（已踩过，不要再踩）**

```markdown
## 已知问题和禁区

### TeeResponseWriter 包装顺序不能改
目前的包装顺序是刻意的：
  AnthropicToOpenAIConverter（最内层）
  → TeeResponseWriter（外层，tee 给 token parser）
  → proxy.ServeHTTP
顺序改变会导致 token 计数为 0。

### injectOpenAIStreamOptions 是无条件执行的
对所有 /v1/chat/completions 请求注入 stream_options（包括 OtoA 转换路径）。
在 OtoA 转换里必须显式删除这个字段，Anthropic 不认识它。
```

**三、环境约束（精确到命令）**

```markdown
## 开发环境
- Go binary: C:/Program Files/Go/bin/go.exe
- 单包测试: go test ./internal/quota/... -v -count=1
- 含 race detector: go test -race ./...
- 覆盖率报告: go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

**四、代码风格的隐性规则**

```markdown
## Logging 层级（Zap）
- DEBUG: 每个请求的 token 数（生产环境关闭）
- INFO: 生命周期事件（启动/关闭/token 刷新）
- WARN: 可恢复的错误（DB 写失败、health check 失败）
- ERROR: 需要人工介入的（不应该出现又出现了的）

原则：WARN 不阻断请求，ERROR 只在真正无法继续时使用。
```

### 实操教训

这个项目的 CLAUDE.md **第一版**是一份中文运维手册，写满了 `sproxy admin user add` 这类管理命令。

AI 读完之后知道怎么管理用户，但完全不知道如何按项目规范写代码。重写之后把架构决策、fail-open 原则、已知禁区写进去——**第一次就能给出符合项目风格的代码，review 时改动量减少约 70%**。

随着项目从 6 万行增长到 12 万行，AGENTS.md 也经历了多次演进：从运维手册→代码风格指南→架构决策记录→并发编程规范。每次踩坑后的教训都被写回 AGENTS.md，形成"踩坑→文档化→不再重复踩"的正循环。

### 可操作建议

项目启动时，花 2 小时写 CLAUDE.md，把脑子里"不言而喻"的东西写出来。
这是 AI Coding 里杠杆最高的单次投资。

> 模板见：[templates/CLAUDE-md-template.md](./templates/CLAUDE-md-template.md)

---

## Part 2：任务定义——Spec 决定了 AI 能飞多高

### 方法论

大多数人给 AI 的任务是这样的：

> "帮我实现 OpenAI 到 Anthropic 的协议转换"

AI 会给你一个实现——可能跑起来，可能有各种边界问题，你不知道它遗漏了什么，也不知道它做了哪些你不知道的假设。

更好的方式：**给 AI 写代码之前，先让 AI 帮你写 Spec，你来审 Spec，确认之后再实现**。

Spec 把"做什么"和"怎么做"分开。AI 实现之前的对齐成本远低于实现之后的返工成本。

### Spec 文档的四个必备要素

#### 要素一：用具体反例描述问题，不用抽象描述

❌ 抽象（AI 容易误解）：
```
旧的 Key 生成算法存在碰撞漏洞
```

✅ 具体（AI 理解准确）：
```
碰撞场景：
- 用户 alice123 → 字符集 {a, l, i, c, e, 1, 2, 3}
- 用户 321ecila → 字符集 {3, 2, 1, e, c, i, l, a}（完全相同！）

两个用户的字符指纹相同，任何能通过 alice123 验证的 Key，
也能通过 321ecila 的验证。攻击者可以枚举出碰撞 Key。
```

**原因**：AI 处理具体例子的准确率远高于抽象描述。越具体，实现偏差越小。

---

#### 要素二：明确写出已拒绝的备选方案

这是 Spec 里最容易被省略的部分，也是最重要的。

```markdown
## 已拒绝的方案

### 方案 A：双格式并存（新旧 Key 同时支持）
**拒绝原因**：需要维护两套验证逻辑，上线后无法强制迁移，
安全漏洞会长期存在。

### 方案 B：复用 jwt_secret 作为 HMAC 签名密钥
**拒绝原因**：密钥用途必须隔离。jwt_secret 泄露影响认证，
keygen_secret 泄露影响 API Key，不应该是同一个密钥。

### 方案 C：带用户名前缀的混合格式（sk-pp-alice-xxxxx）
**拒绝原因**：暴露了用户名信息，增加了攻击面。
```

**为什么必须写这个**：如果不写，AI 在实现过程中可能"发现"一个看起来更优雅的方案，并悄悄采用。而那个方案很可能已经被你否定了——但 AI 不知道。

---

#### 要素三：Success Criteria 必须量化，必须有四个维度

```markdown
## 完成标准

### 功能性
✅ 同一用户名 + secret → 永远同一个 Key（确定性/幂等性）
✅ 旧算法 Key 升级后立即失效（不支持双格式并存）
✅ keygen_secret 缺失时拒绝启动（硬中止，非警告）
✅ keygen_secret 长度 < 32 字符时拒绝启动

### 性能
✅ Key 生成耗时 < 1ms
✅ Key 验证（无缓存，1000 用户遍历）< 10ms
✅ Key 验证（缓存命中）< 1μs

### 测试
✅ generator.go 和 validator.go 覆盖率 100%
✅ 10,000 个随机用户名测试，无碰撞
✅ 旧格式 Key 被正确拒绝的测试

### 文档和用户预期
✅ UPGRADE.md 明确标注 Breaking Change
✅ 用户文档说明："重新生成"会得到同一个 Key（HMAC 确定性）
✅ Key 轮换说明：修改 keygen_secret 会使所有 Key 失效
```

**注意最后一条**：用户行为预期的文档说明——这条是实际踩坑后补入的（见 Part 3 坑2）。

---

#### 要素四：Out of Scope 明确排除

```markdown
## 不在本次范围内

- 双 secret 支持（宽限期过渡）— 列入 Roadmap，本次不做
- 用户级别的独立 Key 过期时间
- Key 吊销 API（当前通过禁用用户实现）
- 前端 UI 重新设计
```

**为什么必须写**：AI 有"求完整"的倾向。看到"还有这个问题没解决"，它会自发扩展范围，在没有说明的情况下在 PR 里加入额外改动。

> 模板见：[templates/spec-template.md](./templates/spec-template.md)

---

## Part 3：执行控制——Plan 文档的精确度决定 AI 的可靠度

### 方法论

Spec 解决"做什么"，Plan 解决"怎么做"。

很多人跳过 Plan 直接让 AI 实现。对于小任务没问题。对于超过 500 行的改动，会出现：
- AI 改了不该改的文件
- 中途发现某个依赖没建好，只能推倒重来
- 实现完了说"好了"，但有几个子功能其实没做

Plan 文档的核心价值：**把复杂任务分解成 AI 能可靠执行的最小步骤**。

### Plan 文档的五个关键机制

#### 机制一：File Map（范围约束）—— 告诉 AI 只能动哪些文件

任务开始前，先列出"白名单"：

```markdown
## File Map

| 文件                                | 操作   | 职责                            |
|------------------------------------|--------|---------------------------------|
| internal/proxy/converter.go        | Modify | 添加 conversionDirection 枚举   |
| internal/proxy/converter_test.go   | Modify | 重命名 TestShouldConvert → TestDetect |
| cmd/sproxy/main.go                 | Modify | 替换 bool 变量为 enum           |
```

只列 3 个文件。AI 修改了第 4 个文件，就是超出范围——这是可审计的边界。

**效果**：AI 知道"之外的文件不用管"，不会在实现时去"顺手修一下"其他地方。

---

#### 机制二：Chunk 分层（由内到外的依赖序）—— 大任务安全分块

OtoA（OpenAI → Anthropic）协议转换，是这个项目最复杂的特性。整个任务被拆成 5 个 Chunk：

```
Chunk 1: 类型层     — 定义 conversionDirection enum 和检测函数
Chunk 2: 请求转换   — OpenAI 请求体 → Anthropic 请求体
Chunk 3a: 响应转换  — Anthropic 响应 → OpenAI 响应（非流式）
Chunk 3b: 错误转换  — Anthropic 错误格式 → OpenAI 错误格式
Chunk 4: 流式转换   — Anthropic SSE → OpenAI SSE（逐行转换）
Chunk 5: 接线       — 把所有转换逻辑接入 sproxy.go 主流程
```

**关键设计**：Chunk 1 到 4 之间用"兼容性 shim"维持代码可编译。

```go
// Chunk 1 结束后插入的临时兼容函数（Chunk 5 时删掉）
// 让 sproxy.go 在 Chunk 2-4 施工期间仍能编译
func shouldConvertProtocol(convDir conversionDirection) bool {
    return convDir != conversionNone
}
```

**效果**：
- 任何时候都能 `go build`，任何时候都能跑测试
- 可以在 Chunk 3 完成后暂停，第二天从 Chunk 4 继续
- 中途出问题可以只回退单个 Chunk，不影响其他进度

---

#### 机制三：Step 的标准结构（最核心）—— 每步都有预期输出

每个 Step 不能只写"做什么"，必须同时写"预期看到什么"：

```markdown
**Step 2.3**: 运行测试，确认它失败（验证 Red 状态）

```bash
go test ./internal/proxy/... -run TestOtoARequest -v -count=1
```

**Expected（必须看到）**:
```
--- FAIL: TestOtoARequest
    converter_test.go:45: undefined: convertOpenAIToAnthropicRequest
FAIL
```

注意：这里预期的是**编译错误**，不是测试逻辑失败。
如果看到的是 PASS，说明测试写错了，没有真正测到预期的函数。
```

三个要素缺一不可：
1. **精确命令**：含 `-run` 过滤（不用 `go test ./...` 这种大炮打蚊子）
2. **Expected 字段**：不只说"运行测试"，说"预期看到什么"
3. **失败原因解释**：预期的失败是什么类型，为什么应该失败

---

#### 机制四：验证失败是独立的 Step（最容易被省略，最不该省略）

标准的 TDD 循环：

```
Step N.1 → 写失败的测试
Step N.2 → 运行测试，验证它确实失败   ← 这一步最容易被跳过
Step N.3 → 实现功能代码
Step N.4 → 运行测试，验证通过
Step N.5 → 运行完整包测试，确认无回归
Step N.6 → git commit
```

**为什么 Step N.2 不能省**：这是防止 AI 自欺欺人的核心机制。

AI 有时会写完测试直接报告"测试通过"——可能是测试写得有问题，可能是测的不是预期路径。"验证它确实失败"这一步让任何作弊行为无处遁形：功能还没实现，测试就应该失败。如果通过了，一定是测试写错了。

---

#### 机制五：Commit 作为检查点

每个 Task 末尾必须有明确的 commit 指令：

```markdown
**Step 2.6**: 提交当前进度

```bash
git add internal/proxy/converter.go internal/proxy/converter_test.go
git commit -m "feat(proxy): add OtoA request body conversion

- convertOpenAIToAnthropicRequest: maps messages, tools, tool_choice
- Handles tool message merging (consecutive role=tool → tool_result blocks)
- Strips OpenAI-only fields: n, logprobs, presence_penalty, stream_options
- 15 new test cases, all passing"
```
```

**为什么**：
- Commit 是进度的物理锁定，下一个 Task 开始前，这个 Task 的成果已经固化
- AI 在后续 Task 里不会意外"优化掉"已完成的代码
- `git log` 成为客观的完成记录，每个 Chunk 是否真的完成，一目了然

> 模板见：[templates/plan-template.md](./templates/plan-template.md)

### 实操教训：Plan 省略了什么，就会在哪里出问题

**案例：token 计数顺序**

Plan 里如果没有明确写操作顺序，AI 自然的写法是先转换格式，再计 token：

```go
// AI 自然写出的顺序（错误）
body = convertAnthropicToOpenAI(rawBody)  // 先格式转换
tw.RecordNonStreaming(body)               // 再计 token → 看到 OpenAI 格式 → 计出 0
```

正确顺序应该是：

```go
// 正确顺序（必须在 Plan 里显式说明）
tw.RecordNonStreaming(rawBody)            // 先用 Anthropic 原始格式计 token
body = convertAnthropicToOpenAI(rawBody) // 再转给客户端
```

顺序搞反：token 计数全部为 0，数据全乱，而代码看起来"完全正确"。

**这种"执行顺序"的约束，无法从函数签名或类型系统推断，只能在 Plan 里显式写出。**

---

## Part 4：AI 特有的失败模式

> 📊 **可视化**：六类失败模式雷达图见 [failure-patterns.html](./failure-patterns.html)

### 方法论

AI 生成的代码，主路径通常是对的。坑集中在几类固定的"盲区"。

提前认识这些模式，可以定向 review，而不是全量 review——全量 review 的成本和不用 AI 差不多，定向 review 才能最大化收益。

### 六类失败模式（真实案例）

---

#### 失败模式一：算法属性的副作用改变了用户预期

**案例（v2.15.0）**：HMAC 是确定性函数，同一用户名 + 同一 secret → 永远同一个 Key。

用户点击"重新生成 API Key"按钮，期望得到一个新 Key，但实际得到了完全相同的 Key。

AI 实现了正确的算法，通过了所有测试，但没有考虑"幂等性会改变用户预期"这个 UX 问题。

**防护**：Spec 的 Success Criteria 里，功能正确性之外，必须单独列"用户行为预期"这一条。

---

#### 失败模式二：降级策略过于严格，宁可系统不可用也要"正确"

**案例（v2.9.1）**：协议转换时遇到不支持的 `thinking` 参数 → AI 选择返回 HTTP 400。

结果：Claude Code 开启 extended thinking 模式后，整个工具完全罢工，用户无法发出任何请求。

AI 的逻辑是正确的：遇到不支持的参数就报错。但在中间件/网关场景，**保持服务可用 > 返回精确错误**。

修复：静默剥离 `thinking` 参数，继续转发请求。

**防护**：在 Spec 里针对每种"遇到不支持的输入"的处理策略，明确写出：降级、透传、还是报错。

---

#### 失败模式三：缓存的安全假设——命中即信任

**案例（v2.9.3）**：JWT 验证命中缓存后直接放行，不再校验用户是否仍处于激活状态。

结果：管理员禁用某个用户，但该用户的 JWT 仍在缓存 TTL（24小时）内有效，禁用操作在 TTL 期间完全无效。

AI 遵循的是"缓存命中即有效"的通用缓存原则——技术上正确，但在安全场景下是漏洞。

修复：缓存命中后额外执行一次 `IsUserActive` 查询（主键索引，< 1ms）。

**防护**：在 Spec 里对每个缓存点，明确写出"哪些状态变化需要 bypass 缓存"。安全相关的缓存不能靠 AI 的"常识"。

---

#### 失败模式四：构建元数据静默失效

**案例（v2.9.4）**：

```dockerfile
# Dockerfile 里的 ldflags 路径写错了
RUN go build -ldflags "-X github.com/wrong/path.Version=${VERSION}" ...
```

Go 的 `-X` flag 在路径不存在时**静默跳过，不报错，不警告**。所有发布版本的 `./sproxy version` 始终显示 `dev`。查了很久才发现，已经有几个版本的二进制元数据是错的。

同批次还有一个错误：`golang:1.25-alpine`（1.25 不存在）→ build 失败，相对容易发现。

**防护**：CI 里加断言：

```bash
./sproxy version | grep -qv "^dev$" || { echo "version injection failed"; exit 1; }
```

可验证的构建产物属性，必须在 CI 里显式断言，不能依赖 AI 主动检查。

---

#### 失败模式五：数据库 upsert 冲突键选错——业务语义盲区

**案例（v2.14.0）**：ConfigSyncer 用 `ON CONFLICT(id)` 做 upsert。

问题：`id` 是每次启动时生成的 UUID。Primary 和 Worker 对同一个 LLM Target URL 生成了不同的 UUID。

`ON CONFLICT(id)` 永远不命中 → 走 INSERT → 触发 `url` 的唯一索引 → UNIQUE constraint failed。

Worker 节点日志持续报错，ConfigSyncer 完全失效，两个节点数据开始分叉。

修复（v2.14.1）：`ON CONFLICT(url)` —— 业务唯一标识是 url，不是 uuid。

**防护**：数据库 Schema 里，每张表的"业务唯一键"必须在 Spec 或 Schema 注释里标注。

```sql
-- llm_targets 表
-- 业务唯一键：url（同一 URL 只能有一条记录）
-- id 是内部主键，不是业务标识，不能作为 upsert 的冲突键
CREATE TABLE llm_targets (
    id   TEXT PRIMARY KEY,  -- 内部 UUID，不稳定
    url  TEXT UNIQUE NOT NULL,  -- 业务唯一键，upsert 用这个
    ...
);
```

---

#### 失败模式六：前端视觉交互的盲区

**案例（v2.9.2）**：Dashboard 的"我的用量"页面，图表高度不断增加，最终填满整个页面，无法滚动。

原因：Chart.js 的 `responsive: true` + `maintainAspectRatio: false` 组合，图表高度 = 父元素高度，图表渲染后撑大父元素，父元素变大触发图表重绘，形成正反馈循环。

AI 不会主动在浏览器里打开页面拖动窗口测试，它认为"代码看起来对"就是对了。

修复：用固定高度的 `<div>` 包裹 `<canvas>`：

```html
<div style="height: 300px; position: relative;">
    <canvas id="usageChart"></canvas>
</div>
```

**防护**：前端视觉交互类改动，必须人工在浏览器里跑一遍。
这类问题 AI 无法覆盖，不在"可自动验证"的范围内。

---

### 六类失败模式的共同规律

AI 的失误不在主路径，而在：

```
算法属性的副作用  ×  用户行为预期
降级策略的选取    ×  中间件场景特殊性
缓存有效性假设    ×  安全场景的状态变化
构建产物元数据    ×  静默失败的工具行为
数据库业务语义    ×  内部 ID vs 业务标识
前端视觉交互      ×  无法自动验证
```

**识别这些模式，可以把 review 工作量减少 60% 以上，同时提高发现率。**

> 完整检查表见：[templates/ai-failure-checklist.md](./templates/ai-failure-checklist.md)

---

## Part 5：验收——让 AI 自我审查，让数字说话

### 方法论

代码实现完≠功能交付完。大多数人让 AI 实现完，自己 review 一遍，觉得"差不多"就提交。

更好的做法：**让 AI 用结构化模板验收自己的工作，你审验收报告，而不是审代码**。

验收报告的信噪比远高于代码 review：一份标准的验收报告能在 10 分钟内让你判断"能不能合并"。

### 验收报告的必备结构

```markdown
## 验收报告：[功能名称] v[版本号]

### 测试结果

| 类型         | 总数  | 通过  | 失败 | 状态      |
|-------------|-------|-------|------|-----------|
| 单元测试      | 2,090+ | 2,090+ | 0    | ✅ PASS   |
| 集成测试      | 8     | 8     | 0    | ✅ PASS   |
| E2E (httptest) | 90+  | 90+   | 0    | ✅ PASS   |
| E2E (真实进程) | 4    | 4     | 0    | ✅ PASS   |

### 覆盖率

| 包                   | 覆盖率 |
|---------------------|--------|
| internal/keygen     | 97.7%  |
| internal/quota      | 95.8%  |
| internal/proxy      | 83.8%  |

### 测试过程中发现并修复的问题

1. **cluster_multinode_e2e_test.go 认证失败（401）**
   - 现象：集群多节点测试认证报 401
   - 根因：doRequest() 函数里多加了 Authorization header，与测试预期冲突
   - 修复：移除该 header
   - 验证：✅ 所有集群测试通过

### 安全性评估
（此功能涉及认证/加密时必填）

### 性能影响
（此功能影响请求路径时必填）

### 四维度评分

| 维度       | 评分   | 说明                          |
|-----------|--------|-------------------------------|
| 功能完整性  | 5/5   | 所有 Spec 要求均已实现         |
| 代码质量   | 5/5   | 符合项目规范，有必要注释        |
| 测试覆盖   | 4/5   | 核心路径 100%，CLI 入口偏低    |
| 文档完整性  | 5/5   | UPGRADE.md 和 manual.md 已更新 |

### 结论

**✅ 通过验收** / ❌ 未通过（原因：...）
```

### 最关键的字段：测试过程中发现并修复的问题

要求 AI 写这个字段，是对"只汇报好消息"动机的定向对抗。

这个字段迫使 AI 把施工过程中遇到的问题暴露出来——**哪怕它自己修了，你也需要知道**。

一个自称"一切顺利"的验收报告，和一个列出"我发现了 3 个问题，修复方式如下"的验收报告，后者可信度远高于前者。

### 数字是不能嘴炮的

```
"所有测试通过"      → 无法审计，可能是没跑测试
"1,894 PASS, 0 FAIL" → 可以审计，运行 go test 就能验证
```

**要求 AI 输出可审计的具体数字，不接受纯文字的"验收通过"结论。**

这不是不信任 AI，而是给 AI 施加"可验证的压力"——知道输出会被核验，AI 的自我审查更认真。

---

## Part 6：方法论总结

> 📊 **可视化**：五个控制点流程管线见 [control-pipeline.html](./control-pipeline.html)

### 五个控制点（总览）

```
┌─────────────────────────────────────────────────────────────────┐
│                     AI Coding 五个控制点                        │
├──────────────┬──────────────────────────────────────────────────┤
│ 1. 上下文管理 │ CLAUDE.md：把"不言而喻"写出来                   │
│             │ → 架构决策、已知禁区、环境约束、代码风格           │
├──────────────┼──────────────────────────────────────────────────┤
│ 2. 任务定义  │ Spec：Why + What + Success Criteria              │
│             │ → 具体反例、已拒绝方案、量化标准、Out of Scope    │
├──────────────┼──────────────────────────────────────────────────┤
│ 3. 执行控制  │ Plan：File Map + Chunk + Step + Expected         │
│             │ → 范围约束、依赖序分块、验证失败、Commit 检查点   │
├──────────────┼──────────────────────────────────────────────────┤
│ 4. 验证机制  │ TDD 强制：先写测试 → 验证失败 → 实现 → 验证通过  │
│             │ → 不可跳过"验证失败"这一步                       │
├──────────────┼──────────────────────────────────────────────────┤
│ 5. 交付验收  │ 结构化验收报告：数字 + 过程问题透明              │
│             │ → 可审计的测试数字、主动暴露修复过的 bug          │
└──────────────┴──────────────────────────────────────────────────┘
```

### 三个反直觉洞察

**第一：减少 AI 的决策空间，比扩大 AI 的能力更有效**

把实现代码直接写进 Plan，AI 的工作从"设计 + 实现"变成"按图执行 + 验证"。

看起来"浪费了 AI 的创造力"，但交付的可预测性大幅提升，返工率接近零。

**第二：给 AI 的文档和给人的文档必须分开写**

给人的文档：解释背景，说清楚是什么
给 AI 的文档：约束行为，说清楚做什么和不做什么

混在一起，两头都失效：人看不完那么多约束，AI 提取不到需要的行为规范。

**第三：AI 的失误是有规律可循的，不是随机的**

六类失败模式你现在已经知道了——算法副作用、降级策略、缓存安全、构建元数据、数据库语义、前端交互。

**定向 review 这六类，不做全量 review**。全量 review 的成本趋近于不用 AI。

### 什么时候这套流程值得用

| 场景 | 建议 |
|------|------|
| 单文件 / < 100 行改动 | 直接让 AI 做，简单 review |
| 多文件协同 / 有数据库改动 | 写 Spec，不需要完整 Plan |
| 跨多个包 / 涉及协议或安全边界 | 完整 Spec + Plan + 验收报告 |
| Exploratory / 快速原型阶段 | 不要套这个流程，先跑起来再说 |

**判断标准**：如果这个任务的一个边界条件没考虑到，代价是否承受得起？代价大 → 写 Spec。

## Part 7：Harness Engineering——从"五个控制点"到系统化 AI 编程基础设施

### 什么是 Harness

2025 年下半年开始，AI 编程社区开始频繁讨论一个概念：**Harness Engineering**。

这个词来源于软件测试中的 "test harness"（测试线束）——让代码可被测试的基础设施。

AI Coding 中的 harness 是同一个意思：

> **Harness = AI 模型的运行时基础设施。它定义模型能看到什么上下文、能调用什么工具、如何被验证、失败后如何恢复。**

公式化表达：

```
coding agent = AI model(s) + harness
```

Anthropic 在 2025 年发布的 "Effective harnesses for long-running agents" 中，把 harness 的核心问题总结为：**跨 context window 的一致性**——AI 的每次会话都是全新开始的，需要基础设施帮它"记住"上次做到哪了。

HumanLayer 的工程师写了一篇流传很广的 "Skill Issue: Harness Engineering for Coding Agents"，核心观点更尖锐：

> **模型可能没问题，是你的 harness 有问题。**

---

### Harness 的六个配置面

结合 Anthropic 官方实践、HumanLayer 的工程经验、以及 Stripe/Shopify/Airbnb 的企业实践，harness 有六个可配置的层面：

```
┌────────────────────────────────────────────────────────────────────┐
│                   Harness 的六个配置面                              │
├──────────────────┬─────────────────────────────────────────────────┤
│ 1. Context Files │ CLAUDE.md / AGENTS.md                           │
│                  │ → 注入项目知识、架构决策、编码规范               │
├──────────────────┼─────────────────────────────────────────────────┤
│ 2. Tools / MCP   │ 扩展 agent 能力的工具接口                       │
│                  │ → 文件系统、Shell、浏览器、数据库                │
├──────────────────┼─────────────────────────────────────────────────┤
│ 3. Sub-agents    │ 上下文隔离与任务分解                             │
│                  │ → 上下文防火墙，防止 context rot                │
├──────────────────┼─────────────────────────────────────────────────┤
│ 4. Skills        │ 渐进式知识披露                                  │
│                  │ → 按需加载指令，不浪费 context window            │
├──────────────────┼─────────────────────────────────────────────────┤
│ 5. Hooks         │ 自动化控制流                                    │
│                  │ → 代码格式化、类型检查、构建验证                 │
├──────────────────┼─────────────────────────────────────────────────┤
│ 6. Back-pressure │ 验证回压                                        │
│                  │ → 测试、Lint、编译检查，强制 AI 修正             │
└──────────────────┴─────────────────────────────────────────────────┘
```

**回头看 PairProxy 项目的五个控制点，它们恰好覆盖了这六个配置面的核心：**

| Harness 配置面 | PairProxy 控制点 | 实际体现 |
|---|---|---|
| Context Files | 控制点 1：上下文管理 | AGENTS.md（架构决策、Fail-Open 原则、并发规范） |
| Tools / MCP | Claude Code 内置 | 文件读写、Bash 执行、LSP 诊断 |
| Sub-agents | Claude Code Explore/Bash | 代码探索、命令执行的上下文隔离 |
| Skills | 项目中未显式使用 | — |
| Hooks | CI 中的验证断言 | version injection 检查、`-race` 检测 |
| Back-pressure | 控制点 4+5：验证+验收 | `make test`、`go vet`、golangci-lint、验收报告 |

---

### PairProxy 的 Harness 全景：分层文档体系

> 📊 **可视化**：五层文档体系交互图见 [harness-layers.html](./harness-layers.html)

回顾项目的 ~100 个 Markdown 文件，它们不是随意堆叠的，而是形成了一个 **5 层 13 类** 的文档体系：

```
                    ┌──────────────────────┐
                    │ L0: AI 运行时上下文    │  ← AI 首先读到的
                    │ AGENTS.md / CLAUDE.md │
                    └──────────┬───────────┘
                               ↓
                    ┌──────────────────────┐
                    │ L1: 产品规划          │  ← 为什么做
                    │ ROADMAP / CHANGELOG   │
                    └──────────┬───────────┘
                               ↓
                    ┌──────────────────────┐
                    │ L2: 设计             │  ← 做什么
                    │ Spec / Design Review  │
                    │ Plan                  │
                    └──────────┬───────────┘
                               ↓
                    ┌──────────────────────┐
                    │ L3: 实施与验证        │  ← 做出来 + 验得证
                    │ Progress / Acceptance │
                    │ Test Report           │
                    └──────────┬───────────┘
                               ↓
                    ┌──────────────────────┐
                    │ L4: 知识沉淀          │  ← 不再重复犯错
                    │ Learnings             │
                    │ Failure Checklist     │
                    │ Templates             │
                    └──────────────────────┘
```

**这个分层体系本身就是 Harness 的一部分**——它确保 AI 在每个阶段都能找到正确的上下文，而不是在一堆杂乱的文档中迷失。

---

### 实际案例：PairProxy 中的 Harness 实践

#### 案例 1：AGENTS.md 作为 Context Harness 的演进

这个项目的 AGENTS.md 经历了四次重写：

| 阶段 | 内容 | 效果 |
|------|------|------|
| v1：运维手册 | CLI 命令列表 | AI 知道怎么管用户，不知道怎么写代码 |
| v2：代码风格指南 | 命名规范、错误处理、日志约定 | review 改动量减少 ~70% |
| v3：架构决策记录 | Fail-Open 原则、两层状态同步、已知禁区 | AI 第一次就能给出符合项目风格的代码 |
| v4：并发规范 | WaitGroup 生命周期、goroutine 清理模式 | v2.22.0 的 7 小时调试 bug，此后再未复发 |

**关键洞察**：AGENTS.md 不是一次性写好的，是"踩坑→文档化→不再重复踩"的正循环产物。每次 AI 犯了新类型的错，就把教训写回去。

---

### 深度剖析：AGENTS.md 的结构、方法论与最佳实践

上面的案例讲了 AGENTS.md 的演进过程。这一节把它拆开，看看里面到底写了什么、为什么这样写、以及你自己的项目应该怎么写。

#### AGENTS.md 是什么？——AI 的"员工手册"

把 AI 想象成一位新入职的高级工程师：代码能力很强，但第一天来上班，不知道你们团队的习惯。你不可能站在他身后每行代码都指导——你需要一本员工手册。

**AGENTS.md 就是这本手册。** 它解决的核心问题是：

> AI 每次打开项目，都是第一天上班。它不记得昨天做了什么、上周踩过什么坑、你们团队的代码风格有什么特殊要求。

这份文件在 463 行里浓缩了一个项目的全部隐性知识。以下是它的七个章节和设计意图：

#### 七个章节的设计逻辑

```
┌─────────────────────────────────────────────────────────────┐
│                  AGENTS.md 的知识架构                         │
├──────────────────┬──────────────────────────────────────────┤
│ 1. Build & Test  │ 环境入口：精确命令，不是"跑一下测试"       │
│ 2. Code Style    │ 行为约束：AI 不读代码猜不到的隐性规则       │
│ 3. Testing       │ 质量防线：防回归 checklist + 并发规范       │
│ 4. Architecture  │ 认知地图：组件表 + 关键设计决策              │
│ 5. Configuration │ 配置契约：新增功能必须同步更新的文件         │
│ 6. 版本特性索引   │ 时间线记忆：每个版本引入了什么               │
│ 7. 复盘 & 举一反三│ 自进化机制：踩坑→规则化→永不复发           │
└──────────────────┴──────────────────────────────────────────┘
```

**这七个章节不是随意排列的，而是遵循一个"从操作到认知"的递进关系**：

1. 先告诉 AI 怎么操作（Build、Test 命令）
2. 再约束 AI 怎么写代码（Code Style）
3. 然后给出质量标准（Testing 规范）
4. 提供架构认知（Architecture、Config）
5. 最后是自我进化机制（复盘、举一反三）

#### 内容总览：AGENTS.md 里到底有什么

| # | 章节 | 核心内容 | 解决什么问题 |
|---|------|---------|-------------|
| 1 | Build & Test Commands | `make build`、`make test-race`、单函数测试命令、dev 工具命令 | AI 不知道你的构建和测试怎么跑 |
| 2 | Code Style | Go 1.24、PascalCase/camelCase 命名、import 分组、Error wrapping、Fail-Open 原则、Zap 日志四级规范 | AI 不读 100 个文件猜不到的隐性规则 |
| 3 | Testing | 三种 E2E 测试类型、Test Helper 模式、9 条防回归 Checklist（Once-set、Provider symmetry、Goroutine 生命周期…）、WaitGroup 并发规范、Race 调试三步法、Lint 规则 | 把踩过的坑变成规则，同类 bug 永不复发 |
| 4 | Architecture | 两层架构图、17 个 internal/ 包的职责表、4 条关键设计决策（协议支持、集群模式、直连代理、版本注入） | 给 AI 一张代码地图，改文件前知道影响范围 |
| 5 | Configuration & API | YAML 格式约定、sproxy.yaml 完整示例、REST 端点、分页/错误响应约定、GORM 数据库约定、协议支持矩阵 | 新功能该改哪些配置文件、API 怎么对齐 |
| 6 | Admin CLI & Version | 完整 CLI 子命令清单、v2.9.0 ~ v2.24.5 版本特性索引 | AI 知道"这个功能从哪个版本开始有" |
| 7 | 复盘 & 举一反三 | 复盘四步法（记录路径→归因→提炼规律→更新知识库）、举一反三三件事（溯源同类风险→补测试→沉淀规则）、触发标准、Pre-commit Checklist | 自进化闭环：犯错→规则化→永不复发 |

**一些数字**：全文 463 行。其中 Testing 章节最厚（~180 行，占 40%），Code Style 和 Architecture 各约 60 行，复盘机制约 50 行。Testing 之所以最厚，是因为**大多数 AI 犯的错都集中在测试环节**——测试写得不够、测试写错了、测试没覆盖边界。

#### 逐章拆解：写什么、不写什么

**章节 1：Build & Test Commands**

```markdown
## Build & Test Commands
make build              # Build cproxy + sproxy to bin/
make test-race          # Run with race detector
go test -v -run TestCheckerNoGroup ./internal/quota/   # 单个测试函数
```

**设计意图**：精确到可以直接复制粘贴的命令。不要写"运行测试"——AI 不知道你的测试命令是什么。

**❌ 不写**：`请认真运行测试`（废话，AI 本来就会跑测试）
**✅ 写**：`make test-race`（AI 不知道你的项目需要 race detector）

---

**章节 2：Code Style**

这里最有价值的不是命名规范（AI 大概率已经知道 PascalCase），而是**项目特有的隐性规则**：

```markdown
## Error Handling
- Fail-open：Quota/database errors must NOT block requests — log warning and bypass
  user, err := s.userRepo.GetByID(userID)
  if err != nil {
      s.logger.Warn("failed to get user, bypassing", zap.Error(err))
      return nil // fail-open
  }
```

**设计意图**：`fail-open` vs `fail-closed` 是架构级决策，AI 从代码里不一定能推导出来。如果一个新功能涉及 quota 查询，AI 必须知道"查询失败时放行"——这不是代码风格，是业务规则。

**❌ 不写**：`错误处理要友好`（AI 不知道"友好"在这个项目里意味着什么）
**✅ 写**：`fail-open` + 代码示例 + 理由

---

**章节 3：Testing（最厚的一章，占全文 40%）**

这一章包含三个递进的层次：

```
第一层：测试框架和约定（工具层）
  → 用什么框架、文件命名规则、test helper 模式

第二层：防回归 Checklist（经验层）
  → 踩过的坑变成的 9 条规则
  → "Once-set semantics"、"Provider symmetry"、"Goroutine 生命周期"……

第三层：并发测试规范（高级经验层）
  → WaitGroup 模式代码模板
  → Race 调试三步法
  → Test Cleanup Checklist
```

**设计意图**：前两层是"别犯已知的错"，第三层是"这类问题有结构化解决方案"。

举一个具体的例子——"Once-set semantics"规则：

```markdown
- **Once-set semantics**: 测试"写入后不被覆盖"的逻辑时，
  后续输入必须携带不同的值，相同值无法区分"写一次"和"写多次"
```

这条规则来自一个真实 bug：测试验证"SetOnce 只写一次"，但测试用的第二组值和第一组相同——结果无论 SetOnce 写了一次还是两次，assertion 都通过。写成规则之后，这类测试 bug 再没出现过。

---

**章节 4：Architecture**

这一章提供两张地图：组件表和关键设计决策。

组件表告诉 AI "代码在哪里"：

```markdown
| internal/proxy | HTTP handlers, middleware, Anthropic ↔ OpenAI protocol conversion |
| internal/tap   | SSE stream parsing, zero-buffering token counting |
```

关键设计决策告诉 AI "为什么这样设计"：

```markdown
- Protocol support: Anthropic, OpenAI, Ollama — auto-conversion between formats
- Cluster modes: SQLite (primary + workers) or PostgreSQL (peer mode)
```

**设计意图**：AI 需要知道改一个文件会影响哪些功能。比如改 `internal/tap` 的 SSE 解析逻辑，AI 需要知道它同时影响 token 计数——组件表建立这个认知。

---

**章节 5：复盘机制 & 举一反三（最独特的一章）**

这是整份文件最有价值的部分，也是大多数项目的 AGENTS.md 缺失的部分。

```markdown
## 解题复盘机制

每次修复 bug 或经历多轮尝试才解决的问题，完成后必须做一次复盘。

1. 记录有效路径
2. 归因根本原因
3. 提炼可复用规律
4. 更新知识库（补充到本文件）
```

```markdown
## 举一反三原则（Bug 发现即普查）

发现一个 bug，必须同步完成三件事：
1. 溯源同类风险 — 同样的根因在项目中还有哪些地方？
2. 补充覆盖全场景的测试
3. 沉淀为不可绕过的规则
```

**设计意图**：这两节构成一个**自进化闭环**：

```
犯错 → 复盘（归因）→ 举一反三（普查）→ 写入 AGENTS.md（规则化）→ 永不复发
```

这个闭环的核心洞察是：**AI 犯的错是有模式的。每次发现新模式，把它变成规则，这个模式就永远消失了。**

PairProxy 项目的 7 条 Learnings，每条都对应一个从 bug 提炼出的 Anti-Pattern。写入 AGENTS.md 之后，此后的实现 AI 都会主动检查——不是因为我们告诉 AI "小心点"，而是因为我们把检查写成了规则。

#### 最佳实践：写好 AGENTS.md 的五个原则

**原则一：只写 AI 不读代码就不知道的东西**

```
AI 看代码就知道的：  函数签名、类型定义、import 路径
AI 看代码不知道的：  架构决策理由、禁区、降级策略、执行顺序约束
```

如果你的 AGENTS.md 里写满了"请认真写单元测试"——那是废话。如果写了"fail-open 原则：quota 查询失败时不阻断请求"——那才是 AI 不读 100 个文件就不知道的隐性知识。

**原则二：用代码示例代替自然语言描述**

```
❌ "日志要分级"
✅ "DEBUG: 每个请求的 token 数（生产关闭）| WARN: 可恢复错误 | ERROR: 需人工介入"
```

代码示例消除了歧义。AI 拿到一个具体的代码模板，比读一段抽象描述更准确。

**原则三：踩过的坑必须沉淀为可执行规则**

不是"我们踩过这个坑"的叙事，而是"以后遇到类似情况，按这条规则执行"的检查项：

```
❌ 叙事：我们曾经因为 GORM 忽略 bool 零值出了 bug
✅ 规则：凡含 default:true 的 bool 字段，Create 路径必须显式设值
```

叙事不会改变 AI 的行为。规则会。

**原则四：文件保持活的状态，随项目演进**

PairProxy 的 AGENTS.md 经历了四次重写（运维手册→代码风格→架构决策→并发规范）。每次踩坑后都更新。如果 AGENTS.md 六个月没改过，要么项目停滞了，要么踩的坑没被记录。

**原则五：两份文件保持一致**

`AGENTS.md` 和 `CLAUDE.md` 内容完全相同——因为不同的 AI 工具读不同的文件（OpenCode 读 AGENTS.md，Claude Code 读 CLAUDE.md）。维护一份，同步到另一份。不一致比没有更危险。

#### 一个检验标准

写完 AGENTS.md 之后，问自己一个问题：

> **如果 AI 读完这份文件后直接开始编码，它第一次提交的代码，需要多少改动？**

如果改动量 > 50%，说明 AGENTS.md 缺少关键的行为约束。
如果改动量 < 20%，说明 AGENTS.md 已经覆盖了核心隐性知识。

PairProxy 项目在重写 AGENTS.md（从运维手册→架构决策记录）之后，**首次提交的 review 改动量从约 50% 降到了约 15%**。

---

这和 HumanLayer 的观点完全一致：

> "每次 agent 犯了一个错误，你花时间工程化一个解决方案，让 agent 再也不会犯那个错误。"

#### 案例 2：Spec 的"拒绝方案"防止 AI 聪明反被聪明误

HMAC Keygen 的 Spec 里有一个经典的"拒绝方案"：

> **已拒绝的备选方案**：
>
> | 方案 | 拒绝原因 |
> |------|---------|
> | UUID v4 随机生成 | 用户重新生成会得到不同 Key，无法幂等 |
> | 指纹嵌入（旧方案）| 字符集碰撞漏洞（alice123 vs 321ecila） |

如果没有这个"拒绝方案"，AI 在实现时很可能"优化"回旧方案——因为旧方案更简单。

**Spec 的"拒绝方案"比"选择方案"更重要**。它防止 AI 用正确的逻辑推导出错误的结论。

#### 案例 3：Learnings 是 AI 的外挂记忆

项目的 `.learnings/LEARNINGS.md` 记录了 7 条踩坑经验，每条都有 Anti-Pattern 总结。例如：

> **Anti-Pattern**：当系统有 DB 持久层 + 内存运行时层两层状态时，写操作必须同时更新两层。只写 DB 会导致两层永久分裂。
>
> 每次 handler 只调用 repo 写 DB 时，必须问：**运行时内存状态有没有同步更新？**

这条教训来自 v2.19.0 的一个 bug：通过 WebUI 添加 LLM target 后，DB 里有了，但内存中的负载均衡器不知道，新 target 永远"不健康"。

**写进 LEARNINGS.md 之后，此后的所有涉及写操作的实现，AI 都会主动检查"两层状态是否同步"。** 这是 harness 的"自动防错"机制。

#### 案例 4：Progress File 解决跨会话连续性

`.sisyphus/README_CURRENT_STATE.md` 包含：
- 当前编译状态
- 已完成的代码改动清单
- 下一步改动点（分优先级）
- 文档清单和必读指南
- 预计剩余工时

这就是 Anthropic 文章中提到的 **progress file** 的实践。每次 AI 新开会话，先读这个文件，就知道"上次做到哪了、接下来做什么"。没有这个文件，AI 要花 20-30 分钟重新理解项目状态。

---

### 从 PairProxy 提炼：理想的 AI Coding 文档体系

综合这个项目的实践和行业趋势，一个 AI Coding 项目应该包含以下文档：

**第 0 层：AI 运行时上下文**（AI 每次会话首先读到）

| 文档 | 作用 | 必含要素 |
|------|------|---------|
| `AGENTS.md` | 项目知识和行为约束 | 架构决策、已知禁区、编码规范、验证命令 |
| `config/*.example` | 配置接口契约 | 新功能必须同步更新 |

**第 1 层：产品规划**（"为什么做"）

| 文档 | 作用 |
|------|------|
| `ROADMAP.md` | 版本路线图、优先级、影响范围 |
| `CHANGELOG.md` | 变更记录 |
| `RELEASE_NOTES` | 各版本发布说明 |

**第 2 层：设计**（"做什么"——最重要的层）

| 文档 | 作用 | 必含要素 |
|------|------|---------|
| `Spec` | 功能边界和验收标准 | 问题反例、**拒绝方案**、量化标准、Out of Scope |
| `Design Review` | AI 对 Spec 的独立审查 | 问题清单、改进方案 |
| `Plan` | 可执行步骤 | **File Map**、Chunk 分解、Step+Expected |

**第 3 层：实施与验证**（"做出来 + 验得证"）

| 文档 | 作用 |
|------|------|
| `Progress` | 当前状态、完成到哪、下一步 |
| `Acceptance Report` | 结构化验收报告 |
| `Test Report` | 覆盖率、失败分析 |

**第 4 层：知识沉淀**（"不再重复犯错"——最被低估的层）

| 文档 | 作用 |
|------|------|
| `Learnings` | 踩坑记录 + Anti-Pattern |
| `Failure Checklist` | 分类的定向检查表 |
| `Templates` | 可复用的 Spec/Plan/CLAUDE.md 模板 |

---

### Harness 的本质

回到核心命题：

**Harness 不是让 AI 更聪明，是让 AI 更可靠。**

没有 harness，AI 是一个天才实习生——能写代码，但你不知道它会写出什么。有了 harness，AI 变成一个有纪律的工程师——在约束范围内发挥能力，每一步都可验证、可回溯。

Stripe 的实践说得更直白：

> 一个窄范围的 agent 可以被测试、被信任。一个宽范围的 agent 难以推理。

PairProxy 项目从 v1.0 到 v2.24.6 的 390 次提交，本质上就是一步步构建 harness 的过程。五个控制点（上下文管理、任务定义、执行控制、验证机制、交付验收）是 harness 的骨架；六类失败模式检查表是 harness 的免疫系统；Learnings 是 harness 的记忆。

---

## Part 8：Git 提交记录中的蛛丝马迹——AI 协作的真实证据

前面七个 Part 讲的是方法论。这个 Part 做一件事：**从 Git 提交记录中寻找这些方法论被实际使用的证据**。

不是我们"认为"自己怎么做的，而是提交记录"证明"我们确实这么做了。

### 证据一：头脑风暴——AI 对 AI 的严厉批评

`.sisyphus/SHARP_CRITIQUE.md` 是一份 335 行的文档，标题叫"犀利点评：初稿设计与 Review 的致命缺陷"，开头就写：

> "初稿设计：一份看似完整但**根本不可行**的设计"

这份文档是 Model-Aware Routing 功能的设计过程中产出的。它给初稿设计评了 55 分，给初稿的 Review 评了 **35 分**——"review 质量比设计本身更差"。

它指出的致命问题包括：

1. **整个架构基于错误假设**：初稿假设 `SyncLLMTargets` 会修改 `sp.targets`，但代码中这个函数从来不改。所有基于此推导的"并发安全问题"都是虚构的。
2. **热更新不可行**：WebUI 修改了 `supported_models`，但路由查询仍然查静态字段，必须重启才能生效——违反了功能自身的核心目标。
3. **Review 识别了错误的问题**：花了大量篇幅讨论一个不存在的并发竞态，同时遗漏了 3 个真实的致命缺陷。

对应的还有 `QUALITY_ASSESSMENT.md`，用 5 个维度给方案打分，逐条列出优缺点。

**这说明什么？** 项目使用了多版本对比的头脑风暴模式：先出方案 A，再让 AI 批评方案 A，最后产出方案 B。批评本身也被文档化、评分化，不是随意的"我觉得可以改进"。

Spec 的迭代也有类似证据。OtoA（OpenAI→Anthropic）协议转换的 Spec 在 3 天内经历了 5 次修改：

```
03-14 16:08 → docs: add OpenAI→Anthropic protocol conversion design spec
03-14 16:24 → docs(spec): revise OtoA conversion design — address spec review issues
03-18 15:22 → fix(spec): address spec review issues
03-18 15:31 → fix(spec): correct code structure to match actual implementation
03-18 15:33 → fix(spec): remove orphaned code snippet at line 443
```

写→审→改→验的快速循环，不是一次性产出。

### 证据二：多 Agent 并行——2 小时 8 个模块

Group-Target Set 功能在 2026-03-27 的提交记录：

```
11:34 → feat(group-target-set): implement core database and selection logic
11:36 → feat(alert): implement alert manager and health monitor
11:36 → feat(api): implement admin API handlers for target sets and alerts
11:53 → feat(group-target-set): add comprehensive unit tests
12:07 → feat(proxy): add group target set integration layer
12:12 → feat(cli,config): add admin CLI commands and configuration support
12:13 → test(proxy): add comprehensive end-to-end integration tests
13:24 → test(e2e): add end-to-end tests for Group-Target Set and alert management
```

**2 小时内完成了 8 个不同模块的实现**——db、alert、api、proxy、cli、config、测试。提交粒度极细（每个只改一个包），时间间隔只有几分钟。这高度符合多个 sub-agent 并行执行再汇总提交的模式。

更直接的证据是文档中的**多模型署名**：

```
# group-target-set-pooling-alerting.md（4629 行的 Spec）
作者: Claude Sonnet 4.6
审查者: Claude Haiku 4.5

# ACCEPTANCE_REPORT.md（1172 行的验收报告）
验收人员: Claude Opus 4.6
验收人员: Claude Haiku 4.5
```

不同模型承担不同角色：
- **Sonnet 4.6**：写 Spec（需要理解和推理能力）
- **Haiku 4.5**：做 Review（快速、便宜，适合检查类任务）
- **Opus 4.6**：做验收（最强推理，适合最终判断）

这正是 HumanLayer 描述的"昂贵模型做规划，便宜模型做执行"模式。

还有一个有趣的痕迹：项目中存在 **GLM（智谱）模型参与**的证据——`test/integration/integration_by_GLM5_test.go` 和 `test(tap): add 2 missing GLM-style SSE parser edge cases`。这说明项目通过 PairProxy 自身的路由功能，让不同模型处理不同类型的任务。

### 证据三：Skills 的使用痕迹

项目中有几个目录结构直接反映了 skill/工具的使用：

**`.sisyphus/` 目录 = Sisyphus Agent 框架的产物**

12 个文件，包括 plans/、IMPLEMENTATION_PROGRESS.md、SHARP_CRITIQUE.md 等。这说明项目在后期（v2.24.x）使用了 Sisyphus Agent 框架进行任务编排。

**`.learnings/` 目录 = Self-Improvement Skill 的产物**

`LEARNINGS.md` 中 7 条踩坑经验，每条都有结构化格式：

```
LRN-20260325-005
Priority: critical
Summary: WebUI 增删改 LLM target 后，运行中的 llmBalancer 从未被通知
Anti-Pattern: 当系统有 DB 持久层 + 内存运行时层两层状态时，写操作必须同时更新两层
```

**提交规范中的 Co-Authored-By**

项目的 `DEVELOPMENT.md` 中明确要求 commit 时写：
```
Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

项目把 AI 当作**协作者**而非工具。

### 证据四：测试驱动开发的四个层次

这个项目的 TDD 不是教科书式的"先写测试再写代码"，而是一个更复杂的四层结构：

**第一层：Plan 中标注 TDD 步骤**

```
docs(plan): add direct proxy implementation plan with TDD steps
```

Plan 文档中明确写了 TDD 的执行顺序。

**第二层：测试覆盖率作为可追踪指标**

```
03-06 14:45 → test(dashboard): comprehensive tests → 覆盖率 58.4% -> 73.8%
03-07 01:35 → test: add AI-generated test cases to enhance coverage
03-07 01:04 → fix: resolve AI-generated test failures in db and integration tests
03-07 12:31 → test: add P0–P2 coverage tests for cluster/lb/proxy/dashboard
03-18 00:58 → test: comprehensive coverage improvements across all packages (+540 tests)
```

注意这个有趣的循环：
1. 让 AI 补测试
2. AI 补的测试也有 bug
3. 再让 AI 修测试
4. 修完还要跑 lint 修 lint 错误（`fix(lint): fix pre-existing lint failures in AI-generated test files`）

**AI 生成的测试也需要被验证**——这本身就是 TDD 精神的体现。

**第三层：举一反三的递归测试**

Issue #6 的修复过程是最精彩的 TDD 实践：

```
04-07 18:50 → refactor(db): add composite unique constraints
04-07 19:11 → refactor(db,api,cmd): systematic composite constraint fixes (举一反三)
04-07 20:16 → fix: close all composite-constraint ambiguity issues (tasks #22-27)
04-07 20:33 → fix: convergence round 2 — CLI and Seed gaps
04-07 21:55 → test: add comprehensive tests for critical bugs and concurrent operations
04-07 22:05 → analysis: comprehensive recursive problem analysis - 3 rounds complete
```

`analysis/comprehensive_recursive_analysis_report.md` 记录了完整的三轮递归分析：

```
第一轮：扫描所有 Where(..).First() 调用 → 发现 5 个候选问题
第二轮：对每个问题继续举一反三 → 识别 3 个根本模式（P1/P2/P3）
第三轮：代码扫描 + 约束验证 → 确认收敛，无新问题
```

最终产出：8 个待修复问题，按 CRITICAL/HIGH/LOW 分级，每个都有具体的代码位置和修复建议。

**举一反三不是口号，是可执行的递归流程，有终止条件（无新问题出现）。**

**第四层：Bug-Pattern 回归测试**

```
03-07 20:33 → test(db): add bug-pattern regression tests from historical bugs
```

配合 `.learnings/LEARNINGS.md` 中的 Anti-Pattern 总结，形成闭环：

```
犯错 → 记录模式 → 写回归测试 → 模式永不复发
```

### 证据汇总

| 方法论 | Git 提交中的证据 | 说服力 |
|--------|----------------|--------|
| **头脑风暴** | SHARP_CRITIQUE（AI 自评 35 分）、QUALITY_ASSESSMENT（方案打分）、Spec 3 天 5 次迭代 | ★★★★ |
| **多 Agent 并行** | 多模型署名（Sonnet 写 + Haiku 审 + Opus 验收）、GLM 参与、2 小时 8 模块 | ★★★★ |
| **Skills 使用** | `.sisyphus/` 目录、`.learnings/` 目录、模板化方法论、Co-Authored-By | ★★★ |
| **TDD** | Plan 标注 TDD 步骤、覆盖率 58%→73% 追踪、三轮递归举一反三、bug-pattern 回归 | ★★★★ |
| **验证回压** | `-race` 检测、golangci-lint、AI 测试也要修 bug、lint 不通过不合并 | ★★★★ |

**最独特的发现**：`.sisyphus/SHARP_CRITIQUE.md` 和 `analysis/comprehensive_recursive_analysis_report.md`——这两份文档只有纯 AI 协作项目才可能产出。人类很少会写一份 335 行的文档来批评自己初稿"根本不可行"，也很少会对一个 bug 做三轮递归举一反三直到确认"无新问题出现"。

---

## Part 9：390 次提交的节奏分析——开发阶段与趋势

前一个 Part 从单条提交中找证据。这个 Part 换一个视角：**把 390 次提交当作一条时间序列，看整体节奏**。

项目时间线：2026-02-27 → 2026-04-11，共 46 个日历天，其中 44 天有提交。

### 七个开发阶段

| 阶段 | 时间 | 活跃天数 | 提交数 | 版本跨度 | 主题 |
|------|------|---------|--------|---------|------|
| **1. 基础搭建** | 02-27 ~ 02-28 | 2 | 31 | v1.0 → v2.0 | 从零到可用：Auth、Proxy、DB、Config 四大模块同时成型 |
| **2. 功能爆发** | 03-05 ~ 03-09 | 5 | 93 | v2.1 → v2.5 | 最高密度期：负载均衡、Quota 管理、Dashboard、Token Tracking 密集上线 |
| **3. 协议转换** | 03-10 ~ 03-12 | 3 | 87 | v2.6 → v2.9.0 | 重量级重构：Anthropic↔OpenAI 双向转换、Ollama 支持、SSE 解析 |
| **4. 稳定化** | 03-14 ~ 03-20 | 7 | 49 | v2.9.1 → v2.15.0 | 节奏放缓：Bug 修复、边界情况处理、集群模式打磨 |
| **5. 智能化** | 03-23 ~ 03-29 | 7 | 49 | v2.18.0 → v2.22.0 | 新维度：语义路由、Corpus 数据收集、Agent 模拟测试 |
| **6. Bug 收敛** | 04-04 ~ 04-09 | 6 | 65 | v2.23 → v2.24.3 | 集中修 Bug：一次系统性的质量攻坚 |
| **7. 收尾打磨** | 04-10 ~ 04-11 | 2 | 4 | v2.24.4 → v2.24.6 | 极少提交：文档更新、微调配置 |

### 五个趋势

**趋势一：提交密度呈钟形曲线**

```
W09 (02/27-03/02): 31 commits  ████
W10 (03/03-03/09): 93 commits  ████████████████  ← 峰值
W11 (03/10-03/16): 87 commits  ██████████████
W12 (03/17-03/23): 32 commits  █████
W13 (03/24-03/30): 49 commits  ████████
W14 (03/31-04/06): 33 commits  █████
W15 (04/07-04/13): 52 commits  █████████
```

W10 + W11 两周贡献了 180 次提交（占总量 46%），是整个项目的核心产出期。这种"先爆发后收敛"的节奏，与 AI Coding 的特性高度吻合——AI 可以在短时间内生成大量代码，但验证和打磨需要时间。

**趋势二：提交粒度随时间递减**

- Phase 1-2：大量"巨型提交"，一次添加整个模块（+2000 行）
- Phase 3：中等粒度，一个协议特性一次提交（+300-500 行）
- Phase 5-7：细粒度，一次修一个 Bug 或加一个测试（+30-100 行）

这说明项目从"搭骨架"过渡到"精雕细琢"，也说明后期 AI 的工作更聚焦、更精确。

**趋势三：feat → fix → test → docs 的生命周期**

每个 Phase 都遵循相同的子模式：

```
Phase 初期：大量 feat 提交（新功能）
Phase 中期：fix 提交增加（修 Bug）
Phase 末期：test 和 docs 提交占主导（补测试、写文档）
```

这不是刻意安排的，而是 AI Coding 的自然节奏：AI 先"写完"功能，人类审查发现问题，AI 修复并补充测试。每个 Phase 末尾的 test/docs 密度，是该 Phase 成熟度的信号。

**趋势四：间隔天数递增**

```
Phase 1-3：几乎无间隔（Day 1→2→8→9→10→11→12→13→14→15，连续 10 天活跃）
Phase 4-5：开始出现 1-2 天间隔
Phase 6-7：间隔拉长到 4-5 天
```

前期 AI 可以连续工作不休息（只要有任务定义），后期间隔增大是因为——功能基本完成，主要在做质量打磨和细节调整，决策变得更谨慎。

**趋势五：版本号加速后减速**

```
v1.0 → v2.0  (1 天)     — 大版本跳跃，基础架构成型
v2.0 → v2.5  (4 天)     — 快速迭代，一天一个版本
v2.5 → v2.15 (10 天)     — 版本号加速，密集的小版本发布
v2.15 → v2.22 (9 天)     — 稍缓，新功能驱动版本号
v2.22 → v2.24.6 (14 天)  — 明显减速，Patch 版本为主
```

版本号的加速度与功能完成度负相关：功能越完善，版本号跳得越慢。v2.22 之后大量提交不升版本号，说明进入"维护模式"。

### 这个节奏告诉了我们什么

这 390 次提交的节奏，揭示了一个 AI Coding 项目的典型生命周期：

1. **爆发期（Phase 1-3）**：AI 的代码生成能力被充分利用，7 天完成 70% 的功能代码
2. **消化期（Phase 4）**：人类审查和系统测试跟上，节奏放缓
3. **增值期（Phase 5）**：基于稳定基础添加高级功能
4. **收敛期（Phase 6-7）**：质量攻坚和收尾

与传统开发最大的区别在于：**爆发期与消化期的时间比**。传统项目中，写代码和调试的时间比大约是 1:1。而在 AI Coding 项目中，生成代码的速度提高了 5-10 倍，但验证和调试的速度没有等比例提高——这导致消化期的相对成本上升，也是为什么 Harness Engineering（任务拆解、验证机制、验收标准）变得如此关键的原因。

**不是 AI 写代码太快，而是人类验证 AI 代码的速度太慢。Harness 的价值在于加速验证过程。**

---

## Part 10：总结——一页纸带走的东西

### 回到核心命题

> AI 是执行能力极强、但记忆极短、判断需要被约束的工程师。
> 你的工作是设计它的工作环境，而不是跟它聊天。

这套方法论的本质，不是发明了什么新东西。而是把软件工程里早已验证过的实践——需求文档、任务拆解、TDD、验收报告——迁移到 AI 协作的语境里，然后用 Harness Engineering 的框架把它们系统化。

工具变了。工程的基本规律没变。

但我们对"工程"的定义变了：**不只是写代码的工程，更是管理 AI 写代码的工程**。

### 五个控制点（浓缩版）

| # | 控制点 | 一句话 | 交付物 |
|---|--------|--------|--------|
| 1 | 上下文管理 | 把"不言而喻"写出来——AI 不知道的隐性知识比知道的更重要 | `AGENTS.md` 463 行 |
| 2 | 任务定义 | 先审 Spec 再实现——对齐成本远低于返工成本 | Spec 文档（含拒绝方案+量化标准） |
| 3 | 执行控制 | 把复杂任务拆成可验证的最小步骤——每步都有预期输出 | Plan 文档（File Map + Chunk + Step + Expected） |
| 4 | 验证机制 | "验证它确实失败"这一步不能跳——防止 AI 自欺欺人 | TDD 循环中红→绿的显式验证 |
| 5 | 交付验收 | 让数字说话，不让结论说话——可审计 > 可相信 | 结构化验收报告（测试数字 + 过程问题透明） |

### 三个最有冲击力的数字

| 数字 | 含义 |
|------|------|
| **44 天，133,000 行代码** | AI Coding 的爆发力——一个开发者 + AI ≈ 一个小团队的产出 |
| **review 改动量 50% → 15%** | AGENTS.md 重写前后的对比——上下文管理的杠杆率 |
| **7 小时 bug → 0 次复发** | 并发规范写入 AGENTS.md 后，同类型 bug 再未出现 |

### 回去第一件事做什么

如果只做一件事：**花 2 小时写（或重写）你的 AGENTS.md**。

不用完美。先回答这三个问题：
1. 你的项目里有哪些 AI 不读 100 个文件就不知道的隐性规则？
2. 你踩过的最痛的三个坑，AI 知道吗？
3. 你的 Fail-Open / Fail-Closed 边界在哪里？

写下来。这就是你的 Harness 起点。

---

## Part 11：展望——AI Coding 的下一个问题和未解之局

前面十个 Part 讲的是过去 44 天的经验。这一Part 讲的是**接下来会怎样，以及哪些问题还没解决**。

### 三个正在发生的趋势

#### 趋势一：从 Copilot 到 Driver——Agent 正在走向主驾

2024 年的 AI Coding 是 Copilot 模式：人写代码，AI 补全。人做决策，AI 执行。

2025 年开始转向 Driver 模式：AI 规划任务，AI 拆解步骤，AI 执行，AI 自验。人从"驾驶员"变成"质检员"——不再逐行写代码，而是审 Spec、审 Plan、审验收报告。

PairProxy 项目已经呈现了这个趋势：390 次提交中 75 次由 Claude Code 独立完成（无人类介入），Spec 的初稿由 AI 写、人类审，验收报告由 AI 生成、人类确认。

**但 Driver 模式有一个前提条件：Harness 必须到位。**

没有 Harness 的 Driver 模式不是"自动驾驶"，是"无人看管的失控"。AI 可以自己写代码，但它不能自己判断"这个架构决策对不对"、"这个降级策略合不合理"、"这个缓存设计安不安全"。这些判断必须预先编码到 Harness 里——也就是 AGENTS.md、Spec、Learnings 这套文档体系。

> **Copilot 需要你会开飞机。Driver 需要你会设计飞行手册。**
> 技能重心从"写代码"转向"设计 AI 的工作环境"。

#### 趋势二：知识检索的进化——从全文注入到精准召回

当前的 AGENTS.md 是一整份 463 行的文档，每次会话全量注入 AI 的上下文窗口。这在 10 万行以下的项目够用，但有两个瓶颈：

**瓶颈一：上下文窗口的物理限制。** AGENTS.md 463 行 + Spec 500 行 + Plan 800 行 + 相关源代码，一次会话轻松消耗 50K+ tokens。项目继续增长，文档体系也会膨胀，最终超出窗口容量。

**瓶颈二：全量注入的效率浪费。** AI 做数据库相关任务时，不需要读协议转换的设计决策。做前端页面时，不需要读并发测试规范。但当前模式下它必须读全部。

PairProxy 项目中已经有了"精准召回"的雏形——Sisyphus Agent 框架的 **Skills 机制**：

```
Skills = 按需加载的指令包

不是把所有知识塞进上下文，而是：
  1. AI 识别当前任务类型（前端 / 后端 / 测试 / 文档）
  2. 按类型加载对应的 Skill（frontend-ui-ux / git-master / review-work）
  3. Skill 里包含该领域的精确指令和模式
```

未来的方向是**分层检索 + 语义路由**：

```
当前模式：
  AGENTS.md（全量 463 行）→ AI 上下文

未来模式：
  任务描述 → 语义分类器 → 匹配最相关的 3-5 个知识片段 → 精准注入
              ├─ "修改 SSE 解析" → tap/ 模块规范 + token 计数规则
              ├─ "添加 REST API" → API 约定 + 分页规范 + JWT 鉴权
              └─ "写并发测试" → WaitGroup 模板 + Race 调试三步法
```

这会把 Harness 的效率提高一个数量级——AI 不再需要在 463 行里找相关内容，而是只看它需要的 50 行。

#### 趋势三：最佳实践的自动化——从文档到技能树

PairProxy 的 AGENTS.md 里有很多模式化的内容：Fail-Open 错误处理、WaitGroup 同步模板、Test Helper 模式、防回归 Checklist。这些内容每次都是 AI 从文档中读取后"手动"应用。

下一个演进是把这些模式固化成**可自动执行的技能**：

```
当前：AI 读文档 → 理解规则 → 在代码中应用（依赖 AI 的理解和执行力）

未来：规则被编码为 AST-grep 规则 / 代码生成模板 / Lint 自定义规则
      → AI 写完代码后自动检查 / 自动应用模式
      → 不再依赖 AI "记得"规则，而是工具强制执行
```

举一个具体例子——PairProxy 的"Fail-Open"原则：

```
当前（文档驱动）：
  AGENTS.md 里写："quota/database errors must NOT block requests"
  → AI 读到后，在写代码时主动遵循
  → 但如果 AI 忘了呢？靠 review 发现

未来（工具驱动）：
  golangci-lint 自定义规则：
  // 检测所有 quota/db 调用后的 error 处理
  // 如果返回 error 给调用者而非 log+bypass → 报 lint 错误
  → AI 忘了也无所谓，CI 会拦住
```

**这是从"告诉 AI 怎么做"到"让工具强制 AI 怎么做"的跨越。** AGENTS.md 不会消失，但它会从"AI 必须记住的所有规则"变成"工具无法自动检查的补充说明"。

---

### 一个未解之局：AI Coding 的依赖陷阱

> 📊 **可视化**：依赖陷阱五阶段曲线见 [dependency-trap.html](./dependency-trap.html)

上面讲的都是令人兴奋的趋势。现在讲一个令人不安的问题。

#### 陷阱描述

```
阶段一：起步
  开发者用 AI 写代码 → 速度快了 5-10 倍 → 信心大增

阶段二：加速
  项目快速膨胀（PairProxy: 44 天 13 万行）→ 开发者越来越依赖 AI
  → 逐行 review 变得不可能 → 开始信任 AI 的输出

阶段三：失控
  项目规模超出单人的完整理解范围
  → 开发者不再是"全知架构师"，变成"审查 AI 工作的质检员"
  → 但质检员自己看不懂所有细节，只能检查 AI 报告里的数字

阶段四：依赖锁定
  离开 AI 无法维护代码（因为不理解细节）
  → 只能继续用 AI 维护 → AI 修改 AI 写的代码
  → 开发者的角色进一步退化

阶段五：效率反转
  项目膨胀到 AI 的上下文窗口也无法覆盖全貌
  → AI 开始重复犯错（不知道之前踩过的坑）
  → AI 开始制造架构冲突（不知道某处已有类似设计）
  → Harness 的维护成本随项目规模非线性增长
  → 最终：AI 的开发效率从 +5x 回落到 +1.5x 甚至更低
```

这不是假设——PairProxy 项目在后期已经出现了阶段三和阶段四的迹象：

- 13 万行代码，没有一个人类开发者能逐行解释每一行
- 后期的 commit 大量是 AI 独立完成的，人类审的是验收报告而不是代码
- AGENTS.md 从 128 行膨胀到 463 行， Harness 的维护本身就是一项持续工作

#### 为什么这是一个真问题

传统软件开发也有"项目太大、单人无法掌控"的问题。但传统模式下，开发者至少写过每一行代码，对系统有肌肉记忆。

AI Coding 模式下，大量代码是 AI 生成的，开发者从未逐行读过。这意味着：

| | 传统开发 | AI Coding |
|---|---------|-----------|
| 代码理解 | 写过 → 理解 | 审过 → 部分理解 |
| Bug 定位 | 凭记忆定位 | 依赖 AI 搜索 |
| 架构演进 | 逐步理解后决策 | 基于 AGENTS.md 约束让 AI 决策 |
| 离线能力 | 可以脱离工具维护 | **离开 AI 几乎无法维护** |

**核心矛盾：AI Coding 让你更快地构建了一个你自己无法完全理解的系统。**

#### 有没有解法？

目前没有完美解法。但有几个方向：

**方向一：强制人类保持"驾驶能力"**

- 定期做"无 AI 编程日"——用手写代码的方式保持对核心模块的理解
- 关键模块（认证、协议转换、配额检查）必须人类逐行 review
- 架构决策永远由人类做出，AI 只能在约束内执行

**方向二：用更智能的检索对抗规模膨胀**

- 前面提到的"精准召回"——AI 不需要理解整个项目，只需要理解当前任务相关的部分
- 架构决策的索引化——不是 463 行的文档，而是一个可查询的知识图谱

**方向三：模块化隔离——限制 AI 的影响半径**

- 严格限制单个 AI 会话能修改的文件范围（Plan 的 File Map 机制）
- 每个 AI 会话只改一个模块，模块间通过接口契约耦合
- AI 不能跨模块重构——这需要人类的全局判断

**方向四：把 Harness 本身也自动化**

- AGENTS.md 的维护成本随项目增长——需要工具自动检测"哪些新经验需要写入"
- Learnings 的提取从"人工判断"变成"AI 自动分析 bug 模式并建议规则"
- 减少人类在 Harness 维护上的时间投入

**但坦率地说，这些方向都是缓解措施，不是根治。** AI Coding 的依赖性问题是结构性的——只要代码生成速度远超人类理解速度，这个差距就存在。

### 还有一个问题：Harness 的复杂度本身成为负担

PairProxy 的文档体系：AGENTS.md（463 行）+ Spec（~500 行/功能）+ Plan（~800 行/功能）+ Learnings（7 条）+ 验收报告（~1000 行/功能）。

一个功能的完整文档链大约 2800 行。项目 24 个版本，假设每个版本平均 2 个功能，就是 **134,400 行文档**。

实际没有这么多——很多功能没有完整的 Spec+Plan+验收链。但即使是部分覆盖，文档量也已经与代码量同一量级。

**Harness 的悖论**：Harness 越完善，AI 越可靠。但 Harness 本身的维护成本也在增长。最终可能出现"维护 Harness 的成本 > 维护代码的成本"。

这个问题目前没有解法。我在这里提出它，是因为：
1. 越早意识到这个问题的存在，越能在早期控制文档体系的膨胀
2. 自动化（方向四）是唯一可能的方向——但自动化本身也需要 Harness 来保证质量

### 我的判断

回到开场的核心命题，我需要修正一下：

> AI 是执行能力极强、但记忆极短、判断需要被约束的工程师。
> 你的工作是设计它的工作环境，而不是跟它聊天。

修正后：

> AI 是执行能力极强、但记忆极短、判断需要被约束的工程师。
> **你的工作是设计它的工作环境，同时保持自己理解这个环境的能力。**
> 一旦你不再理解 AI 在做什么，你就从"管理者"退化成了"旁观者"。

AI Coding 的下一个挑战，不是让 AI 更快，而是**让人类在 AI 越来越快的速度下，依然保持对系统的理解和控制**。

这可能是整个 AI Coding 领域最重要的问题。我没有完整答案。但 PairProxy 项目的实践至少验证了一件事：

> **好的 Harness 不能消除依赖，但可以把依赖控制在可审计的范围内。**
> 你不需要理解每一行代码，但你需要理解每一个决策。

---

*基于 PairProxy 项目（v1.0 → v2.24.6）的真实开发过程整理*
*代码规模：133,000 行（121,000 Go + 8,100 HTML + 3,700 配置/脚本）| 测试：2,090+ 个 | 版本：24 个 | 提交：390 次（75 次由 Claude Code 独立完成）*
