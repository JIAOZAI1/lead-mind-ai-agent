# Agent 记忆管理方案（短期 + 长期 + Session 生命周期）

## Context

`lead-mind-ai-agent` 目前处于纯净的"阶段一"状态：`internal/gateway/handler/chat.go`、`chat_stream.go` 每次请求都是全新构造 Agent，只传入单条 `schema.UserMessage`，完全无状态——`chatRequest.SessionID` 字段存在但从未被读取。`internal/memory/`、`internal/session/`、`internal/checkpoint/` 在 PROJECT.md §3.1 的目标目录树中都只是占位名字，代码库里一行实现都没有（已通过 Explore 确认：13 个 Go 文件，零 Redis/MySQL 依赖）。

用户明确要求：这次把短期记忆（单会话历史）+ 长期记忆（跨会话持久，如用户偏好/历史事实）+ session 生命周期一并设计，并直接实现核心代码（不是纯设计文档）。这是本项目走向"能用的多轮对话 SaaS 产品"的关键一步，也是 PROJECT.md §5 阶段二（多租户与可观测性）前置的功能性缺口。

需求澄清后确认：会话列表需要**完整会话管理**（标题、摘要、置顶、删除/归档），且要**跨短期记忆 TTL 持久**——即使 Redis 里的对话历史已过期，用户仍要能看到"我曾经有过这个会话"。这意味着 session 元数据本身不能只活在 Redis 里，必须落 MySQL，与长期记忆共享同一套"租户 DB 路由"前置设施——原方案里"session 是隐式的、无需持久记录"的假设不再成立，§1 和排期都相应调整。

关键架构事实（已读源码确认）：
- Eino 的 `react.Agent.Generate`/`.Stream` **无状态**——每次调用都要求调用方把完整历史通过 `[]*schema.Message` 传入；Eino 本身不维护 session/thread 概念。
- `react.AgentConfig` 有 `MessageModifier`（本仓库已用于注入 system prompt，每次调用时生效、不改变累积状态）和 `MessageRewriter`（改写累积状态本身，Eino 官方注释明确指出这是"压缩历史消息以适配上下文窗口"的扩展点）两个钩子，是本方案做上下文压缩的正确挂载点。
- `google/uuid` 已经是间接依赖（通过 eino 引入），可直接提升为直接依赖，零新增成本。
- 长期记忆落 MySQL 会牵出一个目前完全不存在的前置依赖：按 PROJECT.md §4.2，租户 MySQL 连接信息必须通过集群内部调用 `GET http://sso-service.default.svc.cluster.local/internal/tenants/:tenantCode/db-info`（带 `X-Internal-Token`）获取，不能硬编码或绕过路由（§6.2 最高优先级安全红线）。这层路由目前一行代码都没有，本方案必须把它作为"最小够用"的前置件一起建，而不是假设它已存在。

## 排期与阶段划分

由于会话列表需要跨 TTL 持久（标题/删除/归档），session 元数据不能只放 Redis——它和长期记忆一样，从一开始就依赖尚不存在的租户 DB 路由层。因此本次不再有"Redis-only、自成一体"的子集，租户 DB 路由层要提前到第 2 步就建，session 元数据存储、长期记忆事实存储共享同一个 `*sql.DB`（同一租户库的不同表），减少重复接线。

实现顺序：
1. 最小租户 DB 连接路由（前置设施，session 元数据和长期记忆都要用）
2. Session 生命周期与会话管理（Redis 短期历史 + MySQL 元数据/列表）
3. 短期记忆（Redis，对话历史本体）
4. 上下文窗口压缩（MessageRewriter）
5. 长期记忆（MySQL，用户偏好/事实摘要）
6. 接入 `agent.go` + 两个 handler + 新增会话管理 HTTP 接口（最后做，因为依赖前面所有接口稳定）

---

## 1. 最小租户 DB 连接路由（前置设施，session 元数据 + 长期记忆共用）

