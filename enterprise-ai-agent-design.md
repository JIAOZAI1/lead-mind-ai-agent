# 企业级 AI Agent 落地设计方案（Go + Eino）

> 目标读者：后端 / 平台工程团队
> 技术栈：Go 1.21+、Eino（cloudwego/eino）、Eino-Ext
> 版本说明：Eino 迭代较快，涉及 CheckPoint 序列化在 v0.3.26 有过一次不兼容性变更，新项目建议直接使用最新版本。落地前请以官方文档 https://www.cloudwego.io/zh/docs/eino/ 为准。

---

## 0. 设计原则

企业级和"能跑起来的 Demo"最大的差别不在模型调用，而在**围绕模型的工程治理**。本方案围绕四条主线展开：

1. **可控性**：所有可产生副作用的操作（下单、发消息、转账、调用外部 API）必须可拦截、可审批、可回滚。
2. **可观测**：每一次推理、每一次工具调用都有 trace、metrics、日志，能定位到 token 级别。
3. **可恢复**：长流程哪怕中断也不丢续跑，实例迁移线上不丢状态。
4. **可扩展**：模型、工具、知识库都是可替换组件，切换供应商不改一大堆业务代码。
5. **可回归**：Prompt 和 Agent 行为纳入评测集，改动前后可对比，避免"改一处崩一片"。

---

## 1. 整体架构

分层架构，纵向贯穿多层的治理面。

```
┌─────────────────────────────────────────────────────────────┐
│                        接入层 (API Gateway)                    │
│   HTTP/gRPC · SSE 流式 · 鉴权 · 限流 · 租户路由 · 幂等          │
└─────────────────────────────────────────────────────────────┘
                              │
┌─────────────────────────────────────────────────────────────┐
│                      Agent 编排层 (Orchestration)              │
│   ReAct Agent · Multi-Agent (Sequential/Parallel/Loop)         │
│   Graph/Workflow 编排 · Interrupt/Resume · CheckPoint          │
└─────────────────────────────────────────────────────────────┘
                              │
┌────────────────────┬───────────────────┬──────────────────────┐
│   模型层            │    工具层          │      知识层 (RAG)     │
│  ChatModel          │   Tool (Function)  │  Indexer / Retriever  │
│  多供应商适配        │   MCP 集成         │  Embedding / VectorStore  │
│  降级/重试/缓存      │   审批网关         │  文档处理/切片        │
└────────────────────┴───────────────────┴──────────────────────┘
                              │
┌─────────────────────────────────────────────────────────────┐
│                      基础设施层 (Infra)                        │
│  PostgreSQL · Redis · 向量库(Milvus/ES) · 对象存储 · MQ        │
└─────────────────────────────────────────────────────────────┘

        贯穿各层：可观测性 (Callback → OpenTelemetry/Langfuse)
                  配置中心 · 密钥管理 · 成本核算
```

**为什么选 Eino 做编排层**：Go 强类型带来编译期检查和整体的线上维护成本，Eino 的 Graph 编排能把 ReAct 这种"循环调用推进"的循环用类型化图表达；ADK 模块自带 ChatModelAgent、Sequential/Parallel/Loop 等 Agent 模式，框架层面天然支持流式处理、Callback 分发和 Interrupt/CheckPoint，这三点正好对应企业最关心的可观测、可控、可恢复。

---

## 2. 模块划分与目录结构

推荐一个务实的、可直接落地的工程骨架：

