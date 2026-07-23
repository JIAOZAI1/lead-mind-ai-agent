# 技术债记录

> 记录当前项目里已知但暂不修复的问题：明确知道有缺陷/风险，出于优先级、成本或需要更多信息等原因先搁置。与 [PROJECT.md](PROJECT.md) §7 决策记录的区别：决策记录是"做出了什么决定、为什么"，本文档是"留了什么坑、什么条件下该填"。
>
> 每条记录需要包含：现象、根因、触发条件、影响范围、可能的修复方向、状态。问题被修复后从本文档移除，修复记录改为写入 PROJECT.md §7 决策记录。

---

## 1. 旧会话超过 Redis TTL 后继续对话，模型看不到之前的历史

**状态**：未修复，2026-07-24 记录

**现象**：用户点开一个较早的历史会话，前端能看到完整的历史消息列表（这部分走 `internal/memory/transcript`，MySQL 持久化，不过期）；但如果距离上次活跃已经超过短期记忆 TTL（`SHORTTERM_SESSION_TTL_SECONDS`，默认 6 小时），此时在这个会话里继续发消息，模型的回复会表现得像完全不知道之前聊过什么——UI 上看得到历史，模型却"失忆"，体验上是错位的。

**根因**：`chat.go`/`chat_stream.go` 里喂给 Agent 的对话上下文，只来自 `d.ShortTerm.LoadHistory(ctx, tenantCode, sessionID)`（[internal/memory/shortterm/store.go](internal/memory/shortterm/store.go)），这是 Redis 实现，key 有 TTL。TTL 到期后 Redis key 被淘汰，`LoadHistory` 在 `redis.Nil` 时返回空切片而不是错误（[store.go:59-61](internal/memory/shortterm/store.go#L59-L61)），这个设计本身是对的（新会话的第一次调用也会走这个分支，不应该报错）——但代码目前无法区分"这是一个从未有过历史的全新会话"和"这是一个有历史、只是 Redis 副本过期了的旧会话"，导致两种情况被一视同仁地当成"空历史"处理。

**触发条件**：`session_id` 非空（不是新会话）且距离上次活跃时间超过短期记忆 TTL。

**影响范围**：仅影响"继续旧会话对话"这个操作的模型上下文质量，不影响：
- 历史消息的可查看性（transcript 读取路径不受影响）
- 会话元数据（标题/置顶/归档，MySQL `agent_sessions`，不过期）
- 新会话或 TTL 内的连续对话

**可能的修复方向**（未实施，待评估）：
在 `chat.go`/`chat_stream.go` 里，当 `!isNewSession` 且 `ShortTerm.LoadHistory` 返回空历史时，判定为"TTL 过期而非全新会话"，回退去查 `internal/memory/transcript.ListTurns` 重建上下文。需要注意：
- transcript 里存的是未压缩的原始消息，可能条数很多，直接整段喂给模型会打爆上下文窗口，需要过一遍 `internal/memory/compaction.go` 的 `Compact` 再喂给模型，或者只取最近 N 轮。
- 重建出的上下文喂给模型之后，是否要顺手把它写回 `ShortTerm.ReplaceHistory`（相当于用这次机会"续热" Redis 缓存），需要一并设计。
- 判空条件本身要小心：`!isNewSession && len(history) == 0` 目前无法区分"真的是空历史的旧会话"（理论上不该出现，因为凡是 `Sessions.Create` 过的会话第一轮之后必然写入过 Redis）和"TTL 过期"，可以认为两者在这条分支下处理方式一致（都去读 transcript 兜底），所以不需要额外状态位区分。

---