新增顶层包 `internal/tenantdb/`（不嵌套在 `memory`/`session` 下——这是 session 元数据和长期记忆都要用的横切关注点，但本次只做这两者刚好需要的最小版本，不做成通用连接池框架，避免违反 §6.4"禁止为未来可能用到而提前引入"）。

**`internal/tenantdb/ssoclient.go`**：
```go
package tenantdb

type DBInfo struct {
    Host, Database, Username, Password string
    Port int
}

type SSOClient struct{ /* baseURL, internalToken, httpClient */ }

func (c *SSOClient) FetchDBInfo(ctx context.Context, tenantCode string) (DBInfo, error)
```
调用 `GET {SSO_SERVICE_BASE_URL}/internal/tenants/{tenantCode}/db-info`，带 `X-Internal-Token: {SSO_INTERNAL_TOKEN}`（均为环境变量，base URL 默认 PROJECT.md 给出的集群内地址）。`context.WithTimeout` 包裹。非 2xx 映射为 `ErrTenantNotFound`/`ErrUnauthorized` 等哨兵错误。

**`internal/tenantdb/registry.go`**：
```go
package tenantdb

type Registry struct { /* client *SSOClient, cacheTTL, 内部 map[string]*poolEntry */ }

func NewRegistry(client *SSOClient, cacheTTL time.Duration) *Registry
func (r *Registry) Get(ctx context.Context, tenantCode string) (*sql.DB, error)
func (r *Registry) Close() error
```
`db-info` 查询结果缓存（默认 10 分钟，`TENANTDB_INFO_CACHE_TTL_SECONDS` 可调），`*sql.DB` 连接池一旦建立复用到进程生命周期结束（本版本的已知简化，注释里写明，不是 bug）。

**MySQL 驱动**：`github.com/go-sql-driver/mysql` + 标准库 `database/sql`，不引入 ORM——本次 SQL 表面只有两张表（session 元数据 + 长期记忆事实）、每张表两三类查询，手写 SQL 比引入 gorm 的抽象/依赖更符合 §6.4。

**迁移机制**：`internal/tenantdb/migrate.go` 提供极简 `ApplyMigrations(ctx, db, fsys) error`——扫 `.sql` 文件，记录到 `schema_migrations` 表，按文件名顺序执行未应用的。不引入 `golang-migrate` 等第三方迁移工具（同样是 §6.4 的克制）。

**§6.2 红线的机械化落实**：session 元数据 Store、长期记忆 Store 的构造函数都只接受 `*tenantdb.Registry`，不接受裸 `*sql.DB`——结构上没有绕过路由拿到连接的办法。

---

## 2. Session 生命周期与会话管理

**ID 生成规则不变**：客户端传了非空 `session_id` → 视为续接会话（不要求还存在——续接一个查无历史/查无元数据的 ID 就等效于新建）。未传 → 服务端生成 UUIDv4（`github.com/google/uuid`，已是间接依赖，提升为直接依赖）。

**范围升级**：因为会话列表要跨 Redis TTL 持久、要支持标题/置顶/删除归档，session 元数据本身要落 MySQL，`internal/session` 从"纯 ID 生成工具"升级为一个完整的会话管理包。

**包位置**：`internal/session/`（不再是单文件，拆成 `session.go` 生成逻辑 + `store.go` 元数据存储）。

**元数据模型与接口**（`internal/session/store.go`）：
```go
package session

type Session struct {
    ID         string
    UserID     string
    Title      string     // 用户可重命名；为空时用首轮消息截断生成默认标题
    Pinned     bool
    Archived   bool
    CreatedAt  time.Time
    LastActiveAt time.Time
}

type Store interface {
    // Create 在首次生成 session_id 时登记一条元数据记录。
    Create(ctx context.Context, tenantCode string, s Session) error

    // Touch 更新 last_active_at（每次该 session 有新一轮对话时调用，
    // 由 handler 在追加短期记忆的同时一并调用）。
    Touch(ctx context.Context, tenantCode, sessionID string) error

    // Rename 更新标题（用户显式重命名，或首轮后自动摘要生成默认标题）。
    Rename(ctx context.Context, tenantCode, sessionID, title string) error

    // SetPinned / SetArchived 置顶和归档，都是软状态切换，不物理删除。
    SetPinned(ctx context.Context, tenantCode, sessionID string, pinned bool) error
    SetArchived(ctx context.Context, tenantCode, sessionID string, archived bool) error

    // Delete 物理删除一条 session 的元数据记录（配合 shortterm.Store.Reset
    // 一起调用清掉 Redis 里可能还没过期的历史）。
    Delete(ctx context.Context, tenantCode, sessionID string) error

    // List 返回某用户的会话列表，默认按 pinned desc, last_active_at desc
    // 排序；includeArchived 控制是否包含已归档会话。
    List(ctx context.Context, tenantCode, userID string, includeArchived bool) ([]Session, error)
}
```