```
enterprise-agent/
├── cmd/
│   ├── server/              # 主服务入口
│   └── worker/              # 异步任务/长流程 worker
├── internal/
│   ├── agent/               # Agent 定义与编排
│   │   ├── react/           # ReAct Agent 封装
│   │   ├── multiagent/      # 多 Agent 协作
│   │   └── registry.go      # Agent 注册表
│   ├── model/               # ChatModel 适配与治理
│   │   ├── provider/        # openai / claude / ark / ollama 适配
│   │   ├── fallback.go      # 降级链
│   │   └── cache.go         # 语义缓存
│   ├── tools/                # 工具实现
│   │   ├── builtin/         # 内置工具
│   │   ├── mcp/             # MCP 客户端集成
│   │   └── approval/        # 审批网关
│   ├── rag/                 # 检索增强
│   │   ├── indexer/
│   │   ├── retriever/
│   │   └── rerank/
│   ├── memory/              # 会话记忆
│   ├── checkpoint/          # CheckPoint 存储实现
│   ├── observability/       # Callback → OTel/Langfuse
│   ├── session/             # 会话管理
│   └── guardrail/           # 输入输出安全护栏
├── pkg/
│   └── schema/               # 跨模块的类型定义
├── api/                     # proto / openapi 定义
├── configs/                 # 配置文件
├── deployments/             # k8s / docker
├── evals/                   # 评估集与回归测试
└── go.mod
```

---

## 3. 核心实现

### 3.1 模型层：多供应商 + 降级 + 缓存

企业不能把身家性命寄托于单一模型供应商。模型层要解决三件事：**统一接口、失败降级、成本控制**。

Eino 官方 ChatModel 实现已覆盖 OpenAI、Claude、Gemini、Ark（火山方舟）、Ollama 等，都实现了统一的 `model.ChatModel` 接口，所以切换供应商对上层是透明的。

```go
package model

import (
    "context"

    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/schema"
)

// FallbackModel 降级链：主模型失败时按顺序尝试备用模型
type FallbackModel struct {
    primary   model.BaseChatModel
    fallbacks []model.BaseChatModel
    cache     SemanticCache
}

func (m *FallbackModel) Generate(
    ctx context.Context,
    input []*schema.Message,
    opts ...model.Option,
) (*schema.Message, error) {
    // 1. 语义缓存命中直接返回，省 token
    if cached, ok := m.cache.Get(ctx, input); ok {
        return cached, nil
    }

    // 2. 主模型 → 备用模型依次尝试
    chain := append([]model.BaseChatModel{m.primary}, m.fallbacks...)
    var lastErr error
    for _, mdl := range chain {
        msg, err := mdl.Generate(ctx, input, opts...)
        if err == nil {
            m.cache.Set(ctx, input, msg)
            return msg, nil
        }
        lastErr = err
        // 记录降级事件（例如告警用）
        recordFallback(ctx, err)
    }
    return nil, lastErr
}
```

**要点**：
- 主模型用能力值强的（如 Claude / GPT 系列），降级模型用便宜且稳定的，保证"降级不停中断"。
- 语义缓存用 embedding 相似度匹配，对高频重复问题（客服场景常见）能显著省成本。注意设 TTL，避免返回过时信息。
- 重试要区分错误类型：限流（429）延长重试，参数错误（400）直接失败不重试。

### 3.2 Agent 层：ReAct 为核心

ReAct（Reasoning + Acting）是当前主流的单 Agent 模式：模型接收输入，自主判断是否调用工具，工具结果反馈回模型作为下一轮上下文，直到不再有 tool call 就输出最终结果。Eino 底层用 `compose.Graph` 编排这个循环。

```go
package react

import (
    "context"

    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/components/tool"
    "github.com/cloudwego/eino/flow/agent/react"
    "github.com/cloudwego/eino/schema"
)

func NewEnterpriseReactAgent(
    ctx context.Context,
    chatModel model.ToolCallingChatModel,
    tools []tool.BaseTool,
) (*react.Agent, error) {
    return react.NewAgent(ctx, &react.AgentConfig{
        ToolCallingModel: chatModel,
        ToolsConfig: compose.ToolsNodeConfig{
            Tools: tools,
        },
        // MaxStep 防止 Agent 陷入死循环，企业场景必设
        MaxStep: 12,
        // MessageModifier 在每次调模型前处理历史消息
        // 可用于注入系统提示、裁剪超长上下文、脱敏
        MessageModifier: func(ctx context.Context, input []*schema.Message) []*schema.Message {
            return prependSystemPrompt(trimContext(input))
        },
    })
}
```

