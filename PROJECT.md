# lead-mind-ai-agent 项目文档

> **每个 session 开始时必须先读取本文档。** 本文档是项目背景、架构决策与工程规范的唯一权威来源（source of truth）。当本文档与代码、旧 PR、旧对话记忆冲突时，以本文档为准；发现代码与本文档不一致，先判断是本文档过时还是代码写错了，再决定更新哪一边。

---

## 0. 项目一句话定位

**lead-mind-ai-agent** 是一个面向中小企业（SMB）的多租户 SaaS 产品，提供**通用任务型 AI Agent 平台**——租户/开发者可以自行配置工具（Tool）、编排多 Agent、接入自己的知识库，而不是绑定在单一垂直场景（如客服或销售）上。技术栈 **Go 1.25.6 + MySQL + Redis**，Agent 编排基于 [Eino](https://github.com/cloudwego/eino)（cloudwego）框架，设计原则参考仓库内 [enterprise-ai-agent-design.md](enterprise-ai-agent-design.md)。

---

## 1. 项目背景与目标客户

### 1.1 目标客户：中小企业（SMB）

- **获客方式**：自助注册（self-serve），按订阅套餐（Free/Pro/Team 等）收费，非销售驱动。
- **隔离级别**：**数据库级物理隔离**——每个租户拥有独立的 MySQL 数据库/schema。`X-Tenant-Code` 由上游网关/认证代理注入到请求头，在本服务的网关层读取并完成路由（决定请求落到哪个租户库），而非用于单库内的表级过滤。
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

贯穿：可观测性(OTel) · 审批网关(HITL) · 多租户隔离(网关层 X-Tenant-Code 路由至独立库) · 密钥管理
```

### 3.1 目录结构（初始骨架，随实现调整）

```
lead-mind-ai-agent/
├── PROJECT.md                    # 本文档：项目背景与规范，每 session 必读
├── enterprise-ai-agent-design.md # 架构设计参考文档（原始输入）
├── Dockerfile                    # 多阶段构建（golang:1.25.6-alpine → distroless/static-debian12:nonroot），已实现
├── .dockerignore
├── .github/
│   └── workflows/
│       └── ci.yml                # build/vet/gofmt/test + 镜像构建推送至 ghcr.io，已实现
├── cmd/
│   ├── server/                   # 主服务入口
│   └── worker/                   # 异步任务/长流程 worker
├── internal/
│   ├── gateway/                   # API 网关：路由、身份识别中间件、HTTP/SSE handler（已实现）
│   │   ├── middleware/            # WithIdentity（读 X-Tenant-Code/X-User-Id/X-Username/X-User-Roles header）、Logging
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
│   ├── identity/                 # 调用方身份上下文（context.go，已实现：TenantCode/UserID/Username/Roles）+ 未来的租户模型/配额/计费状态
│   ├── memory/                   # 会话记忆（短期 Redis / 长期 MySQL 事实摘要 / 不过期的原始对话转录 transcript）
│   ├── checkpoint/               # CheckPoint 存储实现（基于 MySQL 或 Redis）
│   ├── observability/            # Callback → OTel
│   ├── session/                  # 会话管理
│   └── guardrail/                # 输入输出安全护栏
├── pkg/
│   └── schema/                   # 跨模块类型定义
├── api/                          # proto / openapi 定义
├── configs/                      # 配置文件
├── deployments/                  # 已实现：configmap/secret(模板)/deployment/service，但整个目录已 gitignore（见下方决策记录），不在仓库里
├── migrations/                   # MySQL schema 迁移
└── evals/                        # 评估集与回归测试
```

---

## 4. 多租户设计原则（SMB SaaS 的核心约束）

1. **数据库级物理隔离**：每个租户拥有独立的 MySQL 数据库（或 schema）。租户/用户身份由上游网关或认证代理解析后，通过请求头注入本服务：`X-Tenant-Code`（租户标识，用于路由）、`X-User-Id`/`X-Username`/`X-User-Roles`（调用用户信息）。本服务的网关层（`internal/gateway/middleware.WithIdentity`）只读取这些 header 并挂到 context（`internal/identity`），不做认证；`X-Tenant-Code` 的职责是**路由**，不是表内过滤条件——业务代码不应假设同一张表里混着多个租户的数据。
2. **连接管理**：应用层需要一套按 `X-Tenant-Code`（即 `identity.Identity.TenantCode`）动态选择数据库连接（或连接池）的机制（如连接池注册表 + 路由中间件），新租户开通时要有明确的建库/建表（迁移）流程，不能依赖手工建库。
   - **租户库连接信息获取**：租户的 MySQL 连接信息（host/port/db name/credentials 等）不在本服务内维护，需通过集群内部调用 SSO 服务获取：`GET http://sso-service.default.svc.cluster.local/internal/tenants/:tenantCode/db-info`。该调用必须携带 `X-Internal-Token` 请求头，用于让 sso-service 识别这是可信的集群内部调用（区别于外部请求）。本服务侧应对获取结果做缓存（避免每次请求都打 sso-service），并在 token 校验失败/租户不存在时有明确的错误处理路径。
3. **共享层仍需按租户隔离**：Redis 等跨租户共享的基础设施，缓存 key 仍需以 `tenant:{tenant_code}:` 为前缀；日志/trace 仍需打 `tenant_code`、`user_id` 标签，用于排查问题时定位到具体租户库和操作者。
4. **配额与限流按租户维度**：模型调用次数/token 消耗、工具调用频率、Agent 并发数都要有按租户的限流（Redis 计数器），避免单租户耗尽共享资源（如模型 API 配额）影响其他租户。
5. **计费预留字段**：`tenants`（全局路由/元数据库中）表设计时预留套餐等级（`tier`）、计费周期、用量统计字段、对应租户库的连接信息，即使 MVP 阶段先手动开通，也要让数据模型未来能接自动化计费。

---

## 5. 落地路线图（分阶段，禁止一步到位）

沿用参考设计文档 §9 的渐进思路，结合本项目 SMB + 平台化定位调整：

**阶段一：单 Agent 可用（1~2 周）**
搭 ReAct Agent + 基础内置工具 + 一个主力模型接入（国内模型，暂不接兜底）。目标：核心链路走通，租户能创建一个 Agent、配几个工具、跑通对话+工具调用。

**阶段二：多租户与可观测性（1~2 周）**
补齐网关层 `X-Tenant-Code` 路由与租户库连接管理、租户配额限流、Callback → OTel trace/metrics。这一步优先级极高——没有多租户隔离就不能叫 SaaS，没有可观测性后续所有优化都是盲人摸象。

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
- 任何新增的 Redis 读写，Code Review 时必须检查 key 是否带 `tenant_code` 前缀（对标参考文档 §6 的"越权拦截"）。
- 高危工具（发邮件、转账、外呼、写外部系统）必须经过审批网关（参考设计文档 §4.1 的 Gate/CheckPoint/Notification/Resume 四环节），不允许绕过。

### 6.3 可观测性最低要求

- 每个 Agent 运行、每次模型调用、每次工具调用必须有 trace span，且带 `tenant_code`、`user_id`、`session_id` 标签。
- Token 消耗必须能按租户聚合统计（为未来计费和异常预警打基础）。

### 6.4 依赖引入原则

- 引入新的第三方依赖（尤其是向量库、消息队列等重基础设施）前，先确认是否有明确的当前需求驱动，禁止为"未来可能用到"提前引入。参考本文档 §2 关于向量库的决策记录。
- Eino 相关依赖升级前，检查官方 changelog 是否涉及 CheckPoint 序列化等破坏性变更。

### 6.5 命名与日志规范

- **命名可读性**：包名、函数名、变量名优先选择表达意图的完整词汇，不用无意义缩写（`tenantCode` 而非 `tc`，`connectionRegistry` 而非 `connReg`）；仅在算法/数学语境或极短作用域（如循环下标 `i`）下允许简写。
- **注释**：默认不写注释；仅在代码本身无法表达"为什么"时才加——隐藏的约束、非显而易见的不变量、对已知 bug 的绕过、容易让人意外的行为（呼应本文档整体的注释原则，见仓库级 CLAUDE.md）。不写"这段代码在做什么"这类描述性注释。**需要写注释时优先用中文**，与本仓库以中文交流、中文写决策记录的习惯保持一致；不要求回填已有的英文注释。
- **日志规范化**：
  - 所有日志必须结构化输出（非拼接字符串），字段至少包含时间、级别、模块/来源。
  - 涉及租户请求链路的日志（含错误日志），必须携带 `tenant_code`、`user_id`、`session_id`（若存在），与 §6.3 可观测性的 trace span 标签保持一致，便于按租户排查问题。
  - **错误日志**必须包含：完整的错误链（`fmt.Errorf("...: %w", err)` 包装后的错误信息，不能只打印顶层错误丢失上下文）、触发错误的关键入参摘要（脱敏后，如租户/会话标识，不记录密码/API Key/完整用户输入等敏感内容）、发生错误的模块/函数位置。禁止吞错误后不打日志（静默失败）。
  - 日志级别用途明确：`ERROR` 仅用于需要人工关注的异常（外部服务失败、数据不一致等），正常业务分支（如工具未命中、用户输入校验不通过）用 `WARN` 或 `INFO`，避免报警噪音。

### 6.6 时区规范

- **容器/部署时区固定为 `Asia/Shanghai`（+8:00）**：`Dockerfile` runtime stage 必须 `apk add tzdata` 并软链 `/etc/localtime` 到 `Asia/Shanghai`，同时设置 `ENV TZ=Asia/Shanghai` 作为双保险（部分运行时不读 `/etc/localtime`，会退回读 `TZ`）。新增的部署方式（K8s Deployment、docker-compose 等）如果绕开本仓库 Dockerfile 直接指定基础镜像，必须同样设置 `TZ=Asia/Shanghai`。
- **这只影响"人读"的时间展示（日志时间戳、`current_time` 工具的默认时区），不改变任何持久化时间语义**：所有落库/落 Redis 的时间戳字段必须继续显式使用 `time.Now().UTC()`（参考 `internal/memory/shortterm/store.go` 的 `last_active_at`），跨租户/跨服务比较时间时严禁依赖进程本地时区，只允许比较 UTC 时间戳或 `RFC3339` 里带时区偏移量的完整时间值。
- 代码里禁止出现裸的 `time.Now()` 后直接落库/落缓存而不做 `.UTC()` 的写法；Code Review 时按此检查。
- `internal/tools/builtin/current_time.go` 的 `current_time` 工具默认返回 UTC，调用方（模型）需要显式传 `timezone: "Asia/Shanghai"` 才会拿到北京时间——这是工具自身的输入契约，与本节的容器时区设置是两回事，不要混淆："容器时区"决定日志展示，"工具入参"决定返回给模型的时间字符串。

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
| 2026-07-21 | 补齐 `Dockerfile`（多阶段构建，`distroless/static-debian12:nonroot` 运行时）+ `.github/workflows/ci.yml`（build/vet/gofmt/test，镜像推送到 GHCR）+ `deployments/k8s/`（ConfigMap/Secret 模板/Deployment/Service） | 落地 §8 部署与运维；镜像仓库选 GitHub Container Registry（ghcr.io），用 `GITHUB_TOKEN` 免额外配置 secret；K8s YAML 只部署 server 本身，不含 MySQL/Redis（视为外部依赖，按 §4.2 连接管理机制接入）；Secret 只给模板（`MODEL_API_KEY` 等敏感值留空，运维通过 `kubectl create secret` 或外部 secret 管理工具单独注入，不写入仓库）——本地沙箱没有 Docker，Dockerfile 的 build stage 已用完全相同的 `CGO_ENABLED=0 GOOS=linux go build` 命令在本机验证过编译产物；K8s manifest 用 `kubectl apply --dry-run=client` 做过 schema 校验；`docker build`/`docker run` 的镜像本身未实际跑过，落地前建议在有 Docker 的环境跑一次 |
| 2026-07-21 | k8s manifest 去掉 Namespace 和 Ingress，只保留 ConfigMap/Secret/Deployment/Service，且都不再硬编码 `namespace:` 字段 | 用户明确不需要；不再假设特定命名空间或特定 ingress controller（nginx），部署时由调用方通过 `kubectl apply -n <ns>` 或所在 context 决定命名空间；对外暴露方式（Ingress/Gateway API/云厂商 LB 等）留给实际落地时按集群情况另定，不在这批 manifest 里预设 |
| 2026-07-21 | `deployments/` 整个目录改为 gitignore，从 git 追踪中移除（`git rm --cached`），文件仍保留在本地磁盘 | 用户本地会往 `secret.yaml`/`configmap.yaml` 填真实的 API Key、模型端点等值用于自己部署测试；这类文件一旦被 git 追踪就有随手 commit 泄露密钥的风险，索性整个目录不进仓库，比"记得每次留空"更可靠；确认过历史提交（`8d1e9bd`、`86b8f00`）里的 `secret.yaml` 只有空字符串占位，没有真实密钥被推送到过 `origin`，无需重写历史；后续如需在仓库里保留 k8s manifest 模板供团队共享，应采用不含真实值的模板文件 + 文档说明用法，而不是让本地调试用的实例文件进仓库 |
| 2026-07-21 | k8s Deployment/Service 名称改为 `ai-agent`（原 `server`），对外业务路由统一加 `/ai-agent` 前缀：`POST /ai-agent/v1/chat`、`GET /ai-agent/v1/chat/stream`（原 `/v1/chat`、`/v1/chat/stream`）；`/healthz` 保持不变，不加前缀 | k8s 资源命名与对外路由前缀对齐到 `ai-agent`，便于识别归属；`/healthz` 排除在外因为它是集群内部探活端点，不是对外业务接口，没必要跟着改；已用本地 mock OpenAI 兼容 server 验证新路径（同步 chat + SSE stream）均可用，旧路径 `/v1/...` 返回 404（路由已整体迁移，非双活）|
| 2026-07-21 | 放弃自定义 `tenant_id` header 设计，改用上游网关/认证代理已注入的身份 header：`X-Tenant-Code`（租户，必填，缺失返回 400）+ `X-User-Id`/`X-Username`/`X-User-Roles`（用户信息，选填，不做强制校验）。`internal/tenant` 包整体重命名为 `internal/identity`，`identity.Identity{TenantCode, UserID, Username, Roles}` 结构承载全部身份信息；`middleware.WithTenant` 重命名为 `middleware.WithIdentity`；日志/响应体里的 `tenant_id` 字段全部改名 `tenant_code`，并新增 `user_id` | 用户明确要求放弃自建 tenant_id 设计，统一用上游已注入的 `X-Tenant-Code`；用户信息一并读取是为了给后续审批网关（§4.1，需要知道"谁"在操作）、多租户配额限流等场景铺路，不用等到真正要用时再补 header 解析逻辑；User 相关 header 选填而非必填，是因为不排除有只带租户身份、不带具体用户身份的系统级调用场景；已用本地 mock 端到端验证：旧 `tenant_id` header 现在完全不被识别（缺 `X-Tenant-Code` 时仍返回 400），新四个 header 都能正确进 context 并体现在日志和响应体里 |
| 2026-07-23 | 明确租户 MySQL 连接信息的获取方式：不在本服务内静态配置，而是集群内部调用 sso-service（`GET http://sso-service.default.svc.cluster.local/internal/tenants/:tenantCode/db-info`），并携带 `X-Internal-Token` 标识内部调用身份 | 补充 §4.2 连接管理机制的缺失细节——此前只说"按 tenant_code 路由连接"，未明确连接信息从哪来；sso-service 是租户元数据的权威来源，本服务作为消费方通过内部 token 换取 db-info，避免每个服务各自维护一份租户库连接配置 |
| 2026-07-23 | 新增 §6.5 命名与日志规范：命名要求可读性优先（禁止无意义缩写）、注释仅在"为什么"不显而易见时才写；日志要求结构化输出，租户链路日志（含错误日志）必须带 `tenant_code`/`user_id`/`session_id`，错误日志必须保留完整错误链（`%w` 包装）+ 脱敏入参摘要 + 出错位置，禁止吞错误不打日志 | 用户明确提出编码规范要求；与 §6.1（错误处理用 `%w` 包装、禁止吞错误）、§6.3（trace span 标签）已有规范衔接一致，避免规范分散在多处口头约定，统一收敛进 PROJECT.md 便于团队成员和后续 session 遵循 |
| 2026-07-23 | 落地 Agent 记忆管理：`internal/tenantdb`（sso-service db-info 客户端 + 连接池 Registry，**空闲淘汰**而非进程生命周期长驻 + 极简 SQL 迁移执行器）、`internal/session`（session ID 生成/续接 + MySQL 会话元数据 Store，支持标题/置顶/归档/列表/删除）、`internal/memory/shortterm`（Redis 短期对话历史，`tenant:{tenant_code}:session:{id}:...` key 规范，6 小时活跃 TTL）、`internal/memory/compaction.go`（滑动窗口+摘要压缩，通过 Eino `react.AgentConfig.MessageRewriter` 钩子接入，同一份 `Compact` 函数供 handler 落盘复用避免漂移）、`internal/memory/longterm`（MySQL 用户偏好/session 摘要事实表）、`pkg/schema/message.go`（解耦 Message DTO，避免 `internal/memory` 直接依赖 `cloudwego/eino/schema`）；`chat.go`/`chat_stream.go` 接入历史读写与 session 登记，新增 `GET/PATCH/DELETE /ai-agent/v1/sessions...` 会话管理接口；新增直接依赖 `redis/go-redis/v9`、`go-sql-driver/mysql`，`google/uuid` 由间接依赖提升为直接依赖 | 落地 §5 阶段二前置的记忆管理功能性缺口（原 `chatRequest.SessionID` 字段存在但从未使用，服务此前完全无状态）；会话列表要求跨 Redis TTL 持久（标题/置顶/归档），故 session 元数据不能只放 Redis，必须落 MySQL，这牵出此前完全不存在的租户 DB 路由层，一并按"session 元数据 + 长期记忆刚好需要的最小版本"建设，不做通用连接池框架（§6.4）；连接池采用空闲淘汰（而非进程生命周期长驻）是因为长驻会让进程内存里累积所有曾访问过的租户的明文 DB 密码，一旦进程被攻破暴露面等于全部历史租户，空闲淘汰把暴露面收紧到最近活跃窗口；`tenant_code` 全程来自 `identity.FromContext(ctx)`，即每个请求自己的 header，不跨请求缓存，不存在用错租户上下文的风险；`go build`/`go vet`/`gofmt` 全绿，并为不依赖外部服务的纯逻辑部分（session ID 解析、`pkg/schema`↔`eino/schema` 互转、compaction 分轮/阈值/降级截断逻辑、sso-service 客户端的成功/404/401/500 分支、Registry 空闲淘汰的 map 增删）编写了单元测试且全部通过；沙箱环境没有 Docker/Redis/MySQL，**短期记忆 Redis 读写、session/长期记忆的 MySQL 集成、`MessageRewriter` 挂到真实 Eino Agent 后的端到端多轮对话未做真实环境验证**，落地前建议在有 Redis+MySQL 的环境补跑一次 |
| 2026-07-24 | §6.5 补充：代码注释默认不写，需要写时优先用中文 | 用户明确要求；不强制回填已有英文注释，仅约束新增/修改的注释 |
| 2026-07-24 | 新增 [TECH_DEBT.md](TECH_DEBT.md) 作为已知问题记录的固定位置，并在 §9 启动检查清单中加入查阅步骤；首条记录：旧会话超过 Redis 短期记忆 TTL 后继续对话，`chat.go`/`chat_stream.go` 的 `ShortTerm.LoadHistory` 拿到空历史，模型看不到之前的对话内容，但前端历史消息列表（走 `internal/memory/transcript`）仍能正常显示，造成"UI 有历史、模型失忆"的体验错位 | 排查"点开历史会话看不到对话"问题时发现的衍生问题：历史查看功能本身（transcript 读取路径）不受影响，但"继续旧会话对话"这个操作会因为 Redis TTL 过期而丢失上下文；用户明确要求先记录、不修复，避免和这次刚落地的 transcript 存档功能耦合在一起改，需要先明确重建上下文时是否要过 compaction、是否要续热 Redis 缓存等设计点，放到后续单独评估 |
| 2026-07-24 | 落地不过期的对话内容存档：新增 `internal/memory/transcript`（MySQL append-only store，`agent_conversation_turns` 表，`migrations/0002_create_conversation_transcript.sql`，按 `session_id` 索引，只 `INSERT`，从不 `UPDATE`/摘要/截断）；`chat.go`/`chat_stream.go` 在写完 `ShortTerm.ReplaceHistory`（compaction 后的短期记忆）之后，额外把这一轮**压缩前的原始** user+assistant 消息 append 进 transcript；新增 `GET /ai-agent/v1/sessions/{id}/messages`（`session.go` 的 `GetSessionMessages`），复用既有 `ownsSession` 权限检查，返回某个 session 的完整历史消息（角色/内容/tool_calls/时间戳），供前端点开旧会话时拉取历史对话 | 用户反馈前端点开以前的会话看不到历史对话；排查发现 `GET /sessions` 从设计上就只返回元数据（title/pinned/archived等），从未有任何接口暴露对话内容，而唯一存有对话内容的 `internal/memory/shortterm`（Redis）只有 6 小时 TTL，过期后旧会话历史永久丢失、无法补救；用户明确要求"落地一份不过期的对话内容存储"，故新增独立于 compaction 逻辑之外的原始转录存档；写入点选在 compaction 之后仍存原始消息（而非存 `compacted` 结果），是因为 compaction 会把老 turns 摘要化甚至丢弃，若转录也存 compacted 后的内容，翻旧会话时看到的会是摘要而非真实原文，违背"存档"的本意；删除会话时**不**级联删除转录记录（用户明确选择"保留存档，不级联删除"），转录只按 `session_id` 关联，`agent_sessions` 记录被删后 `ownsSession` 会 404，等于该会话的转录内容不再可通过 API 访问（仅留作后台审计/合规用途，符合预期，非 bug）；`go build`/`go vet`/`go test ./...` 全绿，沙箱无 MySQL，新表结构与写入/查询 SQL 未做真实数据库集成验证，落地前建议在有 MySQL 的环境跑一次 |
| 2026-07-23 | 补齐错误日志覆盖：新增 `internal/gateway/middleware/recover.go`（panic 恢复中间件，带栈追踪 + tenant/user 标签，替代 Go 默认的裸栈输出到 stderr）；新增 `internal/gateway/handler/httperror.go`（`httpError` helper，写 HTTP 错误响应的同时用 `slog` 记录底层 `error` 值，5xx 记 `ERROR`、4xx 记 `WARN`）并接入 `chat.go`/`chat_stream.go`/`session.go` 里此前直接 `http.Error(...)` 丢弃 `err` 的全部调用点；`chat_stream.go` 里 SSE 已开始后（headers 已 200）才发生的错误单独加 `slog.Error`（`event: error` 帧不会被外层请求日志记录为失败，因为状态码已提交为 200）；`compaction.go` 里摘要失败降级截断的分支补上 `slog.WarnContext`（原代码注释声称"调用方可以从日志排查"但实际从未打印）；`router.go` 里 `Logging`/`WithIdentity` 顺序调整为 `Recover(Logging(WithIdentity(...)))`，使缺 `X-Tenant-Code` 的 400 也能进请求日志；`Logging`/`Recover` 均直接从 request header 读租户/用户信息而非从 context 读，避开了"外层中间件读不到内层写入 context 的值"这个 `http.Request` context 不会向外传播的陷阱 | 用户反馈"日志不完善，很多报错日志都没有记录"；审计发现全仓库当时唯一的日志点是 `middleware.Logging` 的请求后摘要（只有 status/duration，没有 error 值）和 `main.go` 里的启动期错误，其余全部 `if err != nil { http.Error(...) }` 调用点（`chat.go`/`chat_stream.go`/`session.go` 约 20 处）和一次无 panic-recovery 中间件的裸奔状态；落地方式与刚新增的 §6.5 日志规范（结构化、带 tenant_code/user_id/session_id、保留错误链、ERROR 仅用于需要人工关注的异常）保持一致；`go build`/`go vet`/`gofmt`/`go test ./...` 全绿，并用一次性 smoke test（`httptest` 构造缺 header 请求 + panic handler）验证了缺 `X-Tenant-Code` 请求产生 `WARN` 级请求日志、下游 panic 被 `Recover` 捕获并输出带 `tenant_code`/`user_id`/栈追踪的 `ERROR` 日志，测试代码未保留在仓库里 |
| 2026-07-27 | 修复 TECH_DEBT.md 原第 1 条「旧会话超过 Redis TTL 后继续对话，模型看不到之前的历史」：新增 `internal/gateway/handler/history.go`，把 `chat.go`/`chat_stream.go` 里原先直接调用 `ShortTerm.LoadHistory` 的地方统一收敛到 `AgentDeps.loadHistory(ctx, tenantCode, userID, sessionID, isNewSession)`；当 `!isNewSession && len(history) == 0` 时判定为 Redis 副本已过期，回退读 `internal/memory/transcript.ListTurns` 重建上下文，经 `memory.Compact` 压缩后喂给模型，并写回 `ShortTerm.ReplaceHistory` 续热缓存。三个设计点的取舍：**(1) 重建结果必须过 `Compact`**——transcript 是不压缩、不截断的 append-only 存档，条数无上界，整段喂给模型会打爆上下文窗口；**(2) 续热 Redis**——重建结果顺手写回短期存储，同一会话的后续轮次不必反复读 MySQL，且写回的是压缩后的结果，与正常轮次落盘的内容形态一致；**(3) 回退路径的失败一律降级而非 500**——transcript 读失败、Redis 回写失败都只记 `WARN` 后继续，用户在旧会话里拿到一个缺上下文的回复也好过拿到错误页，这与 `ShortTerm.LoadHistory` 本身失败仍直接返回 500 是有意的区别（后者是基础设施异常，前者是可预期的 TTL 过期兜底）。实现中额外发现并堵上一个隐患：`Compact` 在摘要模型不可用时降级为硬截断，若 `CompactionConfig` 阈值配置过小（极端如零值配置），截断后可能一条消息都不剩，等于回退路径退化成它本要修复的"模型失忆"——故在 `compacted` 为空但 transcript 非空时退回未压缩的原始消息，并记 `WARN`；`go build`/`go vet`/`gofmt`/`go test ./...` 全绿，新增 `history_test.go` 用 fake store（不依赖 Redis/MySQL）表驱动覆盖了短期命中不读 transcript、新会话不读 transcript、TTL 过期重建并续热、旧会话 transcript 确实为空、transcript 失败降级、续热失败仍返回上下文、短期存储失败硬报错、重建结果确实被压缩、以及压缩清空后的兜底共 9 个分支；沙箱无 Redis/MySQL，**真实环境下"TTL 真正过期后继续旧会话"的端到端链路未做集成验证**，落地前建议在有 Redis+MySQL 的环境跑一次（可把 `SHORTTERM_SESSION_TTL_SECONDS` 调至几十秒复现） |
| 2026-07-23 | 新增 §6.6 时区规范：容器/部署时区固定 `Asia/Shanghai`（+8:00），`Dockerfile` runtime stage 软链 `/etc/localtime` + `ENV TZ=Asia/Shanghai` 双保险；明确这只影响日志时间戳等"人读"展示，不改变任何持久化时间语义——落库/落 Redis 的时间戳字段必须继续显式 `.UTC()` | 用户要求部署 Dockerfile 明确指定 +8:00 时区并定下规范；容器默认走 UTC，日志时间戳与运维本地时间对不上，排查问题时容易产生时间困惑；本条规范同时划清边界，避免"设了容器时区"被误解为"以后存储时间可以不用 UTC 了"，与 `internal/memory/shortterm/store.go` 已有的 `time.Now().UTC()` 写法保持一致 |
| 2026-07-28 | 按 `lead-mind-ai-plugin/docs/API-契约.md` 落地 HTTP 轮询式插件 Agent API：`/ai-agent/api/plugin/workflows`、任务创建、指令轮询、结果/观察上传及任务控制；与本服务其他业务接口一致，全部统一挂在 `/ai-agent` 路由前缀下。协议固定为 `browser-agent/v1`，任务内只保留一条未完成指令，结果按 `(taskId, commandId)` 幂等接收。云端实现跨 HTTP 请求持续运行的异步 ReAct 循环：每一步（包括第一步）都由共享 ChatModel 通过强制 `send_browser_action` 工具调用决定，历史模型动作以 assistant tool call 保存，插件执行结果以对应 tool result 保存，当前页面 HTML/DOM 作为下一轮观察输入；模型输出还须经过服务端白名单二次校验。任务快照存 Redis（key 带 `tenant:{tenant_code}:` 前缀，默认 TTL 24 小时），而非进程内存 | 当前 Chrome 插件只负责安全执行结构化指令和采集页面，任务理解与下一步决策必须完全留在云端 Agent；HTTP 轮询把一次 ReAct 工具调用拆成多个请求，所以服务端必须持久化动作历史、工具结果和各页面最新 observation，不能退化成只看最后一份 HTML 的无状态单步 Planner。Redis 持久化同时避免多副本部署时任务只存在于某一进程，租户码进入 key 和快照归属校验以防跨租户领取指令。插件的 Bearer Token 仍由上游认证代理校验并注入 `X-Tenant-Code`/`X-User-Id`，本服务不重复实现 OAuth Token 校验 |
| 2026-07-28 | 插件任务的继续/终止状态改由后端作为唯一权威源：指令轮询响应新增 `status`、`continue` 和完成时的 `resultSummary`；后端 Agent 生成 `COMPLETE` 判定时立即把 Redis 任务状态落为 `COMPLETED`，不再等待插件执行并回传结果后才终止。迁移期间仍把同一 `COMPLETE` 作为旧版插件可幂等领取的终止通知，插件确认后清除待确认命令，但该结果不再驱动状态迁移；`PAUSE`/`RESUME` 后的轮询同样分别由后端返回 `continue=false/true` | 原协议虽由后端决定下一步动作，但插件本地状态机仍负责收到 `COMPLETE` 后先结束、以及决定是否继续轮询，导致前后端可能短暂分叉，也让丢失终止响应时语义不清。后端已持久化完整任务快照，应由它统一裁决运行、暂停和终态；保留一次兼容通知可让尚未升级的插件继续正常停止，同时新插件可直接以服务端状态为准 |
| 2026-07-28 | Google 获客工作流改为结构化累计客户并由后端按目标数自动终止：页面分析结果包含企业名、官网、已观察来源页、相关性证据和明确公开联系方式，后端只接收来源页确实存在于任务 observation 的结果，按官网域名去重并持久化到 Redis 任务快照；轮询响应增加 `leads`、`collectedCount`、`targetCount`，达到 `resultLimit` 时后端覆盖下一动作成 `COMPLETE` 并立即进入 `COMPLETED` | 之前客户只存在于模型自由文本 memory/最终 summary，后端无法可靠计数，插件的“发现客户”一直显示 0，也无法确定何时真正达到目标。结构化结果让计数、去重、展示和停止条件可验证；来源页校验避免模型把未访问、未核实的搜索猜测计入结果，后端自动终止则避免依赖模型记住并正确执行数量边界 |
| 2026-07-28 | 浏览器获客 Agent 拆成主 Agent + 页面分析从 Agent：主 Agent 只负责任务规划、浏览动作、累计数量和流程停止，通过服务端 `analyze_page` 工具调用只读从 Agent；从 Agent 只接收指定的已观察页面，页面同时满足业务相关性且存在明确公开联系方式时才调用 `report_page_contact` 返回一个结构化客户，否则不调用结果工具。主 Agent 收到空结果后继续下一个流程；后端再次强制校验联系方式非空、来源 observation 存在后才计数 | 单 Agent 同时浏览、分析、抽取和计数时职责混杂，容易只把客户写进 memory、漏计数或在没有联系方式时产生低质量结果。把页面分析收敛成主 Agent 的内部工具后，Chrome 插件仍只是执行器，跨 HTTP 的主流程历史保持不变，而联系方式提取具有独立提示词、结构化出口和“无结果即不计入”的明确边界 |

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
3. 如果任务涉及的模块在 [TECH_DEBT.md](TECH_DEBT.md) 中有记录，先了解已知问题的现状和触发条件，避免重复排查或与已知限制混淆。
4. 如果任务涉及具体 Eino 代码模式，参考 [enterprise-ai-agent-design.md](enterprise-ai-agent-design.md) 对应章节。
5. 做出新的架构方向决策后，更新 §7 决策记录；发现新的已知问题但不修复时，更新 TECH_DEBT.md；修复了 TECH_DEBT.md 中的问题后，从中移除对应条目并把修复过程记录进 §7。
