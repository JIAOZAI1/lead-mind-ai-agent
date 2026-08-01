# lead-mind-ai-agent

lead-mind-ai-agent 是一个使用 Go 构建的 SaaS AI Agent 项目，面向多租户场景提供 Agent 执行、工作流编排、工具调用、上下文记忆和知识库检索能力。

项目统一采用 [CloudWeGo Eino](https://github.com/cloudwego/eino) 作为 Agent 与 LLM 应用框架，由 Eino 承担通用 Agent、模型组件、工具和编排能力，项目代码专注于 SaaS 业务规则、租户隔离、权限、配额与审计。

> 当前状态：项目处于初始化阶段，已经建立配置加载、HTTP 服务以及支持 SSE 的基础 Eino Agent 接口。

## 项目目标

- 提供可配置、可观测、可中断和可恢复的 Agent 运行时。
- 支持单 Agent、确定性工作流和多 Agent 协作。
- 统一接入不同模型供应商，同时避免供应商类型侵入业务代码。
- 提供受权限和审计约束的 Web Search、HTTP、Shell 等工具能力。
- 支持会话上下文、短期记忆、长期记忆和摘要压缩。
- 支持文档加载、切分、索引、向量化和检索等 RAG 流程。
- 在所有数据、缓存、检索和异步任务中保持严格的租户隔离。

## 技术栈

- Go 1.25.6
- [CloudWeGo Eino](https://www.cloudwego.io/docs/eino/)
- Eino ADK
- Eino Compose
- Eino Ext
- Gin：HTTP API 与中间件
- GORM：关系型数据库访问
- Viper：配置加载与环境变量绑定
- go-redis：Redis 客户端、缓存和分布式协调
- slog：结构化日志
- gRPC
- PostgreSQL / Redis / 向量存储
- Docker / Kubernetes

当前基础 Agent 使用 OpenAI 或 OpenAI 兼容模型服务，其他基础设施将在实现阶段根据实际需求确定。

## Eino 使用原则

- 单 Agent 优先使用 Eino ADK 的 `ChatModelAgent`、Runner、事件流、会话以及打断与恢复能力。
- 确定性流程使用 Eino Compose 的 Chain、Graph 或 Workflow。
- 多 Agent 协作使用 Eino ADK Agent Collaboration。
- 模型统一使用 Eino `ChatModel` 和 `eino-ext` 官方组件。
- 工具遵循 Eino Tool 接口，并通过 ToolsNode 或 ADK ToolsConfig 接入。
- Prompt 使用 Eino `ChatTemplate`。
- RAG 优先使用 Eino Loader、Document Transformer、Indexer、Embedding 和 Retriever。
- 日志、Tracing、指标和审计通过 Eino Callback 接入。
- 不重复实现 Eino 已经提供的 ReAct 循环、通用 LLM Client 或工作流引擎。
- 自定义能力采用最小适配层，并在架构文档中记录设计原因和边界。

## 架构

项目依赖保持单向：

```text
cmd
  ↓
transport / workflow
  ↓
agent
  ↓
llm / tool / memory / prompt / knowledge
  ↓
pkg/schema / pkg/errors
```

- `cmd` 只负责配置加载、依赖装配、启动和优雅关闭。
- `transport` 只处理 HTTP / gRPC 协议、参数校验、身份提取和响应映射。
- `workflow` 使用 Eino 组织确定性流程和多 Agent 协作。
- `agent` 负责 Eino Agent 装配以及项目特有的运行策略。
- `llm`、`tool`、`memory`、`prompt`、`knowledge` 提供独立能力，不形成循环依赖。
- `pkg` 只放置需要对其他 Go module 承诺兼容性的公共 API。

## 目录结构

```text
lead-mind-ai-agent/
├── cmd/
│   ├── agent/                 # CLI 入口
│   └── server/                # HTTP / gRPC 服务入口
├── internal/
│   ├── agent/                 # Eino ADK Agent 装配与运行策略
│   ├── workflow/              # Eino 工作流与多 Agent 编排
│   ├── llm/                   # ChatModel 配置与供应商适配
│   ├── tool/                  # Eino Tool 实现与安全策略
│   ├── memory/                # 会话与长期记忆适配
│   ├── prompt/                # ChatTemplate 与 System Prompt
│   ├── knowledge/             # RAG 加载、切分、索引和检索
│   ├── transport/             # HTTP / gRPC 对外接口
│   ├── config/                # 配置加载和校验
│   └── observability/         # Callback、日志、Tracing、指标和审计
├── pkg/
│   ├── schema/                # 稳定的公共数据结构
│   └── errors/                # 公共错误类型和错误码
├── configs/                   # 脱敏配置示例和 Prompt 配置
├── migrations/                # PostgreSQL / 向量库迁移
├── docs/                      # 架构与开发规范
├── deployments/               # Docker、Compose 和 Kubernetes
└── tests/
    ├── integration/           # 集成测试
    └── e2e/                   # 端到端测试
```

完整的文件规划和目录职责参见 [AGENTS.md](AGENTS.md)。

## SaaS 多租户约束

- 除公开数据外，所有 Agent、会话、工作流、记忆、知识库和审计数据都必须归属于明确租户。
- `tenantID` 必须来自可信身份上下文，不能接受客户端任意覆盖。
- 数据库查询、向量检索、缓存键、对象存储路径和异步消息必须包含租户边界。
- Shell、HTTP、搜索等工具必须在租户权限、配额和审计策略内运行。
- 跨租户管理能力必须使用独立权限检查并留下审计记录。
- 日志不得记录密钥、访问令牌、个人敏感信息或未脱敏业务数据。

## HTTP 路由约定

业务 HTTP 路由统一以 `/ai-agent` 开头。健康检查等基础设施接口不使用业务前缀，当前健康检查路径为 `/healthz`。

## 开发状态

当前仓库已经完成第一阶段基础能力：

- 已创建 Go module，并固定 Gin `v1.12.0`、Viper `v1.21.0`、Eino `v0.9.13` 与 Eino OpenAI 组件 `v0.1.13`。
- 已实现基于 Gin 的 HTTP 服务和 `GET /healthz` 健康检查接口。
- 已实现基于 Viper 的配置加载，支持 YAML 配置文件与环境变量覆盖。
- 已实现 SIGINT、SIGTERM 信号监听和 HTTP 服务优雅关闭。
- 已使用 Eino ADK `ChatModelAgent` 实现 OpenAI 兼容的无状态单轮 Agent，并提供 SSE 流式接口。
- 尚未实现 CLI、gRPC 服务、对话记忆或 Agent 工具。
- 尚未提供构建、测试和部署命令。

开始实现时应按以下顺序推进：

1. 接入首个受控工具和端到端事件流。
2. 增加租户上下文、权限、配额和审计。
3. 增加会话存储和多轮对话能力。
4. 按真实业务需求扩展工作流、记忆与知识库能力。

## HTTP 服务

HTTP 服务默认监听 `0.0.0.0:8080`。启动前必须通过环境变量注入 OpenAI API 密钥：

```bash
LEAD_MIND_OPENAI_API_KEY=your-api-key go run ./cmd/server
```

健康检查：

```bash
curl http://127.0.0.1:8080/healthz
```

成功响应：

```json
{"status":"ok"}
```

服务收到 `SIGINT` 或 `SIGTERM` 后，会在配置的关闭超时时间内等待现有请求完成。

### Agent SSE 接口

`POST /ai-agent/chat` 接收一次无状态的单轮对话，并使用 SSE 返回模型生成的文本片段：

```bash
curl -N http://127.0.0.1:8080/ai-agent/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"请介绍一下你自己"}'
```

正常情况下依次返回 `message` 和 `done` 事件：

```text
event:message
data:{"content":"你好"}

event:done
data:{"finish_reason":"stop"}
```

模型生成期间发生错误时返回 `error` 事件，服务端日志记录完整错误，SSE 响应不会暴露模型供应商的内部错误。客户端断开连接会取消对应的 Agent 执行。

当前接口尚未接入身份认证、租户上下文、限流和对话记忆，只适合基础能力验证，不应直接作为生产环境的公开接口。为支持长连接，HTTP `write_timeout` 默认设置为 `0`，模型调用仍受 `openai.timeout` 限制。

## 配置

配置优先级从高到低为：环境变量、配置文件、内置默认值。复制示例文件后，服务会自动读取 `configs/config.yaml`：

```bash
cp configs/config.example.yaml configs/config.yaml
LEAD_MIND_OPENAI_API_KEY=your-api-key go run ./cmd/server
```

也可以使用 `LEAD_MIND_CONFIG_FILE` 指定其他 YAML 配置文件：

```bash
LEAD_MIND_CONFIG_FILE=/path/to/config.yaml \
LEAD_MIND_OPENAI_API_KEY=your-api-key \
go run ./cmd/server
```

环境变量使用 `LEAD_MIND_` 前缀，并将配置层级和下划线统一转换为大写下划线形式。例如：

```bash
LEAD_MIND_HTTP_HOST=127.0.0.1 \
LEAD_MIND_HTTP_PORT=9090 \
LEAD_MIND_APP_ENVIRONMENT=production \
LEAD_MIND_HTTP_MODE=release \
LEAD_MIND_OPENAI_API_KEY=your-api-key \
LEAD_MIND_OPENAI_MODEL=gpt-5.6-sol \
go run ./cmd/server
```

使用 OpenAI 兼容服务时，可通过 `LEAD_MIND_OPENAI_BASE_URL` 指定服务地址。API 密钥不得提交到仓库，也不得记录到日志。

可配置项参见 [configs/config.example.yaml](configs/config.example.yaml)。指定的配置文件不存在、格式错误、缺少 API 密钥或模型名称，以及端口和超时参数无效时，服务会拒绝启动。

## 开发规范

所有变更必须遵循：

- [项目约定](AGENTS.md)
- [Go 开发规范](docs/GO_STYLE_GUIDE.md)
- [Git 提交规范](docs/GIT_COMMIT_GUIDE.md)

Git 提交采用 Conventional Commits，`type` 和 `scope` 使用英文，标题和正文使用简体中文：

```text
feat(agent): 新增基础对话代理
fix(tool): 修复工具超时未取消的问题
docs: 补充 Eino 组件使用说明
```

## 文档

- [项目约定与完整目录设计](AGENTS.md)
- [Go 开发规范](docs/GO_STYLE_GUIDE.md)
- [Git 提交规范](docs/GIT_COMMIT_GUIDE.md)
- [Eino 官方文档](https://www.cloudwego.io/docs/eino/)
- [Eino GitHub 仓库](https://github.com/cloudwego/eino)
