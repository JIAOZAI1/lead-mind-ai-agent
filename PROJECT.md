# lead-mind-ai-agent 项目文档

> **每个 session 开始时必须先读取本文档。** 本文档是项目背景、架构决策与工程规范的唯一权威来源（source of truth）。当本文档与代码、旧 PR、旧对话记忆冲突时，以本文档为准；发现代码与本文档不一致，先判断是本文档过时还是代码写错了，再决定更新哪一边。

---

## 0. 项目一句话定位

**lead-mind-ai-agent** 是一个面向中小企业（SMB）的多租户 SaaS 产品，提供**通用任务型 AI Agent 平台**——租户/开发者可以自行配置工具（Tool）、编排多 Agent、接入自己的知识库，而不是绑定在单一垂直场景（如客服或销售）上。技术栈 **Go 1.25.6 + MySQL + Redis**，Agent 编排基于 [Eino](https://github.com/cloudwego/eino)（cloudwego）框架，设计原则参考仓库内 [enterprise-ai-agent-design.md](enterprise-ai-agent-design.md)。

---

## 1. 项目背景与目标客户

### 1.1 目标客户：中小企业（SMB）

- **获客方式**：自助注册（self-serve），按订阅套餐（Free/Pro/Team 等）收费，非销售驱动。
- **隔离级别**：**数据库级物理隔离**——每个租户拥有独立的 MySQL 数据库/schema。`tenant_id` 在网关层携带并完成路由（决定请求落到哪个租户库），而非用于单库内的表级过滤。
- **成本敏感**：SMB 对单价敏感，模型调用成本、基础设施成本必须可控——这直接影响第 3 节的模型供应商策略和第 6 节的多租户配额设计。
- **不需要**：SSO/SAML、私有化部署、专属 VPC——这些是 Enterprise 客户的需求，MVP 阶段不做，但数据模型要为未来"租户级别升级"留口子（例如 `tenants` 表预留 `tier` 字段）。

### 1.2 MVP 核心场景：通用任务型 Agent 平台

不锁定在单一垂直场景（如客服工单、销售 CRM），而是提供平台化能力：

- 租户/开发者可以**配置 Agent**：选择模型、编写 system prompt、挂载工具、挂载知识库。
- 提供**工具市场**雏形：内置工具（HTTP 请求、代码执行沙箱、搜索等）+ 自定义工具注册（webhook 形式）。
- 支持**单 Agent（ReAct）优先**，多 Agent 编排（Sequential/Parallel/Loop）作为高级能力，见 [enterprise-ai-agent-design.md §3.3](enterprise-ai-agent-design.md) 的判断标准："能用单 Agent 就别用多 Agent"。
- 因为是平台化、工具可配置，**高危操作和审批网关（4.1 节机制）从第一天就要设计**，不能等出事再补——租户自定义的工具随时可能包含"发邮件""调用外部 API""写数据库"这类需要人工确认的操作。

### 1.3 模型供应商策略：国内为主 + 海外兜底

- **主用模型**：国内模型（豆包 Doubao / 通义 Qwen 等），原因：面向国内 SMB 市场，需要合规、低延迟、成本可控。
- **兜底/降级链**：海外模型（如 Claude、OpenAI）作为 fallback，用于主模型不可用或能力不足的场景。
- **实现方式**：遵循 [enterprise-ai-agent-design.md §3.1](enterprise-ai-agent-design.md) 的 `FallbackModel` 模式——统一 `model.ChatModel` 接口，主模型失败按顺序尝试兜底模型，语义缓存命中直接返回。
- **不锁定具体 SDK 细节在本文档**：Eino 官方模型实现持续新增，接入哪些 provider 的具体 Go 包版本以 `go.mod` 为准，本文档只锁定"主用+兜底"这个策略方向。

---

## 2. 技术栈与版本锁定

| 层 | 选型 | 备注 |
|---|---|---|
| 语言 | Go 1.25.6+ | 使用 go.mod 锁定最低版本 |
| Agent 编排框架 | [cloudwego/eino](https://github.com/cloudwego/eino) + eino-ext | 版本以 go.mod 为准；Eino 迭代快，CheckPoint 序列化曾在 v0.3.26 有不兼容变更，**升级前必读官方 changelog** |
| 主存储 | MySQL 8.0+ | 租户数据、Agent 配置、会话元数据、审批记录、长期记忆 |
| 缓存/短期状态 | Redis 7+ | 短期会话记忆、语义缓存、限流计数、分布式锁 |
| 向量库 | 待定，MVP 阶段用 MySQL 或轻量方案顶上，不引入 Milvus/ES（见 §5） | 数据量小于百万级前不引入独立向量库 |
| 部署 | Docker + k8s（生产），docker-compose（本地开发） | server 无状态水平扩展，worker 处理长任务 |
| 可观测性 | OpenTelemetry（trace/metrics）+ 结构化日志 | 参考设计文档 §4.2 |

> MySQL 而非 PostgreSQL 是本项目相对参考设计文档的**明确偏离**——原文档建议 PostgreSQL/pgvector，但本项目技术栈锁定 MySQL。MVP 阶段暂不做向量检索，如未来需要 RAG，向量能力可选：MySQL 8.0+ 的 `VECTOR` 类型（如版本支持）、或外部轻量向量库、或转 pgvector（需重新评估）。**不要在没有明确需求前引入向量库依赖。**

---

## 3. 整体架构

沿用参考设计文档的分层思路，四层 + 贯穿治理面：

```
API 网关层 (HTTP/gRPC, SSE 流式, 鉴权, 限流, 租户路由)
        ↓
Agent 编排层 (ReAct Agent 优先, 多 Agent 为高级能力, Interrupt/Resume, CheckPoint)
        ↓
模型层 | 工具层 | 知识层(RAG，后置阶段)
        ↓
基础设施层 (MySQL · Redis · 对象存储 · MQ)

贯穿：可观测性(OTel) · 审批网关(HITL) · 多租户隔离(网关层 tenant_id 路由至独立库) · 密钥管理
```

### 3.1 目录结构（初始骨架，随实现调整）

```
lead-mind-ai-agent/
├── PROJECT.md                    # 本文档：项目背景与规范，每 session 必读
├── enterprise-ai-agent-design.md # 架构设计参考文档（原始输入）
├── cmd/
│   ├── server/                   # 主服务入口
│   └── worker/                   # 异步任务/长流程 worker
├── internal/
│   ├── gateway/                   # API 网关：路由、tenant_id 中间件、HTTP/SSE handler（已实现）
│   │   ├── middleware/            # WithTenant（读 tenant_id header）、Logging
│   │   ├── handler/                # health（占位）/ chat / chat_stream（已接入 ReAct Agent，见下）
│   │   └── router.go
│   ├── agent/
│   │   └── react/                 # ReAct Agent 工厂（agent.go，已实现，包装 eino flow/agent/react）
│   ├── model/                    # ChatModel 接入与治理（已实现 OpenAI 兼容 provider；fallback.go/cache.go 待阶段三）
│   │   ├── config.go              # 主模型配置（环境变量：MODEL_BASE_URL/MODEL_API_KEY/MODEL_NAME）
│   │   └── provider/
│   │       └── openai_compatible.go  # 任意 OpenAI 兼容端点的 ChatModel 工厂
│   ├── tools/
│   │   └── builtin/               # 内置工具（已实现 current_time；custom/approval 待补）
│   ├── rag/                      # 检索增强（后置阶段：indexer/, retriever/, rerank/）
│   ├── tenant/                   # 租户上下文（context.go，已实现）+ 未来的租户模型/配额/计费状态
│   ├── memory/                   # 会话记忆（短期 Redis / 长期 MySQL）
│   ├── checkpoint/               # CheckPoint 存储实现（基于 MySQL 或 Redis）
│   ├── observability/            # Callback → OTel
│   ├── session/                  # 会话管理
│   └── guardrail/                # 输入输出安全护栏
├── pkg/
│   └── schema/                   # 跨模块类型定义
├── api/                          # proto / openapi 定义
├── configs/                      # 配置文件
├── deployments/                  # k8s / docker
├── migrations/                   # MySQL schema 迁移
└── evals/                        # 评估集与回归测试
```

---

## 4. 多租户设计原则（SMB SaaS 的核心约束）

1. **数据库级物理隔离**：每个租户拥有独立的 MySQL 数据库（或 schema）。网关层在请求入口解析 `tenant_id`（如从鉴权 token、子域名或 header 解出），完成到具体租户库的路由/连接选择；`tenant_id` 的职责是**路由**，不是表内过滤条件——业务代码不应假设同一张表里混着多个租户的数据。
2. **连接管理**：应用层需要一套按 `tenant_id` 动态选择数据库连接（或连接池）的机制（如连接池注册表 + 路由中间件），新租户开通时要有明确的建库/建表（迁移）流程，不能依赖手工建库。
3. **共享层仍需 `tenant_id` 隔离**：Redis 等跨租户共享的基础设施，缓存 key 仍需以 `tenant:{tenant_id}:` 为前缀；日志/trace 仍需打 `tenant_id` 标签，用于排查问题时定位到具体租户库。
4. **配额与限流按租户维度**：模型调用次数/token 消耗、工具调用频率、Agent 并发数都要有按租户的限流（Redis 计数器），避免单租户耗尽共享资源（如模型 API 配额）影响其他租户。
5. **计费预留字段**：`tenants`（全局路由/元数据库中）表设计时预留套餐等级（`tier`）、计费周期、用量统计字段、对应租户库的连接信息，即使 MVP 阶段先手动开通，也要让数据模型未来能接自动化计费。

---

## 5. 落地路线图（分阶段，禁止一步到位）

沿用参考设计文档 §9 的渐进思路，结合本项目 SMB + 平台化定位调整：

**阶段一：单 Agent 可用（1~2 周）**
搭 ReAct Agent + 基础内置工具 + 一个主力模型接入（国内模型，暂不接兜底）。目标：核心链路走通，租户能创建一个 Agent、配几个工具、跑通对话+工具调用。

**阶段二：多租户与可观测性（1~2 周）**
补齐网关层 `tenant_id` 路由与租户库连接管理、租户配额限流、Callback → OTel trace/metrics。这一步优先级极高——没有多租户隔离就不能叫 SaaS，没有可观测性后续所有优化都是盲人摸象。

**阶段三：可控可恢复（2~3 周）**
接入 CheckPoint（MySQL 实现）、高危工具审批网关（HITL）、模型降级链（国内主用 + 海外兜底）。这是从"能跑的 Demo"到"能商用"的关键阶段。

**阶段四：知识增强（2~3 周，按需触发）**
如果客户场景验证出 RAG 是刚需，再接入检索增强、文档管理、召回评估。不提前引入向量库依赖。

**阶段五：加固（持续）**
安全护栏、计费系统对接、成本优化、评估集与回归体系。

---

## 6. 工程规范

### 6.1 Go 代码规范

- Go 1.25.6+ 特性可用，但不为用新特性而用——以代码清晰度优先。
- 错误处理：使用标准 `error` + `fmt.Errorf("...: %w", err)` 包装链，禁止吞掉错误（`_ = err`）除非有明确注释说明为何安全。
- 禁止在 internal 包之间产生循环依赖；`pkg/schema` 是唯一允许被所有层依赖的公共类型包。
- 所有对外部服务（模型 API、MQ、DB）的调用必须有超时控制（`context.WithTimeout`），不允许无超时的阻塞调用。

### 6.2 多租户安全红线

- 任何数据库连接的获取都必须经过统一的租户路由中间件/连接管理器，禁止业务代码绕过路由直连某个固定库或硬编码连接串，这是本项目最高优先级的安全红线（防止请求落到错误租户的库）。
- 任何新增的 Redis 读写，Code Review 时必须检查 key 是否带 `tenant_id` 前缀（对标参考文档 §6 的"越权拦截"）。
- 高危工具（发邮件、转账、外呼、写外部系统）必须经过审批网关（参考设计文档 §4.1 的 Gate/CheckPoint/Notification/Resume 四环节），不允许绕过。

### 6.3 可观测性最低要求

- 每个 Agent 运行、每次模型调用、每次工具调用必须有 trace span，且带 `tenant_id`、`session_id` 标签。
- Token 消耗必须能按租户聚合统计（为未来计费和异常预警打基础）。

### 6.4 依赖引入原则

- 引入新的第三方依赖（尤其是向量库、消息队列等重基础设施）前，先确认是否有明确的当前需求驱动，禁止为"未来可能用到"提前引入。参考本文档 §2 关于向量库的决策记录。
- Eino 相关依赖升级前，检查官方 changelog 是否涉及 CheckPoint 序列化等破坏性变更。

---

## 7. 决策记录（Decision Log）

记录偏离参考设计文档或后续变更的关键决策，避免未来重新踩坑或反复横跳。

| 日期 | 决策 | 原因 |
|---|---|---|
| 2026-07-21 | 存储用 MySQL 而非参考文档建议的 PostgreSQL | 项目技术栈明确锁定 Go+MySQL+Redis |
| 2026-07-21 | 定位为通用任务型 Agent 平台，不锁定垂直场景 | 面向 SMB 自助客户，需要灵活配置能力而非单一工作流 |
| 2026-07-21 | 模型策略：国内模型（豆包/通义等）主用，海外模型兜底 | 面向国内市场，合规与成本优先 |
| 2026-07-21 | MVP 阶段不引入独立向量库 | 数据量未到需要独立向量库的规模，避免过早引入基础设施复杂度 |
| 2026-07-21 | 多租户隔离改为**数据库级物理隔离**（独立库/schema），`tenant_id` 仅用于网关层路由，非表内过滤条件 | 明确当前已落地的隔离方式：网关已携带 tenant_id 做路由；更正此前"共享库+逻辑隔离"的错误假设 |
| 2026-07-21 | 网关层落地：`internal/gateway`（router + middleware + handler），用标准库 `net/http`（Go 1.22+ 的 `ServeMux` pattern routing）而非第三方路由框架；`tenant_id` 从 header 读取，暂无鉴权；chat/chat_stream handler 为占位实现 | 遵循 §6.4 依赖引入原则——MVP 网关路由需求简单，标准库够用，不为"可能需要更强路由能力"提前引入 chi/gin；Agent 编排层未就绪前先打通 HTTP/SSE 骨架和 tenant 路由链路 |
| 2026-07-21 | Go module 路径确定为 `github.com/JIAOZAI1/lead-mind-ai-agent`，与实际 GitHub 仓库一致 | 替换此前的占位路径 `github.com/leadmind/lead-mind-ai-agent`；已同步更新 go.mod 及所有内部包 import 路径 |
| 2026-07-21 | ReAct Agent 落地：`internal/agent/react`（包装 `eino/flow/agent/react`）+ `internal/model/provider`（OpenAI 兼容 ChatModel 工厂，经 `MODEL_BASE_URL`/`MODEL_API_KEY`/`MODEL_NAME` 环境变量配置）+ `internal/tools/builtin`（起步工具 `current_time`）；网关 `/v1/chat`、`/v1/chat/stream` 已接入真实 Agent 调用，替换此前占位实现 | 落地 §5 阶段一"单 Agent 可用"；`MaxStep` 固定默认 12（参考设计文档 §3.2 建议 8~15）；模型接入走 OpenAI 兼容协议而非绑定具体国内 SDK 包，实际切换豆包/通义时只需改环境变量指向对应兼容端点，代码不变；用本地 mock OpenAI 兼容 server 完整验证了 chat/stream 两条路径下的工具调用往返（模型请求工具→执行→结果回填→模型生成最终回复），未使用真实模型 API Key |
| 2026-07-21 | Agent 层暂不做主用+海外兜底的降级链（`FallbackModel`），单模型直连 | §1.3 兜底策略是阶段三工作项（配合 CheckPoint/审批网关一起做）；阶段一只验证核心链路，避免过早引入降级逻辑的复杂度和测试面 |

> 后续每次做出影响架构方向的决策（例如：是否上多 Agent、是否切换模型供应商权重、是否引入向量库），都在此表追加一行，写清楚"是什么"和"为什么"。

---

## 8. 与参考设计文档的关系

[enterprise-ai-agent-design.md](enterprise-ai-agent-design.md) 是本项目的架构设计输入（通用企业级 AI Agent 落地方案），提供了 Eino 框架的具体代码模式（FallbackModel、ReAct Agent、审批网关、CheckPoint、Callback 可观测性等）。**本文档（PROJECT.md）是项目专属的裁剪与决策层**：

- 遇到具体代码实现模式（怎么写 FallbackModel、怎么写审批网关代码），去查参考设计文档对应章节。
- 遇到"这个项目该不该做 X"的方向性问题（要不要多 Agent、用什么存储、目标客户是谁），以本文档为准。
- 两份文档冲突时，本文档优先，因为它是针对 lead-mind-ai-agent 这个具体项目的裁剪结果。

---

## 9. Session 启动检查清单

每次新 session 开始处理本项目任务时：

1. 读取本文档（PROJECT.md），确认背景、当前阶段、工程规范。
2. 如果任务涉及架构方向决策，检查 §7 决策记录是否已有相关结论，避免重复讨论已定事项。
3. 如果任务涉及具体 Eino 代码模式，参考 [enterprise-ai-agent-design.md](enterprise-ai-agent-design.md) 对应章节。
4. 做出新的架构方向决策后，更新 §7 决策记录。