**企业关键参数**：
- `MaxStep`：必须设置。防止模型在工具调用循环里无意义空转烧钱。根据业务复杂度设 8~15。
- `MessageModifier`：上下文工程的核心切入点。裁剪历史（控制 token）、注入 system prompt、敏感信息脱敏都在这里做。
- `ToolReturnDirectly`：某些工具（如"人工转接"）执行完就直接结果，不再回模型，用这个配置。

### 3.3 多 Agent 协作

单 Agent 解决不了的复杂场景，用 Eino ADK 的多 Agent 模式组合：

| 模式 | 适用场景 | 说明 |
|------|---------|------|
| **Sequential** | 分阶段处理：调研 → 总结 → 生成报告 | 前一个 Agent 输出作为后一个输入 |
| **Parallel** | 多角度并行：同时做技术/商业/安全分析 | 多 Agent 同时执行，结果汇总 |
| **Loop** | 自我迭代：写稿 → 评审 → 修订 → 再评审 | 反复执行直到满足退出条件 |
| **Host/Router** | 意图分发：客服总台路由到专门 Agent | 一个主 Agent 决定分发给哪个子 Agent |

设计建议：**能用单 Agent 就别用多 Agent**。多 Agent 会放大延迟和成本，且调试难度骤增。只有当职责边界确定、单 Agent 的 prompt 已经膨胀到难以维护时，才拆分。

---

## 4. 企业级治理四大支柱

这是 Demo 到生产系统的真正水岭。

### 4.1 可控性：审批网关（Human-in-the-Loop）

**问题**：当 Agent 开始调用真正有副作用的工具（下单、退款、发邮件、加数据），审批就不能是前端交互，而必须是运行时治理机制。

**Eino 的解法**：Interrupt / Resume + CheckPoint。在工具执行前中断，保存现场，等人工确认后从断点恢复。官方 `react_with_interrupt` 示例演示的正是订票场景：Agent 准备调用订票工具后暂停，用户确认信息无误（或修正参数）后再恢复执行。

一个可靠的审批闭环需要四个环节缺一不可：

1. **拦截网关（Gate）**：识别出被标记为"高危"的工具调用，路由到待审批状态而非直接执行。
2. **检查点（CheckPoint）**：保存 Agent 的完整推理状态，否则恢复时会丢失上下文。
3. **通知（Notification）**：把待审批事项推给正确的人（企业微信/飞书/工单）。
4. **恢复路径（Resume）**：审批通过正确重建工作流并继续。

丢掉任何一环都会得到一个破碎的模式：有网关无检查点 → 丢失推理；有检查点无通知 → 永远躺在队列；有通知无恢复路径 → Agent 永久卡死。

```go
// 高危工具用 wrapper 标记，执行前触发中断
type ApprovalGate struct {
    inner    tool.InvokableTool
    approver ApprovalService
    store    compose.CheckPointStore
}

func (g *ApprovalGate) InvokableRun(
    ctx context.Context,
    argumentsInJSON string,
    opts ...tool.Option,
) (string, error) {
    // 1. 创建审批单并中断
    ticketID := g.approver.CreateTicket(ctx, g.inner.Info(ctx).Name, argumentsInJSON)
    decision, err := g.approver.WaitDecision(ctx, ticketID)
    if err != nil {
        return "", err
    }

    switch decision.Result {
    case Approved:
        // 2. 用（可能被修改过的）参数执行
        return g.inner.InvokableRun(ctx, decision.FinalArguments, opts...)
    case Rejected:
        return fmt.Sprintf("操作被拒绝：%s", decision.Reason), nil
    default:
        return "", ErrApprovalTimeout
    }
}
```

**CheckPoint 存储注意事项**（来自官方文档的坑）：
- `CheckPointStore` 是一个 KV 接口（key string，value []byte），Eino 不提供默认实现，需要自己实现（用 Redis / PostgreSQL）。
- 用到自定义 struct 时，必须提前用 `RegisterSerializableType` 注册类型，否则反序列化失败。
- 流式数据保存 CheckPoint 时需要注册流的拼接方法。
- 恢复时要保证 Graph 编排结构完全一致，且 CallOptions 要完整重新传入。
- 子图要启用 Interrupt/CheckPoint，父图必须设置 CheckPointer，否则 Compile 报错。