**DDL**（并入 `migrations/0001_create_agent_memory.sql`，与长期记忆事实表同一个迁移文件、同一租户库）：
```sql
CREATE TABLE IF NOT EXISTS agent_sessions (
    id              VARCHAR(64) PRIMARY KEY,
    user_id         VARCHAR(191) NOT NULL,
    title           VARCHAR(255) NOT NULL DEFAULT '',
    pinned          TINYINT(1) NOT NULL DEFAULT 0,
    archived        TINYINT(1) NOT NULL DEFAULT 0,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_active_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    KEY idx_user_list (user_id, archived, pinned, last_active_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

**标题生成**：首轮对话结束后，若 `Title` 为空，用当轮用户消息截断（如前 20 个字符 + "..."）生成一个廉价默认标题，不额外调用模型摘要——避免为一个次要 UX 细节多花一次模型调用；用户可随时通过 `Rename` 覆盖。若后续想要"AI 生成标题"，是自然的增量优化，本次不做（见 §7 排除范围）。

**与短期记忆的关系**：`agent_sessions` 表管的是"这个会话存在、叫什么、什么时候活跃过"，不存对话内容本身——内容仍然只在 Redis（`internal/memory/shortterm`）里，过期就没了。列表页返回的是元数据（标题、时间、置顶/归档状态），不是历史消息；用户点进某个已过期的旧会话，`LoadHistory` 返回空历史，等效于"这个会话现在没有可续接的上下文，但记录本身还在列表里"——这个语义已经跟用户确认过（列表跨 TTL 持久，但没要求对话内容本身跨 TTL 持久）。

---

## 3. 短期记忆（Redis）

**包位置**：`internal/memory/shortterm/`（`internal/memory` 下按 `shortterm`/`longterm` 分子包，避免两个 Store 接口命名冲突，也便于未来只需要长期记忆的消费方不必引入 Redis 依赖）。

**Redis 客户端选型**：`github.com/redis/go-redis/v9`——原生 context 支持（满足 §6.1 超时要求）、连接池、pipelining，是 Go 生态事实标准，无需引入 `redigo` 等替代品。这是本方案唯一新增的直接依赖之一。

**接口**（`internal/memory/shortterm/store.go`）：
```go
package shortterm

