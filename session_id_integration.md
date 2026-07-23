# 前后端联调方案：会话（session_id）传递

## 背景

后端 `/ai-agent/v1/chat` 和 `/ai-agent/v1/chat/stream` 都依赖调用方传回上一轮拿到的
`session_id`，才能在 Redis 里找到同一会话的历史（`internal/memory/shortterm`），
再拼进本轮请求发给模型。如果前端每次请求都不带 `session_id`（或者带错、带丢），
后端会把它当成**全新会话**处理，历史永远是空的，模型自然"不记得上一轮说了什么"——
但这不是后端没写记忆，而是前端从未告诉后端"这是同一个会话"。

需要跟前端确认：当前请求里到底有没有把上一轮响应返回的 `session_id` 存起来，并在下一轮请求里带上。

## 接口契约

### 1. `POST /ai-agent/v1/chat`（非流式）

请求体：
```json
{
  "session_id": "",       // 第一轮留空或不传；后续轮次必须回传上一轮响应里的 session_id
  "message": "用户输入"
}
```

响应体：
```json
{
  "tenant_code": "...",
  "session_id": "abc-123",  // 后端生成或沿用的会话 ID，前端必须存下来
  "reply": "模型回复"
}
```

**前端需要做的事**：把响应里的 `session_id` 存到本地状态（内存/localStorage/sessionStorage，
按业务是否要跨页面刷新保留而定），下一次调用同一个对话时，把它塞进请求体的 `session_id` 字段。

### 2. `GET /ai-agent/v1/chat/stream?message=...&session_id=...`（SSE 流式）

`session_id` 是 **query 参数**，不是 body 字段（因为 SSE 用 GET）。

响应是一系列 SSE 事件，第一个事件固定是：
```
event: session
data: {"session_id":"abc-123"}
```
后面才是：
```
event: message
data: {"tenant_code":"...","delta":"..."}

event: message
data: {...}

event: done
data: {}
```

**前端需要做的事**：
- 监听 `event: session`，从中取出 `session_id` 并存下来（这是本次请求"真正生效"的会话 ID，
  不要自己在前端生成一个 UUID 就当作 session_id 用——必须以后端返回的为准）。
- 下一轮发起 `chat/stream` 请求时，把这个 `session_id` 拼进 query string。
- 如果前端在同一个会话里维护了自己的临时 ID 用于 UI key（比如消息列表的 React key），
  注意不要把这个 UI 层 ID 和后端的 `session_id` 弄混、也不要传错的那个过去。

### 3. 会话是每个租户+用户独立的

`session_id` 本身不校验归属（`internal/gateway/handler/chat.go` 不做 ownership 检查，
这是设计如此——chat 接口信任调用方传入的 ID 就是自己这一轮该续接的会话）。真正的租户/用户
身份来自网关注入的 header：

| Header | 说明 |
|---|---|
| `X-Tenant-Code` | 必须，缺失直接 400 |
| `X-User-Id` | 建议带，用于 session 列表归属 |
| `X-Username` | 可选 |
| `X-User-Roles` | 可选 |

这些 header 由前端所在的网关层/BFF 注入，不是前端 JS 直接拼——需要确认前端调用链路上，
这层 identity 注入没有被绕过（比如本地联调直接绕开网关直连后端，会缺 `X-Tenant-Code` 导致
每个请求都 400，或者更隐蔽地——如果本地随手 mock 了固定的 `X-User-Id` 但每次 `X-Tenant-Code`
不一致，也会导致 Redis key `tenant:{tenant_code}:session:{id}:history` 对不上，历史读不到）。

## 排查清单（建议按顺序过一遍）

1. **前端是否保存了 `session_id`？** 打开浏览器 Network 面板，看第一轮 `chat`/`chat/stream`
   响应里的 `session_id`，和第二轮请求发出去的 `session_id`（或 query 参数）是否一致。
   如果第二轮请求里 `session_id` 是空的，或者和第一轮返回的不一样 —— 就是这里的问题。
2. **是不是每次都传了空字符串 `""`？** 空字符串会被 `session.Resolve` 当成"新建会话"
   （[session.go:20-25](internal/session/session.go#L20-L25)），而不是报错，所以现象是
   "看起来正常但记不住"，不会有明显报错，容易被忽略。
3. **流式接口是否正确监听了 `event: session`？** 如果前端的 SSE 解析逻辑只处理了
   `event: message`/`event: done`，没处理第一个 `event: session` 帧，就永远拿不到
   本轮的 session_id。
4. **是否用了浏览器自己生成的 ID 而不是后端返回的？** 如果前端图省事，自己在本地生成一个
   UUID 当 session_id 传给后端，第一轮请求它会被当成"客户端指定的已存在 session"
   （`isNewSession = false`），直接走 `Touch` 而不是 `Create`，如果这个 ID 从来没在
   `Create` 里注册过，`Sessions.Touch` 可能報 not found（取决于 store 实现），或者
   `ShortTerm.LoadHistory` 读到的是全新 TTL 过期后的空历史——这两种都会表现为"记不住"。
5. **多标签页/多组件是否共享同一个 session_id 状态？** 如果 session_id 存在某个组件的
   本地 state 里，页面刷新或者跳转到另一个聊天组件实例后没有从持久化层（如 URL 参数、
   localStorage）恢复，也会导致"看起来是同一个对话，实际上 session_id 已经变了"。

## 建议的前端最小改动

- 用一个稳定的地方（比如聊天页面的 URL query `?session_id=xxx`，或者 Redux/Zustand 之类
  的全局状态）持有当前会话的 `session_id`，从后端首次响应/`event: session` 帧写入，
  此后每次请求都从这里读出来传回。
- 新建会话（用户点"新对话"）时，显式把本地 `session_id` 清空/传空字符串，而不是复用上一个。
- 如果前端有重试逻辑，确认重试时用的还是同一个 `session_id`，不要在重试时误生成新的。