### 4.2 可观测：Callback → OpenTelemetry / Langfuse

**问题**：线上 Agent 出问题，你需要知道——是哪一步的哪次模型调用，消耗了多少 token，工具返回了什么，为什么陷入了死循环。

**Eino 的解法**：Callback 分发机制。在固定切点（OnStart、OnEnd、OnError、OnStartWithStreamInput、OnEndWithStreamOutput）注入日志、追踪、指标，覆盖组件、图、Agent 三个层级。

```go
type OTelCallback struct {
    tracer trace.Tracer
    meter  metric.Meter
}

func (c *OTelCallback) OnStart(
    ctx context.Context,
    info *callbacks.RunInfo,
    input callbacks.CallbackInput,
) context.Context {
    ctx, span := c.tracer.Start(ctx, info.Name,
        trace.WithAttributes(
            attribute.String("component.type", info.Type),
            attribute.String("component.name", info.Name),
        ),
    )
    return context.WithValue(ctx, spanKey{}, span)
}

func (c *OTelCallback) OnEnd(
    ctx context.Context,
    info *callbacks.RunInfo,
    output callbacks.CallbackOutput,
) context.Context {
    if span, ok := ctx.Value(spanKey{}).(trace.Span); ok {
        // 记录 token 消耗到 metrics，用于成本核算
        if usage := extractTokenUsage(output); usage != nil {
            c.recordTokens(ctx, info.Name, usage)
        }
        span.End()
    }
    return ctx
}
```

**必须监控的指标**：

| 类别 | 指标 | 用途 |
|------|------|------|
| 成本 | 每请求/每租户 token 消耗、模型调用次数 | 成本核算、异常烧钱预警 |
| 性能 | 首 token 延迟(TTFT)、端到端延迟、工具调用耗时 | SLA 监控 |
| 质量 | 工具调用成功率、Agent 步数分布、降级率 | 发现模型/工具退化 |
| 可靠性 | 错误率、超时率、审批超时率 | 稳定性 |

生产上建议接 Langfuse（Eino 官方支持，专为 LLM 应用设计，能看到完整的调用树和 token 明细），或自建 OpenTelemetry + Grafana。

### 4.3 可恢复：断点续跑与实例迁移

除了审批场景，CheckPoint 还解决**长流程的可靠性**问题：
- **实例迁移**：实例要重启/缩容时，用 `WithGraphInterrupt` 主动触发中断，保存现场，新实例接管从断点恢复，避免长任务全部重跑（浪费大量时间）。
- **崩溃恢复**：worker 处理长流程时崩溃，重启后从最近 CheckPoint 继续，而非从头再跑（省钱和时间）。
- **异步长任务**：耗时大批量处理（如批量文档审核），放到 worker 异步执行，CheckPoint 记录进度。

官方 BatchNode 示例正是这个场景：审批组批量审核文档，每个文档走独立审核流程，高优先级文档需要人工审批才能完成——批处理 + 并发控制 + 中断恢复的组合。

---

## 5. 知识增强（RAG）

企业 Agent 几乎都需要接入私有知识（产品手册、内部制度、历史工单）。Eino 的 RAG 组件链：

```
文档 → Document(解析/过滤) → Embedding(向量化) → Indexer(存入向量库)
                                                        │
用户问题 → Embedding → Retriever(召回) → Rerank(重排) → 注入 Prompt → ChatModel
```

**组件职责**：
- **Document**：对接各家文档格式解析，把长文档按合适粒度的 chunk。
- **Indexer**：把文本和语义化索引存储（一般用 Embedding），供召回使用。
- **Retriever**：搜索索引内容召回（AI 应用中一般用 Embedding 做语义相似度召回）。
- **Rerank**：召回后用重排模型精排，提升 top-k 质量（召回讲究广度，重排讲究精度）。