type Store interface {
    // LoadHistory 返回 (tenant, session) 已持久化的历史消息，按时间顺序；
    // 不存在时返回空切片而非 error（续接空会话是正常情况）。
    LoadHistory(ctx context.Context, tenantCode, sessionID string) ([]pkgschema.Message, error)

    // AppendTurns 追加新一轮消息并刷新 TTL。
    AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error

    // Reset 清空一个 session 的历史。
    Reset(ctx context.Context, tenantCode, sessionID string) error

    // ReplaceHistory 整体覆盖历史，供压缩逻辑（§3）原子替换用，
    // 避免历史无界增长。
    ReplaceHistory(ctx context.Context, tenantCode, sessionID string, turns []pkgschema.Message) error
}
```

**Redis key 规范**（强制 `tenant:{tenant_code}:` 前缀，对应 PROJECT.md §4.3/§6.2）：
```
tenant:{tenant_code}:session:{session_id}:history   -- STRING，JSON 数组（[]pkgschema.Message）
tenant:{tenant_code}:session:{session_id}:meta       -- HASH：user_id, created_at, last_active_at, turn_count
```
历史存成单个 JSON 字符串而非 Redis LIST：因为压缩逻辑需要 `ReplaceHistory` 做原子整体替换，单 `SET` 比"DEL+多次RPUSH"事务简单；读取也是单次 `GET`+反序列化。由于 §3 的滑动窗口+摘要压缩会限制历史体积上限，这个 blob 不会无界增长。

**TTL 策略**：6 小时不活跃过期（`EX 21600`），每次写入（追加或替换）刷新；通过环境变量 `SHORTTERM_SESSION_TTL_SECONDS` 可调（默认 21600）。`history` 和 `meta` 两个 key 用 Lua/pipeline 保持 TTL 同步，避免其中一个先过期。

**存什么**：完整原始轮次消息（含 user/assistant/tool-call/tool-result），不是只存文本——因为 ReAct 下一轮需要正确配对的 tool_call_id，丢了会让模型困惑或违反某些 provider 的 API 约束。体积上限由 §3 的压缩机制兜底，不是这里手动裁剪。

---

## 4. 上下文窗口压缩（MessageRewriter 钩子）

**新文件**：`internal/memory/compaction.go`（package `memory`）。

```go
package memory

type CompactionConfig struct {
    MaxTurnsVerbatim        int                        // 保留最近 N 轮原文，默认 10
    SummarizeThresholdTurns int                         // 超过此轮数才触发摘要，默认 20
    SummarizerModel         model.ToolCallingChatModel  // 摘要用模型，可与主模型相同
}

// NewMessageRewriter 返回可直接赋给 react.AgentConfig.MessageRewriter 的函数。
func NewMessageRewriter(cfg CompactionConfig) func(ctx context.Context, messages []*schema.Message) []*schema.Message

// Compact 是不依赖 eino 类型的裁剪逻辑，供 MessageRewriter 闭包和
// handler 落盘前调用，确保 Redis 存储和本轮内存状态用同一套压缩结果，
// 不会互相漂移。
func Compact(ctx context.Context, cfg CompactionConfig, history []pkgschema.Message) []pkgschema.Message
```

**行为**：
1. 按"一轮 = 一条 user 消息 + 其后续 assistant/tool 消息"切分。
2. 轮数 ≤ `SummarizeThresholdTurns`：原样返回，不动。
3. 轮数 > 阈值：把 `MaxTurnsVerbatim` 之前的旧轮次拼成文本，调用 `SummarizerModel.Generate`（固定摘要 prompt，`context.WithTimeout` 15s，满足 §6.1 外部调用超时要求），生成摘要文本；用 `[SystemMessage(摘要) + 最近 MaxTurnsVerbatim 轮原文]` 替换整个历史。

**同步 or 异步**：**同步内联**。仓库目前没有 worker/MQ（`cmd/worker/` 是空占位），引入异步队列是明显的过度工程（违反 §6.4）。压缩触发时（约每 10 轮一次）多一次模型调用的延迟，在 MVP 规模可接受；等 阶段三 有了 worker+队列，可以把摘要挪到异步——代码里留注释说明这一点，但本次不做。

**挂载点**：`internal/agent/react/agent.go` 的 `Config` 新增 `MessageRewriter react.MessageModifier` 字段（复用 eino 的 `react.MessageModifier` 类型，不用重新定义——这个包本身就是包一层 eino react，直接用其类型是一致的），`New()` 里仿照现有 `SystemPrompt` 的写法赋给 `agentCfg.MessageRewriter`。`MessageRewriter` 在 eino 内部先于 `MessageModifier` 执行，不需要关心 system prompt 注入的顺序问题。

**关键实现细节（防漂移）**：Eino 的 `Generate`/`Stream` 不会把 rewriter 改写后的状态回传给调用方，所以 handler 落盘前不能"偷看"内部状态，而是要对同一份历史再跑一次同样的 `memory.Compact(...)`，保证 Redis 里存的和下一轮 `MessageRewriter` 即将处理的是同一套裁剪逻辑的产物。

---

## 5. 长期记忆（MySQL）

租户 DB 路由复用 §1 已建好的 `internal/tenantdb.Registry`，不重复建设。

**存什么**：不存完整原始对话（那是短期记忆的职责，且 PROJECT.md 明确推迟向量召回）。只存：
- **用户偏好**：如"回答偏简洁""主要语言粤语"。
- **durable 事实/摘要**：session 结束/压缩时产生的摘要，打上 `session_id` 标签持久化一份。

**怎么写入**：两条路径，都走同一个 `Store` 接口：
1. `NewMessageRewriter` 产生摘要时，顺手 upsert 一条 `kind=session_summary` 的记录（复用已经花的模型调用，不用二次调用）。
2. 未来可加一个 `remember` 类内置工具，让 Agent 主动调用来显式记住偏好——本次只把接口设计成同时支持两条路径，工具本身的实现不在这次文件清单内（明确排除，见 §7）。

**包位置**：`internal/memory/longterm/store.go`。

```go
package longterm

type FactKind string
const (
    FactKindPreference     FactKind = "preference"
    FactKindSessionSummary FactKind = "session_summary"
)

type Fact struct {
    ID                            int64
    UserID, Key, Value            string
    Kind                          FactKind
    SourceSessionID               string
    CreatedAt, UpdatedAt          time.Time
}

type Store interface {
    // UpsertFact：kind=preference 且 Key 非空时按 (user_id, kind, key) upsert；
    // kind=session_summary 时 Key 存 source_session_id，同样走 upsert 语义，
    // 保证"一个 session 一条摘要"。
    UpsertFact(ctx context.Context, tenantCode string, fact Fact) error

    // ListFacts 按 kind 过滤（传空取全部），用于新会话开始时注入偏好
    // （是否注入由 handler 决定，接口本身不强制）。
    ListFacts(ctx context.Context, tenantCode, userID string, kind FactKind) ([]Fact, error)
}
```

每个方法都显式传 `tenantCode`（即使底层 `*sql.DB` 已经是按租户路由好的）——这是自文档化的防御性设计，不是行级过滤列：隔离发生在"选哪个 DB"这一层（物理隔离），SQL 里不需要、也不应该出现 `tenant_code` 过滤条件。

**DDL**（并入 `migrations/0001_create_agent_memory.sql`，与 §2 的 `agent_sessions` 表同一个迁移文件，应用到每个租户库，不是全局路由库）：
```sql
CREATE TABLE IF NOT EXISTS agent_memory_facts (
    id                  BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id             VARCHAR(191) NOT NULL,
    kind                VARCHAR(32)  NOT NULL,
    fact_key            VARCHAR(191) NOT NULL DEFAULT '',
    fact_value          TEXT NOT NULL,
    source_session_id   VARCHAR(64)  NOT NULL DEFAULT '',
    created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
                        ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uq_user_kind_key (user_id, kind, fact_key),
    KEY idx_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

---

## 6. 接入 `internal/agent/react/agent.go`、两个 handler、新增会话管理接口

**`internal/agent/react/agent.go`**：`Config` 新增 `MessageRewriter react.MessageModifier` 字段；`New()` 里若非 nil 则赋给 `agentCfg.MessageRewriter`（与现有 `SystemPrompt` → `MessageModifier` 的写法对称）。这个包本身继续保持"无 I/O，纯 eino 包装"的特性，不直接接触 Redis/MySQL。

**`internal/gateway/handler/agent_deps.go`**：`AgentDeps` 新增 `ShortTerm shortterm.Store`、`LongTerm longterm.Store`、`Sessions session.Store`、`Compaction memory.CompactionConfig` 字段；`newAgent` 把 `MessageRewriter: memory.NewMessageRewriter(d.Compaction)` 传入 `react.Config`。

**`chat.go`**：
- `chatResponse` 新增 `SessionID string \`json:"session_id"\`` 字段。
- Handler 逻辑：`sessionID := session.Resolve(req.SessionID)` → 若是新生成的 ID，调用 `d.Sessions.Create(...)` 登记元数据（含用截断用户消息生成的默认标题）；若是续接，调用 `d.Sessions.Touch(...)` 更新活跃时间 → `d.ShortTerm.LoadHistory(...)`（出错则 500 fail-closed，不静默退化为无历史）→ 拼上新 user 消息调用 `Generate` → 用 `memory.Compact` 处理"历史+新一轮"后 `ReplaceHistory` 落盘 → 响应里带上 `session_id`。

**`chat_stream.go`**：
- 新增 query 参数 `session_id`，同样走 `session.Resolve` + `Sessions.Create`/`Touch`。
- 流式响应新增一个首帧 `event: session\ndata: {"session_id":"..."}\n\n`，在第一条 `event: message` 之前发出。
- `stream.Recv()` 循环结束后，把累积的完整 assistant 回复和用户消息一起落盘（同样走 `memory.Compact` + `ReplaceHistory`）。

**新增会话管理 HTTP 接口**（`internal/gateway/handler/session.go`，新文件，路由注册在 `internal/gateway/router.go`）：
- `GET /ai-agent/v1/sessions?include_archived=false` → `d.Sessions.List(ctx, tenantCode, userID, includeArchived)`，返回会话列表（标题、置顶、归档、时间）。
- `PATCH /ai-agent/v1/sessions/{id}` → body 支持 `{"title": "..."}` 和/或 `{"pinned": true}`/`{"archived": true}`，分别调用 `Rename`/`SetPinned`/`SetArchived`。
- `DELETE /ai-agent/v1/sessions/{id}` → 调用 `d.Sessions.Delete(...)` 删元数据，同时调用 `d.ShortTerm.Reset(...)` 清掉可能还没过期的 Redis 历史。
- 全部走 `identity.FromContext(ctx)` 拿 `tenant_code`/`user_id` 做路由和归属校验（`user_id` 与 session 的 `UserID` 不匹配时返回 404 而非 403，避免暴露"这个 session 存在但不是你的"）。

**`pkg/schema/message.go`**（新增，解耦 DTO）：定义精简版 `Message`（`Role, Content, ToolCalls, ToolCallID, ToolName, Extra` 等，JSON tag 好），加 `ToEinoMessage`/`FromEinoMessage`（及切片版本）转换函数——避免 `internal/memory` 直接依赖 `cloudwego/eino/schema`，符合 PROJECT.md §6.1"`pkg/schema` 是唯一允许被所有层依赖的公共类型包"的要求。

**`cmd/server/main.go`**：组装 `tenantdb.NewRegistry(...)`（最先建，session/longterm 都依赖它）、`shortterm.NewRedisStore(...)`、`session.NewMySQLStore(registry)`、`longterm.NewMySQLStore(registry)`，注入 `AgentDeps`；新增环境变量读取：`REDIS_ADDR`、`SSO_SERVICE_BASE_URL`、`SSO_INTERNAL_TOKEN`、`SHORTTERM_SESSION_TTL_SECONDS`、`TENANTDB_INFO_CACHE_TTL_SECONDS` 等。

---

## 文件清单

| 文件 | 职责 |
|---|---|
| `pkg/schema/message.go` | 解耦的 Message DTO + 与 eino/schema.Message 互转 |
| `internal/tenantdb/ssoclient.go` | SSO db-info 查询客户端 |
| `internal/tenantdb/registry.go` | 租户 DB 连接池注册表（含缓存） |
| `internal/tenantdb/migrate.go` | 极简 SQL 迁移执行器 |
| `internal/session/session.go` | Session ID 生成/续接 |
| `internal/session/store.go` | MySQL 会话元数据 Store 接口 + 实现（标题/置顶/归档/列表） |
| `internal/memory/shortterm/store.go` | Redis 短期记忆 Store 接口 + 实现 |
| `internal/memory/compaction.go` | CompactionConfig、NewMessageRewriter、Compact |
| `internal/memory/longterm/store.go` | MySQL 长期记忆 Store 接口 + 实现 |
| `migrations/0001_create_agent_memory.sql` | `agent_sessions` + `agent_memory_facts` 建表 DDL |
| `internal/agent/react/agent.go` | 改动：新增 `MessageRewriter` 字段 |
| `internal/gateway/handler/agent_deps.go` | 改动：注入 ShortTerm/LongTerm/Sessions/Compaction |
| `internal/gateway/handler/chat.go` | 改动：history 读写、session 登记、session_id 请求/响应 |
| `internal/gateway/handler/chat_stream.go` | 改动：history 读写、session 登记、session_id 参数/SSE 首帧 |
| `internal/gateway/handler/session.go` | 新增：会话列表/重命名/置顶/归档/删除 HTTP 接口 |
| `internal/gateway/router.go` | 改动：注册新的 `/ai-agent/v1/sessions...` 路由 |
| `cmd/server/main.go` | 改动：组装依赖（含 tenantdb.Registry 最先建）、读取新环境变量 |
| `go.mod` | 新增直接依赖：`redis/go-redis/v9`、`go-sql-driver/mysql`；`google/uuid` 提升为直接依赖 |

## 明确排除范围（本次不做）

- `compose.CheckPointStore` / HITL 审批网关 / Interrupt-Resume（阶段三工作项，与对话记忆是相关但不同的概念，本方案不实现该接口）。
- 向量召回 / RAG / 任何向量库（PROJECT.md 明确推迟到阶段四）。
- 按租户的记忆读写配额/限流。
- 通用化的、服务所有未来功能的租户 DB 连接池框架（`internal/tenantdb.Registry` 只做到 session 元数据 + 长期记忆刚好需要的程度）。
- AI 自动生成会话标题（本次标题只是首轮消息截断，"用模型生成更好的标题"是自然的后续增量优化）。
- 自动启发式提取用户偏好（"remember"工具本身的实现，只设计接口不实现工具）。
- OTel trace span 打 `session_id` 标签（`internal/observability` 还不存在，是阶段二工作项）。
- 异步/延迟摘要（等 `cmd/worker/` + MQ 就绪后再做）。
- 会话列表的分页/搜索（本次 `List` 返回该用户全部会话，数据量小的 MVP 阶段够用；分页是后续按需加）。

## 验证方式

1. `go build ./...` 和 `go vet ./...` 全绿。
2. 启动本地 Redis（`docker run -p 6379:6379 redis`），用现有的 mock OpenAI 兼容 server（此前验证 chat/stream 用过的那套）跑通：同一个 `session_id` 连续两次 `POST /ai-agent/v1/chat`，第二次请求应能体现出第一次对话的上下文（例如先说"我叫小明"，第二次问"我叫什么"应正确回答）。
3. 构造超过 `SummarizeThresholdTurns` 轮的对话，验证历史被压缩为摘要+尾部窗口，且 Redis 里存储的历史体积不再无界增长。
4. 长期记忆和 session 元数据部分：若本地没有可用的 sso-service + 租户 MySQL，用最小 mock HTTP server 模拟 `db-info` 响应，验证 `Registry.Get` 能正确拿到连接、`session.Store`/`longterm.Store` 的增删改查都正常；若无法在当前环境跑通 MySQL 集成测试，需在完成后明确告知用户"长期记忆/会话元数据的 MySQL 集成部分未做真实数据库验证，建议在有 MySQL 环境时补跑一次"。
5. `chat_stream.go` 用 `curl -N` 验证 SSE 首帧正确带上 `session_id`，且流结束后 Redis 历史包含完整助手回复。
6. 验证会话列表端到端：连续用两个不同 `session_id` 各发一轮消息 → `GET /ai-agent/v1/sessions` 应返回两条记录（含默认标题）→ `PATCH` 重命名/置顶其中一条 → 再次 `GET` 验证排序和字段更新生效 → `DELETE` 一条后确认列表和 Redis 历史都清除。
7. 验证跨 TTL 持久语义：手动将某 session 的 Redis 历史 key 删除（模拟 TTL 过期），确认 `GET /ai-agent/v1/sessions` 仍能看到该会话记录，且续接该 session 发消息时 `LoadHistory` 返回空历史（等效新对话）而非报错。