**向量库选型**：
- **Milvus**：大规模、高性能，量级在千万级以上向量，Eino 有 SDK 支持。
- **Elasticsearch**：已有 ES 基础设施的团队，支持全文+向量混合检索，Eino 官方支持。
- **pgvector**：数据集不大（百万级以内）、想复用组件的团队，直接用 PostgreSQL 就行。

**RAG 落地经验**：
- 分片粒度是成败关键。太大导致噪声多，太小丢失上下文，一般 300~800 token 区间调优。
- 一定要做重排。纯向量召回的 top-5 往往有 1~2 个不相关，重排能显著提升生成质量。
- 给出处引用。企业场景可信度要求高，回答要能追溯到具体文档，避免幻觉。
- 定期评估召回质量。用真实问题构建评估集，监控 RAG 质量随知识库增长的变化。

---

## 6. 安全护栏（Guardrail）

企业场景对安全和合规的要求远高于个人应用：

**输入侧**：
- **Prompt 注入防护**：识别"忽略以上指令"类攻击，工具返回内容也要当作不可信数据处理，不能直接当指令执行。
- **敏感信息脱敏**：用户输入涉及身份证、手机号、银行卡在进模型前脱敏。
- **权限校验**：Agent 能调用哪些工具、能访问哪些数据，跟调用者身份和租户隔离。

**输出侧**：
- **内容审核**：模型输出违规审核（暴力、违法内容），尤其是 To C 场景。
- **越权拦截**：确保 Agent 不会返回超出当前用户权限的数据（多租户场景严防数据串租）。
- **幻觉抑制**：涉及事实性回答强制走 RAG 并要求引用，不允许模型自由发挥。

**工具侧**：
- **最小权限**：每个工具只有完成特定任务所需的最小权限。
- **参数校验**：工具执行前校验参数合法性（模型可能生成非法参数）。
- **审批分级**：只读工具直接放行，有副作用的高风险等级走不同审批流（见 4.1）。


---

## 7. 会话与记忆管理

企业 Agent 需要区分三种"记忆"，不要混为一谈：

| 类型 | 存储 | 生命周期 | 用途 |
|------|------|---------|------|
| **短期记忆** | Redis | 单会话（TTL 几小时） | 当前对话的历史消息 |
| **长期记忆** | PostgreSQL + 向量库 | 跨会话持久 | 用户偏好、历史事实沉淀 |
| **工作记忆** | Graph State | 单次 Agent 运行 | ReAct 循环中的中间状态 |

**上下文窗口管理**是记忆系统的核心难点。历史消息会无限增长，但模型上下文长度有限且越长越贵。策略：
- **滑动窗口**：只保留最近 N 轮，简单但会丢早期信息。
- **摘要压缩**：超早期对话用模型总结成要点，保留关键信息省 token。
- **向量召回**：把历史存向量库，按当前问题召回相关的段，避免超长对话。

生产上常组合使用：滑动窗 + 摘要摘要 + 相关历史召回。

---

## 8. 部署与运维

### 8.1 部署拓扑

```
                    ┌────────────────┐
                    │  Load Balancer │
                    └────────────────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌─────────┐  ┌─────────┐  ┌─────────┐
        │ server  │  │ server  │  │ server  │   无状态，水平扩展
        └─────────┘  └─────────┘  └─────────┘
              │            │            │
        ┌─────┴────────────┴────────────┴─────┐
        ▼                                      ▼
  ┌─────────┐                          ┌────────────────┐
  │ worker  │  长任务/批处理异步执行     │  共享存储层    │
  │ worker  │                          │ Redis/PG/向量库│
  └─────────┘                          └────────────────┘
```

- **server 无状态**：会话状态外置到 Redis，可自由水平扩展。
- **worker 处理长任务**：耗时任务（批处理、深度研究）从 server 拆离到 worker，通过 MQ 解耦，配合 CheckPoint 支持重启恢复。
- **优雅下线**：收到 SIGTERM 后停止收新请求，正在跑的流程触发 CheckPoint 保存，等待迁移或续跑。

### 8.2 上线检查清单

**性能与成本**
- [ ] 设置了 MaxStep，防止 Agent 死循环烧钱
- [ ] 模型调用有超时和重试边界
- [ ] 高频重复请求有语义缓存
- [ ] Token 消耗按租户/用户核算，有异常告警

**可靠性**
- [ ] 模型降级链配置并测试通过
- [ ] 长流程 CheckPoint 存储就绪（Redis/PG）
- [ ] 优雅下线逻辑验证（触发中断→保存→恢复）
- [ ] 自定义类型已 RegisterSerializableType 注册

**可观测性**
- [ ] Callback 接入 trace/metrics/日志
- [ ] 关键指标看板（延迟、成本、成功率、降级率）
- [ ] 告警规则（错误率、成本突增、降级率飙升）

**安全与合规**
- [ ] 输入 Prompt 注入防护
- [ ] 敏感信息脱敏
- [ ] 高危工具审批流上线
- [ ] 多租户数据隔离验证
- [ ] 输出内容审核（To C 必需）

**质量回归**
- [ ] 建立评估集（真实场景问题）
- [ ] Prompt/Agent 改动前后可对比
- [ ] RAG 召回质量有基线监控

---

## 9. 分阶段落地路线

不要一步到位，推荐渐进迭代：

**阶段一：能跑（1~2 周）**
搭起单 ReAct Agent + 基础工具 + 一个模型供应商。目标是核心业务链路走通，验证 Agent 能正确调用工具解决问题。

**阶段二：可观测（1 周）**
接入 Callback → trace/metrics，建立基础看板。这一步优先级很高——没有可观测性，后面所有优化都是盲人摸象。

**阶段三：可控可恢复（2~3 周）**
接入 CheckPoint、审批网关、模型降级链。这是从 Demo 迈向生产的关键阶段。

**阶段四：知识增强（2~3 周）**
接入 RAG，把私有知识接给 Agent。分片、召回、重排、评估集同步建立。

**阶段五：加固（持续）**
安全护栏、多租户隔离、成本优化、评估回归体系。这些是长期持续投入的方向。

---

## 10. 关键决策速查

| 决策点 | 推荐 | 理由 |
|--------|------|------|
| 编排框架 | Eino | Go 生态成熟且完整，强类型，原生流式/Callback/CheckPoint |
| 单 vs 多 Agent | 优先单 Agent | 多 Agent 放大延迟和成本及调试难度，非必要不上 |
| 模型策略 | 主用+降级链 | 避免单点依赖，降级保业务不中断 |
| 审批机制 | Interrupt+CheckPoint | 有副作用操作必须可拦截可恢复 |
| 可观测 | Langfuse 或 OTel | 前者开箱即用，后者与现有监控体系一体 |
| 向量库 | 数据规模定：pgvector→ES→Milvus | 无脑上量级，别一上来就上重组件 |
| 长流程 | worker + CheckPoint | 与 server 解耦，支持崩溃恢复 |

---

## 附录：核心依赖

```bash
go get github.com/cloudwego/eino@latest
go get github.com/cloudwego/eino-ext/components/model/openai@latest
# 按需引入其它组件实现（claude / ark / ollama / milvus / es 等）
```

参考文档：
- Eino 官方文档：https://www.cloudwego.io/zh/docs/eino/
- ReAct Agent 手册：https://www.cloudwego.io/zh/docs/eino/core_modules/flow_integration_components/react_agent_manual/
- Interrupt & CheckPoint：https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/checkpoint_interrupt/
- GitHub 主库：https://github.com/cloudwego/eino
- 官方示例：https://github.com/cloudwego/eino-examples

> ⚠️ Eino 迭代较快，CheckPoint 序列化在 v0.3.26 有兼容性变更。落地该版本前用于 checkpoint 的业务升级时需评估兼容性，涉及历史数据迁移和向后兼容。落地前请以官方最新文档为准，本方案中的 API 签名可能随版本演进。
